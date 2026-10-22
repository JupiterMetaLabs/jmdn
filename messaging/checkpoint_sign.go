package messaging

// Committee-signed chain-head checkpoints (Phase 1, Option A) — the jmdn side of
// the ThebeDB canonical-log external anchor described in
// docs/CHAIN-HEAD-ANCHOR-DESIGN.md.
//
// This file provides the following, active when config checkpoint.enabled is
// true (the default) and wiring installs the KV handle via
// EnableCheckpointSigning:
//
//  1. CheckpointSigVerifier — a checkpoint.SigVerifier that BLS-verifies a
//     checkpoint's committee signature against the AUTHENTICATED committee keys
//     (sourced from the seed-authority-signed snapshot via AuthorizedCommittee).
//     This is the seam ThebeDB's boot/periodic gate calls. Fail-closed.
//
//  2. SignCheckpoint — builds a checkpoint.Checkpoint for the current canonical
//     head and signs CanonicalCheckpointBytes with THIS node's committee BLS key
//     (the same key used for block votes).
//
//  3. maybeSignChainHeadCheckpoint — the per-committed-block cadence hook,
//     invoked from broadcast.go's commit path. Only the sequencer signs, and
//     only a checkpoint it can verify itself is stored.
//
//  4. CheckpointBootGate — the boot check, with a floor kept in a file outside
//     the store (internal/ckptfloor) so a rollback is caught.
//
// ── PHASE 1 v0 SCOPE: SINGLE-SIGNER ATTESTATION ──────────────────────────────
// v0 stores a checkpoint signed by a SINGLE committee member (the sequencer):
// SignerSet = [thisPeerID], Signature = this node's BLS signature. The verifier
// therefore ACCEPTS any one valid committee-member signature. This is a genuine
// cryptographic anchor (a filesystem/DB attacker without a committee key cannot
// forge it), but it is NOT yet the fleet-agreed 2f+1 guarantee that block
// certificates give.
//
// TODO(Phase 1b): require a committee AGGREGATE — collect 2f+1 buddy signatures
// over CanonicalCheckpointBytes (reuse the vote-aggregation path) OR bind the KV
// head into the block certificate — and make the verifier require a quorum
// rather than a single member. Do NOT fake an aggregate here.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/internal/ckptfloor"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/checkpoint"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/rs/zerolog/log"
)

// chainHeadKey is the ThebeDB canonical-log head pointer. Value layout is
// [8B seq BigEndian][32B SHA256 head] = 40 bytes (internal/merkle/chain.go /
// pkg/kv/badger_store.go). It is READABLE via kv.Store.Get even though the
// __sys: namespace is not scannable/writable by consumers.
const chainHeadKey = "__sys:chain_head"

// checkpointSigner holds the process-wide wiring for the chain-head checkpoint
// feature. It is nil-KV / disabled by default: with checkpointEnabled=false or
// checkpointKV=nil, both the boot gate and the signing hook are complete no-ops,
// so a node that does not enable the feature is byte-identical at runtime.
var (
	checkpointMu          sync.RWMutex
	checkpointKV          kv.Store // nil => signing disabled
	checkpointEnabled     bool
	checkpointIsSequencer bool
	checkpointCadence     uint64 // 0 => per-epoch
	checkpointSelfPeerID  string
	checkpointFloorPath   string // "" => no floor file kept
)

// ErrCheckpointCommitteeUnavailable marks a verification that could not run
// because the committee source was unavailable or empty — as opposed to a
// signature that ran and failed. The boot gate retries on it before applying
// its fail-closed policy.
var ErrCheckpointCommitteeUnavailable = errors.New("checkpoint verify: committee source unavailable")

