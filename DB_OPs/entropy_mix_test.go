package DB_OPs

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"gossipnode/DB_OPs/store"
)

// mixKVHandle is an in-memory sync KV. Only GetSyncKV/PutSyncKV are used by
// the entropy mix store; any other method panics via the nil embedded
// interface, which is what we want if the store ever reaches beyond the KV.
type mixKVHandle struct {
	store.ThebeHandle
	mu      sync.Mutex
	kv      map[string][]byte
	readErr error
	puts    int
}

func (h *mixKVHandle) GetSyncKV(key string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.readErr != nil {
		return nil, h.readErr
	}
	v, ok := h.kv[key]
	if !ok {
		return nil, errors.New("key not found")
	}
	return append([]byte(nil), v...), nil
}

func (h *mixKVHandle) PutSyncKV(key string, value []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kv[key] = append([]byte(nil), value...)
	h.puts++
	return nil
}

func withMixKV(t *testing.T) *mixKVHandle {
	t.Helper()
	h := &mixKVHandle{kv: map[string][]byte{}}
	SetGlobalHandle(h)
	t.Cleanup(func() { SetGlobalHandle(nil) })
	return h
}

const (
	mixA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mixB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestEntropyMixKeyFrozen(t *testing.T) {
	if got := EntropyMixKey(0); got != "entropy_mix:0" {
		t.Fatalf("key format changed: %q", got)
	}
	if got := EntropyMixKey(12345); got != "entropy_mix:12345" {
		t.Fatalf("key format changed: %q", got)
	}
}

func TestEntropyMixRoundTripAndIdempotence(t *testing.T) {
	h := withMixKV(t)

	if _, found, err := GetEntropyMix(nil, 7); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}
	if err := RecordEntropyMix(nil, EntropyMixRecord{Epoch: 7, SeedHex: strings.ToUpper(mixA), Outcome: "mixed"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	rec, found, err := GetEntropyMix(nil, 7)
	if err != nil || !found {
		t.Fatalf("read back: found=%v err=%v", found, err)
	}
	if rec.SeedHex != mixA || rec.Outcome != "mixed" || rec.Epoch != 7 {
		t.Fatalf("round trip mismatch: %+v", rec)
	}
	// Identical rewrite (different outcome label, same seed) is a no-op.
	if err := RecordEntropyMix(nil, EntropyMixRecord{Epoch: 7, SeedHex: mixA, Outcome: "mixed"}); err != nil {
		t.Fatalf("identical rewrite must be idempotent: %v", err)
	}
	if h.puts != 1 {
		t.Fatalf("identical rewrite must not touch storage; puts=%d", h.puts)
	}
}

func TestEntropyMixRefusesConflictingValue(t *testing.T) {
	withMixKV(t)
	if err := RecordEntropyMix(nil, EntropyMixRecord{Epoch: 3, SeedHex: mixA, Outcome: "mixed"}); err != nil {
		t.Fatal(err)
	}
	err := RecordEntropyMix(nil, EntropyMixRecord{Epoch: 3, SeedHex: mixB, Outcome: "fallback"})
	if err == nil || !strings.Contains(err.Error(), "DIFFERENT") {
		t.Fatalf("a conflicting mix must be refused, got %v", err)
	}
	rec, _, _ := GetEntropyMix(nil, 3)
	if rec.SeedHex != mixA {
		t.Fatalf("stored value was overwritten: %s", rec.SeedHex)
	}
}

func TestEntropyMixFailsClosedOnReadError(t *testing.T) {
	h := withMixKV(t)
	h.readErr = errors.New("disk on fire")
	if err := RecordEntropyMix(nil, EntropyMixRecord{Epoch: 1, SeedHex: mixA}); err == nil {
		t.Fatal("a pre-read error must fail the write closed")
	}
	if h.puts != 0 {
		t.Fatal("write reached storage despite the failed pre-read")
	}
	if _, _, err := GetEntropyMix(nil, 1); err == nil {
		t.Fatal("a read error must surface, not read as 'absent'")
	}
}

func TestEntropyMixRejectsMalformed(t *testing.T) {
	h := withMixKV(t)
	for _, bad := range []string{"", "zz", mixA[:62], mixA + "00"} {
		if err := RecordEntropyMix(nil, EntropyMixRecord{Epoch: 1, SeedHex: bad}); err == nil {
			t.Fatalf("seed %q must be rejected", bad)
		}
	}
	if h.puts != 0 {
		t.Fatal("malformed seed reached storage")
	}
	// A record whose body claims a different epoch is rejected on read.
	h.kv[EntropyMixKey(9)] = []byte(`{"epoch":8,"seed":"` + mixA + `","outcome":"mixed"}`)
	if _, _, err := GetEntropyMix(nil, 9); err == nil {
		t.Fatal("epoch mismatch inside the record must be rejected")
	}
}
