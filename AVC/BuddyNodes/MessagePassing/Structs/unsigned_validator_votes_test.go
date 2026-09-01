package Structs

// Buddy-side tests for the unsigned normal-validator seam
// (avc/crdt/votes/unsigned_votes.go). These cover the jmdn half — the
// verifyTallySignatures stage — while the avc half (TallyBlock's
// authorization gate) is covered in avc/crdt/votes/unsigned_votes_test.go.
//
// Reuses stage5TestPeer / stage5SignedRecord and the stage5* constants from
// vote_crdt_stage5_test.go: same package, and the point is to exercise the
// SAME verification stage those tests already pin down, under the new flag.

import (
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/libp2p/go-libp2p/core/peer"
)

// withUnsignedSeam sets the avc-side seam flag for one test and restores it.
// The flag lives in avc/crdt/votes (one flag, one source of truth, read by
// both modules) so it must be set there, not shadowed locally.
func withUnsignedSeam(t *testing.T, on bool) {
	t.Helper()
	original := avcvotes.AllowUnsignedValidatorVotes
	avcvotes.AllowUnsignedValidatorVotes = on
	t.Cleanup(func() { avcvotes.AllowUnsignedValidatorVotes = original })
}

func unsignedValidatorRecord(peerID peer.ID, vote int8) avcvotes.VoteRecord {
	return avcvotes.VoteRecord{
		PeerID:    peerID.String(),
		Vote:      vote,
		BlockHash: stage5BlockHash,
		Height:    stage5Height,
		// No BLSSignature, no BLSPubKeyHex — the approved design's shape.
	}
}

// An unsigned normal-validator vote must survive the verification stage
// rather than being dropped as a forgery. Before the seam, VerifyForBlock was
// handed an empty signature, failed, and the vote was counted as dropped.
func TestVerifyTallySignatures_UnsignedValidatorVoteSurvivesWhenFlagOn(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, true)

	validator := stage5TestPeer(t)
	rec := unsignedValidatorRecord(validator, 1)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{validator.String(): {1}},
		Signatures:            map[string][]avcvotes.VoteRecord{validator.String(): {rec}},
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if dropped != 0 {
		t.Fatalf("an unsigned validator vote must not be dropped with the seam on, dropped=%d", dropped)
	}
	if got, ok := verified.SingleVotePeers()[validator.String()]; !ok || got != 1 {
		t.Fatalf("unsigned validator vote lost at the verification stage: %+v", verified.SingleVotePeers())
	}
}

// Rollback proof for the jmdn half: with the flag off, an unsigned record goes
// to the verifier, fails, and is dropped — exactly the pre-seam behavior.
func TestVerifyTallySignatures_UnsignedValidatorVoteDroppedWhenFlagOff(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, false)

	validator := stage5TestPeer(t)
	rec := unsignedValidatorRecord(validator, 1)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{validator.String(): {1}},
		Signatures:            map[string][]avcvotes.VoteRecord{validator.String(): {rec}},
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if dropped != 1 {
		t.Fatalf("with the seam off an unsigned record must be dropped, dropped=%d", dropped)
	}
	if len(verified.SingleVotePeers()) != 0 {
		t.Fatalf("nothing should survive, got %+v", verified.SingleVotePeers())
	}
}

// The seam must not weaken forgery detection for records that DO carry BLS
// material. A forged signature is still dropped with the flag on, and a
// genuinely-signed Buddy vote in the same tally still counts — proving the two
// kinds travel their intended separate paths through one verification pass.
func TestVerifyTallySignatures_SeamDoesNotWeakenForgeryDetection(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, true)

	buddy := stage5TestPeer(t)
	forger := stage5TestPeer(t)
	validator := stage5TestPeer(t)

	buddyRec := stage5SignedRecord(t, buddy, 1, stage5Height, stage5BlockHash)

	// Forged: real key material, but the signature covers a different message.
	otherSigner, _, err := BLS_Signer.SignMessageForBlock(1, stage5ChainID, stage5Height, "0xdifferent-block", "")
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

	unsignedRec := unsignedValidatorRecord(validator, 1)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{
			buddy.String():     {1},
			forger.String():    {-1},
			validator.String(): {1},
		},
		Signatures: map[string][]avcvotes.VoteRecord{
			buddy.String():     {buddyRec},
			forger.String():    {forgedRec},
			validator.String(): {unsignedRec},
		},
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if dropped != 1 {
		t.Fatalf("expected exactly the forged vote dropped, got dropped=%d", dropped)
	}

	single := verified.SingleVotePeers()
	if _, ok := single[forger.String()]; ok {
		t.Error("forged signature must still be rejected with the seam on")
	}
	if got, ok := single[buddy.String()]; !ok || got != 1 {
		t.Error("a genuinely signed Buddy vote must still be verified and counted")
	}
	if got, ok := single[validator.String()]; !ok || got != 1 {
		t.Error("an unsigned validator vote must be admitted via the seam")
	}
}

// A half-populated record must not slip through the jmdn half either: with a
// signature present but no key, it goes to the verifier and fails, on both
// flag settings. This is the write-side mirror of the avc-side test.
func TestVerifyTallySignatures_HalfSignedRecordStillVerifiedAndDropped(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, true)

	sneaky := stage5TestPeer(t)
	rec := unsignedValidatorRecord(sneaky, 1)
	rec.BLSSignature = "deadbeef" // signature present, key omitted

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{sneaky.String(): {1}},
		Signatures:            map[string][]avcvotes.VoteRecord{sneaky.String(): {rec}},
	}

	_, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if dropped != 1 {
		t.Fatalf("a half-signed record must not bypass verification, dropped=%d", dropped)
	}
}
