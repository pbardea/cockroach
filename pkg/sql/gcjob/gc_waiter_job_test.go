// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package gcjob

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/internal/client"
	"github.com/cockroachdb/cockroach/pkg/jobs"
	"github.com/cockroachdb/cockroach/pkg/jobs/jobspb"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/testutils/serverutils"
	"github.com/cockroachdb/cockroach/pkg/testutils/sqlutils"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/stretchr/testify/require"
)

// TODO(pbardea): Add more testing around the timer calculations.
func TestWaitForGCJob(t *testing.T) {
	defer leaktest.AfterTest(t)()

	defer func(oldAdoptInterval, oldGCInterval time.Duration) {
		jobs.DefaultAdoptInterval = oldAdoptInterval
		MaxSQLGCInterval = oldGCInterval
	}(jobs.DefaultAdoptInterval, MaxSQLGCInterval)
	jobs.DefaultAdoptInterval = 100 * time.Millisecond
	MaxSQLGCInterval = 100 * time.Millisecond

	type DropItem int
	const (
		INDEX = iota
		TABLE
		DATABASE
	)

	for _, dropItem := range []DropItem{INDEX, TABLE, DATABASE} {
		for _, lowerTTL := range []bool{true, false} {
			s, db, kvDB := serverutils.StartServer(t, base.TestServerArgs{})
			ctx := context.Background()
			defer s.Stopper().Stop(ctx)
			sqlDB := sqlutils.MakeSQLRunner(db)

			jobRegistry := s.JobRegistry().(*jobs.Registry)

			sqlDB.Exec(t, "CREATE DATABASE my_db")
			sqlDB.Exec(t, "USE my_db")
			sqlDB.Exec(t, "CREATE TABLE my_table (a int primary key, b int, index (b))")
			sqlDB.Exec(t, "CREATE TABLE my_other_table (a int primary key, b int, index (b))")
			if lowerTTL {
				sqlDB.Exec(t, "ALTER TABLE my_table CONFIGURE ZONE USING gc.ttlseconds = 1")
				sqlDB.Exec(t, "ALTER TABLE my_other_table CONFIGURE ZONE USING gc.ttlseconds = 3")
			}
			myDBID := sqlbase.ID(keys.MinUserDescID + 2)
			myTableID := sqlbase.ID(keys.MinUserDescID + 3)
			myOtherTableID := sqlbase.ID(keys.MinUserDescID + 4)

			var myTableDesc *sqlbase.TableDescriptor
			var myOtherTableDesc *sqlbase.TableDescriptor
			if err := kvDB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
				var err error
				myTableDesc, err = sqlbase.GetTableDescFromID(ctx, txn, myTableID)
				if err != nil {
					return err
				}
				myOtherTableDesc, err = sqlbase.GetTableDescFromID(ctx, txn, myOtherTableID)
				return err
			}); err != nil {
				t.Fatal(err)
			}

			// Start the job that drops an index.
			dropTime := timeutil.Now().UnixNano()
			var details jobspb.WaitingForGCDetails
			switch dropItem {
			case INDEX:
				details = jobspb.WaitingForGCDetails{
					Indexes: []jobspb.WaitingForGCDetails_DroppedIndex{
						{
							IndexID:  sqlbase.IndexID(2),
							DropTime: timeutil.Now().UnixNano(),
						},
					},
					ParentID: myTableID,
				}
			case TABLE:
				details = jobspb.WaitingForGCDetails{
					Tables: []jobspb.WaitingForGCDetails_DroppedID{
						{
							ID:       myTableID,
							DropTime: dropTime,
						},
					},
				}
			case DATABASE:
				details = jobspb.WaitingForGCDetails{
					Tables: []jobspb.WaitingForGCDetails_DroppedID{
						{
							ID:       myTableID,
							DropTime: dropTime,
						},
						{
							ID:       myOtherTableID,
							DropTime: dropTime,
						},
					},
					ParentID: myDBID,
				}
			}

			jobRecord := jobs.Record{
				Description:   fmt.Sprintf("GC Indexes"),
				Username:      "user",
				DescriptorIDs: sqlbase.IDs{myTableID},
				Details:       details,
				Progress:      jobspb.WaitingForGCProgress{},
				NonCancelable: true,
			}

			resultsCh := make(chan tree.Datums)
			job, _, err := jobRegistry.CreateAndStartJob(ctx, resultsCh, jobRecord)
			if err != nil {
				t.Fatal(err)
			}

			switch dropItem {
			case INDEX:
				myTableDesc.Indexes = myTableDesc.Indexes[:0]
				myTableDesc.GCMutations = append(myTableDesc.GCMutations, sqlbase.TableDescriptor_GCDescriptorMutation{
					IndexID:  sqlbase.IndexID(2),
					DropTime: timeutil.Now().UnixNano(),
					JobID:    *job.ID(),
				})
			case DATABASE:
				myOtherTableDesc.State = sqlbase.TableDescriptor_DROP
				myOtherTableDesc.DropTime = dropTime
				fallthrough
			case TABLE:
				myTableDesc.State = sqlbase.TableDescriptor_DROP
				myTableDesc.DropTime = dropTime
			}

			if err := kvDB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
				b := txn.NewBatch()
				descKey := sqlbase.MakeDescMetadataKey(myTableID)
				descDesc := sqlbase.WrapDescriptor(myTableDesc)
				b.Put(descKey, descDesc)
				descKey2 := sqlbase.MakeDescMetadataKey(myOtherTableID)
				descDesc2 := sqlbase.WrapDescriptor(myOtherTableDesc)
				b.Put(descKey2, descDesc2)
				return txn.Run(ctx, b)
			}); err != nil {
				t.Fatal(err)
			}

			// Check that the job started.
			jobIDStr := strconv.Itoa(int(*job.ID()))
			sqlDB.CheckQueryResults(t, "SELECT job_id, status FROM [SHOW JOBS]", [][]string{{jobIDStr, "running"}})

			if lowerTTL {
				sqlDB.CheckQueryResultsRetry(t, "SELECT job_id, status FROM [SHOW JOBS]", [][]string{{jobIDStr, "succeeded"}})
			} else {
				time.Sleep(500 * time.Millisecond)
			}

			if err := kvDB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
				var err error
				myTableDesc, err = sqlbase.GetTableDescFromID(ctx, txn, myTableID)
				if lowerTTL && (dropItem == TABLE || dropItem == DATABASE) {
					// We dropped the table, so expect it to not be found.
					require.EqualError(t, err, "descriptor not found")
					return nil
				}
				myOtherTableDesc, err = sqlbase.GetTableDescFromID(ctx, txn, myOtherTableID)
				if lowerTTL && dropItem == DATABASE {
					// We dropped the entire database, so expect none of the tables to be found.
					require.EqualError(t, err, "descriptor not found")
					return nil
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}

			switch dropItem {
			case INDEX:
				if lowerTTL {
					require.Equal(t, 0, len(myTableDesc.GCMutations))
				} else {
					require.Equal(t, 1, len(myTableDesc.GCMutations))
				}
			case TABLE:
			case DATABASE:
				// Already handled the case where the TTL was lowered, since we expect
				// to not find the descriptor.
				// If the TTL was not lowered, we just expect to have not found an error
				// when fetching the TTL.
			}
		}
	}
}
