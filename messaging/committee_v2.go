// Committee selection v2 - the wiring that removes F1, F2 and F5 from the live
// path.
//
// # Rollout: this file is INERT until a flag is set
//
// Everything here is gated on JMDN_COMMITTEE_V2, which defaults to FALSE. With
// the flag off, VerifyCertificateForRound delegates to the existing
// VerifyCertificate and behaviour is byte-identical to today. That is
// deliberate: the committee travels on the wire inside ConsensusMessage
// (Sequencer/helper/buddynodes_operations.go:73), so old and new nodes running
// different selection would exchange messages carrying different committees.
//
// This is F3. It is not a bug to fix in code - it is a rollout constraint. The
// flag makes it a COORDINATED FLAG FLIP rather than a fork: ship the binary
// everywhere with the flag off, verify, then flip the fleet together.
//
// # What each finding needed
//
//	F1  selection and the certificate drew from DIFFERENT sources, so at P > k
//	    they seat different committees and votes are rejected -> halt.
//	    FIX: SelectCommittee is the ONLY committee producer. Both the vote path
//	    and the tally call it. There is no second query in this file.
//
//	F2  the alphabetical cap froze the voting set to the k first peer ids.
//	    FIX: eligibleMembersUncapped + seed-ranked CommitteeFor. Determinism is
//	    preserved - every node computes the same set - but it now rotates.
//
//	F5  the seed was Sprintf(nodeID, networkSalt): node-local, and with no
//	    height it never changed.
//	    FIX: seed = H(domain || entropy || prevHash || height || period). No node
//	    identity, and PrevHash rather than the block under vote so a proposer
//	    cannot grind its own jury.
//
// F4 is a placement decision, already taken: the pure functions live in the avc
// module (github.com/JupiterMetaLabs/avc/committee), which jmdn already reaches
// through the existing `replace ... => ../avc` directive. This file is the only
// jmdn-side code, and it holds no algorithm - just adaptation.

package messaging

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/JupiterMetaLabs/avc/committee"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/metrics"

	"github.com/rs/zerolog/log"
)

// CommitteeV2Enabled gates every behaviour change in this file.
//
// DEFAULT FALSE. Turning it on changes which peers vote, so it must be flipped
// across the whole fleet together. See the rollout note above.
var CommitteeV2Enabled = envOn("JMDN_COMMITTEE_V2", false)

// ValidateCommitteeV2Pin is the boot-time guard for the block-858 finding-4
// empty-pin liveness trap. Under committee-v2 the vote-requester authz gate
// (CON-03) authorizes the sequencer's vote requests against the pinned seed
// authority BLS public key; that field defaults to "" (config/settings), so a
// fleet that enables committee-v2 without pinning it has every seated validator
// fail-closed and refuse the sequencer — silently, WARN-only, on the validators —
// and nothing finalizes. committee-v2 ALWAYS implies a pinned sequencer key, so
// this is never a valid configuration.
//
// Returns nil when committee-v2 is off (default — no-op) or the pin is non-empty;
// otherwise a boot-refusal error. main() calls this and exits non-zero, converting
// a silent fleet-wide stall into a loud boot failure. Pure and side-effect-free so
// it is unit-testable without the consensus stack.
func ValidateCommitteeV2Pin(committeeV2Enabled bool, seedAuthorityBLSPub string) error {
	if committeeV2Enabled && strings.TrimSpace(seedAuthorityBLSPub) == "" {
		return fmt.Errorf("JMDN_COMMITTEE_V2 is enabled but consensus.seed_authority_bls_pub is empty — " +
			"every seated validator would fail-closed and refuse the sequencer's vote requests " +
			"(silently, WARN-only), so no block can finalize; pin the seed authority BLS public key " +
			"fleet-wide before enabling committee-v2, or disable JMDN_COMMITTEE_V2")
	}
	return nil
}

// UniformSelectionWeight is the weight given to every member.
//
// The signed snapshot (seednode/committee/contracts.go:106) carries peer_id and
// bls_pub only - there is no weight field - and every peer in the live system
// scores defaultSeedPeerSelectionScore = 0.7, so weighting is inert today and
// uniform is the honest default.
//
// Do NOT compute per-peer weights here. Weights are a REPLICATED INPUT: if two
// nodes disagree about a weight they seat different committees. When reputation
// arrives it must travel inside the authenticated snapshot, signed alongside the
// peer ids.
const UniformSelectionWeight = 1.0

// RoundContext is the per-round input selection needs beyond the eligible set.
//
// It exists because a committee cannot be derived from (blockHash, height)
// alone. PrevHash and Period are what make the draw un-grindable and make a
// timed-out round re-draw, and neither is available inside VerifyCertificate
// today - which is why the call sites must pass them in.
// SelectionPeriod is the block-counted clock (EpochForHeight,
// consensus.committee_epoch_blocks) that determines how often the buddy
// candidate pool refreshes. Distinct from committee.EntropyEpoch
// (slot-counted, EpochForSlot) — the two happen to both be called "epoch" in
// casual conversation, but they have never been the same number and must not
// become interchangeable by accident. See
// docs/VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md §14.
type SelectionPeriod uint64

type RoundContext struct {
	// SelectionPeriod selects the frozen validator-pool snapshot
	// (fleetCommitteeSnapshotFor) — the buddy committee's own clock, refreshed
	// every consensus.committee_epoch_blocks blocks. Feeds ONLY the pool
	// fetch, never entropy.
	SelectionPeriod SelectionPeriod

	// EntropyEpoch selects the entropy (committee.SeedSource.EpochEntropy) —
	// always the slot-based clock (EpochForSlot), independent of
	// SelectionPeriod. At Stage 1 the SeedSource (SaltSource) ignores it;
	// Stage 2's real beacon does not, which is the whole reason these two
	// fields must never collapse into one "Epoch" again.
	EntropyEpoch committee.EntropyEpoch

	// PrevHash is the PARENT block's hash (config.ZKBlock.PrevHash).
	//
	// It must NEVER be the hash of the block being voted on. A proposer able to
	// stir the block under vote into the seed could re-roll its own block until
	// the draw returned a jury it liked. This is F5.
	PrevHash []byte

	// Height is the block height.
	Height uint64

	// Period increments on each round timeout, so a stalled height re-draws
	// instead of retrying the same members. Callers that do not yet track
	// timeouts pass 0; see the migration note on VerifyCertificateForRound.
	Period uint64
}

