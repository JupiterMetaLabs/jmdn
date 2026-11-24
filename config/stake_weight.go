package config

import "math/big"

// Buddy staking-reward weight constants. These map a reward address's on-chain
// balance (wei) to the uint64 weight consumed by SplitFee. They are
// CONSENSUS-CRITICAL and must be identical network-wide: two nodes with
// different constants compute different FeeRecipient weights for the same block,
// split fees differently, and diverge on the resulting balances / state
// fingerprint. Change them only as a coordinated fleet-wide flip.
//
// See docs/STAKING-REWARDS-DESIGN.md.
const (
	// BaselineWeight is the floor every address-having participant receives,
	// regardless of balance. It guarantees a ZERO-BALANCE buddy still earns a
	// share (the requirement "if the address has 0 JMDN it can still get the
	// reward"). Must be >= 1.
	BaselineWeight uint64 = 1

	// WeightCap bounds the balance-derived component so a single whale address
	// cannot dominate the split (and so the total weight can never overflow
	// uint64 across a bounded committee). The effective per-address weight is at
	// most BaselineWeight + WeightCap.
	WeightCap uint64 = 1_000_000
)

// WeightScaleWei is the wei-per-weight-unit divisor: one weight unit per whole
// JMDN (1e18 wei). A var (not const) only because big.Int cannot be a Go
// constant; it is never mutated at runtime. Consensus-critical, network-uniform.
var WeightScaleWei = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil) // 1e18

// StakeWeight maps a reward address's balance (wei) to its FeeRecipient weight,
// deterministically:
//
//	weight = BaselineWeight + min(WeightCap, floor(balanceWei / WeightScaleWei))
//
// A nil, zero, or negative balance yields exactly BaselineWeight, so a
// zero-balance participant still earns the baseline share. The balance-derived
// term is integer whole-JMDN, capped at WeightCap. The result is a pure function
// of the input — every node computes the identical weight for the identical
// parent-state balance, which is what lets the fee split be recomputed and
// validated fleet-wide.
func StakeWeight(balanceWei *big.Int) uint64 {
	if balanceWei == nil || balanceWei.Sign() <= 0 {
		return BaselineWeight
	}
	scaled := new(big.Int).Div(balanceWei, WeightScaleWei) // floor(balance / 1e18)
	capWeight := new(big.Int).SetUint64(WeightCap)
	if scaled.Cmp(capWeight) > 0 {
		scaled = capWeight
	}
	// scaled is now in [0, WeightCap], safe to fit uint64 and add to baseline.
	return BaselineWeight + scaled.Uint64()
}
