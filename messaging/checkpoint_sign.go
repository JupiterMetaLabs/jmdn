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
//     invoked from every commit path (broadcast.go, blockPropagation.go and
//     ThebeSync via NoteCommittedForCheckpoint). Every node signs checkpoints
//     of its OWN log with its own BLS key (logs differ per node), and only a
//     checkpoint it can verify itself is stored.
//
//  4. CheckpointBootGate — the boot check, with a floor kept in a file outside
//     the store (internal/ckptfloor) so a rollback is caught. It walks only the
//     log after the floor checkpoint (checkpoint.BootVerifyFromAnchor), so
//     boot cost tracks new records, and a pruned log (storage.profile light)
//     still verifies. StartBackgroundFullVerify re-walks the whole log after
//     boot on nodes that keep it.
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
	"github.com/JupiterMetaLabs/ion"
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
	checkpointMu         sync.RWMutex
	checkpointKV         kv.Store // nil => signing disabled
	checkpointEnabled    bool
	checkpointCadence    uint64 // 0 => per-epoch
	checkpointSelfPeerID string
	checkpointFloorPath  string // "" => no floor file kept
	checkpointSignerPub  string // pinned BLS pub (hex); "" => do not sign
)

// ErrCheckpointCommitteeUnavailable marks a verification that could not run
// because the committee source was unavailable or empty — as opposed to a
// signature that ran and failed. The boot gate retries on it before applying
// its fail-closed policy.
var ErrCheckpointCommitteeUnavailable = errors.New("checkpoint verify: committee source unavailable")

// ErrCheckpointSignerNotInCommittee: the signature did not verify against any
// key in the CURRENT authorized committee. Either the signature is forged or
// the signer has since left the (capped, sorted) eligible set — the committee
// path cannot tell which. Pin checkpoint.signer_bls_pub to avoid the second.
var ErrCheckpointSignerNotInCommittee = errors.New("checkpoint verify: signature not from any current committee member")

// ErrCheckpointBadSignature: the signature does not verify against the pinned
// signer key.
var ErrCheckpointBadSignature = errors.New("checkpoint verify: signature does not match the pinned signer key")

// EnableCheckpointSigning wires committee-signed chain-head checkpoint creation.
// It is a NO-OP for signing unless enabled is true and store is non-nil; when
// disabled nothing is ever signed or stored and the block-commit path is
// unchanged.
//
//   - store:       the ThebeDB canonical KV store (main.go db.KV). The signer
//     reads __sys:chain_head and writes the checkpoint via checkpoint.Store.
//   - enabled:     config checkpoint.enabled.
//   - cadenceBlocks: config checkpoint.cadence_blocks (0 = per-epoch).
//   - selfPeerID:  this node's libp2p peer id, recorded in SignerSet (metadata
//     only; not part of the signed bytes).
//   - floorPath:   config checkpoint.floor_file; raised after every stored
//     checkpoint ("" keeps no floor).
//   - signerPub:   the key checkpoints are verified against (see
//     ResolveCheckpointSigner). The sequencer signs only when it is set; a
//     checkpoint that a later boot could only check against the rotating
//     committee could stop the sequencer from restarting once its key leaves
//     the eligible set.
//
// Safe to call concurrently.
func EnableCheckpointSigning(store kv.Store, enabled bool, cadenceBlocks uint64, selfPeerID, floorPath, signerPub string) {
	checkpointMu.Lock()
	defer checkpointMu.Unlock()
	checkpointKV = store
	checkpointEnabled = enabled
	checkpointCadence = cadenceBlocks
	checkpointSelfPeerID = strings.TrimSpace(selfPeerID)
	checkpointFloorPath = strings.TrimSpace(floorPath)
	checkpointSignerPub = normalizeBLSPub(signerPub)
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
	return CheckpointSigVerifierFor("")
}

