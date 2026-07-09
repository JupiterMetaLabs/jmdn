// MODULE: DB_OPs/Nodeinfo/account_sync_drainwait
// PURPOSE: Drain confirmation for the account sync queue (F5, RCA §6f PROBE D).
//
// PROBLEM: reconciliation's balance effects travel through the Redis queue —
// enqueue ≠ applied (G5). The recon anchor was advanced after data verification
// but while the effects could still be sitting on the queue; Redis queue loss
// (crash without AOF, eviction, flush) meant the anchor claimed ranges whose
// effects never landed → silent permanent skip (I1).
//
// MECHANISM: high-water-mark confirmation.
//   - Producers record the max stream ID they enqueued (noteEnqueuedID, called
//     by enqueueRecordsChunked).
//   - The drain worker records the max stream ID it successfully applied AND
//     ACKed (noteDrainedIDs, called by processBatch).
//   - WaitForAccountQueueDrain(target) blocks until drained ≥ target. Redis
//     stream IDs are totally ordered and the drain processes in stream order,
//     so drained ≥ target ⟺ every entry up to target left the queue.
//
// WHY HWM AND NOT queue-depth==0: the stream is shared with concurrent sync
// traffic (WriteAccounts pages). Depth==0 waits on unrelated producers and may
// never come on a busy node; HWM confirms as soon as OUR entries are applied,
// regardless of what lands behind them.
//
// FAIL DIRECTION: every failure mode of the wait (timeout, worker restart
// losing the in-memory HWM, queue offline) reports NOT-confirmed. Callers skip
// the anchor advance — the anchor LAGS, which is the safe direction
// (reconciliation re-covers; tx_processed markers prevent double-apply).
// Confirmation is never assumed, only observed.
//
// ACCEPTED RESIDUAL: poison entries (undecodable payloads) are ACKed without
// application and do not advance the HWM themselves, but a later good entry in
// the same batch can carry the HWM past them. All queue payloads are
// self-encoded (marshaled by this process), so a poison recon entry is
// unreachable in practice; the historical repair job covers the pathological
// case. (Same class as the §6f XAUTOCLAIM reorder residual.)

package NodeInfo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// drainProgress holds the queue high-water marks. In-memory only BY DESIGN:
// losing it on restart degrades to not-confirmed → anchor lags → safe.
var drainProgress = struct {
	mu           sync.Mutex
	lastEnqueued string // max stream ID this process XADDed to the account stream
	lastDrained  string // max stream ID the worker applied AND ACKed
}{}

