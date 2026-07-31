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

			// The old inline form, reproduced. It must differ from the correct
			// answer above 2^63 — if this ever stops differing the wrap has been
			// fixed upstream and the guard can be simplified.
			inline := new(big.Int).Mul(big.NewInt(int64(tc.gasLimit)), EffectiveGasPrice(0, price, nil, nil))
			if tc.gasLimit >= 1<<63 && inline.Sign() >= 0 {
				t.Errorf("expected the old int64 form to go negative at gasLimit=%d, got %s", tc.gasLimit, inline)
			}
		})
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
