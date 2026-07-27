package messaging

// Consensus verification helpers layered on top of the fail-closed receive
// gate:
//
//   - block-bound committee-certificate verification
//   - an authorized committee-key registry
//   - equivocation detection + parent/height linkage
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
	"sort"
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
	// members count, and an eligibility source MUST be configured. FAIL CLOSED:
	// no source wired, a source error, or an empty eligible set means the
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

	// EnforceBodyBinding: recompute the canonical BlockHash and TxnsRoot
	// from the received transactions and reject any mismatch BEFORE verifying
	// the committee certificate, so a certified hash stays bound to this
	// transaction set. The recompute mirrors the block generator
	// (JMDT-Sequencer-Orchestrator internal/block/generator.go), so honest
	// blocks already satisfy it — enabling this is NOT a wire/consensus change.
	// Default ON.
	EnforceBodyBinding = envOn("JMDN_ENFORCE_BODY_BINDING", true)
)

// ---- Committee eligibility ---------------------------------------------------
//
// Membership is sourced from the live seedNode buddy selection
// (getBuddy/ListBuddy). The eligible set is the buddy peer_id set MINUS the
// operator's block_buddy blocklist.

// committeeEligibilityFn returns the set of peer_id strings currently eligible
// to vote — the live buddy set from getBuddy/ListBuddy (BEFORE the block_buddy
// blocklist is applied; the blocklist is subtracted centrally in
// eligibleMembers so a source that omits it still has it applied).
//
// Wired at node startup via SetCommitteeEligibilitySource (only the sequencer
// can legitimately call getBuddy). nil => FAIL CLOSED.
var (
	committeeEligibilityMu sync.RWMutex
	committeeEligibilityFn func() (map[string]string, error)
)

// SetCommitteeEligibilitySource wires the live committee-eligibility source. The
// map is peer_id -> authenticated bls_pub (lowercase hex): the KEY set is who may
// vote; the VALUE is the committee BLS key bound to that peer_id in the
// authenticated seed snapshot. An empty value means "eligible but no bound key"
// (legacy getBuddy source with no committee snapshot) — the peer_id↔bls_pub
// binding is only ENFORCED when the value is non-empty. Pass nil to clear
// (forces fail-closed). Safe to call concurrently.
func SetCommitteeEligibilitySource(fn func() (map[string]string, error)) {
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

// committeeSizeLimit returns the operator hard cap on the number of validators
// (buddy nodes) counted toward consensus, from consensus.max_validators. 0 = no
// cap. Reads settings only if loaded (robust to init order), mirroring
// blockedBuddies.
func committeeSizeLimit() int {
	if !settings.IsLoaded() {
		return 0
	}
	if n := settings.Get().Consensus.MaxValidators; n > 0 {
		return n
	}
	return 0
}

// eligibleMembers returns the authenticated eligible committee: the live buddy
// set from the configured source, MINUS the block_buddy blocklist. FAIL CLOSED:
// no source wired, a source error, or an empty result yields an error naming
// the defect. Callers MUST treat an error as "no one is eligible".
func eligibleMembers() (map[string]string, error) {
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
	eligible := make(map[string]string, len(buddies))
	for pid, blsPub := range buddies {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		if _, isBlocked := blocked[pid]; isBlocked {
			log.Warn().Str("peer", pid).Msg("committee: buddy excluded by block_buddy blocklist")
			continue
		}
		// Store the authenticated peer_id -> bls_pub binding (normalized) so the
		// verifier can require a vote's pubkey to match the snapshot-bound key.
		eligible[pid] = normalizeBLSPub(blsPub)
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("committee empty after applying block_buddy blocklist")
	}

	// Hard cap on the number of validators (buddy nodes) counted toward
	// consensus. Trim deterministically by sorted peer_id so every node computes
	// the SAME capped committee (and therefore the SAME 2f+1 threshold). 0 = no
	// cap. This bounds the threshold so it can never be sized over more validators
	// than intended (e.g. main+backup=10 requiring 7 while only 5 main vote).
	if lim := committeeSizeLimit(); lim > 0 && len(eligible) > lim {
		ids := make([]string, 0, len(eligible))
		for pid := range eligible {
			ids = append(ids, pid)
		}
		sort.Strings(ids)
		capped := make(map[string]string, lim)
		for _, pid := range ids[:lim] {
			capped[pid] = eligible[pid]
		}
		log.Warn().Int("eligible", len(eligible)).Int("cap", lim).
			Msg("committee: hard-capped validator set to consensus.max_validators")
		eligible = capped
	}
	return eligible, nil
}

// EligibleCommitteePeerIDs returns the set of peer_ids authorized to vote this
// round — exactly the set keyAuthorized checks against (authenticated source,
// with the block_buddy blocklist and the max_validators cap already applied).
// The sequencer's committee SELECTION uses this so it only picks peers that will
// also be AUTHORIZED to vote (selection ⊆ authorization); otherwise a peer that
// is keyed live but absent from the epoch-frozen/capped authorized set gets
// selected and then rejected as "unauthorized", silently dropping quorum.
// FAIL CLOSED: propagates the eligibleMembers error (source unset/failed/empty),
// which callers MUST treat as "no one eligible".
func EligibleCommitteePeerIDs() (map[string]struct{}, error) {
	members, err := eligibleMembers()
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(members))
	for pid := range members {
		set[pid] = struct{}{}
	}
	return set, nil
}

