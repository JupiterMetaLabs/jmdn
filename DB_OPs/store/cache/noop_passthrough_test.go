package cache

import (
	"context"
	"testing"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
)

type fakeProofStore struct {
	store.ZKProofStore
	rec *thebegateway.ZKProofRecord
}

func (f *fakeProofStore) GetZKProof(_ context.Context, _ uint64) (*thebegateway.ZKProofRecord, error) {
	return f.rec, nil
}

// countingCache is a real (non-noop) cache that records Set calls.
type countingCache struct {
	noopCache
	sets int
}

func (c *countingCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	c.sets++
	return nil
}

// With no cache (nil or noop) the decorators are skipped entirely, so a read
// never json-encodes the record.
func TestNewCachedStores_NoopReturnsInner(t *testing.T) {
	inner := &fakeProofStore{}
	if got := NewCachedZKProofStore(inner, NewNoopCache()); got != store.ZKProofStore(inner) {
		t.Fatalf("noop cache: got %T, want the inner store", got)
	}
	if got := NewCachedZKProofStore(inner, nil); got != store.ZKProofStore(inner) {
		t.Fatalf("nil cache: got %T, want the inner store", got)
	}
	if _, ok := NewCachedZKProofStore(inner, &countingCache{}).(*cachedZKProofStore); !ok {
		t.Fatal("a real cache must still be wrapped")
	}
}

// A real cache stores small proofs but not multi-MB ones.
func TestCachedZKProofStore_SkipsLargeProofs(t *testing.T) {
	c := &countingCache{}
	small := NewCachedZKProofStore(&fakeProofStore{rec: &thebegateway.ZKProofRecord{StarkProof: make([]byte, 1024)}}, c)
	if _, err := small.GetZKProof(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if c.sets != 1 {
		t.Fatalf("small proof: sets=%d, want 1", c.sets)
	}
	big := NewCachedZKProofStore(&fakeProofStore{rec: &thebegateway.ZKProofRecord{StarkProof: make([]byte, maxCachedProofBytes+1)}}, c)
	rec, err := big.GetZKProof(context.Background(), 2)
	if err != nil || len(rec.StarkProof) != maxCachedProofBytes+1 {
		t.Fatalf("large proof read: rec=%v err=%v", rec != nil, err)
	}
	if c.sets != 1 {
		t.Fatalf("large proof must not be cached: sets=%d", c.sets)
	}
}
