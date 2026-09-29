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
	"sync/atomic"
)

var latestBlockMu sync.Mutex

// cachedTip mirrors the data-complete tip marker in memory (0 = not yet known
// in this process). Updated on every read and advance through
// UpdateLatestBlockMonotonic. Read lock-free by CachedCommittedTip for hot
// paths (committee pool resolution) that must not touch the DB per call.
var cachedTip atomic.Uint64

// CachedCommittedTip returns this process's last known data-complete tip and
// whether one is known yet. It never touches the DB; callers that get
// known=false may fall back to GetLatestBlockNumber once.
func CachedCommittedTip() (tip uint64, known bool) {
	v := cachedTip.Load()
	return v, v != 0
}

// noteTip raises cachedTip monotonically.
func noteTip(v uint64) {
	for {
		cur := cachedTip.Load()
		if v <= cur || cachedTip.CompareAndSwap(cur, v) {
			return
		}
	}
}

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

	noteTip(current)
	next, moved := nextLatestBlock(current, blockNumber)
	if !moved {
		return current, false, nil
	}
	if err := h.PutSyncKV(LatestBlockMarkerKey, []byte(strconv.FormatUint(next, 10))); err != nil {
		return current, false, fmt.Errorf("latest_block write: %w", err)
	}
	noteTip(next)
	// Marker advanced: a new block's state is committed. Fire the (non-blocking)
	// advance hook so the node can push its fresh head to the seednode now rather
	// than at the next periodic tick. Held under latestBlockMu by contract.
	if onAdvance != nil {
		onAdvance(next)
	}
	return next, true, nil
}

// GetLatestDataCompleteBlock returns the DATA-COMPLETE tip: the latest_block
// marker, which is advanced ONLY after a block's full apply+store succeeds
// (UpdateLatestBlockMonotonic). It returns 0 when the marker is unset (fresh node).
//
// D-858 (review B1): this is DELIBERATELY different from GetLatestBlockNumber, which
// reads SQL MAX(block_number). StoreZKBlock is a non-atomic chain of gateway writes
// (block → snapshot → [zkproof] → transactions); a WriteTransaction projection
// failure (the uq_txn_block_index 858 trigger) leaves the `blocks` row already
// committed, so MAX(block_number) advances to a height whose state was rolled back
// and never applied. The duplicate-height ingress gates and the vote chain-position
// gate MUST read the data-complete marker, not MAX — otherwise a skeleton row from a
// failed store makes them treat a never-applied height as committed: the ingress
// gates then reject re-proposal of that height PERMANENTLY (no TTL), and the vote
// gate links N+1 against a block nobody applied.
func GetLatestDataCompleteBlock() (uint64, error) {
	h, err := getHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("GetLatestDataCompleteBlock: %w", err)
	}
	raw, err := h.GetSyncKV(LatestBlockMarkerKey)
	if err != nil {
		return 0, fmt.Errorf("GetLatestDataCompleteBlock: %w", err)
	}
	var marker uint64
	if raw != nil {
		v, perr := strconv.ParseUint(string(raw), 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("GetLatestDataCompleteBlock: parse marker %q: %w", string(raw), perr)
		}
		marker = v
	}

	// D-858 review B4: the latest_block marker write is NON-FATAL
	// (UpdateLatestBlockMonotonic failures are logged and swallowed on all apply
	// paths), so the marker can transiently LAG the truly-applied state. With the
	// zero-tolerance vote gate that would falsely reject the next height as
	// non-contiguous until reconcile heals it. The applied-anchor
	// (AdvanceAppliedAnchorContiguous) is a second, independent "highest contiguously
	// applied block" signal; take the MAX of the two so a lagging marker cannot cause
	// a false reject. This is a tighter lower bound, NOT a tolerance window — it never
	// admits a height ABOVE the applied tip, so the gap guard (reject N != tip+1 /
	// N <= tip) is preserved and the 844-without-843 class stays closed. A missing
	// anchor (never seeded) contributes 0.
	tip := marker
	if anchor, ok, aerr := GetAppliedAnchor(nil); aerr == nil && ok && anchor > tip {
		tip = anchor
	}
	return tip, nil
}
