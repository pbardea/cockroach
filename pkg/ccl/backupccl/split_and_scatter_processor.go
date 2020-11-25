// Copyright 2020 The Cockroach Authors.
//
// Licensed as a CockroachDB Enterprise file under the Cockroach Community
// License (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
//     https://github.com/cockroachdb/cockroach/blob/master/licenses/CCL.txt

package backupccl

import (
	"context"
	"fmt"

	"github.com/cockroachdb/cockroach/pkg/ccl/storageccl"
	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfra"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfrapb"
	"github.com/cockroachdb/cockroach/pkg/sql/rowenc"
	"github.com/cockroachdb/cockroach/pkg/sql/rowexec"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/ctxgroup"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/protoutil"
)

type splitAndScatterer interface {
	// splitAndScatterSpan issues a split request at a given key and then scatters
	// the range around the cluster. It returns the node ID of the leaseholder of
	// the span after the scatter.
	splitAndScatterKey(ctx context.Context, db *kv.DB, kr *storageccl.KeyRewriter, key roachpb.Key, randomizeLeases bool) (roachpb.NodeID, error)
}

const splitAndScatterProcessorName = "splitAndScatterProcessor"

var splitAndScatterOutputTypes = []*types.T{
	types.Bytes, // Span key for the range router
	types.Bytes, // RestoreDataEntry bytes
}

// splitAndScatterProcessor is given a set of spans (specified as
// RestoreSpanEntry's) to distribute across the cluster. Depending on which node
// the span ends up on, it forwards RestoreSpanEntry as bytes along with the key
// of the span on a row. It expects an output RangeRouter and before it emits
// each row, it updates the entry in the RangeRouter's map with the destination
// of the scatter.
type splitAndScatterProcessor struct {
	execinfra.ProcessorBase

	flowCtx *execinfra.FlowCtx
	spec    execinfrapb.SplitAndScatterSpec
	output  execinfra.RowReceiver

	scatterer            splitAndScatterer
	doneScatterCh        chan entryNode
	splitAndScatterError error

	// A cache for routing datums, so only 1 is allocated per node.
	routingDatumCache map[roachpb.NodeID]rowenc.EncDatum
}

var _ execinfra.Processor = &splitAndScatterProcessor{}
var _ execinfra.RowSource = &splitAndScatterProcessor{}

func newSplitAndScatterProcessor(
	flowCtx *execinfra.FlowCtx,
	processorID int32,
	spec execinfrapb.SplitAndScatterSpec,
	post *execinfrapb.PostProcessSpec,
	output execinfra.RowReceiver,
) (execinfra.Processor, error) {
	ssp := &splitAndScatterProcessor{
		flowCtx:           flowCtx,
		spec:              spec,
		output:            output,
		scatterer:         dbSplitAndScatterer{},
		doneScatterCh:     make(chan entryNode),
		routingDatumCache: make(map[roachpb.NodeID]rowenc.EncDatum),
	}
	if err := ssp.Init(ssp, post, splitAndScatterOutputTypes, flowCtx, processorID, output, nil, /* memMonitor */
		execinfra.ProcStateOpts{
			// This processor has no inputs to drain.
			InputsToDrain: nil,
		}); err != nil {
		return nil, err
	}
	return ssp, nil
}

type entryNode struct {
	entry execinfrapb.RestoreSpanEntry
	node  roachpb.NodeID
}

// Start is part of the RowSource interface.
func (ssp *splitAndScatterProcessor) Start(ctx context.Context) context.Context {
	// TODO: Are we leaking goroutines?
	go func() {
		defer close(ssp.doneScatterCh)
		err := runSplitAndScatter(ctx, ssp.flowCtx, &ssp.spec,
			ssp.scatterer, ssp.doneScatterCh)
		if err != nil {
			log.Errorf(ctx, "error while running split and scatter: %+v", err)
		}
		ssp.splitAndScatterError = err
	}()
	return ssp.StartInternal(ctx, splitAndScatterProcessorName)
}

