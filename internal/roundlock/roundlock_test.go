package roundlock

import (
	"sync"
	"testing"
)

func TestTryLock_BlockThenTimeoutIsRefused(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 848, Period: 0}
	if ok, _ := l.TryLock(r, Block); !ok {
		t.Fatalf("first signature for a round must be allowed")
	}
	ok, taken := l.TryLock(r, Timeout)
	if ok || taken != Block {
		t.Fatalf("timeout after block for the same round must be refused, got ok=%v taken=%v", ok, taken)
	}
}

func TestTryLock_TimeoutThenBlockIsRefused(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 848, Period: 2}
	if ok, _ := l.TryLock(r, Timeout); !ok {
		t.Fatalf("first signature for a round must be allowed")
	}
	ok, taken := l.TryLock(r, Block)
	if ok || taken != Timeout {
		t.Fatalf("block after timeout for the same round must be refused, got ok=%v taken=%v", ok, taken)
	}
}

func TestTryLock_SameSideIsIdempotent(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 1}
	for i := 0; i < 3; i++ {
		if ok, _ := l.TryLock(r, Block); !ok {
			t.Fatalf("retrying the side already taken must stay allowed (attempt %d)", i)
		}
	}
}

func TestTryLock_DifferentRoundsAreIndependent(t *testing.T) {
	l := NewLedger()
	if ok, _ := l.TryLock(Round{Height: 848, Period: 0}, Timeout); !ok {
		t.Fatal("lock p0")
	}
	// Timing out period 0 must not stop signing the block at period 1.
	if ok, _ := l.TryLock(Round{Height: 848, Period: 1}, Block); !ok {
		t.Fatalf("a block result for the NEXT period must be allowed after timing out the previous one")
	}
	if ok, _ := l.TryLock(Round{Height: 849, Period: 0}, Block); !ok {
		t.Fatalf("a different height must be independent")
	}
}

func TestTryLock_ConcurrentOppositeSidesExactlyOneWins(t *testing.T) {
	for trial := 0; trial < 200; trial++ {
		l := NewLedger()
		r := Round{Height: uint64(trial), Period: 0}
		var wg sync.WaitGroup
		results := make([]bool, 2)
		for i, side := range []Side{Block, Timeout} {
			wg.Add(1)
			go func(i int, s Side) {
				defer wg.Done()
				results[i], _ = l.TryLock(r, s)
			}(i, side)
		}
		wg.Wait()
		if results[0] == results[1] {
			t.Fatalf("trial %d: exactly one side must win, got block=%v timeout=%v", trial, results[0], results[1])
		}
	}
}

func TestTryLock_PrunesOldRoundsButKeepsRecent(t *testing.T) {
	l := NewLedger()
	l.TryLock(Round{Height: 1, Period: 0}, Block)
	l.TryLock(Round{Height: 5000, Period: 0}, Block)
	if _, ok := l.Signed(Round{Height: 1, Period: 0}); ok {
		t.Fatalf("a round far below the tip should be pruned")
	}
	l.TryLock(Round{Height: 5000 - retainHeights + 1, Period: 0}, Timeout)
	if _, ok := l.Signed(Round{Height: 5000 - retainHeights + 1, Period: 0}); !ok {
		t.Fatalf("a round inside the retention window must be kept")
	}
}

// ---------------------------------------------------------------------------
// F-3 - "lock-then-fail stranding": TryLock reserves a side on INTENT, before
// the caller has actually produced a signature. A caller whose sign attempt
// then fails must release the reservation via Release, or the round is
// stranded - permanently refused on BOTH sides for the life of the process.
// ---------------------------------------------------------------------------

// TestRelease_FreesTheRoundForTheOtherSide reproduces the production defect
// end to end and proves the fix: a node reserves Block, its BLS signing
// attempt fails (simulated - nothing here assumes WHY it failed), it calls
// Release, and the round must then be exactly as if the failed attempt never
// happened - free for either side, not just the one that failed.
func TestRelease_FreesTheRoundForTheOtherSide(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 848, Period: 0}

	if ok, _ := l.TryLock(r, Block); !ok {
		t.Fatalf("setup: the first reservation for a round must be allowed")
	}

	// Before the fix: this is where the story ends. TryLock(r, Timeout) below
	// would report ok=false, taken=Block, forever - even though no block
	// signature was ever actually produced. Confirm that failure mode is real
	// under these settings before proving the fix removes it.
	if ok, taken := l.TryLock(r, Timeout); ok || taken != Block {
		t.Fatalf("precondition failed: expected the stranding defect to reproduce (ok=false, taken=Block), got ok=%v taken=%v", ok, taken)
	}

	// The fix: the caller whose Block sign attempt failed releases its own
	// reservation instead of leaving it in place.
	l.Release(r, Block)

	if side, signed := l.Signed(r); signed {
		t.Fatalf("round must be completely unlocked after Release, got %v", side)
	}
	if ok, _ := l.TryLock(r, Timeout); !ok {
		t.Fatalf("fixed: the round must now accept a timeout vote, since the block side was never actually signed")
	}
}

// TestRelease_SymmetricForTimeoutSide is the mirror of the above: a failed
// TIMEOUT sign attempt (messaging/timeout_gossip.go, messaging/
// timeout_request.go) must free the round for a block result.
func TestRelease_SymmetricForTimeoutSide(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 900, Period: 3}

	if ok, _ := l.TryLock(r, Timeout); !ok {
		t.Fatalf("setup: the first reservation for a round must be allowed")
	}
	l.Release(r, Timeout)

	if ok, _ := l.TryLock(r, Block); !ok {
		t.Fatalf("fixed: the round must accept a block result, since the timeout side was never actually signed")
	}
}

