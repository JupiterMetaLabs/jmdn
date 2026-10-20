package MessagePassing

import (
	"errors"
	"testing"

	"gossipnode/config"
	"gossipnode/internal/roundlock"
)

func withFreshRoundLedger(t *testing.T) *roundlock.Ledger {
	t.Helper()
	prev := roundLedger
	l := roundlock.NewLedger()
	roundLedger = l
	t.Cleanup(func() { roundLedger = prev })
	return l
}

func refusalOf(t *testing.T, err error) *roundRefusal {
	t.Helper()
	var rr *roundRefusal
	if !errors.As(err, &rr) {
		t.Fatalf("want a *roundRefusal, got %v", err)
	}
	return rr
}

// R1: a buddy signs one block per round; a retry of the same block is allowed.
func TestLockBlockResultRound_OneBlockPerRound(t *testing.T) {
	withFreshRoundLedger(t)
	if _, err := lockBlockResultRound(848, 0, "0xaa", "0xc0"); err != nil {
		t.Fatalf("first block: %v", err)
	}
	if fresh, err := lockBlockResultRound(848, 0, "0xaa", "0xc0"); err != nil || fresh {
		t.Fatalf("retried request for the same block: fresh=%v err=%v, want false,nil", fresh, err)
	}
	rr := refusalOf(t, func() error { _, err := lockBlockResultRound(848, 0, "0xbb", "0xc1"); return err }())
	if rr.Reason != roundlock.Conflict || rr.Highest != 0 {
		t.Fatalf("second block in the round: reason=%v highest=%d, want conflict,0", rr.Reason, rr.Highest)
	}
}

// The same transactions at a new period share a BlockHash but not a
// ConsensusHash; that is a new round and must be signable.
func TestLockBlockResultRound_SameTxsNextPeriodIsANewRound(t *testing.T) {
	withFreshRoundLedger(t)
	if _, err := lockBlockResultRound(848, 0, "0xaa", "0xc0"); err != nil {
		t.Fatal(err)
	}
	if _, err := lockBlockResultRound(848, 1, "0xaa", "0xc1"); err != nil {
		t.Fatalf("same transactions at the next period: %v", err)
	}
}

// R2: never sign backwards, and report the highest period for the sequencer.
func TestLockBlockResultRound_StalePeriodReportsHighest(t *testing.T) {
	withFreshRoundLedger(t)
	if _, err := lockBlockResultRound(900, 2, "0xaa", "0xc2"); err != nil {
		t.Fatal(err)
	}
	rr := refusalOf(t, func() error { _, err := lockBlockResultRound(900, 0, "0xbb", "0xc0"); return err }())
	if rr.Reason != roundlock.Stale || rr.Highest != 2 {
		t.Fatalf("stale period: reason=%v highest=%d, want stale_period,2", rr.Reason, rr.Highest)
	}
}

func TestRoundLedger_DefaultIsTheProcessLedger(t *testing.T) {
	if roundLedger != roundlock.Default {
		t.Fatalf("production must use roundlock.Default")
	}
}

// F-3: a failed sign releases its fresh reservation, so the node can still
// sign a block for the round.
func TestUnlockBlockResultRound_FreesTheRoundAfterAFailedSign(t *testing.T) {
	withFreshRoundLedger(t)
	fresh, err := lockBlockResultRound(848, 0, "0xaa", "0xc0")
	if err != nil || !fresh {
		t.Fatalf("precondition: fresh=%v err=%v", fresh, err)
	}
	unlockBlockResultRound(848, 0, "0xaa", "0xc0")
	if _, err := lockBlockResultRound(848, 0, "0xbb", "0xc1"); err != nil {
		t.Fatalf("after a failed sign the round must be signable again: %v", err)
	}
}

// F-9: a retry whose sign fails must not reopen a round an earlier attempt
// already signed - its reservation is not fresh, so the caller never unlocks.
func TestF9_RetryFailureMustNotReopenTheRound(t *testing.T) {
	withFreshRoundLedger(t)
	if _, err := lockBlockResultRound(848, 0, "0xaa", "0xc0"); err != nil {
		t.Fatal(err)
	}
	fresh, err := lockBlockResultRound(848, 0, "0xaa", "0xc0")
	if err != nil || fresh {
		t.Fatalf("retry: fresh=%v err=%v, want false,nil", fresh, err)
	}
	// The caller sees fresh=false and does not unlock; a second block stays refused.
	if _, err := lockBlockResultRound(848, 0, "0xbb", "0xc1"); err == nil {
		t.Fatalf("round must remain closed to a different block")
	}
}

// A buddy never signs at or below its committed tip, even a block it has no
// record of (the records for old heights are pruned on commit).
func TestLockBlockResultRound_RefusesCommittedHeight(t *testing.T) {
	l := withFreshRoundLedger(t)
	l.ForgetThrough(848)
	rr := refusalOf(t, func() error { _, err := lockBlockResultRound(848, 0, "0xaa", "0xc0"); return err }())
	if rr.Reason != roundlock.Committed {
		t.Fatalf("height at the committed tip: reason=%v, want height_committed", rr.Reason)
	}
	if _, err := lockBlockResultRound(849, 0, "0xaa", "0xc0"); err != nil {
		t.Fatalf("the next height must be signable: %v", err)
	}
}

// A buddy never signs a block above the Period cap, and the refusal reserves
// nothing.
func TestLockBlockResultRound_RefusesAboveThePeriodCap(t *testing.T) {
	l := withFreshRoundLedger(t)
	if _, err := lockBlockResultRound(848, config.MaxPeriod+1, "0xaa", "0xc0"); err == nil {
		t.Fatal("a buddy must not sign above config.MaxPeriod")
	}
	if _, ok := l.Highest(848); ok {
		t.Fatal("a refused over-cap request must not be recorded")
	}
	if _, err := lockBlockResultRound(848, config.MaxPeriod, "0xaa", "0xc0"); err != nil {
		t.Fatalf("a block at the cap must be signable: %v", err)
	}
}
