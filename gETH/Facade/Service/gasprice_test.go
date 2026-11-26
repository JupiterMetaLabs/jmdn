package Service

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"

	"gossipnode/config"
)

// eth_gasPrice suggests the protocol base fee (35 gwei), not 1 gwei: a wallet
// that prices legacy transactions from it must not land under the base fee.
func TestGetGasPriceIsBaseFee(t *testing.T) {
	got, err := (&ServiceImpl{}).GetGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "0x826299e00" { // 35_000_000_000
		t.Fatalf("eth_gasPrice = %s, want 0x826299e00 (35 gwei)", got)
	}
	v, err := hexutil.DecodeBig(got)
	if err != nil || v.Int64() != config.BaseFeeWei {
		t.Fatalf("eth_gasPrice = %s, want config.BaseFeeWei", got)
	}
}

// The priority-fee suggestion is unchanged (1 gwei), so baseFee + tip = 36 gwei.
func TestGetMaxPriorityFeePerGasUnchanged(t *testing.T) {
	got, err := (&ServiceImpl{}).GetMaxPriorityFeePerGas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "0x3b9aca00" {
		t.Fatalf("eth_maxPriorityFeePerGas = %s, want 0x3b9aca00 (1 gwei)", got)
	}
}