// TestRelease_OnlyClearsTheMatchingSide guards against a caller bug (or a
// copy/paste of the wrong side at a call site) silently corrupting a
// legitimate reservation. Release must be a strict compare-and-delete, never
// an unconditional delete.
func TestRelease_OnlyClearsTheMatchingSide(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 0}

	if ok, _ := l.TryLock(r, Block); !ok {
		t.Fatal("setup")
	}
	// Releasing the WRONG side must not touch a real Block reservation.
	l.Release(r, Timeout)

	side, signed := l.Signed(r)
	if !signed || side != Block {
		t.Fatalf("Release(wrong side) corrupted the ledger: signed=%v side=%v, want Block still held", signed, side)
	}
	if ok, taken := l.TryLock(r, Timeout); ok || taken != Block {
		t.Fatalf("the real Block reservation must still refuse Timeout after a mismatched Release, got ok=%v taken=%v", ok, taken)
	}
}

// TestRelease_NoOpOnAnUnlockedRound must never panic and must never lock a
// round it did not itself unlock - Release is not a substitute for TryLock.
func TestRelease_NoOpOnAnUnlockedRound(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 20, Period: 0}

	l.Release(r, Block) // never locked - must be a silent no-op

	if _, signed := l.Signed(r); signed {
		t.Fatalf("Release on a never-locked round must not create a reservation")
	}
}

// TestRelease_DoesNotAffectOtherRounds is a scoping check: Release(r, side)
// touches exactly r, nothing else in the ledger.
func TestRelease_DoesNotAffectOtherRounds(t *testing.T) {
	l := NewLedger()
	r1 := Round{Height: 1, Period: 0}
	r2 := Round{Height: 2, Period: 0}

	l.TryLock(r1, Block)
	l.TryLock(r2, Timeout)
	l.Release(r1, Block)

	if _, signed := l.Signed(r1); signed {
		t.Fatalf("r1 should be released")
	}
	if side, signed := l.Signed(r2); !signed || side != Timeout {
		t.Fatalf("r2 must be untouched by releasing r1, got signed=%v side=%v", signed, side)
	}
}

// TestRelease_AfterReleaseARetryStartsClean confirms Release does not leave
// stray state that would make a subsequent retry of the SAME side behave any
// differently than a genuinely first attempt - the round the caller retries
// after a failure must be indistinguishable from one that was never touched.
func TestRelease_AfterReleaseARetryStartsClean(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 30, Period: 0}

	l.TryLock(r, Block)
	l.Release(r, Block)

	if ok, taken := l.TryLock(r, Block); !ok {
		t.Fatalf("retrying the same side after Release must succeed, got ok=%v taken=%v", ok, taken)
	}
	// And normal mutual exclusion still applies going forward.
	if ok, taken := l.TryLock(r, Timeout); ok || taken != Block {
		t.Fatalf("after a fresh successful lock, the OTHER side must be refused as usual, got ok=%v taken=%v", ok, taken)
	}
}

// TestRelease_ConcurrentWithTryLock_NoRace is the concurrency test the
// finding asked for. It runs under -race: Release and a concurrent retry of
// the SAME side hit the same mutex from two goroutines on every trial, and
// the ledger must end each trial in one of exactly two valid states - never a
// torn or impossible one (e.g. signed for the OTHER side, which would mean
// Release let something illegitimate through).
func TestRelease_ConcurrentWithTryLock_NoRace(t *testing.T) {
	for trial := 0; trial < 500; trial++ {
		l := NewLedger()
		r := Round{Height: uint64(trial), Period: 0}
		if ok, _ := l.TryLock(r, Block); !ok {
			t.Fatalf("trial %d: setup lock failed", trial)
		}

		var wg sync.WaitGroup
		var retryOK bool
		wg.Add(2)
		go func() {
			defer wg.Done()
			l.Release(r, Block)
		}()
		go func() {
			defer wg.Done()
			// A concurrent retry of the SAME side must succeed regardless of
			// whether it interleaves before, during, or after the Release:
			// TryLock is idempotent for a matching side (found existing==
			// requested), and if Release runs first it is simply a fresh
			// lock - both outcomes report ok=true.
			retryOK, _ = l.TryLock(r, Block)
		}()
		wg.Wait()

		if !retryOK {
			t.Fatalf("trial %d: a same-side retry racing with Release must still succeed", trial)
		}
		if side, signed := l.Signed(r); signed && side != Block {
			t.Fatalf("trial %d: round left in an impossible state %v - Release must never let the OTHER side in", trial, side)
		}
	}
}

// TestRelease_ConcurrentOppositeSideAfterRelease_ExactlyOneWins proves the
// package's core safety invariant still holds in the exact scenario Release
// exists to unblock: once a reservation is released, a genuine race between
// the two DIFFERENT sides for that now-free round still resolves to exactly
// one winner, same as TestTryLock_ConcurrentOppositeSidesExactlyOneWins for a
// round that was never locked at all.
func TestRelease_ConcurrentOppositeSideAfterRelease_ExactlyOneWins(t *testing.T) {
	for trial := 0; trial < 200; trial++ {
		l := NewLedger()
		r := Round{Height: uint64(trial), Period: 1}
		l.TryLock(r, Block) // the reservation that is about to fail to sign
		l.Release(r, Block) // ... and gets released

		var wg sync.WaitGroup
		results := make([]bool, 2)
		for i, side := range []Side{Block, Timeout} {
			wg.Add(1)
			go func(i int, s Side) {
				defer wg.Done()
				results[i], _ = l.TryLock(r, s)
			}(i, side)
		}
		wg.Wait()
		if results[0] == results[1] {
			t.Fatalf("trial %d: exactly one side must win after release, got block=%v timeout=%v", trial, results[0], results[1])
		}
	}
}
