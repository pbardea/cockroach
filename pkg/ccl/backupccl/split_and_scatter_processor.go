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
	"time"

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
	"github.com/cockroachdb/cockroach/pkg/util/tracing"
	"github.com/cockroachdb/errors"
)

type splitAndScatterer interface {
	// splitAndScatterSpan issues a split request at a given key and then scatters
	// the range around the cluster. It returns the node ID of the leaseholder of
	// the span after the scatter.
	splitAndScatterKey(ctx context.Context, db *kv.DB, kr *storageccl.KeyRewriter, key roachpb.Key, randomizeLeases bool) (roachpb.NodeID, error)
}


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
	flowCtx   *execinfra.FlowCtx
	spec      execinfrapb.SplitAndScatterSpec
	output    execinfra.RowReceiver
	scatterer splitAndScatterer
}

var _ execinfra.Processor = &splitAndScatterProcessor{}

// OutputTypes implements the execinfra.Processor interface.
func (ssp *splitAndScatterProcessor) OutputTypes() []*types.T {
	return splitAndScatterOutputTypes
}

func newSplitAndScatterProcessor(
	flowCtx *execinfra.FlowCtx,
	_ int32,
	spec execinfrapb.SplitAndScatterSpec,
	output execinfra.RowReceiver,
) (execinfra.Processor, error) {
	ssp := &splitAndScatterProcessor{
		flowCtx:   flowCtx,
		spec:      spec,
		output:    output,
		scatterer: dbSplitAndScatterer{},
	}
	return ssp, nil
}

type entryNode struct {
	entry execinfrapb.RestoreSpanEntry
	node  roachpb.NodeID
}

// Run implements the execinfra.Processor interface.
func (ssp *splitAndScatterProcessor) Run(ctx context.Context) {
	ctx, span := tracing.ChildSpan(ctx, "splitAndScatterProcessor")
	defer span.Finish()
	defer ssp.output.ProducerDone()

	numEntries := 0
	for _, chunk := range ssp.spec.Chunks {
		numEntries += len(chunk.Entries)
	}
	// Large enough so that it never blocks.
	doneScatterCh := make(chan entryNode, numEntries)

	// A cache for routing datums, so only 1 is allocated per node.
	routingDatumCache := make(map[roachpb.NodeID]rowenc.EncDatum)

	var err error
	splitAndScatterCtx, cancelSplitAndScatter := context.WithCancel(ctx)
	defer cancelSplitAndScatter()
	// Note that the loop over doneScatterCh should prevent this goroutine from
	// leaking when there are no errors. However, if that loop needs to exit
	// early, runSplitAndScatter's context will be canceled.
	go func() {
		defer close(doneScatterCh)
		err = runSplitAndScatter(splitAndScatterCtx, ssp.flowCtx, &ssp.spec, ssp.scatterer, doneScatterCh)
		if err != nil {
			log.Errorf(ctx, "error while running split and scatter: %+v", err)
		}
	}()

	for scatteredEntry := range doneScatterCh {
		entry := scatteredEntry.entry
		entryBytes, err := protoutil.Marshal(&entry)
		if err != nil {
			ssp.output.Push(nil, &execinfrapb.ProducerMetadata{Err: err})
			break
		}

		// The routing datums informs the router which output stream should be used.
		routingDatum, ok := routingDatumCache[scatteredEntry.node]
		if !ok {
			routingDatum, _ = routingDatumsForNode(scatteredEntry.node)
			routingDatumCache[scatteredEntry.node] = routingDatum
		}

		row := rowenc.EncDatumRow{
			routingDatum,
			rowenc.DatumToEncDatum(types.Bytes, tree.NewDBytes(tree.DBytes(entryBytes))),
		}
		ssp.output.Push(row, nil)
	}

	if err != nil {
		ssp.output.Push(nil, &execinfrapb.ProducerMetadata{Err: err})
		return
	}
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
