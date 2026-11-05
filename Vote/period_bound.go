package Vote

import (
	"fmt"

	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/internal/roundlock"
)

// votePace is the clock the vote path checks a proposal's period against; a
// variable so tests can replace it. Shared with the result-signing path
// (AVC/BuddyNodes/MessagePassing/round_lock.go) through roundlock.DefaultPace.
var votePace = roundlock.DefaultPace

// checkVotePeriod reports why this node must not vote ACCEPT for a proposal at
// (height, period), or nil, before anything is recorded in roundlock.Votes:
//
//   - the period may not exceed config.MaxPeriod, the cap every other path
//     already enforces;
//   - the period may not run ahead of time (roundlock.Pace,
//     consensus.vote_period_min_round_seconds / vote_period_slack).
//
// Without these, one proposal at a high period would leave every honest
// retry at the height refused, across restarts and a change of sequencer.
func checkVotePeriod(height, period uint64) error {
	if period > config.MaxPeriod {
		return fmt.Errorf("vote period: height %d period %d exceeds the cap %d", height, period, config.MaxPeriod)
	}
	minRound, slack := settings.VotePeriodPace()
	if err := votePace.Check(height, period, minRound, slack); err != nil {
		return fmt.Errorf("vote period: %w", err)
	}
	return nil
}
