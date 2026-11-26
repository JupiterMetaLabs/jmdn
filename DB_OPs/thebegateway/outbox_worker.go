// MODULE: DB_OPs/thebegateway/outbox_worker.go
// PURPOSE: Poll OutboxStore and retry failed ThebeGateway writes with exponential backoff.
//
// CORE DATA STRUCTURES:
//   - OutboxWorker: holds store+gateway deps (interfaces) + interval + stop channel.
//     Stateless per-entry. The stop channel is closed by Stop() — closing is safe
//     to call multiple times via sync.Once.
//   - Internal poll loop: single goroutine; processes entries sequentially.
//     No concurrent gateway calls — avoids thundering-herd on a recovering ThebeDB.
//
// TO MODIFY BEHAVIOR:
//   - Change poll interval: pass different interval to NewOutboxWorker()
//   - Change batch size: edit batchSize constant in this file
//   - Add metrics: wrap gateway calls with counters before/after the switch
//
// DO NOT:
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Call gateway methods concurrently from this worker (sequential is intentional)
//   - Add a second Stop() signal path — sync.Once ensures single close
//
// EXTENSION POINT: new Namespace → add case to the switch in dispatch()
//
// CHANGE SCENARIOS:
//   Add contract namespace (Phase 7): add case NamespaceContractCode → gateway.WriteContractCode
//   Add metrics: wrap dispatch() call with before/after counters — worker loop unchanged

package thebegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const defaultBatchSize = 32

// ExhaustedRetention is how long an entry that hit MaxOutboxAttempts is kept
// for operator inspection before PruneExhausted drops it. Exhausted rows used
// to be retained forever; combined with the retry-amplification bug fixed in
// RetryGateway that let outbox.db grow without bound.
const ExhaustedRetention = 7 * 24 * time.Hour

// maintenanceEvery is the number of poll ticks between prune+compact passes
// (720 × 5 s ≈ 1 h at the recommended interval).
const maintenanceEvery = 720

// maintenanceTimeout caps one prune+compact pass. It is also the worst case a
// pass could outlive Stop() if it were not cancelled — see maintain().
const maintenanceTimeout = time.Minute

// retryGatewayProvider is implemented by the real gateway: it hands back a
// variant that never re-enqueues on failure. The worker MUST retry through
// such a gateway, otherwise every failed retry adds a new row.
type retryGatewayProvider interface {
	RetryGateway() ThebeGateway
}

// outboxMaintainer is optionally implemented by the store (the SQLite store
// does); the worker uses it to prune exhausted rows and reclaim file space.
type outboxMaintainer interface {
	PruneExhausted(ctx context.Context, olderThan time.Duration) (int64, error)
	Compact(ctx context.Context) error
}

// OutboxWorker polls OutboxStore on a fixed interval and retries failed
// ThebeGateway writes with exponential backoff. One goroutine, sequential
// dispatch — no thundering-herd on a recovering ThebeDB.
type OutboxWorker struct {
	store    OutboxStore
	gateway  ThebeGateway
	interval time.Duration
	stop     chan struct{}
	once     sync.Once // guards Stop() — closing a closed channel panics
	ticks    int       // poll ticks since the last maintenance pass

	// done is closed when run() returns, so StopAndWait can wait for the
	// worker before ThebeDB is closed under it. started records that run()
	// was launched (StopAndWait must not wait on a worker never started).
	done      chan struct{}
	startOnce sync.Once
	started   atomic.Bool
}

// NewOutboxWorker creates an OutboxWorker. Call Start() to begin polling.
// interval: how often to poll the outbox (recommended: 5s).
func NewOutboxWorker(store OutboxStore, gateway ThebeGateway, interval time.Duration) *OutboxWorker {
	if p, ok := gateway.(retryGatewayProvider); ok {
		gateway = p.RetryGateway()
	}
	return &OutboxWorker{
		store:    store,
		gateway:  gateway,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the background polling goroutine. Non-blocking.
// Call Stop() to shut it down gracefully.
// Time: O(1) to start; poll loop is O(batchSize) per tick — bounded by defaultBatchSize=32
func (w *OutboxWorker) Start() {
	w.startOnce.Do(func() {
		w.started.Store(true)
		go w.run()
	})
}

// Stop signals the worker to stop and returns at once; it does not wait for
// the worker goroutine. Safe to call multiple times — subsequent calls are
// no-ops. Use StopAndWait before closing the store or gateway the worker uses.
func (w *OutboxWorker) Stop() {
	w.once.Do(func() { close(w.stop) })
}

// StopAndWait signals the worker to stop and waits until its goroutine has
// returned, so no retry is in flight afterwards. Stop cancels the context of
// an in-flight dispatch and the drain stops between entries, so the wait is
// as long as a write takes to notice cancellation; aborted and unreached
// entries stay in the outbox for the next start. Returns at once if Start was never
// called. Shutdown calls this (under a timeout) before closing ThebeDB.
func (w *OutboxWorker) StopAndWait() {
	w.Stop()
	if w.started.Load() {
		<-w.done
	}
}

func (w *OutboxWorker) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.drainBatch()
			w.ticks++
			if w.ticks >= maintenanceEvery {
				w.ticks = 0
				// Don't begin an hour-scale maintenance pass while shutting
				// down; maintain() also aborts in flight if Stop() lands
				// mid-pass.
				select {
				case <-w.stop:
					return
				default:
				}
				w.maintain()
			}
		}
	}
}