// ---------------------------------------------------------------------------
// Round context from a block
// ---------------------------------------------------------------------------

// RoundContextForBlock is the ONE place a RoundContext is built from a block.
//
// Every certificate call site goes through it, for the same reason
// SelectCommittee is the only committee producer: a second derivation is a
// second source of truth, and the two would eventually disagree.
//
// The proposer's own vote path and every verifier reach identical values here,
// because every field is read from the block itself. Nothing is read from the
// clock. See EpochForHeight for why that matters.
//
// Period is the block's own stamped b.Period. It needs no local store and no
// catch-up: the proposer stamps it (Block/consensus_fields.go), it is part of
// the ConsensusHash preimage (Security/consensus_fields_hash.go, enforced by
// checkConsensusBinding), and committee votes are bound to that hash. A
// rewritten Period therefore breaks the hash or the certificate, so a node
// that was offline or just restarted can verify a block at any period from
// the block alone.
func RoundContextForBlock(b *config.ZKBlock) RoundContext {
	if b == nil {
		return RoundContext{}
	}
	rc := RoundContext{
		SelectionPeriod: SelectionPeriod(EpochForHeight(b.BlockNumber)),
		EntropyEpoch:    committee.EntropyEpoch(EpochForSlot(b.Slot)),
		PrevHash:        b.PrevHash.Bytes(),
		Height:          b.BlockNumber,
		Period:          b.Period,
	}

	// Cross-node determinism check (operator-facing, not debug-only): every
	// node that reaches this line for the same Height must log an identical
	// slot/period/entropy_epoch/selection_period tuple. A mismatch here,
	// compared across two nodes' logs at the same height, localizes the
	// divergence to BEFORE committee selection even runs (block sync, the
	// slot clock, or the Period store) rather than inside it. Same
	// zerolog global logger consensus_hardening.go already uses in this
	// package (github.com/rs/zerolog/log) - prints to console with no
	// extra config, unlike the ion/logging named-logger path elsewhere in
	// this codebase, which defaults to level "warn" and would have
	// silently dropped an Info line.
	log.Info().
		Uint64("height", b.BlockNumber).
		Uint64("slot", b.Slot).
		Uint64("period", b.Period).
		Uint64("entropy_epoch", uint64(rc.EntropyEpoch)).
		Uint64("selection_period", uint64(rc.SelectionPeriod)).
		Msg("committee: round context built")

	return rc
}

// EpochForHeight maps a block height to the selection epoch.
//
// IT IS DERIVED FROM THE BLOCK, NEVER FROM THE CLOCK. This is not a style
// preference. The epoch is hashed into the seed, so if two nodes compute
// different epochs for the same block they derive different seeds, seat
// different committees, and reject each other's votes - the exact failure this
// package exists to remove. A verifier replaying a block an hour later, or a
// node whose clock has drifted, must land on the same value as the proposer.
//
// Note the contrast with seednode/committee.EpochForTime, which IS wall-clock
// (unix / committee_epoch_seconds). That one selects WHICH SNAPSHOT the seed
// node serves. This one selects the seed. They are different clocks on purpose,
// and EpochForTime must never be fed into a RoundContext.
//
// consensus.committee_epoch_blocks == 0 (the default) means "one epoch": every
// height maps to epoch 0. That is deliberate and safe at Stage 1, where the
// SeedSource is a fixed salt and ignores the epoch entirely - Height and
// PrevHash already rotate the draw every block. Stage 2 needs a real epoch
// length, because that is the unit the RANDAO round and the beacon are keyed
// on; set it then, network-wide, as a coordinated change.
func EpochForHeight(height uint64) uint64 {
	n := epochLengthBlocks()
	if n == 0 {
		return 0
	}
	return height / n
}

// epochLengthBlocks reads consensus.committee_epoch_blocks, tolerating an
// unloaded settings singleton the way blockedBuddies and committeeSizeLimit do.
func epochLengthBlocks() uint64 {
	if !settings.IsLoaded() {
		return 0
	}
	if n := settings.Get().Consensus.CommitteeEpochBlocks; n > 0 {
		return uint64(n)
	}
	return 0
}

// ---------------------------------------------------------------------------
// The single committee source
// ---------------------------------------------------------------------------

// SelectCommittee returns the committee seated for one round.
//
// This is the ONLY producer of a committee. The vote path and the tally both
// call it, which is what makes F1 unreachable: there is no second source to
// disagree with.
//
// It fails closed. Any error means no committee, and the caller must refuse the
// block rather than fall back to an unauthenticated or differently-derived set.
func SelectCommittee(rc RoundContext) ([]committee.Member, error) {
	return SelectCommitteeWithSize(rc, committeeSizeLimit())
}

// ErrLegacySourceUnderV2 is returned when v2 selection is enabled but the
// committee source is still the unauthenticated legacy one.
//
// This is a PRECONDITION, not a preference. Under the legacy source
// eligibleMembers derives from PeerList.MainPeers, which derives from the
// per-node VRF shuffle in jmdn/AVC/NodeSelection - a set the nodes already
// disagree about. Seed-ranking it would order a different input on every node
// and produce a different committee, which is the exact failure v2 exists to
// remove. Garbage in.
//
// So v2 refuses to run without consensus.seed_authority_bls_pub pinned. Failing
// closed here is far better than silently seeding from a divergent set.
var ErrLegacySourceUnderV2 = errors.New(
	"committee: JMDN_COMMITTEE_V2 requires consensus.seed_authority_bls_pub to be pinned " +
		"(the legacy source derives from a per-node shuffle, so seed-ranking it would still diverge)")

