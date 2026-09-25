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
// A caller whose lockBlockResultRound succeeds but whose BLS signing attempt
// then FAILS must call unlockBlockResultRound below - see its comment (F-3).
func lockBlockResultRound(height, period uint64) error {
	ok, taken := roundLedger.TryLock(roundlock.Round{Height: height, Period: period}, roundlock.Block)
	if !ok {
		return fmt.Errorf("round (height %d, period %d) already signed as %s", height, period, taken)
	}
	return nil
}

// unlockBlockResultRound reverses a lockBlockResultRound reservation that was
// not followed by an actual signature - F-3 ("lock-then-fail stranding").
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
