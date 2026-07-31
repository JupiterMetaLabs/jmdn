package merkle

import (
	"context"
	"fmt"
	"log"
	"sync"

	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	merkletree "github.com/JupiterMetaLabs/JMDN_Merkletree/merkletree"
)

// DefaultFullRebuildEvery is the number of incremental Compute calls between
// forced full rebuilds. The tip-consistency check already catches reorgs, so
// this is a cheap belt-and-suspenders against any out-of-band DB rewrite that
// leaves the cached head block untouched. Set <= 0 to disable.
const DefaultFullRebuildEvery = 20

// Fingerprinter computes the node's local block-state Merkle root incrementally.
//
// The from-scratch BuildLocalMerkleRoot re-reads and re-hashes EVERY block
// (0..head) from ImmuDB on every call — O(chain length) per tick, and the
// dominant source of allocation churn and ImmuDB read load on a running node.
// Fingerprinter caches the per-height leaf hashes and, on each Compute, re-reads
// only the blocks appended since the previous call, rebuilding the tree from the
// cached leaves. Because it feeds the identical leaf sequence to the identical
// builder, the resulting root is byte-for-byte equal to BuildLocalMerkleRoot for
// the same chain state — it is a drop-in for the sync monitor. Per-tick cost
// drops from O(head) to O(new blocks); ImmuDB reads drop from the whole chain to
// the delta (plus one block for the tip check).
//
// Reorg safety and its limit: hashBlock chains through PrevHash and StateRoot,
// so a genuine reorg — successor blocks replaced — changes the cached head
// block's leaf hash, which the per-Compute tip re-hash detects and turns into a
// full rebuild. What the tip check does NOT catch is an isolated in-place write
// BELOW a static head that leaves the head block untouched — e.g. catch-up
// backfilling a gap at height N-5 (zero-hash -> real hash) while the head stays
// at N. That leaves the cache stale, but the failure direction is SAFE: a stale
// root mismatches peers and triggers an extra reconcile; it can never report a
// false "in sync". The periodic full rebuild (fullRebuildEvery) bounds that
// window — and because the rebuild counter advances on the no-change path too
// (see compute), it fires even on a node whose head never moves.
//
// A Fingerprinter is safe for a single serial caller (the sync monitor); the
// mutex guards against an accidental concurrent Compute. It does not touch
// account state and never acquires DB_OPs.LockStateApply.
type Fingerprinter struct {
	mu               sync.Mutex
	leaves           []merkletree.Hash32 // one leaf per height; index == block height
	head             uint64              // highest height currently folded
	ready            bool                // cache populated at least once
	lastRoot         [32]byte            // root for the current leaves (returned when unchanged)
	sinceFullRebuild int                 // Compute calls since the last full rebuild
	fullRebuildEvery int                 // forced-rebuild cadence; <= 0 disables it

	// Lifetime diagnostics, reported on each full-rebuild log line. Not cleared
	// by reset(): they describe this process, not the current cache generation.
	// Healthy steady state is cachedComputes climbing while fullRebuilds grows
	// only on the fullRebuildEvery cadence and leavesRead tracks new blocks, not
	// chain length. fullRebuilds tracking cachedComputes means the cache has
	// stopped engaging and the node is paying the pre-cache O(chain) read cost
	// every tick — invisible otherwise, since the root stays correct either way.
	cachedComputes uint64 // served from the cache — tip check passed
	fullRebuilds   uint64 // required a full O(chain) rescan
	leavesRead     uint64 // leaf hashes read from the DB across all computes
}

// NewFingerprinter returns an incremental fingerprinter. fullRebuildEvery is the
// number of incremental computes between forced full rebuilds (a safety net on
// top of the per-tick tip-consistency check); pass 0 to disable it.
func NewFingerprinter(fullRebuildEvery int) *Fingerprinter {
	return &Fingerprinter{fullRebuildEvery: fullRebuildEvery}
}

// leafScanFn returns the leaf hashes for the inclusive height range [start, end]
// (one entry per height, zero hash for a gap). It is the seam that lets the fold
// logic be exercised without a full BlockInfo, and lets production wrap the real
// ImmuDB-backed iterator.
type leafScanFn func(ctx context.Context, start, end uint64) ([]merkletree.Hash32, error)

// Compute returns the current local Merkle root, reusing cached leaves for every
// block except those appended since the previous call. Drop-in replacement for
// BuildLocalMerkleRoot(ctx, blockInfo).
func (f *Fingerprinter) Compute(ctx context.Context, blockInfo fastsync_types.BlockInfo) (*Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	scan := func(ctx context.Context, start, end uint64) ([]merkletree.Hash32, error) {
		return scanLeaves(ctx, blockInfo, start, end)
	}
	return f.compute(ctx, blockInfo.GetBlockNumber, scan)
}

