package Structs

// Buddy-side tests pinning down verifyTallySignatures' handling of a record
// carrying neither a BLS signature nor a public key, after the unsigned
// normal-validator seam itself was removed (avc D-38, commit 66c0e6c; jmdn
// side removed alongside it). Before the seam ever existed, and after its
// removal, such a record is simply unauthenticated input: it reaches the
// verifier, fails (an empty signature can never verify), and is dropped —
// exactly like any other unverifiable vote. These tests exist so that
// invariant stays pinned down, not because the seam is coming back.
//
// Reuses stage5TestPeer / stage5SignedRecord and the stage5* constants from
// vote_crdt_stage5_test.go: same package, and the point is to exercise the
// SAME verification stage those tests already pin down.

import (
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/libp2p/go-libp2p/core/peer"
)

func unsignedValidatorRecord(peerID peer.ID, vote int8) avcvotes.VoteRecord {
	return avcvotes.VoteRecord{
		PeerID:    peerID.String(),
		Vote:      vote,
		BlockHash: stage5BlockHash,
		Height:    stage5Height,
		// No BLSSignature, no BLSPubKeyHex.
	}
}

// A record with neither a signature nor a key goes to the verifier, fails,
// and is dropped — there is no path that admits it.
func TestVerifyTallySignatures_UnsignedRecordDropped(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")

	validator := stage5TestPeer(t)
	rec := unsignedValidatorRecord(validator, 1)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{validator.String(): {1}},
		Signatures:            map[string][]avcvotes.VoteRecord{validator.String(): {rec}},
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if dropped != 1 {
		t.Fatalf("a record with no signature must be dropped, dropped=%d", dropped)
	}
	if len(verified.SingleVotePeers()) != 0 {
		t.Fatalf("nothing should survive, got %+v", verified.SingleVotePeers())
	}
}

// A half-populated record (signature present, key omitted) must not slip
// through either: it goes to the verifier and fails just like a fully
// unsigned one.
func TestVerifyTallySignatures_HalfSignedRecordStillVerifiedAndDropped(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")

	sneaky := stage5TestPeer(t)
	rec := unsignedValidatorRecord(sneaky, 1)
	rec.BLSSignature = "deadbeef" // signature present, key omitted

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{sneaky.String(): {1}},
		Signatures:            map[string][]avcvotes.VoteRecord{sneaky.String(): {rec}},
	}

	_, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if dropped != 1 {
		t.Fatalf("a half-signed record must not bypass verification, dropped=%d", dropped)
	}
}

// A forged signature (real key material, wrong message) is dropped, while a
// genuinely-signed Buddy vote in the same tally still counts — proving the
// two travel their intended separate paths through one verification pass.
func TestVerifyTallySignatures_ForgeryStillDetectedAlongsideValidSignedVote(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")

	buddy := stage5TestPeer(t)
	forger := stage5TestPeer(t)

	buddyRec := stage5SignedRecord(t, buddy, 1, stage5Height, stage5BlockHash)

	// Forged: real key material, but the signature covers a different message.
	otherSigner, _, err := BLS_Signer.SignMessageForBlock(1, stage5ChainID, stage5Height, "0xdifferent-block", stage5ConsensusHash)
	if err != nil {
		t.Fatalf("building a forged signature fixture: %v", err)
	}
	forgedRec := avcvotes.VoteRecord{
		PeerID:       forger.String(),
		Vote:         -1,
		BlockHash:    stage5BlockHash,
		Height:       stage5Height,
		BLSSignature: otherSigner.Signature,
		BLSPubKeyHex: otherSigner.PubKey,
	}

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{
			buddy.String():  {1},
			forger.String(): {-1},
		},
		Signatures: map[string][]avcvotes.VoteRecord{
			buddy.String():  {buddyRec},
			forger.String(): {forgedRec},
		},
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if dropped != 1 {
		t.Fatalf("expected exactly the forged vote dropped, got dropped=%d", dropped)
	}

	single := verified.SingleVotePeers()
	if _, ok := single[forger.String()]; ok {
		t.Error("forged signature must still be rejected")
	}
	if got, ok := single[buddy.String()]; !ok || got != 1 {
		t.Error("a genuinely signed Buddy vote must still be verified and counted")
	}
}

// The other counters describe upstream work (TallyBlock's own skip/malformed
// accounting) and must still be copied through unchanged.
func TestVerifyTallySignatures_UpstreamCountersStillCopied(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{},
		Signatures:            map[string][]avcvotes.VoteRecord{},
		SkippedUnauthorized:   7,
		MalformedVotes:        3,
		MalformedSignatures:   5,
	}

	verified, _ := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if verified.SkippedUnauthorized != 7 || verified.MalformedVotes != 3 || verified.MalformedSignatures != 5 {
		t.Fatalf("upstream counters must pass through unchanged: skipped=%d malformed=%d malformedSigs=%d",
			verified.SkippedUnauthorized, verified.MalformedVotes, verified.MalformedSignatures)
	}
}
