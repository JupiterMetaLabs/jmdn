package Structs

import (
	"encoding/json"
	"strings"
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"

	avcdatalayer "github.com/JupiterMetaLabs/avc/buddynodes/datalayer"
	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
)

// A-3 fixtures: the block every record below is signed for, cached the way a
// buddy that has seen the proposal would have it.
func a3Block(t *testing.T) (height uint64, hash, consensusHash string) {
	t.Helper()
	withFreshWatermark(t)
	height = 4300
	hash = common.HexToHash("0x4300").String()
	consensusHash = common.HexToHash("0xc043").String()
	(&PubSubMessages.ConsensusMessage{ZKBlock: &config.ZKBlock{
		BlockNumber:   height,
		BlockHash:     common.HexToHash(hash),
		ConsensusHash: common.HexToHash(consensusHash),
	}}).SetGloalVarCacheConsensusMessage()
	return height, hash, consensusHash
}

// mergeJunk stores a signature record for victim the way a Byzantine buddy's
// sync merge would: written by another peer, with no identity check.
func mergeJunk(t *testing.T, layer *avctypes.Controller, relay peer.ID, rec avcvotes.VoteRecord) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := avcdatalayer.Add(layer, relay, avcvotes.BlockSigKey(rec.Height, rec.BlockHash), rec.PeerID+":"+string(b)); err != nil {
		t.Fatal(err)
	}
}

// tallyAndRescue runs the buddy's v2 tally steps: TallyBlock, signature
// verification, then the A-3 rescue.
func tallyAndRescue(t *testing.T, layer *avctypes.Controller, height uint64, hash, consensusHash string, auth map[string]string) (before, after avcvotes.BlockTally) {
	t.Helper()
	tally, err := avcvotes.TallyBlock(layer, height, hash, auth)
	if err != nil {
		t.Fatal(err)
	}
	verified, _ := verifyTallySignatures(tally, BLS_Signer.DomainChainID(), height, hash, consensusHash)
	before = verified
	before.AuthorizedVotesByPeer = copyVotes(verified.AuthorizedVotesByPeer)
	rescueShadowedVotes(layer, &verified, height, hash, consensusHash, auth, "")
	return before, verified
}

func copyVotes(m map[string][]int8) map[string][]int8 {
	out := make(map[string][]int8, len(m))
	for k, v := range m {
		out[k] = append([]int8(nil), v...)
	}
	return out
}

func TestRescue_JunkSignatureCannotHideAGenuineVote(t *testing.T) {
	height, hash, ch := a3Block(t)
	victim, relay := ingestTestPeer(t), ingestTestPeer(t)
	layer := newIngestLayer()
	genuine := signedRecord(t, victim.String(), 1, height, hash, ch)
	if err := avcvotes.AddVote(layer, victim, genuine); err != nil {
		t.Fatal(err)
	}
	junk := genuine
	junk.BLSSignature = "" // sorts before any hex signature
	mergeJunk(t, layer, relay, junk)
	auth := map[string]string{victim.String(): strings.ToLower(genuine.BLSPubKeyHex)}

	before, after := tallyAndRescue(t, layer, height, hash, ch, auth)
	if len(before.AuthorizedVotesByPeer[victim.String()]) != 0 {
		t.Fatal("setup: the junk record should have hidden the genuine vote before the rescue")
	}
	if got := after.AuthorizedVotesByPeer[victim.String()]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("genuine vote not recovered: %v", got)
	}
	if after.Signatures[victim.String()][0].BLSSignature != genuine.BLSSignature {
		t.Fatal("the recovered pair must carry the genuine signature")
	}
}

// A junk record with a foreign key is dropped by TallyBlock's key check, before
// signature verification ever sees the pair. The rescue must still find the
// genuine record.
func TestRescue_JunkKeyCannotHideAGenuineVote(t *testing.T) {
	height, hash, ch := a3Block(t)
	victim, relay := ingestTestPeer(t), ingestTestPeer(t)
	layer := newIngestLayer()
	genuine := signedRecord(t, victim.String(), -1, height, hash, ch)
	if err := avcvotes.AddVote(layer, victim, genuine); err != nil {
		t.Fatal(err)
	}
	junk := genuine
	junk.BLSSignature = ""
	junk.BLSPubKeyHex = "00"
	mergeJunk(t, layer, relay, junk)
	auth := map[string]string{victim.String(): strings.ToLower(genuine.BLSPubKeyHex)}

	_, after := tallyAndRescue(t, layer, height, hash, ch, auth)
	if got := after.AuthorizedVotesByPeer[victim.String()]; len(got) != 1 || got[0] != -1 {
		t.Fatalf("genuine NO vote not recovered: %v", got)
	}
}

