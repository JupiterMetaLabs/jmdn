package Structs

import (
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// The tally counts ballots in the new domain and in the old format alike, so
// the order of restarts within the coordinated upgrade does not matter.
func TestVerifyTallySignatures_CountsBothBallotFormats(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	peerOld := stage5TestPeer(t)
	peerNew := stage5TestPeer(t)

	recOld := stage5SignedRecord(t, peerOld, 1, stage5Height, stage5BlockHash)
	resp, signed, err := BLS_Signer.SignBallotForBlock(1, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if err != nil || !signed {
		t.Fatalf("SignBallotForBlock: signed=%v err=%v", signed, err)
	}
	recNew := avcvotes.VoteRecord{
		PeerID: peerNew.String(), Vote: 1, BlockHash: stage5BlockHash, Height: stage5Height,
		BLSSignature: resp.Signature, BLSPubKeyHex: resp.PubKey,
	}

	tally := avcvotes.BlockTally{
		AuthorizedVotesByPeer: map[string][]int8{peerOld.String(): {1}, peerNew.String(): {1}},
		Signatures: map[string][]avcvotes.VoteRecord{
			peerOld.String(): {recOld},
			peerNew.String(): {recNew},
		},
	}
	verified, dropped := verifyTallySignatures(tally, stage5ChainID, stage5Height, stage5BlockHash, stage5ConsensusHash)
	if dropped != 0 {
		t.Fatalf("dropped %d valid ballots", dropped)
	}
	single := verified.SingleVotePeers()
	if single[peerOld.String()] != 1 || single[peerNew.String()] != 1 {
		t.Fatalf("a ballot was not counted: %+v", single)
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
