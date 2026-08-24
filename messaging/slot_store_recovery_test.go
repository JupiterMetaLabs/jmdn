package messaging

// Tests for the slot-restart fail-closed recovery wiring
// (docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8's "remaining integration
// step" — RecoverSlotStoreAtStartup/EnsureSlotStoreRecovered/SlotStoreReady).
// SeedFromCommittedTip itself is already tested in slot_store_reseed_test.go
// (fresh-store adopt, refuse-once-live, correct subsequent advance, no
// regression on stale height) — these tests cover the layer ABOVE it: the
// getTip contract, the readiness gate, and the "must fail closed, never
// silently permit" property the whole finding was about.

import (
	"errors"
	"fmt"
	"testing"

	"gossipnode/config"
)

// resetSlotStoreForRecoveryTest swaps in a fresh DefaultSlotStore and clears
// the readiness gate for the duration of a test — same pattern
// Block/consensus_fields_test.go's resetSlotAndPeriodStores uses, since
// DefaultSlotStore and the readiness gate are both process-wide singletons
// other tests may depend on.
func resetSlotStoreForRecoveryTest(t *testing.T) {
	t.Helper()
	saved := DefaultSlotStore
	DefaultSlotStore = NewSlotStore()
	wasReady := SlotStoreReady()
	ResetSlotStoreReadyForTest()
	t.Cleanup(func() {
		DefaultSlotStore = saved
		if wasReady {
			MarkSlotStoreReady()
		} else {
			ResetSlotStoreReadyForTest()
		}
	})
}

func TestRecoverSlotStoreAtStartup_SeedsFromRealTip(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	tip := &config.ZKBlock{BlockNumber: 45, Slot: 123, Period: 0}
	err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return tip, nil })
	if err != nil {
		t.Fatalf("RecoverSlotStoreAtStartup: unexpected error: %v", err)
	}
	if !SlotStoreReady() {
		t.Fatal("expected SlotStoreReady() to be true after a successful recovery")
	}
	if got := DefaultSlotStore.Current(); got != 123 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want 123 (the tip's own Slot)", got)
	}

	// The next live commit must build on the recovered value, not restart
	// from zero — this is the entire point of the fix.
	next, advanced := DefaultSlotStore.AdvanceOnCommit(46, 0)
	if !advanced {
		t.Fatal("expected AdvanceOnCommit(46, 0) to advance after a fresh height")
	}
	if next != 124 {
		t.Fatalf("DefaultSlotStore after next commit = %d, want 124 (123 + period(0) + 1)", next)
	}
}

func TestRecoverSlotStoreAtStartup_EmptyChainIsReadyButUnseeded(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) {
		return nil, fmt.Errorf("wrapped: %w", ErrNoCommittedBlock)
	})
	if err != nil {
		t.Fatalf("RecoverSlotStoreAtStartup: expected nil error on empty chain, got %v", err)
	}
	if !SlotStoreReady() {
		t.Fatal("expected SlotStoreReady() to be true on a legitimately empty chain")
	}
	// Must NOT have flipped haveCommitted — a later real tip (this node's own
	// first commit, or a fast-sync catch-up) must still be able to seed it.
	if DefaultSlotStore.haveCommittedForRecoveryCheck() {
		t.Fatal("empty-chain recovery must not mark DefaultSlotStore live — it must remain seedable later")
	}
}

func TestRecoverSlotStoreAtStartup_ReadFailureFailsClosed(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	readErr := errors.New("db unreachable")
	err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return nil, readErr })
	if err == nil {
		t.Fatal("expected RecoverSlotStoreAtStartup to return an error on a genuine read failure")
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("expected the returned error to wrap the underlying read error, got: %v", err)
	}
	if SlotStoreReady() {
		t.Fatal("expected SlotStoreReady() to remain false after a failed recovery — must fail closed")
	}
}

