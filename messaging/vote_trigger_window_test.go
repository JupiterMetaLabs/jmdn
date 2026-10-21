package messaging

// voteTriggerInWindow and admitVoteTrigger: a vote trigger with a nil block,
// or a claimed height far from the local tip, must never be admitted toward
// the consensus-message cache or anything past it.

import (
	"errors"
	"testing"

	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/ethereum/go-ethereum/common"
)

func TestVoteTriggerInWindow(t *testing.T) {
	// MaxConsensusLagBlocks is 2, so the ahead bound is 3: a node allowed to
	// lag 2 blocks behind the real head can still legitimately receive a
	// trigger for (its own tip) + 2 + 1.
	cases := []struct {
		name string
		tip  uint64
		n    uint64
		want bool
	}{
		{"fresh node, tip 0, genesis-ish trigger", 0, 0, true},
		{"fresh node, tip 0, within ahead bound", 0, 3, true},
		{"fresh node, tip 0, one past ahead bound", 0, 4, false},
		{"tip 1, one behind (allowed)", 1, 0, true},
		{"tip 1, at tip", 1, 1, true},
		{"tip 100, exactly one behind", 100, 99, true},
		{"tip 100, two behind, rejected", 100, 98, false},
		{"tip 100, at tip", 100, 100, true},
		{"tip 100, exactly at ahead bound (+3)", 100, 103, true},
		{"tip 100, one past ahead bound (+4)", 100, 104, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := voteTriggerInWindow(c.tip, c.n); got != c.want {
				t.Fatalf("voteTriggerInWindow(tip=%d, n=%d) = %v, want %v", c.tip, c.n, got, c.want)
			}
		})
	}
}

// TestAdmitVoteTrigger is the decisive regression test: it calls
// admitVoteTrigger directly, with no handler, network, or ForListner setup
// involved, so a false "admit" or "drop" can only come from this function's
// own logic — nothing else in the call path could produce it.
func TestAdmitVoteTrigger(t *testing.T) {
	block := func(n uint64) *PubSubMessages.ConsensusMessage {
		return &PubSubMessages.ConsensusMessage{ZKBlock: &config.ZKBlock{BlockNumber: n, BlockHash: common.HexToHash("0xadmit-test")}}
	}

	t.Run("nil ZKBlock is never admitted", func(t *testing.T) {
		if admitVoteTrigger(&PubSubMessages.ConsensusMessage{}, 100, nil) {
			t.Fatal("a nil-ZKBlock message was admitted")
		}
	})

	t.Run("tip read error admits without a window check", func(t *testing.T) {
		if !admitVoteTrigger(block(1_000_000), 0, errors.New("tip unavailable")) {
			t.Fatal("a tip-read error should fail OPEN on the window check (the cache cap still bounds memory), but this was dropped")
		}
	})

	t.Run("in-window height is admitted", func(t *testing.T) {
		if !admitVoteTrigger(block(101), 100, nil) {
			t.Fatal("an in-window height (tip+1) was dropped")
		}
	})

	t.Run("out-of-window height is dropped", func(t *testing.T) {
		if admitVoteTrigger(block(100000), 100, nil) {
			t.Fatal("a wildly out-of-window height was admitted")
		}
	})

	t.Run("admitting prunes stale cache entries below the window", func(t *testing.T) {
		staleHash := common.HexToHash("0x5151515151515151515151515151515151515151515151515151515151515151")
		stale := &PubSubMessages.ConsensusMessage{ZKBlock: &config.ZKBlock{BlockNumber: 50, BlockHash: staleHash}}
		stale.SetGloalVarCacheConsensusMessage()
		if got := PubSubMessages.LookupConsensusMessageByBlockHash(staleHash.Hex()); got == nil {
			t.Fatal("test setup: the stale entry was not cached")
		}

		if !admitVoteTrigger(block(101), 100, nil) {
			t.Fatal("expected this in-window message to be admitted")
		}
		if got := PubSubMessages.LookupConsensusMessageByBlockHash(staleHash.Hex()); got != nil {
			t.Fatal("a stale entry below the window's low bound should have been pruned on admission")
		}
	})
}
