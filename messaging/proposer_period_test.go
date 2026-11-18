package messaging

import (
	"testing"

	"gossipnode/config"
)

func TestProposerPeriods_StartsAtZeroAndAdvancesByOne(t *testing.T) {
	p := NewProposerPeriods()
	if got := p.PeriodFor(10); got != 0 {
		t.Fatalf("fresh height: period %d, want 0", got)
	}
	if got, _ := p.AdvanceAfterFailure(10, 7); got != 1 {
		t.Fatalf("after one failure: %d, want 1", got)
	}
	if got, _ := p.AdvanceAfterFailure(10, 7); got != 2 {
		t.Fatalf("after two failures: %d, want 2", got)
	}
	if got := p.PeriodFor(11); got != 0 {
		t.Fatalf("other heights are independent, got %d", got)
	}
}

// A sequencer that restarted mid-height resumes above the rounds the buddies
// already signed, using the (f+1)-th largest report. n=7 gives f=2.
func TestProposerPeriods_RestartResumesAboveSignedRounds(t *testing.T) {
	p := NewProposerPeriods()
	for i, v := range []uint64{3, 3, 3, 2} {
		p.NoteRefusal(10, string(rune('a'+i)), v)
	}
	if got, _ := p.AdvanceAfterFailure(10, 7); got != 4 {
		t.Fatalf("resume period %d, want 4 (above the vouched-for 3)", got)
	}
}

// One faulty buddy reporting an absurd period cannot drag the height to it.
func TestProposerPeriods_SingleOutlierIsIgnored(t *testing.T) {
	p := NewProposerPeriods()
	p.NoteRefusal(10, "liar", 1_000_000)
	p.NoteRefusal(10, "b", 1)
	p.NoteRefusal(10, "c", 1)
	if got, _ := p.AdvanceAfterFailure(10, 7); got != 2 {
		t.Fatalf("period %d, want 2 (third-largest report is 1)", got)
	}
}

// Fewer than f+1 reports vouch for nothing: plain +1.
func TestProposerPeriods_TooFewReportsArePlainIncrement(t *testing.T) {
	p := NewProposerPeriods()
	p.NoteRefusal(10, "a", 9)
	p.NoteRefusal(10, "b", 9)
	if got, _ := p.AdvanceAfterFailure(10, 7); got != 1 {
		t.Fatalf("period %d, want 1 (only 2 of the f+1=3 needed reports)", got)
	}
}

// Reports describe one round; after it ends they must not carry into the next.
func TestProposerPeriods_ReportsClearedAfterAdvance(t *testing.T) {
	p := NewProposerPeriods()
	for _, b := range []string{"a", "b", "c"} {
		p.NoteRefusal(10, b, 5)
	}
	p.AdvanceAfterFailure(10, 7) // -> 6
	if got, _ := p.AdvanceAfterFailure(10, 7); got != 7 {
		t.Fatalf("period %d, want 7 (old reports must be cleared)", got)
	}
}

func TestProposerPeriods_ForgetThroughDropsCommittedHeights(t *testing.T) {
	p := NewProposerPeriods()
	p.AdvanceAfterFailure(10, 7)
	p.AdvanceAfterFailure(11, 7)
	p.NoteRefusal(10, "a", 3)
	p.ForgetThrough(10)
	if got := p.PeriodFor(10); got != 0 {
		t.Fatalf("committed height must be forgotten, got period %d", got)
	}
	if got := p.PeriodFor(11); got != 1 {
		t.Fatalf("the next height must be kept, got %d", got)
	}
	// A huge height no longer prunes anything on its own.
	p.AdvanceAfterFailure(1<<62, 7)
	if got := p.PeriodFor(11); got != 1 {
		t.Fatalf("a huge height must not clear the live counter, got %d", got)
	}
}

// The proposer never goes past config.MaxPeriod, and says so when it is stuck
// there.
func TestProposerPeriods_StopsAtTheCap(t *testing.T) {
	p := NewProposerPeriods()
	var next uint64
	var capped bool
	for range config.MaxPeriod {
		next, capped = p.AdvanceAfterFailure(10, 7)
	}
	if next != config.MaxPeriod || capped {
		t.Fatalf("after MaxPeriod failures: period %d capped=%v, want %d,false", next, capped, config.MaxPeriod)
	}
	next, capped = p.AdvanceAfterFailure(10, 7)
	if next != config.MaxPeriod || !capped {
		t.Fatalf("one more failure: period %d capped=%v, want %d,true", next, capped, config.MaxPeriod)
	}
}

// Buddy reports cannot push the proposer past the cap, even overflowing ones.
func TestProposerPeriods_HintsAreCapped(t *testing.T) {
	for _, report := range []uint64{config.MaxPeriod + 50, ^uint64(0)} {
		p := NewProposerPeriods()
		for _, b := range []string{"a", "b", "c"} {
			p.NoteRefusal(10, b, report)
		}
		if next, capped := p.AdvanceAfterFailure(10, 7); next != config.MaxPeriod || !capped {
			t.Fatalf("report %d: period %d capped=%v, want %d,true", report, next, capped, config.MaxPeriod)
		}
	}
}
