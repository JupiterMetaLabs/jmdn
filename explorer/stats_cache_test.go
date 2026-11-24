package explorer

import (
	"errors"
	"testing"
	"time"
)

func TestCachedCount_TTLAndLastGood(t *testing.T) {
	var c countCache
	calls := 0
	fetch := func() (int64, error) { calls++; return int64(40 + calls), nil }

	v, err := cachedCount(&c, fetch)
	if err != nil || v != 41 || calls != 1 {
		t.Fatalf("first: v=%d err=%v calls=%d", v, err, calls)
	}
	v, _ = cachedCount(&c, fetch)
	if v != 41 || calls != 1 {
		t.Fatalf("within TTL must not refetch: v=%d calls=%d", v, calls)
	}

	c.fetched = time.Now().Add(-2 * statsCountTTL)
	v, _ = cachedCount(&c, fetch)
	if v != 42 || calls != 2 {
		t.Fatalf("after TTL must refetch: v=%d calls=%d", v, calls)
	}

	c.fetched = time.Now().Add(-2 * statsCountTTL)
	if _, err := cachedCount(&c, func() (int64, error) { return 0, errors.New("db down") }); err == nil {
		t.Fatal("fetch error must propagate")
	}
	if got, ok := c.lastGood(); got != 42 || !ok {
		t.Fatalf("lastGood after error = (%d, %v), want (42, true)", got, ok)
	}
}

// A cache that has never fetched successfully must report ok=false, so callers
// surface the error instead of serving a fabricated 0. This is what separates
// "stale but real" from "no idea" — the distinction the stats handlers rely on.
func TestCachedCount_ColdCacheReportsNoGoodValue(t *testing.T) {
	var c countCache

	if got, ok := c.lastGood(); got != 0 || ok {
		t.Fatalf("cold cache lastGood = (%d, %v), want (0, false)", got, ok)
	}

	if _, err := cachedCount(&c, func() (int64, error) { return 0, errors.New("db down") }); err == nil {
		t.Fatal("fetch error must propagate")
	}
	if got, ok := c.lastGood(); got != 0 || ok {
		t.Fatalf("after a failed first fetch lastGood = (%d, %v), want (0, false)", got, ok)
	}

	if _, err := cachedCount(&c, func() (int64, error) { return 7, nil }); err != nil {
		t.Fatalf("successful fetch: %v", err)
	}
	if got, ok := c.lastGood(); got != 7 || !ok {
		t.Fatalf("after success lastGood = (%d, %v), want (7, true)", got, ok)
	}
}
