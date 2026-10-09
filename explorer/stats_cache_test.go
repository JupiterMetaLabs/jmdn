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
	if got := c.lastGood(); got != 42 {
		t.Fatalf("lastGood after error = %d, want 42", got)
	}
}
