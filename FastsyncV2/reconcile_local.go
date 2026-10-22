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