// SelectCommitteeWithSize is SelectCommittee with the committee size supplied
// explicitly rather than read from consensus.max_validators.
//
// Exported so tests can exercise P > k without mutating global configuration.
// Production calls SelectCommittee; k <= 0 means "no cap", which seats the whole
// eligible set.
func SelectCommitteeWithSize(rc RoundContext, k int) ([]committee.Member, error) {
	if CommitteeV2Enabled && !seedAuthorityPinned() {
		return nil, ErrLegacySourceUnderV2
	}
	// H3 (2026-10-22 audit): the seats come from the fleet pool, WITHOUT this
	// node's block_buddy blocklist. The seats set n and the threshold, and the
	// seed-ranked draw ranks the whole pool, so drawing from a locally filtered
	// pool gave a blocklisting node a smaller committee (n=6, quorum 4 where
	// every peer had n=7, quorum 5) or a different committee altogether, and it
	// accepted blocks its peers rejected. A blocked peer keeps its seat here;
	// TallyAgainst does not count its vote. Same rule as VerifyCertificate
	// (CON-12) and the entropy fold (D-36).
	snap, err := fleetCommitteeSnapshotFor(uint64(rc.SelectionPeriod))
	if err != nil {
		return nil, err
	}

	// Resolved once and reused by the determinism log below, so the log can
	// never report entropy from a different source than the seed was built
	// from.
	seedSrc, err := SeedSourceFor(rc.EntropyEpoch)
	if err != nil {
		return nil, err
	}

	seed, err := committee.DeriveSeed(seedSrc, committee.SeedInput{
		EntropyEpoch: rc.EntropyEpoch,
		PrevHash:     rc.PrevHash,
		Height:       rc.Height,
		Period:       rc.Period,
	})
	if err != nil {
		return nil, err
	}

	if k <= 0 || k > len(snap.Members) {
		// No cap configured, or the pool is not larger than the cap: seat
		// everyone. This is today's shape (MaxValidators = 7 = pool size), where
		// the draw is a determinism fix rather than a sampling one.
		k = len(snap.Members)
	}

	members, err := committee.CommitteeFor(seed, snap, k)
	if err != nil {
		return nil, err
	}

	// Cross-node determinism check (operator-facing, not debug-only): same
	// Height/Period/EntropyEpoch must produce the same entropy_sha256, the
	// same seed, and the same ordered member list on every node. Members are
	// logged in CommitteeFor's OWN return order (selection rank, not
	// re-sorted here), so a log diff also catches a ranking disagreement,
	// not just a membership one. entropy_sha256 is a hash of the raw
	// entropy bytes, not the bytes themselves — sufficient to confirm
	// equality across nodes without printing raw salt/beacon material.
	// This second EpochEntropy call is redundant with the one inside
	// DeriveSeed above but is read-only and side-effect-free (SaltSource
	// returns a static config value; BeaconSource takes an RLock over an
	// in-memory map) — safe to call again purely for observability, and its
	// error is intentionally swallowed here: it must never change this
	// function's return value or error behavior, only what gets logged.
	memberIDs := make([]string, len(members))
	for i, m := range members {
		memberIDs[i] = m.PeerID
	}
	evt := log.Info().
		Uint64("height", rc.Height).
		Uint64("period", rc.Period).
		Uint64("entropy_epoch", uint64(rc.EntropyEpoch)).
		Uint64("selection_period", uint64(rc.SelectionPeriod)).
		Str("seed", seed.String()).
		Int("committee_size", len(members)).
		Str("committee_members", strings.Join(memberIDs, ","))
	if entropy, entropyErr := seedSrc.EpochEntropy(rc.EntropyEpoch); entropyErr == nil {
		evt = evt.Str("entropy_sha256", fmt.Sprintf("%x", sha256.Sum256(entropy)))
	}
	evt.Msg("committee: buddy committee selected")

	return members, nil
}

// ErrCommitteeNotPinned is returned when consensus.require_pinned_committee is
// set but the wired eligibility source cannot serve a specific selection epoch.
//
// Fail closed rather than silently substituting the current set: substituting is
// precisely the W1 defect, and it is invisible until two nodes disagree.
var ErrCommitteeNotPinned = errors.New(
	"committee: consensus.require_pinned_committee is set but the eligibility source " +
		"cannot serve a specific selection epoch (fail closed; see W1 pool pinning)")

// pinnedEligibleForEpoch resolves the candidate pool for one selection epoch.
//
// # THIS IS THE W1 SEAM. Read this before changing anything below it.
//
// The seed a committee is ranked with is derived from the BLOCK
// (RoundContextForBlock -> DeriveSeed). The pool it ranks is, today, read LIVE
// from the eligibility source — so two nodes that resolve the pool either side
// of a membership change rank the same seed over different candidates, seat
// different committees, and compute different n (hence different
// T = ceil(2n/3)). Live: a certificate one node finalises, another rejects.
// Retroactively: "who was seated at block 95?" changes every time membership
// changes, so a syncing node cannot re-derive a committee that has already
// voted.
//
// Two things keep that latent right now, and both are about to be removed:
//  1. JMDN_COMMITTEE_V2 is off, so this function is not on the live path;
//  2. MaxValidators(7) >= pool(7), so SelectCommitteeWithSize takes the
//     seat-everyone branch and the draw is a no-op. Onboarding validator #8
//     arms it.
//
// The fix is to resolve the pool from the epoch's FROZEN authority-signed
// snapshot. The artifact already exists and is self-describing: the signed
// canonical bytes are "jmdt/committee/v1|<epoch>|<seed>|<peer_id:bls_pub,...>",
// so a stored snapshot proves which epoch it belongs to from its signature
// alone. What is missing is only storage/retrieval:
//   - the seed node serving GetCommitteeSnapshot(epoch) for a PAST epoch, or
//   - jmdn persisting each epoch's snapshot WITH its signature and re-verifying
//     on read (an unsigned local cache would be unauthenticated state defining
//     history permanently — do not do that).
//
// Until one of those exists, require_pinned_committee stays false and this
// returns the live set, which is byte-identical to the previous behaviour.
// Turning it on before a source can serve epochs fails every round closed,
// loudly, which is the intended failure direction.
//
// NOTE also: with consensus.committee_epoch_blocks == 0, EpochForHeight returns
// 0 for every height, so "pin per epoch" pins all of history to a single
// snapshot. Setting a real epoch length is a prerequisite for this to mean
// anything, and is itself a coordinated fleet-wide change.
func pinnedEligibleForEpoch(epoch uint64) (map[string]string, error) {
	// W1 fix: once this selection period's pool is chain-anchored
	// (committee_anchor.go), it comes from the anchor block - identical on
	// every node, at any time - regardless of require_pinned_committee.
	if pool, handled, err := anchoredPoolForPeriod(epoch, true); handled {
		return pool, err
	}
	if !requirePinnedCommittee() {
		// UNPINNED — current behaviour, preserved exactly. The pool is whatever
		// the source considers current; the epoch argument is not consulted.
		return eligibleMembersUncapped()
	}
	eligible, err := eligibleMembersUncappedForEpoch(epoch, true)
	if err != nil {
		return nil, fmt.Errorf("%w: epoch %d: %v", ErrCommitteeNotPinned, epoch, err)
	}
	return eligible, nil
}

