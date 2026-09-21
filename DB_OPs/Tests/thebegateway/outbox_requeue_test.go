package thebegateway_test

import (
	"context"
	"testing"
	"time"

	"gossipnode/DB_OPs/thebegateway"
)

// TestRequeueExhausted proves the F recovery hook: an entry that has exhausted
// its MaxOutboxAttempts retries (and is therefore permanently skipped by Next())
// is reset by RequeueExhausted so the worker retries it — the mechanism that
// lands the 834/835 tx rows once ReprojectRange writes their snapshot.
func TestRequeueExhausted(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)

	mustEnqueue(t, store, thebegateway.OutboxEntry{
		Namespace:   thebegateway.NamespaceBlock,
		Method:      "WriteTransaction",
		Payload:     []byte(`{"block_number":834}`),
		NextRetryAt: past,
	})

	// Fetch to get the ID, then exhaust its attempts (MaxOutboxAttempts).
	entries := mustNext(t, store, 10)
	if len(entries) != 1 {
		t.Fatalf("setup: expected 1 ready entry, got %d", len(entries))
	}
	id := entries[0].ID
	for i := 0; i < thebegateway.MaxOutboxAttempts; i++ {
		if err := store.IncrementAttempts(ctx, id, past); err != nil {
			t.Fatalf("IncrementAttempts: %v", err)
		}
	}

	// Exhausted → Next() must skip it (this is why writing the snapshot alone
	// never lands the tx row).
	if got := mustNext(t, store, 10); len(got) != 0 {
		t.Fatalf("expected 0 ready entries once attempts are exhausted, got %d", len(got))
	}

	// Requeue resets attempts=0 / next_retry=now; Next() sees it again.
	n, err := store.RequeueExhausted(ctx)
	if err != nil {
		t.Fatalf("RequeueExhausted: %v", err)
	}
	if n != 1 {
		t.Errorf("RequeueExhausted returned %d, want 1", n)
	}
	if got := mustNext(t, store, 10); len(got) != 1 {
		t.Errorf("after requeue, expected 1 ready entry, got %d", len(got))
	}
}
