package messaging

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// C1 (2026-10-22 audit): a certificate built from ONE attacker key must not
// reach quorum. For every seated member P the attacker sends the same
// signature S twice — once under P's bound key, once under its own. Before
// voteKey carried the public key, the second entry overwrote the first in
// TallyAgainst's lookup map, so the first verified under the attacker's key
// and passed Tally's binding check under P's: 7 of 7 YES with no member key.
func TestTallyAgainst_OneAttackerKeyCannotForgeQuorum(t *testing.T) {
	const height = uint64(500)
	blockHashHex := "0x" + hex.EncodeToString(make([]byte, 32))

	seated := make([]committee.Member, 7)
	for i := range seated {
		_, pubHex := testBLSKey(t, fmt.Sprintf("c1-member-%d", i))
		pub, _ := hex.DecodeString(pubHex)
		seated[i] = committee.Member{PeerID: fmt.Sprintf("member-%d", i), BLSPub: pub, Weight: 1}
	}

	attPriv, attPubHex := testBLSKey(t, "c1-attacker")
	msg, err := BLS_Signer.CanonicalVoteMessageV3(BLS_Signer.DomainChainID(), height, blockHashHex, testBlockConsensusHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := blssign.BLSSign(attPriv, msg)
	if err != nil {
		t.Fatal(err)
	}
	sigHex := hex.EncodeToString(sig)

	var forged []BLS_Signer.BLSresponse
	for _, m := range seated {
		forged = append(forged,
			BLS_Signer.BLSresponse{PeerID: m.PeerID, PubKey: hex.EncodeToString(m.BLSPub), Signature: sigHex, Agree: true},
			BLS_Signer.BLSresponse{PeerID: m.PeerID, PubKey: attPubHex, Signature: sigHex, Agree: true},
		)
	}

	res, err := TallyAgainst(forged, seated, blockHashHex, testBlockConsensusHash, height)
	if err != nil {
		t.Fatal(err)
	}
	if res.YesVotes != 0 || res.Reached {
		t.Fatalf("one attacker key forged %d YES votes (reached=%v); want 0", res.YesVotes, res.Reached)
	}
}

// The fix must not cost honest certificates anything: every member signing
// with its own key still reaches quorum.
func TestTallyAgainst_HonestCertificateStillReachesQuorum(t *testing.T) {
	const height = uint64(500)
	blockHashHex := "0x" + hex.EncodeToString(make([]byte, 32))
	msg, err := BLS_Signer.CanonicalVoteMessageV3(BLS_Signer.DomainChainID(), height, blockHashHex, testBlockConsensusHash, 1)
	if err != nil {
		t.Fatal(err)
	}

	seated := make([]committee.Member, 7)
	var responses []BLS_Signer.BLSresponse
	for i := range seated {
		priv, pubHex := testBLSKey(t, fmt.Sprintf("c1-honest-%d", i))
		pub, _ := hex.DecodeString(pubHex)
		seated[i] = committee.Member{PeerID: fmt.Sprintf("member-%d", i), BLSPub: pub, Weight: 1}
		sig, err := blssign.BLSSign(priv, msg)
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, BLS_Signer.BLSresponse{
			PeerID: seated[i].PeerID, PubKey: pubHex, Signature: hex.EncodeToString(sig), Agree: true,
		})
	}

	res, err := TallyAgainst(responses, seated, blockHashHex, testBlockConsensusHash, height)
	if err != nil {
		t.Fatal(err)
	}
	if res.YesVotes != 7 || !res.Reached {
		t.Fatalf("honest certificate: %d YES (reached=%v); want 7 and reached", res.YesVotes, res.Reached)
	}
}
