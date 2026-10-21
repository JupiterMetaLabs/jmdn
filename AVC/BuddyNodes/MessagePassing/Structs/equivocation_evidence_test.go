package Structs

import (
	"strings"
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/ethereum/go-ethereum/common"
)

func signedRecord(t *testing.T, peerID string, vote int8, height uint64, hash, consensusHash string) avcvotes.VoteRecord {
	t.Helper()
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	resp, ok, err := BLS_Signer.SignMessageForBlock(vote, BLS_Signer.DomainChainID(), height, hash, consensusHash)
	if err != nil || !ok {
		t.Fatalf("sign: ok=%v err=%v", ok, err)
	}
	return avcvotes.VoteRecord{PeerID: peerID, Vote: vote, BlockHash: hash, Height: height,
		BLSSignature: resp.Signature, BLSPubKeyHex: resp.PubKey}
}

func TestSignedEquivocation(t *testing.T) {
	withFreshWatermark(t)
	victim := ingestTestPeer(t)
	height := uint64(4200)
	hash := common.HexToHash("0x4200").String()
	consensusHash := common.HexToHash("0xc045").String()

	// SignedEquivocation sources the ConsensusHash to verify against from
	// this node's own consensus-message cache, the same way
	// processVotesFromCRDT_v2 already does (D-26c) — seed it with the block
	// this test's votes are signed against.
	(&PubSubMessages.ConsensusMessage{ZKBlock: &config.ZKBlock{
		BlockNumber:   height,
		BlockHash:     common.HexToHash(hash),
		ConsensusHash: common.HexToHash(consensusHash),
	}}).SetGloalVarCacheConsensusMessage()

	t.Run("genuine double vote is confirmed", func(t *testing.T) {
		layer := newIngestLayer()
		yes := signedRecord(t, victim.String(), 1, height, hash, consensusHash)
		no := signedRecord(t, victim.String(), -1, height, hash, consensusHash)
		for _, r := range []avcvotes.VoteRecord{yes, no} {
			if err := avcvotes.AddVote(layer, victim, r); err != nil {
				t.Fatal(err)
			}
		}
		auth := map[string]string{victim.String(): strings.ToLower(yes.BLSPubKeyHex)}
		if !SignedEquivocation(layer, height, hash, victim.String(), auth) {
			t.Fatal("two validly signed values must count as equivocation")
		}
	})

	t.Run("forged record is not evidence", func(t *testing.T) {
		layer := newIngestLayer()
		yes := signedRecord(t, victim.String(), 1, height, hash, consensusHash)
		forged := yes
		forged.Vote = -1
		forged.BLSSignature = strings.Repeat("ab", len(yes.BLSSignature)/2)
		for _, r := range []avcvotes.VoteRecord{yes, forged} {
			if err := avcvotes.AddVote(layer, victim, r); err != nil {
				t.Fatal(err)
			}
		}
		auth := map[string]string{victim.String(): strings.ToLower(yes.BLSPubKeyHex)}
		tally, err := avcvotes.TallyBlock(layer, height, hash, auth)
		if err != nil {
			t.Fatal(err)
		}
		if len(avcvotes.DetectEquivocation(tally)) == 0 {
			t.Fatal("setup: the unverified tally should flag the victim")
		}
		if SignedEquivocation(layer, height, hash, victim.String(), auth) {
			t.Fatal("a junk signature must not make the victim an equivocator")
		}
	})
}
