package Security

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

func TestSubmissionPolicy_RejectsUnprotectedLegacy(t *testing.T) {
	from := common.HexToAddress("0x01")
	tx := &config.Transaction{From: &from, Type: types.LegacyTxType, V: big.NewInt(27)}
	SetAllowUnprotectedTxs(false)
	if err := checkSubmissionPolicy(tx, nil); err == nil {
		t.Fatal("pre-EIP-155 legacy tx must be rejected by default")
	}
	SetAllowUnprotectedTxs(true)
	t.Cleanup(func() { SetAllowUnprotectedTxs(false) })
	if err := checkSubmissionPolicy(tx, nil); err != nil {
		t.Fatalf("allowed when enabled: %v", err)
	}
	protected := &config.Transaction{From: &from, Type: types.LegacyTxType, V: big.NewInt(2*8000800 + 35)}
	SetAllowUnprotectedTxs(false)
	if err := checkSubmissionPolicy(protected, nil); err != nil {
		t.Fatalf("EIP-155 legacy tx must pass: %v", err)
	}
}

func TestSubmissionPolicy_BoundsNonceGap(t *testing.T) {
	from := common.HexToAddress("0x02")
	cache := NewSecurityCache()
	defer cache.Close()
	cache.RegisterAccount(from, &DB_OPs.Account{Address: from, TxNonce: 10})
	ok := &config.Transaction{From: &from, Type: types.DynamicFeeTxType, Nonce: 10 + MaxSubmissionNonceGap}
	if err := checkSubmissionPolicy(ok, cache); err != nil {
		t.Fatalf("gap at the limit must pass: %v", err)
	}
	far := &config.Transaction{From: &from, Type: types.DynamicFeeTxType, Nonce: 11 + MaxSubmissionNonceGap}
	if err := checkSubmissionPolicy(far, cache); err == nil {
		t.Fatal("nonce beyond the gap limit must be rejected")
	}
}