// requirePinnedCommittee reports consensus.require_pinned_committee, tolerating
// an unloaded settings singleton the way blockedBuddies and committeeSizeLimit
// do. Default false => today's behaviour.
func requirePinnedCommittee() bool {
	if !settings.IsLoaded() {
		return false
	}
	return settings.Get().Consensus.RequirePinnedCommittee
}

// fleetCommitteeSnapshotFor builds the pure-package snapshot from the
// authenticated eligible set — WITHOUT the alphabetical cap and WITHOUT this
// node's local block_buddy blocklist, the pool every honest node agrees on —
// PINNED BY SelectionPeriod (the block-height clock, EpochForHeight) when
// require_pinned_committee is on. Block committees are seated from it
// (SelectCommitteeWithSize), and the entropy fold sizes its quorum from it.
//
// Dropping the cap here is F2. The cap's purpose was to make every node compute
// the same n and therefore the same threshold; seed-ranked selection preserves
// that property exactly, and adds rotation the cap could never have.
//
// Dropping the blocklist is D-36 and H3: anything that sizes a THRESHOLD, or
// any other value that must be identical across the fleet, must not move when
// one operator edits their own config. The local blocklist only decides whose
// vote this node counts. See FleetEligibleForEpoch for the full reasoning and
// the CON-12 precedent in VerifyCertificate.
//
// Callers MUST pass a SelectionPeriod value here, never a raw EntropyEpoch —
// the two are different clocks with different divisors (committee_epoch_blocks
// vs the fixed 50-slot entropy window) and are not interchangeable once
// pinning is live. See SelectEntropyCommittee's doc comment for the call site
// this bit — it deliberately does NOT go through this function.
func fleetCommitteeSnapshotFor(epoch uint64) (committee.Snapshot, error) {
	// W1: the fleet-agreed (no blocklist) anchored pool for this period.
	if pool, handled, err := anchoredPoolForPeriod(epoch, false); handled {
		if err != nil {
			return committee.Snapshot{}, err
		}
		return snapshotFromEligible(epoch, pool), nil
	}
	var eligible map[string]string
	var err error
	if requirePinnedCommittee() {
		eligible, err = FleetEligibleForEpoch(epoch, true)
		if err != nil {
			return committee.Snapshot{}, fmt.Errorf("%w: epoch %d: %v", ErrCommitteeNotPinned, epoch, err)
		}
	} else {
		// UNPINNED — mirrors pinnedEligibleForEpoch's unpinned branch exactly:
		// the pool is whatever the source considers current and the epoch
		// argument is not consulted.
		eligible, err = FleetEligibleForEpoch(epoch, false)
		if err != nil {
			return committee.Snapshot{}, err
		}
	}
	return snapshotFromEligible(epoch, eligible), nil
}

// snapshotFromEligible builds the pure-package Snapshot from an
// already-resolved eligible set — the part of fleetCommitteeSnapshotFor that
// has nothing to do with HOW the set was resolved (pinned-by-epoch, or live).
// Shared by fleetCommitteeSnapshotFor (SelectionPeriod-pinned, block
// committees) and SelectEntropyCommittee (always-live, entropy committee).
func snapshotFromEligible(epoch uint64, eligible map[string]string) committee.Snapshot {
	ids := make([]string, 0, len(eligible))
	for pid := range eligible {
		ids = append(ids, pid)
	}
	// Canonical order before constructing the snapshot. CommitteeFor sorts
	// internally too, but Go map iteration is randomised and this keeps the
	// intermediate value stable for logging.
	sort.Strings(ids)

	members := make([]committee.Member, 0, len(ids))
	for _, pid := range ids {
		members = append(members, committee.Member{
			PeerID: pid,
			BLSPub: blsKeyBytes(eligible[pid]),
			Weight: UniformSelectionWeight,
		})
	}
	return committee.Snapshot{Epoch: epoch, Members: members}
}

