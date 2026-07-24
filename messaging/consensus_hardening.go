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
	"fmt"
	"os"
	"strings"
	"sync"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"

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

	// EnforceCommitteeRegistry: when true, only votes from ELIGIBLE committee
	// members count, and an eligibility source MUST be configured. FAIL CLOSED
	// (P1): no source wired, a source error, or an empty eligible set means the
	// node refuses consensus participation with a loud error. Absence of a
	// source is never "allow". Default ON; turning it off is an explicit
	// operator decision, not a silent fallback.
	//
	// (Name kept for env/back-compat: JMDN_ENFORCE_COMMITTEE_REGISTRY.)
	EnforceCommitteeRegistry = envOn("JMDN_ENFORCE_COMMITTEE_REGISTRY", true)

	// EnforceBlockLinkage: parent-hash + height checks. Catchup-safe: only the
	// immediate next block (tip+1) is parent-checked; future/gap blocks are
	// tolerated (we may be behind). Default ON.
	EnforceBlockLinkage = envOn("JMDN_ENFORCE_BLOCK_LINKAGE", true)
)

// ---- Committee eligibility (D4) ----------------------------------------------
//
// Membership is DYNAMIC and sourced from the live seedNode buddy selection
// (getBuddy/ListBuddy), NOT from a hand-authored file. The eligible set is the
// buddy peer_id set MINUS the operator's block_buddy blocklist.
//
// Interim scope (per operator decision): only the peer_id is authenticated —
// eligibility is "peer_id ∈ buddy set". The BLS public key a vote carries is
// self-reported and NOT yet bound to the peer_id, because ListBuddy does not
// return bls_pub today. When seedNode adds bls_pub to ListBuddy, bind it in
// eligibleMembers (see the seam marked BLS-BINDING-SEAM) and enforce
// peer_id ↔ bls_pub in keyAuthorized — no consensus-logic change required.
//
// SECURITY NOTE (accepted interim risk): until bls_pub binding lands, an
// attacker who knows an eligible buddy's peer_id can craft a vote under that
// peer_id with their OWN BLS key and it will count. This is a temporary,
// NOT-production-safe control. Tracked by TestP1_ForgeryWindow_*.

// committeeEligibilityFn returns the set of peer_id strings currently eligible
// to vote — the live buddy set from getBuddy/ListBuddy (BEFORE the block_buddy
// blocklist is applied; the blocklist is subtracted centrally in
// eligibleMembers so it cannot be bypassed by a source that forgets it).
//
// Wired at node startup via SetCommitteeEligibilitySource (only the sequencer
// can legitimately call getBuddy). nil => FAIL CLOSED.
var (
	committeeEligibilityMu sync.RWMutex
	committeeEligibilityFn func() (map[string]struct{}, error)
)

// SetCommitteeEligibilitySource wires the live committee-eligibility source
// (typically a closure over the sequencer's QueryBuddyNodes). Pass nil to clear
// it (which forces fail-closed). Safe to call concurrently.
func SetCommitteeEligibilitySource(fn func() (map[string]struct{}, error)) {
	committeeEligibilityMu.Lock()
	committeeEligibilityFn = fn
	committeeEligibilityMu.Unlock()
}

// blockedBuddies returns the operator block_buddy blocklist as a set. Reads
// settings only if they have been loaded; before Load() it returns an empty set
// (no blocklist) rather than panicking, so the hot path is robust to init order.
func blockedBuddies() map[string]struct{} {
	blocked := make(map[string]struct{})
	if !settings.IsLoaded() {
		return blocked
	}
	for _, id := range settings.Get().Consensus.BlockBuddy {
		id = strings.TrimSpace(id)
		if id != "" {
			blocked[id] = struct{}{}
		}
	}
	return blocked
}

