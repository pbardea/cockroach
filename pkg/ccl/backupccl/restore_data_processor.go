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
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/ctxgroup"
	"github.com/cockroachdb/cockroach/pkg/util/protoutil"
	"github.com/cockroachdb/errors"
	gogotypes "github.com/gogo/protobuf/types"
)

type restoreDataProcessor struct {
	flowCtx *execinfra.FlowCtx
	input execinfra.RowSource
	spec execinfrapb.RestoreDataSpec
	output  execinfra.RowReceiver
}

var _ execinfra.Processor = &restoreDataProcessor{}

var outputTypes = []*types.T{}

func newRestoreDataProcessor(
	flowCtx *execinfra.FlowCtx,
	processorID int32,
	input execinfra.RowSource,
	spec execinfrapb.RestoreDataSpec,
	output execinfra.RowReceiver,
) (*restoreDataProcessor, error) {
	rdp := &restoreDataProcessor{
		flowCtx: flowCtx,
		input: input,
		spec: spec,
		output: output,
	}
	return rdp, nil
}

func (rdp *restoreDataProcessor) Run(ctx context.Context) {
	g := ctxgroup.WithContext(ctx)

	kr, err := storageccl.MakeKeyRewriterFromRekeys(rdp.spec.Rekeys)
	if err != nil {
		// Send an error on the channel.
	}

	progCh := make(chan execinfrapb.RemoteProducerMetadata_BulkProcessorProgress)

	// This channel is expected to block. When blocked this processor will not
	// call on its input.
	const presplitLeadLimit = 10
	rowsToImport := make(chan execinfrapb.RestoreSpanEntry, presplitLeadLimit)

	go func() {
		// We don't have to worry about this go routine leaking because next we loop
		// over progCh which is closed only after the go routine returns.
		defer close(progCh)

		for ;; {
			row, meta := rdp.input.Next()
			if meta != nil {
				err := errors.New("unexpected metadata")
				if meta.Err != nil {
					err = meta.Err
				}
				rdp.output.Push(nil, &execinfrapb.ProducerMetadata{Err: err})
				return
			}

			if row == nil {
				// We're done adding rows to the channel. First close the channel to
				// signal that no more entries are coming, then wait for the workers to
				// finish and move to draining.
				close(rowsToImport)
				if err := g.Wait(); err != nil {
					rdp.output.Push(nil, &execinfrapb.ProducerMetadata{Err: err})
				}
				return
			}

			err = rdp.importRows(ctx, rdp.flowCtx.Cfg.DB, kr, rowsToImport, progCh)
		}
	}()

	for prog := range progCh {
		// Take a copy so that we can send the progress address to the output processor.
		p := prog
		rdp.output.Push(nil /* row */, &execinfrapb.ProducerMetadata{BulkProcessorProgress: &p})
	}

	for ;; {

		if err := rdp.processRow(row, rowsToImport); err != nil {
			rdp.output.Push(nil, &execinfrapb.ProducerMetadata{Err: err})
		}
	}
}

func (rdp *restoreDataProcessor) processRow(row sqlbase.EncDatumRow, rowsToImport chan execinfrapb.RestoreSpanEntry) error {
	// Actually process the row.
	var alloc sqlbase.DatumAlloc
	if len(row) < 2 {
		// Error
	}
	d := row[0]
	if err := d.EnsureDecoded(types.Bytes, &alloc); err != nil {
		// return err
	}
	raw, ok := d.Datum.(*tree.DBytes)
	if !ok {
		// return some error
	}
	var entry execinfrapb.RestoreSpanEntry
	if err := protoutil.Unmarshal([]byte(*raw), &entry); err != nil {
		//return errors.NewAssertionErrorWithWrappedErrf(err,
		//	`unmarshalling resolved span: %x`, raw)
	}
	rowsToImport <- entry
}

func (rdp *restoreDataProcessor) importRows(
	ctx context.Context,
	db *kv.DB,
	kr *storageccl.KeyRewriter,
	importEntries chan execinfrapb.RestoreSpanEntry,
	progCh chan execinfrapb.RemoteProducerMetadata_BulkProcessorProgress,
) error {
	for entry := range importEntries {
		newSpanKey, err := rewriteBackupSpanKey(kr, entry.Span.Key)
		if err != nil {
			return err
		}
		importRequest := &roachpb.ImportRequest{
			// Import is a point request because we don't want DistSender to split
			// it. Assume (but don't require) the entire post-rewrite span is on the
			// same range.
			RequestHeader: roachpb.RequestHeader{Key: newSpanKey},
			DataSpan:      entry.Span,
			Files:         entry.Files,
			EndTime:       rdp.spec.RestoreTime,
			Rekeys:        rdp.spec.Rekeys,
			Encryption:    rdp.spec.Encryption,
		}

		importRes, pErr := kv.SendWrapped(ctx, db.NonTransactionalSender(), importRequest)
		if pErr != nil {
			return errors.Wrapf(pErr.GoError(), "importing span %v", importRequest.DataSpan)
		}

		var progress execinfrapb.RemoteProducerMetadata_BulkProcessorProgress
		progDetails := RestoreProgress{
			ProgressIdx: entry.ProgressIdx,
			Key:         entry.Span.Key,
			RowCount:    countRows(importRes.(*roachpb.ImportResponse).Imported, rdp.spec.PKIDs),
		}
		details, err := gogotypes.MarshalAny(&progDetails)
		if err != nil {
			return err
		}
		progress.ProgressDetails = *details
		progCh <- progress
	}
	return nil
}
