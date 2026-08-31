// Package committee holds jmdn's byte-for-byte mirrors of the seedNodes
// committee-source crypto contracts (repo seedNodes, branch JMNS). Every
// canonical byte string here MUST match the seed exactly — the seed signs /
// verifies the identical bytes, so any divergence silently breaks authentication.
//
// Cross-repo contract sources (seedNodes/pkg/peer):
//   - bls_pop.go            -> PoPChallenge / VerifyBLSProofOfPossession
//   - committee_snapshot.go -> canonicalCommitteeBytes / VerifyCommitteeSnapshot
//   - sequencer_auth.go     -> SequencerAuthChallenge / SignSequencerRequest
//
// BLS is dela/bls (BN256), the SAME library+version jmdn uses for votes
// (go.dedis.ch/dela v0.2.0). We reuse AVC/BLS/bls-sign so the serialization is
// identical to jmdn's vote path and the seed's authority path.
//
// This package is pure and additive: nothing in the live path calls it yet. The
// registration/snapshot/selection wiring (which is a breaking wire change) is a
// separate, confirmation-gated phase.
package committee

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	blssign "gossipnode/AVC/BLS/bls-sign"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ---- Versioned domain separators (must match seedNodes exactly) --------------

const (
	// PoPChallengeVersion — seedNodes/pkg/peer/bls_pop.go.
	PoPChallengeVersion = "jmdt/bls-pop/v1"
	// CommitteeSnapshotVersion — seedNodes/pkg/peer/committee_snapshot.go.
	// v2 (staking rewards, R1): committee entries gained reward_address. The
	// canonical bytes changed, so v1 and v2 signatures are NOT interchangeable —
	// a v2 seed and a v1 jmdn reject each other outright (the intended failure
	// mode). MUST match seedNodes pkg/peer/committee_snapshot.go byte-for-byte.
	CommitteeSnapshotVersion = "jmdt/committee/v2"
	// SeqAuthVersion — seedNodes/pkg/peer/sequencer_auth.go.
	SeqAuthVersion = "jmdt/seed-auth/v1"

	// gRPC metadata keys for sequencer-signed selection requests.
	SeqAuthTimestampHeader = "x-seed-auth-timestamp"
	SeqAuthSignatureHeader = "x-seed-auth-signature"

	// DefaultSeqAuthMaxSkew bounds how stale a sequencer request may be (seed default ±30s).
	DefaultSeqAuthMaxSkew = 30 * time.Second
	// DefaultCommitteeEpochSeconds is the shared epoch clock divisor (seed default).
	DefaultCommitteeEpochSeconds int64 = 3600
)

// ---- Proof of possession (bls_pop.go) ----------------------------------------

// PoPChallenge returns the exact bytes a peer's BLS key must sign to prove
// possession, bound to its peer_id and the lowercase-normalized bls_pub under a
// versioned domain separator. MUST match seedNodes PoPChallenge byte-for-byte.
func PoPChallenge(peerID, blsPubHex string) []byte {
	return []byte(PoPChallengeVersion + "|" + peerID + "|" + strings.ToLower(strings.TrimSpace(blsPubHex)))
}

// ProveBLSPossession produces the (lowercase-hex bls_pub, hex bls_pop) a node
// sends at registration: bls_pop is the dela/bls signature by blsPriv over
// PoPChallenge(peerID, bls_pub). Uses jmdn's BLS lib so bytes match votes+seed.
func ProveBLSPossession(peerID string, blsPriv, blsPub []byte) (blsPubHex, blsPopHex string, err error) {
	if len(blsPub) == 0 {
		return "", "", fmt.Errorf("empty bls public key")
	}
	blsPubHex = strings.ToLower(hex.EncodeToString(blsPub))
	sig, err := blssign.BLSSign(blsPriv, PoPChallenge(peerID, blsPubHex))
	if err != nil {
		return "", "", fmt.Errorf("bls proof-of-possession sign: %w", err)
	}
	return blsPubHex, hex.EncodeToString(sig), nil
}

// VerifyBLSProofOfPossession mirrors the seed: verifies blsPopHex is a valid
// dela/bls signature by blsPubHex over PoPChallenge(peerID, blsPub). Fail-closed.
func VerifyBLSProofOfPossession(peerID, blsPubHex, blsPopHex string) error {
	blsPubHex = strings.ToLower(strings.TrimSpace(blsPubHex))
	if blsPubHex == "" {
		return fmt.Errorf("empty bls_pub")
	}
	if strings.TrimSpace(blsPopHex) == "" {
		return fmt.Errorf("empty bls_pop (proof of possession)")
	}
	pubBytes, err := hex.DecodeString(blsPubHex)
	if err != nil {
		return fmt.Errorf("invalid bls_pub hex: %w", err)
	}
	popBytes, err := hex.DecodeString(strings.TrimSpace(blsPopHex))
	if err != nil {
		return fmt.Errorf("invalid bls_pop hex: %w", err)
	}
	if err := blssign.BLSVerify(pubBytes, PoPChallenge(peerID, blsPubHex), popBytes); err != nil {
		return fmt.Errorf("bls proof-of-possession verification failed: %w", err)
	}
	return nil
}

