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

// ---------------------------------------------------------------------------
// J-1: UnsignedValidatorVotes must survive the verification stage.
//
// verifyTallySignatures rebuilds BlockTally field-by-field, and originally
// omitted UnsignedValidatorVotes entirely — so the counter came out 0 no
// matter how many votes had skipped the BLS key gate, and nothing in jmdn
// read it. avc exports that field for one reason (operator visibility of the
// unsigned seam) and asserts it in its own tests, so dropping it here defeated
// the safeguard that the seam's risk acceptance rests on. Not a miscount: the
// votes themselves were handled correctly. A visibility failure only.
// ---------------------------------------------------------------------------

// The count must reach the returned tally, alongside a signed vote so the two
// paths are exercised in one pass. Fails with UnsignedValidatorVotes=0 before
// the fix.
func TestVerifyTallySignatures_PropagatesUnsignedValidatorVotesCount(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, true)

	buddy := stage5TestPeer(t)
	v1 := stage5TestPeer(t)
	v2 := stage5TestPeer(t)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{
			buddy.String(): {1},
			v1.String():    {1},
			v2.String():    {-1},
		},
		Signatures: map[string][]avcvotes.VoteRecord{
			buddy.String(): {stage5SignedRecord(t, buddy, 1, stage5Height, stage5BlockHash)},
			v1.String():    {unsignedValidatorRecord(v1, 1)},
			v2.String():    {unsignedValidatorRecord(v2, -1)},
		},
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if dropped != 0 {
		t.Fatalf("nothing should be dropped here, dropped=%d", dropped)
	}
	if verified.UnsignedValidatorVotes != 2 {
		t.Fatalf("UnsignedValidatorVotes lost in the verified tally: got %d, want 2. "+
			"The seam is invisible downstream: an operator reading this tally cannot tell "+
			"that 2 of 3 counted votes skipped the BLS key gate.",
			verified.UnsignedValidatorVotes)
	}
	if len(verified.AuthorizedVotesByPeer) != 3 {
		t.Fatalf("expected all 3 peers counted, got %d", len(verified.AuthorizedVotesByPeer))
	}
}

// The counter must be DERIVED from surviving pairs, not copied from the input
// tally. This is the test that fails if someone "simplifies" the fix into
// `UnsignedValidatorVotes: tally.UnsignedValidatorVotes` in the struct
// literal: the input here claims 99, and 99 is not the truth about the output.
func TestVerifyTallySignatures_UnsignedCountIsDerivedNotCopied(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, true)

	v1 := stage5TestPeer(t)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{v1.String(): {1}},
		Signatures:            map[string][]avcvotes.VoteRecord{v1.String(): {unsignedValidatorRecord(v1, 1)}},
		// Deliberately wrong: a copy would propagate this verbatim.
		UnsignedValidatorVotes: 99,
	}

	verified, _ := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if verified.UnsignedValidatorVotes != 1 {
		t.Fatalf("UnsignedValidatorVotes must describe THIS tally (1 surviving unsigned vote), "+
			"got %d — a value of 99 means it was copied from the input rather than derived "+
			"from what actually survived, which overstates unauthenticated weight",
			verified.UnsignedValidatorVotes)
	}
}

// With the flag off the counter must be 0 — the default posture must be
// provably unaffected by the fix.
func TestVerifyTallySignatures_UnsignedCountZeroWhenFlagOff(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, false)

	v1 := stage5TestPeer(t)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{v1.String(): {1}},
		Signatures:            map[string][]avcvotes.VoteRecord{v1.String(): {unsignedValidatorRecord(v1, 1)}},
	}

	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if dropped != 1 {
		t.Fatalf("flag off: the unsigned record must still be dropped, dropped=%d", dropped)
	}
	if verified.UnsignedValidatorVotes != 0 {
		t.Fatalf("flag off must never report unsigned votes, got %d", verified.UnsignedValidatorVotes)
	}
}

// The other counters describe upstream work and must still be copied through.
// Guards the opposite over-correction: deriving everything would silently drop
// TallyBlock's own skip/malformed accounting.
func TestVerifyTallySignatures_UpstreamCountersStillCopied(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	withUnsignedSeam(t, true)

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{},
		Signatures:            map[string][]avcvotes.VoteRecord{},
		SkippedUnauthorized:   7,
		MalformedVotes:        3,
		MalformedSignatures:   5,
	}

	verified, _ := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, "")
	if verified.SkippedUnauthorized != 7 || verified.MalformedVotes != 3 || verified.MalformedSignatures != 5 {
		t.Fatalf("upstream counters must pass through unchanged: skipped=%d malformed=%d malformedSigs=%d",
			verified.SkippedUnauthorized, verified.MalformedVotes, verified.MalformedSignatures)
	}
}