// ResolveCheckpointSigner picks the key checkpoints are signed and verified
// with, so an operator does not have to copy it into the config by hand:
//   - configPub (checkpoint.signer_bls_pub) when set: an explicit pin, kept
//     outside the data volume. Also protects against the node's key file being
//     swapped on disk.
//   - otherwise this node's own BLS public key (the key the sequencer signs
//     with), read from its key file.
//   - "" when neither is available: nothing is signed and stored checkpoints
//     are checked against the current committee.
//
// source names which one was used, for the boot log.
func ResolveCheckpointSigner(configPub string) (signerPubHex, source string) {
	if p := normalizeBLSPub(configPub); p != "" {
		return p, "config"
	}
	// Read the key file directly. BLS_Signer.LocalBLSKeypair caches its first
	// result for the process; calling it before the key file exists (a fresh
	// node, before seed registration creates it) would cache the error and
	// break vote signing for the rest of the run.
	if _, pub, err := blssign.LoadBLSKeyPair(); err == nil && len(pub) > 0 {
		return hex.EncodeToString(pub), "node_key"
	}
	return "", "none"
}

// CheckpointSigVerifierFor returns the verifier the boot gate uses.
//
// With signerPubHex set (config checkpoint.signer_bls_pub, read from the config
// file, outside the store) the signature must verify against that key and
// nothing else; committee membership is irrelevant, so a committee change can
// never fail a genuine checkpoint. A mismatch returns ErrCheckpointBadSignature.
//
// With it empty, the signature is checked against the CURRENT authorized
// committee (see CheckpointSigVerifier for the epoch-pinning caveat): no match
// returns ErrCheckpointSignerNotInCommittee, an unavailable source returns
// ErrCheckpointCommitteeUnavailable. Both fail closed.
func CheckpointSigVerifierFor(signerPubHex string) checkpoint.SigVerifier {
	pinned := normalizeBLSPub(signerPubHex)
	return func(canonicalBytes, sig []byte, epoch uint64) error {
		if len(sig) == 0 {
			return fmt.Errorf("checkpoint verify: empty signature (fail closed, epoch %d)", epoch)
		}
		if pinned != "" {
			pub, err := hex.DecodeString(pinned)
			if err != nil {
				return fmt.Errorf("checkpoint verify: checkpoint.signer_bls_pub is not hex: %w", err)
			}
			if blssign.BLSVerify(pub, canonicalBytes, sig) != nil {
				return fmt.Errorf("%w (epoch %d)", ErrCheckpointBadSignature, epoch)
			}
			return nil
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
		return fmt.Errorf("%w (fail closed, epoch %d)", ErrCheckpointSignerNotInCommittee, epoch)
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
	cadence := checkpointCadence
	selfPeerID := checkpointSelfPeerID
	floorPath := checkpointFloorPath
	signerPub := checkpointSignerPub
	checkpointMu.RUnlock()

	if !enabled || store == nil {
		return
	}
	if !checkpointCadenceFires(block.BlockNumber, cadence) {
		return
	}

	if signerPub == "" {
		// No key at boot (fresh node whose key file appeared later): resolve now.
		if signerPub, _ = ResolveCheckpointSigner(""); signerPub == "" {
			warnSignerNotPinnedOnce()
			return
		}
	}
	if err := signAndStoreCheckpoint(store, block, selfPeerID, floorPath, signerPub); err != nil {
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
func signAndStoreCheckpoint(store kv.Store, block *config.ZKBlock, selfPeerID, floorPath, signerPub string) error {
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
	c.BlockNumber = block.BlockNumber // metadata for the light-node pruner; not signed
	return storeVerifiedCheckpoint(store, c, CheckpointSigVerifierFor(signerPub), floorPath)
}

// NoteCommittedForCheckpoint is the checkpoint hook for the receive and sync
// commit paths (blockPropagation.go, ThebeSync); broadcast.go calls the same
// hook on the producer side. Best-effort: never affects the commit.
func NoteCommittedForCheckpoint(block *config.ZKBlock) {
	maybeSignChainHeadCheckpoint(block)
}

var signerNotPinnedOnce sync.Once

// warnSignerNotPinnedOnce tells the operator, once per process, why the
// sequencer is not signing: no signer key could be resolved.
func warnSignerNotPinnedOnce() {
	signerNotPinnedOnce.Do(func() {
		log.Warn().Msg("[checkpoint] not signing: this node has no BLS key and checkpoint.signer_bls_pub is unset")
	})
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
//     a node that has never signed one, and a node whose store was restored
//     from another node with no floor of its own).
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
	if !hasFloor && floorPath != "" {
		// Expected on the first boot after the floor was introduced. Anything
		// else means the file was deleted or the node started from another
		// directory — the rollback check is not active for this boot.
		if latest, ok, lerr := checkpoint.Latest(store); lerr == nil && ok {
			log.Warn().Str("floor_file", floorPath).Uint64("latest_checkpoint_seq", latest.Seq).
				Msg("[checkpoint] checkpoints exist but the floor file is missing — rollback check inactive for this boot; it is recreated below if verification passes")
		}
	}
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

	// Walk only the log after a trusted checkpoint when there is one: the
	// floor checkpoint (checked above), or, on a pruned log with no floor
	// file, the oldest checkpoint the kept log still covers. Otherwise walk
	// from seq 0.
	logStart, err := kv.LogStart(store)
	if err != nil {
		return false, fmt.Errorf("read log start: %w", err)
	}
	bootVerify := func() (bool, error) { return checkpoint.BootVerifyWithFloor(ctx, store, verify, floor) }
	switch {
	case hasFloor:
		anchorSeq := rec.Seq
		bootVerify = func() (bool, error) {
			return checkpoint.BootVerifyFromAnchor(ctx, store, verify, floor, anchorSeq)
		}
	case logStart > 0:
		anchor, aerr := oldestCheckpointCovering(store, logStart)
		if aerr != nil {
			return false, aerr
		}
		broadcastLogger().Warn(ctx, "[checkpoint] pruned log and no floor file — verifying from the oldest checkpoint the kept log covers; rollback check inactive for this boot",
			ion.Uint64("log_start", logStart), ion.Uint64("anchor_seq", anchor.Seq), ion.String("floor_file", floorPath))
		bootVerify = func() (bool, error) {
			return checkpoint.BootVerifyFromAnchor(ctx, store, verify, floor, anchor.Seq)
		}
	}

	var verified bool
	for attempt := 0; ; attempt++ {
		verified, err = bootVerify()
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
	if err != nil && !floor.Required && notSignedByThisNode(err) {
		// The latest checkpoint was signed by another key and this node has no
		// floor: the store came from another node (a snapshot restore). Its
		// checkpoints say nothing about this node, so there is nothing to check
		// until this node signs its own. Same outcome as having no checkpoint.
		log.Warn().Err(err).Str("floor_file", floorPath).
			Msg("[checkpoint] latest checkpoint was signed by another node's key and there is no floor — store restored from another node; boot gate skipped")
		return false, nil
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
			// The floor file shares a volume with the store, so a whole-volume
			// restore rolls both back. checkpoint.min_seq in the read-only
			// config is the floor such a restore cannot undo.
			log.Info().Uint64("checkpoint_seq", latest.Seq).Uint64("configured_min_seq", minSeq).
				Msg("[checkpoint] to make this floor survive a volume restore, set checkpoint.min_seq to checkpoint_seq in this node's jmdn.yaml")
		}
	}
	return verified, nil
}

// oldestCheckpointCovering returns the oldest stored checkpoint whose suffix
// walk the kept log can serve: Seq+1 >= logStart.
func oldestCheckpointCovering(store kv.Store, logStart uint64) (*checkpoint.Checkpoint, error) {
	var found *checkpoint.Checkpoint
	stop := errors.New("found")
	err := checkpoint.Each(store, func(c *checkpoint.Checkpoint) error {
		if c.Seq+1 >= logStart {
			found = c
			return stop
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	if found == nil {
		return nil, fmt.Errorf("canonical log is pruned below seq %d and no stored checkpoint covers it: %w", logStart, checkpoint.ErrNoCheckpoint)
	}
	return found, nil
}

// notSignedByThisNode reports whether a boot verification error is only that the
// checkpoint's signature does not match this node's signer key. A
// recomputed-head mismatch is never this case, and neither is a committee
// verification failure (a node with no key of its own).
func notSignedByThisNode(err error) bool {
	return errors.Is(err, ErrCheckpointBadSignature)
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
