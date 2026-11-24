package thebegateway_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"encoding/json"

	"gossipnode/DB_OPs/thebegateway"
)

// Regression for the outbox retry-amplification defect: the worker used to
// retry through the same gateway whose write() enqueues on failure, so every
// failed retry inserted a NEW row (attempts=0) while the original was also
// incremented — one unwritable record multiplied geometrically for as long as
// ThebeDB was down (1.3 GB outbox.db files on the Oct 3 devnet).
//
// With the REAL gateway (failing appender) behind the worker, a retry that
// fails must produce exactly one IncrementAttempts and zero Enqueue calls.
func TestOutboxWorker_FailedRetryDoesNotReEnqueue(t *testing.T) {
	payload, _ := json.Marshal(thebegateway.BlockRecord{BlockNumber: 7})
	store := &oneShotOutbox{}
	store.nextEntries = []thebegateway.OutboxEntry{
		{ID: 1, Namespace: thebegateway.NamespaceBlock, Method: "WriteBlock", Payload: payload, Attempts: 3},
	}
	app := &spyAppender{err: errors.New("thebedb down")}
	gw := newGateway(app, &spyKV{}, newSpyCache(), &store.spyOutbox) // real gateway, wired to the same outbox

	w := thebegateway.NewOutboxWorker(store, gw, time.Millisecond)
	w.Start()
	time.Sleep(30 * time.Millisecond)
	w.Stop()

	if app.callCount() < 1 {
		t.Fatal("retry never reached the appender")
	}
	if store.incrCount() != 1 {
		t.Fatalf("want exactly 1 IncrementAttempts for the existing row, got %d", store.incrCount())
	}
	// Locked accessor, not the raw field: the worker goroutine writes
	// enqueueCalls and Stop() does not wait for it.
	if n := store.enqueueCount(); n != 0 {
		t.Fatalf("a failed RETRY must not enqueue a new row (amplification), got %d Enqueue calls", n)
	}
	if store.ackCount() != 0 {
		t.Fatalf("failed retry must not Ack, got %d", store.ackCount())
	}
}

// The gateway handed to callers (block apply etc.) must still enqueue on
// failure — only the worker's retry variant suppresses it.
func TestRetryGateway_OnlyRetryVariantSkipsEnqueue(t *testing.T) {
	app := &spyAppender{err: errors.New("thebedb down")}
	out := &spyOutbox{}
	gw := newGateway(app, &spyKV{}, newSpyCache(), out)

	if err := gw.WriteBlock(context.Background(), &thebegateway.BlockRecord{BlockNumber: 1}); err == nil {
		t.Fatal("expected error")
	}
	if n := out.enqueueCount(); n != 1 {
		t.Fatalf("normal gateway: want 1 Enqueue, got %d", n)
	}

	rp, ok := gw.(interface {
		RetryGateway() thebegateway.ThebeGateway
	})
	if !ok {
		t.Fatal("real gateway must expose RetryGateway()")
	}
	if err := rp.RetryGateway().WriteBlock(context.Background(), &thebegateway.BlockRecord{BlockNumber: 2}); err == nil {
		t.Fatal("expected error")
	}
	if n := out.enqueueCount(); n != 1 {
		t.Fatalf("retry gateway: Enqueue count must stay 1, got %d", n)
	}
}

// PruneExhausted removes only rows that are both exhausted AND older than the
// retention window; Compact must succeed on the SQLite store.
func TestOutboxStore_PruneExhaustedAndCompact(t *testing.T) {
	store, err := thebegateway.NewOutboxStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Now().Add(-10 * 24 * time.Hour)
	mk := func(created time.Time) thebegateway.OutboxEntry {
		return thebegateway.OutboxEntry{Namespace: thebegateway.NamespaceBlock, Method: "WriteBlock",
			Payload: []byte(`{}`), CreatedAt: created, NextRetryAt: time.Now()}
	}
	for _, e := range []thebegateway.OutboxEntry{
		mk(old),        // id 1: exhausted + old  → pruned
		mk(time.Now()), // id 2: exhausted, recent → kept (RequeueExhausted may still revive it)
		mk(old),        // id 3: old but still retryable → kept
	} {
		if err := store.Enqueue(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for id, n := range map[int64]int{1: thebegateway.MaxOutboxAttempts, 2: thebegateway.MaxOutboxAttempts, 3: 2} {
		for i := 0; i < n; i++ {
			if err := store.IncrementAttempts(ctx, id, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}

	m, ok := store.(interface {
		PruneExhausted(ctx context.Context, olderThan time.Duration) (int64, error)
		Compact(ctx context.Context) error
	})
	if !ok {
		t.Fatal("sqlite outbox store must implement PruneExhausted/Compact")
	}
	n, err := m.PruneExhausted(ctx, thebegateway.ExhaustedRetention)
	if err != nil || n != 1 {
		t.Fatalf("PruneExhausted = %d, %v; want 1", n, err)
	}
	if err := m.Compact(ctx); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// Row 3 (attempts=2) is still retryable and must come back from Next;
	// row 2 is exhausted (skipped by Next) but retained.
	next, err := store.Next(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].ID != 3 {
		t.Fatalf("Next after prune = %+v, want only id 3", next)
	}
	if maxID, _ := store.MaxID(ctx); maxID != 3 {
		t.Fatalf("max id = %d, want 3", maxID)
	}
}