// keyAuthorized reports whether a vote from (peerID,pubHex) counts toward
// quorum. FAIL CLOSED: a defective/absent eligibility source authorizes
// NOBODY.
//
// When the eligibility source carries a bls_pub for peerID (the seed snapshot),
// the vote's pubHex must equal it. When the bound key is empty (legacy getBuddy
// source with no snapshot), only peer_id membership is checked.
func keyAuthorized(peerID, pubHex string) bool {
	eligible, err := eligibleMembers()
	if err != nil {
		// fmt.Printf (not zerolog) so this denial reliably reaches journald.
		fmt.Printf("🚫 committee auth denied: peer=%s reason=eligibility_source_error err=%v\n", peerID, err)
		return false
	}
	boundKey, ok := eligible[peerID]
	if !ok {
		fmt.Printf("🚫 committee auth denied: peer=%s reason=not_in_eligible_set eligible_count=%d\n", peerID, len(eligible))
		return false
	}
	if boundKey == "" {
		// Legacy/unpinned source carries no bls_pub — peer_id-only authentication.
		// NOT production-safe; a pinned committee snapshot always carries the key.
		fmt.Printf("committee auth: peer=%s legacy peer_id-only (no bls_pub bound)\n", peerID)
		return true
	}
	if normalizeBLSPub(pubHex) != boundKey {
		fmt.Printf("🚫 committee auth denied: peer=%s reason=pubkey_mismatch bound=%s got=%s\n", peerID, boundKey, normalizeBLSPub(pubHex))
		return false
	}
	return true
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

// ---- Certificate verification ------------------------------------------------

// ByzantineQuorum returns the Byzantine fault-tolerant quorum for a committee of
// size n: the general supermajority ceil(2n/3). This is THE threshold for the
// whole node — never a simple majority, never derived from the number of votes
// received. n MUST be the authenticated committee size for the block's epoch.
//
// ceil(2n/3) is the smallest quorum that, for the maximal tolerated
// f = floor((n-1)/3) Byzantine members, guarantees BOTH:
//   - safety: any two quorums intersect in >= f+1 nodes (>= 1 honest), so two
//     conflicting blocks can never both be certified; and
//   - availability: the n-f honest members can always form it.
//
// It is correct at ANY committee size, not only the "nice" n=3f+1 sizes — so the
// committee can scale freely (7, 101, ...) with no hardcoded size. The previous
// 2f+1 was correct ONLY at n=3f+1 and too LOW elsewhere (n=5 gave 3, quorum
// intersection 2q-n=1 < f+1=2 — unsafe). ceil(2n/3) fixes those (5->4, 6->4,
// 8->6, 101->68) and is identical at 3f+1 (4->3, 7->5, 10->7, 100->67).
//
// Worked sizes (asserted by tests): n=4->3, 5->4, 6->4, 7->5, 10->7, 100->67, 101->68.
func ByzantineQuorum(n int) int {
	if n < 1 {
		// No committee => an unmeetable-by-a-lone-vote threshold. Callers reach
		// this only via the fail-closed error path, but keep it safe.
		return 1
	}
	// ceil(2n/3) via integer arithmetic.
	return (2*n + 2) / 3
}

// CertificateResult reports the outcome of the single certificate verifier.
type CertificateResult struct {
	CommitteeSize int  // n — authenticated committee size for the epoch
	Threshold     int  // 2f+1 required
	YesVotes      int  // distinct eligible +1 votes (deduped by peer_id AND bls_pub)
	Reached       bool // YesVotes >= Threshold
}

// VerifyCertificate is THE single authenticated committee-certificate verifier.
// Every consensus path MUST route through it; no path computes its own
// quorum. It:
//   - FAILS CLOSED via the eligibility source: with enforcement on, a
//     missing/failing source (or a set emptied by block_buddy) returns an error
//     and Reached=false;
//   - counts distinct eligible +1 votes, de-duplicated by BOTH peer_id and
//     bls_pub;
//   - requires a Byzantine 2f+1 majority over the authenticated committee size
//     n = len(committee) (never the vote count, never a simple majority).
//
// The committee size is ALWAYS taken from the authenticated eligible set;
// EnforceCommitteeRegistry only controls whether votes from non-members are
// filtered out. Turning enforcement off does NOT remove the 2f+1 requirement,
// so a node with no committee source fails closed regardless.
func VerifyCertificate(responses []BLS_Signer.BLSresponse, blockHashHex string, height uint64) (CertificateResult, error) {
	var res CertificateResult

	committee, err := eligibleMembers()
	if err != nil {
		// No authenticated committee => cannot compute a Byzantine threshold.
		return res, err
	}
	n := len(committee)

	res.YesVotes = countEligibleYes(responses, blockHashHex, height, committee, EnforceCommitteeRegistry)
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
// peer_id.
func countEligibleYes(responses []BLS_Signer.BLSresponse, blockHashHex string, height uint64, committee map[string]string, filterByMembership bool) int {
	countedPeers := make(map[string]bool)
	countedKeys := make(map[string]bool)
	yes := 0
	for _, r := range responses {
		vote := int8(-1)
		if r.Agree {
			vote = 1
		}

		// Prefer block-bound verification. Fall back to legacy only when
		// legacy is still permitted.
		verified := BLS_Verifier.VerifyForBlock(r, BLS_Signer.DomainChainID(), height, blockHashHex, vote) == nil
		if !verified && !RejectLegacyVotes {
			verified = BLS_Verifier.Verify(r, vote) == nil
		}
		if !verified {
			log.Warn().Str("peer", r.PeerID).Msg("committee vote signature failed verification")
			continue
		}

		// committee eligibility (peer_id ∈ live buddy set minus block_buddy)
		// AND peer_id↔bls_pub binding: when the authenticated snapshot binds
		// a bls_pub to this peer_id, the vote's pubkey MUST match it, so a known
		// eligible peer_id voting with a non-matching key does not count. An empty
		// bound key (legacy getBuddy source, no snapshot) skips the binding check.
		if filterByMembership {
			boundKey, ok := committee[r.PeerID]
			if !ok {
				log.Warn().Str("peer", r.PeerID).Msg("committee vote from ineligible peer (not in buddy set / blocklisted)")
				continue
			}
			if boundKey != "" && normalizeBLSPub(r.PubKey) != boundKey {
				log.Warn().Str("peer", r.PeerID).Msg("committee vote pubkey does not match the snapshot-bound bls_pub (rejected)")
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

// ---- Canonical body binding --------------------------------------------------
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
// certificate to this transaction set.

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
// received transactions and rejects any mismatch. This runs BEFORE
// certificate verification so a certified hash cannot authorize a different
// transaction set.
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

// verifyBlockProof is the seam for ZK/STARK proof verification, invoked on the
// block-receive path. Implement verification here as the prover matures.
func verifyBlockProof(_ *config.ZKBlock) error { return nil }

// ---- Equivocation detection --------------------------------------------------

var (
	seenHeightsMu sync.Mutex
	seenHeights   = make(map[uint64]string) // height -> first-seen block hash hex
)

// EquivocationStore persists the first-seen block hash per height so
// equivocation detection survives a process restart. checkEquivocation
// calls these under seenHeightsMu, so implementations need not be internally
// locked. Errors are treated as non-fatal by the caller.
type EquivocationStore interface {
	// FirstSeenHash returns the durably recorded first-seen hash for height,
	// found=false when none is stored yet.
	FirstSeenHash(height uint64) (hashHex string, found bool, err error)
	// RecordFirstSeen durably stores hashHex as the first-seen hash for height.
	RecordFirstSeen(height uint64, hashHex string) error
}

// equivocationStore is the durable backing for equivocation records. When
// nil, detection is best-effort in-memory only and does NOT survive restart.
var equivocationStore EquivocationStore

// SetEquivocationStore wires the durable equivocation store. Call once at
// node startup (see Sequencer/consensus_statemachine.go). Passing nil reverts
// to in-memory-only detection (used by tests that opt out).
func SetEquivocationStore(s EquivocationStore) { equivocationStore = s }

// checkEquivocation records the (height, hash) pair and returns a rejection if a
// DIFFERENT block hash was already seen at this height (a signed fork / double
// proposal). It consults the durable store in addition to the in-memory
// map, so a conflicting block at a height first seen BEFORE a restart is still
// caught. Durable-store errors are non-fatal: it falls back to in-memory
// (degraded) rather than stalling consensus, matching the linkage
// fail-open-on-infra posture. Only fully-validated blocks reach here (called
// last in validateRemoteBlock), so the map is populated only by validated blocks.
func checkEquivocation(number uint64, hashHex string) *blockRejection {
	seenHeightsMu.Lock()
	defer seenHeightsMu.Unlock()

	// Fast path: already recorded in this session.
	if prev, ok := seenHeights[number]; ok {
		if prev != hashHex {
			return reject("equivocation",
				"conflicting block at height %d: already saw %s, now %s", number, prev, hashHex)
		}
		return nil
	}

	// Durable path: a height first seen before a restart still has a record.
	if equivocationStore != nil {
		prev, found, err := equivocationStore.FirstSeenHash(number)
		switch {
		case err != nil:
			log.Warn().Err(err).Uint64("height", number).
				Msg("equivocation: durable read failed; using in-memory only")
		case found:
			seenHeights[number] = prev // warm the in-memory cache
			if prev != hashHex {
				return reject("equivocation",
					"conflicting block at height %d: already saw %s (durable), now %s", number, prev, hashHex)
			}
			return nil
		}
	}

	// First sighting of this height (this session and durably). Record both.
	seenHeights[number] = hashHex
	if equivocationStore != nil {
		if err := equivocationStore.RecordFirstSeen(number, hashHex); err != nil {
			log.Warn().Err(err).Uint64("height", number).
				Msg("equivocation: durable write failed; recorded in-memory only")
		}
	}
	return nil
}

// DBEquivocationStore is the production EquivocationStore backed by the durable
// accountsdb marker (DB_OPs.Get/RecordEquivocationHash). Zero value is usable.
type DBEquivocationStore struct{}

// FirstSeenHash implements EquivocationStore.
func (DBEquivocationStore) FirstSeenHash(height uint64) (string, bool, error) {
	return DB_OPs.GetEquivocationHash(nil, height)
}

// RecordFirstSeen implements EquivocationStore.
func (DBEquivocationStore) RecordFirstSeen(height uint64, hashHex string) error {
	return DB_OPs.RecordEquivocationHash(nil, height, hashHex)
}

// ---- Parent-hash + height linkage (catchup-safe) -----------------------------

// checkLinkage enforces chain linkage for the immediate next block only:
//   - number <= localTip            → stale (we already have this height)
//   - number == localTip+1          → parent hash must equal the local tip's hash
//   - number  > localTip+1          → tolerated (we may be catching up); skipped
//
// Genesis / empty DB (localTip == 0 with no stored block) is tolerated.
// Injectable local-state readers (default: DB_OPs). checkLinkage reads the
// authenticated local tip and the stored parent through these so the pure
// linkage policy can be exercised in tests without a live DB.
var (
	readLocalTip      = func(ctx context.Context) (uint64, error) { return DB_OPs.GetLatestBlockNumber(ctx, nil) }
	readBlockByNumber = func(n uint64) (*config.ZKBlock, error) { return DB_OPs.GetZKBlockByNumber(nil, n) }
)

// catchUpRequester, when set, is nudged the moment a height gap is detected so
// the node begins AUTHENTICATED catch-up immediately (via the sync monitor)
// instead of waiting for the next periodic reconcile. Best-effort and nil-safe:
// rejecting the out-of-band gap block is the correctness guarantee; this only
// accelerates recovery. Deliberately does NOT catch up from the gossip sender
// (which sent the gap block) — the monitor selects
// seednode-vetted peers.
var catchUpRequester func(fromBlock uint64)

// SetCatchUpRequester wires the authenticated catch-up trigger. Call once
// at node startup (see main.go, gated on FastSync.EnableCatchup). Unset => the
// node still rejects gaps and relies on the periodic sync monitor.
func SetCatchUpRequester(fn func(fromBlock uint64)) { catchUpRequester = fn }

func requestCatchUp(fromBlock uint64) {
	if catchUpRequester != nil {
		catchUpRequester(fromBlock)
	}
}

// checkLinkage enforces chain linkage for the immediate next block, FAIL-CLOSED.
// It reads the authenticated local tip/parent and delegates the decision
// to the pure linkageDecision. On a detected height gap it triggers authenticated
// catch-up rather than silently accepting an out-of-band block.
func checkLinkage(ctx context.Context, b *config.ZKBlock) *blockRejection {
	localTip, tipErr := readLocalTip(ctx)

	var parent *config.ZKBlock
	var parentErr error
	if tipErr == nil && localTip > 0 && b.BlockNumber == localTip+1 {
		parent, parentErr = readBlockByNumber(localTip)
	}

	rej := linkageDecision(b, localTip, tipErr, parent, parentErr)
	if rej != nil && (rej.reason == "height_gap" || rej.reason == "not_bootstrapped") {
		// We are missing everything from our next-needed height up to this block
		// (fresh node: from block 1). Trigger authenticated catch-up.
		requestCatchUp(localTip + 1)
	}
	return rej
}

// linkageDecision is the pure, fail-closed linkage policy. Given the
// authenticated local state it returns a rejection or nil:
//
//   - tipErr != nil                 → tip_unreadable (fail closed)
//   - localTip == 0 (fresh node)    → not_bootstrapped for ANY block, including
//     block 1: a node with no local chain must bootstrap via authenticated
//     catch-up, never by ingesting an out-of-band gossip block onto an
//     unverified chain (so it cannot join consensus/state unsynced)
//   - number <= localTip            → stale_height
//   - number  > localTip+1          → height_gap (an out-of-band future block
//     that would break contiguity)
//   - number == localTip+1, parent unreadable/absent → parent_unavailable
//     (fail closed)
//   - parent hash / state-root chain mismatch → bad_parent / bad_stateroot
//   - otherwise                     → accept
func linkageDecision(b *config.ZKBlock, localTip uint64, tipErr error, parent *config.ZKBlock, parentErr error) *blockRejection {
	if tipErr != nil {
		// Cannot authenticate our own tip → cannot safely link. FAIL CLOSED.
		return reject("tip_unreadable",
			"linkage: cannot read local tip to authenticate block %d: %v", b.BlockNumber, tipErr)
	}

	if localTip == 0 {
		// A fresh node (empty chain) must NOT ingest ANY block off the gossip
		// path — including block 1. With no locally-authenticated parent to link
		// against, accepting an out-of-band block would let an unsynced node join
		// consensus/state on an unverified chain. A fresh node bootstraps ONLY
		// through authenticated catch-up (FastSync), which applies blocks via its
		// own path (not this one) and does not use checkLinkage. The sequencer
		// produces blocks on a separate path (validateRemoteBlock is remote-only),
		// so it is unaffected.
		return reject("not_bootstrapped",
			"node has no local chain (tip 0); block %d must arrive via authenticated catch-up, not gossip", b.BlockNumber)
	}

	if b.BlockNumber <= localTip {
		return reject("stale_height",
			"block %d not ahead of local tip %d", b.BlockNumber, localTip)
	}
	if b.BlockNumber > localTip+1 {
		return reject("height_gap",
			"block %d is beyond next-expected %d (gap from tip %d); requires authenticated catch-up",
			b.BlockNumber, localTip+1, localTip)
	}

	// b.BlockNumber == localTip+1 → verify parent linkage. FAIL CLOSED if the
	// parent cannot be authenticated.
	if parentErr != nil || parent == nil {
		return reject("parent_unavailable",
			"linkage: cannot load parent at tip %d to authenticate block %d: %v",
			localTip, b.BlockNumber, parentErr)
	}
	if b.PrevHash != parent.BlockHash {
		return reject("bad_parent",
			"block %d prevHash %s != local tip %d hash %s",
			b.BlockNumber, b.PrevHash.Hex(), localTip, parent.BlockHash.Hex())
	}

	// (State-root chain) The generator computes
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
