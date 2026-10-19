package roundlock

import (
	"errors"
	"testing"
)

type memStore struct {
	m       map[string][]byte
	failPut bool
}

func (s *memStore) Get(k string) ([]byte, error) { return s.m[k], nil }
func (s *memStore) Put(k string, v []byte) error {
	if s.failPut {
		return errors.New("disk full")
	}
	s.m[k] = append([]byte(nil), v...)
	return nil
}

func TestHashBinding_RefusesSecondBlock(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 10, Period: 0}
	if ok, _, _, _, err := l.TryLockHash(r, Block, "0xA"); !ok || err != nil {
		t.Fatalf("first block refused: ok=%v err=%v", ok, err)
	}
	if ok, _, _, _, _ := l.TryLockHash(r, Block, "0xA"); !ok {
		t.Fatal("same block must stay idempotent")
	}
	ok, taken, takenHash, _, _ := l.TryLockHash(r, Block, "0xB")
	if ok || taken != Block || takenHash != "0xA" {
		t.Fatalf("second block: ok=%v taken=%v hash=%q", ok, taken, takenHash)
	}
	if ok, _, _, _, _ := l.TryLockHash(Round{Height: 10, Period: 1}, Block, "0xB"); !ok {
		t.Fatal("a new period must accept a new block")
	}
	if ok, _ := l.TryLock(r, Timeout); ok {
		t.Fatal("block/timeout exclusion lost")
	}
}

func TestUnboundReservation_KeepsOldBehaviour(t *testing.T) {
	l := NewLedger()
	r := Round{Height: 3}
	l.TryLockHash(r, Block, "")
	if ok, _, _, _, _ := l.TryLockHash(r, Block, ""); !ok {
		t.Fatal("unbound re-entry refused")
	}
}

func TestDurable_SurvivesRestart(t *testing.T) {
	st := &memStore{m: map[string][]byte{}}
	l := NewLedger()
	l.SetStore(st, "rl:")
	r := Round{Height: 42, Period: 1}
	if ok, _, _, _, err := l.TryLockHash(r, Block, "0xA"); !ok || err != nil {
		t.Fatal("lock failed")
	}

	restarted := NewLedger()
	restarted.SetStore(st, "rl:")
	if ok, _, _, _, _ := restarted.TryLockHash(r, Block, "0xB"); ok {
		t.Fatal("restart reopened the round for a different block")
	}
	if ok, _ := restarted.TryLock(r, Timeout); ok {
		t.Fatal("restart reopened the round for a timeout")
	}
	if side, ok := restarted.Signed(r); !ok || side != Block {
		t.Fatalf("Signed = %v %v", side, ok)
	}
}

func TestDurable_PutFailureRefuses(t *testing.T) {
	st := &memStore{m: map[string][]byte{}, failPut: true}
	l := NewLedger()
	l.SetStore(st, "rl:")
	ok, _, _, _, err := l.TryLockHash(Round{Height: 1}, Block, "0xA")
	if ok || !errors.Is(err, ErrStore) {
		t.Fatalf("ok=%v err=%v, want refusal with ErrStore", ok, err)
	}
	if ok, _ := l.TryLock(Round{Height: 1}, Timeout); ok {
		t.Fatal("store failure must refuse every side")
	}
}

func TestDurable_ReleaseClearsStore(t *testing.T) {
	st := &memStore{m: map[string][]byte{}}
	l := NewLedger()
	l.SetStore(st, "rl:")
	r := Round{Height: 5}
	l.TryLockHash(r, Block, "0xA")
	l.Release(r, Block)
	restarted := NewLedger()
	restarted.SetStore(st, "rl:")
	if ok, _ := restarted.TryLock(r, Timeout); !ok {
		t.Fatal("released round still reserved after restart")
	}
}
