package votecheck

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func h(b byte) common.Hash {
	var x common.Hash
	x[31] = b
	return x
}

func TestExtendsTip(t *testing.T) {
	tipHash := h(0xAA)

	cases := []struct {
		name     string
		number   uint64
		prevHash common.Hash
		tip      uint64
		tipHash  common.Hash
		wantErr  bool
	}{
		{"accepts the contiguous next block", 859, tipHash, 858, tipHash, false},
		{"rejects a duplicate of the tip height (block-858)", 858, tipHash, 858, tipHash, true},
		{"rejects a height below the tip", 800, tipHash, 858, tipHash, true},
		{"rejects a gap (844 while at 842, missing 843)", 844, h(0xBB), 842, tipHash, true},
		{"rejects a parent-hash mismatch (equivocation at tip+1)", 859, h(0xBB), 858, tipHash, true},
		{"accepts the first block after genesis regardless of hash", 1, h(0x00), 0, common.Hash{}, false},
		{"rejects a non-first block at genesis", 2, h(0x00), 0, common.Hash{}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ExtendsTip(c.number, c.prevHash, c.tip, c.tipHash)
			if c.wantErr && err == nil {
				t.Fatalf("ExtendsTip(%d, prev, tip=%d) = nil, want error", c.number, c.tip)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("ExtendsTip(%d, prev, tip=%d) = %v, want nil", c.number, c.tip, err)
			}
		})
	}
}
