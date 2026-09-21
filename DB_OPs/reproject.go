package DB_OPs

import (
	"context"
	"fmt"
	"time"
)

// outboxRequeuer, when wired by main() (SetOutboxRequeuer), resets exhausted
// outbox entries (attempts >= MaxOutboxAttempts) so the worker retries them.
// ReprojectRange calls it after writing the snapshots: a tx row whose projection
// FK-failed while its snapshot was missing exhausts its 3 attempts within a
// minute and is then permanently skipped by the worker — writing the snapshot
// alone does NOT un-skip it. Nil when no seednode/outbox is configured.
var outboxRequeuer func(context.Context) (int, error)

// SetOutboxRequeuer wires the node's outbox requeue hook (main() passes
// thebegateway OutboxStore.RequeueExhausted). Call once at startup.
func SetOutboxRequeuer(fn func(context.Context) (int, error)) { outboxRequeuer = fn }

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

	// Writing the snapshots is necessary but NOT sufficient: any tx row whose
	// projection FK-failed before the snapshot existed has, by now, exhausted its
	// MaxOutboxAttempts retries and is permanently skipped by the outbox worker.
	// Requeue those exhausted entries so the worker retries them now that the FK
	// parent exists. Without this, ReprojectRange writes snapshots but the missing
	// transactions never land.
	requeued := 0
	if outboxRequeuer != nil {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		n, rqErr := outboxRequeuer(rctx)
		cancel()
		if rqErr != nil {
			fmt.Printf("[reproject] WARNING: outbox requeue failed: %v — exhausted tx rows may stay stranded; "+
				"re-run once the outbox is reachable, or re-pull the range via thebesync\n", rqErr)
		}
		requeued = n
	} else {
		fmt.Printf("[reproject] NOTE: no outbox requeuer wired — if tx rows were exhausted (>=%d attempts) they stay "+
			"skipped; ensure the node's outbox is wired (SetOutboxRequeuer), or re-pull the range via thebesync\n",
			3)
	}

	fmt.Printf("[reproject] range [%d..%d]: re-stored %d block(s), requeued %d exhausted outbox entrie(s) in %s; "+
		"the worker will now drain the pending transaction rows\n",
		from, to, reprojected, requeued, time.Since(start))
	return reprojected, nil
}
