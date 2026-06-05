// White-box test for the bounded-enqueue chunking logic (enqueueRecordsChunked).
// Lives in package NodeInfo because the helper, the RedisStreamer constants, and the
// payload-type tags are unexported. No live Redis/ImmuDB needed — a recording mock
// streamer captures every XADD so we can assert chunk boundaries.
//
// NOTE: craftcode Phase 6 prefers tests under a tests/ tree; Go package-internal
// visibility forces this same-dir _test.go. Matches the repo convention in
// DB_OPs/sqlops/sqlops_test.go.
package NodeInfo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// recordingStreamer captures Enqueue payloads and optionally fails selected chunks.
// Only Enqueue is exercised; the rest satisfy RedisStreamer with inert returns.
type recordingStreamer struct {
	messages []map[string]any
	calls    int
	failEach int // if >0, every Nth Enqueue call returns an error
}

func (r *recordingStreamer) Enqueue(_ context.Context, _ string, values map[string]any) (string, error) {
	r.calls++
	if r.failEach > 0 && r.calls%r.failEach == 0 {
		return "", errors.New("simulated XADD failure")
	}
	r.messages = append(r.messages, values)
	return "id", nil
}

func (r *recordingStreamer) EnsureConsumerGroup(context.Context, string, string) error { return nil }
func (r *recordingStreamer) ReadGroup(context.Context, string, string, string, int64, time.Duration) ([]StreamEntry, error) {
	return nil, nil
}
func (r *recordingStreamer) Ack(context.Context, string, string, ...string) error    { return nil }
func (r *recordingStreamer) Delete(context.Context, string, ...string) error          { return nil }
func (r *recordingStreamer) AutoClaim(context.Context, string, string, string, time.Duration, string, int64) ([]StreamEntry, string, error) {
	return nil, "0-0", nil
}
func (r *recordingStreamer) Len(context.Context, string) (int64, error)               { return 0, nil }
func (r *recordingStreamer) PendingCount(context.Context, string, string) (int64, error) { return 0, nil }

// decodeCount returns how many records a recorded message's "data" field holds.
func decodeCount(t *testing.T, msg map[string]any) int {
	t.Helper()
	data, ok := msg["data"].(string)
	if !ok {
		t.Fatalf("message missing string data field: %#v", msg)
	}
	var recs []json.RawMessage
	if err := json.Unmarshal([]byte(data), &recs); err != nil {
		t.Fatalf("data is not a JSON array: %v", err)
	}
	return len(recs)
}

func TestEnqueueRecordsChunked_Boundaries(t *testing.T) {
	cases := []struct {
		name      string
		n         int
		wantMsgs  int
	}{
		{"empty", 0, 0},
		{"single", 1, 1},
		{"under_one_chunk", 499, 1},
		{"exactly_one_chunk", 500, 1},
		{"one_over", 501, 2},
		{"two_chunks", 1000, 2},
		{"uneven", 2500, 5},
		{"uneven_remainder", 2501, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]int, tc.n)
			for i := range items {
				items[i] = i
			}
			rs := &recordingStreamer{}
			err := enqueueRecordsChunked(context.Background(), rs, payloadTypeAccounts, items)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rs.messages) != tc.wantMsgs {
				t.Fatalf("message count = %d, want %d", len(rs.messages), tc.wantMsgs)
			}
			total := 0
			for _, msg := range rs.messages {
				if tag, _ := msg["type"].(string); tag != string(payloadTypeAccounts) {
					t.Fatalf("type tag = %q, want %q", tag, payloadTypeAccounts)
				}
				c := decodeCount(t, msg)
				if c > maxRecordsPerMessage {
					t.Fatalf("chunk holds %d records, exceeds cap %d", c, maxRecordsPerMessage)
				}
				total += c
			}
			if total != tc.n {
				t.Fatalf("total records across messages = %d, want %d", total, tc.n)
			}
		})
	}
}

// TestEnqueueRecordsChunked_BestEffort verifies that a transient failure on one chunk
// does not drop the others: the helper attempts every chunk, returns an aggregated
// error, yet the successful chunks are still enqueued.
func TestEnqueueRecordsChunked_BestEffort(t *testing.T) {
	const n = 2500 // 5 chunks of 500
	items := make([]int, n)
	rs := &recordingStreamer{failEach: 3} // fail the 3rd Enqueue call

	err := enqueueRecordsChunked(context.Background(), rs, payloadTypeAccounts, items)
	if err == nil {
		t.Fatal("expected aggregated error from failed chunk, got nil")
	}
	if rs.calls != 5 {
		t.Fatalf("Enqueue attempted %d times, want 5 (all chunks attempted despite failure)", rs.calls)
	}
	if len(rs.messages) != 4 {
		t.Fatalf("recorded %d successful messages, want 4 (one chunk failed)", len(rs.messages))
	}
}

func TestChunkCount(t *testing.T) {
	cases := map[int]int{0: 0, 1: 1, 499: 1, 500: 1, 501: 2, 1000: 2, 2500: 5}
	for n, want := range cases {
		if got := chunkCount(n); got != want {
			t.Errorf("chunkCount(%d) = %d, want %d", n, got, want)
		}
	}
}
