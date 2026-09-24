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
func lockBlockResultRound(height, period uint64) error {
	ok, taken := roundLedger.TryLock(roundlock.Round{Height: height, Period: period}, roundlock.Block)
	if !ok {
		return fmt.Errorf("round (height %d, period %d) already signed as %s", height, period, taken)
	}
	return nil
}