func TestRecoverSlotStoreAtStartup_RefusesTipWithNoPersistedSlotAtRealHeight(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	// BlockNumber > 0 with Slot==0 && Period==0 can only mean the persistence
	// fix predates this block, or the read-back conversion broke — either
	// way, silently adopting slot=0 here is exactly the original bug.
	tip := &config.ZKBlock{BlockNumber: 100, Slot: 0, Period: 0}
	err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return tip, nil })
	if err == nil {
		t.Fatal("expected RecoverSlotStoreAtStartup to refuse a real-height tip with no persisted slot/period")
	}
	if SlotStoreReady() {
		t.Fatal("expected SlotStoreReady() to remain false when refusing an unrecoverable tip")
	}
}

func TestRecoverSlotStoreAtStartup_RefusesOnceStoreAlreadyLive(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)
	// Simulate a live commit having already happened (the exact race this
	// function's docs say must never occur if called at the right time —
	// verified here as "refuses safely" rather than "corrupts state" in case
	// some future caller gets the ordering wrong).
	DefaultSlotStore.AdvanceOnCommit(1, 0)

	tip := &config.ZKBlock{BlockNumber: 999, Slot: 999, Period: 0}
	err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return tip, nil })
	if err == nil {
		t.Fatal("expected RecoverSlotStoreAtStartup to refuse once DefaultSlotStore is already live")
	}
	if got := DefaultSlotStore.Current(); got != 1 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want unchanged 1 — a refused recovery must not clobber live progress", got)
	}
}

func TestEnsureSlotStoreRecovered_NoOpsOnceLive(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)
	DefaultSlotStore.AdvanceOnCommit(1, 0)
	MarkSlotStoreReady()

	called := false
	err := EnsureSlotStoreRecovered(func() (*config.ZKBlock, error) {
		called = true
		return &config.ZKBlock{BlockNumber: 500, Slot: 500}, nil
	})
	if err != nil {
		t.Fatalf("EnsureSlotStoreRecovered: unexpected error: %v", err)
	}
	if called {
		t.Fatal("EnsureSlotStoreRecovered must not call getTip once the store is already live")
	}
	if got := DefaultSlotStore.Current(); got != 1 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want unchanged 1", got)
	}
}

// TestEnsureSlotStoreRecovered_SeedsAfterFastSyncCatchesUp is the item-5
// scenario from docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8: a node starts
// with an empty local chain (recovery marks it ready-but-unseeded, per
// TestRecoverSlotStoreAtStartup_EmptyChainIsReadyButUnseeded above), then
// bulk fast-sync catches it up — which bypasses the live commit hooks
// entirely, so DefaultSlotStore is still unseeded — and a second call, using
// the now-populated tip, must finally seed it correctly.
func TestEnsureSlotStoreRecovered_SeedsAfterFastSyncCatchesUp(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	// Step 1: startup, chain still empty.
	if err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) {
		return nil, ErrNoCommittedBlock
	}); err != nil {
		t.Fatalf("initial (empty-chain) recovery: unexpected error: %v", err)
	}
	if got := DefaultSlotStore.Current(); got != 0 {
		t.Fatalf("DefaultSlotStore.Current() before catch-up = %d, want 0", got)
	}

	// Step 2: fast-sync "happens" (bulk writes bypass AdvanceOnCommit, so
	// haveCommitted is still false — nothing to simulate on DefaultSlotStore
	// itself); the local tip now has real history.
	tip := &config.ZKBlock{BlockNumber: 10_000, Slot: 480_000, Period: 0}
	if err := EnsureSlotStoreRecovered(func() (*config.ZKBlock, error) { return tip, nil }); err != nil {
		t.Fatalf("post-catch-up recovery: unexpected error: %v", err)
	}
	if got := DefaultSlotStore.Current(); got != 480_000 {
		t.Fatalf("DefaultSlotStore.Current() after catch-up = %d, want 480000 (seeded from the real tip)", got)
	}
	if !SlotStoreReady() {
		t.Fatal("expected SlotStoreReady() to be true after the post-catch-up seed")
	}
}
