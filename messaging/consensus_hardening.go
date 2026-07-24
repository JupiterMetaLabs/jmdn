package messaging

// Phase-2 consensus hardening for JMDN-001. This file adds, on top of the
// Phase-1 fail-closed receive gate:
//
//   - block-bound committee-certificate verification (D3)
//   - an authorized committee-key registry (D4)
//   - equivocation detection + parent/height linkage (D7-adjacent)
//
// Rollout note: the flags below default ON per operator decision. The one that
// can fork a mixed-version network — RejectLegacyVotes — must only be true once
// EVERY node in the network emits block-bound votes (EmitBlockBoundVotes). Until
// then, verification accepts both formats.
//
// Flags are package vars (not consts) so they are togglable via the environment
// at process start and settable by tests.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/rs/zerolog/log"
)

// envOn reports whether an env var is set to a truthy value, defaulting to def
// when the var is unset. Lets operators flip a default-on flag OFF with "0".
func envOn(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

var (
	// RejectLegacyVotes: when true, only block-bound signatures count toward
	// quorum; legacy "vote:<v>" signatures are ignored. Default ON per operator
	// decision. MUST NOT be true until the whole network emits block-bound votes.
	RejectLegacyVotes = envOn("JMDN_REJECT_LEGACY_VOTES", true)

	// EnforceCommitteeRegistry: when true, only votes from registered committee
	// keys count — and the registry MUST be valid. FAIL CLOSED (P1): a missing,
	// empty, unreadable, or malformed registry means the node refuses consensus
	// participation with a loud error naming the defect. Absence of
	// configuration is never "allow". Default ON; turning it off is an explicit
	// operator decision, not a silent fallback.
	EnforceCommitteeRegistry = envOn("JMDN_ENFORCE_COMMITTEE_REGISTRY", true)

	// EnforceBlockLinkage: parent-hash + height checks. Catchup-safe: only the
	// immediate next block (tip+1) is parent-checked; future/gap blocks are
	// tolerated (we may be behind). Default ON.
	EnforceBlockLinkage = envOn("JMDN_ENFORCE_BLOCK_LINKAGE", true)

	// committeeKeysFile is the on-disk authorized committee registry:
	// a JSON array of {"peer_id","bls_pub"} (bls_pub = hex, as in BLSresponse).
	committeeKeysFile = "./config/committee_keys.json"
)

// ---- Authorized committee-key registry (D4) ----------------------------------

type committeeEntry struct {
	PeerID string `json:"peer_id"`
	BLSPub string `json:"bls_pub"`
}

var (
	committeeOnce sync.Once
	committeeKeys map[string]string // peerID -> lowercased bls pubkey hex
	committeeErr  error
)

// normalizeBLSPub canonicalizes a BLS public key hex string for comparison and
// duplicate detection: trim, lowercase, strip an optional "0x" prefix.
func normalizeBLSPub(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
}

// loadCommitteeKeys reads and validates the registry once. FAIL CLOSED (P1):
// ANY defect — missing file, unreadable file, malformed JSON, empty set, an
// entry with a missing field or non-hex key, a duplicate peer_id, or a
// duplicate bls_pub — yields (nil, error naming the defect). Callers MUST treat
// an error or an empty map as "no one is authorized".
func loadCommitteeKeys() (map[string]string, error) {
	committeeOnce.Do(func() {
		committeeKeys, committeeErr = readCommitteeKeys(committeeKeysFile)
		if committeeErr != nil {
			log.Error().Err(committeeErr).Str("file", committeeKeysFile).
				Msg("committee registry invalid — refusing consensus participation (fail closed)")
		}
	})
	return committeeKeys, committeeErr
}

// readCommitteeKeys parses and validates a committee registry file. It never
// returns an empty or partially-valid map without an error: uniqueness is
// enforced in BOTH directions (peer_id AND bls_pub) so one BLS key can never
// hold two committee identities, and one identity can never have two keys.
func readCommitteeKeys(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("committee registry not configured: %s does not exist", path)
		}
		return nil, fmt.Errorf("committee registry unreadable: %w", err)
	}
	var entries []committeeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("committee registry malformed: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("committee registry empty: %s contains no entries", path)
	}
	m := make(map[string]string, len(entries))
	pubOwner := make(map[string]string, len(entries)) // bls_pub -> peer_id
	for i, e := range entries {
		if e.PeerID == "" || e.BLSPub == "" {
			return nil, fmt.Errorf("committee registry entry %d incomplete: peer_id and bls_pub are both required", i)
		}
		pub := normalizeBLSPub(e.BLSPub)
		if pub == "" {
			return nil, fmt.Errorf("committee registry entry %d: empty bls_pub for peer %s", i, e.PeerID)
		}
		if _, err := hex.DecodeString(pub); err != nil {
			return nil, fmt.Errorf("committee registry entry %d: bls_pub for peer %s is not valid hex", i, e.PeerID)
		}
		if _, dup := m[e.PeerID]; dup {
			return nil, fmt.Errorf("committee registry: duplicate peer_id %s", e.PeerID)
		}
		if prev, dup := pubOwner[pub]; dup {
			return nil, fmt.Errorf("committee registry: duplicate bls_pub shared by peers %s and %s", prev, e.PeerID)
		}
		m[e.PeerID] = pub
		pubOwner[pub] = e.PeerID
	}
	return m, nil
}

