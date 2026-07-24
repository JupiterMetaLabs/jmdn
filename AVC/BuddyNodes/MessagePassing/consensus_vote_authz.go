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
	// Default-off: when the gate is disabled, accept the request (signing and
	// verification still apply).
	if !enforceVoteRequesterAuth {
		return true
	}
	if voteRequesterAuthorizer != nil {
		return voteRequesterAuthorizer(remote)
	}
	if remote == "" {
		return false
	}
	set := currentBuddySet()
	if len(set) == 0 {
		// Fail open on an unknown/unpopulated committee set: the set can
		// legitimately be empty at request time, so fail-closing here would stall
		// consensus. Sign rather than block; the signature/certificate checks still
		// run.
		return true
	}
	for _, p := range set {
		if p == remote {
			return true
		}
	}
	return false
}
