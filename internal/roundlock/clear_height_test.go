package roundlock

import (
	"math"
	"testing"
)

// ClearHeight reopens a stuck height in memory and on disk, and refuses a
// committed one.
func TestClearHeight(t *testing.T) {
	st := newMemStore()
	l := durable(st)
	const h = 90
	if ok, _, _, _ := l.TryLockFresh(Round{Height: h, Period: math.MaxUint64}, "a"); !ok {
		t.Fatal("setup")
	}
	if ok, r, _, _ := l.TryLockFresh(Round{Height: h, Period: 1}, "b"); ok || r != Stale {
		t.Fatalf("before clear: ok=%v r=%v", ok, r)
	}
	cleared, err := l.ClearHeight(h)
	if err != nil || cleared[math.MaxUint64] != "a" || len(cleared) != 1 {
		t.Fatalf("clear: cleared=%v err=%v", cleared, err)
	}
	if ok, r, _, _ := l.TryLockFresh(Round{Height: h, Period: 1}, "b"); !ok {
		t.Fatalf("after clear: refused %v", r)
	}
	// After a restart only the honest record (period 1) is read back.
	restarted := durable(st)
	if ok, _, high, _ := restarted.TryLockFresh(Round{Height: h, Period: 1}, "b"); !ok || high != 1 {
		t.Fatalf("after restart: ok=%v highest=%d", ok, high)
	}

	l.ForgetThrough(h)
	if _, err := l.ClearHeight(h); err == nil {
		t.Fatal("cleared a committed height")
	}
}
