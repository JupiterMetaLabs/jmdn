package messaging

// The reveal INBOX — the piece that was missing between "a reveal can be
// produced" and "a reveal reaches the fold". New 2026-08-20.
//
// # What this closes
//
// Before this file, the M4 pipeline had a hole in the middle that no amount of
// downstream correctness could fix: `ProduceRevealForEpoch` could compute this
// node's reveal, and `foldBlockDeclaredReveals` could fold reveals a block
// declared, but **nothing assigned block.RandaoReveals**, so every block
// shipped empty and the fold never ran on real data. Every epoch therefore had
// 0 of m reveals and, under Rule 1, fell back — permanently.
//
// This file is the buffer between those two halves:
//
//	own reveal (ProduceRevealForEpoch) ─┐
//	                                    ├─> inbox ─> RevealsForBlock ─> block
//	pushed reveals (§4.4 RevealPush) ───┘              (proposer only)
//
// # Verification happens on the way IN, not only on the way out
//
// Every reveal is checked against its epoch's verifier (Ed25519 before the
// entropy cut-over, BLS from it) before it is stored.
// That is deliberate belt-and-braces: `Accumulator.Fold` verifies again when
// the block commits (Rule 2 — the block declares the set, and every node
// re-verifies independently), so this check is not load-bearing for
// correctness. It is load-bearing for not letting an attacker fill a node's
// memory with junk that only gets rejected later, and for not proposing blocks
// carrying reveals this node already knows are invalid.
//
// # What this file deliberately does NOT check: membership
//
// It does not ask whether the peer is on this epoch's entropy committee.
// That question needs `SelectEntropyCommittee`, which needs a live beacon, and
// making the inbox depend on that would mean a node with no beacon yet cannot
// even buffer a reveal it will need later. `Accumulator.Fold` is the authority
// on membership (`ErrNotExpected`), and it runs on every node at commit — so a
// reveal from a non-member is stored here harmlessly and rejected there.
// Storing it costs one map entry; refusing to buffer it could lose a valid
// reveal during startup.
//
// # Redundant inclusion across the window is intentional
//
// `RevealsForBlock` returns every reveal it holds for the epoch on EVERY block
// inside the reveal window `[0, K)`, not just the first. Three blocks × m
// reveals is negligible, and the redundancy is what makes delivery robust: if
// the block carrying a reveal is rejected and retried, the reveal is still
// included in the retry. `Accumulator.Fold` dedups by proposer
// (`ErrDuplicateReveal`), so a repeat is a no-op, never a double-fold —
// which matters because XOR is self-inverse and folding twice would CANCEL a
// member's contribution.
import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/rs/zerolog/log"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
)

// revealInbox holds verified reveals awaiting inclusion in a block, keyed by
// epoch then by the revealing peer.
type revealInbox struct {
	mu      sync.Mutex
	byEpoch map[uint64]map[string][]byte
}

var defaultRevealInbox = &revealInbox{byEpoch: make(map[uint64]map[string][]byte)}

