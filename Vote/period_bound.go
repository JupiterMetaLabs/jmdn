package Vote

import (
	"fmt"
	"sync"
	"time"

	"gossipnode/config"
	"gossipnode/config/settings"
)

// Bounds on a proposal's Period, checked before this node records an ACCEPT
// vote in roundlock.Votes.
//
// The vote ledger refuses any period below the highest one it has accepted
// at a height, and the record is durable. Without a bound, one proposal at a
// huge period would leave every honest retry at that height refused, across
// restarts and across a change of sequencer. Two rules close that:
//
//   - the period may not exceed config.MaxPeriod, the cap every other path
//     already enforces;
//   - the period may not run ahead of time: a failed round takes at least
//     consensus.vote_period_min_round_seconds, so a period above
//     consensus.vote_period_slack + elapsed / min_round cannot be an honest
//     retry. Elapsed is measured from the first proposal this node saw at the
//     height, so a long idle gap before the height started does not widen it.

const (
	defaultVotePeriodMinRound = time.Second
	defaultVotePeriodSlack    = 16
	// periodClockKeep bounds the first-seen map: heights more than this far
	// below the newest one seen are dropped.
	periodClockKeep = 64
)

type periodClock struct {
	mu    sync.Mutex
	first map[uint64]time.Time
	top   uint64
	now   func() time.Time
}

func newPeriodClock(now func() time.Time) *periodClock {
	return &periodClock{first: make(map[uint64]time.Time), now: now}
}

// votePeriods is the process-wide clock; a variable so tests can replace it.
var votePeriods = newPeriodClock(time.Now)

// elapsed returns the time since the first proposal seen at height, recording
// now as that time on the first call.
func (c *periodClock) elapsed(height uint64) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	first, ok := c.first[height]
	if !ok {
		c.first[height] = now
		first = now
		if height > c.top {
			c.top = height
		}
		if c.top > periodClockKeep {
			for h := range c.first {
				if h < c.top-periodClockKeep {
					delete(c.first, h)
				}
			}
		}
	}
	if now.Before(first) {
		return 0
	}
	return now.Sub(first)
}

func votePeriodLimits() (minRound time.Duration, slack uint64) {
	if !settings.IsLoaded() {
		return defaultVotePeriodMinRound, defaultVotePeriodSlack
	}
	c := settings.Get().Consensus
	return time.Duration(c.VotePeriodMinRoundSeconds) * time.Second, c.VotePeriodSlack
}

// checkVotePeriod reports why this node must not vote ACCEPT for a proposal at
// (height, period), or nil.
func checkVotePeriod(height, period uint64) error {
	if period > config.MaxPeriod {
		return fmt.Errorf("vote period: height %d period %d exceeds the cap %d", height, period, config.MaxPeriod)
	}
	elapsed := votePeriods.elapsed(height)
	minRound, slack := votePeriodLimits()
	if minRound <= 0 {
		return nil
	}
	allowed := slack + uint64(elapsed/minRound)
	if period > allowed {
		return fmt.Errorf("vote period: height %d period %d is ahead of time (at most %d after %s at this height)",
			height, period, allowed, elapsed.Truncate(time.Second))
	}
	return nil
}
