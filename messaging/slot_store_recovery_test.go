package messaging

// Tests for slot recovery at startup and after catch-up
// (RecoverSlotStoreAtStartup / EnsureSlotStoreRecovered / SlotStoreReady).
// The slot is the block number, so recovery only records the committed tip;
// what remains to test is the getTip contract, the readiness gate, and that
// catch-up always moves the slot up to the new tip.

import (
	"errors"
	"fmt"
	"testing"

	"gossipnode/config"
	"gossipnode/internal/roundlock"
)

// resetSlotStoreForRecoveryTest swaps in fresh process-wide stores that
// recovery writes to (slot store, signing ledger, proposer periods) and clears
// the readiness gate for the duration of a test.
func resetSlotStoreForRecoveryTest(t *testing.T) {
	t.Helper()
	savedSlots, savedPeriods, savedLedger := DefaultSlotStore, DefaultProposerPeriods, roundlock.Default
	DefaultSlotStore, DefaultProposerPeriods, roundlock.Default = NewSlotStore(), NewProposerPeriods(), roundlock.NewLedger()
	wasReady := SlotStoreReady()
	ResetSlotStoreReadyForTest()
	t.Cleanup(func() {
		DefaultSlotStore, DefaultProposerPeriods, roundlock.Default = savedSlots, savedPeriods, savedLedger
		if wasReady {
			MarkSlotStoreReady()
		} else {
			ResetSlotStoreReadyForTest()
		}
	})
}

func TestRecoverSlotStoreAtStartup_RecordsTheTipHeight(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	tip := &config.ZKBlock{BlockNumber: 45, Slot: 45}
	if err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return tip, nil }); err != nil {
		t.Fatalf("RecoverSlotStoreAtStartup: unexpected error: %v", err)
	}
	if !SlotStoreReady() {
		t.Fatal("expected SlotStoreReady() after a successful recovery")
	}
	if got := DefaultSlotStore.Current(); got != 45 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want 45", got)
	}
	if tip, ok := roundlock.Default.CommittedTip(); !ok || tip != 45 {
		t.Fatalf("signing ledger committed tip = %d %v, want 45 (recovery arms the ledger too)", tip, ok)
	}
	if next, advanced := DefaultSlotStore.AdvanceOnCommit(46); !advanced || next != 46 {
		t.Fatalf("next commit: advanced=%v slot=%d, want true,46", advanced, next)
	}
}

// A tip written before slots were persisted (Slot 0 at a real height) used to
// refuse startup unless JMDN_SLOT_RECOVERY_PRESLOT_MIGRATION=1. The slot is
// the height, so it now recovers like any other tip, with no flag.
func TestRecoverSlotStoreAtStartup_TipWithoutStoredSlotRecovers(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	tip := &config.ZKBlock{BlockNumber: 100, Slot: 0, Period: 0}
	if err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return tip, nil }); err != nil {
		t.Fatalf("a tip without a stored slot must recover: %v", err)
	}
	if !SlotStoreReady() {
		t.Fatal("expected SlotStoreReady()")
	}
	if got := DefaultSlotStore.Current(); got != 100 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want 100 (the tip height)", got)
	}
}

// A tip stamped under the old rule (slot ahead of height after a failed round)
// is flagged, but still recovers to its height: the warning is for the
// operator, not a startup refusal.
func TestRecoverSlotStoreAtStartup_MisalignedTipWarnsButRecovers(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	tip := &config.ZKBlock{BlockNumber: 100, Slot: 102, Period: 0}
	if !tipSlotMisaligned(tip) {
		t.Fatal("tipSlotMisaligned = false for slot 102 at height 100, want true")
	}
	if err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return tip, nil }); err != nil {
		t.Fatalf("a misaligned tip must still recover: %v", err)
	}
	if !SlotStoreReady() {
		t.Fatal("expected SlotStoreReady()")
	}
	if got := DefaultSlotStore.Current(); got != 100 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want 100 (the tip height, not the stored slot)", got)
	}
}

