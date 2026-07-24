package MessagePassing

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// C-04: only an authenticated committee member (the sequencer) may request this
// node's signed vote. An outsider must never obtain a signature — that was the
// open signing oracle. voteRequesterAuthorized is the fail-closed gate.
func TestVoteRequesterAuthorized_BuddySetMembership(t *testing.T) {
	// Restore package seams after the test.
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)

	seq := peer.ID("sequencer-peer")
	other := peer.ID("committee-peer-2")
	attacker := peer.ID("attacker-peer")

	// Authenticated buddy set = {sequencer, committee-peer-2}.
	SetVoteResultRequesterAuthorizer(nil)
	SetVoteBuddySetProvider(func() []peer.ID { return []peer.ID{seq, other} })

	if !voteRequesterAuthorized(seq) {
		t.Fatalf("legitimate sequencer (in buddy set) must be authorized")
	}
	if !voteRequesterAuthorized(other) {
		t.Fatalf("committee member (in buddy set) must be authorized")
	}
	if voteRequesterAuthorized(attacker) {
		t.Fatalf("SECURITY (C-04): non-committee peer authorized to request a signed vote")
	}
	if voteRequesterAuthorized("") {
		t.Fatalf("empty peer id must never be authorized")
	}
}

// Fail-closed: an empty/unknown buddy set authorizes no one, so the node signs
// for nobody rather than for an unauthenticated caller.
func TestVoteRequesterAuthorized_EmptySetFailsClosed(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)

	SetVoteResultRequesterAuthorizer(nil)
	SetVoteBuddySetProvider(func() []peer.ID { return nil })

	if voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("SECURITY (C-04): empty buddy set must authorize no requester (fail-closed)")
	}
}

// An injected authorizer (startup/test override) has final say over the built-in
// membership check.
func TestVoteRequesterAuthorized_InjectedAuthorizerWins(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)

	// Buddy set would reject "x", but the injected authorizer allows exactly it.
	SetVoteBuddySetProvider(func() []peer.ID { return []peer.ID{peer.ID("someone-else")} })
	SetVoteResultRequesterAuthorizer(func(p peer.ID) bool { return p == peer.ID("x") })

	if !voteRequesterAuthorized(peer.ID("x")) {
		t.Fatalf("injected authorizer must allow x")
	}
	if voteRequesterAuthorized(peer.ID("someone-else")) {
		t.Fatalf("injected authorizer must override the buddy-set membership path")
	}
}
