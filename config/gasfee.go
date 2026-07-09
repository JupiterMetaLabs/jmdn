// MODULE: config/gasfee.go
// PURPOSE: Canonical gas-fee math for JMDN transactions. Single source of
//          truth inside this repo — any consumer MUST use these instead of
//          re-deriving. If the base fee or formula changes, change it here only.
//
// NOTE: FastsyncV2/deltas.go operates on JMDN-FastSync types.Transaction
//       (a separate type) so it carries a local copy of effectiveGasPrice.
//       If this formula changes, update that copy in the same commit.

package config

import "math/big"

// BaseFeeWei is the flat network base fee used for EIP-1559 effective price
// calculation. Matches the constant in messaging/BlockProcessing/Processing.go.
const BaseFeeWei = int64(35_000_000_000) // 35 gwei

var (
	oneGwei = big.NewInt(1_000_000_000)
	baseFee = big.NewInt(BaseFeeWei)
)

// EffectiveGasPrice returns the effective gas price for a transaction.
//
//	EIP-1559 (type 2): min(maxFee, baseFee(35 gwei) + tip)  — matches Processing.go
//	Legacy   (0/1):    GasPrice → MaxFee → MaxPriorityFee → 1 Gwei
func EffectiveGasPrice(tx *Transaction) *big.Int {
	if tx == nil {
		return new(big.Int).Set(oneGwei)
	}
	switch tx.Type {
	case 2: // EIP-1559: effective = min(maxFee, baseFee + tip)
		maxFee := tx.MaxFee
		if maxFee == nil || maxFee.Sign() <= 0 {
			maxFee = new(big.Int).Set(baseFee) // safe fallback
		}
		tip := tx.MaxPriorityFee
		if tip == nil {
			tip = new(big.Int)
		}
		basePlusTip := new(big.Int).Add(baseFee, tip)
		if maxFee.Cmp(basePlusTip) <= 0 {
			return new(big.Int).Set(maxFee)
		}
		return new(big.Int).Set(basePlusTip)
	default: // Legacy / EIP-2930
		if tx.GasPrice != nil && tx.GasPrice.Sign() > 0 {
			return new(big.Int).Set(tx.GasPrice)
		}
		if tx.MaxFee != nil && tx.MaxFee.Sign() > 0 {
			return new(big.Int).Set(tx.MaxFee)
		}
		if tx.MaxPriorityFee != nil && tx.MaxPriorityFee.Sign() > 0 {
			return new(big.Int).Set(tx.MaxPriorityFee)
		}
	}
	return new(big.Int).Set(oneGwei)
}

// GasFee returns gasLimit * EffectiveGasPrice — the total fee charged to the
// sender, split between coinbase (half + remainder) and ZKVM (half).
func GasFee(tx *Transaction) *big.Int {
	if tx == nil || tx.GasLimit == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), EffectiveGasPrice(tx))
}
