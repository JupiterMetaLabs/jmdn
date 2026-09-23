package MessagePassing

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// D-26(d) / CON-03, AVC-CONSENSUS-HANDOVER.md rev 7: the gate is now
// permanently enforced, with no disabled state and no fail-open branch.
// These tests replace the pre-rev-7 suite, which asserted a default-off
// master switch and two distinct fail-open cases (empty buddy set, gate
// disabled) — both deliberately inverted or removed below, matching the
// same rev-7 change these tests exist to pin.

// Only an authenticated member of the resolved authoritative set (committee
// ∪ pinned sequencer) may request this node's signed vote; every other peer
// is rejected.
func TestVoteRequesterAuthorized_SourceResolvesMembershipDecides(t *testing.T) {
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)

	seq := peer.ID("pinned-sequencer")
	member := peer.ID("committee-1")
	stranger := peer.ID("stranger")

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return AuthorizedRequesterSet([]peer.ID{member}, seq), true
	})

	if !voteRequesterAuthorized(seq) {
		t.Fatalf("pinned sequencer must be authorized even though it is not a committee member")
	}
	if !voteRequesterAuthorized(member) {
		t.Fatalf("committee member must be authorized")
	}
	if voteRequesterAuthorized(stranger) {
		t.Fatalf("non-member must be rejected when the authoritative source resolves")
	}
}

// Inverted from the pre-rev-7 suite: an empty resolved set must reject
// everyone, not fail open. A deployment with no sequencer pin and no
// committee members resolved has nobody to authorize — that is a
// misconfiguration to surface as rejected requests, not a reason to accept
// an arbitrary caller's signature request.
func TestVoteRequesterAuthorized_EmptyResolvedSetRejectsEveryone(t *testing.T) {
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return map[peer.ID]struct{}{}, true
	})

	if voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("an empty authoritative set must reject every requester (no more fail-open)")
	}
}

// Inverted from TestVoteRequesterAuthorized_SourceIndeterminateFallsBack: a
// momentarily unresolvable source (ok==false) now fails CLOSED. There is no
// more legacy buddy-set fallback to fall back to.
func TestVoteRequesterAuthorized_SourceIndeterminateFailsClosed(t *testing.T) {
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) { return nil, false })

	if voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("an indeterminate source must reject the requester (fail closed), not fall back to an unauthenticated legacy path")
	}
}

// New: the source not being wired at all (nil) — the state of this node's
// own boot window before main.go's startup wiring runs — also fails closed.
func TestVoteRequesterAuthorized_SourceNotWiredFailsClosed(t *testing.T) {
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)
	SetAuthorizedRequesterSource(nil)

	if voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("an unwired source must reject the requester (fail closed)")
	}
}

// An injected authorizer (startup/test override) has final say and is
// consulted before the authoritative source at all.
func TestVoteRequesterAuthorized_InjectedAuthorizerWins(t *testing.T) {
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return map[peer.ID]struct{}{}, true // would reject everyone if consulted
	})
	SetVoteResultRequesterAuthorizer(func(p peer.ID) bool { return p == peer.ID("x") })

	if !voteRequesterAuthorized(peer.ID("x")) {
		t.Fatalf("injected authorizer must allow x")
	}
	if voteRequesterAuthorized(peer.ID("someone-else")) {
		t.Fatalf("injected authorizer must override the authoritative-source path")
	}
}

// An empty remote peer ID (should not occur — the stream layer authenticates
// the remote peer — but defense in depth) is always rejected, regardless of
// the source.
func TestVoteRequesterAuthorized_EmptyRemotePeerRejected(t *testing.T) {
	defer SetVoteResultRequesterAuthorizer(nil)
	defer SetAuthorizedRequesterSource(nil)

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return AuthorizedRequesterSet([]peer.ID{peer.ID("")}, ""), true
	})

	if voteRequesterAuthorized(peer.ID("")) {
		t.Fatalf("an empty remote peer id must never be authorized")
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
