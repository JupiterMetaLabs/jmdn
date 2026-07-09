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
// Gas fee: computed by config.GasFee / config.EffectiveGasPrice — the single
// source of truth shared with the live execution path (Processing.go). Any
// divergence between the two paths corrupts account balances on reconciliation;
// see config/gasfee.go for the exact formula and the history of that bug.

import (
	"math/big"
	"strings"

	"gossipnode/config"

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

		gasFee := config.GasFee(tx.Type, tx.GasLimit, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee)

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

// Gas-fee helpers intentionally removed: the formula previously duplicated here
// drifted from Processing.go (raw MaxFee, no baseFee clamp, 1 gwei fallback,
// zero fee on GasLimit==0) and corrupted balances on every reconciliation of
// EIP-1559 transactions. Use config.GasFee / config.EffectiveGasPrice only.
