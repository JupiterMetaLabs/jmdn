package roundlock

import (
	"sync"
	"testing"
)

func TestTryLockFresh_SameBlockIsIdempotent(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 0}
	ok, _, _, fresh := l.TryLockFresh(r, "A")
	if !ok || !fresh {
		t.Fatalf("first lock: ok=%v fresh=%v, want true,true", ok, fresh)
	}
	ok, refusal, _, fresh := l.TryLockFresh(r, "A")
	if !ok || fresh || refusal != None {
		t.Fatalf("retry of the same block: ok=%v fresh=%v refusal=%v, want true,false,none", ok, fresh, refusal)
	}
}

// R1: one block per (height, period).
func TestTryLockFresh_DifferentBlockSameRoundIsRefused(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 0}
	l.TryLockFresh(r, "A")
	ok, refusal, highest, _ := l.TryLockFresh(r, "B")
	if ok || refusal != Conflict || highest != 0 {
		t.Fatalf("second block in one round: ok=%v refusal=%v highest=%d, want false,conflict,0", ok, refusal, highest)
	}
	if got, _ := l.Signed(r); got != "A" {
		t.Fatalf("refusal must not overwrite the signed block, got %q", got)
	}
}

// A retry at the next period is a new round, even for a different block.
func TestTryLockFresh_NextPeriodIsANewRound(t *testing.T) {
	l := NewLedger()
	l.TryLockFresh(Round{Height: 10, Period: 0}, "A")
	ok, _, highest, fresh := l.TryLockFresh(Round{Height: 10, Period: 1}, "B")
	if !ok || !fresh || highest != 1 {
		t.Fatalf("next period: ok=%v fresh=%v highest=%d, want true,true,1", ok, fresh, highest)
	}
}

// R2: never sign a lower period of a height once a higher one is signed.
func TestTryLockFresh_LowerPeriodIsRefusedAsStale(t *testing.T) {
	l := NewLedger()
	l.TryLockFresh(Round{Height: 10, Period: 2}, "C")
	ok, refusal, highest, _ := l.TryLockFresh(Round{Height: 10, Period: 1}, "B")
	if ok || refusal != Stale || highest != 2 {
		t.Fatalf("lower period: ok=%v refusal=%v highest=%d, want false,stale,2", ok, refusal, highest)
	}
	// The same rule does not reach other heights.
	if ok, _, _, _ := l.TryLockFresh(Round{Height: 11, Period: 0}, "D"); !ok {
		t.Fatalf("a different height must be independent")
	}
}

// A node that missed earlier periods (offline, restarted) may sign whatever
// period it is first asked for: no record is no information.
func TestTryLockFresh_NoRecordAcceptsAnyPeriod(t *testing.T) {
	l := NewLedger()
	if ok, _, highest, _ := l.TryLockFresh(Round{Height: 10, Period: 3}, "X"); !ok || highest != 3 {
		t.Fatalf("first sight of a height at period 3: ok=%v highest=%d, want true,3", ok, highest)
	}
}

func TestTryLockFresh_ConcurrentDifferentBlocksExactlyOneWins(t *testing.T) {
	for trial := 0; trial < 200; trial++ {
		l := NewLedger()
		r := Round{Height: uint64(trial), Period: 0}
		var wg sync.WaitGroup
		results := make([]bool, 2)
		for i, b := range []string{"A", "B"} {
			wg.Add(1)
			go func(i int, b string) {
				defer wg.Done()
				results[i], _, _, _ = l.TryLockFresh(r, b)
			}(i, b)
		}
		wg.Wait()
		if results[0] == results[1] {
			t.Fatalf("trial %d: exactly one block must win, got A=%v B=%v", trial, results[0], results[1])
		}
	}
}

// A request at a bogus huge height must not clear the live height's records:
// pruning follows commits only.
func TestTryLockFresh_HugeHeightDoesNotClearLiveRecords(t *testing.T) {
	l := NewLedger()
	l.ForgetThrough(99)
	live := Round{Height: 100, Period: 0}
	l.TryLockFresh(live, "A")
	l.TryLockFresh(Round{Height: 1 << 62, Period: 0}, "junk")
	if got, ok := l.Signed(live); !ok || got != "A" {
		t.Fatalf("live record lost after a huge-height request: %q %v", got, ok)
	}
	if ok, refusal, _, _ := l.TryLockFresh(live, "B"); ok || refusal != Conflict {
		t.Fatalf("live round must stay closed to a second block: ok=%v refusal=%v", ok, refusal)
	}
}

// Once a height commits, no block at or below it is signed again.
func TestTryLockFresh_RefusesAtOrBelowCommittedTip(t *testing.T) {
	l := NewLedger()
	l.TryLockFresh(Round{Height: 100, Period: 0}, "A")
	l.ForgetThrough(100)
	for _, h := range []uint64{100, 99, 1} {
		ok, refusal, _, _ := l.TryLockFresh(Round{Height: h, Period: 5}, "B")
		if ok || refusal != Committed {
			t.Fatalf("height %d (tip 100): ok=%v refusal=%v, want false,height_committed", h, ok, refusal)
		}
	}
	if ok, _, _, _ := l.TryLockFresh(Round{Height: 101, Period: 0}, "C"); !ok {
		t.Fatalf("the next height must be signable")
	}
}

