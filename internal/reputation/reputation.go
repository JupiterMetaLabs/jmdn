// Package reputation implements the OBSERVE-ONLY validator reputation model.
//
// Design (operator-approved 2026-07-25):
//
//   - EFFECT: reputation is a future-SELECTION signal only. It never feeds the
//     live 2f+1 quorum tally — the in-round vote count stays equal-weight, so
//     no single round's safety can depend on these scores.
//
//   - DISSENT IS NEVER PUNISHED: a -1 vote against a block that finalized
//     carries ZERO delta. Penalizing dissent creates rubber-stamp incentives
//     (rational validators always vote with the majority → independent
//     validation collapses) and lets a faulty majority grind down honest
//     minorities. Conversely a -1 on a block that FAILED to finalize is
//     rewarded — the dissenter was right.
//
//   - ONLY OBJECTIVE FAULTS ARE PENALIZED: equivocation (provable signed
//     fork), an invalid signature on a vote response, and absence (selected
//     for the committee but returned no vote).
//
//   - RECOVERY: scores decay toward the neutral Start each epoch, so
//     transient faults heal and no honest node is permanently depressed. The
//     Floor is > 0: reputation alone can never fully evict a peer.
//
//   - ROLLOUT: observe-only. This package classifies rounds, updates an
//     in-memory store, and logs one line per round. It does NOT alter buddy
//     selection, consensus, or seed state. Enforcement (feeding the selection
//     score and pushing sequencer-signed reputation to the seed — NOT the
//     self-signed UpdatePeerWeights path, which a peer could abuse) is a
//     separate, later step once observed data validates the tuning.
//
// The store is in-memory and resets on restart — acceptable for the
// observe-only phase; durable, authority-signed persistence belongs to the
// enforcement phase.
package reputation

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event classifies what a committee member did (or provably failed to do) in
// one concluded consensus round.
type Event string

const (
	// AgreeFinalized: voted +1 and the block finalized. Rewarded (availability
	// + alignment with the certified outcome).
	AgreeFinalized Event = "agree_finalized"
	// RejectNotFinalized: voted -1 and the block did NOT finalize. Rewarded —
	// the dissenter was correct.
	RejectNotFinalized Event = "reject_not_finalized"
	// MinorityDissent: voted against the round's outcome (-1 on a finalized
	// block, or +1 on a failed one). ZERO delta by design — dissent is never
	// penalized (see package doc).
	MinorityDissent Event = "minority_dissent"
	// Absent: selected into the voting committee but no vote response was
	// collected (timeout / no reply). Objective liveness fault.
	Absent Event = "absent"
	// BadSignature: a vote response that failed BLS verification. Objective
	// protocol fault. (Hook: call Default.Observe from the verification site;
	// not classified by ClassifyRound, which only sees collected votes.)
	BadSignature Event = "bad_signature"
	// Equivocation: provable signed fork (two different blocks at one height,
	// or two conflicting signed votes). Severest objective fault. (Hook: call
	// Default.Observe from the equivocation detector.)
	Equivocation Event = "equivocation"
)

const (
	// Start is the neutral score for a peer never observed before, and the
	// value decay pulls toward.
	Start = 0.50
	// Floor is the minimum score. Strictly > 0: reputation alone never fully
	// evicts an honest-but-unlucky peer.
	Floor = 0.10
	// Cap is the maximum score.
	Cap = 1.00
	// DecayPerEpoch is the geometric pull toward Start per epoch: after one
	// epoch a score keeps 90% of its distance from Start.
	DecayPerEpoch = 0.10
	// EpochSeconds matches the committee epoch clock (seed
	// COMMITTEE_EPOCH_SECONDS / consensus.committee_epoch_seconds default).
	EpochSeconds = 3600
)

// Delta returns the bounded score change for an event. Values are deliberately
// asymmetric: objective faults cost 5-25x more than a single round's reward,
// while dissent costs nothing.
func Delta(e Event) float64 {
	switch e {
	case AgreeFinalized, RejectNotFinalized:
		return +0.02
	case MinorityDissent:
		return 0
	case Absent:
		return -0.10
	case BadSignature:
		return -0.30
	case Equivocation:
		return -0.50
	default:
		return 0
	}
}

// clamp bounds a score to [Floor, Cap].
func clamp(s float64) float64 {
	if s < Floor {
		return Floor
	}
	if s > Cap {
		return Cap
	}
	return s
}

// DecayScore pulls score geometrically toward Start over the given number of
// epochs (fractional epochs allowed). Zero or negative epochs is a no-op.
func DecayScore(score float64, epochs float64) float64 {
	if epochs <= 0 {
		return score
	}
	keep := math.Pow(1-DecayPerEpoch, epochs)
	return Start + (score-Start)*keep
}

