// Copyright 2021 The Cockroach Authors.
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
	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/blobs"
	"github.com/cockroachdb/cockroach/pkg/security"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfra"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/storage/cloud"
	"github.com/cockroachdb/cockroach/pkg/storage/enginepb"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"testing"

	"github.com/cockroachdb/cockroach/pkg/ccl/workloadccl/format"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfrapb"
	"github.com/cockroachdb/cockroach/pkg/sql/rowenc"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/cockroach/pkg/workload/tpcc"
	"github.com/stretchr/testify/require"
	_ "github.com/cockroachdb/cockroach/pkg/storage/cloud/testsink"
)

/*
Things to mock:
- Cloud storage with a fixed latency that produces SSTs.
- Null AddSSTable op.
*/

func makeTestStorage(
	uri string,
) (cloud.ExternalStorage, error) {
	conf, err := cloud.ExternalStorageConfFromURI(uri, security.RootUserName())
	if err != nil {
		return nil, err
	}
	testSettings := cluster.MakeTestingClusterSettings()

	// Setup a sink for the given args.
	clientFactory := blobs.TestBlobServiceClient(testSettings.ExternalIODir)
	s, err := cloud.MakeExternalStorage(context.Background(), conf, base.ExternalIODirConfig{},
		testSettings, clientFactory, nil, nil)
	if err != nil {
		return nil, err
	}

	return s, nil
}


type testRowSource struct{}

var _ execinfra.RowSource = testRowSource{}

func (testRowSource) OutputTypes() []*types.T { return nil }
func (testRowSource) Start(context.Context)   {}
func (testRowSource) Next() (rowenc.EncDatumRow, *execinfrapb.ProducerMetadata) {
	return nil, nil
}
func (testRowSource) ConsumerDone()   {}
func (testRowSource) ConsumerClosed() {}

// getSSTs generates a list of SSTs, with all keys at a fixed timestamp.
func getSSTs(b *testing.B) []execinfrapb.RestoreSpanEntry {
	g := tpcc.FromWarehouses(1)

	ts := timeutil.Now()
	var entries []execinfrapb.RestoreSpanEntry
	dir, err := makeTestStorage("test://test/")
	require.NoError(b, err)
	for i, table := range g.Tables() {
		tableID := descpb.ID(keys.MinUserDescID + 1 + i)
		sst, err := format.ToSSTable(table, tableID, ts)
		require.NoError(b, err)

		filename := fmt.Sprintf("%d.sst", i)
		writer, err := dir.Writer(context.Background(), filename)
		require.NoError(b, err)
		_, err = writer.Write(sst)
		require.NoError(b, err)

		startKey := keys.SystemSQLCodec.TablePrefix(uint32(tableID))
		// "Upload" the bytes to the mock external storage.
		entry := execinfrapb.RestoreSpanEntry{
			Span: roachpb.Span{
				Key:    startKey,
				EndKey: startKey.PrefixEnd(),
			},
			Files: []execinfrapb.RestoreFileSpec{
				{
					Dir:  dir.Conf(),
					Path: filename,
				},
			},
			ProgressIdx: int64(i),
		}
		entry.Span.EndKey = entry.Span.Key.PrefixEnd()

		entries = append(entries, entry)
	}

	return entries
}

type noopSender struct{}

func (noopSender) AddSSTable(
	_ context.Context,
	_, _ interface{},
	_ []byte,
	_ bool,
	_ *enginepb.MVCCStats,
	_ bool,
	_ hlc.Timestamp,
) error {
	return nil
}

func (noopSender) SplitAndScatter(ctx context.Context, _ roachpb.Key, _ hlc.Timestamp) error {
	return nil
}


// Benchmark how long it takes to
//  1. Open an iterator to remote storage
//  2. Iterate the SST
//  3. Buffer the SST
//  4. Call flush on the SST (todo: should this be mocked, or just set to 0?)
func BenchmarkSomething(b *testing.B) {
	// TODO: Setup.
	//  - Make a mock data processor.
	//  - Make an input suorce.
	// TODO: Refactor this helper to just use entries.
	entries := getSSTs(b)
	entriesCh := make(chan execinfrapb.RestoreSpanEntry, len(entries))
	for _, entry := range entries {
		entriesCh <- entry
	}
	// TODO: Make a no-op rekeyer.
	rekeys := []execinfrapb.TableRekey{}
	mockRestoreDataSpec := execinfrapb.RestoreDataSpec{
		Rekeys: rekeys,
	}
	ctx := context.Background()
	testSettings := cluster.MakeTestingClusterSettings()
	evalCtx := tree.EvalContext{Settings: testSettings}
	flowCtx := execinfra.FlowCtx{Cfg: &execinfra.ServerConfig{DB: nil /* db */,
		ExternalStorage: func(ctx context.Context, dest roachpb.ExternalStorage) (cloud.ExternalStorage, error) {
			return cloud.MakeExternalStorage(ctx, dest, base.ExternalIODirConfig{},
				testSettings, nil /* blob service */, nil /* ie */, nil /* kvDB */)
		},
		Settings: testSettings,
		Codec:    keys.SystemSQLCodec,
	},
		EvalCtx: &tree.EvalContext{
			Codec:    keys.SystemSQLCodec,
			Settings: testSettings,
		},
	}

	mockRestoreDataProcessor, err := newTestingRestoreDataProcessor(ctx, &evalCtx, &flowCtx,
		mockRestoreDataSpec)
	require.NoError(b, err)

	b.ResetTimer()
	// Run benchmark in loop.
	for i := 0; i < b.N; i++ {
		require.NoError(b, mockRestoreDataProcessor.runRestoreWorkers(noopSender{}, entriesCh))
		// Run benchmark.
	}
}

// func BenchmarkDatabaseBackup(b *testing.B) {
// 	// NB: This benchmark takes liberties in how b.N is used compared to the go
// 	// documentation's description. We're getting useful information out of it,
// 	// but this is not a pattern to cargo-cult.

// 	_, _, sqlDB, dir, cleanupFn := backupccl.BackupRestoreTestSetup(b, backupccl.MultiNode,
// 		0 /* numAccounts */, backupccl.InitManualReplication)
// 	defer cleanupFn()
// 	sqlDB.Exec(b, `DROP TABLE data.bank`)

// 	bankData := bank.FromRows(b.N).Tables()[0]
// 	loadURI := "nodelocal://0/load"
// 	if _, err := sampledataccl.ToBackup(b, bankData, dir, "load"); err != nil {
// 		b.Fatalf("%+v", err)
// 	}
// 	sqlDB.Exec(b, fmt.Sprintf(`RESTORE data.* FROM '%s'`, loadURI))

// 	// TODO(dan): Ideally, this would split and rebalance the ranges in a more
// 	// controlled way. A previous version of this code did it manually with
// 	// `SPLIT AT` and TestCluster's TransferRangeLease, but it seemed to still
// 	// be doing work after returning, which threw off the timing and the results
// 	// of the benchmark. DistSQL is working on improving this infrastructure, so
// 	// use what they build.

// 	b.ResetTimer()
// 	var unused string
// 	var dataSize int64
// 	sqlDB.QueryRow(b, fmt.Sprintf(`BACKUP DATABASE data TO '%s'`, backupccl.LocalFoo)).Scan(
// 		&unused, &unused, &unused, &unused, &unused, &dataSize,
// 	)
// 	b.StopTimer()
// 	b.SetBytes(dataSize / int64(b.N))
// }
