package messaging

// Committed-slot high-water mark and entropy-pool finality.
//
// The entropy committee's pool for epoch e is "the newest committee anchor at
// or before e's freeze cutoff slot". That answer is only final once no block
// can still be committed at a slot <= the cutoff; before that a newer
// qualifying anchor may still arrive. These helpers let entropyAnchoredPool
// check that, and fail closed (ErrEntropyPoolNotFinal) instead of guessing.

import (
	"errors"
	"sync/atomic"
)

// ErrEntropyPoolNotFinal is returned when the entropy committee's pool for an
// epoch is asked for before the chain can no longer change it.
var ErrEntropyPoolNotFinal = errors.New("messaging: entropy committee pool is not final yet (fail closed)")

// ---------------------------------------------------------------------------
// Committed-slot high-water mark
// ---------------------------------------------------------------------------

var committedSlotHW atomic.Uint64

// noteCommittedSlot records that a block at slot has been committed locally.
func noteCommittedSlot(slot uint64) {
	for {
		cur := committedSlotHW.Load()
		if slot <= cur || committedSlotHW.CompareAndSwap(cur, slot) {
			return
		}
	}
}

// committedSlotHighWater is the highest slot this node has committed, from the
// entropy path or the (startup-seeded) slot store, whichever is further.
func committedSlotHighWater() uint64 {
	hw := committedSlotHW.Load()
	if cur := DefaultSlotStore.Current(); cur > hw {
		return cur
	}
	return hw
}

// nextPossibleSlotFn returns the lowest slot any block still to be committed
// after tip can occupy: committed slot + the next height's certified timeouts
// + 1 (§7.1: slot only grows). Seam for tests.
var nextPossibleSlotFn = func(tip uint64) uint64 {
	return committedSlotHighWater() + DefaultPeriodStore.PeriodFor(tip+1) + 1
}

// EntropyBoundaryCommitted reports whether this node has already committed a
// block at or past forEpoch's boundary slot, i.e. the block that carries
// ENTROPY-forEpoch's proof is behind us and no proof is needed to build it.
func EntropyBoundaryCommitted(forEpoch uint64) bool {
	return committedSlotHighWater() >= EpochBoundarySlot(forEpoch)
}
