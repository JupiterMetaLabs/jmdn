package BLS_Signer

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	blssign "gossipnode/AVC/BLS/bls-sign"
)

// EmitBlockBoundVotes controls whether buddies sign block-bound votes
// (SignMessageForBlock) rather than legacy unbound votes (SignMessage).
// Default ON (JMDN-001 / D3). Set JMDN_EMIT_BLOCK_BOUND_VOTES=0 to fall back to
// legacy emission during a staged rollout.
var EmitBlockBoundVotes = os.Getenv("JMDN_EMIT_BLOCK_BOUND_VOTES") != "0"

// normalizeBindings canonicalizes a block-hash binding string so the signer and
// verifier agree regardless of case/whitespace differences.
func normalizeBindings(b string) string { return strings.ToLower(strings.TrimSpace(b)) }

type BLSresponse struct {
	Signature        string
	Agree            bool
	PubKey           string
	PeerID           string
	RejectionReasons map[string]string // peerID → reason, populated when Agree=false
}

// cached BLS keypair (per-process)
var (
	blsOnce sync.Once
	blsPriv []byte
	blsPub  []byte
	blsErr  error
)

func getBLSKeypair() ([]byte, []byte, error) {
	blsOnce.Do(func() {
		blsPriv, blsPub, blsErr = blssign.GenerateBLSKeyPair()
	})
	return blsPriv, blsPub, blsErr
}

// Service functions for the BLSresponse struct
// Signs canonical message "vote:<v>" for BOTH 1 and -1
func SignMessage(vote int8) (BLSresponse, bool, error) {
	if vote != -1 && vote != 1 {
		return *NewBLSresponseBuilder(nil), false, fmt.Errorf("invalid vote")
	}

	priv, pub, err := getBLSKeypair()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	msg := []byte("vote:" + strconv.Itoa(int(vote)))
	sig, err := blssign.BLSSign(priv, msg)
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	pubHex := hex.EncodeToString(pub)

	return *NewBLSresponseBuilder(nil).
		SetSignature(hex.EncodeToString(sig)).
		SetAgree(vote == 1).
		SetPubKey(pubHex).
		Build(), true, nil
}

// BlockBoundVotePrefix is the domain-separation prefix for a vote that is
// cryptographically bound to a specific block. It differs from the legacy
// "vote:" prefix so a legacy signature can never be reinterpreted as a
// block-bound one (or vice-versa). The verifier
// (BLS_Verifier.CanonicalBlockVoteMessage) MUST build the identical message.
const BlockBoundVotePrefix = "zkvote:"

// SignMessageForBlock signs a vote that is bound to a specific block. `bindings`
// must uniquely identify the block (the receiver uses the block hash hex). This
// closes the replay gap where a signature over the unbound constant "vote:1"
// could be reused to authorize any block (JMDN-001 / D3).
func SignMessageForBlock(vote int8, bindings string) (BLSresponse, bool, error) {
	if vote != -1 && vote != 1 {
		return *NewBLSresponseBuilder(nil), false, fmt.Errorf("invalid vote")
	}
	bindings = normalizeBindings(bindings)
	if bindings == "" {
		return *NewBLSresponseBuilder(nil), false, fmt.Errorf("empty block bindings")
	}

	priv, pub, err := getBLSKeypair()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	msg := []byte(BlockBoundVotePrefix + bindings + ":" + strconv.Itoa(int(vote)))
	sig, err := blssign.BLSSign(priv, msg)
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	return *NewBLSresponseBuilder(nil).
		SetSignature(hex.EncodeToString(sig)).
		SetAgree(vote == 1).
		SetPubKey(hex.EncodeToString(pub)).
		Build(), true, nil
}

// failed to create BLS signer from seed: while unmarshaling scalar: UnmarshalBinary: value out of range