// blsKeyBytes decodes a bls_pub to raw bytes for the binding comparison, making
// it independent of case and of any 0x prefix. A key that will not decode is
// treated as ABSENT rather than as a mismatch, which preserves today's
// behaviour under the legacy source (empty keys).
func blsKeyBytes(hexKey string) []byte {
	s := normalizeBLSPub(hexKey)
	if s == "" {
		return nil
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return raw
}

// SeedSourceFor returns the entropy source for an epoch.
//
// THIS IS THE STAGE-2 SEAM. Stage 1 is a configured salt. Stage 2 returns the
// RANDAO+VDF beacon (committee.BeaconSource) and nothing else in this file, or
// anywhere downstream, changes.
//
// FAIL-CLOSED SINCE 2026-09-03. The three states are now distinct, where
// two of them used to collapse into the same silent salt:
//
//	no beacon installed          -> SaltSource, nil   (Stage 1, uniform fleet-wide: safe)
//	beacon installed, has epoch  -> BeaconSource, nil (Stage 2)
//	beacon installed, no epoch   -> nil, ErrBeaconEpochUnavailable
//
// The third case used to return the salt. That is the whole silent-divergence
// seam: beacon.Has is a PER-NODE map lookup, so a node that missed the fold
// (restarted, fast-synced, failed to record a slot) took the salt branch
// while its peers took the beacon branch, drew a DIFFERENT committee, and
// produced below-threshold certificates that named no cause. Uniform-wrong
// is safe; per-node-different is a halt. Refusing to select is the same
// liveness outcome with a named error instead of a mystery.
//
// This mirrors SelectEntropyCommittee's existing discipline
// (ErrNoBeaconInstalled / ErrEntropyUnavailable, entropy_committee.go) and
// committee.BeaconSource.EpochEntropy's own instruction: "Callers MUST fail
// closed on it. Falling back to a default seed would let two nodes - one
// with the entropy, one without - seat different committees, which is worse
// than refusing the block."
func SeedSourceFor(epoch committee.EntropyEpoch) (committee.SeedSource, error) {
	beacon := activeBeacon()
	if beacon == nil {
		// Stage 1. Every node in the fleet is in this state together, because
		// the beacon is installed from network-wide operator configuration
		// (Sequencer.InstallAVCBeaconFromEnv), not from local observation.
		return committee.SaltSource{Salt: stage1Salt()}, nil
	}
	if beacon.Has(uint64(epoch)) {
		return beacon, nil
	}
	log.Error().
		Uint64("entropy_epoch", uint64(epoch)).
		Msg("committee: beacon is installed but has no entropy for this epoch — refusing to select a committee. " +
			"This node cannot agree with peers that DO have it, so selecting from the Stage-1 salt here would " +
			"seat a different committee and produce certificates that fail below threshold with no named cause. " +
			"Recover the epoch (accept a peer's VDF proof, or rehydrate persisted beacon state) before this node can vote")
	return nil, fmt.Errorf("%w: epoch %d", ErrBeaconEpochUnavailable, epoch)
}

// ErrBeaconEpochUnavailable is returned by SeedSourceFor when a beacon IS
// installed but has published nothing for the requested epoch.
//
// Deliberately distinct from ErrNoBeaconInstalled (entropy_committee.go),
// which means "no beacon exists at all" and is the legitimate Stage-1 state.
// This one means "the fleet is on Stage 2 and this node is missing a value
// its peers have" — a local recovery problem, not a configuration one.
var ErrBeaconEpochUnavailable = errors.New("messaging: beacon installed but no entropy published for this epoch (fail closed)")

// stage1Salt binds the salt to the pinned authority key when one is configured,
// so two networks with different authorities cannot share a committee schedule.
func stage1Salt() []byte {
	salt := []byte("jmdt/committee/v1")
	if settings.IsLoaded() {
		if s := strings.TrimSpace(settings.Get().Consensus.SeedAuthorityBLSPub); s != "" {
			salt = append(salt, []byte("|"+strings.ToLower(s))...)
		}
	}
	return salt
}

// beaconSource is the optional Stage-2 entropy source, wired at startup once
// RANDAO+VDF is deployed. Nil means Stage 1.
var beaconSource *committee.BeaconSource

// SetBeaconSource installs the Stage-2 beacon. Call once at startup.
func SetBeaconSource(b *committee.BeaconSource) { beaconSource = b }

func activeBeacon() *committee.BeaconSource { return beaconSource }

// WarmupPeerIDsForEpoch returns the peers the sequencer must have a live
// connection to before a round can reach quorum, PINNED to epoch (the round's
// SelectionPeriod).
//
// This is NOT the committee. It is the connectivity pool the committee is
// drawn from, and under v2 the two are deliberately different sizes.
//
// WHY IT CANNOT JUST BE EligibleCommitteePeerIDs: that returns the CAPPED set -
// the k alphabetically-first peers. Under v2 the seated committee is drawn from
// the whole uncapped pool and rotates every height, so warming up only the
// capped prefix would leave the sequencer unable to dial the peers it is about
// to seat. They would never receive a vote request, never sign, and the
// certificate would sit below threshold: a halt, produced by a warmup that
// looked correct.
//
// F-7: THIS MUST STAY POOL-IDENTICAL TO fleetCommitteeSnapshotFor, minus this
// node's local block_buddy blocklist. The seats include blocked peers (H3:
// they still size n), but this node does not count their votes, so it does
// not dial them either. Both pools used to
// call eligibleMembersUncapped() (the live view), and stayed identical only
// because pinnedEligibleForEpoch's pinned branch is not reachable yet (W1's
// prerequisite - see its own doc comment). The moment
// consensus.require_pinned_committee flips on, the seat snapshot draws the
// SEATED committee from the epoch-FROZEN snapshot while this function, called
// unconditionally live, would keep authorizing warmup/candidate membership
// against the CURRENT pool. A peer seated from the frozen snapshot but since
// dropped from the live pool would then be unreachable AND unauthorized to be
// added back by any seat-resolution step that gates on this function's return
// value - reopening, one layer up, the exact seat/candidate pool mismatch such
// a step would exist to close. Routing through pinnedEligibleForEpoch(epoch)
// instead closes it: with pinning off this is byte-identical to the old
// always-live call (pinnedEligibleForEpoch's own unpinned branch ignores epoch
// and reads the live source, same as before); with pinning on, it resolves the
// SAME frozen snapshot fleetCommitteeSnapshotFor uses for this epoch, so the
// two pools cannot diverge again.
//
// With v2 off this returns exactly what it returned before - the capped set -
// so the warmup path is unchanged until the flag flips.
func WarmupPeerIDsForEpoch(epoch SelectionPeriod) (map[string]struct{}, error) {
	if !CommitteeV2Enabled {
		return EligibleCommitteePeerIDs()
	}
	pool, err := pinnedEligibleForEpoch(uint64(epoch))
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(pool))
	for pid := range pool {
		set[pid] = struct{}{}
	}
	return set, nil
}

// WarmupPeerIDs is WarmupPeerIDsForEpoch(0) - the always-live view (epoch 0
// under the unpinned branch is never actually consulted; see
// pinnedEligibleForEpoch). Kept for callers with no round context yet.
//
// Once consensus.require_pinned_committee is on, epoch 0 is a REAL, distinct
// selection epoch (see pinnedEligibleForEpoch's own note on
// committee_epoch_blocks == 0), so this is NOT a safe stand-in for a caller's
// actual round epoch under pinning - a caller that knows its epoch (e.g.
// Consensus.Start, via messaging.EpochForHeight(zkblock.BlockNumber)) MUST
// call WarmupPeerIDsForEpoch directly, which is what F-7 changed it to do.
func WarmupPeerIDs() (map[string]struct{}, error) {
	return WarmupPeerIDsForEpoch(0)
}

