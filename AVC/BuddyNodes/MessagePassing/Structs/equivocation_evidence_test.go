package Structs

import (
	"strings"
	"testing"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

func signedRecord(t *testing.T, peerID string, vote int8, height uint64, hash string) avcvotes.VoteRecord {
	t.Helper()
	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	resp, ok, err := BLS_Signer.SignMessageForBlock(vote, BLS_Signer.DomainChainID(), height, hash, "")
	if err != nil || !ok {
		t.Fatalf("sign: ok=%v err=%v", ok, err)
	}
	return avcvotes.VoteRecord{PeerID: peerID, Vote: vote, BlockHash: hash, Height: height,
		BLSSignature: resp.Signature, BLSPubKeyHex: resp.PubKey}
}

func TestSignedEquivocation(t *testing.T) {
	withFreshWatermark(t)
	victim := ingestTestPeer(t)
	const height, hash = uint64(4200), "0xequiv"

	t.Run("genuine double vote is confirmed", func(t *testing.T) {
		layer := newIngestLayer()
		yes := signedRecord(t, victim.String(), 1, height, hash)
		no := signedRecord(t, victim.String(), -1, height, hash)
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
		yes := signedRecord(t, victim.String(), 1, height, hash)
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
