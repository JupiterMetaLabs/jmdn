package Structs

// Phase 1.5 tests, per docs/VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md §12.5.

import (
	"encoding/hex"
	"testing"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

func TestBuildVoteCertificate_AggregatesOnlyYesVoters(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	peerYes1 := stage5TestPeer(t)
	peerYes2 := stage5TestPeer(t)
	peerNo := stage5TestPeer(t)

	recYes1 := stage5SignedRecord(t, peerYes1, 1, stage5Height, stage5BlockHash)
	recYes2 := stage5SignedRecord(t, peerYes2, 1, stage5Height, stage5BlockHash)
	recNo := stage5SignedRecord(t, peerNo, -1, stage5Height, stage5BlockHash)

	tally := avcvotes.BlockTally{
		Signatures: map[string][]avcvotes.VoteRecord{
			peerYes1.String(): {recYes1},
			peerYes2.String(): {recYes2},
			peerNo.String():   {recNo},
		},
	}
	single := map[string]int8{
		peerYes1.String(): 1,
		peerYes2.String(): 1,
		peerNo.String():   -1,
	}

	cert, err := buildVoteCertificate(tally, single)
	if err != nil {
		t.Fatalf("buildVoteCertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected a non-nil certificate with 2 YES voters present")
	}
	if len(cert.Signers) != 2 {
		t.Fatalf("expected exactly 2 signers (YES voters only), got %v", cert.Signers)
	}
	for _, s := range cert.Signers {
		if s == peerNo.String() {
			t.Fatal("NO voter must never appear in the certificate's signer list")
		}
	}
	if cert.AggSig == "" {
		t.Fatal("expected a non-empty hex aggregate signature")
	}
	if _, err := hex.DecodeString(cert.AggSig); err != nil {
		t.Fatalf("AggSig is not valid hex: %v", err)
	}
}

func TestBuildVoteCertificate_NilWhenNoYesVoters(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	peerNo := stage5TestPeer(t)
	recNo := stage5SignedRecord(t, peerNo, -1, stage5Height, stage5BlockHash)

	tally := avcvotes.BlockTally{
		Signatures: map[string][]avcvotes.VoteRecord{peerNo.String(): {recNo}},
	}
	single := map[string]int8{peerNo.String(): -1}

	cert, err := buildVoteCertificate(tally, single)
	if err != nil {
		t.Fatalf("buildVoteCertificate: %v", err)
	}
	if cert != nil {
		t.Fatalf("expected nil certificate with zero YES voters, got %+v", cert)
	}
}

func TestBuildVoteCertificate_EmptySingleMapIsNilNotError(t *testing.T) {
	cert, err := buildVoteCertificate(avcvotes.BlockTally{}, map[string]int8{})
	if err != nil {
		t.Fatalf("buildVoteCertificate on empty input: %v", err)
	}
	if cert != nil {
		t.Fatalf("expected nil certificate for empty input, got %+v", cert)
	}
}

// A peer present in `single` but missing from tally.Signatures (shouldn't
// happen given SingleVotePeers' contract, but defensive) must be excluded
// from the certificate rather than panicking or failing the whole build.
func TestBuildVoteCertificate_SkipsPeerWithNoBackingSignature(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	peerYes := stage5TestPeer(t)
	peerGhost := stage5TestPeer(t) // in `single`, absent from Signatures

	recYes := stage5SignedRecord(t, peerYes, 1, stage5Height, stage5BlockHash)
	tally := avcvotes.BlockTally{
		Signatures: map[string][]avcvotes.VoteRecord{peerYes.String(): {recYes}},
	}
	single := map[string]int8{
		peerYes.String():   1,
		peerGhost.String(): 1,
	}

	cert, err := buildVoteCertificate(tally, single)
	if err != nil {
		t.Fatalf("buildVoteCertificate: %v", err)
	}
	if cert == nil || len(cert.Signers) != 1 || cert.Signers[0] != peerYes.String() {
		t.Fatalf("expected exactly [peerYes] in the certificate, got %+v", cert)
	}
}

// The aggregate must actually verify against the real fast-aggregate-verify
// path — not just decode as hex. Confirms buildVoteCertificate produces
// genuinely usable BLS output, not a placeholder.
func TestBuildVoteCertificate_AggregateVerifiesWithFastAggregateVerify(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	peerA := stage5TestPeer(t)
	peerB := stage5TestPeer(t)

	recA := stage5SignedRecord(t, peerA, 1, stage5Height, stage5BlockHash)
	recB := stage5SignedRecord(t, peerB, 1, stage5Height, stage5BlockHash)

	tally := avcvotes.BlockTally{
		Signatures: map[string][]avcvotes.VoteRecord{
			peerA.String(): {recA},
			peerB.String(): {recB},
		},
	}
	single := map[string]int8{peerA.String(): 1, peerB.String(): 1}

	cert, err := buildVoteCertificate(tally, single)
	if err != nil {
		t.Fatalf("buildVoteCertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected a certificate")
	}

	aggSigBytes, err := hex.DecodeString(cert.AggSig)
	if err != nil {
		t.Fatalf("decode AggSig: %v", err)
	}
	pubA, err := hex.DecodeString(recA.BLSPubKeyHex)
	if err != nil {
		t.Fatalf("decode pubA: %v", err)
	}
	pubB, err := hex.DecodeString(recB.BLSPubKeyHex)
	if err != nil {
		t.Fatalf("decode pubB: %v", err)
	}

	// Same message both signed: stage5SignedRecord signs vote=1 for
	// (stage5ChainID, stage5Height, stage5BlockHash) via SignMessageForBlock,
	// so this reconstructs the identical v3 canonical bytes independently
	// rather than trusting that buildVoteCertificate got it right.
	msg, err := BLS_Signer.CanonicalVoteMessageV3(stage5ChainID, stage5Height, stage5BlockHash, 1)
	if err != nil {
		t.Fatalf("CanonicalVoteMessageV3: %v", err)
	}
	ok, err := blssign.BLSFastAggregateVerify([][]byte{pubA, pubB}, msg, aggSigBytes)
	if err != nil {
		t.Fatalf("BLSFastAggregateVerify: %v", err)
	}
	if !ok {
		t.Fatal("aggregate signature must verify against the two YES voters' pubkeys and the canonical v3 message")
	}
}
