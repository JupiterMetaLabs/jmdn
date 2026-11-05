package messaging

import (
	"fmt"
	"testing"
	"time"

	"gossipnode/internal/roundlock"
)

// A faulty proposer that stalls a height and then pushes a high period into
// every validator's ledgers no longer freezes the height. Every vote result
// reports the validator's highest recorded period, the next proposer resumes
// above it, and the validators accept that round. The period cap is far above
// anything the time bound lets a proposal reach.
func TestPeriodPoison_NextProposerResumesAbove(t *testing.T) {
	const (
		h         = 5000
		committee = 4
		poison    = 1024
	)
	now := time.Unix(1_700_000_000, 0)
	pace := roundlock.NewPace(func() time.Time { return now })
	ledgers := make([]*roundlock.Ledger, committee)
	for i := range ledgers {
		ledgers[i] = roundlock.NewLedger()
	}
	pace.Elapsed(h) // first proposal at the height seen at t0

	// The faulty proposer stalls the height, then proposes period 1024 once
	// the time bound allows it; every validator records it.
	now = now.Add((poison - 16) * time.Second)
	if err := pace.Check(h, poison, time.Second, 16); err != nil {
		t.Fatalf("setup: %v", err)
	}
	for _, l := range ledgers {
		if ok, _, _, _ := l.TryLockFresh(roundlock.Round{Height: h, Period: poison}, "evil/ch"); !ok {
			t.Fatal("setup: poison not recorded")
		}
	}

	// A replacement proposer starts the height at period 0 (its counter is in
	// memory). The round fails; each validator's result reports its highest
	// recorded period.
	pp := NewProposerPeriods()
	for i, l := range ledgers {
		if ok, refusal, _, _ := l.TryLockFresh(roundlock.Round{Height: h, Period: pp.PeriodFor(h)}, "honest0/ch"); ok || refusal != roundlock.Stale {
			t.Fatalf("validator %d: honest period 0 not refused as stale", i)
		}
		hp, _ := l.Highest(h)
		pp.NoteRefusal(h, fmt.Sprintf("v%d", i), hp)
	}
	next, capped := pp.AdvanceAfterFailure(h, committee)
	if capped || next != poison+1 {
		t.Fatalf("next period %d capped=%v, want %d", next, capped, poison+1)
	}

	// The retry above the poisoned period is accepted by every validator.
	now = now.Add(5 * time.Second)
	if err := pace.Check(h, next, time.Second, 16); err != nil {
		t.Fatalf("time bound refused the honest retry: %v", err)
	}
	for i, l := range ledgers {
		if ok, refusal, _, _ := l.TryLockFresh(roundlock.Round{Height: h, Period: next}, "honest1/ch"); !ok {
			t.Fatalf("validator %d refused the honest retry: %v", i, refusal)
		}
	}
}
