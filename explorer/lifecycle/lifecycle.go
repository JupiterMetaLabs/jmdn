// Package lifecycle is the explorer's in-memory, Tron-style transaction
// lifecycle tracker. It records the stages a transaction passes through on the
// SEQUENCER — QUEUED (received) -> PENDING (in a proposed block) ->
// EXECUTING (consensus in progress, with live vote/validation counts) ->
// SUCCESS / FAILED — so the explorer API can report progress the durable chain
// store cannot yet answer.
//
// DESIGN
//   - Leaf package: imports only the standard library, so ANY package (Block,
//     Sequencer, messaging) can feed it without an import cycle and the explorer
//     HTTP layer can read it. All feature LOGIC lives here / in the explorer;
//     callers only fire a thin, best-effort notification (mirrors
//     helper.NotifyBroadcast).
//   - In-memory + TTL-bounded on purpose: this is live, transient UI state. The
//     authoritative answer for a mined tx is always the chain store, which the
//     explorer consults FIRST (chain > registry > unknown), so a restart that
//     drops in-flight entries only degrades a not-yet-committed tx back to
//     "unknown" — the safe direction, never a wrong "success".
//   - Every Mark* is guarded and non-blocking: a panic or a slow map write must
//     never touch the consensus hot path. Callers may invoke them inline.
package lifecycle

import (
	"strings"
	"sync"
	"time"
)

// Stage is the coarse Tron-style status surfaced to the explorer client.
type Stage string

const (
	StageQueued    Stage = "QUEUED"    // received by the sequencer, not yet in a block
	StagePending   Stage = "PENDING"   // included in a block proposed to /api/process-block
	StageExecuting Stage = "EXECUTING" // consensus running: votes / validation in progress
	StageSuccess   Stage = "SUCCESS"   // 2f+1 reached and applied
	StageFailed    Stage = "FAILED"    // consensus not reached or the tx/block was rejected
)

// Progress carries the live consensus counts shown during EXECUTING, e.g.
// "22 voted" (VotesReceived) and "5/7 validated" (YesVotes/CommitteeSize).
type Progress struct {
	SubStage      string `json:"sub_stage,omitempty"` // "voting" | "validating" | "certifying"
	VotesReceived int    `json:"votes_received,omitempty"`
	YesVotes      int    `json:"yes_votes,omitempty"`
	NoVotes       int    `json:"no_votes,omitempty"`
	CommitteeSize int    `json:"committee_size,omitempty"` // n
	Threshold     int    `json:"threshold,omitempty"`      // 2f+1 needed
}

// Entry is one transaction's current lifecycle snapshot.
type Entry struct {
	Hash        string    `json:"hash"`
	Stage       Stage     `json:"stage"`
	BlockNumber uint64    `json:"block_number,omitempty"`
	Progress    Progress  `json:"progress,omitempty"`
	Reason      string    `json:"reason,omitempty"` // set on FAILED
	UpdatedAt   time.Time `json:"updated_at"`
}

// registry is the process-wide tracker.
type registry struct {
	mu       sync.RWMutex
	byTx     map[string]*Entry   // txHash -> snapshot
	blockTxs map[uint64][]string // blockNumber -> its txHashes (set at MarkProposed)
	ttl      time.Duration
	capacity int
	now      func() time.Time
}

var reg = &registry{
	byTx:     make(map[string]*Entry),
	blockTxs: make(map[uint64][]string),
	ttl:      10 * time.Minute,
	capacity: 200_000,
	now:      time.Now,
}

// Configure sets TTL and capacity. ttl<=0 or capacity<=0 disables the tracker
// (every Mark* becomes a no-op and Get always misses), so the explorer falls
// back to chain-only status.
func Configure(ttl time.Duration, capacity int) {
	reg.mu.Lock()
	reg.ttl = ttl
	reg.capacity = capacity
	reg.mu.Unlock()
}

func (r *registry) enabled() bool { return r.ttl > 0 && r.capacity > 0 }

func norm(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "0x") {
		h = "0x" + h
	}
	return h
}

