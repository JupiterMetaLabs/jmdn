package config

import (
	"math"
	"math/big"
	"testing"
)

// The fee formula has exactly one implementation: config.GasFee. Three call
// sites depend on it agreeing with itself across the whole input domain —
// validation (Security/security_cache.go), live execution
// (messaging/BlockProcessing/Processing.go) and reconciliation
// (DB_OPs/account_recon.go). They previously did NOT all call it: live
// execution re-derived the product inline as
//
//	big.NewInt(int64(tx.GasLimit)) * EffectiveGasPrice(...)
//
// which is equal for realistic inputs but produces a NEGATIVE fee once
// GasLimit >= 2^63, because the uint64→int64 conversion wraps. A negative fee
// credits the sender and debits the fee recipients.
//
// This test pins the property that made that inline copy wrong, so the formula
// cannot be forked back in without a failure. config/gasfee.go's header records
// the earlier drift that corrupted balances on every reconciliation of an
// EIP-1559 transaction.
func TestGasFee_NeverNegative_AcrossGasLimitDomain(t *testing.T) {
	price := big.NewInt(1_000_000_000) // 1 gwei, legacy tx

	cases := []struct {
		name     string
		gasLimit uint64
	}{
		{"zero applies the fallback", 0},
		{"typical transfer", 21_000},
		{"large but sane", 30_000_000},
		{"exactly 2^63 — the int64 wrap point", 1 << 63},
		{"above 2^63", (1 << 63) + 12345},
		{"max uint64", math.MaxUint64},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GasFee(0, tc.gasLimit, price, nil, nil)
			if got.Sign() < 0 {
				t.Fatalf("GasFee(gasLimit=%d) = %s — negative fees credit the sender", tc.gasLimit, got)
			}

			// Independent recomputation from the exported primitives, so this is
			// not just GasFee agreeing with itself.
			want := new(big.Int).Mul(
				new(big.Int).SetUint64(EffectiveGasLimit(tc.gasLimit)),
				EffectiveGasPrice(0, price, nil, nil),
			)
			if got.Cmp(want) != 0 {
				t.Fatalf("GasFee(gasLimit=%d) = %s, want %s", tc.gasLimit, got, want)
			}

			// EQUIVALENCE. Reproduce exactly what live execution used to compute:
			//
			//   gasLimit = tx.GasLimit != 0 ? big.NewInt(int64(tx.GasLimit))
			//                               : big.NewInt(DefaultGasLimit /* 21000 */)
			//   fee      = gasLimit * parsedTx.EffectiveGasFee
			//
			// where parsedTx.EffectiveGasFee was itself
			// EffectiveGasPrice(tx.Type, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee)
			// (parseTransaction, messaging/BlockProcessing/Processing.go).
			//
			// Below the int64 wrap the two MUST agree exactly — that is what makes
			// switching live execution to config.GasFee a no-op for every real
			// transaction. At or above 2^63 they must diverge, because that is the
			// bug being removed.
			var oldGasLimit *big.Int
			if tc.gasLimit != 0 {
				oldGasLimit = big.NewInt(int64(tc.gasLimit))
			} else {
				oldGasLimit = big.NewInt(21_000) // BlockProcessing.DefaultGasLimit
			}
			oldFee := new(big.Int).Mul(oldGasLimit, EffectiveGasPrice(0, price, nil, nil))

			if tc.gasLimit < 1<<63 {
				if oldFee.Cmp(got) != 0 {
					t.Fatalf("BEHAVIOUR CHANGE at gasLimit=%d: old inline form = %s, config.GasFee = %s",
						tc.gasLimit, oldFee, got)
				}
			} else if oldFee.Sign() >= 0 {
				t.Errorf("expected the old int64 form to go negative at gasLimit=%d, got %s", tc.gasLimit, oldFee)
			}
		})
	}
}

// Equivalence across the realistic gas-limit domain and every transaction type,
// including EIP-1559 where EffectiveGasPrice clamps maxFee to baseFee+tip. This
// is the regression net for "switching live execution to config.GasFee changed
// no balance": if any real transaction would be charged differently, it fails.
func TestGasFee_MatchesRetiredInlineForm(t *testing.T) {
	gwei := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1_000_000_000)) }

	types := []struct {
		txType                   uint8
		gasPrice, maxFee, maxTip *big.Int
	}{
		{0, gwei(1), nil, nil},          // legacy, explicit price
		{0, nil, nil, nil},              // legacy, all nil -> fallback price
		{0, nil, gwei(50), nil},         // legacy, maxFee only
		{1, gwei(7), nil, nil},          // access-list
		{2, nil, gwei(20), gwei(1)},     // 1559, maxFee under base+tip
		{2, nil, gwei(500), gwei(2)},    // 1559, maxFee above base+tip -> clamped
		{2, nil, nil, gwei(3)},          // 1559, maxFee nil -> BaseFeeWei default
		{2, gwei(9), gwei(40), gwei(1)}, // 1559 with a stray gasPrice set
	}
	limits := []uint64{0, 1, 21_000, 100_000, 30_000_000, math.MaxInt64}

	for _, ty := range types {
		for _, gl := range limits {
			got := GasFee(ty.txType, gl, ty.gasPrice, ty.maxFee, ty.maxTip)

			var oldGasLimit *big.Int
			if gl != 0 {
				oldGasLimit = big.NewInt(int64(gl))
			} else {
				oldGasLimit = big.NewInt(21_000)
			}
			want := new(big.Int).Mul(oldGasLimit, EffectiveGasPrice(ty.txType, ty.gasPrice, ty.maxFee, ty.maxTip))

			if got.Cmp(want) != 0 {
				t.Errorf("type=%d gasLimit=%d: config.GasFee = %s, retired inline form = %s",
					ty.txType, gl, got, want)
			}
		}
	}
}

// EffectiveGasLimit is the single definition of the zero-gas-limit fallback.
// messaging/BlockProcessing reports it in trace attributes and log lines, so it
// must stay exactly FallbackTxGasLimit — a second copy is how this drifted
// before.
func TestEffectiveGasLimit_FallbackIsSingleSourced(t *testing.T) {
	if got := EffectiveGasLimit(0); got != FallbackTxGasLimit {
		t.Fatalf("EffectiveGasLimit(0) = %d, want FallbackTxGasLimit (%d)", got, FallbackTxGasLimit)
	}
	if got := EffectiveGasLimit(21_000); got != 21_000 {
		t.Fatalf("EffectiveGasLimit(21000) = %d, want 21000 (declared value must pass through)", got)
	}
}