// ClassifyRound maps every committee member to the event observed in one
// concluded round.
//
//	committee — the selected voting set (the MAIN peers), the only peers that
//	            can be classified; votes from peers outside it are ignored
//	            (they are already rejected upstream by keyAuthorized).
//	votes     — collected responses: peerID → agree (+1 = true, -1 = false).
//	finalized — whether the round reached quorum and the block was accepted.
//
// Truth table per member:
//
//	no response            → Absent
//	agree  &&  finalized   → AgreeFinalized      (+)
//	!agree && !finalized   → RejectNotFinalized  (+)  correct dissent
//	!agree &&  finalized   → MinorityDissent     (0)  never penalized
//	agree  && !finalized   → MinorityDissent     (0)
func ClassifyRound(committee []string, votes map[string]bool, finalized bool) map[string]Event {
	out := make(map[string]Event, len(committee))
	for _, id := range committee {
		agree, voted := votes[id]
		switch {
		case !voted:
			out[id] = Absent
		case agree == finalized:
			if finalized {
				out[id] = AgreeFinalized
			} else {
				out[id] = RejectNotFinalized
			}
		default:
			out[id] = MinorityDissent
		}
	}
	return out
}

// entry is one peer's stored score plus the time it was last touched (for
// lazy decay).
type entry struct {
	score float64
	last  time.Time
}

// Store is a thread-safe, in-memory reputation store with lazy epoch decay:
// on each observation the elapsed decay since the last touch is applied first,
// then the event delta, then the clamp.
type Store struct {
	mu     sync.Mutex
	scores map[string]entry
	now    func() time.Time
}

// NewStore returns a Store using the wall clock.
func NewStore() *Store { return NewStoreWithClock(time.Now) }

// NewStoreWithClock returns a Store with an injectable clock (tests).
func NewStoreWithClock(clock func() time.Time) *Store {
	return &Store{scores: make(map[string]entry), now: clock}
}

// Observe applies one event for a peer and returns the (old, updated) scores.
// A peer never seen before starts at Start.
func (s *Store) Observe(peerID string, e Event) (old, cur float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	ent, ok := s.scores[peerID]
	if !ok {
		ent = entry{score: Start, last: now}
	}
	old = ent.score
	decayed := DecayScore(ent.score, now.Sub(ent.last).Seconds()/EpochSeconds)
	cur = clamp(decayed + Delta(e))
	s.scores[peerID] = entry{score: cur, last: now}
	return old, cur
}

// Score returns the peer's current score (decayed to now) without recording
// an event. Unknown peers return Start.
func (s *Store) Score(peerID string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.scores[peerID]
	if !ok {
		return Start
	}
	return DecayScore(cur.score, s.now().Sub(cur.last).Seconds()/EpochSeconds)
}

// Snapshot returns a copy of all current scores (decayed to now).
func (s *Store) Snapshot() map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make(map[string]float64, len(s.scores))
	for id, e := range s.scores {
		out[id] = DecayScore(e.score, now.Sub(e.last).Seconds()/EpochSeconds)
	}
	return out
}

// Enabled gates the observe-only recorder. Default ON (it is pure logging —
// no consensus or selection effect). Set JMDN_REPUTATION_OBSERVE=0 to silence.
var Enabled = envOn("JMDN_REPUTATION_OBSERVE", true)

// Default is the process-wide store used by ObserveRound and by fault-report
// call sites (equivocation / bad-signature hooks).
var Default = NewStore()

// ObserveRound classifies one concluded consensus round into per-peer events,
// records them in Default, and logs a single compact line. Observe-only: no
// selection or consensus effect. Returns the classification (nil when
// disabled) so callers/tests can inspect it.
func ObserveRound(blockNumber uint64, blockHash string, committee []string, votes map[string]bool, finalized bool) map[string]Event {
	if !Enabled {
		return nil
	}
	events := ClassifyRound(committee, votes, finalized)

	ids := make([]string, 0, len(events))
	for id := range events {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		old, cur := Default.Observe(id, events[id])
		parts = append(parts, fmt.Sprintf("%s:%s:%.3f→%.3f", shortID(id), events[id], old, cur))
	}
	fmt.Printf("📊 reputation(observe-only): block=%d hash=%s finalized=%v voted=%d/%d | %s\n",
		blockNumber, shortHash(blockHash), finalized, len(votes), len(committee), strings.Join(parts, " "))
	return events
}

// shortID trims a libp2p peer ID to its last 6 chars for compact logs.
func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[len(id)-6:]
}

// shortHash trims a block hash for compact logs.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// envOn mirrors the messaging package's flag convention: unset → def;
// "0"/"false"/"no"/"off" → false; anything else → true.
func envOn(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
