package Sequencer

import (
	"context"
	"fmt"
	"sync"

	"gossipnode/Sequencer/Alerts"
)

// A height that keeps failing rounds moves up one period per round. At the
// period cap (config.MaxPeriodAt) it cannot commit until an operator clears
// the round ledgers, and at the legacy cap of 1024 with ~90 s rounds that is
// about a day away. These alerts give on-call warning while there is still
// time to fix the cause: once at 75% of the cap and once at 90%, per height.
// The cap itself is reported by advancePeriodAfterFailedRound.

// periodCapNearLevels are the warning points, as fractions of the cap, from
// lowest to highest.
var periodCapNearLevels = []struct {
	num, den uint64
	severity string
	label    string
}{
	{3, 4, Alerts.SeverityWarning, "75%"},
	{9, 10, Alerts.SeverityError, "90%"},
}

// periodCapThreshold is the first period at or above level i of limit.
func periodCapThreshold(i int, limit uint64) uint64 {
	l := periodCapNearLevels[i]
	// floor(limit*num/den), split so limit*num cannot overflow.
	return limit/l.den*l.num + limit%l.den*l.num/l.den
}

// periodCapWatch remembers the highest level already alerted for the height
// the sequencer is working on. Heights move forward one at a time, so a new
// height simply resets it.
type periodCapWatch struct {
	mu     sync.Mutex
	height uint64
	level  int // levels already alerted at height (0 = none)
}

// crossed returns the index of the highest level period has reached at height
// when that level has not been alerted yet, and records it. A period that
// jumps past several levels at once (a refusal hint can lift it) yields only
// the highest.
func (w *periodCapWatch) crossed(height, period, limit uint64) (int, bool) {
	reached := 0
	for i := range periodCapNearLevels {
		if period >= periodCapThreshold(i, limit) {
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

var defaultPeriodCapWatch periodCapWatch

// sendPeriodCapNearAlert is replaced in tests.
var sendPeriodCapNearAlert = func(ctx context.Context, height, period, limit uint64, level int, reason string) {
	l := periodCapNearLevels[level]
	Alerts.NewAlertBuilder(ctx).
		AlertName("Consensus: Period Cap Near").
		Status(l.severity).
		Severity(l.severity).
		Description(fmt.Sprintf("Height %d is at period %d of %d (%s of the cap); it stops committing at the cap until an operator clears the round ledgers", height, period, limit, l.label)).
		Label("block_number", fmt.Sprintf("%d", height)).
		Label("period", fmt.Sprintf("%d", period)).
		Label("period_cap", fmt.Sprintf("%d", limit)).
		Label("periods_left", fmt.Sprintf("%d", limit-period)).
		Label("reason", reason).
		Send()
}

// notePeriodNearCap alerts when period at height has reached a warning level
// it has not alerted for yet. period must be below limit; the cap itself has
// its own alert.
func notePeriodNearCap(ctx context.Context, height, period, limit uint64, reason string) {
	if level, ok := defaultPeriodCapWatch.crossed(height, period, limit); ok {
		sendPeriodCapNearAlert(ctx, height, period, limit, level, reason)
	}
}
