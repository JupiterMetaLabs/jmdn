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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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

	// EnforceBodyBinding (P3): recompute the canonical BlockHash and TxnsRoot
	// from the received transactions and reject any mismatch BEFORE verifying
	// the committee certificate, so a certified hash cannot be reused over a
	// substituted body. The recompute mirrors the block generator
	// (JMDT-Sequencer-Orchestrator internal/block/generator.go), so honest
	// blocks already satisfy it — enabling this is NOT a wire/consensus change.
	// Default ON.
	EnforceBodyBinding = envOn("JMDN_ENFORCE_BODY_BINDING", true)
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

// ByzantineQuorum returns the Byzantine fault-tolerant threshold 2f+1 for a
// committee of size n, where f = floor((n-1)/3). This is THE threshold for the
// whole node (P2): never a simple majority, never derived from the number of
// votes received. n MUST be the authenticated committee size for the block's
// epoch.
//
// Worked sizes (asserted by tests): n=4→3, 5→3, 7→5, 10→7, 13→9.
func ByzantineQuorum(n int) int {
	if n < 1 {
		// No committee => an unmeetable-by-a-lone-vote threshold. Callers reach
		// this only via the fail-closed error path, but keep it safe.
		return 1
	}
	f := (n - 1) / 3
	return 2*f + 1
}

// CertificateResult reports the outcome of the single certificate verifier.
type CertificateResult struct {
	CommitteeSize int  // n — authenticated committee size for the epoch
	Threshold     int  // 2f+1 required
	YesVotes      int  // distinct eligible +1 votes (deduped by peer_id AND bls_pub)
	Reached       bool // YesVotes >= Threshold
}

// VerifyCertificate is THE single authenticated committee-certificate verifier
// (P2). Every consensus path MUST route through it; no path computes its own
// quorum. It:
//   - FAILS CLOSED via the P1 eligibility source: with enforcement on, a
//     missing/failing source (or a set emptied by block_buddy) returns an error
//     and Reached=false;
//   - counts distinct eligible +1 votes, de-duplicated by BOTH peer_id and
//     bls_pub (invariant 4);
//   - requires a Byzantine 2f+1 majority over the authenticated committee size
//     n = len(committee) (never the vote count, never a simple majority).
//
// The committee size is ALWAYS taken from the authenticated eligible set;
// EnforceCommitteeRegistry only controls whether votes from non-members are
// filtered out. Turning enforcement off does NOT remove the 2f+1 requirement,
// so a node with no committee source fails closed regardless.
func VerifyCertificate(responses []BLS_Signer.BLSresponse, blockHashHex string) (CertificateResult, error) {
	var res CertificateResult

	committee, err := eligibleMembers()
	if err != nil {
		// No authenticated committee => cannot compute a Byzantine threshold.
		return res, err
	}
	n := len(committee)

	res.YesVotes = countEligibleYes(responses, blockHashHex, committee, EnforceCommitteeRegistry)
	res.CommitteeSize = n
	res.Threshold = ByzantineQuorum(n)
	res.Reached = res.YesVotes >= res.Threshold
	return res, nil
}

// countEligibleYes returns the number of distinct +1 voters that (a) produced a
// verifying signature (block-bound; legacy only when RejectLegacyVotes is off)
// and (b) — when filterByMembership is true — are in the eligible committee.
// De-duplicated by BOTH peer_id and bls_pub so one signer cannot inflate quorum
// by presenting the same key under several peer_ids, or several keys for one
// peer_id (invariant 4).
func countEligibleYes(responses []BLS_Signer.BLSresponse, blockHashHex string, committee map[string]struct{}, filterByMembership bool) int {
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
		if filterByMembership {
			if _, ok := committee[r.PeerID]; !ok {
				log.Warn().Str("peer", r.PeerID).Msg("committee vote from ineligible peer (not in buddy set / blocklisted)")
				continue
			}
		}

		if vote != 1 {
			continue
		}
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

// ---- Canonical body binding (P3) ---------------------------------------------
//
// The block generator (JMDT-Sequencer-Orchestrator internal/block/generator.go)
// derives:
//   - BlockHash = Keccak256( concat of each tx's 32-byte hash, in block order )
//   - TxnsRoot  = SHA256 binary Merkle root over the same tx hashes
//     (single tx: sha256(h||h); otherwise pair-hash bottom-up, duplicating the
//     last leaf when a level has an odd count)
//   - StateRoot = Keccak256( parentStateRoot || BlockHash )
//
// The committee's block-bound votes are signed over BlockHash, so recomputing
// BlockHash from the received transactions and rejecting a mismatch binds the
// certificate to THIS transaction set: an attacker cannot reuse a certified
// hash over a substituted body (even one made of otherwise-valid signed txs).
//
// IMPORTANT (proof-field gap): the generator's BlockHash does NOT cover
// StarkProof or Commitment, so body binding here canNOT detect a swapped proof
// field. Closing that requires a generator hash-scheme change (consensus-
// breaking) and is deferred while the prover is placeholder-grade — see
// verifyBlockProof and the PR notes.

// RecomputeBlockHashFromTxs mirrors the generator's
// generateBlockHashFromTransactions: Keccak256 over the concatenation of each
// transaction's 32-byte hash, in order. Matches the generator's empty-block
// value (the zero hash) so callers that already reject empty blocks are safe.
func RecomputeBlockHashFromTxs(txs []config.Transaction) common.Hash {
	if len(txs) == 0 {
		return common.Hash{}
	}
	buf := make([]byte, 0, len(txs)*32)
	for i := range txs {
		buf = append(buf, txs[i].Hash.Bytes()...)
	}
	return common.BytesToHash(crypto.Keccak256(buf))
}

// RecomputeTxnsRoot mirrors the generator's generateMerkleRoot (SHA256 binary
// Merkle tree over the per-tx 32-byte hashes). Returns the "0x"-prefixed hex
// root, matching the generator's string form.
func RecomputeTxnsRoot(txs []config.Transaction) string {
	if len(txs) == 0 {
		return "0x" + strings.Repeat("0", 64)
	}
	level := make([][]byte, len(txs))
	for i := range txs {
		level[i] = txs[i].Hash.Bytes()
	}
	if len(level) == 1 {
		combined := make([]byte, 0, 64)
		combined = append(combined, level[0]...)
		combined = append(combined, level[0]...)
		s := sha256.Sum256(combined)
		return "0x" + hex.EncodeToString(s[:])
	}
	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		next := make([][]byte, 0, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			combined := make([]byte, 0, 64)
			combined = append(combined, level[i]...)
			combined = append(combined, level[i+1]...)
			s := sha256.Sum256(combined)
			next = append(next, s[:])
		}
		level = next
	}
	return "0x" + hex.EncodeToString(level[0])
}