func (w *OutboxWorker) drainBatch() {
	// Cancelled as soon as w.stop closes (as maintain() does), so Stop aborts
	// an in-flight dispatch instead of letting it run on: shutdown closes
	// ThebeDB right after StopAndWait, and a slow write must not hold that
	// up or be cut by the close. An aborted write leaves its entry in the
	// outbox for the next start.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-w.stop:
			cancel()
		case <-done:
		}
	}()

	entries, err := w.store.Next(ctx, defaultBatchSize)
	if err != nil {
		// log or ignore — worker must not crash on store errors
		return
	}
	for _, entry := range entries {
		// Stop between entries, not only between ticks: shutdown waits for
		// this goroutine (StopAndWait) before closing ThebeDB, so the wait is
		// one dispatch at most. Unprocessed entries stay in the outbox.
		select {
		case <-w.stop:
			return
		default:
		}
		if entry.Attempts >= MaxOutboxAttempts {
			continue // exhausted — left for operator inspection
		}
		if err := w.dispatch(ctx, entry); err != nil {
			if ctx.Err() != nil {
				// Aborted by Stop, not a failed write: do not count it as an
				// attempt. The entry stays queued for the next start.
				return
			}
			w.bookkeep(func(bctx context.Context) error {
				return w.store.IncrementAttempts(bctx, entry.ID, ExponentialBackoff(entry.Attempts))
			})
		} else {
			// The write succeeded; record that even if Stop landed meanwhile.
			// Using the drain context here would fail the Ack once Stop has
			// cancelled it, and the entry would be written again next start.
			w.bookkeep(func(bctx context.Context) error { return w.store.Ack(bctx, entry.ID) })
		}
	}
}

// bookkeepTimeout bounds one outbox bookkeeping call (Ack/IncrementAttempts)
// made after a dispatch, so it cannot hold StopAndWait up for long.
const bookkeepTimeout = time.Second

// bookkeep runs one outbox bookkeeping call on its own short-lived context,
// independent of the drain context: the outcome of a dispatch that has
// already happened must be recorded even when Stop cancelled the drain.
func (w *OutboxWorker) bookkeep(fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), bookkeepTimeout)
	defer cancel()
	_ = fn(ctx)
}

// maintain drops exhausted rows older than ExhaustedRetention and asks the
// store to reclaim free pages. Best-effort: errors are ignored, the worker
// must never crash on maintenance.
//
// Shutdown: run() only re-checks w.stop between ticks, so a pass that has
// already begun would otherwise keep writing to the outbox for up to
// maintenanceTimeout after Stop() returned — and Stop() does not wait for this
// goroutine. The context is therefore cancelled as soon as w.stop closes, so an
// in-flight PruneExhausted/Compact aborts instead of outliving shutdown.
func (w *OutboxWorker) maintain() {
	m, ok := w.store.(outboxMaintainer)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), maintenanceTimeout)
	defer cancel()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-w.stop:
			cancel()
		case <-done:
		}
	}()

	_, _ = m.PruneExhausted(ctx, ExhaustedRetention)
	_ = m.Compact(ctx)
}

// dispatch is the ONE place switch/case on Namespace is allowed.
// Deserializes entry.Payload into the correct *Record type and calls the matching gateway method.
// Returns nil for unknown namespaces — acks the entry to drain it silently.
func (w *OutboxWorker) dispatch(ctx context.Context, entry OutboxEntry) error {
	switch entry.Namespace {
	case NamespaceAccount:
		var r AccountRecord
		if err := json.Unmarshal(entry.Payload, &r); err != nil {
			return fmt.Errorf("dispatch account: unmarshal: %w", err)
		}
		if r.Deleted {
			// DeleteAccount never enqueues; a delete here is not one of ours.
			// Replaying it late could remove a newer account, so drop it.
			return nil
		}
		return w.gateway.WriteAccount(ctx, &r)

	case NamespaceBlock:
		var r BlockRecord
		if err := json.Unmarshal(entry.Payload, &r); err != nil {
			return fmt.Errorf("dispatch block: unmarshal: %w", err)
		}
		return w.gateway.WriteBlock(ctx, &r)

	case NamespaceSnapshot:
		var r SnapshotRecord
		if err := json.Unmarshal(entry.Payload, &r); err != nil {
			return fmt.Errorf("dispatch snapshot: unmarshal: %w", err)
		}
		return w.gateway.WriteSnapshot(ctx, &r)

	case NamespaceTransaction:
		var r TransactionRecord
		if err := json.Unmarshal(entry.Payload, &r); err != nil {
			return fmt.Errorf("dispatch tx: unmarshal: %w", err)
		}
		return w.gateway.WriteTransaction(ctx, &r)

	case NamespaceZKProof:
		var r ZKProofRecord
		if err := json.Unmarshal(entry.Payload, &r); err != nil {
			return fmt.Errorf("dispatch zk: unmarshal: %w", err)
		}
		return w.gateway.WriteZKProof(ctx, &r)

	case NamespaceL1Finality:
		var r L1FinalityRecord
		if err := json.Unmarshal(entry.Payload, &r); err != nil {
			return fmt.Errorf("dispatch l1_finality: unmarshal: %w", err)
		}
		return w.gateway.WriteL1Finality(ctx, &r)

	case NamespaceContractReceipt:
		var r ContractReceiptRecord
		if err := json.Unmarshal(entry.Payload, &r); err != nil {
			return fmt.Errorf("dispatch contract_receipt: unmarshal: %w", err)
		}
		return w.gateway.WriteContractReceipt(ctx, &r)

	default:
		// Unknown namespace — ack to drain it; leaving it causes infinite retry.
		// Every namespace the gateway enqueues MUST have a case above, or its
		// retries are silently discarded here (review finding R6).
		return nil
	}
}
