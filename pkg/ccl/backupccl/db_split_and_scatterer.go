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
	"time"

	"github.com/cockroachdb/cockroach/pkg/ccl/storageccl"
	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/errors"
)

// dbSplitAndScatter is the production implementation of this processor's
// scatterer. It actually issues the split and scatter requests for KV. This is
// mocked out in some tests.
type dbSplitAndScatterer struct{}

// splitAndScatterKey implements the splitAndScatterer interface.
// It splits and scatters a span specified by a given key, and returns the node
// to which the span was scattered. If the destination node could not be
// determined, node ID of 0 is returned.
func (s dbSplitAndScatterer) splitAndScatterKey(
	ctx context.Context, db *kv.DB, kr *storageccl.KeyRewriter, key roachpb.Key, randomizeLeases bool,
) (roachpb.NodeID, error) {
	expirationTime := db.Clock().Now().Add(time.Hour.Nanoseconds(), 0)
	newSpanKey, err := rewriteBackupSpanKey(kr, key)
	if err != nil {
		return 0, err
	}

	// TODO(pbardea): Really, this should be splitting the Key of the _next_
	// entry.
	log.VEventf(ctx, 1, "presplitting new key %+v", newSpanKey)
	if err := db.AdminSplit(ctx, newSpanKey, expirationTime); err != nil {
		return 0, errors.Wrapf(err, "splitting key %s", newSpanKey)
	}

	log.VEventf(ctx, 1, "scattering new key %+v", newSpanKey)
	req := &roachpb.AdminScatterRequest{
		RequestHeader: roachpb.RequestHeaderFromSpan(roachpb.Span{
			Key:    newSpanKey,
			EndKey: newSpanKey.Next(),
		}),
		// This is a bit of a hack, but it seems to be an effective one (see #36665
		// for graphs). As of the commit that added this, scatter is not very good
		// at actually balancing leases. This is likely for two reasons: 1) there's
		// almost certainly some regression in scatter's behavior, it used to work
		// much better and 2) scatter has to operate by balancing leases for all
		// ranges in a cluster, but in RESTORE, we really just want it to be
		// balancing the span being restored into.
		RandomizeLeases: randomizeLeases,
	}

	res, pErr := kv.SendWrapped(ctx, db.NonTransactionalSender(), req)
	if pErr != nil {
		// TODO(pbardea): Unfortunately, Scatter is still too unreliable to
		// fail the RESTORE when Scatter fails. I'm uncomfortable that
		// this could break entirely and not start failing the tests,
		// but on the bright side, it doesn't affect correctness, only
		// throughput.
		log.Errorf(ctx, "failed to scatter span [%s,%s): %+v",
			newSpanKey, newSpanKey.Next(), pErr.GoError())
		return 0, nil
	}

	return s.findDestination(res.(*roachpb.AdminScatterResponse)), nil
}

// findDestination returns the node ID of the node of the destination of the
// AdminScatter request. If the destination cannot be found, 0 is returned.
func (s dbSplitAndScatterer) findDestination(res *roachpb.AdminScatterResponse) roachpb.NodeID {
	// A request from a 20.1 node will not have a RangeInfos with a lease.
	// For this mixed-version state, we'll report the destination as node 0
	// and suffer a bit of inefficiency.
	if len(res.RangeInfos) > 0 {
		// If the lease is not populated, we return the 0 value anyway. We receive 1
		// RangeInfo per range that was scattered. Since we send a scatter request
		// to each range that we make, we are only interested in the first range,
		// which contains the key at which we're splitting and scattering.
		return res.RangeInfos[0].Lease.Replica.NodeID
	}

	return roachpb.NodeID(0)
}