// SeatedPeerIDs returns the seated committee for a round as a lookup set.
//
// The sequencer uses it to decide which buddies to put on the wire, so the
// peers ASKED to vote are the peers the verifier will COUNT. That identity is
// the whole of F1.
func SeatedPeerIDs(rc RoundContext) (map[string]struct{}, error) {
	seated, err := SelectCommittee(rc)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(seated))
	for _, m := range seated {
		set[m.PeerID] = struct{}{}
	}
	return set, nil
}

// DialTargetsForRound is the node-local view: the seated committee minus this
// node. Selection is global and self-included; only dialling excludes self.
// That split is the fix for FilterEligible (avc/nodeselection/.../filter.go:39),
// which did both jobs in one function and made the eligible set node-local.
func DialTargetsForRound(seated []committee.Member, selfPeerID string) []committee.Member {
	return committee.DialTargets(seated, selfPeerID)
}

// ---------------------------------------------------------------------------
// The tally
// ---------------------------------------------------------------------------

// blsVerifier adapts the production BLS verifier to committee.SigVerifier.
//
// CRITICAL - `bindings` is the ORIGINAL caller string, not a re-encoding.
// VerifyForBlock feeds it straight into CanonicalVoteMessageV3, so it is hashed
// into the signed message byte for byte. Decoding block.BlockHash.Hex() and
// re-encoding it would change the message on any casing or prefix difference
// and fail EVERY signature - a total halt, not a degraded mode.
//
// The original BLSresponse is preserved for the same reason at one remove: its
// PubKey and Signature are hex-decoded rather than hashed, so casing is
// harmless, but silently stripping a "0x" prefix would ACCEPT votes the current
// code rejects. Behaviour must be identical, so the original is verified.
type blsVerifier struct {
	bindings      string
	consensusHash string // must be non-empty — VerifyForBlock rejects an empty ConsensusHash
	orig          map[string]BLS_Signer.BLSresponse
	// seated maps the seated committee's peer ids to their snapshot-bound BLS
	// pubkey (normalized hex; "" when the snapshot carried no binding). Used
	// ONLY to bound the legacy-vote classification below. It never affects
	// what verifies: committee.Tally owns the membership and key-binding
	// decisions and makes them after this verifier returns.
	seated map[string]string
}

// voteKey identifies one response by peer, public key AND signature. The key
// must be part of it: committee.Tally checks the key binding against the
// vote's own BLSPub, so VerifyVote has to verify with that same key. Keyed by
// (peer, sig) alone, a second response {P, attackerKey, S} overwrote
// {P, P's bound key, S}; the first then verified under the attacker's key and
// passed the binding under P's, so one attacker signature counted once for
// every seated member (2026-10-22 audit, C1).
func voteKey(peerID string, pub, sig []byte) string {
	return peerID + "|" + hex.EncodeToString(pub) + "|" + hex.EncodeToString(sig)
}

// seatedPeerKeys indexes the seated committee by peer id, carrying each
// member's bound BLS pubkey. Used only to bound the legacy-vote classification
// in VerifyVote — never to decide membership, which is committee.Tally's job.
func seatedPeerKeys(seated []committee.Member) map[string]string {
	out := make(map[string]string, len(seated))
	for _, m := range seated {
		key := ""
		if len(m.BLSPub) > 0 {
			key = normalizeBLSPub(hex.EncodeToString(m.BLSPub))
		}
		out[m.PeerID] = key
	}
	return out
}

func (b blsVerifier) VerifyVote(v committee.Vote, chainID, height uint64, _ []byte) bool {
	resp, ok := b.orig[voteKey(v.PeerID, v.BLSPub, v.Signature)]
	if !ok {
		return false
	}
	vote := int8(-1)
	if v.Approve {
		vote = 1
	}
	if BLS_Verifier.VerifyForBlock(resp, chainID, height, b.bindings, b.consensusHash, vote) == nil {
		return true
	}
	if !RejectLegacyVotes {
		if BLS_Verifier.Verify(resp, vote) == nil {
			metrics.ConsensusLegacyVotesTotal.WithLabelValues(metrics.LegacyVoteSiteCommitteeV2).Inc()
			log.Warn().Str("peer", v.PeerID).Msg("legacy (unbound) BLS vote accepted (reject_legacy_votes is off)")
			return true
		}
		return false
	}
	// reject_legacy_votes is ON: refused either way, but classify so the
	// metric detects a validator still signing the legacy domain.
	//
	// Bounded to seated members on purpose. committee.Tally calls this
	// verifier BEFORE its membership check — deliberately, so a forged vote
	// never reaches the membership map — and the vote slice is
	// attacker-supplied with no length cap, so classifying every failure
	// would sell a flood of garbage signatures a second BLS verification each.
	//
	// Key-bound on purpose: Verify checks the signature against the pubkey
	// carried in the response, so a seated peer_id alone would let anyone
	// fire this detector against an innocent validator with a key of their
	// own. Same binding rule committee.Tally applies to acceptance.
	if legacyDetectable(b.seated, resp) && BLS_Verifier.Verify(resp, vote) == nil {
		metrics.ConsensusLegacyVotesTotal.WithLabelValues(metrics.LegacyVoteSiteCommitteeV2).Inc()
		log.Warn().Str("peer", v.PeerID).Msg("legacy (unbound) BLS vote detected and refused (reject_legacy_votes is on)")
	}
	return false
}

// VerifyCertificateForRound is the new entry point for the three certificate
// call sites.
//
// With CommitteeV2Enabled false it delegates to VerifyCertificate and is
// byte-identical to today. With it true, the tally runs against the SEATED
// committee that SelectCommittee produced - the same one the voters used.
//
// MIGRATION NOTE ON Period: callers that do not yet track round timeouts pass
// 0. That is correct but incomplete - with Period pinned at 0 a timed-out round
// re-derives the SAME committee, so an offline or hostile committee can stall
// the height. Thread the real period through as soon as the round loop tracks
// it; the seed already accounts for it.
func VerifyCertificateForRound(
	responses []BLS_Signer.BLSresponse,
	blockHashHex string,
	consensusHashHex string,
	height uint64,
	rc RoundContext,
) (CertificateResult, error) {
	if !CommitteeV2Enabled {
		// Byte-identical to today: the legacy verifier over the alphabetically
		// capped eligible set. rc is ignored on this path by design - the legacy
		// selection has no notion of a round.
		return VerifyCertificate(responses, blockHashHex, consensusHashHex, height)
	}

	seated, err := SelectCommittee(rc)
	if err != nil {
		return CertificateResult{}, err
	}
	return TallyAgainst(responses, seated, blockHashHex, consensusHashHex, height)
}

