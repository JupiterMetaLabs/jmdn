package FastsyncV2

// reconcileBlocksLocally is the reconciliation entry point for a block range:
// it enumerates the locally stored blocks in [fromBlock..toBlock] and hands
// each block that still has unapplied transactions to the account sync queue
// as a block reference. The drain worker (DB_OPs.ApplyBlockRecon) recomputes
// the block's balance deltas from the stored block AT APPLY TIME, filters the
// tx_processed markers under the global state-apply lock, and commits
// balances + markers in one ExecAll — so every transaction's effect lands
// exactly once and commutes with live execution, regardless of ordering or
// retries.
//
// No balances are computed here and no markers are enqueued separately: both
// are owned by the apply side, atomically per block.

import (
	"fmt"
	"log"

	"gossipnode/DB_OPs"
	NodeInfo "gossipnode/DB_OPs/Nodeinfo"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

// normalizeFetchedTaggedAccounts resets the volatile ledger fields of
// accounts fetched from a peer because they were TAGGED — i.e. they appear in
// transactions of the sync range. A tagged account's balance is derived
// locally by applying its blocks (ApplyBlockRecon / live execution); the
// peer's current balance already contains those same effects, so writing it
// verbatim and then applying the range locally would count the range twice.
// Identity fields (DID, metadata, ART nonce, account type) are kept — that is
// the information the fetch exists to provide. UpdatedAt is zeroed so the
// restore always defers to any newer local document under LWW.
//
// Zero-tx account syncs (AccountSync, phase 2.5/4) are NOT normalized: those
// accounts have no in-range transactions, so the peer's stored balance is the
// only source of their state.
func normalizeFetchedTaggedAccounts(accounts []*types.Account) {
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		acc.Balance = "0"
		acc.TxNonce = 0
		acc.TxCountSent = 0
		acc.UpdatedAt = 0
	}
}

// reconcileBlocksLocally returns (blocksHandedOff, failedBlockNumbers, err).
// A block is handed off only if it exists locally and has at least one tx
// without a tx_processed marker (advisory prefilter — the apply side
// re-checks authoritatively). Fail closed on iterator or marker-read errors:
// callers keep the anchor lagging and retry the range.
func (fs *FastsyncV2) reconcileBlocksLocally(fromBlock, toBlock uint64) (int, []uint64, error) {
	const batchSize = 500
	iter := fs.blockInfoAdapter.NewBlockIterator(fromBlock, toBlock, batchSize)
	defer iter.Close()

	var refs []NodeInfo.BlockReconRef
	for {
		batch, err := iter.Next()
		if err != nil {
			return 0, nil, fmt.Errorf("reconcile blocks [%d..%d]: iterator: %w", fromBlock, toBlock, err)
		}
		if len(batch) == 0 {
			break
		}

		// Advisory prefilter: skip blocks whose txs are all already applied.
		// Stale reads here are harmless — ApplyBlockRecon re-filters under
		// the state-apply lock and no-ops on fully applied blocks.
		var hashes []string
		for _, blk := range batch {
			if blk == nil {
				continue
			}
			for i := range blk.Transactions {
				hashes = append(hashes, blk.Transactions[i].Hash.String())
			}
		}
		applied, err := DB_OPs.FilterProcessedTxMarkers(hashes)
		if err != nil {
			return 0, nil, fmt.Errorf("reconcile blocks [%d..%d]: marker prefilter (fail closed): %w", fromBlock, toBlock, err)
		}

		for _, blk := range batch {
			if blk == nil || len(blk.Transactions) == 0 {
				continue
			}
			pending := false
			for i := range blk.Transactions {
				if !applied[blk.Transactions[i].Hash.String()] {
					pending = true
					break
				}
			}
			if !pending {
				continue
			}
			refs = append(refs, NodeInfo.BlockReconRef{
				BlockNumber: blk.BlockNumber,
				BlockHash:   blk.BlockHash.Hex(),
			})
		}
	}

	if len(refs) == 0 {
		log.Printf("[FastsyncV2] reconcile [%d..%d]: nothing outstanding", fromBlock, toBlock)
		return 0, nil, nil
	}

	failedRefs, err := NodeInfo.EnqueueBlockRecons(refs)
	failed := make([]uint64, 0, len(failedRefs))
	for _, r := range failedRefs {
		failed = append(failed, r.BlockNumber)
	}
	log.Printf("[FastsyncV2] reconcile [%d..%d]: %d block(s) handed off, %d failed", fromBlock, toBlock, len(refs)-len(failedRefs), len(failedRefs))
	return len(refs) - len(failedRefs), failed, err
}
