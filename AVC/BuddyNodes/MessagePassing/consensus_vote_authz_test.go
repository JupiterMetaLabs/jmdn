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

// Only the pinned sequencer may request this node's signed vote; committee
// members and everyone else are rejected.
func TestVoteRequesterAuthorized_SourceResolvesMembershipDecides(t *testing.T) {
	defer setVoteRequesterAuthorizerForTest(nil)
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
	if voteRequesterAuthorized(member) {
		t.Fatalf("with a pinned sequencer, a committee member must not be able to request a vote result")
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
	defer setVoteRequesterAuthorizerForTest(nil)
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
	defer setVoteRequesterAuthorizerForTest(nil)
	defer SetAuthorizedRequesterSource(nil)

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) { return nil, false })

	if voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("an indeterminate source must reject the requester (fail closed), not fall back to an unauthenticated legacy path")
	}
}

// New: the source not being wired at all (nil) — the state of this node's
// own boot window before main.go's startup wiring runs — also fails closed.
func TestVoteRequesterAuthorized_SourceNotWiredFailsClosed(t *testing.T) {
	defer setVoteRequesterAuthorizerForTest(nil)
	defer SetAuthorizedRequesterSource(nil)
	SetAuthorizedRequesterSource(nil)

	if voteRequesterAuthorized(peer.ID("anyone")) {
		t.Fatalf("an unwired source must reject the requester (fail closed)")
	}
}

// An injected authorizer (startup/test override) has final say and is
// consulted before the authoritative source at all.
func TestVoteRequesterAuthorized_InjectedAuthorizerWins(t *testing.T) {
	defer setVoteRequesterAuthorizerForTest(nil)
	defer SetAuthorizedRequesterSource(nil)

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return map[peer.ID]struct{}{}, true // would reject everyone if consulted
	})
	setVoteRequesterAuthorizerForTest(func(p peer.ID) bool { return p == peer.ID("x") })

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
	defer setVoteRequesterAuthorizerForTest(nil)
	defer SetAuthorizedRequesterSource(nil)

	SetAuthorizedRequesterSource(func() (map[peer.ID]struct{}, bool) {
		return AuthorizedRequesterSet([]peer.ID{peer.ID("")}, ""), true
	})

	if voteRequesterAuthorized(peer.ID("")) {
		t.Fatalf("an empty remote peer id must never be authorized")
	}
}

// AuthorizedRequesterSet: the pinned sequencer alone when pinned; the
// committee (empties dropped) when not.
func TestAuthorizedRequesterSet_Composition(t *testing.T) {
	seq := peer.ID("sequencer-peer")
	set := AuthorizedRequesterSet([]peer.ID{peer.ID("c1"), "", peer.ID("c2")}, seq)
	if _, ok := set[seq]; !ok || len(set) != 1 {
		t.Fatalf("pinned: want only the sequencer, got %v", set)
	}
	noSeq := AuthorizedRequesterSet([]peer.ID{peer.ID("c1"), "", peer.ID("c2")}, "")
	for _, want := range []peer.ID{"c1", "c2"} {
		if _, ok := noSeq[want]; !ok {
			t.Fatalf("unpinned: expected %q in the authorized set", want)
		}
	}
	if _, ok := noSeq[peer.ID("")]; ok || len(noSeq) != 2 {
		t.Fatalf("unpinned: want 2 committee members, got %v", noSeq)
	}
}

// Moved from consensus_vote_authz.go (test-only helper; golangci-lint runs with tests: false,
// so it was flagged unused in non-test code).
// setVoteRequesterAuthorizerForTest overrides the built-in committee-
// membership check. Test-only -- see voteRequesterAuthorizer's comment for
// why this is unexported rather than merely documented as test-only.
func setVoteRequesterAuthorizerForTest(fn func(peer.ID) bool) { voteRequesterAuthorizer = fn }
