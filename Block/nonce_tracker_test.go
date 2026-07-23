package Block

import (
	"testing"
	"time"
)

func newTestTracker(ttl time.Duration) (*PendingNonceTracker, *time.Time) {
	now := time.Unix(1_752_570_000, 0)
	t := NewPendingNonceTracker(ttl)
	t.now = func() time.Time { return now }
	return t, &now
}

func TestPendingNonceTracker_NextAfterRecord(t *testing.T) {
	tr, _ := newTestTracker(time.Minute)

	tr.Record("0xAbC", 7)

	// case-insensitive lookup; next = highest+1 when above confirmed
	if got := tr.NextFor("0xabc", 5); got != 8 {
		t.Errorf("NextFor = %d, want 8", got)
	}
	// confirmed state wins when it has caught up (or passed)
	if got := tr.NextFor("0xabc", 9); got != 9 {
		t.Errorf("confirmed must win upward: got %d, want 9", got)
	}
	// unknown sender → confirmed
	if got := tr.NextFor("0xother", 3); got != 3 {
		t.Errorf("unknown sender must return confirmed: got %d, want 3", got)
	}
}

func TestPendingNonceTracker_KeepsHighestOnly(t *testing.T) {
	tr, _ := newTestTracker(time.Minute)

	tr.Record("0xabc", 7)
	tr.Record("0xabc", 5) // lower resubmit must not regress

	if got := tr.NextFor("0xabc", 0); got != 8 {
		t.Errorf("lower record must not regress highest: got %d, want 8", got)
	}
}

func TestPendingNonceTracker_TTLExpiry(t *testing.T) {
	tr, now := newTestTracker(time.Minute)

	tr.Record("0xabc", 7)
	*now = now.Add(2 * time.Minute) // past TTL

	if got := tr.NextFor("0xabc", 5); got != 5 {
		t.Errorf("expired entry must be ignored: got %d, want confirmed 5", got)
	}
	// and lazily deleted
	tr.mu.RLock()
	_, still := tr.entries["0xabc"]
	tr.mu.RUnlock()
	if still {
		t.Error("expired entry must be lazily deleted")
	}
}

// TestSubmitToMempool_RecordsPendingNonce pins the accept-path feed: a
// successfully routed tx must be visible to pending-nonce queries. (The
// primary, race-free feed is the synchronous Record in SubmitRawTransaction
// — Server.go — which runs before the RPC returns; this accept-path Record
// is the idempotent belt.)
func TestSubmitToMempool_RecordsPendingNonce(t *testing.T) {
	fake := &fakeRouter{submitResult: &SubmitResult{Accepted: true, Hash: "0xh"}}
	withRouter(t, fake)

	tx := fullTx() // From = 0x1111..., Nonce = 42
	if err := SubmitToMempool(t.Context(), tx, "0xh"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := GetPendingNonceTracker().NextFor(tx.From.Hex(), 0); got != 43 {
		t.Errorf("tracker must know nonce 42 after accepted submit: NextFor = %d, want 43", got)
	}
}

func TestPendingNonceTracker_LowerRecordDoesNotExtendTTL(t *testing.T) {
	tr, now := newTestTracker(time.Minute)

	tr.Record("0xabc", 7)
	*now = now.Add(50 * time.Second)
	tr.Record("0xabc", 3) // stale lower resubmit near expiry
	*now = now.Add(30 * time.Second)

	// 80s since the nonce-7 record: entry must be expired — the nonce-3
	// resubmit must not have refreshed the timestamp of the higher entry.
	if got := tr.NextFor("0xabc", 5); got != 5 {
		t.Errorf("lower record must not extend TTL: got %d, want 5", got)
	}
}
