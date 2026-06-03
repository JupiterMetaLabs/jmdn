package thebegateway_test

import (
	"context"
	"testing"
	"time"

	"gossipnode/DB_OPs/thebegateway"
)

func newStore(t *testing.T) thebegateway.OutboxStore {
	t.Helper()
	store, err := thebegateway.NewOutboxStore(":memory:")
	if err != nil {
		t.Fatalf("NewOutboxStore: %v", err)
	}
	return store
}

func mustEnqueue(t *testing.T, store thebegateway.OutboxStore, entry thebegateway.OutboxEntry) {
	t.Helper()
	if err := store.Enqueue(context.Background(), entry); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

func mustNext(t *testing.T, store thebegateway.OutboxStore, limit int) []thebegateway.OutboxEntry {
	t.Helper()
	entries, err := store.Next(context.Background(), limit)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return entries
}

func TestEnqueueAndNext(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	tests := []struct {
		name        string
		entry       thebegateway.OutboxEntry
		wantCount   int
		checkFields bool
	}{
		{
			name: "ready_entry_returned",
			entry: thebegateway.OutboxEntry{
				Namespace:   thebegateway.NamespaceAccount,
				Method:      "WriteAccount",
				Payload:     []byte(`{"address":"0xabc"}`),
				NextRetryAt: past,
			},
			wantCount:   1,
			checkFields: true,
		},
		{
			name: "future_next_retry_not_returned",
			entry: thebegateway.OutboxEntry{
				Namespace:   thebegateway.NamespaceBlock,
				Method:      "WriteBlock",
				Payload:     []byte(`{"block_number":1}`),
				NextRetryAt: future,
			},
			wantCount:   0,
			checkFields: false,
		},
		{
			name: "zero_next_retry_treated_as_now_and_returned",
			entry: thebegateway.OutboxEntry{
				Namespace: thebegateway.NamespaceTransaction,
				Method:    "WriteTransaction",
				Payload:   []byte(`{"tx_hash":"0xdef"}`),
				// zero NextRetryAt → Enqueue sets it to time.Now()
			},
			wantCount:   1,
			checkFields: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			mustEnqueue(t, store, tc.entry)

			entries := mustNext(t, store, 10)
			if len(entries) != tc.wantCount {
				t.Fatalf("want %d entries, got %d", tc.wantCount, len(entries))
			}

			if tc.checkFields && len(entries) > 0 {
				e := entries[0]
				if e.Namespace != tc.entry.Namespace {
					t.Errorf("Namespace: want %q, got %q", tc.entry.Namespace, e.Namespace)
				}
				if e.Method != tc.entry.Method {
					t.Errorf("Method: want %q, got %q", tc.entry.Method, e.Method)
				}
				if string(e.Payload) != string(tc.entry.Payload) {
					t.Errorf("Payload: want %q, got %q", tc.entry.Payload, e.Payload)
				}
				if e.Attempts != 0 {
					t.Errorf("Attempts: want 0, got %d", e.Attempts)
				}
			}
		})
	}
}

func TestAck(t *testing.T) {
	store := newStore(t)
	mustEnqueue(t, store, thebegateway.OutboxEntry{
		Namespace:   thebegateway.NamespaceAccount,
		Method:      "WriteAccount",
		Payload:     []byte(`{}`),
		NextRetryAt: time.Now().Add(-time.Second),
	})

	entries := mustNext(t, store, 10)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry before Ack, got %d", len(entries))
	}

	if err := store.Ack(context.Background(), entries[0].ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	after := mustNext(t, store, 10)
	if len(after) != 0 {
		t.Fatalf("want 0 entries after Ack, got %d", len(after))
	}
}

func TestIncrementAttempts(t *testing.T) {
	store := newStore(t)
	now := time.Now()

	mustEnqueue(t, store, thebegateway.OutboxEntry{
		Namespace:   thebegateway.NamespaceBlock,
		Method:      "WriteBlock",
		Payload:     []byte(`{}`),
		NextRetryAt: now.Add(-time.Second),
	})

	entries := mustNext(t, store, 10)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	id := entries[0].ID

	futureRetry := now.Add(10 * time.Minute)
	if err := store.IncrementAttempts(context.Background(), id, futureRetry); err != nil {
		t.Fatalf("IncrementAttempts: %v", err)
	}

	// Not yet returned — next_retry_at is in the future
	before := mustNext(t, store, 10)
	if len(before) != 0 {
		t.Fatalf("want 0 entries before futureRetry passes, got %d", len(before))
	}

	// Simulate time passing by setting next_retry_at to past
	pastRetry := now.Add(-time.Second)
	if err := store.IncrementAttempts(context.Background(), id, pastRetry); err != nil {
		t.Fatalf("IncrementAttempts (set past): %v", err)
	}

	after := mustNext(t, store, 10)
	if len(after) != 1 {
		t.Fatalf("want 1 entry after retry time passes, got %d", len(after))
	}
	// Two IncrementAttempts calls → attempts=2
	if after[0].Attempts != 2 {
		t.Errorf("Attempts: want 2, got %d", after[0].Attempts)
	}
}

