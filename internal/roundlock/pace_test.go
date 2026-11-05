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
	for h := uint64(100); h < 100+3*paceKeep; h++ {
		p.Elapsed(h)
	}
	if n := len(p.first); n > paceKeep+1 {
		t.Fatalf("first-seen map holds %d heights", n)
	}
}
