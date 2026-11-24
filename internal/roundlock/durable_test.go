package roundlock

import (
	"errors"
	"testing"
)

type memStore struct {
	m       map[string][]byte
	failPut bool
	failGet bool
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(k string) ([]byte, error) {
	if s.failGet {
		return nil, errors.New("read failed")
	}
	return s.m[k], nil
}

func (s *memStore) Put(k string, v []byte) error {
	if s.failPut {
		return errors.New("disk full")
	}
	s.m[k] = append([]byte(nil), v...)
	return nil
}

func durable(st *memStore) *Ledger {
	l := NewLedger()
	l.SetStore(st, "rl:")
	return l
}

func TestDurable_RestartKeepsR1AndR2(t *testing.T) {
	st := newMemStore()
	l := durable(st)
	if ok, _, _, fresh := l.TryLockFresh(Round{Height: 50, Period: 2}, "A/c1"); !ok || !fresh {
		t.Fatal("first lock refused")
	}

	re := durable(st) // restart: empty memory, same store
	if ok, why, _, _ := re.TryLockFresh(Round{Height: 50, Period: 2}, "B/c2"); ok || why != Conflict {
		t.Fatalf("restart reopened the round: ok=%v why=%v", ok, why)
	}
	if ok, why, high, _ := re.TryLockFresh(Round{Height: 50, Period: 1}, "C/c3"); ok || why != Stale || high != 2 {
		t.Fatalf("restart lost the highest period: ok=%v why=%v high=%d", ok, why, high)
	}
	if ok, _, _, fresh := re.TryLockFresh(Round{Height: 50, Period: 2}, "A/c1"); !ok || fresh {
		t.Fatalf("same block after restart must be an idempotent re-entry: ok=%v fresh=%v", ok, fresh)
	}
	if ok, _, high, _ := re.TryLockFresh(Round{Height: 50, Period: 3}, "D/c4"); !ok || high != 3 {
		t.Fatal("a higher period must be open after restart")
	}
}

func TestDurable_WriteFailureRefusesAndLeavesNoReservation(t *testing.T) {
	st := newMemStore()
	st.failPut = true
	l := durable(st)
	ok, why, _, _ := l.TryLockFresh(Round{Height: 7}, "A/c")
	if ok || why != Unavailable {
		t.Fatalf("ok=%v why=%v, want refusal Unavailable", ok, why)
	}
	st.failPut = false
	if ok, _, _, fresh := l.TryLockFresh(Round{Height: 7}, "B/c"); !ok || !fresh {
		t.Fatal("a refused reservation must not linger in memory")
	}
}

func TestDurable_ReadFailureRefuses(t *testing.T) {
	st := newMemStore()
	st.failGet = true
	l := durable(st)
	if ok, why, _, _ := l.TryLockFresh(Round{Height: 9}, "A/c"); ok || why != Unavailable {
		t.Fatalf("ok=%v why=%v, want refusal Unavailable", ok, why)
	}
}

func TestDurable_CorruptRecordRefuses(t *testing.T) {
	st := newMemStore()
	st.m["rl:11"] = []byte("{not json")
	l := durable(st)
	if ok, why, _, _ := l.TryLockFresh(Round{Height: 11}, "A/c"); ok || why != Unavailable {
		t.Fatalf("ok=%v why=%v, want refusal Unavailable", ok, why)
	}
}

func TestDurable_ReleaseClearsStore(t *testing.T) {
	st := newMemStore()
	l := durable(st)
	l.TryLockFresh(Round{Height: 5}, "A/c")
	l.Release(Round{Height: 5}, "A/c")
	if ok, _, _, _ := durable(st).TryLockFresh(Round{Height: 5}, "B/c"); !ok {
		t.Fatal("released round still reserved after restart")
	}
}

func TestDurable_ForgetThroughClearsStoredHeights(t *testing.T) {
	st := newMemStore()
	l := durable(st)
	l.TryLockFresh(Round{Height: 10}, "A/c")
	l.TryLockFresh(Round{Height: 100}, "B/c")
	l.ForgetThrough(100 + commitMargin - 1) // floor = 99: prunes 10, keeps 100
	if len(st.m["rl:10"]) != 0 {
		t.Fatal("pruned height still stored")
	}
	if len(st.m["rl:100"]) == 0 {
		t.Fatal("height above the floor was cleared")
	}
}
