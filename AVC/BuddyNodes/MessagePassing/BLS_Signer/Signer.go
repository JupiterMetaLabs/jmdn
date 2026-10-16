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
// Default ON. Set JMDN_EMIT_BLOCK_BOUND_VOTES=0 to fall back to
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

// getBLSKeypair returns the node's persistent committee BLS keypair for signing
// votes. By default it LOADS the provisioned key and fails if absent — never
// auto-mints, because a freshly-minted key would not be in the committee
// snapshot and the node would silently self-exclude. Set JMDN_BLS_AUTOGEN=1
// for dev/first-boot to generate+persist one.
func getBLSKeypair() ([]byte, []byte, error) {
	blsOnce.Do(func() {
		if os.Getenv("JMDN_BLS_AUTOGEN") == "1" {
			blsPriv, blsPub, blsErr = blssign.GenerateBLSKeyPair()
		} else {
			blsPriv, blsPub, blsErr = blssign.LoadBLSKeyPair()
		}
	})
	return blsPriv, blsPub, blsErr
}

// LocalBLSKeypair returns this node's own persistent committee BLS keypair —
// the exact same cached material SignMessage/SignMessageForBlock already
// sign with, exposed for callers (e.g. messaging's timeout-vote wiring, M0
// §7.1c) that need to sign a DIFFERENT canonical message than the two vote
// domains this file hardcodes. This never mints a new key: it is the same
// getBLSKeypair() singleton, so a caller here and a block-vote caller always
// agree on this node's identity.
func LocalBLSKeypair() (priv, pub []byte, err error) {
	return getBLSKeypair()
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
// block-bound one (or vice-versa). Signer and verifier both derive the message
// from CanonicalVoteMessageV3, so they cannot drift.
const BlockBoundVotePrefix = "zkvote:"

// VoteDomainVersionV3 binds chain id, block HEIGHT, block hash and the block's
// ConsensusHash (the consensus-fields digest — Slot/Period/PrevAggCert/
// FeeRecipients/CommitteeSnapshotHash/tx contents):
// "zkvote:v3:chain=<id>:h=<height>:<blockhash>:ch=<consensushash>:<vote>".
// ConsensusHash is mandatory: checkConsensusBinding (messaging/consensus_hardening.go)
// already rejects any received non-genesis block whose ConsensusHash is empty,
// so every block a vote could legitimately be cast for already carries one.
// This used to be two formats (v3 block-hash-only, v4 +ConsensusHash) with the
// verifier falling back from v4 to v3; that fallback was the vulnerability — a
// forged ConsensusHash could never be detected by a vote that didn't bind it,
// as long as a verifier was still willing to accept the no-ConsensusHash
// format. There is exactly one format now, and the tag keeps the "v3" name so
// a stale binary emitting the old (pre-ConsensusHash) preimage produces a
// diagnosable verification failure rather than silently downgrading.
const VoteDomainVersionV3 = "v3"

// CanonicalVoteMessageV3 builds the EXACT bytes signed and verified for a
// block-bound vote: chain id, height, block hash, consensus hash and vote.
// Signer and verifier both derive their bytes from here so they cannot drift.
// consensusHash must be non-empty: a vote that does not commit to the
// consensus fields is not a valid vote (see the doc comment above).
func CanonicalVoteMessageV3(chainID, height uint64, bindings, consensusHash string, vote int8) ([]byte, error) {
	if vote != -1 && vote != 1 {
		return nil, fmt.Errorf("invalid vote: %d", vote)
	}
	bindings = normalizeBindings(bindings)
	if bindings == "" {
		return nil, fmt.Errorf("empty block bindings")
	}
	ch := normalizeBindings(consensusHash)
	if ch == "" {
		return nil, fmt.Errorf("empty consensus hash")
	}
	msg := BlockBoundVotePrefix + VoteDomainVersionV3 + ":chain=" +
		strconv.FormatUint(chainID, 10) + ":h=" + strconv.FormatUint(height, 10) +
		":" + bindings + ":ch=" + ch + ":" + strconv.Itoa(int(vote))
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

// SignMessageForBlock signs a vote bound to the network chain id, the block
// HEIGHT, the block hash and the block's ConsensusHash. `bindings` must
// uniquely identify the block (the receiver uses the block hash hex). This
// ties a vote to a single block on a single chain/fork at a single height,
// unlike the unbound constant "vote:1" used by SignMessage. consensusHash must
// be non-empty — see CanonicalVoteMessageV3's doc comment for why.
func SignMessageForBlock(vote int8, chainID, height uint64, bindings, consensusHash string) (BLSresponse, bool, error) {
	msg, err := CanonicalVoteMessageV3(chainID, height, bindings, consensusHash, vote)
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
