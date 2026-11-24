package config

// UNTESTED-LOCALLY: no Go toolchain in the authoring environment. Run with:
//   go test ./config/ -run TestStakeWeight -v

import (
	"math/big"
	"testing"
)

func jmdn(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), WeightScaleWei)
}

func TestStakeWeight(t *testing.T) {
	cases := []struct {
		name string
		bal  *big.Int
		want uint64
	}{
		{"nil -> baseline", nil, BaselineWeight},
		{"zero -> baseline (0 JMDN still earns)", big.NewInt(0), BaselineWeight},
		{"negative -> baseline", big.NewInt(-5), BaselineWeight},
		{"sub-1-JMDN floors to 0 -> baseline", big.NewInt(999_999_999_999_999_999), BaselineWeight},
		{"exactly 1 JMDN", jmdn(1), BaselineWeight + 1},
		{"5 JMDN", jmdn(5), BaselineWeight + 5},
		{"at cap", jmdn(int64(WeightCap)), BaselineWeight + WeightCap},
		{"above cap clamps", jmdn(int64(WeightCap) + 10_000), BaselineWeight + WeightCap},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StakeWeight(c.bal)
			if got != c.want {
				t.Fatalf("StakeWeight(%v) = %d, want %d", c.bal, got, c.want)
			}
		})
	}
}

// Determinism: the same input always yields the same output, and the input is
// not mutated (SplitFee callers reuse the balance big.Int).
func TestStakeWeightPureAndNonMutating(t *testing.T) {
	bal := jmdn(42)
	snapshot := new(big.Int).Set(bal)
	first := StakeWeight(bal)
	for i := 0; i < 100; i++ {
		if StakeWeight(bal) != first {
			t.Fatal("StakeWeight is not a pure function of its input")
		}
	}
	if bal.Cmp(snapshot) != 0 {
		t.Fatalf("StakeWeight mutated its input: %v != %v", bal, snapshot)
	}
}