func TestRescue_NoGenuineRecordRecoversNothing(t *testing.T) {
	height, hash, ch := a3Block(t)
	victim, relay := ingestTestPeer(t), ingestTestPeer(t)
	layer := newIngestLayer()
	genuine := signedRecord(t, victim.String(), 1, height, hash, ch)
	forged := genuine
	forged.BLSSignature = strings.Repeat("ab", len(genuine.BLSSignature)/2)
	if err := avcvotes.AddVote(layer, victim, forged); err != nil {
		t.Fatal(err)
	}
	junk := genuine
	junk.BLSSignature = ""
	mergeJunk(t, layer, relay, junk)
	auth := map[string]string{victim.String(): strings.ToLower(genuine.BLSPubKeyHex)}

	_, after := tallyAndRescue(t, layer, height, hash, ch, auth)
	if got := after.AuthorizedVotesByPeer[victim.String()]; len(got) != 0 {
		t.Fatalf("a vote with no genuine signature must not count: %v", got)
	}
}

func TestSignedEquivocation_JunkCannotHideIt(t *testing.T) {
	height, hash, ch := a3Block(t)
	victim, relay := ingestTestPeer(t), ingestTestPeer(t)
	layer := newIngestLayer()
	yes := signedRecord(t, victim.String(), 1, height, hash, ch)
	no := signedRecord(t, victim.String(), -1, height, hash, ch)
	for _, r := range []avcvotes.VoteRecord{yes, no} {
		if err := avcvotes.AddVote(layer, victim, r); err != nil {
			t.Fatal(err)
		}
	}
	junk := no
	junk.BLSSignature = ""
	mergeJunk(t, layer, relay, junk)
	auth := map[string]string{victim.String(): strings.ToLower(yes.BLSPubKeyHex)}

	if !SignedEquivocation(layer, height, hash, victim.String(), auth) {
		t.Fatal("a junk record ahead of one value must not hide a genuine equivocation")
	}
}

func TestSigMergeVerifier(t *testing.T) {
	height, hash, ch := a3Block(t)
	victim := ingestTestPeer(t)
	genuine := signedRecord(t, victim.String(), 1, height, hash, ch)
	auth := map[string]string{victim.String(): strings.ToLower(genuine.BLSPubKeyHex)}
	saved := authorizedVotersForHeightFn
	authorizedVotersForHeightFn = func(uint64) (map[string]string, error) { return auth, nil }
	t.Cleanup(func() { authorizedVotersForHeightFn = saved })

	m, err := NewSigMergeVerifier(avcvotes.BlockSigKey(height, hash))
	if err != nil {
		t.Fatal(err)
	}
	elem := func(r avcvotes.VoteRecord) string {
		b, _ := json.Marshal(r)
		return r.PeerID + ":" + string(b)
	}
	if !m.Verify(elem(genuine)) {
		t.Fatal("a genuine record must be accepted")
	}
	badSig := genuine
	badSig.BLSSignature = ""
	badKey := genuine
	badKey.BLSPubKeyHex = "00"
	otherHeight := genuine
	otherHeight.Height = height + 1
	for name, r := range map[string]avcvotes.VoteRecord{"bad signature": badSig, "foreign key": badKey, "wrong height": otherHeight} {
		if m.Verify(elem(r)) {
			t.Fatalf("%s: a record that would not verify at tally was accepted", name)
		}
	}
	if m.Verify("not-a-record") {
		t.Fatal("an unparseable element was accepted")
	}

	if _, err := NewSigMergeVerifier(avcvotes.BlockVoteKey(height, hash)); err == nil {
		t.Fatal("a votes: key must not get a signature verifier")
	}
	if _, err := NewSigMergeVerifier(avcvotes.BlockSigKey(height, common.HexToHash("0xdead").String())); err == nil {
		t.Fatal("a block this node has not seen cannot be verified against")
	}
}
