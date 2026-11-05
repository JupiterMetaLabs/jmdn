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
	mu            sync.Mutex
	first         map[uint64]time.Time
	committed     uint64
	haveCommitted bool
	now           func() time.Time
}

const (
	// paceAhead: once a committed tip is known, heights more than this far
	// above it are not recorded (no proposal that far ahead can be voted or
	// signed), so they cannot crowd out the live height.
	paceAhead = 64
	// paceMaxHeights bounds the map before any commit is known.
	paceMaxHeights = 1024
)

// NewPace returns an empty Pace reading the given clock.
func NewPace(now func() time.Time) *Pace {
	return &Pace{first: make(map[uint64]time.Time), now: now}
}

// DefaultPace is the process-wide Pace both ledgers' callers consult.
var DefaultPace = NewPace(time.Now)

// Elapsed returns the time since the first proposal seen at height, recording
// now as that time on the first call. A height far above the committed tip
// is not recorded and reports 0.
func (p *Pace) Elapsed(height uint64) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	first, ok := p.first[height]
	if !ok {
		if p.haveCommitted && (height <= p.committed || height-p.committed > paceAhead) {
			return 0
		}
		if !p.haveCommitted && len(p.first) >= paceMaxHeights {
			p.first = make(map[uint64]time.Time)
		}
		p.first[height] = now
		first = now
	}
	if now.Before(first) {
		return 0
	}
	return now.Sub(first)
}

// ForgetThrough records that height is committed: its record and every lower
// one are dropped, and later heights are recorded only up to paceAhead above
// it. The tip only moves up.
func (p *Pace) ForgetThrough(height uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.haveCommitted && height <= p.committed {
		return
	}
	p.committed, p.haveCommitted = height, true
	for h := range p.first {
		if h <= height || h-height > paceAhead {
			delete(p.first, h)
		}
	}
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
