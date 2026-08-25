package messaging

// M0 - period counter + timeout certificates (Architecture §7.1c, build order
// M0 in §9.1). Makes `period` chain-derived: a quorum-certified round timeout
// advances it, replacing the literal 0 RoundContextForBlock hardcoded before
// this landed.
//
// Scope: chain-derived only, NOT tamper-evident. That second property depends
// on M2b folding `period` into the block hash (specced, not yet wired) - a
// relay can still rewrite a committed block's `period` and silently change
// which committee it claims until M2b lands. This is safe to land behind the
// existing feature flag, but the flag cannot flip to on until M2b does.
//
// Out of scope for this file, deliberately: M3 (slot-based seed derivation),
// M4 (entropy committee), M5 (secrets), the CRDT vote-store re-key, and any
// wiring/flag-flip work. M0 is a prerequisite for M3/M4 in the build graph,
// not bundled with them.

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	bft "gossipnode/AVC/BFT/bft"
	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/internal/reputation"
)

// TimeoutVoteDomain is the domain-separation prefix for a TimeoutVote's
// signed bytes. It must differ from BLS_Signer.BlockBoundVotePrefix
// ("zkvote:") so a signature over one can never be replayed as the other -
// §7.1b's new rule (a validator must not cast both for the same
// (height, period)) depends on the two being cryptographically
// distinguishable, not just logically distinct.
const TimeoutVoteDomain = "jmdt/timeout-vote/v1"

// CanonicalTimeoutVoteMessage builds the exact bytes signed and verified for a
// TimeoutVote. Signer and verifier both derive from here so they cannot drift
// - the same pattern BLS_Signer.CanonicalVoteMessageV3 already uses for block
// votes.
func CanonicalTimeoutVoteMessage(chainID, height, period uint64) []byte {
	return fmt.Appendf(nil, "%s:chain=%d:h=%d:p=%d", TimeoutVoteDomain, chainID, height, period)
}

// TimeoutVote is one validator's certified claim that round (height, period)
// timed out without reaching consensus - not that it was rejected. See
// AVC-Architecture-End-to-End.md §7.1c §0 for why that distinction is the
// entire reason this type exists (a genuine timeout was previously
// indistinguishable from Reject at the per-buddy engine level; the sequencer-
// level finalDecision=="" / consensusReached==false signal is the clean one
// to trigger this against - jmdn/AVC/BFT/bft/sequencer_client.go:275-277).
type TimeoutVote struct {
	Height  uint64
	Period  uint64
	VoterID string
	Sig     []byte
}

// TimeoutCertificate proves that period P at height H timed out: a pool-wide
// T_vote quorum of TimeoutVotes exists for (height, period). PrevIndex is the
// period this certificate supersedes (always Period-1) - an index reference,
// not a hash chain (§7.1c point 2: two valid certificates at the same index
// can legitimately carry different signer subsets and therefore different
// hashes, so chaining on hash identity would make honest nodes reject each
// other's certificates).
type TimeoutCertificate struct {
	Height    uint64
	Period    uint64
	PrevIndex uint64
	AggSig    []byte
	// SignerBitmap holds voter IDs, sorted, rather than a bit-packed bitmap
	// against a fixed pool ordering - same information, no separate pool-
	// index registry required for this pass.
	SignerBitmap []string
}

// SignTimeoutVote builds and signs a TimeoutVote for (height, period) with the
// caller's BLS private key.
func SignTimeoutVote(priv []byte, voterID string, chainID, height, period uint64) (TimeoutVote, error) {
	sig, err := blssign.BLSSign(priv, CanonicalTimeoutVoteMessage(chainID, height, period))
	if err != nil {
		return TimeoutVote{}, fmt.Errorf("sign timeout vote: %w", err)
	}
	return TimeoutVote{Height: height, Period: period, VoterID: voterID, Sig: sig}, nil
}

