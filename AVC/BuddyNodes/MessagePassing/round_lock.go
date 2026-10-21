package MessagePassing

import (
	"fmt"

	"gossipnode/config"
	"gossipnode/internal/roundlock"
)

// roundLedger is the per-round signing ledger this package consults. A
// package variable so tests can use a fresh ledger; production uses the
// process-wide roundlock.Default.
var roundLedger = roundlock.Default

// roundRefusal is returned by lockBlockResultRound when this node must not
// sign. Highest is the highest period this node has signed for the height; it
// travels back to the sequencer in the refusal so a sequencer that restarted
// mid-height can resume above every round it already burned.
type roundRefusal struct {
	Height  uint64
	Period  uint64
	Reason  roundlock.Refusal
	Highest uint64
}

func (e *roundRefusal) Error() string {
	return fmt.Sprintf("round (height %d, period %d) refused: %s (highest signed period %d)",
		e.Height, e.Period, e.Reason, e.Highest)
}

// blockIdentity is what R1 compares: BlockHash covers only the transactions,
// so the ConsensusHash (which covers Period, Slot and the other consensus
// fields) is what tells two proposals of the same transactions apart.
func blockIdentity(blockHash, consensusHash string) string {
	return blockHash + "/" + consensusHash
}

// lockBlockResultRound records that this node is about to BLS-sign the block
// identified by (blockHash, consensusHash) for round (height, period). It
// returns a *roundRefusal - and the caller must not sign - when this node
// already signed a different block for the round (R1), a block at a higher
// period for the height (R2), or the height is already committed. It returns
// a plain error when period exceeds config.MaxPeriod. Re-signing the same
// block (a retried request) is allowed.
//
// It returns fresh=true only when THIS call created the reservation. A caller
// whose signing attempt then FAILS must call unlockBlockResultRound below ONLY
// when fresh is true - on a re-entry an earlier attempt owns the reservation
// and may already have shipped a signature (F-9). See roundlock.TryLockFresh.
func lockBlockResultRound(height, period uint64, blockHash, consensusHash string) (fresh bool, err error) {
	if period > config.MaxPeriod {
		// Every verifier rejects such a block (config.MaxPeriod), so a
		// signature on it could only help certify a block nobody accepts.
		return false, fmt.Errorf("round (height %d, period %d) refused: period exceeds the cap %d",
			height, period, config.MaxPeriod)
	}
	ok, refusal, highest, fresh := roundLedger.TryLockFresh(
		roundlock.Round{Height: height, Period: period}, blockIdentity(blockHash, consensusHash))
	if !ok {
		return false, &roundRefusal{Height: height, Period: period, Reason: refusal, Highest: highest}
	}
	return fresh, nil
}

// unlockBlockResultRound reverses a lockBlockResultRound reservation that was
// not followed by an actual signature - F-3 ("lock-then-fail stranding").
//
// CALL ONLY WHEN lockBlockResultRound RETURNED fresh=true. On a re-entry the
// reservation belongs to an earlier attempt that may already have shipped a
// signature; releasing it would let this node sign a second block for the
// same round (F-9).
//
// Without this, a round whose sign failed would stay reserved for the
// process's life and this node could never contribute to it. Call this from
// the signing failure path in handleVoteResultRequest (ListenerHandler.go),
// and nowhere else - see roundlock.Ledger.Release.
func unlockBlockResultRound(height, period uint64, blockHash, consensusHash string) {
	roundLedger.Release(roundlock.Round{Height: height, Period: period}, blockIdentity(blockHash, consensusHash))
}