func TestMaxAttemptsFilter(t *testing.T) {
	store := newStore(t)

	mustEnqueue(t, store, thebegateway.OutboxEntry{
		Namespace:   thebegateway.NamespaceSnapshot,
		Method:      "WriteSnapshot",
		Payload:     []byte(`{}`),
		NextRetryAt: time.Now().Add(-time.Second),
	})

	entries := mustNext(t, store, 10)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	id := entries[0].ID

	// Exhaust all attempts — MaxOutboxAttempts=3
	for i := 0; i < thebegateway.MaxOutboxAttempts; i++ {
		// Always set retry in past so Next can see it, until it exceeds max
		if err := store.IncrementAttempts(context.Background(), id, time.Now().Add(-time.Second)); err != nil {
			t.Fatalf("IncrementAttempts iteration %d: %v", i, err)
		}
	}

	exhausted := mustNext(t, store, 10)
	if len(exhausted) != 0 {
		t.Fatalf("want 0 entries after %d attempts, got %d", thebegateway.MaxOutboxAttempts, len(exhausted))
	}
}

func TestNextOrdering(t *testing.T) {
	store := newStore(t)
	now := time.Now()

	// Enqueue 3 entries with different past timestamps; expect ASC order by next_retry_at
	entries := []thebegateway.OutboxEntry{
		{Namespace: thebegateway.NamespaceBlock, Method: "B3", Payload: []byte(`{"n":3}`), NextRetryAt: now.Add(-1 * time.Second)},
		{Namespace: thebegateway.NamespaceBlock, Method: "B1", Payload: []byte(`{"n":1}`), NextRetryAt: now.Add(-3 * time.Second)},
		{Namespace: thebegateway.NamespaceBlock, Method: "B2", Payload: []byte(`{"n":2}`), NextRetryAt: now.Add(-2 * time.Second)},
	}
	for _, e := range entries {
		mustEnqueue(t, store, e)
	}

	got := mustNext(t, store, 10)
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}

	wantOrder := []string{"B1", "B2", "B3"}
	for i, want := range wantOrder {
		if got[i].Method != want {
			t.Errorf("position %d: want Method=%q, got %q", i, want, got[i].Method)
		}
	}
}

func TestExponentialBackoff(t *testing.T) {
	maxDelay := 5 * time.Minute

	tests := []struct {
		name        string
		attempts    int
		wantMinSecs float64
		wantMaxSecs float64
	}{
		{
			name:        "attempts_0_approx_1s",
			attempts:    0,
			wantMinSecs: 0.9,
			wantMaxSecs: 2.0,
		},
		{
			name:        "attempts_1_approx_2s",
			attempts:    1,
			wantMinSecs: 1.9,
			wantMaxSecs: 3.0,
		},
		{
			name:        "attempts_2_approx_4s",
			attempts:    2,
			wantMinSecs: 3.9,
			wantMaxSecs: 5.0,
		},
		{
			name:     "attempts_20_capped_at_5min",
			attempts: 20,
			// 2^20 = ~17 days; must be capped at 5 min
			wantMinSecs: 0,
			wantMaxSecs: maxDelay.Seconds() + 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			result := thebegateway.ExponentialBackoff(tc.attempts)
			after := time.Now()

			delay := result.Sub(before)

			if tc.attempts == 20 {
				// Cap check: must not exceed 5 minutes from call time
				maxAllowed := after.Add(maxDelay)
				if result.After(maxAllowed) {
					t.Errorf("attempts=20: result %v exceeds cap %v", result, maxAllowed)
				}
			} else {
				minDur := time.Duration(tc.wantMinSecs * float64(time.Second))
				maxDur := time.Duration(tc.wantMaxSecs * float64(time.Second))

				if delay < minDur || delay > maxDur {
					t.Errorf("attempts=%d: delay %v not in [%v, %v]", tc.attempts, delay, minDur, maxDur)
				}
			}
		})
	}
}