// Next is part of the RowSource interface
func (ssp *splitAndScatterProcessor) Next() (rowenc.EncDatumRow, *execinfrapb.ProducerMetadata) {
	if ssp.State != execinfra.StateRunning {
		return nil, ssp.DrainHelper()
	}

	for scatteredEntry := range ssp.doneScatterCh {
		entry := scatteredEntry.entry
		entryBytes, err := protoutil.Marshal(&entry)
		if err != nil {
			ssp.MoveToDraining(err)
			return nil, ssp.DrainHelper()
		}

		// The routing datums informs the router which output stream should be used.
		routingDatum, ok := ssp.routingDatumCache[scatteredEntry.node]
		if !ok {
			routingDatum, _ = routingDatumsForNode(scatteredEntry.node)
			ssp.routingDatumCache[scatteredEntry.node] = routingDatum
		}

		row := rowenc.EncDatumRow{
			routingDatum,
			rowenc.DatumToEncDatum(types.Bytes, tree.NewDBytes(tree.DBytes(entryBytes))),
		}

		return row, nil
	}

	if ssp.splitAndScatterError != nil {
		ssp.MoveToDraining(ssp.splitAndScatterError)
		return nil, ssp.DrainHelper()
	}

	ssp.MoveToDraining(nil /* err */)
	return nil, ssp.DrainHelper()
}

// ConsumerDone is part of the RowSource interface.
func (ssp *splitAndScatterProcessor) ConsumerDone() {
	ssp.MoveToDraining(nil /* err */)
}

// ConsumerClosed is part of the RowSource interface.
func (ssp *splitAndScatterProcessor) ConsumerClosed() {
	// The consumer is done, Next() will not be called again.
	ssp.InternalClose()
}

func runSplitAndScatter(
	ctx context.Context,
	flowCtx *execinfra.FlowCtx,
	spec *execinfrapb.SplitAndScatterSpec,
	scatterer splitAndScatterer,
	doneScatterCh chan entryNode,
) error {
	g := ctxgroup.WithContext(ctx)
	db := flowCtx.Cfg.DB
	kr, err := storageccl.MakeKeyRewriterFromRekeys(spec.Rekeys)
	if err != nil {
		return err
	}

	importSpanChunksCh := make(chan []execinfrapb.RestoreSpanEntry)
	g.GoCtx(func(ctx context.Context) error {
		defer close(importSpanChunksCh)
		for _, importSpanChunk := range spec.Chunks {
			_, err := scatterer.splitAndScatterKey(ctx, db, kr, importSpanChunk.Entries[0].Span.Key, true /* randomizeLeases */)
			if err != nil {
				return err
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case importSpanChunksCh <- importSpanChunk.Entries:
			}
		}
		return nil
	})

	// TODO(pbardea): This tries to cover for a bad scatter by having 2 * the
	// number of nodes in the cluster. Is it necessary?
	splitScatterWorkers := 2
	for worker := 0; worker < splitScatterWorkers; worker++ {
		g.GoCtx(func(ctx context.Context) error {
			for importSpanChunk := range importSpanChunksCh {
				log.Infof(ctx, "processing a chunk")
				for _, importSpan := range importSpanChunk {
					log.Infof(ctx, "processing a span [%s,%s)", importSpan.Span.Key, importSpan.Span.EndKey)
					destination, err := scatterer.splitAndScatterKey(ctx, db, kr, importSpan.Span.Key, false /* randomizeLeases */)
					if err != nil {
						return err
					}

					scatteredEntry := entryNode{
						entry: importSpan,
						node:  destination,
					}

					select {
					case <-ctx.Done():
						return ctx.Err()
					case doneScatterCh <- scatteredEntry:
					}
				}
			}
			return nil
		})
	}

	return g.Wait()
}

func routingDatumsForNode(nodeID roachpb.NodeID) (rowenc.EncDatum, rowenc.EncDatum) {
	routingBytes := roachpb.Key(fmt.Sprintf("node%d", nodeID))
	startDatum := rowenc.DatumToEncDatum(types.Bytes, tree.NewDBytes(tree.DBytes(routingBytes)))
	endDatum := rowenc.DatumToEncDatum(types.Bytes, tree.NewDBytes(tree.DBytes(routingBytes.Next())))
	return startDatum, endDatum
}

// routingSpanForNode provides the mapping to be used during distsql planning
// when setting up the output router.
func routingSpanForNode(nodeID roachpb.NodeID) ([]byte, []byte, error) {
	var alloc rowenc.DatumAlloc
	startDatum, endDatum := routingDatumsForNode(nodeID)

	startBytes, endBytes := make([]byte, 0), make([]byte, 0)
	startBytes, err := startDatum.Encode(splitAndScatterOutputTypes[0], &alloc, descpb.DatumEncoding_ASCENDING_KEY, startBytes)
	if err != nil {
		return nil, nil, err
	}
	endBytes, err = endDatum.Encode(splitAndScatterOutputTypes[0], &alloc, descpb.DatumEncoding_ASCENDING_KEY, endBytes)
	if err != nil {
		return nil, nil, err
	}
	return startBytes, endBytes, nil
}

func init() {
	rowexec.NewSplitAndScatterProcessor = newSplitAndScatterProcessor
}
