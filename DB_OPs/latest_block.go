// MODULE: DB_OPs/latest_block
// PURPOSE: Single monotonic choke point for the defaultdb `latest_block` marker.
//
// HISTORY: latest_block had FOUR writers and no guard —
//   1. StoreZKBlock wrote it blindly per block (immuclient.go:1949, since
//      removed): any out-of-order store (PoTS WAL dump, replayed block, sync worker)
//      regressed the tip; header-sync SKELETON blocks (headers, no data)
//      advanced it past the data-complete tip.
//   2. The headers writer therefore snapshot/RESTORED it around header batches
//      (immudb_headers_writer.go:38,118, since removed) — and the restore
//      clobbered any legitimate advance committed concurrently by DataSync
//      workers or live processing: a regression vector built to patch another.
//   3. The data writer's batch-end update was blind: a stale catchup batch
//      committing after newer live blocks moved the marker backwards (the
//      exact race txindex.setMetaMonotonicMax guards against).
//   4. Catchup phase 8 wrote remoteTip blindly.
//
// NOW: every writer goes through UpdateLatestBlockMonotonic. The marker never
// regresses; skeleton blocks never touch it (StoreZKBlock no longer writes it —
// callers that store FULL blocks bump it explicitly).
//
// CONCURRENCY: live block processing, DataSync workers, and catchup all write
// from this process; the mutex serializes the read-decide-write cycle (same
// pattern and rationale as the applied-anchor mutex in sync_anchor.go).

package DB_OPs

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var latestBlockMu sync.Mutex

// onAdvance, guarded by latestBlockMu, is fired (under the lock) whenever the
// marker actually advances — i.e. this node just committed a new block's state
// (balances). It is injected from main to push an immediate seednode
// block-state report right after apply, instead of waiting for the periodic
// sync-monitor tick. Injection (rather than importing syncmonitor/seednode here)
// avoids an import cycle.
//
// CONTRACT: the hook MUST be non-blocking and MUST NOT call back into DB_OPs —
// it runs while latestBlockMu is held, so any blocking or re-entrancy stalls all
// block application. Production wraps it in a debounced, fire-and-forget pusher
// (startSeedBlockHeadPusher) that only sets a flag and returns.
var onAdvance func(uint64)

// SetLatestBlockAdvanceHook installs the marker-advance hook (nil clears it).
// Call once at startup, before block application begins. Read and write of the
// hook are both serialized by latestBlockMu, so this is race-safe against a
// concurrent UpdateLatestBlockMonotonic.
func SetLatestBlockAdvanceHook(fn func(uint64)) {
	latestBlockMu.Lock()
	defer latestBlockMu.Unlock()
	onAdvance = fn
}

// nextLatestBlock is the pure monotonic decision (unit-tested).
func nextLatestBlock(current, candidate uint64) (uint64, bool) {
	if candidate > current {
		return candidate, true
	}
	return current, false
}

// UpdateLatestBlockMonotonic advances the latest_block marker to blockNumber
// iff it is greater than the stored value. Returns the resulting marker value
// and whether it moved. All latest_block writes MUST go through here.
func UpdateLatestBlockMonotonic(blockNumber uint64) (uint64, bool, error) {
	latestBlockMu.Lock()
	defer latestBlockMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	current, err := GetLatestBlockNumber(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("latest_block monotonic update: read: %w", err)
	}

	next, advance := nextLatestBlock(current, blockNumber)
	if !advance {
		return current, false, nil
	}
	if err := Update("latest_block", next); err != nil {
		return current, false, fmt.Errorf("latest_block monotonic update: write %d: %w", next, err)
	}
	// Marker advanced: a new block's state is committed. Fire the (non-blocking)
	// advance hook so the node can push its fresh head to the seednode now rather
	// than at the next periodic tick. Held under latestBlockMu by contract.
	if onAdvance != nil {
		onAdvance(next)
	}
	return next, true, nil
}
