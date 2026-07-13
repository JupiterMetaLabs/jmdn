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
	return next, true, nil
}
