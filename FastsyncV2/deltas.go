package FastsyncV2

// computeAccountDeltas performs a single forward pass over the locally stored blocks
// in [fromBlock..toBlock] and computes per-account balance/nonce deltas.
//
// This replaces the prior per-account GetTransactionsForAccountInRange scan:
// instead of O(accounts × blocks) DB queries, this is one O(blocks) iterator pass.
//
// Balance rules follow processBlockTransactions in messaging/BlockProcessing/Processing.go:
//
//	Sender    → deduct value + gasFee; advance Nonce and TxCountSent
//	Receiver  → credit value
//	Coinbase  → credit gasFee/2 + gasFee%2  (half + remainder)
//	ZKVM      → credit gasFee/2
//
// Gas fee:
//
//	EIP-1559 (type 2): effectiveGasPrice = MaxFee ?? MaxPriorityFee ?? GasPrice ?? 1e9
//	Legacy   (type 0/1): effectiveGasPrice = GasPrice ?? MaxFee ?? MaxPriorityFee ?? 1e9
//	gasFee = gasLimit * effectiveGasPrice

import (
	"math/big"
	"strings"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

// computeAccountDeltas iterates all blocks in [fromBlock..toBlock] and returns a map
// of lowercase-hex-address → *types.AccountDelta. Accounts not touched in the range
// are absent from the map.
func (fs *FastsyncV2) computeAccountDeltas(fromBlock, toBlock uint64) map[string]*types.AccountDelta {
	const batchSize = 500
	iter := fs.blockInfoAdapter.NewBlockIterator(fromBlock, toBlock, batchSize)
	defer iter.Close()

	deltas := make(map[string]*types.AccountDelta)

	for {
		batch, err := iter.Next()
		if err != nil || len(batch) == 0 {
			break
		}
		for _, blk := range batch {
			if blk == nil {
				continue
			}
			applyBlockDeltas(blk, deltas)
		}
	}

	return deltas
}

// applyBlockDeltas applies the transaction effects of one ZKBlock to the delta map.
func applyBlockDeltas(blk *types.ZKBlock, deltas map[string]*types.AccountDelta) {
	if blk == nil {
		return
	}
	var coinbaseAddr, zkvmAddr string
	if blk.CoinbaseAddr != nil {
		coinbaseAddr = strings.ToLower(blk.CoinbaseAddr.Hex())
	}
	if blk.ZKVMAddr != nil {
		zkvmAddr = strings.ToLower(blk.ZKVMAddr.Hex())
	}

	for i := range blk.Transactions {
		tx := &blk.Transactions[i]

		var fromAddr, toAddr string
		if tx.From != nil {
			fromAddr = strings.ToLower(tx.From.Hex())
		}
		if tx.To != nil {
			toAddr = strings.ToLower(tx.To.Hex())
		}

		gasFee := computeGasFee(tx)

		halfGas := new(big.Int).Div(gasFee, big.NewInt(2))
		remainder := new(big.Int).Mod(gasFee, big.NewInt(2))
		coinbaseGas := new(big.Int).Add(halfGas, remainder)
		zkvmGas := new(big.Int).Set(halfGas)

		// Sender: deduct value + gasFee; advance nonce; increment TxCountSent
		if fromAddr != "" {
			d := getDelta(deltas, fromAddr)
			d.BalanceDelta.Sub(d.BalanceDelta, gasFee)
			if tx.Value != nil && tx.Value.Sign() > 0 {
				d.BalanceDelta.Sub(d.BalanceDelta, tx.Value)
			}
			if tx.Nonce > d.Nonce {
				d.Nonce = tx.Nonce
				d.TxNonce = tx.Nonce + 1
			}
			d.TxCountSent++
			d.IsSender = true
		}

		// Receiver: credit value only
		if toAddr != "" && tx.Value != nil && tx.Value.Sign() > 0 {
			d := getDelta(deltas, toAddr)
			d.BalanceDelta.Add(d.BalanceDelta, tx.Value)
		}

		// Coinbase: credit half + remainder of gasFee
		if coinbaseAddr != "" {
			d := getDelta(deltas, coinbaseAddr)
			d.BalanceDelta.Add(d.BalanceDelta, coinbaseGas)
		}

		// ZKVM: credit exact half of gasFee
		if zkvmAddr != "" {
			d := getDelta(deltas, zkvmAddr)
			d.BalanceDelta.Add(d.BalanceDelta, zkvmGas)
		}
	}
}

// getDelta returns the existing delta for addr, creating a zero entry if absent.
func getDelta(deltas map[string]*types.AccountDelta, addr string) *types.AccountDelta {
	d, ok := deltas[addr]
	if !ok {
		d = &types.AccountDelta{BalanceDelta: big.NewInt(0)}
		deltas[addr] = d
	}
	return d
}

// computeGasFee returns gasLimit * effectiveGasPrice following Processing.go rules.
func computeGasFee(tx *types.Transaction) *big.Int {
	if tx.GasLimit == 0 {
		return big.NewInt(0)
	}
	gasLimit := new(big.Int).SetUint64(tx.GasLimit)
	effectivePrice := effectiveGasPrice(tx)
	return new(big.Int).Mul(gasLimit, effectivePrice)
}

var oneGwei = big.NewInt(1_000_000_000)

// effectiveGasPrice returns the effective gas price for a transaction.
//
//	EIP-1559 (type 2): MaxFee → MaxPriorityFee → GasPrice → 1 Gwei
//	Legacy   (0/1):    GasPrice → MaxFee → MaxPriorityFee → 1 Gwei
func effectiveGasPrice(tx *types.Transaction) *big.Int {
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
	return oneGwei
}
