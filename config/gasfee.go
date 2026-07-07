// MODULE: config/gasfee.go
// PURPOSE: Canonical gas-fee math for JMDN transactions. Single source of
//          truth inside this repo — the write path (backend/tx.go), the
//          historical-balance reader (DB_OPs/historical_balance.go), and any
//          future consumer MUST use these instead of re-deriving.
//
// NOTE: FastsyncV2/deltas.go carries the same rules for types.Transaction
//       (FastSync module type). If these change, change both in one commit.

package config

import "math/big"

var oneGwei = big.NewInt(1_000_000_000)

// EffectiveGasPrice returns the effective gas price for a transaction.
//
//	EIP-1559 (type 2): MaxFee → MaxPriorityFee → GasPrice → 1 Gwei
//	Legacy   (0/1):    GasPrice → MaxFee → MaxPriorityFee → 1 Gwei
func EffectiveGasPrice(tx *Transaction) *big.Int {
	if tx == nil {
		return new(big.Int).Set(oneGwei)
	}
	switch tx.Type {
	case 2: // EIP-1559
		if tx.MaxFee != nil && tx.MaxFee.Sign() > 0 {
			return tx.MaxFee
		}
		if tx.MaxPriorityFee != nil && tx.MaxPriorityFee.Sign() > 0 {
			return tx.MaxPriorityFee
		}
		if tx.GasPrice != nil && tx.GasPrice.Sign() > 0 {
			return tx.GasPrice
		}
	default: // Legacy / EIP-2930
		if tx.GasPrice != nil && tx.GasPrice.Sign() > 0 {
			return tx.GasPrice
		}
		if tx.MaxFee != nil && tx.MaxFee.Sign() > 0 {
			return tx.MaxFee
		}
		if tx.MaxPriorityFee != nil && tx.MaxPriorityFee.Sign() > 0 {
			return tx.MaxPriorityFee
		}
	}
	return new(big.Int).Set(oneGwei)
}

// GasFee returns gasLimit * EffectiveGasPrice — the fee charged to the sender
// and split between coinbase (half + remainder) and ZKVM (half).
func GasFee(tx *Transaction) *big.Int {
	if tx == nil || tx.GasLimit == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), EffectiveGasPrice(tx))
}
