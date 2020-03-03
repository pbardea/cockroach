// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package sql

import (
	"context"
	"time"

	"github.com/cockroachdb/cockroach/pkg/config"
	"github.com/cockroachdb/cockroach/pkg/internal/client"
	"github.com/cockroachdb/cockroach/pkg/jobs/jobspb"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/storage/protectedts"
	"github.com/cockroachdb/cockroach/pkg/storage/protectedts/ptpb"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/errors"
)

// DropTableDesc removes a descriptor from the KV database.
func dropTableDesc(ctx context.Context, txn *client.Txn, tableDesc *sqlbase.TableDescriptor) error {
	descKey := sqlbase.MakeDescMetadataKey(tableDesc.ID)
	zoneKeyPrefix := config.MakeZoneKeyPrefix(uint32(tableDesc.ID))

	// Finished deleting all the table data, now delete the table meta data.
	// Delete table descriptor
	b := &client.Batch{}
	// Delete the descriptor.
	b.Del(descKey)
	// Delete the zone config entry for this table.
	b.DelRange(zoneKeyPrefix, zoneKeyPrefix.PrefixEnd(), false /* returnKeys */)
	if err := txn.SetSystemConfigTrigger(); err != nil {
		return err
	}

	return txn.Run(ctx, b)
}

// shouldGC returns nil if the table should be GC'd, an error otherwise.
// TODO(pbardea): Add protected timestamp verification.
func shouldGC(
	ctx context.Context,
	txn *client.Txn,
	protectedtsCache protectedts.Cache,
	table *sqlbase.TableDescriptor,
) (bool, error) {
	_, zoneCfg, _, err := GetZoneConfigInTxn(ctx, txn, uint32(table.ID),
		&sqlbase.IndexDescriptor{}, "", false /* getInheritedDefault */)
	if err != nil {
		return false, err
	}
	deadline := table.DropTime + int64(zoneCfg.GC.TTLSeconds)*time.Second.Nanoseconds()
	timePastDeadline := timeutil.Since(timeutil.Unix(0, deadline))

	// TODO(pbardea): Do interleaved tables ever reach here?
	sp := table.TableSpan()
	readAt := protectedtsCache.Iterate(ctx,
		sp.Key, sp.EndKey,
		func(r *ptpb.Record) (wantMore bool) {
			return true
		})
	protectedTime := readAt.WallTime
	if timeutil.Since(timeutil.Unix(0, protectedTime)) > 0 {

	}

	return timePastDeadline >= 0, nil
}

// truncateTable deletes all of the data in the specified table.
func truncateTable(
	ctx context.Context, txn *client.Txn, distSender *kv.DistSender, table *sqlbase.TableDescriptor,
) error {
	// If DropTime isn't set, assume this drop request is from a version
	// 1.1 server and invoke legacy code that uses DeleteRange and range GC.
	if table.DropTime == 0 {
		return errors.Errorf("expected the table to have a drop time")
	}

	tableKey := roachpb.RKey(keys.MakeTablePrefix(uint32(table.ID)))
	tableSpan := roachpb.RSpan{Key: tableKey, EndKey: tableKey.PrefixEnd()}

	// ClearRange requests lays down RocksDB range deletion tombstones that have
	// serious performance implications (#24029). The logic below attempts to
	// bound the number of tombstones in one store by sending the ClearRange
	// requests to each range in the table in small, sequential batches rather
	// than letting DistSender send them all in parallel, to hopefully give the
	// compaction queue time to compact the range tombstones away in between
	// requests.
	//
	// As written, this approach has several deficiencies. It does not actually
	// wait for the compaction queue to compact the tombstones away before
	// sending the next request. It is likely insufficient if multiple DROP
	// TABLEs are in flight at once. It does not save its progress in case the
	// coordinator goes down. These deficiences could be addressed, but this code
	// was originally a stopgap to avoid the range tombstone performance hit. The
	// RocksDB range tombstone implementation has since been improved and the
	// performance implications of many range tombstones has been reduced
	// dramatically making this simplistic throttling sufficient.

	// These numbers were chosen empirically for the clearrange roachtest and
	// could certainly use more tuning.
	const batchSize = 100
	const waitTime = 500 * time.Millisecond

	var n int
	lastKey := tableSpan.Key
	ri := kv.NewRangeIterator(distSender)
	for ri.Seek(ctx, tableSpan.Key, kv.Ascending); ; ri.Next(ctx) {
		if !ri.Valid() {
			return ri.Error().GoError()
		}

		if n++; n >= batchSize || !ri.NeedAnother(tableSpan) {
			endKey := ri.Desc().EndKey
			if tableSpan.EndKey.Less(endKey) {
				endKey = tableSpan.EndKey
			}
			var b client.Batch
			b.AddRawRequest(&roachpb.ClearRangeRequest{
				RequestHeader: roachpb.RequestHeader{
					Key:    lastKey.AsRawKey(),
					EndKey: endKey.AsRawKey(),
				},
			})
			log.VEventf(ctx, 2, "ClearRange %s - %s", lastKey, endKey)
			if err := txn.Run(ctx, &b); err != nil {
				return err
			}
			n = 0
			lastKey = endKey
			time.Sleep(waitTime)
		}

		if !ri.NeedAnother(tableSpan) {
			break
		}
	}

	return nil
}

// maybe Drop a table. Return nil if successfully dropped.
func dropTables(
	ctx context.Context,
	txn *client.Txn,
	distSender *kv.DistSender,
	protectedtsCache protectedts.Cache,
	details *jobspb.WaitingForGCDetails,
) error {
	for _, droppedTable := range details.Tables {
		var table *sqlbase.TableDescriptor
		var err error
		table, err = sqlbase.GetTableDescFromID(ctx, txn, droppedTable.ID)
		if err != nil {
			return err
		}

		// Get the tables.
		if !table.Dropped() {
			return nil
		}

		if ok, err := shouldGC(ctx, txn, protectedtsCache, table); err != nil {
			return err
		} else if !ok {
			// Not ready to GC yet.
			return nil
		}

		// First, delete all the table data.
		if err := truncateTable(ctx, txn, distSender, table); err != nil {
			return err
		}

		// Then, delete the table ID.
		if err := dropTableDesc(ctx, txn, table); err != nil {
			return err
		}
	}
	return nil
}

func cleanupDatabaseZoneConfig(ctx context.Context, txn *client.Txn, databaseID sqlbase.ID) error {
	// Finished deleting all the table data, now delete the table meta data.
	b := &client.Batch{}

	// Delete the zone config entry for the dropped database associated with the
	// job, if it exists.
	if databaseID == sqlbase.InvalidID {
		return nil
	}
	dbZoneKeyPrefix := config.MakeZoneKeyPrefix(uint32(databaseID))
	b.DelRange(dbZoneKeyPrefix, dbZoneKeyPrefix.PrefixEnd(), false /* returnKeys */)
	return txn.Run(ctx, b)
}
