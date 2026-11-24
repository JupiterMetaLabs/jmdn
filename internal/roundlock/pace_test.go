package roundlock

import (
	"testing"
	"time"
)

func TestPace(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := NewPace(func() time.Time { return now })
	if err := p.Check(10, 1024, time.Second, 16); err == nil {
		t.Fatal("first proposal at a height jumped to period 1024")
	}
	if err := p.Check(10, 16, time.Second, 16); err != nil {
		t.Fatalf("period at the slack: %v", err)
	}
	now = now.Add(30 * time.Second)
	if err := p.Check(10, 46, time.Second, 16); err != nil {
		t.Fatalf("period 46 after 30 s: %v", err)
	}
	if err := p.Check(10, 47, time.Second, 16); err == nil {
		t.Fatal("period 47 after 30 s accepted")
	}
	// The clock starts at the first proposal at each height.
	if err := p.Check(11, 40, time.Second, 16); err == nil {
		t.Fatal("new height inherited the previous height's elapsed time")
	}
	if err := p.Check(12, 1024, 0, 16); err != nil {
		t.Fatal("minRound 0 must turn the check off")
	}
}

// A far-ahead height cannot stop the live height's clock from running, and
// committed heights are dropped.
func TestPace_FarAheadHeightAndCommits(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := NewPace(func() time.Time { return now })
	p.ForgetThrough(99)
	p.Elapsed(1 << 60)
	if err := p.Check(100, 0, time.Second, 16); err != nil {
		t.Fatal(err)
	}
	now = now.Add(60 * time.Second)
	if err := p.Check(100, 20, time.Second, 16); err != nil {
		t.Fatalf("live height's clock stopped by a far-ahead height: %v", err)
	}
	if _, ok := p.first[1<<60]; ok {
		t.Fatal("far-ahead height recorded")
	}
	p.ForgetThrough(100)
	if _, ok := p.first[100]; ok {
		t.Fatal("committed height kept")
	}
	// Before any commit is known, the map stays bounded.
	q := NewPace(func() time.Time { return now })
	for h := uint64(1); h <= 3*paceMaxHeights; h++ {
		q.Elapsed(h)
	}
	if len(q.first) > paceMaxHeights {
		t.Fatalf("map holds %d heights", len(q.first))
	}
}

// With a store, a restart keeps the time a height was first proposed, so the
// round that resumes above a period recorded before the restart is not
// refused as ahead of time.
func TestPace_DurableAcrossRestart(t *testing.T) {
	st := newMemStore()
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }
	p := NewPace(clock)
	p.SetStore(st, "pace:")
	p.Elapsed(500) // first proposal at the height

	now = now.Add(6 * time.Hour)
	poisoned := uint64(16 + 6*3600)
	if err := p.Check(500, poisoned, time.Second, 16); err != nil {
		t.Fatalf("setup: %v", err)
	}

	restarted := NewPace(clock)
	restarted.SetStore(st, "pace:")
	if err := restarted.Check(500, poisoned+1, time.Second, 16); err == nil {
		t.Fatal("expected the period to still be ahead by one second")
	}
	now = now.Add(time.Second)
	if err := restarted.Check(500, poisoned+1, time.Second, 16); err != nil {
		t.Fatalf("restarted node refused the recovery round: %v", err)
	}

	// A commit drops the stored record.
	restarted.ForgetThrough(500)
	if raw, _ := st.Get("pace:500"); len(raw) != 0 {
		t.Fatal("stored first-seen time kept after commit")
	}
}