// EnableCheckpointSigning wires committee-signed chain-head checkpoint creation.
// It is a NO-OP for signing unless enabled is true and store is non-nil; when
// disabled nothing is ever signed or stored and the block-commit path is
// unchanged.
//
//   - store:       the ThebeDB canonical KV store (main.go db.KV). The signer
//     reads __sys:chain_head and writes the checkpoint via checkpoint.Store.
//   - enabled:     config checkpoint.enabled.
//   - isSequencer: true ONLY on the authoritative block producer
//     (enable_catchup==false — the same discriminator the sync monitor and the
//     committee-source wiring use). Non-sequencers never sign.
//   - cadenceBlocks: config checkpoint.cadence_blocks (0 = per-epoch).
//   - selfPeerID:  this node's libp2p peer id, recorded in SignerSet (metadata
//     only; not part of the signed bytes).
//   - floorPath:   config checkpoint.floor_file; raised after every stored
//     checkpoint ("" keeps no floor).
//
// Safe to call concurrently.
func EnableCheckpointSigning(store kv.Store, enabled, isSequencer bool, cadenceBlocks uint64, selfPeerID, floorPath string) {
	checkpointMu.Lock()
	defer checkpointMu.Unlock()
	checkpointKV = store
	checkpointEnabled = enabled
	checkpointIsSequencer = isSequencer
	checkpointCadence = cadenceBlocks
	checkpointSelfPeerID = strings.TrimSpace(selfPeerID)
	checkpointFloorPath = strings.TrimSpace(floorPath)
}

// CheckpointSigVerifier returns a checkpoint.SigVerifier that verifies a
// checkpoint's committee signature. It resolves the committee for the checkpoint
// via the AUTHENTICATED eligibility source (AuthorizedCommittee — peer_id ->
// snapshot-bound bls_pub, which comes from the seed-authority-signed snapshot,
// external to ThebeDB; that external key set is what makes the anchor sound) and
// BLS-verifies the signature over the ThebeDB-provided canonical bytes.
//
// FAIL-CLOSED on any resolution/verify error: an unavailable/empty committee
// source, an empty signature, or a signature that matches no eligible member all
// return a non-nil error.
//
// v0 single-signer: accepts the signature if it verifies against ANY ONE
// eligible committee member's bls_pub. See the file header TODO(Phase 1b) for the
// quorum upgrade.
//
// NOTE on epoch pinning: the committee is resolved from the CURRENT authenticated
// snapshot (AuthorizedCommittee), not pinned to c.Epoch. Historical-epoch pinned
// resolution needs consensus.require_pinned_committee plus a source that serves a
// past epoch; until then a committee membership change between signing and
// verification could reject a genuine older checkpoint (fail-closed, never
// fail-open). Documented deliberately rather than silently resolving "current".
func CheckpointSigVerifier() checkpoint.SigVerifier {
	return func(canonicalBytes, sig []byte, epoch uint64) error {
		if len(sig) == 0 {
			return fmt.Errorf("checkpoint verify: empty signature (fail closed, epoch %d)", epoch)
		}
		committee, err := AuthorizedCommittee()
		if err != nil {
			return fmt.Errorf("%w (fail closed, epoch %d): %v", ErrCheckpointCommitteeUnavailable, epoch, err)
		}
		if len(committee) == 0 {
			return fmt.Errorf("%w: empty committee (fail closed, epoch %d)", ErrCheckpointCommitteeUnavailable, epoch)
		}
		for _, pubHex := range committee {
			pubHex = normalizeBLSPub(pubHex)
			if pubHex == "" {
				// Legacy/unpinned source with no bound bls_pub: cannot verify a
				// signature against it. Skip; another member may carry a key.
				continue
			}
			pubBytes, decErr := hex.DecodeString(pubHex)
			if decErr != nil {
				continue
			}
			if blssign.BLSVerify(pubBytes, canonicalBytes, sig) == nil {
				return nil // v0: one valid committee-member signature is accepted.
			}
		}
		return fmt.Errorf("checkpoint verify: signature not from any eligible committee member (fail closed, epoch %d)", epoch)
	}
}

// SignCheckpoint builds a checkpoint.Checkpoint for (chainID, seq, head,
// stateRoot, epoch) and signs CanonicalCheckpointBytes with THIS node's committee
// BLS key (BLS_Signer.LocalBLSKeypair — the exact key material block votes use,
// never a freshly minted one). v0 single-signer: SignerSet = [thisPeerID].
func SignCheckpoint(chainID, seq uint64, head, stateRoot []byte, epoch uint64, selfPeerID string) (*checkpoint.Checkpoint, error) {
	if len(head) != 32 {
		return nil, fmt.Errorf("sign checkpoint: head must be 32 bytes, got %d", len(head))
	}
	c := &checkpoint.Checkpoint{
		ChainID:   chainID,
		Seq:       seq,
		Head:      head,
		StateRoot: stateRoot,
		Epoch:     epoch,
	}
	priv, _, err := BLS_Signer.LocalBLSKeypair()
	if err != nil {
		return nil, fmt.Errorf("sign checkpoint: load committee BLS key: %w", err)
	}
	sig, err := blssign.BLSSign(priv, checkpoint.CanonicalCheckpointBytes(c))
	if err != nil {
		return nil, fmt.Errorf("sign checkpoint: bls sign: %w", err)
	}
	c.Signature = sig
	if id := strings.TrimSpace(selfPeerID); id != "" {
		c.SignerSet = []string{id}
	}
	return c, nil
}

