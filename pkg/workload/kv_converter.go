// Copyright 2019 The Cockroach Authors.
//
// Licensed as a CockroachDB Enterprise file under the Cockroach Community
// License (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
//     https://github.com/cockroachdb/cockroach/blob/master/licenses/CCL.txt

package workload

import (
	"context"
	"github.com/cockroachdb/apd/v2"
	"github.com/cockroachdb/cockroach/pkg/col/coldata"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/row"
	"github.com/cockroachdb/cockroach/pkg/sql/rowenc"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/bufalloc"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil/pgdate"
	"github.com/cockroachdb/errors"
	"sync/atomic"
	"unsafe"
)

// WorkloadKVConverter converts workload.BatchedTuples to []roachpb.KeyValues.
type WorkloadKVConverter struct {
	tableDesc      catalog.TableDescriptor
	rows           BatchedTuples
	batchIdxAtomic int64
	batchEnd       int
	kvCh           chan row.KVBatch

	// For progress reporting
	fileID                int32
	totalBatches          float32
	finishedBatchesAtomic int64
}

// NewWorkloadKVConverter returns a WorkloadKVConverter for the given table and
// range of batches, emitted converted kvs to the given channel.
func NewWorkloadKVConverter(
	fileID int32,
	tableDesc catalog.TableDescriptor,
	rows BatchedTuples,
	batchStart, batchEnd int,
	kvCh chan row.KVBatch,
) *WorkloadKVConverter {
	return &WorkloadKVConverter{
		tableDesc:      tableDesc,
		rows:           rows,
		batchIdxAtomic: int64(batchStart) - 1,
		batchEnd:       batchEnd,
		kvCh:           kvCh,
		totalBatches:   float32(batchEnd - batchStart),
		fileID:         fileID,
	}
}

// Worker can be called concurrently to create multiple workers to process
// batches in order. This keeps concurrently running workers ~adjacent batches
// at any given moment (as opposed to handing large ranges of batches to each
// worker, e.g. 0-999 to worker 1, 1000-1999 to worker 2, etc). This property is
// relevant when ordered workload batches produce ordered PK data, since the
// workers feed into a shared kvCH so then contiguous blocks of PK data will
// usually be buffered together and thus batched together in the SST builder,
// minimzing the amount of overlapping SSTs ingested.
//
// This worker needs its own EvalContext and DatumAlloc.
func (w *WorkloadKVConverter) Worker(ctx context.Context, evalCtx *tree.EvalContext) error {
	conv, err := row.NewDatumRowConverter(ctx, w.tableDesc, nil /* targetColNames */, evalCtx,
		w.kvCh, nil /* seqChunkProvider */)
	if err != nil {
		return err
	}
	conv.KvBatch.Source = w.fileID
	conv.FractionFn = func() float32 {
		return float32(atomic.LoadInt64(&w.finishedBatchesAtomic)) / w.totalBatches
	}
	var alloc rowenc.DatumAlloc
	var a bufalloc.ByteAllocator
	cb := coldata.NewMemBatchWithCapacity(nil /* typs */, 0 /* capacity */, coldata.StandardColumnFactory)

	for {
		batchIdx := int(atomic.AddInt64(&w.batchIdxAtomic, 1))
		if batchIdx >= w.batchEnd {
			break
		}
		a = a[:0]
		w.rows.FillBatch(batchIdx, cb, &a)
		for rowIdx, numRows := 0, cb.Length(); rowIdx < numRows; rowIdx++ {
			for colIdx, col := range cb.ColVecs() {
				// TODO(dan): This does a type switch once per-datum. Reduce this to
				// a one-time switch per column.
				converted, err := makeDatumFromColOffset(
					&alloc, conv.VisibleColTypes[colIdx], evalCtx, col, rowIdx)
				if err != nil {
					return err
				}
				conv.Datums[colIdx] = converted
			}
			// `conv.Row` uses these as arguments to GenerateUniqueID to generate
			// hidden primary keys, when necessary. We want them to be ascending per
			// batch (to reduce overlap in the resulting kvs) and non-conflicting
			// (because of primary key uniqueness). The ids that come out of
			// GenerateUniqueID are sorted by (fileIdx, timestamp) and unique as long
			// as the two inputs are a unique combo, so using the index of the batch
			// within the table and the index of the row within the batch should do
			// what we want.
			fileIdx, timestamp := int32(batchIdx), int64(rowIdx)
			if err := conv.Row(ctx, fileIdx, timestamp); err != nil {
				return err
			}
		}
		atomic.AddInt64(&w.finishedBatchesAtomic, 1)
	}
	return conv.SendBatch(ctx)
}

// makeDatumFromColOffset tries to fast-path a few workload-generated types into
// directly datums, to dodge making a string and then the parsing it.
func makeDatumFromColOffset(
	alloc *rowenc.DatumAlloc, hint *types.T, evalCtx *tree.EvalContext, col coldata.Vec, rowIdx int,
) (tree.Datum, error) {
	if col.Nulls().NullAt(rowIdx) {
		return tree.DNull, nil
	}
	switch t := col.Type(); col.CanonicalTypeFamily() {
	case types.BoolFamily:
		return tree.MakeDBool(tree.DBool(col.Bool()[rowIdx])), nil
	case types.IntFamily:
		switch t.Width() {
		case 0, 64:
			switch hint.Family() {
			case types.IntFamily:
				return alloc.NewDInt(tree.DInt(col.Int64()[rowIdx])), nil
			case types.DecimalFamily:
				d := *apd.New(col.Int64()[rowIdx], 0)
				return alloc.NewDDecimal(tree.DDecimal{Decimal: d}), nil
			case types.DateFamily:
				date, err := pgdate.MakeDateFromUnixEpoch(col.Int64()[rowIdx])
				if err != nil {
					return nil, err
				}
				return alloc.NewDDate(tree.DDate{Date: date}), nil
			}
		case 16:
			switch hint.Family() {
			case types.IntFamily:
				return alloc.NewDInt(tree.DInt(col.Int16()[rowIdx])), nil
			}
		}
	case types.FloatFamily:
		switch hint.Family() {
		case types.FloatFamily:
			return alloc.NewDFloat(tree.DFloat(col.Float64()[rowIdx])), nil
		case types.DecimalFamily:
			var d apd.Decimal
			if _, err := d.SetFloat64(col.Float64()[rowIdx]); err != nil {
				return nil, err
			}
			return alloc.NewDDecimal(tree.DDecimal{Decimal: d}), nil
		}
	case types.BytesFamily:
		switch hint.Family() {
		case types.BytesFamily:
			return alloc.NewDBytes(tree.DBytes(col.Bytes().Get(rowIdx))), nil
		case types.StringFamily:
			data := col.Bytes().Get(rowIdx)
			str := *(*string)(unsafe.Pointer(&data))
			return alloc.NewDString(tree.DString(str)), nil
		default:
			data := col.Bytes().Get(rowIdx)
			str := *(*string)(unsafe.Pointer(&data))
			return rowenc.ParseDatumStringAs(hint, str, evalCtx)
		}
	}
	return nil, errors.Errorf(
		`don't know how to interpret %s column as %s`, col.Type(), hint)
}

