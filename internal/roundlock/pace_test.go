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