// maybeSignChainHeadCheckpoint is the per-committed-block cadence hook, called
// from broadcast.go after a block is stored and its epoch is finalised. It is a
// best-effort side observer: it NEVER affects block production, consensus, or the
// append path — any failure is logged and swallowed.
//
// It is a complete no-op unless the feature was enabled via
// EnableCheckpointSigning AND this node is the sequencer AND the cadence fires.
func maybeSignChainHeadCheckpoint(block *config.ZKBlock) {
	if block == nil {
		return
	}
	checkpointMu.RLock()
	enabled := checkpointEnabled
	store := checkpointKV
	isSeq := checkpointIsSequencer
	cadence := checkpointCadence
	selfPeerID := checkpointSelfPeerID
	floorPath := checkpointFloorPath
	checkpointMu.RUnlock()

	if !enabled || store == nil || !isSeq {
		return
	}
	if !checkpointCadenceFires(block.BlockNumber, cadence) {
		return
	}

	if err := signAndStoreCheckpoint(store, block, selfPeerID, floorPath); err != nil {
		log.Warn().Err(err).
			Uint64("block_number", block.BlockNumber).
			Msg("[checkpoint] sign/store failed (non-fatal; block commit unaffected)")
		return
	}
	log.Info().
		Uint64("block_number", block.BlockNumber).
		Msg("[checkpoint] committee-signed chain-head checkpoint stored")
}

// signAndStoreCheckpoint reads the current canonical head from the KV store,
// builds+signs a checkpoint, and persists it. Kept separate so the cadence hook
// stays trivial.
func signAndStoreCheckpoint(store kv.Store, block *config.ZKBlock, selfPeerID, floorPath string) error {
	raw, err := store.Get([]byte(chainHeadKey))
	if err != nil {
		return fmt.Errorf("read %s: %w", chainHeadKey, err)
	}
	if len(raw) < 40 {
		return fmt.Errorf("invalid chain head length %d (want >=40)", len(raw))
	}
	seq := binary.BigEndian.Uint64(raw[:8])
	head := make([]byte, 32)
	copy(head, raw[8:40])

	c, err := SignCheckpoint(
		BLS_Signer.DomainChainID(), // authenticated network chain id (same source vote domain uses)
		seq,
		head,
		checkpointStateRoot(block),
		EpochForHeight(block.BlockNumber),
		selfPeerID,
	)
	if err != nil {
		return err
	}
	return storeVerifiedCheckpoint(store, c, CheckpointSigVerifier(), floorPath)
}

// storeVerifiedCheckpoint stores c only if it verifies with the same verifier
// the boot gate uses, then raises the floor. A checkpoint this node cannot
// verify (for example its key is not in the authorized committee) must never be
// stored: the next boot would find it, fail verification and refuse to start.
func storeVerifiedCheckpoint(store kv.Store, c *checkpoint.Checkpoint, verify checkpoint.SigVerifier, floorPath string) error {
	if err := verify(checkpoint.CanonicalCheckpointBytes(c), c.Signature, c.Epoch); err != nil {
		return fmt.Errorf("not storing checkpoint (seq %d): it does not verify against the authorized committee: %w", c.Seq, err)
	}
	if err := checkpoint.Store(store, c); err != nil {
		return fmt.Errorf("store checkpoint (seq %d): %w", c.Seq, err)
	}
	if err := ckptfloor.Raise(floorPath, c.ChainID, c.Seq, c.Head); err != nil {
		return fmt.Errorf("checkpoint stored (seq %d) but floor not raised: %w", c.Seq, err)
	}
	return nil
}

