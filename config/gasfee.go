// MODULE: config/gasfee
// PURPOSE: Single source of truth for the consensus gas-fee formula.
//
// Both balance-mutation paths MUST use these functions:
//   - Live execution:      messaging/BlockProcessing/Processing.go (parseTransaction)
//   - Delta reconciliation: FastsyncV2/deltas.go (computeAccountDeltas)
//
// HISTORY: Before this file existed the two paths drifted — live used
// min(maxFee, 35 gwei baseFee + tip) with a 35 gwei fallback, while
// reconciliation used raw MaxFee with a 1 gwei fallback and no clamp.
// Every reconciled type-2 transaction with maxFee > baseFee+tip therefore
// recomputed a DIFFERENT balance than the one consensus applied, corrupting
// account state on every catchup. Do not fork this logic again.
package config

import (
	"bytes"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
)

// FeeRecipient is one destination for a share of the coinbase-side gas fee,
// weighted relative to the other recipients. It is carried on the block. An
// empty recipient list means the whole coinbase share goes to the single
// coinbase address (the historical behavior).
type FeeRecipient struct {
	Addr   common.Address `json:"addr"`
	Weight uint64         `json:"weight"`
}

// FeeCredit is a resolved (address, amount) crediting instruction produced by
// SplitFee: the caller adds Amount to Addr's balance.
type FeeCredit struct {
	Addr   common.Address
	Amount *big.Int
}

// SplitFee divides a transaction's total gasFee into the ZKVM share and the
// coinbase-side distribution, deterministically and to the exact wei. This is
// the SINGLE source of truth for fee distribution: every balance-mutation path
// (live execution and catch-up reconciliation) MUST call it so they can never
// disagree on a single wei.
//
//	zkvmShare     = floor(gasFee / 2)               -> credited to the ZKVM address
//	coinbaseShare = gasFee - zkvmShare (= ceil/2)   -> distributed as below
//
// Distribution of coinbaseShare:
//   - recipients empty (or total weight 0) -> one credit of coinbaseShare to
//     `coinbase` (byte-identical to the historical single-coinbase split, where
//     the odd-wei remainder stayed with the coinbase).
//   - recipients set -> split by integer weight, credit_i =
//     coinbaseShare*weight_i/totalWeight, with the leftover remainder added to
//     the FIRST recipient in canonical order (sorted by address bytes) so the
//     total is exact and identical on every node regardless of input ordering.
//
// INVARIANT (guaranteed): zkvmShare + Σ(returned amounts) == gasFee.
func SplitFee(gasFee *big.Int, coinbase common.Address, recipients []FeeRecipient) (zkvmShare *big.Int, coinbaseCredits []FeeCredit) {
	g := gasFee
	if g == nil {
		g = new(big.Int)
	}
	two := big.NewInt(2)
	zkvmShare = new(big.Int).Div(g, two)            // floor(gasFee/2)
	coinbaseShare := new(big.Int).Sub(g, zkvmShare) // ceil(gasFee/2) = half + odd-wei remainder

	total := new(big.Int)
	for _, r := range recipients {
		if r.Weight > 0 {
			total.Add(total, new(big.Int).SetUint64(r.Weight))
		}
	}
	// Empty / all-zero-weight -> single coinbase credit (historical behavior).
	if len(recipients) == 0 || total.Sign() == 0 {
		return zkvmShare, []FeeCredit{{Addr: coinbase, Amount: coinbaseShare}}
	}

	// Canonical order: sort a copy by address bytes so distribution is identical
	// regardless of the block's recipient ordering.
	ordered := make([]FeeRecipient, len(recipients))
	copy(ordered, recipients)
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare(ordered[i].Addr.Bytes(), ordered[j].Addr.Bytes()) < 0
	})

	credits := make([]FeeCredit, 0, len(ordered))
	distributed := new(big.Int)
	for _, r := range ordered {
		if r.Weight == 0 {
			continue
		}
		amt := new(big.Int).Mul(coinbaseShare, new(big.Int).SetUint64(r.Weight))
		amt.Div(amt, total)
		credits = append(credits, FeeCredit{Addr: r.Addr, Amount: amt})
		distributed.Add(distributed, amt)
	}
	// Assign the leftover remainder to the first (canonical) recipient so the sum
	// is exact.
	if rem := new(big.Int).Sub(coinbaseShare, distributed); rem.Sign() != 0 && len(credits) > 0 {
		credits[0].Amount.Add(credits[0].Amount, rem)
	}
	return zkvmShare, credits
}

const (
	// BaseFeeWei is JMDN's flat protocol base fee (35 gwei).
	// Also hardcoded for RPC display in gETH/Facade (eth_gasPrice / fee history);
	// keep those in sync if this changes.
	BaseFeeWei = int64(35_000_000_000)

	// FallbackGasPriceWei is the last-resort effective gas price for legacy
	// transactions that carry no fee fields at all (1 gwei).
	FallbackGasPriceWei = int64(1_000_000_000)

	// FallbackTxGasLimit is used when a transaction carries GasLimit == 0 (21000).
	FallbackTxGasLimit = uint64(21000)
)

// EffectiveGasPrice returns the consensus effective gas price for a transaction.
//
//	EIP-1559 (type 2): min(maxFee, BaseFeeWei + tip)
//	                   nil maxFee → BaseFeeWei; nil tip → 0
//	Legacy / AccessList (type 0/1): GasPrice → MaxFee → MaxPriorityFee → FallbackGasPriceWei
//	                   (nil checks only — a present-but-zero GasPrice is honoured as zero)
//
// The returned *big.Int is always a fresh allocation — callers may mutate it.
func EffectiveGasPrice(txType uint8, gasPrice, maxFee, maxPriorityFee *big.Int) *big.Int {
	if txType == 2 {
		mf := maxFee
		if mf == nil {
			mf = big.NewInt(BaseFeeWei)
		}
		tip := maxPriorityFee
		if tip == nil {
			tip = new(big.Int)
		}
		basePlusTip := new(big.Int).Add(big.NewInt(BaseFeeWei), tip)
		if mf.Cmp(basePlusTip) <= 0 {
			return new(big.Int).Set(mf)
		}
		return basePlusTip
	}

	if gasPrice != nil {
		return new(big.Int).Set(gasPrice)
	}
	if maxFee != nil {
		return new(big.Int).Set(maxFee)
	}
	if maxPriorityFee != nil {
		return new(big.Int).Set(maxPriorityFee)
	}
	return big.NewInt(FallbackGasPriceWei)
}

// EffectiveGasLimit returns the gas limit actually charged for a transaction:
// the declared value, or FallbackTxGasLimit when it is 0. Exported so callers
// that need to report the charged limit (trace attributes, log lines) can do so
// without re-implementing the fallback — duplicating it is how the fee formula
// drifted before.
func EffectiveGasLimit(gasLimit uint64) uint64 {
	if gasLimit == 0 {
		return FallbackTxGasLimit
	}
	return gasLimit
}

// GasFee returns gasLimit × EffectiveGasPrice with the FallbackTxGasLimit
// applied when gasLimit == 0. This is the total fee deducted from the sender
// and split between coinbase and ZKVM.
func GasFee(txType uint8, gasLimit uint64, gasPrice, maxFee, maxPriorityFee *big.Int) *big.Int {
	return new(big.Int).Mul(
		new(big.Int).SetUint64(EffectiveGasLimit(gasLimit)),
		EffectiveGasPrice(txType, gasPrice, maxFee, maxPriorityFee),
	)
}
