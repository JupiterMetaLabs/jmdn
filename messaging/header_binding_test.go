package messaging

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/execbridge"
)

type fakeExecutor struct{ execbridge.ContractExecutor }

func headerBoundBlock(t *testing.T) *config.ZKBlock {
	t.Helper()
	cb := common.HexToAddress("0xc0")
	b := &config.ZKBlock{BlockNumber: 50, BlockHash: common.HexToHash("0xab"), CoinbaseAddr: &cb, StateFingerprint: "fp"}
	b.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(b)
	return b
}

// With header binding active, a relayed copy of a certified block whose
// coinbase was changed no longer matches its ConsensusHash (and so its
// certificate).
func TestCheckConsensusBinding_RejectsRewrittenCoinbase(t *testing.T) {
	Security.SetHeaderBindingFromHeight(1)
	t.Cleanup(func() { Security.SetHeaderBindingFromHeight(0) })
	b := headerBoundBlock(t)
	if rej := checkConsensusBinding(b); rej != nil {
		t.Fatalf("untouched block rejected: %v", rej.err)
	}
	other := common.HexToAddress("0xbad")
	b.CoinbaseAddr = &other
	if rej := checkConsensusBinding(b); rej == nil || rej.reason != "consensus_hash_mismatch" {
		t.Fatalf("rewritten coinbase must be rejected, got %+v", rej)
	}
}

// With header binding active and contract execution on, a block without the
// producer's state fingerprint is rejected.
func TestCheckConsensusBinding_RequiresStateFingerprint(t *testing.T) {
	Security.SetHeaderBindingFromHeight(1)
	execbridge.SetExecutor(fakeExecutor{})
	t.Cleanup(func() { Security.SetHeaderBindingFromHeight(0); execbridge.SetExecutor(nil) })
	b := headerBoundBlock(t)
	b.StateFingerprint = ""
	if rej := checkConsensusBinding(b); rej == nil || rej.reason != "state_fingerprint_missing" {
		t.Fatalf("missing fingerprint must be rejected, got %+v", rej)
	}
	_ = context.Background()
}