// VerifySyncedBlockCertificate is ThebeSync's certificate check.
//
// Anchored period under committee-v2: the live path's check, against the
// block's SEATED committee from the chain-anchored pool. Exact and deterministic.
//
// Everything else (today's testnet: committee-v2 on, committee_anchor_activation_height
// 0): the legacy verifier first — byte-identical to before when it passes — and,
// ONLY when that falls short of quorum, two wider tallies before giving up:
//
//  1. the live receive path's check (SelectCommittee over the current pool), the
//     same thing every peer that accepted this block live ran;
//  2. a POOL-WIDE tally: distinct verified +1 votes from ANY member of the
//     fleet-eligible pool, against the fixed committee size k
//     (consensus.max_validators) and threshold ceil(2k/3). NOT available once
//     committee anchoring is configured (see poolWideSyncFallbackAllowed):
//     anchoring is what lets a node prove a block's committee, and production
//     refuses to boot without it, so this is a bridge for chains that have not
//     turned anchoring on.
//
// WHY (2026-10-13/15, six incidents): without an anchor nothing on this node can
// reproduce the committee a past block was sealed under. The legacy verifier
// counts only the alphabetically-first k of the CURRENT pool, the round check
// re-draws from the CURRENT pool; both drift from the sealing committee as the
// seed snapshot changes, and then every block sealed by a different seven is
// "3/5 over committee 7" forever — a node one block behind can never rejoin
// (node-1 at 1761, all 8 WAN nodes at 2127). Pool membership itself is stable
// and seed-authenticated, and every vote is block-bound (chain id, height, hash,
// consensus hash), so "≥ 2f+1 distinct registered validators signed exactly
// this block" is the strongest statement this node CAN verify for an
// unanchored past block. It is only reachable on the catch-up path — never on
// live acceptance, never on the sequencer's own finalisation — and it is
// superseded, per period, the moment an anchor exists. Accepting on a fallback
// is logged at WARN with the counts so the frequency is visible.
func VerifySyncedBlockCertificate(block *config.ZKBlock, responses []BLS_Signer.BLSresponse) (CertificateResult, error) {
	blockHashHex, consensusHashHex, height := block.BlockHash.Hex(), block.ConsensusHashHex(), block.BlockNumber
	res, err := verifySyncedBlockCertificateStrict(block, responses)
	return relaxForTrustedSequencer("thebesync", res, err, responses, blockHashHex, consensusHashHex, height)
}

// verifySyncedBlockCertificateStrict is VerifySyncedBlockCertificate without
// the TrustSequencerCertificate override.
func verifySyncedBlockCertificateStrict(block *config.ZKBlock, responses []BLS_Signer.BLSresponse) (CertificateResult, error) {
	blockHashHex, consensusHashHex, height := block.BlockHash.Hex(), block.ConsensusHashHex(), block.BlockNumber
	if CommitteeV2Enabled && periodIsAnchored(EpochForHeight(height)) {
		// Stage 2: this block's committee is seeded by ENTROPY(EpochForSlot(slot)).
		// On a boundary block that entropy arrives in the block's own VdfProof, so
		// adopt the proof first. Safe before the certificate: the proof is checked
		// against this node's own mix(E-1) and the pinned group and T, so a valid
		// proof is the one correct value whoever relays it, and Publish is idempotent.
		adoptSyncedBoundaryProof(block)
		rc := RoundContextForBlock(block)
		return VerifyCertificateForRound(responses, blockHashHex, consensusHashHex, height, rc)
	}

	legacy, err := VerifyCertificate(responses, blockHashHex, consensusHashHex, height)
	if err != nil || legacy.Reached {
		return legacy, err
	}

	// Fallback 1: the live receive path's check.
	if CommitteeV2Enabled {
		adoptSyncedBoundaryProof(block)
		rc := RoundContextForBlock(block)
		if round, verr := VerifyCertificateForRound(responses, blockHashHex, consensusHashHex, height, rc); verr == nil && round.Reached {
			log.Warn().Uint64("height", height).
				Int("legacy_yes", legacy.YesVotes).Int("legacy_threshold", legacy.Threshold).
				Int("seated_yes", round.YesVotes).Int("seated_threshold", round.Threshold).
				Msg("thebesync: certificate accepted on the seated-committee check; legacy alphabetical cap fell short (unanchored period)")
			return round, nil
		}
	}

	// Fallback 2: pool-wide tally. Testnet-only. It counts votes from ANY
	// current pool member against ceil(2k/3) of k, regardless of pool size, so
	// a key that joined the pool after this block was sealed can help certify
	// it. Once anchoring is configured the bridge is closed: a block below the
	// anchored start is checked on the legacy and seated tallies only, and a
	// block whose committee cannot be proven is rejected instead.
	if !poolWideSyncFallbackAllowed() {
		log.Warn().Uint64("height", height).
			Int("legacy_yes", legacy.YesVotes).Int("legacy_threshold", legacy.Threshold).
			Msg("thebesync: certificate below quorum on the legacy and seated checks; pool-wide fallback is disabled because committee anchoring is configured, rejecting")
		return legacy, nil
	}
	pool, perr := verifySyncedCertificatePoolWide(responses, blockHashHex, consensusHashHex, height)
	if perr == nil && pool.Reached {
		log.Warn().Uint64("height", height).
			Int("legacy_yes", legacy.YesVotes).Int("legacy_threshold", legacy.Threshold).
			Int("pool_yes", pool.YesVotes).Int("pool_threshold", pool.Threshold).Int("pool_size", pool.PoolSize).
			Msg("thebesync: certificate accepted on the pool-wide tally; this block's seated committee is not reproducible without an anchor")
		return pool.CertificateResult, nil
	}
	return legacy, nil
}

