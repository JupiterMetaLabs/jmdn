package BLS_Verifier

import (
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// Ballots are signed in their own domain (zkballot:v1) so a ballot collected
// from gossip never verifies as a certificate signature.

func signBallot(t *testing.T, vote int8) BLS_Signer.BLSresponse {
	t.Helper()
	resp, ok, err := BLS_Signer.SignBallotForBlock(vote, chainA, 100, blkHash, consHash)
	if err != nil || !ok {
		t.Fatalf("sign ballot: ok=%v err=%v", ok, err)
	}
	return resp
}

func TestBallotDomain_BallotIsNotACertificateSignature(t *testing.T) {
	ballot := signBallot(t, 1)
	if err := VerifyBallotForBlock(ballot, chainA, 100, blkHash, consHash, 1); err != nil {
		t.Fatalf("ballot should verify as a ballot: %v", err)
	}
	if err := VerifyForBlock(ballot, chainA, 100, blkHash, consHash, 1); err == nil {
		t.Fatal("a ballot verified as a certificate signature")
	}
}

// Ballots signed in the old format, by nodes that have not switched yet, still
// count as ballots.
func TestBallotDomain_OldFormatBallotStillCounts(t *testing.T) {
	old, ok, err := BLS_Signer.SignMessageForBlock(1, chainA, 100, blkHash, consHash)
	if err != nil || !ok {
		t.Fatalf("sign old format: ok=%v err=%v", ok, err)
	}
	if err := VerifyBallotForBlock(old, chainA, 100, blkHash, consHash, 1); err != nil {
		t.Fatalf("old-format ballot should still count: %v", err)
	}
	if err := VerifyBallotForBlock(old, chainA, 100, blkHash, consHash, -1); err == nil {
		t.Fatal("old-format +1 ballot verified as -1")
	}
}

func TestBallotDomain_FieldBinding(t *testing.T) {
	ballot := signBallot(t, 1)
	otherHash := "0x2222222222222222222222222222222222222222222222222222222222222222"
	cases := []struct {
		name    string
		chain   uint64
		height  uint64
		block   string
		consens string
		vote    int8
	}{
		{"other chain", chainB, 100, blkHash, consHash, 1},
		{"other height", chainA, 101, blkHash, consHash, 1},
		{"other block", chainA, 100, otherHash, consHash, 1},
		{"other consensus hash", chainA, 100, blkHash, consHashB, 1},
		{"other vote", chainA, 100, blkHash, consHash, -1},
	}
	for _, c := range cases {
		if err := VerifyBallotForBlock(ballot, c.chain, c.height, c.block, c.consens, c.vote); err == nil {
			t.Errorf("%s: ballot verified for a different field", c.name)
		}
	}
}

func TestBallotDomain_MessageLayout(t *testing.T) {
	base, err := BLS_Signer.CanonicalVoteMessageV3(chainA, 100, blkHash, consHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	ballot, err := BLS_Signer.CanonicalBallotMessageV1(chainA, 100, blkHash, consHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := BLS_Signer.BallotDomainV1 + string(base); string(ballot) != want {
		t.Fatalf("ballot message = %q, want %q", ballot, want)
	}
	if _, err := BLS_Signer.CanonicalBallotMessageV1(chainA, 100, blkHash, "", 1); err == nil {
		t.Error("empty consensus hash accepted")
	}
	if _, err := BLS_Signer.CanonicalBallotMessageV1(chainA, 100, blkHash, consHash, 0); err == nil {
		t.Error("vote 0 accepted")
	}
	if _, _, err := BLS_Signer.SignBallotForBlock(1, chainA, 100, "", consHash); err == nil {
		t.Error("empty block hash signed")
	}
}
