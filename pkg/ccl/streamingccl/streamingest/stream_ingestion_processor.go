// Copyright 2020 The Cockroach Authors.
//
// Licensed as a CockroachDB Enterprise file under the Cockroach Community
// License (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
//     https://github.com/cockroachdb/cockroach/blob/master/licenses/CCL.txt

package streamingest

import (
	"context"
	"sort"
	"time"

	"github.com/cockroachdb/cockroach/pkg/ccl/storageccl"
	"github.com/cockroachdb/cockroach/pkg/ccl/streamingccl"
	"github.com/cockroachdb/cockroach/pkg/ccl/streamingccl/streamclient"
	"github.com/cockroachdb/cockroach/pkg/jobs/jobspb"
	"github.com/cockroachdb/cockroach/pkg/kv/bulk"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/settings"
	"github.com/cockroachdb/cockroach/pkg/sql"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfra"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfrapb"
	"github.com/cockroachdb/cockroach/pkg/sql/rowenc"
	"github.com/cockroachdb/cockroach/pkg/sql/rowexec"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/storage"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"github.com/cockroachdb/cockroach/pkg/util/protoutil"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/errors"
	"golang.org/x/sync/errgroup"
)

var minimumFlushInterval = settings.RegisterPublicDurationSettingWithExplicitUnit(
	"bulkio.stream_ingestion.minimum_flush_interval",
	"the minimum timestamp between flushes; unless the internal memory buffer is full",
	5*time.Second,
	nil, /* validateFn */
)

var streamIngestionResultTypes = []*types.T{
	types.Bytes, // jobspb.ResolvedSpan
}

type mvccKeyValues []storage.MVCCKeyValue

func (s mvccKeyValues) Len() int           { return len(s) }
func (s mvccKeyValues) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s mvccKeyValues) Less(i, j int) bool { return s[i].Key.Less(s[j].Key) }

type streamIngestionProcessor struct {
	execinfra.ProcessorBase

	flowCtx *execinfra.FlowCtx
	spec    execinfrapb.StreamIngestionDataSpec
	output  execinfra.RowReceiver

	// curBatch temporarily batches MVCC Keys so they can be
	// sorted before ingestion.
	// TODO: This doesn't yet use a buffering adder since the current
	// implementation is specific to ingesting KV pairs without timestamps rather
	// than MVCCKeys.
	curBatch mvccKeyValues
	// batcher is used to flush SSTs to the storage layer.
	batcher *bulk.SSTBatcher

	// client is a streaming client which provides a stream of events from a given
	// address.
	client streamclient.Client

	// ingestionErr stores any error that is returned from the worker goroutine so
	// that it can be forwarded through the DistSQL flow.
	ingestionErr error

	flushedCheckpoints  chan *jobspb.ResolvedSpan
	lastFlushTime       time.Time
	bufferedCheckpoints map[streamingccl.PartitionAddress]hlc.Timestamp
	events chan partitionEvent
}

// partitionEvent augments a normal event with the partition it came from.
type partitionEvent struct {
	streamingccl.Event
	partition streamingccl.PartitionAddress
}

var _ execinfra.Processor = &streamIngestionProcessor{}
var _ execinfra.RowSource = &streamIngestionProcessor{}

const streamIngestionProcessorName = "stream-ingestion-processor"

func newStreamIngestionDataProcessor(
	flowCtx *execinfra.FlowCtx,
	processorID int32,
	spec execinfrapb.StreamIngestionDataSpec,
	post *execinfrapb.PostProcessSpec,
	output execinfra.RowReceiver,
) (execinfra.Processor, error) {
	streamClient, err := streamclient.NewStreamClient(spec.StreamAddress)
	if err != nil {
		return nil, err
	}

	// Check if there are any interceptor methods that need to be registered with
	// the stream client.
	// These methods are invoked on every emitted Event.
	if knobs, ok := flowCtx.Cfg.TestingKnobs.StreamIngestionTestingKnobs.(*sql.
		StreamIngestionTestingKnobs); ok {
		if knobs.Interceptors != nil {
			if interceptable, ok := streamClient.(streamclient.InterceptableStreamClient); ok {
				for _, interceptor := range knobs.Interceptors {
					interceptable.RegisterInterception(interceptor)
				}
			}
		}
	}

	sip := &streamIngestionProcessor{
		flowCtx:             flowCtx,
		spec:                spec,
		output:              output,
		curBatch:            make([]storage.MVCCKeyValue, 0),
		client:              streamClient,
		flushedCheckpoints:  make(chan *jobspb.ResolvedSpan, len(spec.PartitionAddresses)),
		bufferedCheckpoints: make(map[streamingccl.PartitionAddress]hlc.Timestamp),
	}

	evalCtx := flowCtx.EvalCtx
	db := flowCtx.Cfg.DB
	sip.batcher, err = bulk.MakeStreamSSTBatcher(sip.Ctx, db, evalCtx.Settings,
		func() int64 { return storageccl.MaxImportBatchSize(evalCtx.Settings) })
	if err != nil {
		return nil, errors.Wrap(err, "making sst batcher")
	}

	if err := sip.Init(sip, post, streamIngestionResultTypes, flowCtx, processorID, output, nil, /* memMonitor */
		execinfra.ProcStateOpts{
			InputsToDrain:        []execinfra.RowSource{},
			TrailingMetaCallback: nil, // no resources to close
		},
	); err != nil {
		return nil, err
	}

	return sip, nil
}

