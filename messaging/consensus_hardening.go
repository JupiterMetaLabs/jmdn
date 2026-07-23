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
	"encoding/json"
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

	// EnforceCommitteeRegistry: when true AND a registry is loaded, only votes
	// from registered committee keys count. If no registry file is present, the
	// check is skipped with a loud warning (so a missing file cannot silently
	// brick a node). Default ON.
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

// loadCommitteeKeys reads the registry once. Returns (nil,nil) when no file
// exists — callers treat that as "registry not configured".
func loadCommitteeKeys() (map[string]string, error) {
	committeeOnce.Do(func() {
		data, err := os.ReadFile(committeeKeysFile)
		if err != nil {
			if os.IsNotExist(err) {
				committeeKeys = nil // not configured
				return
			}
			committeeErr = err
			return
		}
		var entries []committeeEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			committeeErr = err
			return
		}
		m := make(map[string]string, len(entries))
		for _, e := range entries {
			if e.PeerID == "" || e.BLSPub == "" {
				continue
			}
			m[e.PeerID] = strings.ToLower(e.BLSPub)
		}
		committeeKeys = m
	})
	return committeeKeys, committeeErr
}

// keyAuthorized reports whether (peerID,pubHex) matches a registry entry. When
// the registry is not configured it returns true (membership not enforced) so a
// missing file does not brick the node.
func keyAuthorized(peerID, pubHex string) bool {
	keys, err := loadCommitteeKeys()
	if err != nil {
		log.Error().Err(err).Msg("committee registry load failed; skipping membership check")
		return true
	}
	if len(keys) == 0 {
		return true // not configured
	}
	want, ok := keys[peerID]
	return ok && want == strings.ToLower(pubHex)
}

// registryConfigured reports whether a non-empty registry is loaded.
func registryConfigured() bool {
	keys, err := loadCommitteeKeys()
	return err == nil && len(keys) > 0
}

// CommitteeKeyAuthorized reports whether (peerID,pubHex) is an authorized
// committee key per the registry. Exported for the sequencer's vote-aggregation
// path (Sequencer/Consensus.go). Returns true when the registry is unconfigured.
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
