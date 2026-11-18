package Sequencer

import (
	"context"
	"testing"

	"gossipnode/config"
	"gossipnode/messaging"
)

func TestPeriodCapThresholds(t *testing.T) {
	cases := []struct {
		limit      uint64
		at75, at90 uint64
	}{
		{1024, 768, 921},
		{config.MaxPeriod, 786432, 943718},
	}
	for _, c := range cases {
		if got := periodCapThreshold(0, c.limit); got != c.at75 {
			t.Errorf("cap %d: 75%% threshold = %d, want %d", c.limit, got, c.at75)
		}
		if got := periodCapThreshold(1, c.limit); got != c.at90 {
			t.Errorf("cap %d: 90%% threshold = %d, want %d", c.limit, got, c.at90)
		}
	}
}

// Each level alerts once per height; a new height starts again; a jump past
// both levels alerts only the higher one. A small cap keeps the numbers short;
// the rule does not depend on the cap.
func TestPeriodCapWatchAlertsOncePerLevel(t *testing.T) {
	const limit = 1024
	var w periodCapWatch
	type step struct {
		height, period uint64
		wantLevel      int
		wantAlert      bool
	}
	steps := []step{
		{10, 1, 0, false},
		{10, 767, 0, false},
		{10, 768, 0, true}, // 75%
		{10, 769, 0, false},
		{10, 920, 0, false},
		{10, 921, 1, true}, // 90%
		{10, 1000, 0, false},
		{11, 5, 0, false},   // next height resets
		{11, 950, 1, true},  // jumped past both: only 90%
		{11, 960, 0, false}, // 75% is not sent afterwards
	}
	for i, s := range steps {
		level, ok := w.crossed(s.height, s.period, limit)
		if ok != s.wantAlert || (ok && level != s.wantLevel) {
			t.Fatalf("step %d (h=%d p=%d): got level=%d alert=%v, want level=%d alert=%v",
				i, s.height, s.period, level, ok, s.wantLevel, s.wantAlert)
		}
	}
}

// The alert uses the real cap: a period of 900, which was close to the old
// cap of 1024, is far from MaxPeriod and does not alert.
func TestPeriodCapNearUsesTheCap(t *testing.T) {
	defaultPeriodCapWatch = periodCapWatch{}
	t.Cleanup(func() { defaultPeriodCapWatch = periodCapWatch{} })

	var sent []uint64
	orig := sendPeriodCapNearAlert
	sendPeriodCapNearAlert = func(_ context.Context, _, period, _ uint64, _ int, _ string) {
		sent = append(sent, period)
	}
	t.Cleanup(func() { sendPeriodCapNearAlert = orig })

	ctx := context.Background()
	at75 := periodCapThreshold(0, config.MaxPeriod)
	notePeriodNearCap(ctx, 99, 900, config.MaxPeriodAt(99), "test")
	notePeriodNearCap(ctx, 99, at75, config.MaxPeriodAt(99), "test")
	if len(sent) != 1 || sent[0] != at75 {
		t.Fatalf("alerts sent at periods %v, want [%d]", sent, at75)
	}
}

// A failed round that lifts the height to 75% of the cap alerts through the
// real failed-round path; the next failed round does not repeat it.
func TestFailedRoundAlertsNearCap(t *testing.T) {
	withFreshProposerPeriods(t)
	defaultPeriodCapWatch = periodCapWatch{}
	t.Cleanup(func() { defaultPeriodCapWatch = periodCapWatch{} })
	var levels []int
	orig := sendPeriodCapNearAlert
	sendPeriodCapNearAlert = func(_ context.Context, _, _, _ uint64, level int, _ string) {
		levels = append(levels, level)
	}
	t.Cleanup(func() { sendPeriodCapNearAlert = orig })

	const h = 50
	at75 := periodCapThreshold(0, config.MaxPeriodAt(h))
	for _, id := range []string{"12D3KooWA", "12D3KooWB", "12D3KooWC"} {
		messaging.DefaultProposerPeriods.NoteRefusal(h, id, at75-1)
	}
	c := consensusAtHeight(h)
	c.advancePeriodAfterFailedRound(context.Background(), h, "test") // -> at75
	c.advancePeriodAfterFailedRound(context.Background(), h, "test") // -> at75+1
	if got := messaging.DefaultProposerPeriods.PeriodFor(h); got != at75+1 {
		t.Fatalf("period %d, want %d", got, at75+1)
	}
	if len(levels) != 1 || levels[0] != 0 {
		t.Fatalf("alerts %v, want one 75%% alert", levels)
	}
}
