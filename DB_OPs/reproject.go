package DB_OPs

import (
	"fmt"
	"time"
)

// ReprojectRange rebuilds the SQL projection for blocks [from,to] on THIS node,
// repairing the D-64 catch-up gap where a block got a `blocks` row but no
// `snapshots` row (so its `transactions` rows FK-failed to the outbox).
//
// For each block present locally it re-runs the idempotent StoreZKBlock chain,
// which — post D-64 — writes the snapshot UNCONDITIONALLY (block → snapshot →
// [zkproof] → txs, every write ON CONFLICT DO NOTHING). Creating that missing
// FK parent is exactly what the manual psql repair did (TESTNET-RUNBOOK §5c):
// once the `snapshots` row exists, the persistent outbox worker drains the
// block's still-pending transaction projections on its next retry. So this
// recovers snapshot AND transactions from the node's own canonical/outbox state,
// with no cross-node fetch.
//
// Idempotent and safe on already-healthy blocks (re-storing is a no-op via the
// ON CONFLICT guards). A block not present locally is SKIPPED, not an error, so a
// range wider than the local chain is fine. Returns the number of blocks
// re-stored.
//
// NOTE: this ensures the FK parent; it does not itself re-read tx bytes — the
// tx CanonicalRecords already live in the node's outbox/KV from the original
// (FK-failed) write, and drain once the snapshot exists. If an operator has
// purged the outbox, re-pull the range via thebesync instead.
func ReprojectRange(from, to uint64) (reprojected int, err error) {
	if to < from {
		return 0, fmt.Errorf("ReprojectRange: to(%d) < from(%d)", to, from)
	}
	start := time.Now()
	for n := from; n <= to; n++ {
		blk, gerr := GetZKBlockByNumber(nil, n)
		if gerr != nil {
			if IsNotFound(gerr) {
				continue // block not present locally — nothing to reproject
			}
			return reprojected, fmt.Errorf("ReprojectRange: read block %d: %w", n, gerr)
		}
		if serr := StoreZKBlock(nil, blk); serr != nil {
			return reprojected, fmt.Errorf("ReprojectRange: store block %d: %w", n, serr)
		}
		reprojected++
	}
	fmt.Printf("[reproject] range [%d..%d]: re-stored %d block(s) in %s; "+
		"the outbox will drain any pending transaction rows now their snapshot FK parent exists\n",
		from, to, reprojected, time.Since(start))
	return reprojected, nil
}
