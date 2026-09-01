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

	"github.com/rs/zerolog/log"
)

// CommitteeV2Enabled gates every behaviour change in this file.
//
// DEFAULT FALSE. Turning it on changes which peers vote, so it must be flipped
// across the whole fleet together. See the rollout note above.
var CommitteeV2Enabled = envOn("JMDN_COMMITTEE_V2", false)

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
	// (committeeSnapshotFor) — the buddy committee's own clock, refreshed
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

// ErrPeriodNotSynced is returned by RoundContextForBlock when this node's
// locally-derived Period (DefaultPeriodStore.PeriodFor) disagrees with the
// Period the block itself claims (config.ZKBlock.Period, stamped by whoever
// proposed it).
//
// This is not a cosmetic mismatch: Period feeds DeriveSeed's SeedInput, so
// two nodes that silently used different Period values for the same height
// would compute two different committees for the same round - the exact
// failure PeriodStore's own struct-level comment warns about ("if any two
// nodes ever disagreed on a height's period, they would compute two
// different committees for the same round"). Fail closed instead: refuse to
// build a RoundContext rather than guess which side is right.
//
// This is a LOCAL READINESS signal, not evidence the block is wrong - the
// far more common case is this node simply hasn't yet processed/verified the
// TimeoutCertificate that advanced Period for this height (gossip lag, a
// node that just rejoined, a certificate still in flight). The fix on the
// caller's side is to catch up - e.g. via the existing multi-peer
// RequestLatestTimeoutCertificateFromPeers rejoin RPC (timeout_rejoin.go),
// which independently re-verifies whatever it fetches before trusting it -
// and retry, not to fall back to either side's claimed value unverified.
var ErrPeriodNotSynced = errors.New(
	"committee: local Period disagrees with the block's stamped Period (fail closed; this node may not have processed the certifying TimeoutCertificate yet)")

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
// Returns ErrPeriodNotSynced (fail closed, see its own doc comment) instead
// of a RoundContext when the block's own stamped b.Period disagrees with
// this node's locally-verified DefaultPeriodStore value. b.Period == 0 is
// treated as "caller doesn't track timeouts yet" (unchanged, pre-existing
// convention - see RoundContext.Period's own doc comment) and is never
// compared. The VALUE actually used, on a match, is still the local
// DefaultPeriodStore read, never the block's bare claim - b.Period is
// consulted only as a consistency check, never as the trusted source, so a
// block cannot steer its own committee seed by lying about its Period.
func RoundContextForBlock(b *config.ZKBlock) (RoundContext, error) {
	if b == nil {
		return RoundContext{}, nil
	}
	localPeriod := DefaultPeriodStore.PeriodFor(b.BlockNumber)
	if b.Period != 0 && b.Period != localPeriod {
		return RoundContext{}, fmt.Errorf("%w: height %d, local period %d, block claims period %d",
			ErrPeriodNotSynced, b.BlockNumber, localPeriod, b.Period)
	}
	rc := RoundContext{
		SelectionPeriod: SelectionPeriod(EpochForHeight(b.BlockNumber)),
		EntropyEpoch:    committee.EntropyEpoch(EpochForSlot(b.Slot)),
		PrevHash:        b.PrevHash.Bytes(),
		Height:          b.BlockNumber,
		// Period is chain-derived via DefaultPeriodStore (M0,
		// timeout_certificates.go): it advances only when a quorum-certified
		// TimeoutCertificate lands for this height, never from a local guess.
		// A height with no certificate yet reads back 0, matching the
		// pre-M0 behavior for the common case where a round never times out.
		// Cross-checked against the block's own stamped Period above -
		// this is the verified local value, not the block's bare claim.
		Period: localPeriod,
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
		Uint64("period", localPeriod).
		Uint64("entropy_epoch", uint64(rc.EntropyEpoch)).
		Uint64("selection_period", uint64(rc.SelectionPeriod)).
		Msg("committee: round context built")

	return rc, nil
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
	snap, err := committeeSnapshotFor(uint64(rc.SelectionPeriod))
	if err != nil {
		return nil, err
	}

	seed, err := committee.DeriveSeed(SeedSourceFor(rc.EntropyEpoch), committee.SeedInput{
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
	if entropy, entropyErr := SeedSourceFor(rc.EntropyEpoch).EpochEntropy(rc.EntropyEpoch); entropyErr == nil {
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

// committeeSnapshotFor builds the pure-package snapshot from the authenticated
// eligible set, WITHOUT the alphabetical cap, PINNED BY SelectionPeriod (the
// block-height clock, EpochForHeight) when require_pinned_committee is on.
//
// Dropping the cap here is F2. The cap's purpose was to make every node compute
// the same n and therefore the same threshold; seed-ranked selection preserves
// that property exactly, and adds rotation the cap could never have.
//
// Callers MUST pass a SelectionPeriod value here, never a raw EntropyEpoch —
// the two are different clocks with different divisors (committee_epoch_blocks
// vs the fixed 50-slot entropy window) and are not interchangeable once
// pinning is live. See SelectEntropyCommittee's doc comment for the call site
// this bit — it deliberately does NOT go through this function.
func committeeSnapshotFor(epoch uint64) (committee.Snapshot, error) {
	eligible, err := pinnedEligibleForEpoch(epoch)
	if err != nil {
		return committee.Snapshot{}, err
	}
	return snapshotFromEligible(epoch, eligible), nil
}

// snapshotFromEligible builds the pure-package Snapshot from an
// already-resolved eligible set — the part of committeeSnapshotFor that has
// nothing to do with HOW the set was resolved (pinned-by-epoch, or live).
// Shared by committeeSnapshotFor (SelectionPeriod-pinned, block committees)
// and SelectEntropyCommittee (always-live, entropy committee).
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
func SeedSourceFor(epoch committee.EntropyEpoch) committee.SeedSource {
	if beacon := activeBeacon(); beacon != nil && beacon.Has(uint64(epoch)) {
		return beacon
	}
	return committee.SaltSource{Salt: stage1Salt()}
}

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

// WarmupPeerIDs returns the peers the sequencer must have a live connection to
// before a round can reach quorum.
//
// This is NOT the committee. It is the connectivity pool the committee is drawn
// from, and under v2 the two are deliberately different sizes.
//
// WHY IT CANNOT JUST BE EligibleCommitteePeerIDs: that returns the CAPPED set -
// the k alphabetically-first peers. Under v2 the seated committee is drawn from
// the whole uncapped pool and rotates every height, so warming up only the
// capped prefix would leave the sequencer unable to dial the peers it is about
// to seat. They would never receive a vote request, never sign, and the
// certificate would sit below threshold: a halt, produced by a warmup that
// looked correct.
//
// With v2 off this returns exactly what it returned before - the capped set -
// so the warmup path is unchanged until the flag flips.
func WarmupPeerIDs() (map[string]struct{}, error) {
	if !CommitteeV2Enabled {
		return EligibleCommitteePeerIDs()
	}
	pool, err := eligibleMembersUncapped()
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(pool))
	for pid := range pool {
		set[pid] = struct{}{}
	}
	return set, nil
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
	consensusHash string // v4 binding; empty => v3 (block hash only)
	orig          map[string]BLS_Signer.BLSresponse
}

func voteKey(peerID string, sig []byte) string {
	return peerID + "|" + hex.EncodeToString(sig)
}

func (b blsVerifier) VerifyVote(v committee.Vote, chainID, height uint64, _ []byte) bool {
	resp, ok := b.orig[voteKey(v.PeerID, v.Signature)]
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
		return BLS_Verifier.Verify(resp, vote) == nil
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

	votes := make([]committee.Vote, 0, len(responses))
	orig := make(map[string]BLS_Signer.BLSresponse, len(responses))
	for _, r := range responses {
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
		orig[voteKey(v.PeerID, v.Signature)] = r
	}

	res, err := committee.Tally(committee.TallyInput{
		ChainID:   BLS_Signer.DomainChainID(),
		Height:    height,
		BlockHash: blockHash,
		Committee: seated,
		Votes:     votes,
		Verifier:  blsVerifier{bindings: blockHashHex, consensusHash: consensusHashHex, orig: orig},
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
