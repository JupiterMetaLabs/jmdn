package messaging

import (
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// linkedPair returns a stored parent at tip and the next block linked to it,
// with the given slots. Period 0, no state roots: only the slot rule decides.
func linkedPair(tip, parentSlot, childSlot uint64) (parent, child *config.ZKBlock) {
	parent = &config.ZKBlock{
		BlockNumber: tip,
		BlockHash:   common.HexToHash("0xa1"),
		Slot:        parentSlot,
	}
	child = &config.ZKBlock{
		BlockNumber: tip + 1,
		BlockHash:   common.HexToHash("0xa2"),
		PrevHash:    parent.BlockHash,
		Slot:        childSlot,
	}
	return parent, child
}

// A main-branch block arriving live in legacy follow mode carries slot 0. It
// was rejected for bad_slot (block 21829 and every block since on the ThebeDB
// node), so the node only advanced by catch-up, one block behind. It now takes
// the catch-up rule: slot 0 after a slot-0 parent.
func TestLinkage_LegacyBlockSlotRule(t *testing.T) {
	const tip = 21828

	t.Run("legacy block, slot 0 after slot-0 parent: accepted", func(t *testing.T) {
		withLegacyFollow(t, true, testPeerID(t), 0)
		parent, b := linkedPair(tip, 0, 0)
		if rej := linkageDecision(b, tip, nil, parent, nil); rej != nil {
			t.Fatalf("rejected: %s %v", rej.reason, rej.err)
		}
	})
	t.Run("legacy block, slot 0 after a slotted parent: bad_slot", func(t *testing.T) {
		withLegacyFollow(t, true, testPeerID(t), 0)
		parent, b := linkedPair(tip, tip, 0)
		if rej := linkageDecision(b, tip, nil, parent, nil); rej == nil || rej.reason != "bad_slot" {
			t.Fatalf("got %v, want bad_slot", rej)
		}
	})
	t.Run("legacy block, wrong non-zero slot: bad_slot", func(t *testing.T) {
		withLegacyFollow(t, true, testPeerID(t), 0)
		parent, b := linkedPair(tip, 0, 7)
		if rej := linkageDecision(b, tip, nil, parent, nil); rej == nil || rej.reason != "bad_slot" {
			t.Fatalf("got %v, want bad_slot", rej)
		}
	})
	t.Run("legacy follow off: slot 0 is bad_slot", func(t *testing.T) {
		withLegacyFollow(t, false, testPeerID(t), 0)
		parent, b := linkedPair(tip, 0, 0)
		if rej := linkageDecision(b, tip, nil, parent, nil); rej == nil || rej.reason != "bad_slot" {
			t.Fatalf("got %v, want bad_slot", rej)
		}
	})
	t.Run("above until_height: slot 0 is bad_slot", func(t *testing.T) {
		withLegacyFollow(t, true, testPeerID(t), tip)
		parent, b := linkedPair(tip, 0, 0)
		if rej := linkageDecision(b, tip, nil, parent, nil); rej == nil || rej.reason != "bad_slot" {
			t.Fatalf("got %v, want bad_slot", rej)
		}
	})
	t.Run("v3 block with its slot: accepted", func(t *testing.T) {
		withLegacyFollow(t, false, testPeerID(t), 0)
		parent, b := linkedPair(tip, tip, tip+1)
		if rej := linkageDecision(b, tip, nil, parent, nil); rej != nil {
			t.Fatalf("rejected: %s %v", rej.reason, rej.err)
		}
	})
}
