// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// TODO(pbardea):
//  - [ ] Audit if we actually want to return an error in all these places.
//  - [ ] Add logging.

// The job details should contain the following depending on the props:
// 1. Index deletions: One or more deletions of an index on a table.
//      details.Indexes -> the indexes to GC. These indexes must be
//      non-interleaved.
//      details.TableID -> the ID of the table which owns these indexes.
//
// 2. Table deletions: The deletion of a single table.
//      details.Tables -> the tables to be deleted. (To confirm: May be multiple
//      in case of cascade deletions?)
//
// 3. Database deletions: The deletion of a database and therefore all its tables.
//      details.Tables -> the IDs of the tables to GC.
//      details.DatabaseID -> the ID of the database to drop.
//

package sql

import (
	"context"
	"time"

	"github.com/cockroachdb/cockroach/pkg/config"
	"github.com/cockroachdb/cockroach/pkg/gossip"
	"github.com/cockroachdb/cockroach/pkg/internal/client"
	"github.com/cockroachdb/cockroach/pkg/jobs"
	"github.com/cockroachdb/cockroach/pkg/jobs/jobspb"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/util/encoding"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/errors"
)

const (
	// TODO(pbardea): Think of a better name.
	gcJobInterval = 30 * time.Second
)

type waitForGCResumer struct {
	jobID int64
}

// TODO(pbardea): Update this to be smarter about lookup up zone configs.
func (r *waitForGCResumer) updateDeadlines(ctx context.Context, execCfg *ExecutorConfig) error {
	k := keys.MakeTablePrefix(uint32(keys.ZonesTableID))
	k = encoding.EncodeUvarintAscending(k, uint64(keys.ZonesTablePrimaryIndexID))
	zoneCfgFilter := gossip.MakeSystemConfigDeltaFilter(k)
	descKeyPrefix := keys.MakeTablePrefix(uint32(sqlbase.DescriptorTable.ID))
	cfgFilter := gossip.MakeSystemConfigDeltaFilter(descKeyPrefix)
	job, err := execCfg.JobRegistry.LoadJob(ctx, r.jobID)
	if err != nil {
		return err
	}
	details := job.Details().(jobspb.WaitingForGCDetails)

	cfg := execCfg.Gossip.GetSystemConfig()
	if log.V(2) {
		log.Info(ctx, "received a new config")
	}

	tablesToGC := make(map[sqlbase.ID]struct{})
	for _, tableID := range details.Tables {
		tablesToGC[tableID.ID] = struct{}{}
	}
	// Check to see if the zone cfg or any of the descriptors have been modified.
	tablesToCheck := make(map[sqlbase.ID]struct{})
	zoneCfgFilter.ForModified(cfg, func(kv roachpb.KeyValue) {
		// Check all the tables.
		tablesToCheck = tablesToGC
	})

	// On table descriptor changes, update the GC TTL.
	cfgFilter.ForModified(cfg, func(kv roachpb.KeyValue) {
		// Attempt to unmarshal config into a table/database descriptor.
		var descriptor sqlbase.Descriptor
		if err := kv.Value.GetProto(&descriptor); err != nil {
			log.Warningf(ctx, "%s: unable to unmarshal descriptor %v", kv.Key, kv.Value)
			return
		}
		switch union := descriptor.Union.(type) {
		case *sqlbase.Descriptor_Table:
			table := union.Table
			if err := table.MaybeFillInDescriptor(ctx, nil); err != nil {
				log.Errorf(ctx, "%s: failed to fill in table descriptor %v", kv.Key, table)
				return
			}
			if err := table.ValidateTable(); err != nil {
				log.Errorf(ctx, "%s: received invalid table descriptor: %s. Desc: %v",
					kv.Key, err, table,
				)
				return
			}
			// Update the deadline since the descriptor changed.
			if _, ok := tablesToGC[table.ID]; ok {
				tablesToCheck[table.ID] = struct{}{}
			}

		case *sqlbase.Descriptor_Database:
			// We don't care if the database descriptor changes as it doesn't have any
			// effect on the TTL of it's tables.
		}
	})

	for tableID := range tablesToCheck {
		if err := r.updateDeadlineForTable(ctx, cfg, execCfg, tableID, details); err != nil {
			return err
		}
	}

	return nil
}

