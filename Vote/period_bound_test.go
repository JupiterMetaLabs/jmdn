package Vote

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
	"gossipnode/internal/roundlock"
)

// fakeClock replaces the process clock for one test.
func fakeClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	prev := votePace
	votePace = roundlock.NewPace(func() time.Time { return now })
	t.Cleanup(func() { votePace = prev })
	return &now
}

func freshVotes(t *testing.T) {
	t.Helper()
	prev := roundlock.Votes
	roundlock.Votes = roundlock.NewLedger()
	t.Cleanup(func() { roundlock.Votes = prev })
}

func periodBlock(height, period uint64, hash string) *config.ZKBlock {
	return &config.ZKBlock{BlockNumber: height, Period: period, BlockHash: common.HexToHash(hash)}
}

// A proposal above the period cap is refused and leaves nothing in the
// ledger, so the honest first round at the height is still voted ACCEPT.
func TestReserveAcceptVote_PeriodAboveCapLeavesNoRecord(t *testing.T) {
	freshVotes(t)
	fakeClock(t, time.Unix(1_700_000_000, 0))
	for _, p := range []uint64{config.MaxPeriod + 1, math.MaxUint64} {
		if err := reserveAcceptVote(periodBlock(40, p, "0xbad")); err == nil || !strings.Contains(err.Error(), "exceeds the cap") {
			t.Fatalf("period %d: err = %v", p, err)
		}
	}
	if _, ok := roundlock.Votes.Highest(40); ok {
		t.Fatal("a refused period was recorded")
	}
	if err := reserveAcceptVote(periodBlock(40, 0, "0xgood")); err != nil {
		t.Fatalf("honest period 0 refused: %v", err)
	}
}

// A first proposal at the cap is refused: it is ahead of time. Honest retries
// that arrive as time passes are accepted.
func TestReserveAcceptVote_PeriodMayNotRunAheadOfTime(t *testing.T) {
	freshVotes(t)
	now := fakeClock(t, time.Unix(1_700_000_000, 0))

	if err := reserveAcceptVote(periodBlock(41, config.MaxPeriod, "0xbad")); err == nil || !strings.Contains(err.Error(), "ahead of time") {
		t.Fatalf("period at the cap as the first proposal: err = %v", err)
	}
	if err := reserveAcceptVote(periodBlock(41, 0, "0xp0")); err != nil {
		t.Fatalf("period 0: %v", err)
	}
	if err := reserveAcceptVote(periodBlock(41, 16, "0xp16")); err != nil {
		t.Fatalf("period at the slack: %v", err)
	}
	if err := reserveAcceptVote(periodBlock(41, 16+10, "0xp26")); err == nil {
		t.Fatal("period 10 past the slack accepted at once")
	}
	*now = now.Add(10 * time.Second)
	if err := reserveAcceptVote(periodBlock(41, 16+10, "0xp26")); err != nil {
		t.Fatalf("period 10 past the slack after 10 s: %v", err)
	}
}

// The clock starts at the first proposal seen at the height, not at the
// parent: an idle gap before the height does not widen the bound.
func TestVotePeriodClock_StartsAtFirstProposal(t *testing.T) {
	now := fakeClock(t, time.Unix(1_700_000_000, 0))
	if err := checkVotePeriod(50, 0); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	if err := checkVotePeriod(51, 1024); err == nil {
		t.Fatal("first proposal at a new height jumped to period 1024")
	}
	if err := checkVotePeriod(50, 1024); err != nil {
		t.Fatalf("height seen an hour ago: %v", err)
	}
}
