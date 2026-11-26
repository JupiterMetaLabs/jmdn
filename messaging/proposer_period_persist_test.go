package messaging

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"gossipnode/config"
)

// memStore is an in-memory roundlock.Store standing in for the sync-state KV.
type memStore struct {
	mu      sync.Mutex
	kv      map[string][]byte
	failGet bool
	failPut bool
}

func newMemStore() *memStore { return &memStore{kv: map[string][]byte{}} }

func (m *memStore) Get(k string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet {
		return nil, errors.New("get failed")
	}
	return m.kv[k], nil
}

func (m *memStore) Put(k string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut {
		return errors.New("put failed")
	}
	m.kv[k] = append([]byte(nil), v...)
	return nil
}

// restart simulates a process restart: a fresh counter over the same store.
func restart(s *memStore) *ProposerPeriods {
	p := NewProposerPeriods()
	p.SetStore(s, "proposerperiod:")
	return p
}

// The incident: proposed at period 1, crashed/restarted before the round
// ended. The new process must not reuse period 1.
func TestProposerPeriods_RestartAfterClaimSkipsTheClaimedPeriod(t *testing.T) {
	s := newMemStore()
	p := restart(s)
	if got, err := p.ClaimPeriod(22464); err != nil || got != 0 {
		t.Fatalf("first claim: %d, %v; want 0", got, err)
	}
	p.AdvanceAfterFailure(22464, 7)
	if got, err := p.ClaimPeriod(22464); err != nil || got != 1 {
		t.Fatalf("second claim: %d, %v; want 1", got, err)
	}
	p = restart(s)
	if got, err := p.ClaimPeriod(22464); err != nil || got != 2 {
		t.Fatalf("after restart: %d, %v; want 2 (period 1 is burned)", got, err)
	}
}

// Restart after a failure but before the next claim: resumes at the period
// the failure moved to, not one above it.
func TestProposerPeriods_RestartAfterFailureResumesAtNext(t *testing.T) {
	s := newMemStore()
	p := restart(s)
	p.ClaimPeriod(10) // period 0 proposed
	p.AdvanceAfterFailure(10, 7)
	p = restart(s)
	if got := p.PeriodFor(10); got != 1 {
		t.Fatalf("after restart: %d, want 1", got)
	}
}

// An all-absent round after a restart (no hints) must still advance above the
// stored round, not to stored+0.
func TestProposerPeriods_AbsentRoundAfterRestartStillAdvances(t *testing.T) {
	s := newMemStore()
	p := restart(s)
	p.ClaimPeriod(10)
	p.AdvanceAfterFailure(10, 7)
	p.ClaimPeriod(10) // period 1 proposed, then crash
	p = restart(s)
	got, _ := p.ClaimPeriod(10) // 2
	if got != 2 {
		t.Fatalf("claim after restart: %d, want 2", got)
	}
	if next, _ := p.AdvanceAfterFailure(10, 7); next != 3 { // no hints at all
		t.Fatalf("advance with no hints: %d, want 3", next)
	}
}

// Hints still win when they are higher than the stored record (e.g. a store
// that was wiped, or a new sequencer).
func TestProposerPeriods_HintsStillRaiseAboveStore(t *testing.T) {
	s := newMemStore()
	p := restart(s)
	p.ClaimPeriod(10)
	for i, v := range []uint64{5, 5, 5} {
		p.NoteRefusal(10, string(rune('a'+i)), v)
	}
	if next, _ := p.AdvanceAfterFailure(10, 7); next != 6 {
		t.Fatalf("advance: %d, want 6", next)
	}
}

// Commit clears the stored record, so the height is not carried forever.
func TestProposerPeriods_CommitClearsStoredRecord(t *testing.T) {
	s := newMemStore()
	p := restart(s)
	p.ClaimPeriod(10)
	p.AdvanceAfterFailure(10, 7)
	p.ClaimPeriod(10)
	p.ForgetThrough(10)
	if v := s.kv["proposerperiod:10"]; len(v) != 0 {
		t.Fatalf("stored record not cleared: %q", v)
	}
	p = restart(s)
	if got := p.PeriodFor(10); got != 0 {
		t.Fatalf("after commit + restart: %d, want 0", got)
	}
}

// Fail closed: if the record cannot be written, no period is handed out.
func TestProposerPeriods_ClaimFailsClosedOnStoreError(t *testing.T) {
	s := newMemStore()
	p := restart(s)
	s.failPut = true
	if _, err := p.ClaimPeriod(10); err == nil {
		t.Fatal("claim with failing Put succeeded")
	}
	s.failPut, s.failGet = false, true
	p = restart(s)
	if _, err := p.ClaimPeriod(10); err == nil {
		t.Fatal("claim with failing Get succeeded")
	}
}

// A stored period at the cap stays at the cap after restart (no overflow).
func TestProposerPeriods_StoredCapStaysAtCap(t *testing.T) {
	s := newMemStore()
	limit := config.MaxPeriodAt(10)
	s.kv["proposerperiod:10"] = []byte(`{"last_proposed":` + strconv.FormatUint(limit, 10) + `}`)
	p := restart(s)
	if got := p.PeriodFor(10); got != limit {
		t.Fatalf("at cap: %d, want %d", got, limit)
	}
}

// Without a store the behaviour is exactly the old in-memory one.
func TestProposerPeriods_NoStoreIsInMemory(t *testing.T) {
	p := NewProposerPeriods()
	if got, err := p.ClaimPeriod(10); err != nil || got != 0 {
		t.Fatalf("claim: %d, %v", got, err)
	}
	p.AdvanceAfterFailure(10, 7)
	if got := NewProposerPeriods().PeriodFor(10); got != 0 {
		t.Fatalf("fresh in-memory counter: %d, want 0", got)
	}
}