// CheckpointBootGate is the start-up integrity check. It verifies the canonical
// log against the latest stored checkpoint, with a floor taken from the floor
// file (outside the store) and minSeq (config). Returns:
//   - (true, nil):  a checkpoint exists, is at or above the floor, and verifies.
//     The floor file is raised to it.
//   - (false, nil): no checkpoint and no floor (a node that has never had one —
//     every non-sequencer, and a sequencer before its first signing).
//   - (false, err): anything else. The caller applies boot_fail_closed.
//
// Verification that cannot run because the committee source is unavailable is
// retried `retries` times, `wait` apart, before it is returned.
func CheckpointBootGate(ctx context.Context, store kv.Store, verify checkpoint.SigVerifier, floorPath string, minSeq uint64, retries int, wait time.Duration) (bool, error) {
	rec, hasFloor, err := ckptfloor.Load(floorPath)
	if err != nil {
		return false, err
	}
	floor := checkpoint.Floor{Required: hasFloor || minSeq > 0, MinSeq: minSeq}
	if hasFloor {
		if rec.Seq > floor.MinSeq {
			floor.MinSeq = rec.Seq
		}
		// The checkpoint the floor was taken from must still be there, unchanged.
		// Otherwise the stored checkpoint history was replaced.
		at, gerr := checkpoint.Get(store, rec.Seq)
		if gerr != nil {
			return false, fmt.Errorf("checkpoint at floor seq %d is missing (store rolled back or checkpoints deleted; floor file %s): %w", rec.Seq, floorPath, gerr)
		}
		if want := rec.HeadBytes(); want != nil && !bytes.Equal(at.Head, want) {
			return false, fmt.Errorf("checkpoint at floor seq %d has a different head than the floor file %s records (checkpoint history replaced)", rec.Seq, floorPath)
		}
		if rec.ChainID != 0 && at.ChainID != rec.ChainID {
			return false, fmt.Errorf("checkpoint at floor seq %d is for chain %d, floor file %s says %d", rec.Seq, at.ChainID, floorPath, rec.ChainID)
		}
	}

	var verified bool
	for attempt := 0; ; attempt++ {
		verified, err = checkpoint.BootVerifyWithFloor(ctx, store, verify, floor)
		if err == nil || !errors.Is(err, ErrCheckpointCommitteeUnavailable) || attempt >= retries {
			break
		}
		log.Warn().Err(err).Int("attempt", attempt+1).Int("of", retries).
			Msg("[checkpoint] committee source unavailable at boot — retrying before applying fail-closed policy")
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(wait):
		}
	}
	if err != nil {
		if errors.Is(err, checkpoint.ErrNoCheckpoint) || errors.Is(err, checkpoint.ErrCheckpointBelowFloor) {
			return false, fmt.Errorf("%w (floor file %s; delete it only after a deliberate restore from an older snapshot)", err, floorPath)
		}
		return false, err
	}
	if verified {
		if latest, ok, lerr := checkpoint.Latest(store); lerr == nil && ok {
			if rerr := ckptfloor.Raise(floorPath, latest.ChainID, latest.Seq, latest.Head); rerr != nil {
				log.Warn().Err(rerr).Str("floor_file", floorPath).Msg("[checkpoint] verified, but the floor file could not be raised")
			}
		}
	}
	return verified, nil
}

// checkpointStateRoot picks the state digest the checkpoint covers. It prefers
// the block's StateFingerprint (the P2.5 account-state digest — the design's
// recommendation, so account-balance tampering is detectable on local reads),
// falling back to the consensus StateRoot when no fingerprint is present. The
// value is self-consistent: whatever is signed is stored, and the verifier
// reconstructs canonical bytes from the stored checkpoint, so this choice never
// has to match a recomputation elsewhere.
func checkpointStateRoot(block *config.ZKBlock) []byte {
	if fp := strings.TrimSpace(block.StateFingerprint); fp != "" {
		if b, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(fp), "0x")); err == nil && len(b) > 0 {
			return b
		}
	}
	return block.StateRoot.Bytes()
}

// checkpointCadenceFires reports whether a checkpoint should be signed at height.
//   - cadence > 0 : every N committed blocks (height % N == 0).
//   - cadence == 0: per selection epoch — the first height of a new committee
//     epoch (EpochForHeight boundary). With committee_epoch_blocks=0 this fires
//     only at genesis; see CheckpointSettings.CadenceBlocks.
func checkpointCadenceFires(height, cadence uint64) bool {
	if cadence > 0 {
		return height > 0 && height%cadence == 0
	}
	if height == 0 {
		return true
	}
	return EpochForHeight(height) != EpochForHeight(height-1)
}
