package BlockProcessing

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

// A block with transactions but no coinbase or zkVM address is refused before
// any state is touched (the apply loop dereferences both).
func TestProcessBlockTransactions_RejectsMissingFeeAddresses(t *testing.T) {
	from := common.HexToAddress("0x01")
	to := common.HexToAddress("0x02")
	tx := config.Transaction{Hash: common.HexToHash("0xaa"), From: &from, To: &to, Value: big.NewInt(1)}
	zk := common.HexToAddress("0x03")
	for name, b := range map[string]*config.ZKBlock{
		"no coinbase": {BlockNumber: 5, Transactions: []config.Transaction{tx}, ZKVMAddr: &zk},
		"no zkvm":     {BlockNumber: 6, Transactions: []config.Transaction{tx}, CoinbaseAddr: &zk},
	} {
		err := ProcessBlockTransactions(context.Background(), b, nil)
		if err == nil || !strings.Contains(err.Error(), "no coinbase or zkVM address") {
			t.Errorf("%s: want a refusal, got %v", name, err)
		}
	}
	if err := ProcessBlockTransactions(context.Background(), nil, nil); err == nil {
		t.Error("nil block must be refused")
	}
}
