package Security

// Golden check: RecomputeBlockHashWithConsensusFields must stay byte-identical
// across the refactor that moved its body into consensusFieldsDigest (shared
// with RecomputeConsensusHashFromTxHashes). The expected value below was
// computed by the PRE-refactor implementation on v3base 77089b8 for this exact
// block. If this test fails, every node's block-hash check changes - do not
// update the constant unless that is the intent.

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func goldenConsensusBlock() *config.ZKBlock {
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")
	from := common.HexToAddress("0x2222222222222222222222222222222222222222")
	return &config.ZKBlock{
		BlockNumber:           4242,
		PrevHash:              common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Slot:                  4300,
		Period:                2,
		RandaoReveals:         []config.Reveal{{ProposerID: "peer-a", Secret: []byte{1, 2, 3}}, {ProposerID: "peer-b", Secret: []byte{4, 5, 6}}},
		VdfProof:              []byte("vdf-proof-bytes"),
		SeedEpoch:             86,
		VotingSnapshotEpoch:   85,
		PrevAggCert:           []config.CertSigner{{PeerID: "peer-a", PubKey: "ab", Signature: "cd"}},
		CommitteeSnapshotHash: []byte{9, 8, 7, 6},
		VdfParamsDigest:       "vdf-params-digest",
		Transactions: []config.Transaction{{
			Hash: common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			Type: 2, ChainID: big.NewInt(7000700), Nonce: 7, From: &from, To: &to, Value: big.NewInt(12345),
			GasLimit: 21000, MaxFee: big.NewInt(2e9), MaxPriorityFee: big.NewInt(1e9), Data: []byte{0xde, 0xad},
			V: big.NewInt(1), R: big.NewInt(2), S: big.NewInt(3),
		}},
	}
}

const goldenBlockHashWithConsensusFields = "0x715b14f43c31642a1e3f7fade888b3312fd4d960702c34638b639b27349bf02d"

func TestRecomputeBlockHashWithConsensusFields_Golden(t *testing.T) {
	got := RecomputeBlockHashWithConsensusFields(goldenConsensusBlock()).Hex()
	if got != goldenBlockHashWithConsensusFields {
		t.Fatalf("block hash digest changed: got %s, want %s", got, goldenBlockHashWithConsensusFields)
	}
}
