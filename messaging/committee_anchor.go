package messaging

// Chain-anchored committee pool (the W1 fix).
//
// THE DEFECT
//
// Under JMDN_COMMITTEE_V2 a block's committee is draw(seed, pool). The seed is
// derived from the block, so every node computes the same one. The POOL was
// read live from the seed node ("current" snapshot, keyed by wall-clock hour)
// whenever a node happened to need it. Two nodes checking the same block either
// side of a membership change, or an hour boundary, drew from different pools,
// seated different committees and disagreed about the block's certificate; a
// node syncing later re-derived old committees from today's pool. Pinning by
// SelectionPeriod could not fix it: the seed reads that number as a wall-clock
// epoch, builds a never-requested "past" epoch from TODAY's members, and
// treats epoch 0 as "current" (defaults.go: pinning was reverted for exactly
// this).
//
// THE FIX
//
// The pool comes from the chain. From the activation height on, every block
// whose number is a multiple of L (= consensus.committee_epoch_blocks) is an
// ANCHOR block: its proposer fetches the seed's CURRENT signed snapshot (the
// one request that always works) and carries it in CommitteeSnapshotAnchor,
// with CommitteeSnapshotHash = AnchorSnapshotHash(snapshot). That hash field is
// already covered by ConsensusHash, so the committee's v4 certificate signs it.
// Receivers check the seed-authority signature, the hash, and that the
// snapshot epoch never goes backwards. The anchor at height h defines the pool
// for selection period h/L + 1 - one full period of look-ahead - so the anchor
// block's own committee is always drawn from an already-fixed pool.
//
// Every consumer of the pool resolves through this file: the seated committee
// (sequencer and every verifier), the buddy tally, the timeout-vote pool, the
// reward-address map, the entropy (epoch) committee, the legacy verifier and
// the requester gate. So all of them, on every node, use the pool the chain
// says - not the pool the seed happened to say when each node asked.
//
// ROLLOUT
//
// consensus.committee_anchor_activation_height is a fleet-agreed consensus
// parameter (0 = off, unchanged behaviour). Below it nothing here runs. Every
// node must run this build and carry the same value before the chain reaches
// it: an old node relaying a block re-encodes it and drops the unknown field.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/config/settings"
	seedcommittee "gossipnode/seednode/committee"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
)

const committeeAnchorDomain = "jmdt/committee-anchor/v1"

// ErrCommitteeAnchorMissing: an anchored selection period has no pool this node
// can prove from the chain. Callers MUST fail closed - never fall back to the
// live pool, which is exactly the divergence this file removes.
var ErrCommitteeAnchorMissing = errors.New("committee anchor: no anchored pool for this selection period (fail closed)")

// entropyAnchorSearchLimit bounds how many periods the entropy-committee rule
// walks back looking for an anchor committed before an epoch's cutoff slot.
const entropyAnchorSearchLimit = 256

// ---------------------------------------------------------------------------
// Schedule
// ---------------------------------------------------------------------------

// firstCommitteeAnchorHeight is the activation height rounded up to a multiple
// of L, or 0 when anchoring is off (activation 0, or L == 0).
func firstCommitteeAnchorHeight() uint64 {
	if !settings.IsLoaded() {
		return 0
	}
	act := settings.Get().Consensus.CommitteeAnchorActivationHeight
	l := epochLengthBlocks()
	if act == 0 || l == 0 {
		return 0
	}
	if r := act % l; r != 0 {
		act += l - r
	}
	return act
}

// CommitteeAnchoringEnabled reports whether the chain-anchored pool is configured.
func CommitteeAnchoringEnabled() bool { return firstCommitteeAnchorHeight() > 0 }

// IsCommitteeAnchorHeight reports whether height must carry a committee anchor.
func IsCommitteeAnchorHeight(height uint64) bool {
	first := firstCommitteeAnchorHeight()
	return first > 0 && height >= first && height%epochLengthBlocks() == 0
}

