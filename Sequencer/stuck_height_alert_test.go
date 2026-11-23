package Sequencer

import (
	"context"
	"testing"

	"gossipnode/messaging"
)

// Each level alerts once per height; a new height starts again; a jump past
// both levels alerts only the higher one.
func TestStuckHeightWatchAlertsOncePerLevel(t *testing.T) {
	var w stuckHeightWatch
	type step struct {
		height, period uint64
		wantLevel      int
		wantAlert      bool
	}
	steps := []step{
		{10, 1, 0, false},
		{10, 19, 0, false},
		{10, 20, 0, true}, // warning
		{10, 21, 0, false},
		{10, 99, 0, false},
		{10, 100, 1, true}, // error
		{10, 500, 0, false},
		{11, 5, 0, false},   // next height resets
		{11, 150, 1, true},  // jumped past both: only the error
		{11, 160, 0, false}, // the warning is not sent afterwards
	}
	for i, s := range steps {
		level, ok := w.crossed(s.height, s.period)
		if ok != s.wantAlert || (ok && level != s.wantLevel) {
			t.Fatalf("step %d (h=%d p=%d): got level=%d alert=%v, want level=%d alert=%v",
				i, s.height, s.period, level, ok, s.wantLevel, s.wantAlert)
		}
	}
}

// Failed rounds through the real path: the round that reaches period 20
// alerts, the next one does not.
func TestFailedRoundAlertsStuckHeight(t *testing.T) {
	withFreshProposerPeriods(t)
	defaultStuckHeightWatch = stuckHeightWatch{}
	t.Cleanup(func() { defaultStuckHeightWatch = stuckHeightWatch{} })
	type sent struct {
		period uint64
		level  int
	}
	var alerts []sent
	orig := sendStuckHeightAlert
	sendStuckHeightAlert = func(_ context.Context, _, period uint64, level int, _ string) {
		alerts = append(alerts, sent{period, level})
	}
	t.Cleanup(func() { sendStuckHeightAlert = orig })

	const h = 50
	c := consensusAtHeight(h)
	for range 21 { // periods 1 .. 21
		c.advancePeriodAfterFailedRound(context.Background(), h, "test")
	}
	if got := messaging.DefaultProposerPeriods.PeriodFor(h); got != 21 {
		t.Fatalf("period %d, want 21", got)
	}
	if len(alerts) != 1 || alerts[0] != (sent{20, 0}) {
		t.Fatalf("alerts %+v, want one warning at period 20", alerts)
	}
}