// TallyTimeoutVotes verifies each vote's own signature, dedups by VoterID,
// excludes any voter already flagged by DetectTimeoutBlockVoteEquivocation,
// and builds a TimeoutCertificate once a pool-wide T_vote quorum is reached -
// ceil(2*poolSize/3) via bft.HasQuorum, the SAME threshold function the block-
// vote path uses, never the buddy committee's smaller T_agg (recovery must
// not depend on the entity that failed to reach consensus in the first
// place). Returns (nil, false, nil) - not an error - when quorum simply
// hasn't been reached yet.
func TallyTimeoutVotes(votes []TimeoutVote, height, period uint64, poolSize int, pubKeys map[string][]byte, excluded map[string]bool) (*TimeoutCertificate, bool, error) {
	if poolSize <= 0 {
		return nil, false, errors.New("timeout tally: pool size must be > 0")
	}
	if period == 0 {
		return nil, false, errors.New("timeout tally: period 0 has no predecessor to certify a timeout for")
	}
	msg := CanonicalTimeoutVoteMessage(BLS_Signer.DomainChainID(), height, period)

	seen := make(map[string]bool, len(votes))
	var validVoters []string
	var validSigs [][]byte
	for _, v := range votes {
		if v.Height != height || v.Period != period {
			continue // not for this round
		}
		if seen[v.VoterID] || excluded[v.VoterID] {
			continue // dedup; an equivocating voter never counts
		}
		pub, ok := pubKeys[v.VoterID]
		if !ok {
			continue // unknown voter - excluded, not fatal to the tally
		}
		if err := blssign.BLSVerify(pub, msg, v.Sig); err != nil {
			continue // bad signature - excluded, not fatal to the tally
		}
		seen[v.VoterID] = true
		validVoters = append(validVoters, v.VoterID)
		validSigs = append(validSigs, v.Sig)
	}

	if !bft.HasQuorum(len(validVoters), poolSize) {
		return nil, false, nil
	}

	aggSig, err := blssign.BLSAggregate(validSigs...)
	if err != nil {
		return nil, false, fmt.Errorf("timeout tally: aggregate: %w", err)
	}
	sort.Strings(validVoters) // canonical order: two honest nodes tallying the same set land on the same bitmap

	return &TimeoutCertificate{
		Height:       height,
		Period:       period,
		PrevIndex:    period - 1,
		AggSig:       aggSig,
		SignerBitmap: validVoters,
	}, true, nil
}

// VerifyTimeoutCertificate checks a certificate's own internal self-
// consistency (Period == PrevIndex+1) and its aggregate signature against the
// pool's known pubkeys. It does NOT require having seen any previous
// certificate - that is what lets a syncing node accept the latest
// certificate for a height without replaying every intermediate timeout
// (§7.1c point 1: "a single certificate proves its entire prefix").
func VerifyTimeoutCertificate(cert TimeoutCertificate, poolSize int, pubKeys map[string][]byte) (bool, error) {
	if cert.Period == 0 {
		return false, errors.New("timeout cert: period 0 cannot be a timeout result")
	}
	if cert.Period != cert.PrevIndex+1 {
		return false, fmt.Errorf("timeout cert: period %d does not follow PrevIndex %d", cert.Period, cert.PrevIndex)
	}
	if !bft.HasQuorum(len(cert.SignerBitmap), poolSize) {
		return false, fmt.Errorf("timeout cert: %d signers do not reach quorum of %d", len(cert.SignerBitmap), poolSize)
	}

	pubs := make([][]byte, 0, len(cert.SignerBitmap))
	seen := make(map[string]bool, len(cert.SignerBitmap))
	for _, voterID := range cert.SignerBitmap {
		if seen[voterID] {
			return false, fmt.Errorf("timeout cert: duplicate signer %s in bitmap", voterID)
		}
		seen[voterID] = true
		pub, ok := pubKeys[voterID]
		if !ok {
			return false, fmt.Errorf("timeout cert: unknown signer %s", voterID)
		}
		pubs = append(pubs, pub)
	}

	msg := CanonicalTimeoutVoteMessage(BLS_Signer.DomainChainID(), cert.Height, cert.Period)
	ok, err := blssign.BLSFastAggregateVerify(pubs, msg, cert.AggSig)
	if err != nil {
		return false, fmt.Errorf("timeout cert: aggregate verify: %w", err)
	}
	return ok, nil
}

