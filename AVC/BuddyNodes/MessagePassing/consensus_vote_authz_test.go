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

// AuthorizedRequesterSet composes committee ∪ pinned sequencer, dropping empties.
func TestAuthorizedRequesterSet_Composition(t *testing.T) {
	seq := peer.ID("sequencer-peer")
	set := AuthorizedRequesterSet([]peer.ID{peer.ID("c1"), "", peer.ID("c2")}, seq)
	for _, want := range []peer.ID{"c1", "c2", seq} {
		if _, ok := set[want]; !ok {
			t.Fatalf("expected %q in the authorized set", want)
		}
	}
	if _, ok := set[peer.ID("")]; ok {
		t.Fatalf("empty peer id must be dropped")
	}
	if len(set) != 3 {
		t.Fatalf("expected 3 members, got %d", len(set))
	}
	// Empty pinned sequencer is simply omitted (no panic, no empty key).
	noSeq := AuthorizedRequesterSet([]peer.ID{peer.ID("c1")}, "")
	if len(noSeq) != 1 {
		t.Fatalf("empty pinned sequencer must be omitted; got %d members", len(noSeq))
	}
}

// CON-03 core: when the authoritative source RESOLVES (ok==true), it is
// definitive. The pinned sequencer — which is NOT in the buddy set — is allowed,
// and a non-member is rejected EVEN WHEN the legacy buddy set is empty (the old
// fail-open is closed).
func TestVoteRequesterAuthorized_AuthoritativeSourceIsDefinitive(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)
	enforceVoteRequesterAuth = true

	seq := peer.ID("pinned-sequencer")
	member := peer.ID("committee-1")
	stranger := peer.ID("stranger")

	// Empty buddy set on purpose: proves the decision comes from the source, and
	// that the old "empty set -> accept anyone" fail-open no longer applies.
	SetVoteBuddySetProvider(func() []peer.ID { return nil })
	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return AuthorizedRequesterSet([]peer.ID{member}, seq), true
	})

	if !voteRequesterAuthorized(seq) {
		t.Fatalf("pinned sequencer must be authorized even though it is not in the buddy set")
	}
	if !voteRequesterAuthorized(member) {
		t.Fatalf("committee member must be authorized")
	}
	if voteRequesterAuthorized(stranger) {
		t.Fatalf("non-member must be REJECTED when the authoritative source resolves (fail-open closed)")
	}
}

// When the authoritative source is momentarily UNRESOLVABLE (ok==false), the gate
// falls back to the liveness-preserving legacy buddy-set path rather than halting.
func TestVoteRequesterAuthorized_SourceIndeterminateFallsBack(t *testing.T) {
	defer SetVoteBuddySetProvider(nil)
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)
	enforceVoteRequesterAuth = true

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) { return nil, false })

	// Fallback with an empty buddy set -> liveness fail-open (accept).
	SetVoteBuddySetProvider(func() []peer.ID { return nil })
	if !voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("indeterminate source + empty buddy set must fail open (liveness)")
	}

	// Fallback with a populated buddy set -> membership decides.
	inSet := peer.ID("buddy-1")
	SetVoteBuddySetProvider(func() []peer.ID { return []peer.ID{inSet} })
	if !voteRequesterAuthorized(inSet) {
		t.Fatalf("indeterminate source: buddy-set member must be accepted")
	}
	if voteRequesterAuthorized(peer.ID("outsider")) {
		t.Fatalf("indeterminate source: non-buddy must be rejected when the set is populated")
	}
}

// The master switch still wins: with the gate disabled, even a configured
// authoritative source that would reject the peer is bypassed (non-breaking).
func TestVoteRequesterAuthorized_DisabledBypassesSource(t *testing.T) {
	defer SetAuthorizedRequesterSource(nil)
	defer func(v bool) { enforceVoteRequesterAuth = v }(enforceVoteRequesterAuth)
	enforceVoteRequesterAuth = false

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return map[peer.ID]struct{}{}, true // would reject everyone if consulted
	})
	if !voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("disabled gate must accept without consulting the source")
	}
}
