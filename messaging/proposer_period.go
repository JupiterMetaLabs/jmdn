package messaging

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"gossipnode/config"
	"gossipnode/internal/roundlock"
)

// ProposerPeriods is the PROPOSER's per-height retry counter (R3): the Period
// the next block proposed at a height is stamped with.
//
// Only the node that builds blocks reads it (Block/consensus_fields.go). It
// does not affect the slot: every attempt at a height sits in slot = height.
// Verifiers never read it: a block carries its own Period, bound into
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
// Durable when a Store is set (SetStore, wired by main next to the roundlock
// ledgers). ClaimPeriod writes the period it hands out BEFORE the proposal is
// stamped, so after a restart the height resumes at last-proposed+1: the
// outcome of that round is unknown, so it is treated as burned. Without a
// Store (tests, or a node that never proposes) the counter is in-memory and a
// restart starts the height again at 0; buddies that already signed a higher
// period refuse with their highest signed period, and
// NoteRefusal/AdvanceAfterFailure use those reports to resume above it. That
// hint path stays as the fallback (e.g. a new sequencer with an empty store).
type ProposerPeriods struct {
	mu     sync.Mutex
	period map[uint64]uint64
	hints  map[uint64]map[string]uint64

	store  roundlock.Store
	prefix string
	loaded map[uint64]bool
	// onDisk marks heights known to have a non-empty stored record: written by
	// ClaimPeriod, or read back by loadLocked. ForgetThrough clears only these,
	// so a commit never writes a key for a height that was never stored (the
	// same rule as roundlock.Ledger's onDisk). An empty put is not a delete in
	// the KV: it leaves a zero-length entry, one per block on every node.
	onDisk map[uint64]bool
}

// NewProposerPeriods returns an empty counter.
func NewProposerPeriods() *ProposerPeriods {
	return &ProposerPeriods{
		period: make(map[uint64]uint64),
		hints:  make(map[uint64]map[string]uint64),
		loaded: make(map[uint64]bool),
		onDisk: make(map[uint64]bool),
	}
}

// proposedRecord is the stored form of one height: the last period a proposal
// at it was stamped with.
type proposedRecord struct {
	LastProposed uint64 `json:"last_proposed"`
}

// SetStore makes the counter durable under the given key prefix. Call once at
// startup, before any block is built.
func (p *ProposerPeriods) SetStore(store roundlock.Store, prefix string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store, p.prefix = store, prefix
	p.loaded = make(map[uint64]bool)
	p.onDisk = make(map[uint64]bool)
}

func (p *ProposerPeriods) key(height uint64) string {
	return p.prefix + strconv.FormatUint(height, 10)
}

// loadLocked reads height's stored record the first time the height is
// touched and raises the in-memory counter to last-proposed+1 (capped). It only
// raises, so it can never move a height back. Caller holds p.mu.
func (p *ProposerPeriods) loadLocked(height uint64) error {
	if p.store == nil || p.loaded[height] {
		return nil
	}
	raw, err := p.store.Get(p.key(height))
	if err != nil {
		return fmt.Errorf("proposer period: read height %d: %w", height, err)
	}
	if len(raw) > 0 {
		var rec proposedRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("proposer period: decode height %d: %w", height, err)
		}
		next := rec.LastProposed
		if limit := config.MaxPeriodAt(height); next >= limit {
			next = limit
		} else {
			next++
		}
		if next > p.period[height] {
			p.period[height] = next
		}
		p.onDisk[height] = true
	}
	p.loaded[height] = true
	return nil
}

// ClaimPeriod returns the period the next proposal at height is stamped with
// and durably records it before returning. The block builder calls this, not
// PeriodFor. Fail-closed: if the record cannot be read or written the caller
// must not propose, since a restart could then reuse a period buddies signed.
func (p *ProposerPeriods) ClaimPeriod(height uint64) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.loadLocked(height); err != nil {
		return 0, err
	}
	period := p.period[height]
	if p.store != nil {
		raw, err := json.Marshal(proposedRecord{LastProposed: period})
		if err != nil {
			return 0, fmt.Errorf("proposer period: encode height %d: %w", height, err)
		}
		if err := p.store.Put(p.key(height), raw); err != nil {
			return 0, fmt.Errorf("proposer period: write height %d: %w", height, err)
		}
		p.onDisk[height] = true
	}
	return period, nil
}

// DefaultProposerPeriods is the process-wide counter the block builder uses.
var DefaultProposerPeriods = NewProposerPeriods()

// PeriodFor returns the period the next proposal at height is stamped with.
// Read-only; a store read error leaves the in-memory value (ClaimPeriod is the
// fail-closed path).
func (p *ProposerPeriods) PeriodFor(height uint64) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.loadLocked(height)
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
// The period never exceeds config.MaxPeriodAt, which every node enforces. capped
// is true when the height is at the cap: its next proposal reuses MaxPeriod,
// buddies that signed that round refuse it, and the height cannot commit
// without operator attention.
func (p *ProposerPeriods) AdvanceAfterFailure(height uint64, committeeSize int) (next uint64, capped bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Best effort: ClaimPeriod already loaded this height before the round.
	_ = p.loadLocked(height)
	// Every comparison is against MaxPeriod before adding, so neither the
	// counter nor a reported hint (up to 2^64-1) can overflow.
	floor := p.period[height]
	if hint, ok := hintFloor(p.hints[height], committeeSize); ok && hint > floor {
		floor = hint
	}
	if limit := config.MaxPeriodAt(height); floor >= limit {
		next, capped = limit, true
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
//
// Stored records are cleared only for heights at or below it that this
// process knows have one (onDisk). Every node calls this on every commit
// (NoteHeightCommitted), including nodes that never propose and every block
// applied by catch-up, so clearing unconditionally would write a key per block
// on every node; an empty put leaves a zero-length entry, it does not delete.
// Best effort: a record left behind (failed write, or stored before a restart
// and never touched again) is for a committed height, which is never proposed
// at again, so it is only unused space.
func (p *ProposerPeriods) ForgetThrough(height uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for h := range p.onDisk {
		if h <= height {
			if p.store == nil || p.store.Put(p.key(h), []byte{}) == nil {
				delete(p.onDisk, h)
			}
		}
	}
	for h := range p.loaded {
		if h <= height {
			delete(p.loaded, h)
		}
	}
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

// NoteHeightCommitted records that height is committed locally - the one
// place every local commit point reports to: the live receive path, the
// proposer's own commit, ThebeSync, and startup/catch-up tip recovery. It
//   - raises the slot store to height (slot == block number),
//   - makes the buddy signing ledger (roundlock) stop signing at or below
//     height and drop old records,
//   - drops the proposer's period counter for height.
//
// Every effect only moves forward, so calls may repeat or arrive out of
// order (a sync and a live commit racing) without leaving anything behind.
func NoteHeightCommitted(height uint64) {
	DefaultSlotStore.AdvanceOnCommit(height)
	roundlock.Default.ForgetThrough(height)
	roundlock.Votes.ForgetThrough(height)
	roundlock.DefaultPace.ForgetThrough(height)
	DefaultProposerPeriods.ForgetThrough(height)
}
