// Package lifecycle is the explorer's in-memory, Tron-style transaction
// lifecycle tracker. It records the stages a transaction passes through on the
// SEQUENCER — QUEUED (received) -> PENDING (in a proposed block) ->
// EXECUTING (consensus in progress, with live vote/validation counts) ->
// SUCCESS / FAILED — so the explorer API can report progress the durable chain
// store cannot yet answer, and push transitions to SSE subscribers.
//
// DESIGN
//   - Leaf package: imports only the standard library, so ANY package (Block,
//     Sequencer, messaging, AVC) can feed it without an import cycle and the
//     explorer HTTP layer can read it and subscribe. All feature LOGIC lives
//     here / in the explorer; callers only fire a thin, best-effort
//     notification (mirrors helper.NotifyBroadcast).
//   - In-memory + TTL-bounded on purpose: this is live, transient UI state. The
//     authoritative answer for a mined tx is always the chain store, which the
//     explorer consults FIRST (chain > registry > unknown), so a restart that
//     drops in-flight entries only degrades a not-yet-committed tx back to
//     "unknown" — the safe direction, never a wrong "success".
//   - Every Mark* is guarded and non-blocking: a panic or a slow map write must
//     never touch the consensus hot path. Callers may invoke them inline. The
//     observer (SSE fan-out) is always invoked AFTER the lock is released.
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
	SubStage      string `json:"sub_stage,omitempty"` // "voting" | "validating"
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

type registry struct {
	mu       sync.RWMutex
	byTx     map[string]*Entry   // txHash -> snapshot
	blockTxs map[uint64][]string // blockNumber -> its txHashes (set at MarkProposed)
	hashToNum map[string]uint64  // blockHash -> blockNumber (for vote-arrival lookup)
	numToHash map[uint64]string  // blockNumber -> blockHash (for terminal cleanup)
	voters   map[uint64]map[string]bool // blockNumber -> set of counted voter peer IDs
	ttl      time.Duration
	capacity int
	now      func() time.Time
	obs      func(Entry) // SSE observer, invoked after unlock
}

var reg = &registry{
	byTx:      make(map[string]*Entry),
	blockTxs:  make(map[uint64][]string),
	hashToNum: make(map[string]uint64),
	numToHash: make(map[uint64]string),
	voters:    make(map[uint64]map[string]bool),
	ttl:       10 * time.Minute,
	capacity:  200_000,
	now:       time.Now,
}

// Configure sets TTL and capacity. ttl<=0 or capacity<=0 disables the tracker.
func Configure(ttl time.Duration, capacity int) {
	reg.mu.Lock()
	reg.ttl = ttl
	reg.capacity = capacity
	reg.mu.Unlock()
}

