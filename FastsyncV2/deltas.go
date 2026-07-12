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
	"context"
	"fmt"
	"math/big"
	"strings"

	"gossipnode/DB_OPs"
	NodeInfo "gossipnode/DB_OPs/Nodeinfo"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

// computeAccountDeltas iterates all blocks in [fromBlock..toBlock] and returns a map
// of lowercase-hex-address → *types.AccountDelta. Accounts not touched in the range
// are absent from the map.
//
// MARKER EXCLUSION (effects must never be applied twice): transactions that
// carry a persistent `tx_processed:` marker were already applied by the LIVE
// path (ProcessBlockTransactions); including them here would apply their
// effects a second time. The filter dual-reads defaultdb AND accountsdb (a
// historical marker cluster lives in accountsdb). Gap blocks fetched by
// catchup were never live-processed, carry no markers, and get full deltas —
// their effects must never be silently skipped.
//
// FAIL CLOSED: any iterator or marker-filter error aborts with an error —
// partial deltas, or deltas computed without the exclusion filter, corrupt
// balances when applied. Callers skip reconciliation and leave the anchor.
//
// The second return value lists the tx hashes whose deltas ARE included
// (i.e. not excluded by markers). After a clean ReconcileWithDeltas, callers
// enqueue tx_processed markers for exactly these hashes so a recon re-run
// (e.g. after a failed anchor advance) excludes them instead of re-applying.
func (fs *FastsyncV2) computeAccountDeltas(fromBlock, toBlock uint64) (map[string]*types.AccountDelta, []string, error) {
	// ENTRY GATE: the exclusion filter below reads tx_processed markers
	// from the DATABASE — markers (and balances) still on the Redis queue are
	// invisible to it. Computing deltas while a previous recon's effects are in
	// flight re-includes those txs → both applications eventually drain →
	// double-apply. The advance gate's timeout path makes this routine: a
	// timed-out advance leaves the queue loaded and the range open, and the
	// next recon run lands exactly here. Fail closed: no deltas over a
	// non-quiescent queue. (After a restart the in-process HWM is empty
	// while Redis may still hold pre-restart entries — the gate falls back to
	// a queue-empty check rather than assuming quiescence.)
	gateCtx, gateCancel := context.WithTimeout(context.Background(), drainConfirmTimeout)
	defer gateCancel()
	if err := NodeInfo.WaitForQueueQuiescence(gateCtx); err != nil {
		return nil, nil, fmt.Errorf("delta computation: queue not quiescent — prior recon effects may be in flight (fail closed): %w", err)
	}

	const batchSize = 500
	iter := fs.blockInfoAdapter.NewBlockIterator(fromBlock, toBlock, batchSize)
	defer iter.Close()

	deltas := make(map[string]*types.AccountDelta)
	var appliedHashes []string

	for {
		batch, err := iter.Next()
		if err != nil {
			return nil, nil, fmt.Errorf("delta computation: block iterator at [%d..%d]: %w", fromBlock, toBlock, err)
		}
		if len(batch) == 0 {
			break
		}

		// Collect this batch's tx hashes and resolve which are already live-applied.
		var hashes []string
		for _, blk := range batch {
			if blk == nil {
				continue
			}
			for i := range blk.Transactions {
				hashes = append(hashes, blk.Transactions[i].Hash.String())
			}
		}
		liveApplied, err := DB_OPs.FilterProcessedTxMarkers(hashes)
		if err != nil {
			return nil, nil, fmt.Errorf("delta computation: tx_processed marker filter: %w", err)
		}

		for _, blk := range batch {
			if blk == nil {
				continue
			}
			applyBlockDeltas(blk, deltas, liveApplied)
		}
		for _, h := range hashes {
			if !liveApplied[h] {
				appliedHashes = append(appliedHashes, h)
			}
		}
	}

	return deltas, appliedHashes, nil
}

// applyBlockDeltas applies the transaction effects of one ZKBlock to the delta
// map, skipping any tx whose hash is in skipTxs (already applied by the live path).
func applyBlockDeltas(blk *types.ZKBlock, deltas map[string]*types.AccountDelta, skipTxs map[string]bool) {
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

		// Already applied by live block processing — applying its delta again
		// would double-count. See computeAccountDeltas doc.
		if skipTxs[tx.Hash.String()] {
			continue
		}

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
