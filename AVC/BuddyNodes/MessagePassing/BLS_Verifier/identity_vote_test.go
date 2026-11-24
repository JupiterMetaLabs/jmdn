package BLS_Verifier

import (
	"encoding/hex"
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// A-6 on the live vote path: a vote carrying the identity key and the
// identity signature used to verify for ANY block, height and vote.
func TestVerifyForBlock_RejectsIdentityKeyAndSignature(t *testing.T) {
	resp := BLS_Signer.BLSresponse{
		PeerID:    "peer",
		PubKey:    hex.EncodeToString(make([]byte, 128)),
		Signature: hex.EncodeToString(make([]byte, 64)),
	}
	for _, vote := range []int8{1, -1} {
		if VerifyForBlock(resp, 7000700, 42, "0xabc", "0xdef", vote) == nil {
			t.Fatalf("identity key and signature verified a %+d vote", vote)
		}
	}
}