// upsert applies fn to the entry for hash, creating it if absent. Caller holds
// the write lock. Enforces capacity by evicting the oldest on overflow.
func (r *registry) upsert(hash string, fn func(*Entry)) {
	e, ok := r.byTx[hash]
	if !ok {
		if len(r.byTx) >= r.capacity {
			r.evictOldestLocked()
		}
		e = &Entry{Hash: hash}
		r.byTx[hash] = e
	}
	fn(e)
	e.UpdatedAt = r.now()
}

func (r *registry) evictOldestLocked() {
	var oldestKey string
	var oldestAt time.Time
	for k, e := range r.byTx {
		if oldestKey == "" || e.UpdatedAt.Before(oldestAt) {
			oldestKey, oldestAt = k, e.UpdatedAt
		}
	}
	if oldestKey != "" {
		delete(r.byTx, oldestKey)
	}
}

// guard runs fn under recover so a tracker bug can never crash a caller on the
// consensus hot path.
func guard(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// MarkQueued records that the sequencer received a transaction (pre-proposal).
func MarkQueued(hash string) {
	guard(func() {
		h := norm(hash)
		if h == "" || !reg.enabled() {
			return
		}
		reg.mu.Lock()
		defer reg.mu.Unlock()
		reg.upsert(h, func(e *Entry) {
			if e.Stage == "" { // never regress a further-along tx
				e.Stage = StageQueued
			}
		})
	})
}

// MarkProposed records that a block carrying these txs was proposed, and stores
// the block->tx mapping so later block-level updates need only the number.
func MarkProposed(blockNumber uint64, txHashes []string) {
	guard(func() {
		if !reg.enabled() || len(txHashes) == 0 {
			return
		}
		reg.mu.Lock()
		defer reg.mu.Unlock()
		norms := make([]string, 0, len(txHashes))
		for _, h := range txHashes {
			n := norm(h)
			if n == "" {
				continue
			}
			norms = append(norms, n)
			reg.upsert(n, func(e *Entry) {
				e.Stage = StagePending
				e.BlockNumber = blockNumber
			})
		}
		reg.blockTxs[blockNumber] = norms
	})
}

// MarkExecuting updates every tx of a block to EXECUTING with live counts.
func MarkExecuting(blockNumber uint64, p Progress) {
	guard(func() {
		if !reg.enabled() {
			return
		}
		reg.mu.Lock()
		defer reg.mu.Unlock()
		for _, h := range reg.blockTxs[blockNumber] {
			reg.upsert(h, func(e *Entry) {
				if e.Stage == StageSuccess || e.Stage == StageFailed {
					return // terminal: chain/commit already won
				}
				e.Stage = StageExecuting
				e.BlockNumber = blockNumber
				e.Progress = p
			})
		}
	})
}

// MarkCommitted moves every tx of a block to SUCCESS.
func MarkCommitted(blockNumber uint64) {
	guard(func() {
		if !reg.enabled() {
			return
		}
		reg.mu.Lock()
		defer reg.mu.Unlock()
		for _, h := range reg.blockTxs[blockNumber] {
			reg.upsert(h, func(e *Entry) { e.Stage = StageSuccess })
		}
		delete(reg.blockTxs, blockNumber) // terminal: release the block->tx index
	})
}

// MarkFailed moves every tx of a block to FAILED with a reason.
func MarkFailed(blockNumber uint64, reason string) {
	guard(func() {
		if !reg.enabled() {
			return
		}
		reg.mu.Lock()
		defer reg.mu.Unlock()
		for _, h := range reg.blockTxs[blockNumber] {
			reg.upsert(h, func(e *Entry) {
				e.Stage = StageFailed
				e.Reason = reason
			})
		}
	})
}

// Get returns a copy of the tx's current snapshot, expiring stale entries.
func Get(hash string) (Entry, bool) {
	h := norm(hash)
	if h == "" || !reg.enabled() {
		return Entry{}, false
	}
	reg.mu.RLock()
	e, ok := reg.byTx[h]
	var cp Entry
	var expired bool
	if ok {
		cp = *e
		expired = reg.now().Sub(e.UpdatedAt) > reg.ttl
	}
	reg.mu.RUnlock()
	if !ok || expired {
		if expired {
			reg.mu.Lock()
			delete(reg.byTx, h)
			reg.mu.Unlock()
		}
		return Entry{}, false
	}
	return cp, true
}