// periodIsAnchored reports whether selection period's pool comes from an anchor.
// The first anchor (at height first) defines period first/L + 1.
func periodIsAnchored(period uint64) bool {
	first := firstCommitteeAnchorHeight()
	return first > 0 && period >= first/epochLengthBlocks()+1
}

// anchorDefinesPeriod is the selection period whose pool the anchor at height defines.
func anchorDefinesPeriod(height uint64) uint64 { return height/epochLengthBlocks() + 1 }

// ---------------------------------------------------------------------------
// Hash, verification
// ---------------------------------------------------------------------------

// AnchorSnapshotHash is the digest stamped in CommitteeSnapshotHash. It covers
// the exact authority-signed bytes plus the authority key and signature, with
// length-prefixed fields (committee.WriteField), so one hash names exactly one
// signed snapshot.
func AnchorSnapshotHash(snap *seedcommittee.CommitteeSnapshot) [32]byte {
	h := sha256.New()
	committee.WriteField(h, []byte(committeeAnchorDomain))
	committee.WriteField(h, seedcommittee.CanonicalCommitteeBytes(snap.Epoch, snap.Seed, snap.Entries))
	committee.WriteField(h, []byte(strings.ToLower(strings.TrimSpace(snap.AuthorityPubHex))))
	committee.WriteField(h, []byte(strings.ToLower(strings.TrimSpace(snap.Signature))))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func pinnedSeedAuthority() string {
	if !settings.IsLoaded() {
		return ""
	}
	return strings.TrimSpace(settings.Get().Consensus.SeedAuthorityBLSPub)
}

// parseAndVerifyAnchor decodes body and checks the seed-authority signature
// against the PINNED key and that it hashes to wantHash.
func parseAndVerifyAnchor(body string, wantHash []byte) (*seedcommittee.CommitteeSnapshot, error) {
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("anchor block carries no committee snapshot")
	}
	pin := pinnedSeedAuthority()
	if pin == "" {
		return nil, errors.New("no pinned seed authority key (fail closed)")
	}
	var snap seedcommittee.CommitteeSnapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		return nil, fmt.Errorf("malformed committee snapshot: %w", err)
	}
	if err := seedcommittee.VerifyCommitteeSnapshot(&snap, pin); err != nil {
		return nil, err
	}
	got := AnchorSnapshotHash(&snap)
	if len(wantHash) != len(got) || string(wantHash) != string(got[:]) {
		return nil, fmt.Errorf("snapshot does not match CommitteeSnapshotHash")
	}
	return &snap, nil
}

// anchorBindingError binds an anchor block's snapshot to what the committee
// signed. The seed signature only proves "the seed issued this membership at
// some point"; many such snapshots exist. Which one defines the next period is
// decided by the certificate: its v4 vote covers ConsensusHash, which covers
// CommitteeSnapshotHash, which covers the body. Without this a relay or a sync
// peer (thebesync verifies the certificate against the block's CLAIMED
// ConsensusHash and never recomputes it) could swap in a different, validly
// seed-signed snapshot and hand one node a different pool.
func anchorBindingError(b *config.ZKBlock) error {
	if (b.ConsensusHash == common.Hash{}) {
		return fmt.Errorf("anchor block %d carries no ConsensusHash, so its committee snapshot is not covered by the certificate", b.BlockNumber)
	}
	// Tx-hash form: identical to the contents form on every committed block, and
	// the only form that still matches after a ThebeDB round trip (the stored
	// transactions lose ChainID). See Security.RecomputeConsensusHashFromTxHashes;
	// its soundness needs BlockHash == H(tx.Hash...), which every caller has
	// already checked (checkBodyBinding live, thebesync step 1, own DB).
	if want := Security.RecomputeConsensusHashFromTxHashes(b); b.ConsensusHash != want {
		return fmt.Errorf("anchor block %d: ConsensusHash %s does not match its consensus fields %s (committee snapshot hash rewritten?)",
			b.BlockNumber, b.ConsensusHash.Hex(), want.Hex())
	}
	return nil
}