// AddInboundReveal verifies and files one reveal.
//
// Called from two places: this node's own reveal (ensureSelfReveal below) and
// §4.4's RevealPush receive path (entropy_reveal_push.go).
//
// Returns an error when the reveal does not verify, so the caller can decide
// whether that is a protocol violation worth logging loudly (a pushed reveal)
// or an internal bug (our own). First reveal for a peer wins: a second,
// different value for the same (peer, epoch) is rejected rather than
// overwriting, because there is meant to be exactly one valid reveal per
// (peer, epoch) - guaranteed for BLS reveals, and the reason Ed25519 ones are
// replaced at the cut-over — two different ones means one is
// forged, and silently taking the later one would let an attacker displace a
// good reveal with a bad one.
//
// Bounds (the inbox is fed by peers, and any peer can sign a valid reveal for
// its own ID): once this node has a slot clock, reveals for epochs more than
// maxRevealEpochsBehind behind or maxRevealEpochsAhead ahead of the current
// epoch are refused; at most maxRevealInboxEpochs epochs are buffered; and
// once an epoch holds maxRevealsPerEpoch reveals, further peers are admitted
// only if they are seated on that epoch's entropy committee, so a flood of
// self-signed non-member reveals cannot crowd out real members.
func AddInboundReveal(epoch uint64, peerID string, reveal []byte) error {
	if peerID == "" {
		return ErrEmptyRevealPeer
	}
	if err := checkRevealEpochWindow(epoch); err != nil {
		return err
	}
	verifier, err := revealVerifierFor(epoch)
	if err != nil {
		return fmt.Errorf("%w: no verifier for epoch %d: %v", ErrRevealDidNotVerify, epoch, err)
	}
	chainID := BLS_Signer.DomainChainID()
	if !verifier.VerifyReveal(peerID, chainID, epoch, reveal) {
		return ErrRevealDidNotVerify
	}

	defaultRevealInbox.mu.Lock()
	defer defaultRevealInbox.mu.Unlock()

	byPeer, ok := defaultRevealInbox.byEpoch[epoch]
	if !ok {
		if len(defaultRevealInbox.byEpoch) >= maxRevealInboxEpochs {
			return ErrRevealInboxFull
		}
		byPeer = make(map[string][]byte)
		defaultRevealInbox.byEpoch[epoch] = byPeer
	}
	if existing, dup := byPeer[peerID]; dup {
		if string(existing) == string(reveal) {
			return nil // identical re-delivery: harmless, and expected under §4.4's per-slot retry
		}
		return ErrConflictingReveal
	}
	if len(byPeer) >= maxRevealsPerEpoch && !revealPeerSeated(epoch, peerID) {
		return ErrRevealInboxFull
	}
	cp := make([]byte, len(reveal))
	copy(cp, reveal)
	byPeer[peerID] = cp
	return nil
}

// ensureSelfReveal produces this node's own reveal for epoch and files it, if
// this node is seated on the entropy committee.
//
// Silent no-op in the ordinary cases: no identity installed, not seated, or no
// beacon yet. Those are not errors — most nodes are not on the committee in
// most epochs, and a node without Stage F configured simply has nothing to
// contribute. Only a genuine failure to sign while seated is logged.
func ensureSelfReveal(epoch uint64) {
	peerID, err := nodeIdentity()
	if err != nil {
		return // no identity installed — nothing to contribute
	}

	defaultRevealInbox.mu.Lock()
	_, already := defaultRevealInbox.byEpoch[epoch][peerID]
	defaultRevealInbox.mu.Unlock()
	if already {
		return // deterministic, so re-deriving would produce the identical bytes
	}

	sig, err := ProduceRevealForEpoch(epoch)
	if err != nil {
		// Not seated / no beacon / no anchored pool are the common, expected
		// cases. A key mismatch is not: this node is seated but its BLS key is
		// not the one its snapshot binds, so it will be counted as withheld
		// and force the epoch to fallback every time it is seated.
		if errors.Is(err, ErrRevealBLSKeyMismatch) {
			log.Error().Err(err).Uint64("epoch", epoch).Str("peer", peerID).
				Msg("entropy: seated on the entropy committee but this node's BLS key is not the one its committee snapshot binds — no reveal produced, the epoch will fall back; re-register the key or restore the right key file")
		}
		return
	}
	if err := AddInboundReveal(epoch, peerID, sig); err != nil {
		log.Error().Err(err).Uint64("epoch", epoch).Str("peer", peerID).
			Msg("entropy: this node's OWN reveal was refused by the inbox (failed verification under its epoch's scheme - its libp2p key before the cut-over, its snapshot-bound BLS key from it - or the inbox is full); this node will be counted as withheld and the epoch will fall back")
	}
}

