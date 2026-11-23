package Sequencer

import (
	"context"
	"fmt"
	"sync"

	"gossipnode/Sequencer/Alerts"
)

// A height that keeps failing rounds moves up one period per failed round, so
// its period is roughly how many rounds it has failed. "Block Rejected By
// Consensus" fires on every failed round; these alerts say the height is
// stuck: once when it reaches period 20 (about 30 minutes of ~90 s rounds)
// and once at period 100 (about 2.5 hours), per height. The usual cause is
// three or more committee members down or unreachable. The period cap itself
// is reported by advancePeriodAfterFailedRound.

// stuckHeightLevels are the alert points, lowest first.
var stuckHeightLevels = []struct {
	period   uint64
	severity string
}{
	{20, Alerts.SeverityWarning},
	{100, Alerts.SeverityError},
}

// stuckHeightWatch remembers the highest level already alerted for the
// height the sequencer is working on. Heights move forward one at a time, so a
// new height simply resets it.
type stuckHeightWatch struct {
	mu     sync.Mutex
	height uint64
	level  int // levels already alerted at height (0 = none)
}

// crossed returns the index of the highest level period has reached at height
// when that level has not been alerted yet, and records it. A period that
// jumps past several levels at once (a refusal hint can lift it) yields only
// the highest.
func (w *stuckHeightWatch) crossed(height, period uint64) (int, bool) {
	reached := 0
	for i, l := range stuckHeightLevels {
		if period >= l.period {
			reached = i + 1
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if height != w.height {
		w.height, w.level = height, 0
	}
	if reached <= w.level {
		return 0, false
	}
	w.level = reached
	return reached - 1, true
}

var defaultStuckHeightWatch stuckHeightWatch

// sendStuckHeightAlert is replaced in tests.
var sendStuckHeightAlert = func(ctx context.Context, height, period uint64, level int, reason string) {
	l := stuckHeightLevels[level]
	Alerts.NewAlertBuilder(ctx).
		AlertName("Consensus: Height Stuck").
		Status(l.severity).
		Severity(l.severity).
		Description(fmt.Sprintf("Height %d has not committed after %d periods; rounds keep failing (often three or more committee members are down)", height, period)).
		Label("block_number", fmt.Sprintf("%d", height)).
		Label("period", fmt.Sprintf("%d", period)).
		Label("threshold", fmt.Sprintf("%d", l.period)).
		Label("reason", reason).
		Send()
}

// noteStuckHeight alerts when period at height has reached a level it has not
// alerted for yet.
func noteStuckHeight(ctx context.Context, height, period uint64, reason string) {
	if level, ok := defaultStuckHeightWatch.crossed(height, period); ok {
		sendStuckHeightAlert(ctx, height, period, level, reason)
	}
}