// compute is the pure fold logic, decoupled from BlockInfo for testing. Callers
// hold f.mu.
func (f *Fingerprinter) compute(ctx context.Context, headFn func() uint64, scan leafScanFn) (*Result, error) {
	head := headFn()
	if head == 0 {
		f.reset()
		return &Result{}, nil
	}

	// Count this compute BEFORE any branch (including the no-change fast path)
	// so the forced-rebuild counter advances even when the head is static —
	// otherwise a below-head change the tip check cannot see could sit in the
	// cache indefinitely on an idle node (see the "Reorg safety" note). fullScan
	// resets this to 0, so with ">=" a full rebuild lands exactly every
	// fullRebuildEvery computes.
	f.sinceFullRebuild++
	forceFull := f.fullRebuildEvery > 0 && f.sinceFullRebuild >= f.fullRebuildEvery
	cacheUsable := f.ready && !forceFull && head >= f.head && uint64(len(f.leaves)) == f.head+1

	if cacheUsable {
		// Tip-consistency: the cached head block must be unchanged. This detects a
		// reorg (which rewrites the head block too), not an in-place write below a
		// static head — see the reorg-safety note on Fingerprinter.
		tip, err := scan(ctx, f.head, f.head)
		if err != nil {
			return nil, err
		}
		f.leavesRead += uint64(len(tip))
		if len(tip) == 1 && tip[0] == f.leaves[f.head] {
			f.cachedComputes++
			if head == f.head {
				// Nothing changed — return the cached root without rebuilding.
				return &Result{Root: f.lastRoot, Head: f.head, Total: uint64(len(f.leaves))}, nil
			}
			// Pure append: read only the new blocks (f.head, head].
			newLeaves, err := scan(ctx, f.head+1, head)
			if err != nil {
				return nil, err
			}
			f.leavesRead += uint64(len(newLeaves))
			f.leaves = append(f.leaves, newLeaves...)
			f.head = head
			return f.finalize(head)
		}
		// Tip changed → a reorg/rewrite at or below the cached head → full rebuild.
	}

	// Cold start, forced periodic rebuild, chain shrank, or reorg detected.
	if err := f.fullScan(ctx, scan, head); err != nil {
		return nil, err
	}
	return f.finalize(head)
}

// finalize builds the root from the current cached leaves, caches it, and
// returns the Result. Callers hold f.mu.
func (f *Fingerprinter) finalize(head uint64) (*Result, error) {
	root, err := rootFromLeaves(head, f.leaves)
	if err != nil {
		return nil, err
	}
	f.lastRoot = root
	// NOTE: sinceFullRebuild is advanced at the top of compute (every call),
	// not here, so the periodic rebuild also fires on the no-change path.
	return &Result{Root: root, Head: head, Total: uint64(len(f.leaves))}, nil
}

// fullScan re-reads every leaf 0..head and resets the rebuild counter.
func (f *Fingerprinter) fullScan(ctx context.Context, scan leafScanFn, head uint64) error {
	f.fullRebuilds++
	// Logged with the running tallies so a node that has silently fallen back to
	// rebuilding every tick (cache never usable) is visible in the logs alone,
	// without needing to scrape Stats.
	log.Printf("[merkle] fingerprint full rebuild at head %d (rebuilds=%d, cached=%d, leaves_read=%d)",
		head, f.fullRebuilds, f.cachedComputes, f.leavesRead)
	leaves, err := scan(ctx, 0, head)
	if err != nil {
		return err
	}
	f.leavesRead += uint64(len(leaves))
	f.leaves = leaves
	f.head = head
	f.ready = true
	f.sinceFullRebuild = 0
	return nil
}

// reset drops the cache (head 0 / empty chain). The lifetime counters in Stats
// are deliberately NOT cleared — they describe this process, not this cache
// generation.
func (f *Fingerprinter) reset() {
	f.leaves = nil
	f.head = 0
	f.ready = false
	f.lastRoot = [32]byte{}
	f.sinceFullRebuild = 0
}

// scanLeaves reads blocks [start, end] (inclusive) from ImmuDB and returns their
// leaf hashes, mirroring BuildLocalMerkleRoot's leaf computation exactly: a
// missing block (gap) becomes a zero hash; otherwise hashBlock over all block
// content. Keeping this identical to builder.go is what guarantees the same root.
func scanLeaves(ctx context.Context, blockInfo fastsync_types.BlockInfo, start, end uint64) ([]merkletree.Hash32, error) {
	iter := blockInfo.NewBlockIterator(start, end, defaultBatchSize)
	defer iter.Close()

	leaves := make([]merkletree.Hash32, 0, end-start+1)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		blocks, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("block iterator error near height %d: %w", start+uint64(len(leaves)), err)
		}
		if len(blocks) == 0 {
			break // exhausted
		}
		for _, b := range blocks {
			if b == nil {
				leaves = append(leaves, merkletree.Hash32{})
				continue
			}
			leaves = append(leaves, merkletree.Hash32(hashBlock(b)))
		}
	}
	return leaves, nil
}

// rootFromLeaves builds the Merkle root over an in-memory leaf slice using the
// same builder, config, and batch boundaries as BuildLocalMerkleRoot, so the
// root is identical for the same leaves. The root is a function of the ordered
// leaves only (all nodes must agree regardless of ingestion), so the fixed
// batching here matches the iterator-driven batching in builder.go.
func rootFromLeaves(head uint64, leaves []merkletree.Hash32) ([32]byte, error) {
	builder, err := merkletree.NewBuilder(merkletree.Config{ExpectedTotal: head + 1})
	if err != nil {
		return [32]byte{}, fmt.Errorf("failed to create Merkle builder: %w", err)
	}
	for start := 0; start < len(leaves); start += defaultBatchSize {
		end := start + defaultBatchSize
		if end > len(leaves) {
			end = len(leaves)
		}
		if _, err := builder.Push(uint64(start), leaves[start:end]); err != nil {
			return [32]byte{}, fmt.Errorf("Merkle push failed at height %d: %w", start, err)
		}
	}
	root, err := builder.Finalize()
	if err != nil {
		return [32]byte{}, fmt.Errorf("Merkle finalize failed: %w", err)
	}
	return root, nil
}
