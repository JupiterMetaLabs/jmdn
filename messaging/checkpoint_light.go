package messaging

// Background full verification and light-node log pruning, both built on the
// chain-head checkpoints in checkpoint_sign.go.
//
// The boot gate walks only the log after the floor checkpoint. On a node that
// keeps the whole log, StartBackgroundFullVerify then walks it from seq 0
// against the latest checkpoint without delaying boot, so a change to records
// below the floor is still reported.
//
// A light node (storage.profile light) deletes canonical-log entries older
// than about storage.light_keep_blocks blocks. It cuts only below a checkpoint
// that is at or below its floor checkpoint: the boot gate anchors at the
// floor, so every record it reads is kept. Kept records are never rewritten.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gossipnode/internal/ckptfloor"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/checkpoint"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ion"
)

// RunBackgroundFullVerify walks the whole log against the latest checkpoint.
// It returns (false, nil) when there is nothing to check: no checkpoint, or a
// pruned log (which cannot be walked from seq 0).
func RunBackgroundFullVerify(ctx context.Context, store kv.Store, verify checkpoint.SigVerifier) (bool, error) {
	start, err := kv.LogStart(store)
	if err != nil {
		return false, fmt.Errorf("read log start: %w", err)
	}
	if start > 0 {
		return false, nil
	}
	latest, ok, err := checkpoint.Latest(store)
	if err != nil {
		return false, fmt.Errorf("load latest checkpoint: %w", err)
	}
	if !ok {
		return false, nil
	}
	if err := checkpoint.VerifyAgainstCheckpoint(ctx, store, latest, 0, make([]byte, 32), verify); err != nil {
		return false, err
	}
	return true, nil
}

// StartBackgroundFullVerify runs RunBackgroundFullVerify once and logs the
// result. Call it in its own goroutine after the boot gate.
func StartBackgroundFullVerify(ctx context.Context, store kv.Store, verify checkpoint.SigVerifier) {
	started := time.Now()
	ok, err := RunBackgroundFullVerify(ctx, store, verify)
	l := broadcastLogger()
	switch {
	case err != nil && ctx.Err() != nil:
		return
	case err != nil:
		l.Error(ctx, "[checkpoint] background full verification FAILED — the canonical log does not match its signed checkpoint from seq 0",
			err, ion.Duration("took", time.Since(started)))
	case ok:
		l.Info(ctx, "[checkpoint] background full verification passed (whole log from seq 0)",
			ion.Duration("took", time.Since(started)))
	default:
		l.Info(ctx, "[checkpoint] background full verification skipped (no checkpoint yet, or the log is pruned)")
	}
}

// pickPruneCheckpoint returns the newest checkpoint that may be pruned below:
// it has a block number, is at least keepBlocks blocks behind headBlock, and
// is at or below the floor checkpoint (floorSeq). ok=false when none fits.
// cps must be in ascending seq order.
func pickPruneCheckpoint(cps []*checkpoint.Checkpoint, headBlock, keepBlocks, floorSeq uint64) (*checkpoint.Checkpoint, bool) {
	if headBlock < keepBlocks {
		return nil, false
	}
	cutoff := headBlock - keepBlocks
	var pick *checkpoint.Checkpoint
	for _, c := range cps {
		if c.Seq > floorSeq {
			break
		}
		if c.BlockNumber == 0 || c.BlockNumber > cutoff {
			continue
		}
		pick = c
	}
	return pick, pick != nil
}

// PruneResult reports one light-node prune pass.
type PruneResult struct {
	Before    uint64 // new log start; 0 when nothing was pruned
	Deleted   int    // log entries deleted
	HeadBlock uint64 // block of the latest checkpoint
	CutBlock  uint64 // block of the checkpoint pruned below
}

// PruneLightLogOnce deletes canonical-log entries below the newest
// checkpoint that is keepBlocks behind the latest checkpoint and at or below
// the floor checkpoint in floorPath. Without a floor nothing is pruned: the
// floor is what the boot gate anchors on.
func PruneLightLogOnce(store kv.Store, floorPath string, keepBlocks uint64) (PruneResult, error) {
	floor, hasFloor, err := ckptfloor.Load(floorPath)
	if err != nil {
		return PruneResult{}, err
	}
	if !hasFloor {
		return PruneResult{}, nil
	}
	var cps []*checkpoint.Checkpoint
	if err := checkpoint.Each(store, func(c *checkpoint.Checkpoint) error {
		cps = append(cps, c)
		return nil
	}); err != nil {
		return PruneResult{}, fmt.Errorf("list checkpoints: %w", err)
	}
	if len(cps) == 0 {
		return PruneResult{}, nil
	}
	res := PruneResult{HeadBlock: cps[len(cps)-1].BlockNumber}
	pick, ok := pickPruneCheckpoint(cps, res.HeadBlock, keepBlocks, floor.Seq)
	if !ok {
		return res, nil
	}
	start, err := kv.LogStart(store)
	if err != nil {
		return res, fmt.Errorf("read log start: %w", err)
	}
	if pick.Seq+1 <= start {
		return res, nil // already pruned this far
	}
	n, err := kv.PruneLogBefore(store, pick.Seq+1)
	if err != nil {
		return res, fmt.Errorf("prune log below seq %d: %w", pick.Seq+1, err)
	}
	res.Before, res.Deleted, res.CutBlock = pick.Seq+1, n, pick.BlockNumber
	return res, nil
}

// RunLightLogPruner prunes every interval until ctx ends, and reclaims disk
// after a pass that deleted entries.
func RunLightLogPruner(ctx context.Context, store kv.Store, floorPath string, keepBlocks uint64, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		l := broadcastLogger()
		res, err := PruneLightLogOnce(store, floorPath, keepBlocks)
		switch {
		case err != nil:
			l.Warn(ctx, "[storage] light-node log prune failed (will retry)", ion.Err(err))
		case res.Deleted > 0:
			runs, gerr := kv.ReclaimSpace(store, 0.5, 20)
			if gerr != nil && !errors.Is(gerr, kv.ErrNotPrunable) {
				l.Warn(ctx, "[storage] value-log GC after prune failed", ion.Err(gerr))
			}
			l.Info(ctx, "[storage] light node pruned canonical log",
				ion.Uint64("log_start", res.Before), ion.Int("deleted", res.Deleted),
				ion.Uint64("cut_block", res.CutBlock), ion.Uint64("head_block", res.HeadBlock), ion.Int("gc_runs", runs))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
