package messaging

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/Security"
	"gossipnode/config"
)

// checkBodyBinding on a bound block: the header hash binds, and a relay that
// changes the coinbase, zkvm address or timestamp is rejected.
func TestCheckBodyBinding_HeaderBound(t *testing.T) {
	Security.SetHeaderBinding(Security.HeaderBindingConfig{FromHeight: 10})
	defer Security.SetHeaderBinding(Security.HeaderBindingConfig{})

	key, _ := crypto.GenerateKey()
	txs := []config.Transaction{signedTx(t, key, 0)}
	cb, zk := common.HexToAddress("0xc1"), common.HexToAddress("0xc2")
	b := &config.ZKBlock{BlockNumber: 12, Transactions: txs, CoinbaseAddr: &cb, ZKVMAddr: &zk, Timestamp: 1_700_000_000,
		TxnsRoot: RecomputeTxnsRoot(txs)}
	b.BlockHash = Security.HeaderBlockHash(RecomputeBlockHashFromTxs(txs), &cb, &zk, b.Timestamp)
	if rej := checkBodyBinding(b); rej != nil {
		t.Fatalf("honest bound block rejected: %v", rej.err)
	}
	attacker := common.HexToAddress("0xa77ac4e7")
	for name, edit := range map[string]func(*config.ZKBlock){
		"coinbase":  func(x *config.ZKBlock) { x.CoinbaseAddr = &attacker },
		"zkvm":      func(x *config.ZKBlock) { x.ZKVMAddr = &attacker },
		"timestamp": func(x *config.ZKBlock) { x.Timestamp++ },
	} {
		c := *b
		edit(&c)
		if rej := checkBodyBinding(&c); rej == nil || rej.reason != "body_mismatch" {
			t.Fatalf("changed %s: %v", name, rej)
		}
	}
	// The pre-activation (transactions-only) hash is refused at a bound height.
	c := *b
	c.BlockHash = RecomputeBlockHashFromTxs(txs)
	if rej := checkBodyBinding(&c); rej == nil {
		t.Fatal("transactions-only hash accepted at a bound height")
	}
}
