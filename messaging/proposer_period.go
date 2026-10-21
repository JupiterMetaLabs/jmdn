package messaging

import (
	"sort"
	"sync"

	"gossipnode/config"
	"gossipnode/internal/roundlock"
)

// ProposerPeriods is the PROPOSER's per-height retry counter (R3): the Period
// the next block proposed at a height is stamped with.
//
// Only the node that builds blocks reads it (Block/consensus_fields.go via
// LiveSlotFor). Verifiers never do: a block carries its own Period, bound into
// its ConsensusHash and so into every committee vote, and RoundContextForBlock
// reads it from there. A node that was offline or restarted therefore needs
// nothing from this store to verify a block.
//
// The counter moves only up, and only when a round at the height ends without
// the block being committed (Sequencer.BroadcastAndProcessBlock). Each failed
// round burns its period, so every retry is a new round: buddies may sign it
// (roundlock R1), and under committee v2 with a pool larger than the
// committee it seats a different committee.
//
// In-memory. After a restart a height starts again at 0; buddies that already
// signed a higher period refuse with their highest signed period, and
// NoteRefusal/AdvanceAfterFailure use those reports to resume above it.
type ProposerPeriods struct {
	mu     sync.Mutex
	period map[uint64]uint64
	hints  map[uint64]map[string]uint64
}

// NewProposerPeriods returns an empty counter.
func NewProposerPeriods() *ProposerPeriods {
	return &ProposerPeriods{period: make(map[uint64]uint64), hints: make(map[uint64]map[string]uint64)}
}

// DefaultProposerPeriods is the process-wide counter the block builder uses.
var DefaultProposerPeriods = NewProposerPeriods()

// PeriodFor returns the period the next proposal at height is stamped with.
func (p *ProposerPeriods) PeriodFor(height uint64) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.period[height]
}

// NoteRefusal records that buddy refused a proposal at height and reported
// highest as the highest period it has signed there. One report per buddy per
// height; a later report from the same buddy replaces its earlier one. The
// reporter is the authenticated libp2p peer of the response stream, so one
// buddy cannot report on another's behalf.
func (p *ProposerPeriods) NoteRefusal(height uint64, buddy string, highest uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hints[height] == nil {
		p.hints[height] = make(map[string]uint64)
	}
	p.hints[height][buddy] = highest
}

// AdvanceAfterFailure moves height past the round that just failed and
// returns the new period.
//
// The new period is the current one plus one, raised above the buddies'
// refusal reports when they show rounds this process does not remember (it
// restarted mid-height). Only the (f+1)-th largest report counts, with f =
// (committeeSize-1)/3, so at least one honest buddy signed that high: a single
// faulty buddy reporting a huge period cannot push the height to it.
// Reports for the height are cleared, since they describe the round that has
// now ended.
//
// The period never exceeds config.MaxPeriod, which every node enforces. capped
// is true when the height is at the cap: its next proposal reuses MaxPeriod,
// buddies that signed that round refuse it, and the height cannot commit
// without operator attention.
func (p *ProposerPeriods) AdvanceAfterFailure(height uint64, committeeSize int) (next uint64, capped bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Every comparison is against MaxPeriod before adding, so neither the
	// counter nor a reported hint (up to 2^64-1) can overflow.
	floor := p.period[height]
	if hint, ok := hintFloor(p.hints[height], committeeSize); ok && hint > floor {
		floor = hint
	}
	if floor >= config.MaxPeriod {
		next, capped = config.MaxPeriod, true
	} else {
		next = floor + 1
	}
	p.period[height] = next
	delete(p.hints, height)
	return next, capped
}

// hintFloor returns the (f+1)-th largest reported period, f = (n-1)/3. ok is
// false when fewer than f+1 buddies reported, i.e. nothing is vouched for by
// at least one honest buddy.
func hintFloor(reports map[string]uint64, committeeSize int) (uint64, bool) {
	f := 0
	if committeeSize > 0 {
		f = (committeeSize - 1) / 3
	}
	if len(reports) < f+1 {
		return 0, false
	}
	vals := make([]uint64, 0, len(reports))
	for _, v := range reports {
		vals = append(vals, v)
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] > vals[j] })
	return vals[f], true
}

// ForgetThrough drops the counter and hints of every height at or below
// height, which is committed: no proposal at it can be made again.
func (p *ProposerPeriods) ForgetThrough(height uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for h := range p.period {
		if h <= height {
			delete(p.period, h)
		}
	}
	for h := range p.hints {
		if h <= height {
			delete(p.hints, h)
		}
	}
}

// NoteHeightCommitted tells the per-round ledgers that height is committed
// locally: the buddy signing ledger (roundlock) stops signing at or below it
// and drops old records, and the proposer's period counter drops it. Call it
// from every local commit point - the live receive path, the proposer's own
// commit, ThebeSync, and the startup tip recovery - so pruning follows commits
// rather than the heights of incoming requests.
func NoteHeightCommitted(height uint64) {
	roundlock.Default.ForgetThrough(height)
	roundlock.Votes.ForgetThrough(height)
	DefaultProposerPeriods.ForgetThrough(height)
}
