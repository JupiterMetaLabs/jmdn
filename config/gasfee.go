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

import "math/big"

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

// GasFee returns gasLimit × EffectiveGasPrice with the FallbackTxGasLimit
// applied when gasLimit == 0. This is the total fee deducted from the sender
// and split between coinbase and ZKVM.
func GasFee(txType uint8, gasLimit uint64, gasPrice, maxFee, maxPriorityFee *big.Int) *big.Int {
	gl := gasLimit
	if gl == 0 {
		gl = FallbackTxGasLimit
	}
	return new(big.Int).Mul(
		new(big.Int).SetUint64(gl),
		EffectiveGasPrice(txType, gasPrice, maxFee, maxPriorityFee),
	)
}
