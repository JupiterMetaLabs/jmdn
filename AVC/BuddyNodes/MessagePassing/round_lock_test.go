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

	if err := lockBlockResultRound(848, 0); err == nil {
		t.Fatalf("a buddy must not sign a block result for a round it already timed out")
	}
	// The next period is a new round and must be signable.
	if err := lockBlockResultRound(848, 1); err != nil {
		t.Fatalf("block result for the next period must be allowed: %v", err)
	}
}

func TestLockBlockResultRound_BlocksLaterTimeoutAndIsIdempotent(t *testing.T) {
	l := withFreshRoundLedger(t)
	if err := lockBlockResultRound(900, 2); err != nil {
		t.Fatalf("first block result must be allowed: %v", err)
	}
	if err := lockBlockResultRound(900, 2); err != nil {
		t.Fatalf("a retried vote-result request for the same round must be allowed: %v", err)
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

	if err := lockBlockResultRound(848, 0); err != nil {
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
