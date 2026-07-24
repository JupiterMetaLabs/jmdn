package BLS_Signer

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	blssign "gossipnode/AVC/BLS/bls-sign"
	settings "gossipnode/config/settings"
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

// VoteDomainVersion tags the canonical block-bound vote-message FORMAT. It is
// part of the signed bytes, so bumping it invalidates every prior signature and
// MUST be done only as a coordinated network upgrade (P4).
//
//   - v1 (legacy): "zkvote:<blockhash>:<vote>"        — block-bound only.
//   - v2 (current): "zkvote:v2:chain=<id>:<blockhash>:<vote>" — adds chain-id
//     domain separation so a signature captured on chain A cannot be replayed
//     as a valid committee vote on chain B (fork / testnet↔mainnet).
//
// The chain id is the authenticated network id (settings.Network.ChainID); it
// is a per-node config constant, identical across honest nodes on a network,
// and is NOT taken from any attacker-supplied per-request field.
const VoteDomainVersion = "v2"

// CanonicalVoteMessage builds the EXACT bytes signed and verified for a v2
// block-bound vote. This is the single definition of the format: both
// SignMessageForBlock and BLS_Verifier.VerifyForBlock derive their bytes from
// here, so signer and verifier cannot drift. `bindings` uniquely identifies the
// block (block hash hex); it is normalized (lowercase + trim) to match the
// verifier regardless of case/whitespace.
func CanonicalVoteMessage(chainID uint64, bindings string, vote int8) ([]byte, error) {
	if vote != -1 && vote != 1 {
		return nil, fmt.Errorf("invalid vote: %d", vote)
	}
	bindings = normalizeBindings(bindings)
	if bindings == "" {
		return nil, fmt.Errorf("empty block bindings")
	}
	msg := BlockBoundVotePrefix + VoteDomainVersion + ":chain=" +
		strconv.FormatUint(chainID, 10) + ":" + bindings + ":" + strconv.Itoa(int(vote))
	return []byte(msg), nil
}

// DefaultDomainChainID is the fallback chain id used ONLY when settings have not
// been Load()ed yet (early init, or unit tests that never call Load()). It
// mirrors the compiled network default so signer and verifier agree even on the
// fallback path within a single process. Production always calls Load() before
// consensus, so the configured chain id is used there.
var DefaultDomainChainID = uint64(settings.DefaultConfig().Network.ChainID)

// DomainChainID returns the authenticated network chain id used for vote domain
// separation. Reading it through this single accessor guarantees the signer and
// every verifier in the process derive the identical value.
func DomainChainID() uint64 {
	if settings.IsLoaded() {
		return uint64(settings.Get().Network.ChainID)
	}
	return DefaultDomainChainID
}

// SignMessageForBlock signs a vote that is bound to a specific block AND to the
// network chain id (P4 / v2). `bindings` must uniquely identify the block (the
// receiver uses the block hash hex). This closes two replay gaps: the unbound
// constant "vote:1" could authorize any block (JMDN-001 / D3), and a v1
// block-bound signature could be replayed onto another chain/fork (P4).
func SignMessageForBlock(vote int8, chainID uint64, bindings string) (BLSresponse, bool, error) {
	msg, err := CanonicalVoteMessage(chainID, bindings, vote)
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	priv, pub, err := getBLSKeypair()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

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
