package MessagePassing

import (
	"fmt"

	"gossipnode/internal/roundlock"
)

// roundLedger is the per-round signing ledger this package consults. A
// package variable so tests can use a fresh ledger; production uses the
// process-wide roundlock.Default, the SAME ledger the timeout-vote signer in
// messaging uses - the rule only holds if both sides share one ledger.
var roundLedger = roundlock.Default

// lockBlockResultRound records that this node is about to BLS-sign a block
// result for round (height, period). It returns an error - and the caller must
// not sign - when this node has already signed a timeout vote for that round.
// Re-signing a block result for the same round (a retried request) is allowed.
//
// It returns fresh=true only when THIS call created the reservation. A caller
// whose signing attempt then FAILS must call unlockBlockResultRound below ONLY
// when fresh is true - on a re-entry an earlier attempt owns the reservation
// and may already have shipped a signature (F-9). See TryLockFresh.
func lockBlockResultRound(height, period uint64) (fresh bool, err error) {
	ok, taken, fresh := roundLedger.TryLockFresh(roundlock.Round{Height: height, Period: period}, roundlock.Block)
	if !ok {
		return false, fmt.Errorf("round (height %d, period %d) already signed as %s", height, period, taken)
	}
	return fresh, nil
}

// unlockBlockResultRound reverses a lockBlockResultRound reservation that was
// not followed by an actual signature - F-3 ("lock-then-fail stranding").
//
// CALL ONLY WHEN lockBlockResultRound RETURNED fresh=true. On a re-entry the
// reservation belongs to an earlier attempt that may already have shipped a
// signature; releasing it would let this node also sign a timeout for the same
// round (F-9).
//
// lockBlockResultRound marks the round Block-signed the moment it succeeds,
// before the caller has actually produced a BLS signature. If the sign that
// follows fails (see ListenerHandler.go's handleVoteResultRequest, the one
// production caller), the round would otherwise stay permanently marked as
// signed by this node even though no valid signature was ever produced -
// stranding it: this node could then never sign a timeout vote for the same
// round either, contributing to neither certificate for the rest of the
// process's life. Call this from that failure path, and nowhere else - see
// roundlock.Ledger.Release's doc comment for the full reasoning and the
// safety property this does NOT weaken.
func unlockBlockResultRound(height, period uint64) {
	roundLedger.Release(roundlock.Round{Height: height, Period: period}, roundlock.Block)
}