func TestForgetThrough_KeepsMarginAndDropsOlder(t *testing.T) {
	l := NewLedger()
	for h := uint64(1); h <= 100; h++ {
		l.TryLockFresh(Round{Height: h, Period: 0}, "A")
	}
	l.ForgetThrough(100)
	if _, ok := l.Signed(Round{Height: 100 - commitMargin, Period: 0}); ok {
		t.Fatalf("a record commitMargin below the tip must be dropped")
	}
	if _, ok := l.Highest(100 - commitMargin); ok {
		t.Fatalf("its highest-period record must be dropped too")
	}
	if _, ok := l.Signed(Round{Height: 100 - commitMargin + 1, Period: 0}); !ok {
		t.Fatalf("records inside the margin must be kept")
	}
}

func TestForgetThrough_NeverMovesBackwards(t *testing.T) {
	l := NewLedger()
	l.ForgetThrough(50)
	l.ForgetThrough(40)
	if tip, ok := l.CommittedTip(); !ok || tip != 50 {
		t.Fatalf("committed tip %d %v, want 50", tip, ok)
	}
	if ok, _, _, _ := l.TryLockFresh(Round{Height: 45, Period: 0}, "A"); ok {
		t.Fatalf("height 45 is below the tip and must be refused")
	}
}

func TestForgetThrough_SmallTipDropsNothing(t *testing.T) {
	l := NewLedger()
	l.TryLockFresh(Round{Height: 3, Period: 0}, "A")
	l.ForgetThrough(2)
	if _, ok := l.Signed(Round{Height: 3, Period: 0}); !ok {
		t.Fatalf("a tip below commitMargin must not underflow into dropping records")
	}
}

// F-3: a fresh reservation whose sign failed is released, so the round (and
// any lower period R2 would otherwise refuse) is signable again.
func TestRelease_FreshReservationFreesTheRound(t *testing.T) {
	l := NewLedger()
	l.TryLockFresh(Round{Height: 10, Period: 0}, "A")
	r := Round{Height: 10, Period: 1}
	_, _, _, fresh := l.TryLockFresh(r, "B")
	if !fresh {
		t.Fatalf("precondition: reservation must be fresh")
	}
	l.Release(r, "B")
	if _, ok := l.Signed(r); ok {
		t.Fatalf("released round must be empty")
	}
	if high, _ := l.Highest(10); high != 0 {
		t.Fatalf("highest must fall back to the remaining signed period 0, got %d", high)
	}
	if ok, _, _, _ := l.TryLockFresh(r, "C"); !ok {
		t.Fatalf("after release a different block may be signed for the round")
	}
}

func TestRelease_LastRoundOfHeightClearsHighest(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 4}
	l.TryLockFresh(r, "A")
	l.Release(r, "A")
	if _, ok := l.Highest(10); ok {
		t.Fatalf("no signed round left: highest must be cleared")
	}
	if ok, _, _, _ := l.TryLockFresh(Round{Height: 10, Period: 0}, "B"); !ok {
		t.Fatalf("with nothing signed, a lower period must not be refused as stale")
	}
}

func TestRelease_OnlyClearsTheMatchingBlock(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 0}
	l.TryLockFresh(r, "A")
	l.Release(r, "B")
	if got, _ := l.Signed(r); got != "A" {
		t.Fatalf("release of a different block must be a no-op, round now holds %q", got)
	}
}

func TestRelease_NoOpOnAnUnlockedRound(t *testing.T) {
	l := NewLedger()
	l.Release(Round{Height: 10, Period: 0}, "A")
	if _, ok := l.Signed(Round{Height: 10, Period: 0}); ok {
		t.Fatalf("release of an unlocked round must not create an entry")
	}
}

// F-9: a re-entry (fresh=false) must not release, otherwise a retry whose sign
// fails would reopen a round an earlier attempt already signed.
func TestF9_ReentryIsNotFresh(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 0}
	l.TryLockFresh(r, "A") // earlier attempt: signed and shipped
	_, _, _, fresh := l.TryLockFresh(r, "A")
	if fresh {
		t.Fatalf("a re-entry must report fresh=false so its caller does not release")
	}
	if ok, refusal, _, _ := l.TryLockFresh(r, "B"); ok || refusal != Conflict {
		t.Fatalf("round must stay closed to a second block: ok=%v refusal=%v", ok, refusal)
	}
}

func TestRelease_ConcurrentWithTryLock_NoRace(t *testing.T) {
	l := NewLedger()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		r := Round{Height: uint64(i % 5), Period: uint64(i % 3)}
		go func() { defer wg.Done(); l.TryLockFresh(r, "A") }()
		go func() { defer wg.Done(); l.Release(r, "A") }()
	}
	wg.Wait()
}
