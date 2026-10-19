package Security

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

func withHeaderBinding(t *testing.T, from uint64) {
	t.Helper()
	prev := headerBindingFromHeight.Load()
	SetHeaderBindingFromHeight(from)
	t.Cleanup(func() { SetHeaderBindingFromHeight(prev) })
}

func headerBlock(n uint64) *config.ZKBlock {
	cb := common.HexToAddress("0xc0")
	zk := common.HexToAddress("0xd0")
	return &config.ZKBlock{
		BlockNumber: n, PrevHash: common.HexToHash("0x01"), Timestamp: 1_790_000_000,
		GasLimit: 30_000_000, GasUsed: 21_000, StateRoot: common.HexToHash("0x02"),
		CoinbaseAddr: &cb, ZKVMAddr: &zk, ExtraData: "x",
		FeeRecipients: []config.FeeRecipient{{Addr: common.HexToAddress("0xe0"), Weight: 1}},
		AccountNonces: []config.AccountNonce{{Address: common.HexToAddress("0xf0"), Nonce: 7}},
	}
}

// Below the activation height the preimage is unchanged, so existing blocks
// keep their ConsensusHash.
func TestHeaderBinding_InactiveLeavesHashUnchanged(t *testing.T) {
	withHeaderBinding(t, 0)
	base := RecomputeBlockHashWithConsensusFields(headerBlock(100))
	withHeaderBinding(t, 101)
	if got := RecomputeBlockHashWithConsensusFields(headerBlock(100)); got != base {
		t.Fatal("ConsensusHash of a block below the activation height changed")
	}
	active := RecomputeBlockHashWithConsensusFields(headerBlock(101))
	SetHeaderBindingFromHeight(0)
	if RecomputeBlockHashWithConsensusFields(headerBlock(101)) == active {
		t.Fatal("activation must change the ConsensusHash at and above the height")
	}
}

// From the activation height on, each apply-affecting header field changes
// the ConsensusHash; ExtraData does not.
func TestHeaderBinding_CoversApplyAffectingFields(t *testing.T) {
	withHeaderBinding(t, 1)
	base := RecomputeBlockHashWithConsensusFields(headerBlock(5))
	other := common.HexToAddress("0xbad")
	mutations := map[string]func(b *config.ZKBlock){
		"coinbase":       func(b *config.ZKBlock) { b.CoinbaseAddr = &other },
		"coinbase nil":   func(b *config.ZKBlock) { b.CoinbaseAddr = nil },
		"zkvm":           func(b *config.ZKBlock) { b.ZKVMAddr = &other },
		"timestamp":      func(b *config.ZKBlock) { b.Timestamp++ },
		"gas limit":      func(b *config.ZKBlock) { b.GasLimit++ },
		"gas used":       func(b *config.ZKBlock) { b.GasUsed++ },
		"state root":     func(b *config.ZKBlock) { b.StateRoot = common.HexToHash("0x03") },
		"fee recipient":  func(b *config.ZKBlock) { b.FeeRecipients[0].Addr = other },
		"fee weight":     func(b *config.ZKBlock) { b.FeeRecipients[0].Weight = 2 },
		"account nonce":  func(b *config.ZKBlock) { b.AccountNonces[0].Nonce = 8 },
		"nonce address":  func(b *config.ZKBlock) { b.AccountNonces[0].Address = other },
		"nonces dropped": func(b *config.ZKBlock) { b.AccountNonces = nil },
	}
	for name, mut := range mutations {
		b := headerBlock(5)
		mut(b)
		if RecomputeBlockHashWithConsensusFields(b) == base {
			t.Errorf("%s is not covered by the ConsensusHash", name)
		}
	}
	b := headerBlock(5)
	b.ExtraData = "y"
	if RecomputeBlockHashWithConsensusFields(b) != base {
		t.Error("ExtraData must not be covered (it does not round-trip through storage)")
	}
}