// keyAuthorized reports whether (peerID,pubHex) matches a registry entry.
// FAIL CLOSED (P1): a defective (missing/empty/unreadable/malformed/duplicate)
// registry authorizes NOBODY. The defect is logged loudly at load time.
func keyAuthorized(peerID, pubHex string) bool {
	keys, err := loadCommitteeKeys()
	if err != nil || len(keys) == 0 {
		return false
	}
	want, ok := keys[peerID]
	return ok && want == normalizeBLSPub(pubHex)
}

// registryConfigured reports whether a valid, non-empty registry is loaded.
func registryConfigured() bool {
	keys, err := loadCommitteeKeys()
	return err == nil && len(keys) > 0
}

// ValidateCommitteeRegistry returns nil when a valid, non-empty committee
// registry is loaded; otherwise the error naming the exact defect. Call this
// on every consensus path (and at boot) so a node with a defective registry
// refuses consensus participation loudly instead of failing open.
func ValidateCommitteeRegistry() error {
	keys, err := loadCommitteeKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("committee registry empty: no authorized committee keys")
	}
	return nil
}

// CommitteeKeyAuthorized reports whether (peerID,pubHex) is an authorized
// committee key per the registry. Exported for the sequencer's vote-aggregation
// path (Sequencer/Consensus.go). FAIL CLOSED: returns false when the registry
// is missing or defective.
func CommitteeKeyAuthorized(peerID, pubHex string) bool { return keyAuthorized(peerID, pubHex) }

// RegistryConfigured is the exported form of registryConfigured for callers
// outside this package.
func RegistryConfigured() bool { return registryConfigured() }

// ---- Certificate verification (D2/D3/D4) -------------------------------------

// countCertQuorum verifies the certificate and returns the number of distinct,
// authorized committee members that signed a valid +1 vote for this block.
// Votes are de-duplicated by PeerID. A vote counts only if:
//   - its signature verifies (block-bound; legacy also accepted unless
//     RejectLegacyVotes), AND
//   - EnforceCommitteeRegistry is off / unconfigured, or the signer's key is in
//     the registry.
func countCertQuorum(responses []BLS_Signer.BLSresponse, blockHashHex string) int {
	countedYes := make(map[string]bool)
	for _, r := range responses {
		vote := int8(-1)
		if r.Agree {
			vote = 1
		}

		// Prefer block-bound verification (D3). Fall back to legacy only when
		// legacy is still permitted.
		verified := BLS_Verifier.VerifyForBlock(r, blockHashHex, vote) == nil
		if !verified && !RejectLegacyVotes {
			verified = BLS_Verifier.Verify(r, vote) == nil
		}
		if !verified {
			log.Warn().Str("peer", r.PeerID).Msg("committee vote signature failed verification")
			continue
		}

		// (D4) committee membership.
		if EnforceCommitteeRegistry && !keyAuthorized(r.PeerID, r.PubKey) {
			log.Warn().Str("peer", r.PeerID).Msg("committee vote from unauthorized key (not in registry)")
			continue
		}

		if vote == 1 {
			countedYes[r.PeerID] = true
		}
	}
	return len(countedYes)
}

// ---- Equivocation detection --------------------------------------------------

var (
	seenHeightsMu sync.Mutex
	seenHeights   = make(map[uint64]string) // height -> first-seen block hash hex
)

// checkEquivocation records the (height, hash) pair and returns a rejection if a
// DIFFERENT block hash was already seen at this height (a signed fork / double
// proposal). Best-effort, in-memory (resets on restart) — catches live
// equivocation on the gossip path.
func checkEquivocation(number uint64, hashHex string) *blockRejection {
	seenHeightsMu.Lock()
	defer seenHeightsMu.Unlock()
	if prev, ok := seenHeights[number]; ok && prev != hashHex {
		return reject("equivocation",
			"conflicting block at height %d: already saw %s, now %s", number, prev, hashHex)
	}
	seenHeights[number] = hashHex
	return nil
}

// ---- Parent-hash + height linkage (catchup-safe) -----------------------------

// checkLinkage enforces chain linkage for the immediate next block only:
//   - number <= localTip            → stale (we already have this height)
//   - number == localTip+1          → parent hash must equal the local tip's hash
//   - number  > localTip+1          → tolerated (we may be catching up); skipped
//
// Genesis / empty DB (localTip == 0 with no stored block) is tolerated.
func checkLinkage(ctx context.Context, b *config.ZKBlock) *blockRejection {
	localTip, err := DB_OPs.GetLatestBlockNumber(ctx, nil)
	if err != nil {
		// Can't read tip — do not reject (fail open on linkage only; the cert +
		// signature checks still gate acceptance). Log for visibility.
		log.Warn().Err(err).Msg("linkage: failed to read local tip; skipping linkage check")
		return nil
	}

	if localTip == 0 {
		return nil // genesis / fresh node: nothing to link against yet
	}
	if b.BlockNumber <= localTip {
		return reject("stale_height",
			"block %d not ahead of local tip %d", b.BlockNumber, localTip)
	}
	if b.BlockNumber > localTip+1 {
		return nil // gap — likely behind / catching up; tolerate
	}

	// b.BlockNumber == localTip+1 → verify parent linkage.
	parent, err := DB_OPs.GetZKBlockByNumber(nil, localTip)
	if err != nil || parent == nil {
		log.Warn().Err(err).Uint64("tip", localTip).Msg("linkage: failed to load parent; skipping parent check")
		return nil
	}
	if b.PrevHash != parent.BlockHash {
		return reject("bad_parent",
			"block %d prevHash %s != local tip %d hash %s",
			b.BlockNumber, b.PrevHash.Hex(), localTip, parent.BlockHash.Hex())
	}
	return nil
}
