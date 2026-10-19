package messaging

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/Security"
	"gossipnode/config"
)

func syncedBlock(t *testing.T, num uint64, txs ...config.Transaction) *config.ZKBlock {
	t.Helper()
	b := &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  num,
		Transactions: txs,
	}
	b.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(b)
	return b
}

// A well-formed signed block passes the synced-content gate.
func TestValidateSyncedBlockContents_AcceptsSignedBlock(t *testing.T) {
	key, _ := crypto.GenerateKey()
	b := syncedBlock(t, 10, signedTx(t, key, 0), signedTx(t, key, 1))
	if err := ValidateSyncedBlockContents(context.Background(), b); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}
}

// Rewriting a transaction's recipient or value while keeping its tx.Hash (and
// so BlockHash and any certificate over it) must be rejected: the hash no
// longer matches the contents and the signature no longer recovers the sender.
func TestValidateSyncedBlockContents_RejectsRewrittenContents(t *testing.T) {
	key, _ := crypto.GenerateKey()
	b := syncedBlock(t, 11, signedTx(t, key, 0))
	other := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	b.Transactions[0].To = &other
	b.Transactions[0].Value = big.NewInt(1_000_000)
	if err := ValidateSyncedBlockContents(context.Background(), b); err == nil {
		t.Fatal("block with rewritten tx contents was accepted")
	}
}

// A transaction with no valid signature for its declared sender is rejected.
func TestValidateSyncedBlockContents_RejectsUnsignedTx(t *testing.T) {
	key, _ := crypto.GenerateKey()
	tx := signedTx(t, key, 0)
	claimed := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	tx.From = &claimed // declared sender is not the signer
	b := syncedBlock(t, 12, tx)
	if err := ValidateSyncedBlockContents(context.Background(), b); err == nil {
		t.Fatal("block whose tx sender does not match its signature was accepted")
	}
	tx2 := signedTx(t, key, 0)
	tx2.V, tx2.R, tx2.S = nil, nil, nil
	if err := ValidateSyncedBlockContents(context.Background(), syncedBlock(t, 13, tx2)); err == nil {
		t.Fatal("block with an unsigned tx was accepted")
	}
}

// A typed transaction read back without its chain id still validates once the
// configured chain id is filled in.
func TestValidateSyncedBlockContents_FillsMissingChainID(t *testing.T) {
	Security.SetExpectedChainIDBig(testChainID)
	key, _ := crypto.GenerateKey()
	b := syncedBlock(t, 14, signedTx(t, key, 0))
	b.Transactions[0].ChainID = nil
	if err := ValidateSyncedBlockContents(context.Background(), b); err != nil {
		t.Fatalf("stored tx without chain id rejected: %v", err)
	}
}