// SetObserver installs a single fan-out callback fired (guarded, after the lock
// is released) on every entry transition. The explorer sets this to push SSE.
func SetObserver(fn func(Entry)) {
	reg.mu.Lock()
	reg.obs = fn
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

// upsert applies fn to hash's entry (creating it if absent), stamps the time,
// and returns a COPY for post-unlock emission. Caller holds the write lock.
func (r *registry) upsert(hash string, fn func(*Entry)) Entry {
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
	return *e
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

func guard(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// emit fans out updated snapshots to the observer, after the lock is released.
func emit(updated []Entry) {
	reg.mu.RLock()
	obs := reg.obs
	reg.mu.RUnlock()
	if obs == nil {
		return
	}
	for _, e := range updated {
		func(x Entry) { defer func() { _ = recover() }(); obs(x) }(e)
	}
}

// MarkQueued records that the sequencer received a transaction (pre-proposal).
func MarkQueued(hash string) {
	guard(func() {
		h := norm(hash)
		if h == "" || !reg.enabled() {
			return
		}
		var updated []Entry
		func() {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			cp := reg.upsert(h, func(e *Entry) {
				if e.Stage == "" { // never regress a further-along tx
					e.Stage = StageQueued
				}
			})
			updated = append(updated, cp)
		}()
		emit(updated)
	})
}

// MarkProposed records a proposed block's txs and its hash->number mapping.
func MarkProposed(blockNumber uint64, blockHash string, txHashes []string) {
	guard(func() {
		if !reg.enabled() || len(txHashes) == 0 {
			return
		}
		var updated []Entry
		func() {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			norms := make([]string, 0, len(txHashes))
			for _, h := range txHashes {
				n := norm(h)
				if n == "" {
					continue
				}
				norms = append(norms, n)
				cp := reg.upsert(n, func(e *Entry) {
					e.Stage = StagePending
					e.BlockNumber = blockNumber
				})
				updated = append(updated, cp)
			}
			reg.blockTxs[blockNumber] = norms
			if bh := norm(blockHash); bh != "" {
				reg.hashToNum[bh] = blockNumber
				reg.numToHash[blockNumber] = bh
			}
			reg.voters[blockNumber] = make(map[string]bool)
		}()
		emit(updated)
	})
}

// IncVoteArrival counts ONE distinct voter for the block identified by blockHash
// and moves its txs to EXECUTING(voting) with a live, climbing tally. A repeated
// vote from the same peer, or a vote for a block this node did not propose, is a
// no-op. This is the real-time counter behind "22 nodes voted".
func IncVoteArrival(blockHash, peerID string, agree bool) {
	guard(func() {
		if !reg.enabled() {
			return
		}
		var updated []Entry
		func() {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			num, ok := reg.hashToNum[norm(blockHash)]
			if !ok {
				return // not a block we recorded proposing — ignore
			}
			vs := reg.voters[num]
			if vs == nil {
				vs = make(map[string]bool)
				reg.voters[num] = vs
			}
			pid := strings.TrimSpace(peerID)
			if pid == "" || vs[pid] {
				return // dedupe: one count per distinct voter
			}
			vs[pid] = true
			for _, h := range reg.blockTxs[num] {
				cp := reg.upsert(h, func(e *Entry) {
					if e.Stage == StageSuccess || e.Stage == StageFailed {
						return // terminal
					}
					e.Stage = StageExecuting
					e.BlockNumber = num
					e.Progress.SubStage = "voting"
					e.Progress.VotesReceived++
					if agree {
						e.Progress.YesVotes++
					} else {
						e.Progress.NoVotes++
					}
				})
				updated = append(updated, cp)
			}
		}()
		emit(updated)
	})
}

// MarkExecuting sets an authoritative snapshot of consensus progress for a block
// (e.g. the certificate tally: yes/committee/threshold). Reconciles counts the
// live per-vote path accumulated.
func MarkExecuting(blockNumber uint64, p Progress) {
	guard(func() {
		if !reg.enabled() {
			return
		}
		var updated []Entry
		func() {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			for _, h := range reg.blockTxs[blockNumber] {
				cp := reg.upsert(h, func(e *Entry) {
					if e.Stage == StageSuccess || e.Stage == StageFailed {
						return
					}
					e.Stage = StageExecuting
					e.BlockNumber = blockNumber
					// Merge: keep the larger live vote tally, adopt cert fields.
					if p.VotesReceived > e.Progress.VotesReceived {
						e.Progress.VotesReceived = p.VotesReceived
					}
					if p.YesVotes > e.Progress.YesVotes {
						e.Progress.YesVotes = p.YesVotes
					}
					if p.NoVotes > e.Progress.NoVotes {
						e.Progress.NoVotes = p.NoVotes
					}
					if p.CommitteeSize > 0 {
						e.Progress.CommitteeSize = p.CommitteeSize
					}
					if p.Threshold > 0 {
						e.Progress.Threshold = p.Threshold
					}
					if p.SubStage != "" {
						e.Progress.SubStage = p.SubStage
					}
				})
				updated = append(updated, cp)
			}
		}()
		emit(updated)
	})
}

// MarkCommitted moves every tx of a block to SUCCESS and releases block indexes.
func MarkCommitted(blockNumber uint64) {
	guard(func() {
		if !reg.enabled() {
			return
		}
		var updated []Entry
		func() {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			for _, h := range reg.blockTxs[blockNumber] {
				cp := reg.upsert(h, func(e *Entry) { e.Stage = StageSuccess })
				updated = append(updated, cp)
			}
			reg.releaseBlockLocked(blockNumber)
		}()
		emit(updated)
	})
}

// MarkFailed moves every tx of a block to FAILED and releases block indexes.
func MarkFailed(blockNumber uint64, reason string) {
	guard(func() {
		if !reg.enabled() {
			return
		}
		var updated []Entry
		func() {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			for _, h := range reg.blockTxs[blockNumber] {
				cp := reg.upsert(h, func(e *Entry) {
					e.Stage = StageFailed
					e.Reason = reason
				})
				updated = append(updated, cp)
			}
			reg.releaseBlockLocked(blockNumber)
		}()
		emit(updated)
	})
}

// releaseBlockLocked drops the per-block indexes at a terminal stage so they do
// not grow unbounded. The per-tx entries remain (TTL-expired by Get).
func (r *registry) releaseBlockLocked(blockNumber uint64) {
	delete(r.blockTxs, blockNumber)
	delete(r.voters, blockNumber)
	if bh, ok := r.numToHash[blockNumber]; ok {
		delete(r.hashToNum, bh)
		delete(r.numToHash, blockNumber)
	}
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
