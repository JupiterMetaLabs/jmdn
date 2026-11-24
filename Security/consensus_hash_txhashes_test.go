package Security

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// signedConfigTxs returns one legacy and one EIP-1559 transaction, really
// signed, in the shape the ingest path produces (tx.Hash = geth hash).
func signedConfigTxs(t *testing.T) []config.Transaction {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")
	chain := big.NewInt(7000700)
	signer := types.LatestSignerForChainID(chain)
	var out []config.Transaction
	for _, inner := range []types.TxData{
		&types.LegacyTx{Nonce: 1, To: &to, Value: big.NewInt(5), Gas: 21000, GasPrice: big.NewInt(1e9)},
		&types.DynamicFeeTx{ChainID: chain, Nonce: 2, To: &to, Value: big.NewInt(5), Gas: 21000, GasTipCap: big.NewInt(1e9), GasFeeCap: big.NewInt(2e9)},
	} {
		stx, err := types.SignNewTx(key, signer, inner)
		if err != nil {
			t.Fatal(err)
		}
		v, r, s := stx.RawSignatureValues()
		ct := config.Transaction{Hash: stx.Hash(), Type: stx.Type(), Nonce: stx.Nonce(), To: stx.To(), Value: stx.Value(),
			GasLimit: stx.Gas(), GasPrice: stx.GasPrice(), MaxFee: stx.GasFeeCap(), MaxPriorityFee: stx.GasTipCap(), Data: stx.Data(), V: v, R: r, S: s}
		if stx.Type() != types.LegacyTxType {
			ct.ChainID = stx.ChainId()
		}
		out = append(out, ct)
	}
	return out
}

func TestRecomputeConsensusHashFromTxHashes_EqualsContentsHashOnHonestBlocks(t *testing.T) {
	for name, b := range map[string]*config.ZKBlock{
		"no transactions": sampleBlockNoTxs(),
		"legacy + 1559":   withTxs(sampleBlockNoTxs(), signedConfigTxs(t)),
	} {
		if RecomputeConsensusHashFromTxHashes(b) != RecomputeBlockHashWithConsensusFields(b) {
			t.Fatalf("%s: tx-hash variant must equal the contents-based ConsensusHash when tx.Hash is the contents hash", name)
		}
	}
}

// The reason the variant exists: a ThebeDB round trip drops ChainID (and
// AccessList), so the contents-based recompute of a stored type-2 transaction
// changes while the carried tx.Hash does not.
func TestRecomputeConsensusHashFromTxHashes_SurvivesTheStoredShape(t *testing.T) {
	b := withTxs(sampleBlockNoTxs(), signedConfigTxs(t))
	want := RecomputeBlockHashWithConsensusFields(b)

	stored := *b
	stored.Transactions = append([]config.Transaction(nil), b.Transactions...)
	stored.Transactions[1].ChainID = nil // exactly what txRecordToTransaction returns

	if RecomputeBlockHashWithConsensusFields(&stored) == want {
		t.Fatalf("precondition: dropping ChainID must change the contents-based recompute (else this test proves nothing)")
	}
	if RecomputeConsensusHashFromTxHashes(&stored) != want {
		t.Fatalf("the tx-hash variant must still match the certified ConsensusHash on the stored shape")
	}
}

func TestRecomputeConsensusHashFromTxHashes_BindsEveryInput(t *testing.T) {
	base := withTxs(sampleBlockNoTxs(), signedConfigTxs(t))
	h := RecomputeConsensusHashFromTxHashes(base)
	for name, mutate := range map[string]func(b *config.ZKBlock){
		"tx hash":                 func(b *config.ZKBlock) { b.Transactions[0].Hash[0] ^= 1 },
		"tx order":                func(b *config.ZKBlock) { b.Transactions[0], b.Transactions[1] = b.Transactions[1], b.Transactions[0] },
		"committee snapshot hash": func(b *config.ZKBlock) { b.CommitteeSnapshotHash = []byte("other") },
		"period":                  func(b *config.ZKBlock) { b.Period++ },
		"vdf params digest":       func(b *config.ZKBlock) { b.VdfParamsDigest = "x" },
	} {
		c := *base
		c.Transactions = append([]config.Transaction(nil), base.Transactions...)
		mutate(&c)
		if RecomputeConsensusHashFromTxHashes(&c) == h {
			t.Errorf("changing the %s must change the digest", name)
		}
	}
}

func sampleBlockNoTxs() *config.ZKBlock {
	b := sampleBlock()
	b.Transactions = nil
	b.BlockNumber = 860
	return b
}

func withTxs(b *config.ZKBlock, txs []config.Transaction) *config.ZKBlock {
	b.Transactions = txs
	return b
}
