package DB_OPs

// Unit tests for the persisted ART ordinal counter after moving it off the
// removed ImmuDB Read/Update path onto the ThebeDB sync-state KV
// (GetSyncKV/PutSyncKV). Pins:
//   - fresh chain (key never written) → counter starts at 1
//   - reserve persists the advanced counter BEFORE handing ordinals out
//   - the failover floor raises the counter but never lowers it
//
// HOST-GATED: package DB_OPs pulls go-ethereum crypto (CGO). Run with:
//   CGO_ENABLED=1 go test ./DB_OPs/... -run Ordinal

import (
	"sync"
	"testing"

	"gossipnode/DB_OPs/store"
)

// artSyncKVHandle is a store.ThebeHandle whose only real behaviour is an
// in-memory sync-state KV. Every other method comes from the embedded (nil)
// interface and must never be exercised by these tests — art_ordinal only ever
// touches GetSyncKV/PutSyncKV.
type artSyncKVHandle struct {
	store.ThebeHandle
	mu sync.Mutex
	kv map[string][]byte
}

func newARTSyncKVHandle() *artSyncKVHandle {
	return &artSyncKVHandle{kv: make(map[string][]byte)}
}

func (h *artSyncKVHandle) PutSyncKV(key string, value []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	h.kv[key] = cp
	return nil
}

func (h *artSyncKVHandle) GetSyncKV(key string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.kv[key], nil // (nil, nil) when absent — matches the ThebeDB contract
}

func TestARTOrdinalReserveAndPersist(t *testing.T) {
	SetGlobalHandle(newARTSyncKVHandle())
	t.Cleanup(func() { SetGlobalHandle(nil) })

	// Fresh chain: nothing seeded → first reserved ordinal is 1.
	first, err := reserveARTOrdinals(2)
	if err != nil {
		t.Fatalf("reserve(2): %v", err)
	}
	if first != 1 {
		t.Fatalf("first ordinal = %d, want 1", first)
	}

	// The reservation persisted the advanced counter: next unassigned is 3.
	next, err := readARTOrdinalNext()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if next != 3 {
		t.Fatalf("next-after-reserve = %d, want 3", next)
	}

	// A subsequent reservation continues from 3 (proves persistence, not a
	// per-call reset).
	third, err := reserveARTOrdinals(1)
	if err != nil {
		t.Fatalf("reserve(1): %v", err)
	}
	if third != 3 {
		t.Fatalf("second reserve start = %d, want 3", third)
	}
}

func TestARTOrdinalFloorRaisesNeverLowers(t *testing.T) {
	SetGlobalHandle(newARTSyncKVHandle())
	t.Cleanup(func() { SetGlobalHandle(nil) })

	// Seed the counter to 5 by reserving 4 from a fresh chain (1 → next 5).
	if _, err := reserveARTOrdinals(4); err != nil {
		t.Fatalf("reserve(4): %v", err)
	}
	if n, _ := readARTOrdinalNext(); n != 5 {
		t.Fatalf("pre-floor next = %d, want 5", n)
	}

	// A floor at or below the counter is a no-op (never lowers).
	if err := BumpARTOrdinalFloor(3); err != nil {
		t.Fatalf("floor(3): %v", err)
	}
	if n, _ := readARTOrdinalNext(); n != 5 {
		t.Fatalf("floor lowered counter to %d, want 5", n)
	}

	// A higher floor raises the counter.
	if err := BumpARTOrdinalFloor(10); err != nil {
		t.Fatalf("floor(10): %v", err)
	}
	if n, _ := readARTOrdinalNext(); n != 10 {
		t.Fatalf("floor did not raise counter: %d, want 10", n)
	}

	// Zero and out-of-space floors are ignored.
	if err := BumpARTOrdinalFloor(0); err != nil {
		t.Fatalf("floor(0): %v", err)
	}
	if err := BumpARTOrdinalFloor(ARTOrdinalMax); err != nil {
		t.Fatalf("floor(max): %v", err)
	}
	if n, _ := readARTOrdinalNext(); n != 10 {
		t.Fatalf("ignored floor changed counter to %d, want 10", n)
	}
}