// PeriodStore tracks the currently-known period per height. A height with no
// entry is implicitly at period 0 - "reset to 0 on the next height" (§7.1c)
// falls out of this for free: a brand-new height was never written, so
// PeriodFor returns Go's zero value until that height's own first certificate
// lands.
type PeriodStore struct {
	mu sync.RWMutex

	// periods MUST have exactly one writer in this entire codebase:
	// AcceptTimeoutCertificate below, and only through its verify-then-
	// strictly-monotonic path. This is a hard invariant, not a convention —
	// Period feeds committee selection (RoundContextForBlock ->
	// SelectCommitteeWithSize's seed input), so if any two nodes ever
	// disagreed on a height's period, they would compute two different
	// committees for the same round. There is no such thing as a "local"
	// or "fast-path" period bump: not for a stuck timer, not for the
	// sequencer, not for an operator override. If you are tempted to add a
	// second write site to this map — for ANY reason — stop: the fix
	// belongs in how/when a TimeoutCertificate gets built and verified, not
	// in a new way to write here. Confirmed by grep as of 2026-08-24: this
	// field is written at exactly one line in the whole repo (this file's
	// AcceptTimeoutCertificate) — verify-m4.sh checks this mechanically so
	// a future second writer can't land silently.
	periods map[uint64]uint64
}

// NewPeriodStore returns an empty store - every height starts at period 0.
func NewPeriodStore() *PeriodStore {
	return &PeriodStore{periods: make(map[uint64]uint64)}
}

// PeriodFor returns the current period for height, or 0 if none is known yet.
func (s *PeriodStore) PeriodFor(height uint64) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.periods[height]
}

// AcceptTimeoutCertificate is the ONLY valid path by which Period may ever
// change (see the struct-level invariant comment above). It verifies cert
// and, if valid and strictly newer than what is already known for its
// height, advances the stored period.
//
// This is a state-machine rule, not a timeout timer: a node holds at its
// current period for as long as it takes — seconds or hours — until a
// certificate meeting this exact bar arrives, from anywhere (gossip, a
// rejoin-RPC fetch, a locally-assembled quorum). There is no deadline after
// which a node may advance on weaker evidence, and no caller — including
// whichever node happened to assemble/broadcast the certificate — gets any
// special authority here: the same verify-then-monotonic check applies
// identically no matter who calls this.
//
//   - (newPeriod, true, nil): accepted, period advanced.
//   - (currentPeriod, false, nil): a stale or already-known certificate -
//     not an error, just a no-op (e.g. a replayed or late-arriving cert for
//     a period this store has already moved past).
//   - (currentPeriod, false, err): the certificate itself failed
//     verification.
func (s *PeriodStore) AcceptTimeoutCertificate(cert TimeoutCertificate, poolSize int, pubKeys map[string][]byte) (uint64, bool, error) {
	ok, err := VerifyTimeoutCertificate(cert, poolSize, pubKeys)
	if err != nil {
		return s.PeriodFor(cert.Height), false, err
	}
	if !ok {
		return s.PeriodFor(cert.Height), false, errors.New("timeout cert: signature verification failed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.periods[cert.Height]
	if cert.Period <= current {
		return current, false, nil
	}
	s.periods[cert.Height] = cert.Period
	return cert.Period, true, nil
}

// DefaultPeriodStore is the process-wide store RoundContextForBlock reads
// from - a package-level default keeps RoundContextForBlock's existing
// signature (no store parameter to thread through every caller), mirroring
// this same package's existing SetEquivocationStore pattern
// (consensus_hardening.go).
var DefaultPeriodStore = NewPeriodStore()

// DetectTimeoutBlockVoteEquivocation returns every peer present in both sets
// - a validator that cast a block-vote AND a TimeoutVote for the same
// (height, period) violates §7.1b's new rule. Callers exclude these peers
// from both tallies (TallyTimeoutVotes' excluded parameter, and the
// equivalent on the block-vote side) and report them via
// RecordTimeoutBlockVoteEquivocation.
func DetectTimeoutBlockVoteEquivocation(blockVoters, timeoutVoters map[string]bool) []string {
	var bad []string
	for peer := range blockVoters {
		if timeoutVoters[peer] {
			bad = append(bad, peer)
		}
	}
	sort.Strings(bad)
	return bad
}

// RecordTimeoutBlockVoteEquivocation reports each detected peer through the
// existing reputation.Equivocation channel (§6.1's classification, the same
// path already used for a provable signed block fork) - not a new mechanism,
// per §7.1c item 5's explicit instruction.
func RecordTimeoutBlockVoteEquivocation(peers []string) {
	for _, peer := range peers {
		reputation.Default.Observe(peer, reputation.Equivocation)
	}
}
