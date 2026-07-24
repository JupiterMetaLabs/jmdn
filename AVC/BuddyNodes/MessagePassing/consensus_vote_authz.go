package MessagePassing

import (
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Vote-result requester authorization (C-04).
//
// A vote-result request is the sequencer asking a committee member to return —
// and BLS-sign — this node's aggregated vote for a specific block. Before this
// gate, handleVoteResultRequest signed for ANY peer that opened the stream, over
// a caller-supplied block hash. That is a signing oracle: an outsider (or a
// merely seed-weighted peer) could harvest genuine committee signatures for a
// hash of its choosing and assemble a certificate.
//
// The libp2p stream's remote peer ID is cryptographically authenticated by the
// transport handshake, so restricting WHO may request a signature is sound. The
// legitimate sequencer is always a member of this node's authenticated buddy
// set — that is exactly how the buddy itself resolves the sequencer when it
// sends results back (sendVoteResultToSequencer picks the sequencer from
// BuddyNodes.Buddies_Nodes). So the gate is: the requester MUST be in the
// authenticated committee/buddy set; every other peer is rejected.
//
// This closes the oracle to outsiders. It does NOT by itself stop a committee
// member from requesting (the aggregate-over-partial-view and
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
	if voteRequesterAuthorizer != nil {
		return voteRequesterAuthorizer(remote)
	}
	if remote == "" {
		return false
	}
	for _, p := range currentBuddySet() {
		if p == remote {
			return true
		}
	}
	return false
}
