package Service

import (
	"context"
	"math/big"
	"strings"

	block "gossipnode/Block"
)

// Pending-nonce support for eth_getTransactionCount("pending").
//
// The pending nonce is layered from three views, taking the max
// (docs/MRE-V1-MIGRATION-TRACKER.md, S2 design):
//
//  1. confirmed  — accountsdb TxNonce (authoritative floor)
//  2. tracker    — highest nonce this node routed to the MRE (+1); covers the
//     sequencing in-flight window for same-node submissions
//  3. pool run   — contiguous run of the sender's nonces sitting in the
//     mempool, walked from the confirmed nonce; covers txs submitted via
//     other nodes that are still queued
//
// Known residual gap (deliberate, tracker D-5): a tx submitted via a
// DIFFERENT node that is mid-sequencing (pulled from the pool, not yet
// applied) is invisible to all three views until the mempool ack protocol
// lands. Contiguous-run semantics are geth-exact: gapped pool nonces beyond
// the run do not advance the result — with chain-side nonce-jumping live,
// over-advancing would manufacture orphaned transactions.

// pendingNonceLookaheadLimit caps the mempool peek. Matches txpool_content's
// cap; the MRE rejects limit<=0.
const pendingNonceLookaheadLimit = 5000

// pendingNonce computes the layered pending nonce. Pool unavailability
// degrades gracefully to tracker/confirmed — a pending-nonce query must not
// fail because the mempool is briefly unreachable.
func (s *ServiceImpl) pendingNonce(ctx context.Context, addr string, confirmed uint64) *big.Int {
	next := block.GetPendingNonceTracker().NextFor(addr, confirmed)

	if router, err := block.GetRoutingClient(ctx); err == nil {
		if batch, peekErr := router.PeekPendingTransactions(ctx, pendingNonceLookaheadLimit); peekErr == nil {
			if fromPool := nextAfterContiguousRun(addr, confirmed, batch.Transactions); fromPool > next {
				next = fromPool
			}
		}
	}

	return new(big.Int).SetUint64(next)
}

// nextAfterContiguousRun walks the sender's pool nonces upward from the
// confirmed nonce and returns the first nonce NOT covered by a contiguous
// run. Pure function; geth-exact semantics.
//
//	confirmed=5, pool={5,6,8} → 7   (gap at 7; 8 cannot mine yet)
//	confirmed=5, pool={}      → 5
//	confirmed=5, pool={7,8}   → 5   (run never starts)
func nextAfterContiguousRun(sender string, confirmed uint64, txs []block.PendingTx) uint64 {
	want := strings.ToLower(sender)

	pooled := make(map[uint64]struct{})
	for _, tx := range txs {
		if strings.ToLower(tx.GetFrom()) == want {
			pooled[tx.GetNonce()] = struct{}{}
		}
	}

	next := confirmed
	for {
		if _, ok := pooled[next]; !ok {
			return next
		}
		next++
	}
}
