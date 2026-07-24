package BlockProcessing

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// Execution defense-in-depth: parseTransaction must refuse to produce a
// parsed transaction with a negative value, so a tx that reaches execution
// without passing the ingress/remote value gates still cannot reach the
// balance arithmetic.
func TestParseTransaction_RejectsNegativeValue(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := config.Transaction{
		From:  &addr,
		To:    &addr,
		Type:  0,
		Value: big.NewInt(-100),
	}
	if _, err := parseTransaction(tx); err == nil {
		t.Fatalf("SECURITY (C-03): parseTransaction accepted a negative value")
	}
}

// A non-negative value still parses (the guard must not brick the honest path).
func TestParseTransaction_AcceptsNonNegativeValue(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := config.Transaction{
		From:     &addr,
		To:       &addr,
		Type:     0,
		Value:    big.NewInt(100),
		GasPrice: big.NewInt(1),
	}
	if _, err := parseTransaction(tx); err != nil {
		t.Fatalf("non-negative value must parse, got %v", err)
	}
}
