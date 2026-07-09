package config

import (
	"math/big"
	"testing"
)

// oldLiveEffectiveGasPrice reimplements, byte-for-byte, the fee logic that lived
// in messaging/BlockProcessing/Processing.go parseTransaction BEFORE the
// refactor to config.EffectiveGasPrice. It is the consensus-authoritative
// historical behaviour: every block already on chain was priced by this code.
//
// This golden copy exists so the test can prove the refactor changed NOTHING
// for the live path. Do not "fix" or modernise it — it is a historical record.
func oldLiveEffectiveGasPrice(txType uint8, gasPrice, maxFee, maxPriorityFee *big.Int) *big.Int {
	if txType == 2 { // EIP-1559
		const baseFeeWei = int64(35_000_000_000)

		mf := maxFee
		if mf == nil {
			mf = big.NewInt(baseFeeWei) // safe fallback
		}

		tip := maxPriorityFee
		if tip == nil {
			tip = new(big.Int)
		}
		basePlusTip := new(big.Int).Add(big.NewInt(baseFeeWei), tip)

		// effective = min(maxFee, baseFee + tip)
		if mf.Cmp(basePlusTip) <= 0 {
			return new(big.Int).Set(mf)
		}
		return basePlusTip
	}

	// Legacy / AccessList: GasPrice → MaxFee → MaxPriorityFee → 1 gwei.
	// NOTE: nil checks only — a present-but-zero value was honoured as zero.
	if gasPrice != nil {
		return new(big.Int).Set(gasPrice)
	}
	if maxFee != nil {
		return new(big.Int).Set(maxFee)
	}
	if maxPriorityFee != nil {
		return new(big.Int).Set(maxPriorityFee)
	}
	return big.NewInt(1_000_000_000) // DefaultGasPrice
}

// wei helpers for readable tables.
func gwei(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1_000_000_000)) }

// TestEffectiveGasPrice_MatchesHistoricalLivePath exhaustively compares the
// shared formula against the pre-refactor live formula across tx types and
// nil/zero/low/high permutations of every fee field. Any mismatch means the
// refactor changed consensus-visible behaviour — which would corrupt account
// reconciliation replaying historical blocks.
func TestEffectiveGasPrice_MatchesHistoricalLivePath(t *testing.T) {
	// nil, zero, below base (1 gwei), at base (35 gwei), above base (100 gwei)
	fieldValues := []*big.Int{nil, big.NewInt(0), gwei(1), gwei(35), gwei(100)}
	txTypes := []uint8{0, 1, 2}

	name := func(v *big.Int) string {
		if v == nil {
			return "nil"
		}
		return v.String()
	}

	for _, txType := range txTypes {
		for _, gp := range fieldValues {
			for _, mf := range fieldValues {
				for _, tip := range fieldValues {
					want := oldLiveEffectiveGasPrice(txType, gp, mf, tip)
					got := EffectiveGasPrice(txType, gp, mf, tip)
					if want.Cmp(got) != 0 {
						t.Errorf("type=%d gasPrice=%s maxFee=%s tip=%s: old live=%s, new=%s",
							txType, name(gp), name(mf), name(tip), want, got)
					}
				}
			}
		}
	}
}

// TestEffectiveGasPrice_DoesNotMutateInputs guards against the aliasing bug
// class: the returned value must be a fresh allocation, never one of the
// caller's *big.Int fields (mutating the result must not corrupt the tx).
func TestEffectiveGasPrice_DoesNotMutateInputs(t *testing.T) {
	gp := gwei(50)
	got := EffectiveGasPrice(0, gp, nil, nil)
	got.Add(got, big.NewInt(1))
	if gp.Cmp(gwei(50)) != 0 {
		t.Fatalf("EffectiveGasPrice returned an aliased input: gasPrice mutated to %s", gp)
	}
}

// TestGasFee_GasLimitFallback pins the GasLimit==0 behaviour: the live path
// always charged FallbackTxGasLimit (21000) × price, while the OLD deltas.go
// charged zero — one of the drifts that corrupted reconciliation. The shared
// GasFee must follow the live behaviour.
func TestGasFee_GasLimitFallback(t *testing.T) {
	price := EffectiveGasPrice(0, gwei(35), nil, nil)

	got := GasFee(0, 0, gwei(35), nil, nil)
	want := new(big.Int).Mul(new(big.Int).SetUint64(FallbackTxGasLimit), price)
	if want.Cmp(got) != 0 {
		t.Fatalf("GasFee(gasLimit=0): want %s (21000×price, live behaviour), got %s", want, got)
	}

	got = GasFee(0, 100_000, gwei(35), nil, nil)
	want = new(big.Int).Mul(big.NewInt(100_000), price)
	if want.Cmp(got) != 0 {
		t.Fatalf("GasFee(gasLimit=100000): want %s, got %s", want, got)
	}
}

// TestEffectiveGasPrice_PinnedExamples pins a few human-checkable values so a
// future "small" formula change fails loudly with concrete numbers.
func TestEffectiveGasPrice_PinnedExamples(t *testing.T) {
	cases := []struct {
		name                  string
		txType                uint8
		gasPrice, maxFee, tip *big.Int
		want                  *big.Int
	}{
		{"type2 maxFee above base+tip clamps", 2, nil, gwei(100), gwei(2), gwei(37)},
		{"type2 maxFee below base+tip passes", 2, nil, gwei(20), gwei(2), gwei(20)},
		{"type2 all nil falls back to base", 2, nil, nil, nil, gwei(35)},
		{"type2 zero maxFee honoured (min(0, base+tip)=0)", 2, nil, big.NewInt(0), gwei(2), big.NewInt(0)},
		{"legacy gasPrice wins", 0, gwei(50), gwei(100), gwei(2), gwei(50)},
		{"legacy zero gasPrice honoured as zero", 0, big.NewInt(0), gwei(100), nil, big.NewInt(0)},
		{"legacy all nil falls back to 1 gwei", 0, nil, nil, nil, gwei(1)},
	}
	for _, c := range cases {
		got := EffectiveGasPrice(c.txType, c.gasPrice, c.maxFee, c.tip)
		if c.want.Cmp(got) != 0 {
			t.Errorf("%s: want %s, got %s", c.name, c.want, got)
		}
	}
}