// ---- Committee snapshot (committee_snapshot.go) ------------------------------

// CommitteeEntry is one eligible validator (peer_id + committee BLS key).
// JSON tags match the seed's wire/JSON shape.
type CommitteeEntry struct {
	PeerID string `json:"peer_id"`
	BLSPub string `json:"bls_pub"` // lowercase hex
	// RewardAddress is the peer's bound operator wallet (lowercase "0x" + 40 hex),
	// or "" when the peer has bound none. Empty is a legitimate value carried
	// verbatim into the canonical bytes (three colon-separated fields ALWAYS —
	// an unset address is a trailing colon, never dropped, never placeholdered).
	// jmdn applies its own fallback for a signer with no address (omit from the
	// fee split). Must match seedNodes CommitteeEntry.RewardAddress.
	RewardAddress string `json:"reward_address"`
}

// CommitteeSnapshot mirrors the seed's authenticated, epoch-pinned eligible set.
type CommitteeSnapshot struct {
	Epoch           uint64           `json:"epoch"`
	Entries         []CommitteeEntry `json:"entries"`
	Seed            string           `json:"seed,omitempty"`
	AuthorityPubHex string           `json:"authority_pubkey"`
	Signature       string           `json:"signature"`
}

// sortedEntries orders by peer_id with bls_pub lowercased+trimmed — the exact
// canonical order the seed signs and compares.
func sortedEntries(entries []CommitteeEntry) []CommitteeEntry {
	out := make([]CommitteeEntry, len(entries))
	for i, e := range entries {
		out[i] = CommitteeEntry{
			PeerID:        e.PeerID,
			BLSPub:        strings.ToLower(strings.TrimSpace(e.BLSPub)),
			RewardAddress: strings.ToLower(strings.TrimSpace(e.RewardAddress)),
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out
}

// CanonicalCommitteeBytes reconstructs the authority-signed bytes:
// version|epoch|seed|<peer_id:bls_pub>,... (entries sorted by peer_id).
// MUST match seedNodes canonicalCommitteeBytes byte-for-byte.
func CanonicalCommitteeBytes(epoch uint64, seed string, entries []CommitteeEntry) []byte {
	parts := make([]string, 0, len(entries))
	for _, e := range sortedEntries(entries) {
		// Three colon-separated fields ALWAYS; an unset reward_address is a
		// trailing colon, never dropped. Must match seedNodes byte-for-byte.
		parts = append(parts, e.PeerID+":"+e.BLSPub+":"+e.RewardAddress)
	}
	return []byte(CommitteeSnapshotVersion + "|" + strconv.FormatUint(epoch, 10) + "|" + seed + "|" + strings.Join(parts, ","))
}

// VerifyCommitteeSnapshot verifies the authority signature over the snapshot's
// canonical bytes. When expectedAuthorityPubHex is set (the PINNED, OOB-
// distributed authority) the snapshot's authority key MUST equal it. Fail-closed
// on nil/empty. Mirrors seedNodes VerifyCommitteeSnapshot.
func VerifyCommitteeSnapshot(snap *CommitteeSnapshot, expectedAuthorityPubHex string) error {
	if snap == nil {
		return fmt.Errorf("nil committee snapshot")
	}
	if len(snap.Entries) == 0 {
		return fmt.Errorf("empty committee snapshot (fail closed: no validators)")
	}
	if exp := strings.ToLower(strings.TrimSpace(expectedAuthorityPubHex)); exp != "" {
		if strings.ToLower(strings.TrimSpace(snap.AuthorityPubHex)) != exp {
			return fmt.Errorf("committee snapshot not signed by the pinned authority key")
		}
	}
	pubBytes, err := hex.DecodeString(strings.TrimSpace(snap.AuthorityPubHex))
	if err != nil {
		return fmt.Errorf("bad authority pubkey hex: %w", err)
	}
	sigBytes, err := hex.DecodeString(strings.TrimSpace(snap.Signature))
	if err != nil {
		return fmt.Errorf("bad snapshot signature hex: %w", err)
	}
	if err := blssign.BLSVerify(pubBytes, CanonicalCommitteeBytes(snap.Epoch, snap.Seed, snap.Entries), sigBytes); err != nil {
		return fmt.Errorf("committee snapshot signature invalid: %w", err)
	}
	return nil
}

// PeerIDSet returns the snapshot's eligible peer_id set — the value the
// eligibility source (messaging.SetCommitteeEligibilitySource) consumes.
// Call only on a snapshot that VerifyCommitteeSnapshot accepted.
func (snap *CommitteeSnapshot) PeerIDSet() map[string]struct{} {
	set := make(map[string]struct{}, len(snap.Entries))
	for _, e := range snap.Entries {
		set[e.PeerID] = struct{}{}
	}
	return set
}

// BLSPubByPeer maps peer_id -> lowercase bls_pub hex from the snapshot, the
// authenticated peer_id->bls_pub binding used to authenticate votes.
func (snap *CommitteeSnapshot) BLSPubByPeer() map[string]string {
	m := make(map[string]string, len(snap.Entries))
	for _, e := range snap.Entries {
		m[e.PeerID] = strings.ToLower(strings.TrimSpace(e.BLSPub))
	}
	return m
}

// RewardAddrByPeer maps peer_id -> lowercase reward-address hex from the
// snapshot — the AUTHENTICATED buddy->wallet binding the fee split is derived
// from. A peer with no bound address is OMITTED from the map (its "" is not a
// destination); the fee-split builder treats an absent entry as "no reward,
// redistribute to address-having buddies". Call only on a snapshot that
// VerifyCommitteeSnapshot accepted. Must stay consistent with the canonical
// bytes' normalization (lowercase+trim).
func (snap *CommitteeSnapshot) RewardAddrByPeer() map[string]string {
	m := make(map[string]string, len(snap.Entries))
	for _, e := range snap.Entries {
		addr := strings.ToLower(strings.TrimSpace(e.RewardAddress))
		if addr == "" {
			continue
		}
		m[e.PeerID] = addr
	}
	return m
}

// ---- Epoch clock -------------------------------------------------------------

// EpochForTime returns unix/epochSeconds (shared clock, seed default 3600).
// epochSeconds <= 0 uses DefaultCommitteeEpochSeconds.
func EpochForTime(unix int64, epochSeconds int64) uint64 {
	if epochSeconds <= 0 {
		epochSeconds = DefaultCommitteeEpochSeconds
	}
	if unix < 0 {
		return 0
	}
	return uint64(unix / epochSeconds)
}

// EpochFreshnessWindow is how many epochs of lag/skew are tolerated (an epoch
// boundary where the seed still serves the previous snapshot, plus clock skew)
// before a validly-signed snapshot is treated as stale.
const EpochFreshnessWindow = 1

// CheckSnapshotEpochFresh rejects a validly-signed but STALE committee snapshot:
// its epoch must be within EpochFreshnessWindow of the current epoch
// (unix/epochSeconds). Without this, an old but authority-signed snapshot — an
// epoch with a pre-rotation/pre-revocation committee — could be re-presented,
// defeating rotation/revocation. The GetCommitteeSnapshot read is
// unauthenticated, so freshness must be enforced by the consumer.
func CheckSnapshotEpochFresh(snapEpoch uint64, nowUnix, epochSeconds int64) error {
	cur := EpochForTime(nowUnix, epochSeconds)
	d := int64(snapEpoch) - int64(cur)
	if d < 0 {
		d = -d
	}
	if d > EpochFreshnessWindow {
		return fmt.Errorf("stale committee snapshot: epoch %d not within ±%d of current epoch %d", snapEpoch, EpochFreshnessWindow, cur)
	}
	return nil
}

// ---- Sequencer-gated selection auth (sequencer_auth.go) ----------------------

// SequencerAuthChallenge is the canonical string the sequencer signs and the
// seed reconstructs: version|method|sequencer_peer_id|unix_ts. MUST match seed.
func SequencerAuthChallenge(method, sequencerPeerID string, unixTs int64) []byte {
	return fmt.Appendf(nil, "%s|%s|%s|%d", SeqAuthVersion, method, sequencerPeerID, unixTs)
}

// SignSequencerRequest signs a selection request with the sequencer's libp2p
// identity key, returning the timestamp + hex signature for the gRPC metadata
// headers. Mirrors seedNodes SignSequencerRequest.
func SignSequencerRequest(priv ic.PrivKey, method string, now time.Time) (unixTs int64, sigHex string, err error) {
	pid, err := peer.IDFromPublicKey(priv.GetPublic())
	if err != nil {
		return 0, "", fmt.Errorf("derive peer id: %w", err)
	}
	unixTs = now.Unix()
	sig, err := priv.Sign(SequencerAuthChallenge(method, pid.String(), unixTs))
	if err != nil {
		return 0, "", fmt.Errorf("sign challenge: %w", err)
	}
	return unixTs, hex.EncodeToString(sig), nil
}