// Start is part of the RowSource interface.
func (sip *streamIngestionProcessor) Start(ctx context.Context) context.Context {
	ctx = sip.StartInternal(ctx, streamIngestionProcessorName)

	go func() {
		defer close(sip.flushedCheckpoints)

		startTime := timeutil.Unix(0 /* sec */, sip.spec.StartTime.WallTime)
		eventChs := make(map[streamingccl.PartitionAddress]chan streamingccl.Event)
		for _, partitionAddress := range sip.spec.PartitionAddresses {
			eventCh, err := sip.client.ConsumePartition(ctx, partitionAddress, startTime)
			if err != nil {
				sip.ingestionErr = errors.Wrapf(err, "consuming partition %v", partitionAddress)
				return
			}
			eventChs[partitionAddress] = eventCh
		}
		mergedEvents := sip.merge(ctx, eventChs)

		sip.ingestionErr = sip.consumeEvents(mergedEvents)
	}()

	return ctx
}

// Next is part of the RowSource interface.
func (sip *streamIngestionProcessor) Next() (rowenc.EncDatumRow, *execinfrapb.ProducerMetadata) {
	if sip.State != execinfra.StateRunning {
		return nil, sip.DrainHelper()
	}

	// rRead from buffered resolved events.
	progressUpdate, ok := <-sip.flushedCheckpoints
	if ok && progressUpdate != nil {
		progressBytes, err := protoutil.Marshal(progressUpdate)
		if err != nil {
			sip.MoveToDraining(err)
			return nil, sip.DrainHelper()
		}
		row := rowenc.EncDatumRow{
			rowenc.DatumToEncDatum(types.Bytes, tree.NewDBytes(tree.DBytes(progressBytes))),
		}
		return row, nil
	}

	if sip.ingestionErr != nil {
		sip.MoveToDraining(sip.ingestionErr)
		return nil, sip.DrainHelper()
	}

	sip.MoveToDraining(nil /* error */)
	return nil, sip.DrainHelper()
}

// ConsumerClosed is part of the RowSource interface.
func (sip *streamIngestionProcessor) ConsumerClosed() {
	sip.InternalClose()
}

func (sip *streamIngestionProcessor) flush() error {
	// Ensure that the current batch is sorted.
	sort.Sort(sip.curBatch)

	for _, kv := range sip.curBatch {
		if err := sip.batcher.AddMVCCKey(sip.Ctx, kv.Key, kv.Value); err != nil {
			return errors.Wrapf(err, "adding key %+v", kv)
		}
	}

	if err := sip.batcher.Flush(sip.Ctx); err != nil {
		return errors.Wrap(err, "flushing")
	}

	// Go through buffered checkpoint events, and put them on the channel to be
	// emitted to the downstream frontier processor.
	for partition, timestamp := range sip.bufferedCheckpoints {
		// Each partition is represented by a span defined by the
		// partition address.
		spanStartKey := roachpb.Key(partition)
		sip.flushedCheckpoints <- &jobspb.ResolvedSpan{
			Span:      roachpb.Span{Key: spanStartKey, EndKey: spanStartKey.Next()},
			Timestamp: timestamp,
		}
	}

	// Reset the current batch.
	sip.curBatch = nil
	sip.lastFlushTime = timeutil.Now()
	sip.bufferedCheckpoints = make(map[streamingccl.PartitionAddress]hlc.Timestamp)

	return sip.batcher.Reset(sip.Ctx)
}