// updateDeadlineForTable
func (r *waitForGCResumer) updateDeadlineForTable(
	ctx context.Context,
	cfg *config.SystemConfig,
	execCfg *ExecutorConfig,
	tableID sqlbase.ID,
	details jobspb.WaitingForGCDetails,
) error {
	defTTL := execCfg.DefaultZoneConfig.GC.TTLSeconds

	if err := execCfg.DB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
		table, err := sqlbase.GetTableDescFromID(ctx, txn, tableID)
		if err != nil {
			return err
		}

		zoneCfg, placeholder, _, err := ZoneConfigHook(cfg, uint32(tableID))
		if err != nil {
			log.Errorf(ctx, "zone config for desc: %d, err = %+v", tableID, err)
			return nil
		}

		// Check if the table is dropping.
		if table.Dropped() {
			lifetime := int64(defTTL) * time.Second.Nanoseconds()
			if zoneCfg != nil {
				lifetime = int64(zoneCfg.GC.TTLSeconds) * time.Second.Nanoseconds()
			}
			for _, t := range details.Tables {
				if t.ID == table.ID {
					t.Deadline = t.DropTime + lifetime
					break
				}
			}
		}

		if zoneCfg == nil {

		}

		if placeholder == nil {
			placeholder = zoneCfg
		}
		// Update the deadline for indexes that are being dropped, if any.
		indexes := details.Indexes
		for i := 0; i < len(indexes); i++ {
			droppedIdx := &indexes[i]

			ttlSeconds := zoneCfg.GC.TTLSeconds
			if subzone := placeholder.GetSubzone(
				uint32(droppedIdx.IndexID), ""); subzone != nil && subzone.Config.GC != nil {
				ttlSeconds = subzone.Config.GC.TTLSeconds
			}

			droppedIdx.Deadline = droppedIdx.DropTime + int64(ttlSeconds)*time.Second.Nanoseconds()
		}
		details.Indexes = indexes

		// Update the jobs payload.
		job, err := execCfg.JobRegistry.LoadJobWithTxn(ctx, r.jobID, txn)
		if err != nil {
			return err
		}
		return job.WithTxn(txn).SetDetails(ctx, details)
	}); err != nil {
		return err
	}
	return nil
}

// Check if we are done GC'ing everything we should be.
func (r *waitForGCResumer) isDoneGC(details jobspb.WaitingForGCDetails) bool {
	return len(details.Indexes) == 0 && len(details.Tables) == 0
}

// Resume is part of the jobs.Resumer interface.
func (r waitForGCResumer) Resume(
	ctx context.Context, phs interface{}, resultsCh chan<- tree.Datums,
) error {
	// Make sure to update the deadlines are updated before starting to listen for
	// zone config changes.
	p := phs.(PlanHookState)
	execCfg := p.ExecCfg()
	db := execCfg.DB
	var details jobspb.WaitingForGCDetails
	var job *jobs.Job
	if err := db.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
		var err error
		job, err = execCfg.JobRegistry.LoadJobWithTxn(ctx, r.jobID, txn)
		if err != nil {
			return err
		}
		details = job.Details().(jobspb.WaitingForGCDetails)
		return nil
	}); err != nil {
		return err
	}

	gossipUpdateC := execCfg.Gossip.RegisterSystemConfigChannel()
	timer := time.NewTimer(gcJobInterval)

	// Ensure that the deadlines of the elements we want to drop are up to date.
	if err := r.updateDeadlines(ctx, execCfg); err != nil {
		return err
	}

	for {
		select {
		case <-gossipUpdateC:
			if err := r.updateDeadlines(ctx, execCfg); err != nil {
				return err
			}
		case <-timer.C:
			// The wait for GC jobs will either wait to drop indexes or to drop
			// tables(s).
			switch details.DroppingElement {
			case jobspb.WaitingForGCDetails_INDEXES:
				if err := execCfg.DB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
					if details.IndexParent == nil {
						return errors.Errorf("table which holds the indexes to gc must be specified")
					}
					table, err := sqlbase.GetTableDescFromID(ctx, txn, details.IndexParent.ID)
					if err != nil {
						return err
					}
					if err := gcIndexes(ctx, txn, execCfg, table, &details); err != nil {
						return err
					}
					return job.WithTxn(txn).SetDetails(ctx, details)
				}); err != nil {
					return err
				}
			case jobspb.WaitingForGCDetails_TABLES:
				if err := execCfg.DB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
					if err := dropTables(ctx, txn, execCfg.DistSender, execCfg.ProtectedTimestampProvider, &details); err != nil {
						return err
					}
					return job.WithTxn(txn).SetDetails(ctx, details)
				}); err != nil {
					return err
				}
			case jobspb.WaitingForGCDetails_DATABASE:
				if err := execCfg.DB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
					if err := dropTables(ctx, txn, execCfg.DistSender, execCfg.ProtectedTimestampProvider, &details); err != nil {
						return err
					}
					if err := job.WithTxn(txn).SetDetails(ctx, details); err != nil {
						return err
					}

					return cleanupDatabaseZoneConfig(ctx, txn, details.DatabaseID)
				}); err != nil {
					return err
				}
			case jobspb.WaitingForGCDetails_NONE:
				return errors.Errorf("DroppingElement must be specified for GC job")
			}

			if r.isDoneGC(details) {
				break
			}

			// Reset the timer.
			timer = time.NewTimer(gcJobInterval)
		}
	}
}

// OnSuccess is part of the jobs.Resumer interface.
func (r waitForGCResumer) OnSuccess(context.Context, *client.Txn) error {
	return nil
}

// OnTerminal is part of the jobs.Resumer interface.
func (r waitForGCResumer) OnTerminal(context.Context, jobs.Status, chan<- tree.Datums) {
}

// OnFailOrCancel is part of the jobs.Resumer interface.
func (r waitForGCResumer) OnFailOrCancel(ctx context.Context, phs interface{}) error {
	return nil
}

func init() {
	createResumerFn := func(job *jobs.Job, settings *cluster.Settings) jobs.Resumer {
		return &waitForGCResumer{
			jobID: *job.ID(),
		}
	}
	jobs.RegisterConstructor(jobspb.TypeWaitingForGC, createResumerFn)
}