// eligibleMembers returns the authenticated eligible committee: the live buddy
// set from the configured source, MINUS the block_buddy blocklist. FAIL CLOSED:
// no source wired, a source error, or an empty result yields an error naming
// the defect. Callers MUST treat an error as "no one is eligible".
func eligibleMembers() (map[string]struct{}, error) {
	committeeEligibilityMu.RLock()
	fn := committeeEligibilityFn
	committeeEligibilityMu.RUnlock()

	if fn == nil {
		return nil, fmt.Errorf("committee eligibility source not configured (fail closed): call messaging.SetCommitteeEligibilitySource at startup")
	}
	buddies, err := fn()
	if err != nil {
		return nil, fmt.Errorf("committee eligibility source failed: %w", err)
	}
	if len(buddies) == 0 {
		return nil, fmt.Errorf("committee eligibility source returned an empty buddy set")
	}

	blocked := blockedBuddies()
	eligible := make(map[string]struct{}, len(buddies))
	for pid := range buddies {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		if _, isBlocked := blocked[pid]; isBlocked {
			log.Warn().Str("peer", pid).Msg("committee: buddy excluded by block_buddy blocklist")
			continue
		}
		// BLS-BINDING-SEAM: when ListBuddy returns bls_pub, store
		// peer_id -> bls_pub here (change the value type) so keyAuthorized can
		// enforce the binding.
		eligible[pid] = struct{}{}
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("committee empty after applying block_buddy blocklist")
	}
	return eligible, nil
}

// keyAuthorized reports whether a vote from (peerID,pubHex) counts toward
// quorum. FAIL CLOSED (P1): a defective/absent eligibility source authorizes
// NOBODY.
//
// Interim: authenticates peer_id membership only; pubHex is accepted as
// self-reported (see BLS-BINDING-SEAM / SECURITY NOTE above).
func keyAuthorized(peerID, pubHex string) bool {
	_ = pubHex // not yet bound to peer_id — see BLS-BINDING-SEAM
	eligible, err := eligibleMembers()
	if err != nil {
		return false
	}
	_, ok := eligible[peerID]
	return ok
}

// ValidateCommitteeSource returns nil when a valid, non-empty eligible committee
// is available (source wired, no error, non-empty after the blocklist);
// otherwise the error naming the defect. Call this on every consensus path (and
// at boot) so a node with no/failed eligibility source refuses consensus
// participation loudly instead of failing open.
func ValidateCommitteeSource() error {
	_, err := eligibleMembers()
	return err
}

// ValidateCommitteeRegistry is retained as a back-compat alias for callers and
// now validates the dynamic eligibility source.
func ValidateCommitteeRegistry() error { return ValidateCommitteeSource() }

// CommitteeKeyAuthorized reports whether a vote from (peerID,pubHex) is from an
// eligible committee member. Exported for the sequencer's vote-aggregation path
// (Sequencer/Consensus.go). FAIL CLOSED: returns false when the eligibility
// source is missing or failing.
func CommitteeKeyAuthorized(peerID, pubHex string) bool { return keyAuthorized(peerID, pubHex) }

// RegistryConfigured reports whether a valid, non-empty eligible committee is
// available. Retained for callers outside this package.
func RegistryConfigured() bool { return ValidateCommitteeSource() == nil }

// ---- Certificate verification (D2/D3/D4) -------------------------------------

// countCertQuorum verifies the certificate and returns the number of distinct,
// eligible committee members that signed a valid +1 vote for this block.
// Votes are de-duplicated by BOTH PeerID and BLS public key (invariant 4): the
// same key under two PeerIDs, or two keys claimed by one PeerID, counts once.
// A vote counts only if:
//   - its signature verifies (block-bound; legacy also accepted unless
//     RejectLegacyVotes), AND
//   - EnforceCommitteeRegistry is off, or the signer's peer_id is eligible
//     (peer_id ∈ live buddy set minus block_buddy). See keyAuthorized.
func countCertQuorum(responses []BLS_Signer.BLSresponse, blockHashHex string) int {
	countedPeers := make(map[string]bool)
	countedKeys := make(map[string]bool)
	yes := 0
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

		// (D4) committee eligibility (peer_id ∈ live buddy set minus block_buddy).
		if EnforceCommitteeRegistry && !keyAuthorized(r.PeerID, r.PubKey) {
			log.Warn().Str("peer", r.PeerID).Msg("committee vote from ineligible peer (not in buddy set / blocklisted)")
			continue
		}

		if vote != 1 {
			continue
		}
		// Dedup by BOTH identity axes so one signer cannot inflate quorum by
		// presenting the same key under several peer_ids, or by claiming several
		// keys for one peer_id (invariant 4).
		pubKey := normalizeBLSPub(r.PubKey)
		if countedPeers[r.PeerID] || (pubKey != "" && countedKeys[pubKey]) {
			continue
		}
		countedPeers[r.PeerID] = true
		if pubKey != "" {
			countedKeys[pubKey] = true
		}
		yes++
	}
	return yes
}

// normalizeBLSPub canonicalizes a BLS public key hex string for comparison and
// de-duplication: trim, lowercase, strip an optional "0x" prefix.
func normalizeBLSPub(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
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