// poolWideSyncFallbackAllowed reports whether the pool-wide tally may be used on
// the sync path. It is allowed only while committee anchoring is NOT configured.
//
// It deliberately does not read IsProductionPosture: that helper treats an
// unset network.environment as "mainnet" (a fail-safe default chosen for the
// boot-refusal checks), so on a node nobody configured it is true, and a gate
// built on it would switch the fallback off for every unconfigured node and
// test run. Whether anchoring is on is an explicit, fleet-agreed setting, and
// ValidateProductionConsensusPosture already refuses to boot a production node
// running committee-v2 without it, so on production this fallback is never
// reachable.
func poolWideSyncFallbackAllowed() bool { return !CommitteeAnchoringEnabled() }

// poolWideResult is a CertificateResult plus the size of the pool the +1
// votes were counted over (CommitteeSize stays k, the threshold base).
type poolWideResult struct {
	CertificateResult
	PoolSize int
}

// verifySyncedCertificatePoolWide counts distinct verified +1 votes from ANY
// member of the fleet-eligible pool (uncapped, no local blocklist — the
// fleet-agreed set, so every node sizes the threshold the same), except peers
// this node blocks, whose votes it never counts. Signature
// verification and the peer_id↔bls_pub binding are the shared
// countEligibleYes rules; only the membership set is wider.
//
// Threshold: the larger of ceil(2k/3) (k = consensus.max_validators, or the
// pool size when smaller — what an honest seated committee produces) and
// f_pool+1 with f_pool = (N-1)/3 over the whole pool. Counting over a wider
// set than the seats means up to f_pool pool keys may be faulty; f_pool+1
// guarantees at least one honest signer, so faulty pool members — including
// keys that joined after the block — cannot certify a block on their own. When
// f_pool+1 exceeds k the fallback cannot succeed and the block needs an
// anchored committee instead.
//
// The pool is resolved for the block's own selection period when the source
// can (pinned), else the current pool.
func verifySyncedCertificatePoolWide(responses []BLS_Signer.BLSresponse, blockHashHex, consensusHashHex string, height uint64) (poolWideResult, error) {
	var res poolWideResult
	epoch := EpochForHeight(height)
	pool, err := FleetEligibleForEpoch(epoch, true)
	if err != nil || len(pool) == 0 {
		pool, err = FleetEligibleForEpoch(epoch, false)
	}
	if err != nil {
		return res, err
	}
	if len(pool) == 0 {
		return res, fmt.Errorf("pool-wide certificate check: fleet-eligible pool is empty (fail closed)")
	}
	k := committeeSizeLimit()
	if k <= 0 || k > len(pool) {
		k = len(pool)
	}
	res.PoolSize = len(pool)
	res.CommitteeSize = k
	res.Threshold = ByzantineQuorum(k)
	if floor := (len(pool)-1)/3 + 1; floor > res.Threshold {
		res.Threshold = floor
	}
	// Sized over the whole pool above; a blocked peer's vote is not counted.
	res.YesVotes = countEligibleYes(responses, blockHashHex, consensusHashHex, height, withoutBlockedBuddies(pool), true)
	res.Reached = res.YesVotes >= res.Threshold
	return res, nil
}

// TallyAgainst counts a certificate against an explicitly supplied committee.
//
// Everything the existing verifier guarantees is preserved: YES-only counting,
// dedupe by peer_id AND bls_pub, the peer_id-to-bls_pub binding, T = ceil(2n/3)
// fixed from n before any vote is read, and fail-closed on every error. The
// only change is WHERE the committee comes from.
func TallyAgainst(
	responses []BLS_Signer.BLSresponse,
	seated []committee.Member,
	blockHashHex string,
	consensusHashHex string,
	height uint64,
) (CertificateResult, error) {
	var out CertificateResult

	// Decoded only to satisfy TallyInput.BlockHash's non-emptiness check; the
	// verifier uses blockHashHex verbatim.
	blockHash, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(blockHashHex), "0x"))
	if err != nil {
		return out, fmt.Errorf("committee: bad block hash %q: %w", blockHashHex, err)
	}

	// A blocked peer keeps its seat in `seated` (it sizes n and the threshold,
	// H3), but its vote is not counted on this node: blocking can only make
	// quorum harder (CON-12).
	blocked := blockedBuddies()
	votes := make([]committee.Vote, 0, len(responses))
	orig := make(map[string]BLS_Signer.BLSresponse, len(responses))
	for _, r := range responses {
		if _, isBlocked := blocked[strings.TrimSpace(r.PeerID)]; isBlocked {
			continue
		}
		sigBytes, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(r.Signature), "0x"))
		if err != nil {
			// An undecodable signature can never verify; drop it rather than
			// pass garbage into the tally.
			continue
		}
		v := committee.Vote{
			PeerID:    r.PeerID,
			BLSPub:    blsKeyBytes(r.PubKey),
			Approve:   r.Agree,
			Signature: sigBytes,
		}
		votes = append(votes, v)
		orig[voteKey(v.PeerID, v.BLSPub, v.Signature)] = r
	}

	res, err := committee.Tally(committee.TallyInput{
		ChainID:   BLS_Signer.DomainChainID(),
		Height:    height,
		BlockHash: blockHash,
		Committee: seated,
		Votes:     votes,
		Verifier:  blsVerifier{bindings: blockHashHex, consensusHash: consensusHashHex, orig: orig, seated: seatedPeerKeys(seated)},
		// Members carrying a bound key are always checked. This flag only
		// decides whether an EMPTY bound key is tolerated, and the legacy
		// source supplies empty keys - so requiring the binding is safe exactly
		// when the authenticated snapshot is pinned.
		RequireKeyBinding: EnforceCommitteeRegistry && seedAuthorityPinned(),
	})
	if err != nil {
		return out, err
	}

	out.YesVotes = res.YesVotes
	out.CommitteeSize = res.CommitteeSize
	out.Threshold = res.Threshold
	out.Reached = res.Reached
	return out, nil
}

func seedAuthorityPinned() bool {
	return settings.IsLoaded() &&
		strings.TrimSpace(settings.Get().Consensus.SeedAuthorityBLSPub) != ""
}