// checkCommitteeAnchor is the receive-path rule (validateRemoteBlock, ThebeSync
// apply). Anchor heights must carry a valid, hash-bound, non-regressing
// snapshot; every other block must carry neither snapshot nor anchor hash once
// anchoring is configured. No-op when anchoring is off (see the body).
func checkCommitteeAnchor(b *config.ZKBlock) *blockRejection {
	if b == nil {
		return nil
	}
	// Off (activation 0) is fully inert - exactly v3base, which does not know
	// the field and ignores it. This is also the rollback path: setting the
	// activation height back to 0 fleet-wide must not make a node reject the
	// anchor blocks already in the chain (history, sync, restarts). The body is
	// not covered by ConsensusHash, so ignoring it cannot change consensus.
	if !CommitteeAnchoringEnabled() {
		return nil
	}
	if b.BlockNumber < firstCommitteeAnchorHeight() {
		if b.CommitteeSnapshotAnchor != "" {
			return reject("unexpected_committee_anchor",
				"block %d carries a committee snapshot but is below the anchor activation height", b.BlockNumber)
		}
		return nil
	}
	if !IsCommitteeAnchorHeight(b.BlockNumber) {
		if b.CommitteeSnapshotAnchor != "" || len(b.CommitteeSnapshotHash) != 0 {
			return reject("unexpected_committee_anchor",
				"block %d is not an anchor height but carries a committee snapshot or anchor hash", b.BlockNumber)
		}
		return nil
	}
	if err := anchorBindingError(b); err != nil {
		return reject("committee_anchor_unbound", "%v", err)
	}
	snap, err := parseAndVerifyAnchor(b.CommitteeSnapshotAnchor, b.CommitteeSnapshotHash)
	if err != nil {
		return reject("committee_anchor_invalid", "anchor block %d: %v", b.BlockNumber, err)
	}
	// Monotonic: the membership epoch may repeat (seed unreachable, previous
	// snapshot re-carried) but never go backwards. A previous anchor that should
	// exist but cannot be read is a rejection, not a skip: the regression check
	// is exactly what protects a node that is missing history.
	prev, perr := anchoredRecord(b.BlockNumber / epochLengthBlocks())
	if perr != nil {
		return reject("committee_anchor_unverifiable",
			"anchor block %d: previous anchor unavailable, cannot prove the membership epoch does not regress: %v",
			b.BlockNumber, perr)
	}
	if prev != nil && snap.Epoch < prev.Snapshot.Epoch {
		return reject("committee_anchor_regressed",
			"anchor block %d: snapshot epoch %d is older than the previous anchor's %d",
			b.BlockNumber, snap.Epoch, prev.Snapshot.Epoch)
	}
	return nil
}