// parseStreamID splits a Redis stream ID "ms-seq" into its numeric parts.
// Returns ok=false for anything malformed.
func parseStreamID(id string) (ms uint64, seq uint64, ok bool) {
	dash := strings.IndexByte(id, '-')
	if dash <= 0 || dash == len(id)-1 {
		return 0, 0, false
	}
	ms, err := strconv.ParseUint(id[:dash], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	seq, err = strconv.ParseUint(id[dash+1:], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return ms, seq, true
}

// streamIDGTE reports a ≥ b in Redis stream-ID order. Malformed IDs compare as
// NOT-gte (fail toward not-confirmed — the safe direction).
func streamIDGTE(a, b string) bool {
	ams, aseq, ok := parseStreamID(a)
	if !ok {
		return false
	}
	bms, bseq, ok := parseStreamID(b)
	if !ok {
		return false
	}
	if ams != bms {
		return ams > bms
	}
	return aseq >= bseq
}

// noteEnqueuedID records an XADD-assigned stream ID (monotonic max).
// Malformed IDs are ignored: recording one as the wait TARGET would wedge the
// gate permanently (malformed compares as not-gte); real XADD IDs are always
// well-formed "ms-seq".
func noteEnqueuedID(id string) {
	if _, _, ok := parseStreamID(id); !ok {
		return
	}
	drainProgress.mu.Lock()
	defer drainProgress.mu.Unlock()
	if drainProgress.lastEnqueued == "" || streamIDGTE(id, drainProgress.lastEnqueued) {
		drainProgress.lastEnqueued = id
	}
}

// noteDrainedIDs records successfully applied+ACKed stream IDs (monotonic max).
// Malformed IDs are skipped (see noteEnqueuedID).
func noteDrainedIDs(ids []string) {
	if len(ids) == 0 {
		return
	}
	drainProgress.mu.Lock()
	defer drainProgress.mu.Unlock()
	for _, id := range ids {
		if _, _, ok := parseStreamID(id); !ok {
			continue
		}
		if drainProgress.lastDrained == "" || streamIDGTE(id, drainProgress.lastDrained) {
			drainProgress.lastDrained = id
		}
	}
}

// LastAccountEnqueueID returns the max stream ID this process has enqueued to
// the account sync stream ("" = nothing enqueued since boot). Reconciliation
// captures this AFTER its balance + marker enqueues as the confirmation target.
func LastAccountEnqueueID() string {
	drainProgress.mu.Lock()
	defer drainProgress.mu.Unlock()
	return drainProgress.lastEnqueued
}

// drainConfirmed is the pure confirmation decision (unit-tested):
// an empty target means nothing was enqueued through the queue (direct-write
// fallback or no work) — trivially confirmed. Otherwise the drain HWM must
// have reached the target.
func drainConfirmed(lastDrained, target string) bool {
	if target == "" {
		return true
	}
	if lastDrained == "" {
		return false
	}
	return streamIDGTE(lastDrained, target)
}

// WaitForQueueQuiescence is the reconciliation ENTRY gate (F5-B1/B2): it
// confirms that every previously enqueued account-stream entry — balances AND
// tx markers from earlier recon runs or pre-restart sessions — has been applied
// to the database. computeAccountDeltas MUST pass this gate before running the
// marker exclusion filter: markers still sitting ON THE QUEUE are invisible to
// the filter (it reads DB state), so computing deltas over a loaded queue
// re-includes txs whose effects are in flight → double-apply on drain. The
// advance gate's own timeout makes this scenario routine, not exotic: every
// timed-out advance forces a recon re-run over a queue still holding the
// previous run's markers (B1, review of d78a34c).
//
// Empty in-process HWM does NOT short-circuit here (B2, unlike the advance
// gate): after a restart the HWM is lost while Redis may still hold
// pre-restart entries — exactly the blind window. Fallback: poll the queue
// itself (XLEN + XPENDING) until fully empty. This fallback only runs when
// nothing has been enqueued since boot, so the shared-stream starvation
// argument against depth checks does not apply (concurrent sync phases would
// have set the HWM, taking the precise path instead).
//
// Fail direction: any error/timeout = NOT quiescent → callers fail closed
// (skip this recon round; SyncMonitor retries later).
func WaitForQueueQuiescence(ctx context.Context) error {
	if target := LastAccountEnqueueID(); target != "" {
		return WaitForAccountQueueDrain(ctx, target)
	}

	s, mgr := getAccountQueue()
	if s == nil {
		// Queue never installed: every write in this mode was synchronous
		// (direct fallback) — nothing can be in flight.
		return nil
	}
	if mgr != nil {
		mgr.EnsureActive() // make sure a worker is draining any backlog
	}

	const pollInterval = 500 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		qlen, lenErr := s.Len(ctx, accountSyncStream)
		pending, pendErr := s.PendingCount(ctx, accountSyncStream, accountSyncGroup)
		if lenErr == nil && pendErr == nil && qlen == 0 && pending == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("queue not quiescent (len=%d lenErr=%v pending=%d pendErr=%v): %w",
				qlen, lenErr, pending, pendErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

// WaitForAccountQueueDrain blocks until every account-stream entry up to
// target has been applied and ACKed by the drain worker, or ctx expires.
// Returns nil = CONFIRMED; any error = NOT confirmed (callers must treat the
// work as possibly-unapplied and skip anchor advancement).
//
// The empty-target shortcut is valid HERE (advance gate) and only here: a
// worst-case wrongly-skipped advance re-opens the range, which marker
// exclusion handles — provided the ENTRY gate (WaitForQueueQuiescence) did its
// job. Do not reuse this shortcut for entry gating (B2).
func WaitForAccountQueueDrain(ctx context.Context, target string) error {
	if target == "" {
		return nil
	}

	// The worker is lazy — make sure one is running to drain our entries.
	if _, mgr := getAccountQueue(); mgr != nil {
		mgr.EnsureActive()
	}

	const pollInterval = 500 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		drainProgress.mu.Lock()
		drained := drainProgress.lastDrained
		drainProgress.mu.Unlock()
		if drainConfirmed(drained, target) {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("drain not confirmed up to %s (drained: %q): %w", target, drained, ctx.Err())
		case <-ticker.C:
		}
	}
}
