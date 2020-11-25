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

	"github.com/cockroachdb/cockroach/pkg/ccl/storageccl"
	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfra"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfrapb"
	"github.com/cockroachdb/cockroach/pkg/sql/rowenc"
	"github.com/cockroachdb/cockroach/pkg/sql/rowexec"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/protoutil"
	"github.com/cockroachdb/errors"
	gogotypes "github.com/gogo/protobuf/types"
)

// Progress is streamed to the coordinator through metadata.
var restoreDataOutputTypes = []*types.T{}

type restoreDataProcessor struct {
	execinfra.ProcessorBase

	flowCtx *execinfra.FlowCtx
	spec    execinfrapb.RestoreDataSpec
	input   execinfra.RowSource
	output  execinfra.RowReceiver

	alloc rowenc.DatumAlloc
	kr    *storageccl.KeyRewriter

	progCh     chan execinfrapb.RemoteProducerMetadata_BulkProcessorProgress
	restoreErr error
}

var _ execinfra.Processor = &restoreDataProcessor{}
var _ execinfra.RowSource = &restoreDataProcessor{}

const restoreDataProcName = "restoreDataProcessor"

func newRestoreDataProcessor(
	flowCtx *execinfra.FlowCtx,
	processorID int32,
	spec execinfrapb.RestoreDataSpec,
	post *execinfrapb.PostProcessSpec,
	input execinfra.RowSource,
	output execinfra.RowReceiver,
) (execinfra.Processor, error) {
	rd := &restoreDataProcessor{
		flowCtx: flowCtx,
		input:   input,
		spec:    spec,
		output:  output,
		progCh:  make(chan execinfrapb.RemoteProducerMetadata_BulkProcessorProgress),
	}

	var err error
	rd.kr, err = storageccl.MakeKeyRewriterFromRekeys(rd.spec.Rekeys)
	if err != nil {
		return nil, err
	}

	if err := rd.Init(rd, post, restoreDataOutputTypes, flowCtx, processorID, output, nil, /* memMonitor */
		execinfra.ProcStateOpts{
			InputsToDrain: []execinfra.RowSource{input},
		}); err != nil {
		return nil, err
	}
	return rd, nil
}

// Start is part of the RowSource interface.
func (rd *restoreDataProcessor) Start(ctx context.Context) context.Context {
	rd.input.Start(ctx)
	go func() {
		rd.restoreErr = rd.runRestore()
	}()
	return rd.StartInternal(ctx, restoreDataProcName)
}

func (rd *restoreDataProcessor) runRestore() error {
	// We read rows from the SplitAndScatter processor. We expect each row to
	// contain 2 columns. The first is used to route the row to this processor,
	// and the second contains the RestoreSpanEntry that we're interested in.
	for {
		row, meta := rd.input.Next()
		// Do we expect to get metadata?
		// If we do we should send it over a channel or something.
		if meta != nil {
			// TODO: Considering erroring if the meta is nil, because we don't expect that.
			return meta.Err
		}
		if row == nil {
			// Done consuming.
			return nil
		}

		if len(row) != 2 {
			return errors.New("expected input rows to have exactly 2 columns")
		}
		if err := row[1].EnsureDecoded(types.Bytes, &rd.alloc); err != nil {
			return err
		}
		datum := row[1].Datum
		entryDatumBytes, ok := datum.(*tree.DBytes)
		if !ok {
			return errors.AssertionFailedf(`unexpected datum type %T: %+v`, datum, row)
		}

		var entry execinfrapb.RestoreSpanEntry
		if err := protoutil.Unmarshal([]byte(*entryDatumBytes), &entry); err != nil {
			return errors.Wrap(err, "un-marshaling restore span entry")
		}

		newSpanKey, err := rewriteBackupSpanKey(rd.kr, entry.Span.Key)
		if err != nil {
			return errors.Wrap(err, "re-writing span key to import")
		}

		log.VEventf(rd.Ctx, 1 /* level */, "importing span %v", entry.Span)
		importRequest := &roachpb.ImportRequest{
			// Import is a point request because we don't want DistSender to split
			// it. Assume (but don't require) the entire post-rewrite span is on the
			// same range.
			RequestHeader: roachpb.RequestHeader{Key: newSpanKey},
			DataSpan:      entry.Span,
			Files:         entry.Files,
			EndTime:       rd.spec.RestoreTime,
			Rekeys:        rd.spec.Rekeys,
			Encryption:    rd.spec.Encryption,
		}

		importRes, pErr := kv.SendWrapped(rd.Ctx, rd.flowCtx.Cfg.DB.NonTransactionalSender(), importRequest)
		if pErr != nil {
			return errors.Wrapf(pErr.GoError(), "importing span %v", importRequest.DataSpan)
		}

		var prog execinfrapb.RemoteProducerMetadata_BulkProcessorProgress
		progDetails := RestoreProgress{}
		progDetails.Summary = countRows(importRes.(*roachpb.ImportResponse).Imported, rd.spec.PKIDs)
		progDetails.ProgressIdx = entry.ProgressIdx
		progDetails.DataSpan = entry.Span
		details, err := gogotypes.MarshalAny(&progDetails)
		if err != nil {
			return err
		}
		prog.ProgressDetails = *details
	}
}

// Next is part of the RowSource interface.
func (rd *restoreDataProcessor) Next() (rowenc.EncDatumRow, *execinfrapb.ProducerMetadata) {
	if rd.State != execinfra.StateRunning {
		return nil, rd.DrainHelper()
	}

	prog, ok := <-rd.progCh
	if ok {
		return nil, &execinfrapb.ProducerMetadata{BulkProcessorProgress: &prog}
	}

	// Done consuming the channel, check for errors.
	if rd.restoreErr != nil {
		rd.MoveToDraining(rd.restoreErr)
		return nil, rd.DrainHelper()
	}

	rd.MoveToDraining(nil /* err */)
	return nil, rd.DrainHelper()
}

// ConsumerClosed is part of the RowSource interface.
func (rd *restoreDataProcessor) ConsumerClosed() {
	rd.InternalClose()
}

func init() {
	rowexec.NewRestoreDataProcessor = newRestoreDataProcessor
}