func TestTipSlotMisaligned(t *testing.T) {
	cases := []struct {
		height, slot uint64
		want         bool
	}{
		{100, 100, false}, // aligned
		{100, 0, false},   // written before slots were persisted
		{0, 0, false},     // genesis
		{100, 102, true},  // old rule after failed rounds
		{100, 99, true},
	}
	for _, c := range cases {
		if got := tipSlotMisaligned(&config.ZKBlock{BlockNumber: c.height, Slot: c.slot}); got != c.want {
			t.Errorf("tipSlotMisaligned(height %d, slot %d) = %v, want %v", c.height, c.slot, got, c.want)
		}
	}
}

func TestRecoverSlotStoreAtStartup_EmptyChainIsReadyWithNothingRecorded(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) {
		return nil, fmt.Errorf("wrapped: %w", ErrNoCommittedBlock)
	})
	if err != nil {
		t.Fatalf("expected nil error on an empty chain, got %v", err)
	}
	if !SlotStoreReady() {
		t.Fatal("expected SlotStoreReady() on a legitimately empty chain")
	}
	if _, ok := roundlock.Default.CommittedTip(); ok {
		t.Fatal("an empty chain must not record a committed tip")
	}
}

func TestRecoverSlotStoreAtStartup_ReadFailureFailsClosed(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	readErr := errors.New("db unreachable")
	err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return nil, readErr })
	if err == nil {
		t.Fatal("expected an error on a genuine read failure")
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("expected the error to wrap the read error, got: %v", err)
	}
	if SlotStoreReady() {
		t.Fatal("SlotStoreReady() must stay false after a failed recovery")
	}
}

// Recovery after live commits have started can only move the store up, never
// rewind it: recording is order-independent.
func TestRecoverSlotStoreAtStartup_NeverRewindsALiveStore(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)
	NoteHeightCommitted(500)

	stale := &config.ZKBlock{BlockNumber: 400, Slot: 400}
	if err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return stale, nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := DefaultSlotStore.Current(); got != 500 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want 500 (a stale tip read must not rewind)", got)
	}
}

// The stale-slot bug: a node that stays running, falls behind and catches up
// in bulk (writes that bypass the live commit hooks) used to keep its old slot,
// because the catch-up re-seed was skipped once the store was live. Now the
// catch-up re-read moves it to the new tip.
func TestEnsureSlotStoreRecovered_RunningNodeCatchesUpToTheTip(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	// Running and live at height 19,900.
	if err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) {
		return &config.ZKBlock{BlockNumber: 19_900, Slot: 19_900}, nil
	}); err != nil {
		t.Fatal(err)
	}
	NoteHeightCommitted(19_901)

	// Falls behind; bulk catch-up writes heights up to 20,000 without hooks.
	tip := &config.ZKBlock{BlockNumber: 20_000, Slot: 20_000}
	if err := EnsureSlotStoreRecovered(func() (*config.ZKBlock, error) { return tip, nil }); err != nil {
		t.Fatalf("post-catch-up recovery: %v", err)
	}
	if got := DefaultSlotStore.Current(); got != 20_000 {
		t.Fatalf("DefaultSlotStore.Current() after catch-up = %d, want 20000", got)
	}
	if got, _ := roundlock.Default.CommittedTip(); got != 20_000 {
		t.Fatalf("signing ledger tip after catch-up = %d, want 20000", got)
	}
	if got := LiveSlotFor(20_001); got != 20_001 {
		t.Fatalf("next block's slot = %d, want 20001", got)
	}
}

// A node that started empty and then synced its whole chain.
func TestEnsureSlotStoreRecovered_EmptyThenSynced(t *testing.T) {
	resetSlotStoreForRecoveryTest(t)

	if err := RecoverSlotStoreAtStartup(func() (*config.ZKBlock, error) { return nil, ErrNoCommittedBlock }); err != nil {
		t.Fatal(err)
	}
	tip := &config.ZKBlock{BlockNumber: 10_000, Slot: 10_000}
	if err := EnsureSlotStoreRecovered(func() (*config.ZKBlock, error) { return tip, nil }); err != nil {
		t.Fatalf("post-catch-up recovery: %v", err)
	}
	if got := DefaultSlotStore.Current(); got != 10_000 {
		t.Fatalf("DefaultSlotStore.Current() = %d, want 10000", got)
	}
}