// ValidateCommitteeAnchor is checkCommitteeAnchor for other packages (the
// ThebeSync apply path): nil when the block satisfies the anchor rule.
func ValidateCommitteeAnchor(b *config.ZKBlock) error {
	if rej := checkCommitteeAnchor(b); rej != nil {
		return rej.err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

type anchorRecord struct {
	Period       uint64                          `json:"period"`
	AnchorHeight uint64                          `json:"anchor_height"`
	AnchorSlot   uint64                          `json:"anchor_slot"`
	Snapshot     seedcommittee.CommitteeSnapshot `json:"snapshot"`
}

var (
	anchorMu      sync.RWMutex
	anchorRecords = map[uint64]*anchorRecord{}

	// Persistence seams (tests substitute these).
	anchorKVGet   = func(period uint64) ([]byte, bool, error) { return DB_OPs.GetCommitteeAnchor(nil, period) }
	anchorKVPut   = func(period uint64, raw []byte) error { return DB_OPs.PutCommitteeAnchorIfAbsent(nil, period, raw) }
	anchorBlockAt = func(height uint64) (*config.ZKBlock, error) { return DB_OPs.GetZKBlockByNumber(nil, height) }
)

func rememberAnchor(rec *anchorRecord) {
	anchorMu.Lock()
	if _, exists := anchorRecords[rec.Period]; !exists {
		anchorRecords[rec.Period] = rec
	}
	anchorMu.Unlock()
}

// RecordCommitteeAnchor stores the pool an admitted anchor block defines. Call
// only after the block passed full validation (its certificate included).
// Non-anchor blocks are a no-op. The snapshot is re-verified here so every
// recording path (live receive, sequencer-local, sync) is equally strict.
func RecordCommitteeAnchor(b *config.ZKBlock) error {
	if b == nil || !IsCommitteeAnchorHeight(b.BlockNumber) {
		return nil
	}
	if err := anchorBindingError(b); err != nil {
		return fmt.Errorf("record committee anchor at %d: %w", b.BlockNumber, err)
	}
	snap, err := parseAndVerifyAnchor(b.CommitteeSnapshotAnchor, b.CommitteeSnapshotHash)
	if err != nil {
		return fmt.Errorf("record committee anchor at %d: %w", b.BlockNumber, err)
	}
	rec := &anchorRecord{
		Period:       anchorDefinesPeriod(b.BlockNumber),
		AnchorHeight: b.BlockNumber,
		AnchorSlot:   b.Slot,
		Snapshot:     *snap,
	}
	rememberAnchor(rec)
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("record committee anchor at %d: encode: %w", b.BlockNumber, err)
	}
	if err := anchorKVPut(rec.Period, raw); err != nil {
		return fmt.Errorf("record committee anchor at %d: persist: %w", b.BlockNumber, err)
	}
	log.Info().Uint64("anchor_height", b.BlockNumber).Uint64("defines_period", rec.Period).
		Uint64("snapshot_epoch", snap.Epoch).Int("members", len(snap.Entries)).
		Msg("committee anchor: recorded the pool for the next selection period")
	return nil
}

// anchoredRecord returns the anchored record for period: memory, then the DB
// record, then rebuilt from this node's own copy of the anchor block. Returns
// (nil, nil) for a period that is not anchored.
func anchoredRecord(period uint64) (*anchorRecord, error) {
	if !periodIsAnchored(period) {
		return nil, nil
	}
	anchorMu.RLock()
	rec := anchorRecords[period]
	anchorMu.RUnlock()
	if rec != nil {
		return rec, nil
	}
	anchorHeight := (period - 1) * epochLengthBlocks()
	// The committed anchor block outranks the local KV index. Read it once; when
	// it is stored, a KV record is used only if it is the snapshot that block
	// certified (CommitteeSnapshotHash, covered by ConsensusHash). A KV record
	// that disagrees (local corruption, a bug, a hand-edited DB) is ignored and
	// the pool is rebuilt from the block. With no stored block (pruned, or the
	// block store unavailable), the KV record - verified against the chain when
	// it was written - is the only local evidence and is used as is.
	blk, blkErr := anchorBlockAt(anchorHeight)
	haveBlock := blkErr == nil && blk != nil
	if raw, found, err := anchorKVGet(period); err == nil && found {
		var r anchorRecord
		if jerr := json.Unmarshal(raw, &r); jerr == nil && r.Period == period {
			if verr := seedcommittee.VerifyCommitteeSnapshot(&r.Snapshot, pinnedSeedAuthority()); verr == nil {
				if !haveBlock || kvRecordMatchesBlock(&r, blk) {
					rememberAnchor(&r)
					return &r, nil
				}
				log.Error().Uint64("period", period).Uint64("anchor_height", anchorHeight).
					Msg("committee anchor: local KV record does not match the committed anchor block - ignoring it and rebuilding the pool from the block")
			}
		}
	}
	if haveBlock {
		if rerr := RecordCommitteeAnchor(blk); rerr == nil {
			anchorMu.RLock()
			rec = anchorRecords[period]
			anchorMu.RUnlock()
			if rec != nil {
				return rec, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: period %d (anchor block %d)", ErrCommitteeAnchorMissing, period, anchorHeight)
}

// kvRecordMatchesBlock reports whether a KV anchor record is the snapshot the
// committed anchor block certified.
func kvRecordMatchesBlock(r *anchorRecord, blk *config.ZKBlock) bool {
	h := AnchorSnapshotHash(&r.Snapshot)
	return r.AnchorHeight == blk.BlockNumber && len(blk.CommitteeSnapshotHash) == len(h) &&
		string(blk.CommitteeSnapshotHash) == string(h[:])
}

// ---------------------------------------------------------------------------
// Resolvers used by every pool consumer
// ---------------------------------------------------------------------------

func poolFromSnapshot(snap *seedcommittee.CommitteeSnapshot, applyBlocklist bool) (map[string]string, error) {
	var blocked map[string]struct{}
	if applyBlocklist {
		blocked = blockedBuddies()
	}
	out := make(map[string]string, len(snap.Entries))
	for _, e := range snap.Entries {
		pid := strings.TrimSpace(e.PeerID)
		if pid == "" {
			continue
		}
		if _, isBlocked := blocked[pid]; isBlocked {
			continue
		}
		out[pid] = normalizeBLSPub(e.BLSPub)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("committee anchor: pool empty after applying block_buddy blocklist")
	}
	return out, nil
}

// anchoredPoolForPeriod returns (pool, true, nil) for an anchored period,
// (nil, false, nil) when the period is not anchored (caller keeps the legacy
// behaviour), or an error that callers MUST treat as fail-closed.
func anchoredPoolForPeriod(period uint64, applyBlocklist bool) (map[string]string, bool, error) {
	if !periodIsAnchored(period) {
		return nil, false, nil
	}
	rec, err := anchoredRecord(period)
	if err != nil {
		return nil, true, err
	}
	pool, err := poolFromSnapshot(&rec.Snapshot, applyBlocklist)
	return pool, true, err
}

// AnchoredPoolForHeight is anchoredPoolForPeriod for the period containing height.
func AnchoredPoolForHeight(height uint64, applyBlocklist bool) (map[string]string, bool, error) {
	return anchoredPoolForPeriod(EpochForHeight(height), applyBlocklist)
}

var startupTip atomic.Uint64

// committedTipFn returns this node's committed tip (a seam for tests).
var committedTipFn = committedTipFromDB

// currentPoolPeriod is the selection period of the next height to be decided
// (committed tip + 1): what "the current pool" means once it comes from the
// chain rather than the seed's wall clock.
func currentPoolPeriod() uint64 { return EpochForHeight(committedTipFn() + 1) }

func committedTipFromDB() uint64 {
	tip, known := DB_OPs.CachedCommittedTip()
	if !known {
		if v := startupTip.Load(); v != 0 {
			tip = v
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if v, err := DB_OPs.GetLatestBlockNumber(ctx, nil); err == nil {
				startupTip.Store(v)
				tip = v
			}
			cancel()
		}
	}
	return tip
}

// anchoredPoolForCurrent resolves the "current" pool from the chain.
func anchoredPoolForCurrent(applyBlocklist bool) (map[string]string, bool, error) {
	if !CommitteeAnchoringEnabled() {
		return nil, false, nil
	}
	return anchoredPoolForPeriod(currentPoolPeriod(), applyBlocklist)
}

// AnchoredRewardAddressesForHeight is the reward-address map of height's
// anchored pool (peers with no bound address omitted), same shape as
// CommitteeSnapshot.RewardAddrByPeer.
func AnchoredRewardAddressesForHeight(height uint64) (map[string]string, bool, error) {
	period := EpochForHeight(height)
	if !periodIsAnchored(period) {
		return nil, false, nil
	}
	rec, err := anchoredRecord(period)
	if err != nil {
		return nil, true, err
	}
	return rec.Snapshot.RewardAddrByPeer(), true, nil
}

// entropyAnchoredPool is the entropy (epoch) committee's pool for epoch: the
// snapshot of the newest anchor block whose slot is at or before the epoch's
// freeze cutoff (epoch*N - SnapshotFreezeLookahead). Deterministic from the
// chain: every node that has the chain up to that slot agrees. (nil, false,
// nil) when no anchor precedes the cutoff yet (transition: legacy pool).
func entropyAnchoredPool(epoch uint64) (map[string]string, bool, error) {
	if !CommitteeAnchoringEnabled() {
		return nil, false, nil
	}
	var cutoff uint64
	if epoch*N > SnapshotFreezeLookahead {
		cutoff = epoch*N - SnapshotFreezeLookahead
	}
	firstPeriod := firstCommitteeAnchorHeight()/epochLengthBlocks() + 1
	// Finality: the answer is "the newest anchor at or before the cutoff", so
	// it is only final once no block can still be committed at a slot <= the
	// cutoff. Before that a newer qualifying anchor may still arrive, and the
	// entropy accumulator caches the expected set it is built with. Callers on
	// the block path (fold, decide) and the reveal pusher (live slot inside
	// the reveal window) are always past this point; only an early, off-path
	// query can hit it, and it fails closed instead of guessing.
	tip := committedTipFn()
	if next := nextPossibleSlotFn(tip); next <= cutoff {
		return nil, true, fmt.Errorf("%w: entropy epoch %d cutoff slot %d, next committable slot %d",
			ErrEntropyPoolNotFinal, epoch, cutoff, next)
	}
	period := currentPoolPeriod() + 1
	for i := 0; i < entropyAnchorSearchLimit && period >= firstPeriod; i++ {
		rec, err := anchoredRecord(period)
		if err == nil && rec != nil && rec.AnchorSlot <= cutoff {
			// Fleet pool, NO local blocklist: this pool defines who is EXPECTED
			// to reveal. A node that removed a blocklisted member would expect
			// one reveal fewer, finalise "mixed" where its peers finalise
			// "fallback" (or the reverse), and seal a different ENTROPY.
			pool, perr := poolFromSnapshot(&rec.Snapshot, false)
			return pool, true, perr
		}
		// A period whose anchor block this node HAS committed must resolve.
		// Skipping it would silently pick an older anchor on this node only.
		if err != nil && (period-1)*epochLengthBlocks() <= tip {
			return nil, true, fmt.Errorf("entropy epoch %d: anchor for period %d (height %d) is committed but unreadable: %w",
				epoch, period, (period-1)*epochLengthBlocks(), err)
		}
		if period == firstPeriod {
			break
		}
		period--
	}
	if period <= firstPeriod {
		return nil, false, nil // no anchor before the cutoff yet: pre-activation pool
	}
	return nil, true, fmt.Errorf("%w: no anchor found within %d periods before entropy epoch %d's cutoff slot %d",
		ErrCommitteeAnchorMissing, entropyAnchorSearchLimit, epoch, cutoff)
}

// ---------------------------------------------------------------------------
// Proposer side
// ---------------------------------------------------------------------------

var (
	anchorSourceMu sync.RWMutex
	anchorSource   func(ctx context.Context) (*seedcommittee.CommitteeSnapshot, error)
)

// SetCommitteeAnchorSource wires where the proposer gets the seed's CURRENT
// signed snapshot (Sequencer.WireCommitteeSources). Only the proposer needs it.
func SetCommitteeAnchorSource(fn func(ctx context.Context) (*seedcommittee.CommitteeSnapshot, error)) {
	anchorSourceMu.Lock()
	anchorSource = fn
	anchorSourceMu.Unlock()
}

// PreflightCommitteeAnchorSource fetches the seed's current snapshot once and
// verifies it against the pinned authority, so a wrong
// consensus.seed_authority_bls_pub (or an unreachable seed) is reported at
// start-up instead of at the first anchor height - where it would stop the
// chain (the proposer cannot build the anchor) or knock this node off it (it
// rejects a valid anchor). Returns the snapshot's epoch and member count.
// No-op (0, 0, nil) when anchoring is off.
func PreflightCommitteeAnchorSource() (epoch uint64, members int, err error) {
	if !CommitteeAnchoringEnabled() {
		return 0, 0, nil
	}
	anchorSourceMu.RLock()
	src := anchorSource
	anchorSourceMu.RUnlock()
	if src == nil {
		return 0, 0, errors.New("no committee anchor source wired (seed client unavailable): this node cannot build or cross-check anchor snapshots")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := src(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("fetching the seed's committee snapshot: %w", err)
	}
	if err := seedcommittee.VerifyCommitteeSnapshot(snap, pinnedSeedAuthority()); err != nil {
		return 0, 0, fmt.Errorf("the seed's committee snapshot does not verify against consensus.seed_authority_bls_pub: %w", err)
	}
	return snap.Epoch, len(snap.Entries), nil
}

// BuildCommitteeAnchor returns the snapshot body and hash block height must
// carry ("" and nil for a non-anchor height). It fetches the seed's current
// snapshot; if that fails, fails verification, or would move the membership
// epoch backwards, it re-carries the previous anchor's snapshot, so the pool
// simply stays the same and the chain stays live. Only the very first anchor
// has no fallback.
func BuildCommitteeAnchor(height uint64) (body string, hash []byte, err error) {
	if !IsCommitteeAnchorHeight(height) {
		return "", nil, nil
	}
	var prev *seedcommittee.CommitteeSnapshot
	rec, perr := anchoredRecord(height / epochLengthBlocks())
	if perr != nil {
		return "", nil, fmt.Errorf("committee anchor: previous anchor for height %d unreadable (fail closed): %w", height, perr)
	}
	if rec != nil {
		s := rec.Snapshot
		prev = &s
	}

	var chosen *seedcommittee.CommitteeSnapshot
	anchorSourceMu.RLock()
	src := anchorSource
	anchorSourceMu.RUnlock()
	if src != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		snap, ferr := src(ctx)
		cancel()
		switch {
		case ferr != nil:
			log.Warn().Err(ferr).Uint64("height", height).Msg("committee anchor: seed snapshot unavailable, re-carrying the previous pool")
		case seedcommittee.VerifyCommitteeSnapshot(snap, pinnedSeedAuthority()) != nil:
			log.Warn().Uint64("height", height).Msg("committee anchor: seed snapshot failed verification, re-carrying the previous pool")
		case prev != nil && snap.Epoch < prev.Epoch:
			log.Warn().Uint64("height", height).Uint64("seed_epoch", snap.Epoch).Uint64("prev_epoch", prev.Epoch).
				Msg("committee anchor: seed snapshot is older than the previous anchor, re-carrying the previous pool")
		default:
			chosen = snap
		}
	}
	if chosen == nil {
		chosen = prev
	}
	if chosen == nil {
		return "", nil, fmt.Errorf("committee anchor: no seed snapshot and no previous anchor for anchor height %d (fail closed)", height)
	}
	raw, err := json.Marshal(chosen)
	if err != nil {
		return "", nil, fmt.Errorf("committee anchor: encode snapshot: %w", err)
	}
	h := AnchorSnapshotHash(chosen)
	return string(raw), h[:], nil
}

// ValidateCommitteeAnchorConfig is the startup check (main.go): anchoring needs
// a pinned seed authority, a non-zero selection-period length, and the older
// hash-only anchor flag off (both would write CommitteeSnapshotHash).
func ValidateCommitteeAnchorConfig() error {
	if !settings.IsLoaded() || settings.Get().Consensus.CommitteeAnchorActivationHeight == 0 {
		return nil
	}
	if epochLengthBlocks() == 0 {
		return errors.New("consensus.committee_anchor_activation_height requires consensus.committee_epoch_blocks > 0")
	}
	if pinnedSeedAuthority() == "" {
		return errors.New("consensus.committee_anchor_activation_height requires consensus.seed_authority_bls_pub to be pinned")
	}
	if CommitteeSnapshotAnchorEnabled {
		return errors.New("consensus.committee_anchor_activation_height and JMDN_COMMITTEE_SNAPSHOT_ANCHOR cannot both be on (both write CommitteeSnapshotHash)")
	}
	return nil
}
