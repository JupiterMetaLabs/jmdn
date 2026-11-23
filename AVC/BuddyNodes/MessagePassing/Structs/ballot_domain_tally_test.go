package Structs

import (
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// The tally counts ballots in the ballot domain and drops a record whose
// signature is in the vote-result format.
func TestVerifyTallySignatures_DropsVoteResultFormat(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	peerBallot := stage5TestPeer(t)
	peerResult := stage5TestPeer(t)

	recBallot := stage5SignedRecord(t, peerBallot, 1, stage5Height, stage5BlockHash)
	resp, signed, err := BLS_Signer.SignMessageForBlock(1, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if err != nil || !signed {
		t.Fatalf("SignMessageForBlock: signed=%v err=%v", signed, err)
	}
	recResult := avcvotes.VoteRecord{
		PeerID: peerResult.String(), Vote: 1, BlockHash: stage5BlockHash, Height: stage5Height,
		BLSSignature: resp.Signature, BLSPubKeyHex: resp.PubKey,
	}

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{peerBallot.String(): {1}, peerResult.String(): {1}},
		Signatures: map[string][]avcvotes.VoteRecord{
			peerBallot.String(): {recBallot},
			peerResult.String(): {recResult},
		},
	}
	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if dropped != 1 {
		t.Fatalf("want the vote-result record dropped (1), dropped %d", dropped)
	}
	single := verified.SingleVotePeers()
	if single[peerBallot.String()] != 1 {
		t.Fatalf("the ballot was not counted: %+v", single)
	}
	if _, ok := single[peerResult.String()]; ok {
		t.Fatalf("a vote-result signature was counted as a ballot: %+v", single)
	}
}

// verifyVoteRecord (CRDT merge check and shadowed-vote rescue) accepts a
// ballot in the new domain.
func TestVerifyVoteRecord_AcceptsBallotDomain(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	p := stage5TestPeer(t)
	resp, signed, err := BLS_Signer.SignBallotForBlock(1, BLS_Signer.DomainChainID(), stage5Height, stage5BlockHash, stage5ConsensusHash)
	if err != nil || !signed {
		t.Fatalf("SignBallotForBlock: signed=%v err=%v", signed, err)
	}
	rec := avcvotes.VoteRecord{
		PeerID: p.String(), Vote: 1, BlockHash: stage5BlockHash, Height: stage5Height,
		BLSSignature: resp.Signature, BLSPubKeyHex: resp.PubKey,
	}
	authorized := map[string]string{p.String(): resp.PubKey}
	if !verifyVoteRecord(rec, stage5Height, stage5BlockHash, stage5ConsensusHash, authorized) {
		t.Fatal("new-domain ballot rejected by verifyVoteRecord")
	}
	rec.Vote = -1
	if verifyVoteRecord(rec, stage5Height, stage5BlockHash, stage5ConsensusHash, authorized) {
		t.Fatal("+1 ballot accepted as -1")
	}
}
