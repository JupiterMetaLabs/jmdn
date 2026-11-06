package roundlock

import (
	"encoding/binary"
	"fmt"
	"strconv"
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
//
// With a Store the first-seen time of each height is durable, like the
// ledgers it protects: a node restarted during a long stall keeps the time
// the height started, so it does not refuse the round that resumes above a
// period it recorded before the restart.
type Pace struct {
	mu            sync.Mutex
	first         map[uint64]time.Time
	committed     uint64
	haveCommitted bool
	now           func() time.Time

	store  Store
	prefix string
	loaded map[uint64]bool
	onDisk map[uint64]bool
}

const (
	// paceAhead: once a committed tip is known, heights more than this far
	// above it are not recorded (no proposal that far ahead can be voted or
	// signed), so they cannot crowd out the live height.
	paceAhead = 64
	// paceMaxHeights bounds the map before any commit is known.
	paceMaxHeights = 1024
)

// NewPace returns an empty in-memory Pace reading the given clock.
func NewPace(now func() time.Time) *Pace {
	return &Pace{
		first:  make(map[uint64]time.Time),
		now:    now,
		loaded: make(map[uint64]bool),
		onDisk: make(map[uint64]bool),
	}
}

// DefaultPace is the process-wide Pace both ledgers' callers consult.
var DefaultPace = NewPace(time.Now)

// SetStore makes first-seen times durable under the given key prefix. Call
// once at startup, before any vote or signature.
func (p *Pace) SetStore(store Store, prefix string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store, p.prefix = store, prefix
}

func (p *Pace) key(height uint64) string { return p.prefix + strconv.FormatUint(height, 10) }

// loadLocked reads height's stored first-seen time the first time the height
// is touched this process. A read error leaves it unset, so the height starts
// now: stricter, never looser. Caller holds p.mu.
func (p *Pace) loadLocked(height uint64) {
	if p.store == nil || p.loaded[height] {
		return
	}
	p.loaded[height] = true
	raw, err := p.store.Get(p.key(height))
	if err != nil || len(raw) != 8 {
		return
	}
	p.first[height] = time.Unix(0, int64(binary.BigEndian.Uint64(raw)))
	p.onDisk[height] = true
}

// persistLocked writes height's first-seen time. A write failure keeps it in
// memory only: the bound then restarts at a restart, as without a store.
// Caller holds p.mu.
func (p *Pace) persistLocked(height uint64, t time.Time) {
	if p.store == nil {
		return
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(t.UnixNano()))
	if p.store.Put(p.key(height), buf[:]) == nil {
		p.onDisk[height] = true
	}
}

// Elapsed returns the time since the first proposal seen at height, recording
// now as that time on the first call. A height at or below the committed tip,
// or far above it, is not recorded and reports 0.
func (p *Pace) Elapsed(height uint64) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.haveCommitted && (height <= p.committed || height-p.committed > paceAhead) {
		return 0
	}
	p.loadLocked(height)
	first, ok := p.first[height]
	if !ok {
		if !p.haveCommitted && len(p.first) >= paceMaxHeights {
			p.first = make(map[uint64]time.Time)
		}
		p.first[height] = now
		first = now
		p.persistLocked(height, now)
	}
	if now.Before(first) {
		return 0
	}
	return now.Sub(first)
}

// ForgetThrough records that height is committed: its record and every lower
// one are dropped, in memory and in the store, and later heights are recorded
// only up to paceAhead above it. The tip only moves up.
func (p *Pace) ForgetThrough(height uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.haveCommitted && height <= p.committed {
		return
	}
	p.committed, p.haveCommitted = height, true
	drop := func(h uint64) bool { return h <= height || h-height > paceAhead }
	for h := range p.first {
		if drop(h) {
			delete(p.first, h)
		}
	}
	for h := range p.loaded {
		if drop(h) {
			delete(p.loaded, h)
		}
	}
	for h := range p.onDisk {
		if drop(h) && (p.store == nil || p.store.Put(p.key(h), []byte{}) == nil) {
			delete(p.onDisk, h)
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
