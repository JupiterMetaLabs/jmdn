package PubSubMessages

// D-26(c): handleVoteResultRequest resolves block_number/consensus_hash from
// this node's OWN view via LookupConsensusMessageByBlockHash instead of
// trusting the requester's payload, so a mismatched pair cannot be signed
// into existence. This pins the accessor itself -- the one piece of that fix
// that is a pure, isolated function -- since the handler it feeds is a large,
// stream-driven method with no existing test scaffolding in this package to
// build on (see the finding's own commit message, which disclosed this as
// "Untested: no Go toolchain on this machine" and validated by parse/build
// only). Exercising the handler end-to-end remains an open follow-up.

import "testing"

func TestLookupConsensusMessageByBlockHash_EmptyHashReturnsNil(t *testing.T) {
	if got := LookupConsensusMessageByBlockHash(""); got != nil {
		t.Fatalf("LookupConsensusMessageByBlockHash(\"\") = %+v, want nil", got)
	}
}

func TestLookupConsensusMessageByBlockHash_UnknownHashReturnsNil(t *testing.T) {
	if got := LookupConsensusMessageByBlockHash("hash-never-inserted"); got != nil {
		t.Fatalf("LookupConsensusMessageByBlockHash(unknown) = %+v, want nil", got)
	}
}

// The core security property: a hash this node genuinely has resolves to the
// SAME message this node cached for it, not to anything a caller supplies —
// there is no parameter here but the hash itself.
func TestLookupConsensusMessageByBlockHash_KnownHashReturnsTheCachedMessage(t *testing.T) {
	const hash = "d26c-test-block-hash"
	want := &ConsensusMessage{SequencerID: "seq-under-test", TotalNodes: 7}

	cacheMu.Lock()
	cacheConsensuMessage[hash] = want
	cacheMu.Unlock()
	defer func() {
		cacheMu.Lock()
		delete(cacheConsensuMessage, hash)
		cacheMu.Unlock()
	}()

	got := LookupConsensusMessageByBlockHash(hash)
	if got != want {
		t.Fatalf("LookupConsensusMessageByBlockHash(%q) = %+v, want the exact cached pointer %+v",
			hash, got, want)
	}
}
