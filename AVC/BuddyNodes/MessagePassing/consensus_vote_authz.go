package MessagePassing

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

// Vote-result requester authorization (D-26(d) / THEBE-AUDIT-HLD.md CON-03).
//
// A vote-result request is the sequencer asking a committee member to return —
// and BLS-sign — this node's aggregated vote for a specific block. This gate
// restricts WHO may request that signature so the node does not BLS-sign a
// caller-supplied block hash for an arbitrary peer.
//
// The libp2p stream's remote peer ID is cryptographically authenticated by the
// transport handshake, so restricting WHO may request a signature is sound.
// The authorized set is the authenticated committee snapshot UNION the
// PINNED sequencer peer_id (config.Consensus.SequencerPinnedPeerID — the pin
// CON-03 introduces; see config/settings/config.go's doc comment on that
// field for why a config-level pin, not a discovered value, is the correct
// source: the sequencer is a single, static, per-fleet identity with no
// rotation mechanism anywhere in this codebase).
//
// AVC-CONSENSUS-HANDOVER.md rev 7: this gate is now permanently enforced —
// no env-gated master switch, no fail-open branch. It shipped for a long
// time default-off (JMDN_ENFORCE_VOTE_REQUESTER_AUTH, default false) because
// there was no reliable authenticated source for "who is the sequencer": the
// only signals available to a buddy were self-declared and unauthenticated
// (a broadcast buddy list, or msg.SequencerID in a consensus message), so
// fail-closing against them risked rejecting the legitimate sequencer and
// stalling the chain. SetAuthorizedRequesterSource + the sequencer pin close
// that gap, so the flag and its fail-open branches no longer serve a
// purpose — and per the user's standing instruction, this ships as one
// coordinated fleet-wide restart, not a flag flip some nodes might miss
// (which is exactly the failure mode a leftover two-state flag invites; see
// audit RC-2, "no observe/shadow rung on two-state rollout flags" — the
// fix here is to not have the second state at all).
//
// This restricts signing to committee members (∪ the pinned sequencer). It
// does NOT by itself constrain a committee member from requesting (the
// aggregate-over-partial-view and caller-supplied-hash concerns are a
// separate, larger consensus change); see the residual noted on
// handleVoteResultRequest.

// voteRequesterAuthorizer, when non-nil, fully decides whether a requester peer
// is authorized. Tests inject this to force allow/deny without a live source.
var voteRequesterAuthorizer func(peer.ID) bool

// SetVoteResultRequesterAuthorizer overrides the built-in committee-membership
// check. Call once at startup or in tests.
func SetVoteResultRequesterAuthorizer(fn func(peer.ID) bool) { voteRequesterAuthorizer = fn }

// authorizedRequesterSource supplies the AUTHORITATIVE set of peers allowed
// to request this node's signed vote, plus an `ok` flag telling whether that
// set could be resolved right now. Wired unconditionally at startup from two
// call sites in main.go (the seed-client / non-sequencer branch, and the
// sequencer's own WireCommitteeSources branch) using AuthorizedRequesterSet
// (committee snapshot ∪ SequencerPinnedPeerID). There is no other path to
// authorization any more — see voteRequesterAuthorized below.
var authorizedRequesterSource func() (set map[peer.ID]struct{}, ok bool)

// SetAuthorizedRequesterSource wires the authoritative requester set
// (committee snapshot ∪ pinned sequencer). Call once at startup — both
// main.go call sites do this unconditionally, not gated by any flag.
func SetAuthorizedRequesterSource(fn func() (map[peer.ID]struct{}, bool)) {
	authorizedRequesterSource = fn
}

// AuthorizedRequesterSet composes the authoritative requester set from the
// authenticated committee members and the pinned sequencer peer_id. Empty
// pinnedSequencer ("") is omitted — that is the state of an unpinned
// deployment (config.Consensus.SequencerPinnedPeerID == ""), which degrades
// the authorized set to committee-only; see that field's doc comment for the
// operational risk this carries and production_posture.go for the boot-time
// warning.
func AuthorizedRequesterSet(committee []peer.ID, pinnedSequencer peer.ID) map[peer.ID]struct{} {
	set := make(map[peer.ID]struct{}, len(committee)+1)
	for _, p := range committee {
		if p != "" {
			set[p] = struct{}{}
		}
	}
	if pinnedSequencer != "" {
		set[pinnedSequencer] = struct{}{}
	}
	return set
}

// voteRequesterAuthorized is the fail-closed gate applied in
// handleVoteResultRequest. It returns true only when the authenticated
// stream peer is authorized to request this node's signed vote:
//   - an injected authorizer (test/startup override) has final say; otherwise
//   - the requester must be a member of the authoritative set (committee ∪
//     pinned sequencer), resolved fresh on every call.
//
// It fails closed in every other case: the source not being wired yet
// (expected only transiently during this node's own boot window — see
// package doc comment), the source being momentarily unresolvable, or the
// requester simply not being a member. There is no more empty-set or
// disabled-gate fail-open: a rejected request costs the requester one
// missed signature contribution for one round, the same cost as that
// responding node being briefly offline, which BFT quorum already
// tolerates — it does not stall the fleet.
func voteRequesterAuthorized(remote peer.ID) bool {
	if voteRequesterAuthorizer != nil {
		return voteRequesterAuthorizer(remote)
	}
	if remote == "" {
		return false
	}
	if authorizedRequesterSource == nil {
		return false
	}
	set, ok := authorizedRequesterSource()
	if !ok {
		return false
	}
	_, member := set[remote]
	return member
}
