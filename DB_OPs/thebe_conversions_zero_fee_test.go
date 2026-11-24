package DB_OPs

// A transaction with a zero gas price is charged a fee of 0 when its block is
// applied live: the decoded transaction carries a non-nil big.Int(0), and
// config.GasFee multiplies by exactly that. DB_OPs/backend/tx.go then persists
// the field as "0", because it writes whenever the pointer is non-nil.
//
// txRecordToTransaction used to skip "0" alongside "" when reading those fields
// back, so the replayed transaction carried a NIL fee — and nil is what
// config.EffectiveGasPrice treats as "unset", falling back to
// FallbackGasPriceWei for a legacy transaction and BaseFeeWei for a type-2 one.
// ApplyBlockRecon (account_recon.go) therefore charged a fee the live apply
// never charged, giving the reconciling node a different sender balance from
// every node that applied the block live. Balances feed the account-state
// fingerprint, so that node fails its own fingerprint check and halts.
//
// Nothing in this repo stops a zero-fee transaction reaching a block:
// Security.AllChecks rejects only NEGATIVE fees, and the 35 gwei floor exists
// only in the eth_gasPrice suggestion. A misconfigured wallet is enough.

import (
	"math/big"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// recWithFees builds a stored transaction row carrying the given fee strings.
func recWithFees(txType int16, gasPrice, maxFee, maxPriority string) *thebegateway.TransactionRecord {
	return &thebegateway.TransactionRecord{
		TxHash:            "0xaa",
		BlockNumber:       7,
		FromAddr:          "0x00000000000000000000000000000000000000a1",
		ValueWei:          "1",
		Nonce:             "0",
		Type:              txType,
		GasLimit:          "21000",
		GasPriceWei:       gasPrice,
		MaxFeeWei:         maxFee,
		MaxPriorityFeeWei: maxPriority,
	}
}

// feeOf replays what both apply paths compute: config.GasFee over the converted
// transaction. Live apply (BlockProcessing/Processing.go) and reconciliation
// (account_recon.go) call the identical function — the only thing that ever
// differed was the transaction handed to it.
func feeOf(t *testing.T, rec *thebegateway.TransactionRecord) *big.Int {
	t.Helper()
	tx := txRecordToTransaction(rec)
	if tx == nil {
		t.Fatal("txRecordToTransaction returned nil")
	}
	return config.GasFee(tx.Type, tx.GasLimit, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee)
}

// A stored "0" must read back as a real zero, so reconciliation charges the
// same 0 the live apply charged.
func TestTxRecordToTransaction_StoredZeroFeeIsNotTreatedAsAbsent(t *testing.T) {
	// Legacy: live charged 0. Before the fix this read back nil and
	// EffectiveGasPrice fell through to FallbackGasPriceWei (1 gwei),
	// charging 21000 * 1e9 = 21_000_000_000_000 wei.
	if got := feeOf(t, recWithFees(0, "0", "", "")); got.Sign() != 0 {
		t.Fatalf("legacy zero-gas-price tx: reconciliation charged %s, live charges 0", got)
	}

	// Type 2: live charged 0. Before the fix a nil MaxFee fell through to
	// BaseFeeWei (35 gwei), charging 21000 * 35e9 = 735_000_000_000_000 wei.
	if got := feeOf(t, recWithFees(2, "", "0", "0")); got.Sign() != 0 {
		t.Fatalf("type-2 zero-fee tx: reconciliation charged %s, live charges 0", got)
	}
}

// The fix must not change a transaction that genuinely carries no fee field.
// Only "" means absent — tx.go writes a field only when the pointer is non-nil,
// and the columns have no DEFAULT — so an empty string must still fall back.
func TestTxRecordToTransaction_AbsentFeeStillFallsBack(t *testing.T) {
	legacy := feeOf(t, recWithFees(0, "", "", ""))
	wantLegacy := new(big.Int).Mul(big.NewInt(21000), big.NewInt(config.FallbackGasPriceWei))
	if legacy.Cmp(wantLegacy) != 0 {
		t.Fatalf("legacy absent fee: got %s, want the %s fallback", legacy, wantLegacy)
	}

	typed := feeOf(t, recWithFees(2, "", "", ""))
	wantTyped := new(big.Int).Mul(big.NewInt(21000), big.NewInt(config.BaseFeeWei))
	if typed.Cmp(wantTyped) != 0 {
		t.Fatalf("type-2 absent fee: got %s, want the %s base fee", typed, wantTyped)
	}
}

// A non-zero stored fee is unaffected.
func TestTxRecordToTransaction_NonZeroFeeRoundTrips(t *testing.T) {
	got := feeOf(t, recWithFees(0, "7000000000", "", ""))
	want := new(big.Int).Mul(big.NewInt(21000), big.NewInt(7_000_000_000))
	if got.Cmp(want) != 0 {
		t.Fatalf("got %s, want %s", got, want)
	}
}
