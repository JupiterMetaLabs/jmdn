package MessagePassing

import (
	"testing"

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

func TestLockBlockResultRound_RefusedAfterTimeout(t *testing.T) {
	l := withFreshRoundLedger(t)
	l.TryLock(roundlock.Round{Height: 848, Period: 0}, roundlock.Timeout)

	if _, err := lockBlockResultRound(848, 0); err == nil {
		t.Fatalf("a buddy must not sign a block result for a round it already timed out")
	}
	// The next period is a new round and must be signable.
	if _, err := lockBlockResultRound(848, 1); err != nil {
		t.Fatalf("block result for the next period must be allowed: %v", err)
	}
}

func TestLockBlockResultRound_BlocksLaterTimeoutAndIsIdempotent(t *testing.T) {
	l := withFreshRoundLedger(t)
	fresh, err := lockBlockResultRound(900, 2)
	if err != nil {
		t.Fatalf("first block result must be allowed: %v", err)
	}
	if !fresh {
		t.Fatal("the first acquire must report fresh=true")
	}
	// F-9: a retry is allowed, but must NOT report fresh - the reservation
	// belongs to the first attempt, which may already have shipped a
	// signature. Only fresh=true callers may release on a sign failure.
	retryFresh, err := lockBlockResultRound(900, 2)
	if err != nil {
		t.Fatalf("a retried vote-result request for the same round must be allowed: %v", err)
	}
	if retryFresh {
		t.Fatal("F-9: a retried request must report fresh=false, or its failure path " +
			"would release the reservation standing behind the first attempt's signature")
	}
	if ok, _ := l.TryLock(roundlock.Round{Height: 900, Period: 2}, roundlock.Timeout); ok {
		t.Fatalf("after signing a block result, a timeout vote for the same round must be refused")
	}
}

func TestRoundLedger_DefaultIsSharedWithTimeoutSigner(t *testing.T) {
	// The rule only holds if the buddy signer and the timeout signer consult
	// ONE ledger. Production must use roundlock.Default.
	if roundLedger != roundlock.Default {
		t.Fatalf("MessagePassing must use roundlock.Default in production")
	}
}

// TestUnlockBlockResultRound_FreesTheRoundAfterAFailedSign is F-3's regression
// test at this call site: handleVoteResultRequest reserves the round via
// lockBlockResultRound BEFORE it actually BLS-signs, so a signing failure
// AFTER a successful lock must not leave the round permanently claimed - see
// unlockBlockResultRound's doc comment.
func TestUnlockBlockResultRound_FreesTheRoundAfterAFailedSign(t *testing.T) {
	l := withFreshRoundLedger(t)

	if _, err := lockBlockResultRound(848, 0); err != nil {
		t.Fatalf("first reservation must succeed: %v", err)
	}

	// Simulate what handleVoteResultRequest now does on the BLS.SignMessage*
	// error path: the reservation is released because no signature was
	// actually produced.
	unlockBlockResultRound(848, 0)

	if _, signed := l.Signed(roundlock.Round{Height: 848, Period: 0}); signed {
		t.Fatalf("round must be completely unlocked after unlockBlockResultRound")
	}
	if ok, _ := l.TryLock(roundlock.Round{Height: 848, Period: 0}, roundlock.Timeout); !ok {
		t.Fatalf("fixed: a timeout vote must now be signable, since no block signature was ever actually produced")
	}
}

// TestUnlockBlockResultRound_DoesNotClearAGenuineTimeoutLock guards against
// unlockBlockResultRound (called only on OUR OWN failed sign, always with
// side=Block) accidentally clearing a legitimate Timeout reservation that
// happens to share the same round - Release is a strict compare-and-delete.
func TestUnlockBlockResultRound_DoesNotClearAGenuineTimeoutLock(t *testing.T) {
	l := withFreshRoundLedger(t)
	l.TryLock(roundlock.Round{Height: 5, Period: 0}, roundlock.Timeout)

	unlockBlockResultRound(5, 0) // this node never held the Block side here

	side, signed := l.Signed(roundlock.Round{Height: 5, Period: 0})
	if !signed || side != roundlock.Timeout {
		t.Fatalf("a real Timeout reservation must survive an unrelated unlockBlockResultRound call, got signed=%v side=%v", signed, side)
	}
}

// TestF9_RetryFailureMustNotReopenTheRound is the adapter-level F-9 regression:
// the production sequence in handleVoteResultRequest, with the fresh guard.
func TestF9_RetryFailureMustNotReopenTheRound(t *testing.T) {
	l := withFreshRoundLedger(t)

	fresh1, err := lockBlockResultRound(777, 0) // first request
	if err != nil || !fresh1 {
		t.Fatalf("setup: fresh1=%v err=%v", fresh1, err)
	}
	// its BLS sign SUCCEEDS - a signature ships.

	fresh2, err := lockBlockResultRound(777, 0) // retried request
	if err != nil {
		t.Fatalf("setup: retry must be allowed: %v", err)
	}
	// its BLS sign FAILS - the handler releases only when its own lock was fresh.
	if fresh2 {
		unlockBlockResultRound(777, 0)
	}

	if ok, _ := l.TryLock(roundlock.Round{Height: 777, Period: 0}, roundlock.Timeout); ok {
		t.Fatal("F-9: timeout admitted for a round already block-signed")
	}
}
