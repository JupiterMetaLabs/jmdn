// MODULE: DB_OPs/latest_block
// PURPOSE: Single monotonic choke point for the latest_block tip marker.
//          ThebeDB retarget of the F6 module.
//
// ON THEBEDB the authoritative tip READ is SQL MAX(block_number)
// (GetLatestBlockNumber) — monotonic by construction, and skeleton header
// rows cannot regress it. The explicit marker is still maintained through
// this choke point because (a) callers gate on the "did it move" result and
// (b) the marker records the DATA-COMPLETE tip (writers call it only after
// full-block writes — headers-only writers never do, preserving F6's
// "skeletons never advance it").
//
// CONCURRENCY: live block processing, DataSync workers, and catchup all
// write from this process; the mutex serializes the read-decide-write cycle
// (same pattern and rationale as the applied-anchor mutex in sync_anchor.go).

package DB_OPs

import (
	"fmt"
	"strconv"
	"sync"
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

// LatestBlockMarkerKey is the sync-state key holding the data-complete tip.
const LatestBlockMarkerKey = "latest_block"

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

	h, err := getHandle(nil)
	if err != nil {
		return 0, false, fmt.Errorf("latest_block: %w", err)
	}

	var current uint64
	if raw, err := h.GetSyncKV(LatestBlockMarkerKey); err != nil {
		return 0, false, fmt.Errorf("latest_block read: %w", err)
	} else if raw != nil {
		if v, perr := strconv.ParseUint(string(raw), 10, 64); perr == nil {
			current = v
		}
	}

	next, moved := nextLatestBlock(current, blockNumber)
	if !moved {
		return current, false, nil
	}
	if err := h.PutSyncKV(LatestBlockMarkerKey, []byte(strconv.FormatUint(next, 10))); err != nil {
		return current, false, fmt.Errorf("latest_block write: %w", err)
	}
	// Marker advanced: a new block's state is committed. Fire the (non-blocking)
	// advance hook so the node can push its fresh head to the seednode now rather
	// than at the next periodic tick. Held under latestBlockMu by contract.
	if onAdvance != nil {
		onAdvance(next)
	}
	return next, true, nil
}