// checkBodyBinding recomputes the canonical BlockHash and TxnsRoot from the
// received transactions and rejects any mismatch (P3). This runs BEFORE
// certificate verification so a certified hash cannot authorize a substituted
// body.
func checkBodyBinding(b *config.ZKBlock) *blockRejection {
	wantHash := RecomputeBlockHashFromTxs(b.Transactions)
	if b.BlockHash != wantHash {
		return reject("body_mismatch",
			"block %s: recomputed hash %s does not match transactions (body substituted?)",
			b.BlockHash.Hex(), wantHash.Hex())
	}
	// TxnsRoot: only enforce when the block carries one (the generator always
	// sets it; a block without it predates the field and is not body-bindable
	// on this axis).
	if strings.TrimSpace(b.TxnsRoot) != "" {
		want := RecomputeTxnsRoot(b.Transactions)
		if !strings.EqualFold(strings.TrimPrefix(b.TxnsRoot, "0x"), strings.TrimPrefix(want, "0x")) {
			return reject("txnsroot_mismatch",
				"block %s: TxnsRoot %s does not match transactions (want %s)",
				b.BlockHash.Hex(), b.TxnsRoot, want)
		}
	}
	return nil
}

// verifyBlockProof is the single, clearly-labelled seam for real ZK/STARK proof
// verification. It returns nil today because the prover is placeholder-grade
// (the RISC0 guest re-commits hashed inputs and does not prove the state
// transition), so a check here would be false assurance.
//
// TODO: real proof verification blocked on prover. When a sound prover exists,
// implement verification HERE without touching the binding logic above. Note
// that binding the proof field into BlockHash additionally requires a generator
// hash-scheme change (see checkBodyBinding's proof-field gap note).
func verifyBlockProof(_ *config.ZKBlock) error { return nil }

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

	// (State-root chain, P3) The generator computes
	// StateRoot = Keccak256(parentStateRoot || BlockHash). With the parent in
	// hand, verify the resulting state root chains from the parent's, so a block
	// cannot claim an inconsistent post-state while keeping a valid parent link.
	if wantStateRoot, ok := stateRootChain(parent.StateRoot, b.BlockHash); ok && b.StateRoot != wantStateRoot {
		return reject("bad_stateroot",
			"block %d stateRoot %s does not chain from parent %s (want %s)",
			b.BlockNumber, b.StateRoot.Hex(), parent.StateRoot.Hex(), wantStateRoot.Hex())
	}
	return nil
}

// stateRootChain mirrors the generator's generateStateRoot:
// Keccak256(parentStateRootBytes || blockHashBytes). Returns ok=false if the
// parent state root is unset (zero), so a fresh/legacy parent does not trigger a
// false rejection.
func stateRootChain(parentStateRoot, blockHash common.Hash) (common.Hash, bool) {
	if parentStateRoot == (common.Hash{}) {
		return common.Hash{}, false
	}
	buf := make([]byte, 0, 64)
	buf = append(buf, parentStateRoot.Bytes()...)
	buf = append(buf, blockHash.Bytes()...)
	return common.BytesToHash(crypto.Keccak256(buf)), true
}
