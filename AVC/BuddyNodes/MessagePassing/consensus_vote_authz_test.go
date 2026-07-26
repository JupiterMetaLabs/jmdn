package MessagePassing

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Default-off: with the gate disabled, every requester is accepted so consensus
// liveness is preserved. This is the state a stock deployment runs in until
// JMDN_ENFORCE_VOTE_REQUESTER_AUTH=1 is set.
func TestVoteRequesterAuthorized_DisabledByDefaultAcceptsAll(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)

	enforceVoteRequesterAuth = false
	SetVoteBuddySetProvider(func() []peer.ID { return []peer.ID{peer.ID("committee-peer")} })

	if !voteRequesterAuthorized(peer.ID("any-peer")) {
		t.Fatalf("gate disabled: any requester must be accepted (liveness)")
	}
}

// Enabled: only an authenticated committee member (the sequencer) may request
// this node's signed vote; every other peer is rejected.
func TestVoteRequesterAuthorized_EnabledBuddySetMembership(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)
	enforceVoteRequesterAuth = true

	seq := peer.ID("sequencer-peer")
	other := peer.ID("committee-peer-2")
	nonMember := peer.ID("nonmember-peer")

	SetVoteResultRequesterAuthorizer(nil)
	SetVoteBuddySetProvider(func() []peer.ID { return []peer.ID{seq, other} })

	if !voteRequesterAuthorized(seq) {
		t.Fatalf("legitimate sequencer (in buddy set) must be authorized")
	}
	if !voteRequesterAuthorized(other) {
		t.Fatalf("committee member (in buddy set) must be authorized")
	}
	if voteRequesterAuthorized(nonMember) {
		t.Fatalf("non-committee peer authorized to request a signed vote")
	}
}

// Enabled but the committee set is unknown/empty at request time: fail open
// (accept) rather than stall consensus. This is the case where fail-closing
// would block liveness.
func TestVoteRequesterAuthorized_EnabledEmptySetFailsOpen(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)
	enforceVoteRequesterAuth = true

	SetVoteResultRequesterAuthorizer(nil)
	SetVoteBuddySetProvider(func() []peer.ID { return nil })

	if !voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("empty committee set must FAIL OPEN (accept) to preserve liveness")
	}
}

// An injected authorizer (startup/test override) has final say when enabled.
func TestVoteRequesterAuthorized_EnabledInjectedAuthorizerWins(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)
	enforceVoteRequesterAuth = true

	SetVoteBuddySetProvider(func() []peer.ID { return []peer.ID{peer.ID("someone-else")} })
	SetVoteResultRequesterAuthorizer(func(p peer.ID) bool { return p == peer.ID("x") })

	if !voteRequesterAuthorized(peer.ID("x")) {
		t.Fatalf("injected authorizer must allow x")
	}
	if voteRequesterAuthorized(peer.ID("someone-else")) {
		t.Fatalf("injected authorizer must override the buddy-set membership path")
	}
}
