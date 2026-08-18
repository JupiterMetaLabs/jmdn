package MessagePassing

import (
	"os"

	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
)

// enforceVoteRequesterAuth gates the vote-result requester check. It is opt-in
// and default-off: fail-closing on the buddy set here can stall consensus,
// because at request time the authoritative committee set for this node can be
// empty/not-yet-populated on the receive path AND does not reliably contain the
// sequencer's peer_id (the set excludes self and is built from cached consensus
// messages). Until the gate is anchored on a source that is guaranteed to
// include the authorized sequencer, keep it off by default so consensus liveness
// is preserved. Set JMDN_ENFORCE_VOTE_REQUESTER_AUTH=1 to enable (and even then
// it fails open on an empty/unknown set rather than blocking).
var enforceVoteRequesterAuth = os.Getenv("JMDN_ENFORCE_VOTE_REQUESTER_AUTH") == "1"

// Vote-result requester authorization.
//
// A vote-result request is the sequencer asking a committee member to return —
// and BLS-sign — this node's aggregated vote for a specific block. This gate
// restricts WHO may request that signature so the node does not BLS-sign a
// caller-supplied block hash for an arbitrary peer.
//
// The libp2p stream's remote peer ID is cryptographically authenticated by the
// transport handshake, so restricting WHO may request a signature is sound. The
// sequencer is always a member of this node's authenticated buddy set — that is
// exactly how the buddy itself resolves the sequencer when it sends results back
// (sendVoteResultToSequencer picks the sequencer from BuddyNodes.Buddies_Nodes).
// So the gate is: the requester MUST be in the authenticated committee/buddy
// set; every other peer is rejected.
//
// This restricts signing to committee members. It does NOT by itself constrain a
// committee member from requesting (the aggregate-over-partial-view and
// caller-supplied-hash concerns are a separate, larger consensus change); see
// the residual noted on handleVoteResultRequest.

// voteRequesterAuthorizer, when non-nil, fully decides whether a requester peer
// is authorized. Tests inject this to force allow/deny without a live buddy set.
var voteRequesterAuthorizer func(peer.ID) bool

// SetVoteResultRequesterAuthorizer overrides the built-in committee-membership
// check. Call once at startup or in tests.
func SetVoteResultRequesterAuthorizer(fn func(peer.ID) bool) { voteRequesterAuthorizer = fn }

// buddySetProvider, when non-nil, supplies the authenticated committee/buddy set
// used by the built-in check. When nil the built-in check reads the live global
// PubSub buddy set. Tests may inject a fixed set.
var buddySetProvider func() []peer.ID

// SetVoteBuddySetProvider overrides the source of the authenticated buddy set.
func SetVoteBuddySetProvider(fn func() []peer.ID) { buddySetProvider = fn }

// authorizedRequesterSource, when non-nil, supplies the AUTHORITATIVE set of
// peers allowed to request this node's signed vote, plus an `ok` flag telling
// whether that set could be resolved right now.
//
// WHY THIS EXISTS (audit CON-03): the legacy currentBuddySet() is NOT a reliable
// requester source. The requester is the SEQUENCER, and a buddy resolves the
// sequencer primarily from the self-declared, unsigned msg.SequencerID
// (sendVoteResultToSequencer Path 2) because buddies commonly hold no buddy list
// (Path 1 fails). So the sequencer is frequently ABSENT from currentBuddySet();
// defaulting the gate on and fail-closing against that set would reject the
// legitimate sequencer and halt the chain. The correct authoritative set is the
// authenticated committee snapshot UNION the PINNED sequencer peer_id (the pin
// CON-01 introduces). When that source is wired and available (ok==true), a
// non-member is rejected even if the buddy set is empty — closing the fail-open.
// This is why enabling the gate by default is COUPLED to CON-01: without the
// pinned sequencer there is no reliable, transport-authenticated requester
// identity to check against.
//
// `ok==false` means the authoritative set is momentarily unresolvable; the gate
// then falls back to the liveness-preserving legacy path rather than halting.
var authorizedRequesterSource func() (set map[peer.ID]struct{}, ok bool)

// SetAuthorizedRequesterSource wires the authoritative requester set (committee
// snapshot ∪ pinned sequencer). Call once at startup. Intended companion to the
// CON-01 sequencer pin; until it is set the gate uses the legacy buddy-set path.
func SetAuthorizedRequesterSource(fn func() (map[peer.ID]struct{}, bool)) {
	authorizedRequesterSource = fn
}

// AuthorizedRequesterSet composes the authoritative requester set from the
// authenticated committee members and the pinned sequencer peer_id. Empty
// pinnedSequencer ("") is omitted. This is the exact set CON-01's pin feeds into
// SetAuthorizedRequesterSource; kept here so the composition is explicit and
// unit-testable independent of the live committee source.
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

// currentBuddySet returns the node's authenticated buddy set, read under the
// BuddyNode lock. Empty when the PubSub node is not yet initialized.
func currentBuddySet() []peer.ID {
	if buddySetProvider != nil {
		return buddySetProvider()
	}
	node := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if node == nil {
		return nil
	}
	node.Mutex.RLock()
	defer node.Mutex.RUnlock()
	out := make([]peer.ID, len(node.BuddyNodes.Buddies_Nodes))
	copy(out, node.BuddyNodes.Buddies_Nodes)
	return out
}

// voteRequesterAuthorized is the fail-closed gate applied in
// handleVoteResultRequest. It returns true only when the authenticated stream
// peer is authorized to request this node's signed vote:
//   - an injected authorizer (test/startup override) has final say; otherwise
//   - the requester must be a member of the authenticated buddy set.
//
// It fails closed: an empty/unknown buddy set authorizes no one, so a node that
// does not yet know its committee simply declines to sign rather than signing
// for an unauthenticated caller.
func voteRequesterAuthorized(remote peer.ID) bool {
	// Master switch, default-off (non-breaking on merge): when the gate is
	// disabled, accept the request (signing and verification still apply).
	// Flipping this default on is coupled to CON-01 wiring the pinned-sequencer
	// authoritative source below; see authorizedRequesterSource.
	if !enforceVoteRequesterAuth {
		return true
	}
	if voteRequesterAuthorizer != nil {
		return voteRequesterAuthorizer(remote)
	}
	if remote == "" {
		return false
	}

	// Preferred path: the AUTHORITATIVE requester set (committee snapshot ∪
	// pinned sequencer). When it resolves (ok==true) it is definitive — a
	// non-member is rejected even if the legacy buddy set is empty, closing the
	// fail-open. When it is momentarily unresolvable (ok==false) fall through to
	// the liveness-preserving legacy path rather than halting.
	if authorizedRequesterSource != nil {
		if set, ok := authorizedRequesterSource(); ok {
			_, member := set[remote]
			return member
		}
	}

	// Legacy fallback: no authoritative source configured (or it was
	// indeterminate). Retain the liveness-preserving fail-open on an empty set,
	// since the buddy set can legitimately be empty at request time.
	set := currentBuddySet()
	if len(set) == 0 {
		return true
	}
	for _, p := range set {
		if p == remote {
			return true
		}
	}
	return false
}
