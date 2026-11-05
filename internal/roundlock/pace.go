package roundlock

import (
	"fmt"
	"sync"
	"time"
)

// Pace bounds how far a proposal's Period may run ahead of time at a height.
//
// Both ledgers refuse every period below the highest one recorded at a
// height, durably, so one proposal at a high period would leave every honest
// retry at that height refused. A failed round takes at least minRound, so a
// period above slack + elapsed/minRound cannot be an honest retry. Elapsed is
// measured from the first proposal this node saw at the height, so an idle
// gap before the height started does not widen the bound. The vote path and
// the result-signing path share one Pace, so they agree on when a height
// started.
type Pace struct {
	mu    sync.Mutex
	first map[uint64]time.Time
	top   uint64
	now   func() time.Time
}

// paceKeep bounds the first-seen map: heights more than this far below the
// newest one seen are dropped.
const paceKeep = 64

// NewPace returns an empty Pace reading the given clock.
func NewPace(now func() time.Time) *Pace {
	return &Pace{first: make(map[uint64]time.Time), now: now}
}

// DefaultPace is the process-wide Pace both ledgers' callers consult.
var DefaultPace = NewPace(time.Now)

// Elapsed returns the time since the first proposal seen at height, recording
// now as that time on the first call.
func (p *Pace) Elapsed(height uint64) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	first, ok := p.first[height]
	if !ok {
		p.first[height] = now
		first = now
		if height > p.top {
			p.top = height
		}
		if p.top > paceKeep {
			for h := range p.first {
				if h < p.top-paceKeep {
					delete(p.first, h)
				}
			}
		}
	}
	if now.Before(first) {
		return 0
	}
	return now.Sub(first)
}

// Check reports an error when period at height runs ahead of time:
// period > slack + Elapsed(height)/minRound. minRound <= 0 turns the check
// off (Elapsed is still recorded).
func (p *Pace) Check(height, period uint64, minRound time.Duration, slack uint64) error {
	elapsed := p.Elapsed(height)
	if minRound <= 0 {
		return nil
	}
	allowed := slack + uint64(elapsed/minRound)
	if period > allowed {
		return fmt.Errorf("height %d period %d is ahead of time (at most %d after %s at this height)",
			height, period, allowed, elapsed.Truncate(time.Second))
	}
	return nil
}
