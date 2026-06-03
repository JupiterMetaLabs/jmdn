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
	"time"
)

const defaultBatchSize = 32

// OutboxWorker polls OutboxStore on a fixed interval and retries failed
// ThebeGateway writes with exponential backoff. One goroutine, sequential
// dispatch — no thundering-herd on a recovering ThebeDB.
type OutboxWorker struct {
	store    OutboxStore
	gateway  ThebeGateway
	interval time.Duration
	stop     chan struct{}
	once     sync.Once // guards Stop() — closing a closed channel panics
}

// NewOutboxWorker creates an OutboxWorker. Call Start() to begin polling.
// interval: how often to poll the outbox (recommended: 5s).
func NewOutboxWorker(store OutboxStore, gateway ThebeGateway, interval time.Duration) *OutboxWorker {
	return &OutboxWorker{
		store:    store,
		gateway:  gateway,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start launches the background polling goroutine. Non-blocking.
// Call Stop() to shut it down gracefully.
func (w *OutboxWorker) Start() {
	go w.run()
}

// Stop signals the worker to stop after the current batch completes.
// Safe to call multiple times — subsequent calls are no-ops.
func (w *OutboxWorker) Stop() {
	w.once.Do(func() { close(w.stop) })
}

func (w *OutboxWorker) run() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.drainBatch()
		}
	}
}

func (w *OutboxWorker) drainBatch() {
	ctx := context.Background()
	entries, err := w.store.Next(ctx, defaultBatchSize)
	if err != nil {
		// log or ignore — worker must not crash on store errors
		return
	}
	for _, entry := range entries {
		if entry.Attempts >= MaxOutboxAttempts {
			continue // exhausted — left for operator inspection
		}
		if err := w.dispatch(ctx, entry); err != nil {
			_ = w.store.IncrementAttempts(ctx, entry.ID, ExponentialBackoff(entry.Attempts))
		} else {
			_ = w.store.Ack(ctx, entry.ID)
		}
	}
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

	default:
		// Unknown namespace — ack to drain it; leaving it causes infinite retry
		return nil
	}
}
