// Package votecheck holds the pure chain-position decision for a validator's vote.
//
// A vote must mean "I hold the parent and this block extends my tip." Before the
// block-858 fix, validators voted on transaction/hash validity alone, so:
//   - nodes at tip 858 voted ACCEPT for a SECOND, different candidate at 858
//     (block-858, 2026-09-25), and
//   - nodes at tip 842 voted ACCEPT for 844 before discovering they lacked 843
//     (2026-09-24).
//
// Both are chain-position faults, not transaction faults. ExtendsTip is the single,
// unit-tested predicate the vote path uses to catch them fail-closed. It is a pure
// function (no DB, no globals) so it can be exhaustively tested without the consensus
// stack; the caller (Vote.SubmitVote) reads the local tip and tip hash and passes
// them in.
package votecheck

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// ExtendsTip returns nil when a block at (number, prevHash) legitimately extends the
// local tip at (tip, tipHash), and a descriptive error otherwise.
//
//   - number <= tip        -> already committed / duplicate height (reject)
//   - number != tip+1      -> a gap: this node does not hold the parent yet (reject)
//   - prevHash != tipHash  -> parent mismatch: equivocation at the same height, or a
//     different chain (reject)
//
// tipHash is not consulted when tip == 0: nothing is committed to link against (the
// first block after genesis), so contiguity (number == 1) is the only requirement.
func ExtendsTip(number uint64, prevHash common.Hash, tip uint64, tipHash common.Hash) error {
	if number <= tip {
		return fmt.Errorf("height %d <= local tip %d (already committed / duplicate)", number, tip)
	}
	if number != tip+1 {
		return fmt.Errorf("non-contiguous height %d (local tip %d; missing %d..%d)", number, tip, tip+1, number-1)
	}
	if tip == 0 {
		return nil
	}
	if prevHash != tipHash {
		return fmt.Errorf("parent mismatch — block %d PrevHash %s != local tip %d hash %s",
			number, prevHash.Hex(), tip, tipHash.Hex())
	}
	return nil
}
