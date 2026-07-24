package MessagePassing

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Default-OFF (post-incident): with the gate disabled, every requester is
// accepted so consensus liveness matches pre-C-04 behavior. This is the state a
// stock deployment runs in until JMDN_ENFORCE_VOTE_REQUESTER_AUTH=1 is set.
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

// C-04 ENABLED: only an authenticated committee member (the sequencer) may
// request this node's signed vote; an outsider is rejected.
func TestVoteRequesterAuthorized_EnabledBuddySetMembership(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)
	enforceVoteRequesterAuth = true

	seq := peer.ID("sequencer-peer")
	other := peer.ID("committee-peer-2")
	attacker := peer.ID("attacker-peer")

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
}

// C-04 ENABLED but the committee set is unknown/empty at request time: FAIL OPEN
// (accept) rather than brick consensus. This is the exact case that caused the
// 2026-07 halt when the gate fail-closed.
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
