package messaging

// P7 / invariant 7: linkage is FAIL-CLOSED. When the local tip or parent cannot
// be authenticated, or the block is beyond the next-expected height, the block
// is rejected (never silently accepted out of band), and a height gap triggers
// authenticated catch-up. These tests exercise the pure linkageDecision (no DB)
// and the checkLinkage catch-up nudge (via injected readers).

import (
	"context"
	"errors"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func blkAt(num uint64, prev common.Hash) *config.ZKBlock {
	return &config.ZKBlock{BlockNumber: num, PrevHash: prev, BlockHash: common.BytesToHash([]byte{byte(num)})}
}

// distinct, VALID, non-zero hashes (HexToHash on non-hex text silently yields
// the zero hash, which would make unequal-looking values compare equal).
var (
	hashTip   = common.BytesToHash([]byte{0xaa})
	hashWrong = common.BytesToHash([]byte{0xbb})
)

func TestP7_LinkageDecision(t *testing.T) {
	tipHash := hashTip
	parent := &config.ZKBlock{BlockNumber: 10, BlockHash: tipHash}

	cases := []struct {
		name       string
		b          *config.ZKBlock
		localTip   uint64
		tipErr     error
		parent     *config.ZKBlock
		parentErr  error
		wantReason string // "" == accept
	}{
		{"tip unreadable → fail closed", blkAt(11, tipHash), 0, errors.New("db down"), nil, nil, "tip_unreadable"},
		{"fresh node accepts block 1", blkAt(1, common.Hash{}), 0, nil, nil, nil, ""},
		{"fresh node rejects out-of-band block 5", blkAt(5, common.Hash{}), 0, nil, nil, nil, "height_gap"},
		{"stale height rejected", blkAt(8, tipHash), 10, nil, nil, nil, "stale_height"},
		{"equal height rejected", blkAt(10, tipHash), 10, nil, nil, nil, "stale_height"},
		{"gap beyond tip+1 rejected", blkAt(15, tipHash), 10, nil, nil, nil, "height_gap"},
		{"tip+1 parent missing → fail closed", blkAt(11, tipHash), 10, nil, nil, nil, "parent_unavailable"},
		{"tip+1 parent load error → fail closed", blkAt(11, tipHash), 10, nil, nil, errors.New("read fail"), "parent_unavailable"},
		{"tip+1 wrong parent hash rejected", blkAt(11, common.HexToHash("0xwrong")), 10, nil, parent, nil, "bad_parent"},
		{"tip+1 correct parent accepted", blkAt(11, tipHash), 10, nil, parent, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rej := linkageDecision(tc.b, tc.localTip, tc.tipErr, tc.parent, tc.parentErr)
			if tc.wantReason == "" {
				if rej != nil {
					t.Fatalf("expected accept, got reject reason=%s err=%v", rej.reason, rej.err)
				}
				return
			}
			if rej == nil || rej.reason != tc.wantReason {
				t.Fatalf("reason=%v, want %s", rej, tc.wantReason)
			}
		})
	}
}

// TestP7_BadStateRootRejected covers the state-root chain axis (needs a parent
// with a non-zero state root so stateRootChain engages).
func TestP7_BadStateRootRejected(t *testing.T) {
	parent := &config.ZKBlock{
		BlockNumber: 20,
		BlockHash:   common.HexToHash("0xparent"),
		StateRoot:   common.HexToHash("0xdeadbeef"),
	}
	b := &config.ZKBlock{
		BlockNumber: 21,
		PrevHash:    parent.BlockHash,
		BlockHash:   common.HexToHash("0xchild"),
		StateRoot:   common.HexToHash("0xbogus"), // does not chain from parent
	}
	rej := linkageDecision(b, 20, nil, parent, nil)
	if rej == nil || rej.reason != "bad_stateroot" {
		t.Fatalf("want bad_stateroot, got %v", rej)
	}
}

// TestP7_HeightGapTriggersCatchUp verifies checkLinkage nudges the authenticated
// catch-up requester on a gap, using injected readers so no DB is required.
func TestP7_HeightGapTriggersCatchUp(t *testing.T) {
	// Save & restore the injectable seams.
	origTip, origBlk, origReq := readLocalTip, readBlockByNumber, catchUpRequester
	t.Cleanup(func() { readLocalTip, readBlockByNumber, catchUpRequester = origTip, origBlk, origReq })

	// Local tip 10, so a block at 15 is a gap.
	readLocalTip = func(context.Context) (uint64, error) { return 10, nil }
	readBlockByNumber = func(uint64) (*config.ZKBlock, error) { return nil, nil }

	var gotFrom uint64
	var fired bool
	SetCatchUpRequester(func(from uint64) { fired = true; gotFrom = from })

	rej := checkLinkage(context.Background(), blkAt(15, hashTip))
	if rej == nil || rej.reason != "height_gap" {
		t.Fatalf("want height_gap, got %v", rej)
	}
	if !fired {
		t.Fatalf("SECURITY (P7): height gap must trigger authenticated catch-up")
	}
	if gotFrom != 11 { // next-needed height = tip+1
		t.Fatalf("catch-up fromBlock = %d, want 11", gotFrom)
	}
}

// TestP7_NoCatchUpOnCleanAccept confirms an in-order block does not trigger
// catch-up (no false reconciles on the happy path).
func TestP7_NoCatchUpOnCleanAccept(t *testing.T) {
	origTip, origBlk, origReq := readLocalTip, readBlockByNumber, catchUpRequester
	t.Cleanup(func() { readLocalTip, readBlockByNumber, catchUpRequester = origTip, origBlk, origReq })

	tipHash := hashTip
	readLocalTip = func(context.Context) (uint64, error) { return 10, nil }
	readBlockByNumber = func(uint64) (*config.ZKBlock, error) {
		return &config.ZKBlock{BlockNumber: 10, BlockHash: tipHash}, nil
	}
	fired := false
	SetCatchUpRequester(func(uint64) { fired = true })

	if rej := checkLinkage(context.Background(), blkAt(11, tipHash)); rej != nil {
		t.Fatalf("in-order block should be accepted, got %s", rej.reason)
	}
	if fired {
		t.Fatalf("clean accept must not trigger catch-up")
	}
}