// merge takes events from all the streams and merges them into a single
// channel.
func (sip *streamIngestionProcessor) merge(
	ctx context.Context, partitionStreams map[streamingccl.PartitionAddress]chan streamingccl.Event,
) chan partitionEvent {
	merged := make(chan partitionEvent)

	g, gctx := errgroup.WithContext(ctx)

	for partition, eventCh := range partitionStreams {
		partition := partition
		eventCh := eventCh
		g.Go(func() error {
			ctxDone := gctx.Done()
			for {
				select {
				case event, ok := <-eventCh:
					if !ok {
						return nil
					}

					pe := partitionEvent{
						Event:     event,
						partition: partition,
					}

					select {
					case merged <- pe:
					case <-ctxDone:
						return errors.Wrap(gctx.Err(), "gctx err")
					}
				case <-ctxDone:
					return errors.Wrap(gctx.Err(), "gctx err")
				}
			}
		})
	}
	go func() {
		sip.ingestionErr = g.Wait()
		close(merged)
	}()

	return merged
}

// consumeEvents continues to consume events until it flushes a checkpoint
// event. It buffers incoming events, and periodically flushes.
func (sip *streamIngestionProcessor) consumeEvents() error {
	// This timer is used to batch up resolved timestamp events that occur within
	// a given time interval, as to not flush too often and allow the buffer to
	// accumulate data.
	// A flush may still occur if the in memory buffer becomes full.
	var timer timeutil.Timer
	defer timer.Stop()
	sv := &sip.FlowCtx.Cfg.Settings.SV

	defer func() {
		if sip.batcher != nil {
			sip.batcher.Close()
		}
	}()

	for {
		select {
		case event, ok := sip.events<-events:
			if !ok {
				// Done consuming events, flush the remaining.
				return sip.flush()
			}

			switch event.Type() {
			case streamingccl.KVEvent:
				if err := sip.bufferKV(event); err != nil {
					return err
				}
			case streamingccl.CheckpointEvent:
				if err := sip.bufferCheckpoint(event); err != nil {
					return err
				}

				minFlushInterval := minimumFlushInterval.Get(sv)
				if timeutil.Since(sip.lastFlushTime) < minFlushInterval {
					// Not enough time has passed since the last flush. Let's set a timer
					// that will trigger a flush eventually.
					// TODO: This resets the timer every checkpoint event, but we only
					// need to reset it once.
					timer.Reset(time.Until(sip.lastFlushTime.Add(minFlushInterval)))
					continue
				}

				// If a checkpoint event comes in at the same time that the timer is
				// allowed to fire, the checkpoint event may fire first. We can cancel
				// it here to avoid a double-fire in this scenario.
				timer.Stop()
				if err := sip.flush(); err != nil {
					return err
				}
			default:
				return errors.Newf("unknown streaming event type %v", event.Type())
			}
		case <-timer.C:
			timer.Read = true
			if err := sip.flush(); err != nil {
				return err
			}
		}
	}
}

func (sip *streamIngestionProcessor) bufferKV(event partitionEvent) error {
	// TODO: In addition to flushing when receiving a checkpoint event, we
	// should also flush when we've buffered sufficient KVs. A buffering adder
	// would save us here.

	kv := event.GetKV()
	if kv == nil {
		return errors.New("kv event expected to have kv")
	}
	mvccKey := storage.MVCCKey{
		Key:       kv.Key,
		Timestamp: kv.Value.Timestamp,
	}
	sip.curBatch = append(sip.curBatch, storage.MVCCKeyValue{Key: mvccKey, Value: kv.Value.RawBytes})
	return nil
}

func (sip *streamIngestionProcessor) bufferCheckpoint(event partitionEvent) error {
	resolvedTimePtr := event.GetResolved()
	if resolvedTimePtr == nil {
		return errors.New("checkpoint event expected to have a resolved timestamp")
	}
	resolvedTime := *resolvedTimePtr

	// Buffer the checkpoint.
	if lastTimestamp, ok := sip.bufferedCheckpoints[event.partition]; !ok || lastTimestamp.Less(resolvedTime) {
		sip.bufferedCheckpoints[event.partition] = resolvedTime
	}
	return nil
}

func init() {
	rowexec.NewStreamIngestionDataProcessor = newStreamIngestionDataProcessor
}
