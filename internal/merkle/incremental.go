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
// Reorg safety: hashBlock chains through PrevHash and StateRoot, so a change to
// any block at or below the cached head changes the head block's leaf hash. Each
// Compute re-hashes the cached head block and compares it to the cached value; a
// mismatch (or a head that moved backwards) forces a full rebuild. The periodic
// full rebuild (fullRebuildEvery) is an additional safety net.
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

	forceFull := f.fullRebuildEvery > 0 && f.sinceFullRebuild >= f.fullRebuildEvery
	cacheUsable := f.ready && !forceFull && head >= f.head && uint64(len(f.leaves)) == f.head+1

	if cacheUsable {
		// Tip-consistency: the cached head block must be unchanged. hashBlock
		// chains via PrevHash+StateRoot, so any rewrite at or below f.head shows
		// up in the head block's leaf hash.
		tip, err := scan(ctx, f.head, f.head)
		if err != nil {
			return nil, err
		}
		if len(tip) == 1 && tip[0] == f.leaves[f.head] {
			if head == f.head {
				// Nothing changed — return the cached root without rebuilding.
				return &Result{Root: f.lastRoot, Head: f.head, Total: uint64(len(f.leaves))}, nil
			}
			// Pure append: read only the new blocks (f.head, head].
			newLeaves, err := scan(ctx, f.head+1, head)
			if err != nil {
				return nil, err
			}
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
	f.sinceFullRebuild++
	return &Result{Root: root, Head: head, Total: uint64(len(f.leaves))}, nil
}

// fullScan re-reads every leaf 0..head and resets the rebuild counter.
func (f *Fingerprinter) fullScan(ctx context.Context, scan leafScanFn, head uint64) error {
	log.Printf("[merkle] fingerprint full rebuild at head %d", head)
	leaves, err := scan(ctx, 0, head)
	if err != nil {
		return err
	}
	f.leaves = leaves
	f.head = head
	f.ready = true
	f.sinceFullRebuild = 0
	return nil
}

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