// RevealsForBlock returns the reveals to declare in a block at slot.
//
// Empty outside the reveal window `[E·N, E·N+K)` — Architecture §7.2: reveals
// are accepted only in the first K slots of an epoch, and a reveal declared
// after the cutoff is one the fold has already stopped waiting for. Returning
// them anyway would put values in blocks that every node's `Finalise()` has
// already moved past.
//
// The result is sorted by peer ID so two nodes assembling the same block from
// the same inbox produce a byte-identical `RandaoReveals` list. That matters
// once M2b hash coverage is on: the reveal list is hashed in array order
// (`Security.EncodeReveals`), so an unstable order would mean two honest nodes
// computing different block hashes from identical data.
func RevealsForBlock(slot uint64) []config.Reveal {
	epoch := EpochForSlot(slot)
	if slot < epoch*N+0 || slot >= cutoffSlotFor(epoch) {
		return nil // outside [E·N, E·N+K)
	}

	ensureSelfReveal(epoch)

	defaultRevealInbox.mu.Lock()
	byPeer := defaultRevealInbox.byEpoch[epoch]
	out := make([]config.Reveal, 0, len(byPeer))
	for peerID, sig := range byPeer {
		cp := make([]byte, len(sig))
		copy(cp, sig)
		out = append(out, config.Reveal{ProposerID: peerID, Secret: cp})
	}
	defaultRevealInbox.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ProposerID < out[j].ProposerID })
	return out
}

// pruneRevealsBelow drops buffered reveals for epochs before the given one.
// Called after an epoch finalises: its reveal window is closed, so nothing
// held for it can ever be used again.
//
// Also drops Accumulators more than one epoch older than that: the epoch just
// finalised (epoch-1) keeps its Accumulator; anything before it is settled.
func pruneRevealsBelow(epoch uint64) {
	pruneBLSRevealCommitteesBelow(epoch)
	defaultRevealInbox.mu.Lock()
	for e := range defaultRevealInbox.byEpoch {
		if e < epoch {
			delete(defaultRevealInbox.byEpoch, e)
		}
	}
	defaultRevealInbox.mu.Unlock()

	if epoch < 2 {
		return
	}
	defaultEntropyAccumulatorStore.mu.Lock()
	for e := range defaultEntropyAccumulatorStore.accs {
		if e < epoch-1 {
			delete(defaultEntropyAccumulatorStore.accs, e)
		}
	}
	defaultEntropyAccumulatorStore.mu.Unlock()
}

// InboxCountForEpoch reports how many reveals are buffered for epoch.
// Exported for operational visibility — "are reveals actually arriving" is the
// first question to ask when an epoch falls back unexpectedly.
func InboxCountForEpoch(epoch uint64) int {
	defaultRevealInbox.mu.Lock()
	defer defaultRevealInbox.mu.Unlock()
	return len(defaultRevealInbox.byEpoch[epoch])
}

// Inbox bounds; see AddInboundReveal.
const (
	maxRevealEpochsAhead  = 2
	maxRevealEpochsBehind = 1
	maxRevealInboxEpochs  = 8
	maxRevealsPerEpoch    = 4 * EntropyCommitteeSize
)

// revealCurrentEpoch reports this node's current entropy epoch, or false
// before the slot clock has advanced (startup), when no bound can be applied.
var revealCurrentEpoch = func() (uint64, bool) {
	if !DefaultSlotStore.hasCommitted() {
		return 0, false
	}
	return EpochForSlot(DefaultSlotStore.Current()), true
}

// revealPeerSeated reports whether peerID is on epoch's entropy committee.
// Only consulted once an epoch is at capacity. Unresolvable committee (no
// beacon or no anchored pool yet) counts as not seated: the inbox is already
// full of candidates.
var revealPeerSeated = func(epoch uint64, peerID string) bool {
	members, err := revealCommitteeFor(epoch)
	if err != nil {
		return false
	}
	for _, m := range members {
		if m.PeerID == peerID {
			return true
		}
	}
	return false
}

func checkRevealEpochWindow(epoch uint64) error {
	cur, ok := revealCurrentEpoch()
	if !ok {
		return nil
	}
	if epoch > cur+maxRevealEpochsAhead || epoch+maxRevealEpochsBehind < cur {
		return ErrRevealEpochOutOfWindow
	}
	return nil
}
