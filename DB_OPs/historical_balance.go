// MODULE: DB_OPs/historical_balance.go
// PURPOSE: Reconstruct an account's balance at an arbitrary historical block
//          (eth_getBalance with a block tag) without per-block state snapshots.
//
// ALGORITHM (reverse delta):
//   balance_at(N) = latest_balance − Σ delta(addr, block) for blocks (N+1 .. tip)
//
//   Per transaction, mirroring FastsyncV2/deltas.go (reconciliation semantics —
//   the two MUST stay in sync so historical balances agree with reconciled ones):
//     addr == sender   → −value −gasFee        (reverse: add back)
//     addr == receiver → +value                (reverse: subtract)
//     addr == coinbase → +gasFee/2 + remainder (reverse: subtract)
//     addr == zkvm     → +gasFee/2             (reverse: subtract)
//
// COST: O(txs touching addr in (N..tip]) + O(blocks where addr earned gas).
//   Cheap for user addresses at recent blocks (indexed range scans); expensive
//   for the sequencer/coinbase address over deep history — bounded by
//   MaxBalanceLookback.
//
// DO NOT: change gas-fee math here without changing FastsyncV2/deltas.go and
//         messaging/BlockProcessing (Processing.go) in the same commit.

package DB_OPs

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// MaxBalanceLookback bounds how far back eth_getBalance may reconstruct.
// Protects the node from unbounded replay on the coinbase/ZKVM address.
var MaxBalanceLookback = uint64(250_000)

// GetBalanceAtBlock returns the balance of addr as of block atBlock (inclusive).
// atBlock >= chain tip returns the latest balance.
func GetBalanceAtBlock(addr common.Address, atBlock uint64) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetBalanceAtBlock: %w", err)
	}

	tip, err := h.GetLatestBlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetBalanceAtBlock: chain tip: %w", err)
	}

	// Latest balance is the starting point in every case.
	account, err := GetAccount(nil, addr)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no rows") {
			// Account has never existed → balance was zero at every height.
			return big.NewInt(0), nil
		}
		return nil, fmt.Errorf("GetBalanceAtBlock: latest balance: %w", err)
	}
	latest := new(big.Int)
	if account.Balance != "" {
		if _, ok := latest.SetString(account.Balance, 10); !ok {
			return nil, fmt.Errorf("GetBalanceAtBlock: unparseable balance %q", account.Balance)
		}
	}

	if atBlock >= tip {
		return latest, nil
	}
	if tip-atBlock > MaxBalanceLookback {
		return nil, fmt.Errorf("GetBalanceAtBlock: history too deep (requested block %d, tip %d, max lookback %d)", atBlock, tip, MaxBalanceLookback)
	}

	from, to := atBlock+1, tip
	addrLower := strings.ToLower(addr.Hex())
	delta := big.NewInt(0) // net credit to addr over (atBlock, tip]

	// ── Sender / receiver legs ────────────────────────────────────────────────
	dbtxs, err := GetDBTransactionsByAccountInRange(&addr, from, to)
	if err != nil {
		return nil, fmt.Errorf("GetBalanceAtBlock: txs in range: %w", err)
	}
	for _, t := range dbtxs {
		tx := t.Tx
		gasFee := computeGasFeeConfig(tx)
		if tx.From != nil && strings.ToLower(tx.From.Hex()) == addrLower {
			delta.Sub(delta, gasFee)
			if tx.Value != nil && tx.Value.Sign() > 0 {
				delta.Sub(delta, tx.Value)
			}
		}
		if tx.To != nil && strings.ToLower(tx.To.Hex()) == addrLower {
			if tx.Value != nil && tx.Value.Sign() > 0 {
				delta.Add(delta, tx.Value)
			}
		}
	}

	// ── Gas-recipient legs (coinbase / ZKVM) ─────────────────────────────────
	rewardBlocks, err := h.GetBlocksByRewardAddress(ctx, addr.Hex(), from, to)
	if err != nil {
		return nil, fmt.Errorf("GetBalanceAtBlock: reward blocks: %w", err)
	}
	two := big.NewInt(2)
	for _, blk := range rewardBlocks {
		isCoinbase := strings.ToLower(blk.CoinbaseAddr) == addrLower
		isZKVM := strings.ToLower(blk.ZKVMAddr) == addrLower
		if !isCoinbase && !isZKVM {
			continue
		}
		txRecs, err := h.GetTransactionsByBlock(ctx, blk.BlockNumber)
		if err != nil {
			return nil, fmt.Errorf("GetBalanceAtBlock: txs of block %d: %w", blk.BlockNumber, err)
		}
		for _, r := range txRecs {
			tx := txRecordToConfig(r)
			gasFee := computeGasFeeConfig(tx)
			half := new(big.Int).Div(gasFee, two)
			remainder := new(big.Int).Mod(gasFee, two)
			if isCoinbase {
				delta.Add(delta, new(big.Int).Add(half, remainder))
			}
			if isZKVM {
				delta.Add(delta, half)
			}
		}
	}

	balanceAt := new(big.Int).Sub(latest, delta)
	if balanceAt.Sign() < 0 {
		// Gas approximations (gasLimit-based fees, same as reconciliation) can
		// under/overshoot slightly around the boundary — clamp, never negative.
		balanceAt.SetInt64(0)
	}
	return balanceAt, nil
}

// computeGasFeeConfig returns gasLimit * effectiveGasPrice for a config.Transaction.
// Mirrors FastsyncV2/deltas.go computeGasFee / effectiveGasPrice exactly.
func computeGasFeeConfig(tx *config.Transaction) *big.Int {
	if tx == nil || tx.GasLimit == 0 {
		return big.NewInt(0)
	}
	gasLimit := new(big.Int).SetUint64(tx.GasLimit)
	return new(big.Int).Mul(gasLimit, effectiveGasPriceConfig(tx))
}

var oneGweiHist = big.NewInt(1_000_000_000)

// effectiveGasPriceConfig mirrors FastsyncV2/deltas.go effectiveGasPrice:
//
//	EIP-1559 (type 2): MaxFee → MaxPriorityFee → GasPrice → 1 Gwei
//	Legacy   (0/1):    GasPrice → MaxFee → MaxPriorityFee → 1 Gwei
func effectiveGasPriceConfig(tx *config.Transaction) *big.Int {
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
	return oneGweiHist
}
