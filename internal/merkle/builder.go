// Package merkle builds a local Merkle root over all block hashes stored in
// the node's ImmuDB, using the JMDN_Merkletree library.
//
// This root is sent to the seednode as this node's block-state fingerprint.
// A mismatch against the sequencer's root means blocks are missing or corrupted.
package merkle

import (
	"context"
	"fmt"
	"log"

	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	merkletree "github.com/JupiterMetaLabs/JMDN_Merkletree/merkletree"
)

const defaultBatchSize = 1000

// Result holds the output of BuildLocalMerkleRoot.
type Result struct {
	Root  [32]byte // Merkle root over all block hashes 0..Head
	Head  uint64   // Latest block number included
	Total uint64   // Total blocks processed
}

// BuildLocalMerkleRoot iterates all blocks from the node's ImmuDB and returns
// a Merkle root commitment over their hashes.
//
// blockInfo is the NodeInfo adapter (types.BlockInfo) that provides block
// access — pass NodeInfo.NewSyncStruct() from the jmdn DB_OPs/Nodeinfo package.
func BuildLocalMerkleRoot(ctx context.Context, blockInfo fastsync_types.BlockInfo) (*Result, error) {
	head := blockInfo.GetBlockNumber()
	if head == 0 {
		return &Result{}, nil
	}

	start := uint64(0)

	cfg := merkletree.Config{
		ExpectedTotal: head + 1,
		// BlockMerge defaults to 0.5% of ExpectedTotal inside NewBuilder
	}
	builder, err := merkletree.NewBuilder(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Merkle builder: %w", err)
	}

	iter := blockInfo.NewBlockIterator(start, head, defaultBatchSize)
	defer iter.Close()

	var processed uint64
	for {
		// Check for context cancellation between batches
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		blocks, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("block iterator error at block ~%d: %w", processed, err)
		}
		if len(blocks) == 0 {
			break // exhausted
		}

		hashes := make([]merkletree.Hash32, 0, len(blocks))
		for _, b := range blocks {
			if b == nil {
				// Gap in DB — substitute zero hash so tree covers contiguous range
				hashes = append(hashes, merkletree.Hash32{})
				continue
			}
			// Recompute from all block fields (including transactions) rather
			// than trusting BlockHash, which is a wire-derived value from the
			// proto snapshot and does not cover transaction content.
			hashes = append(hashes, merkletree.Hash32(hashBlock(b)))
		}

		batchStart := processed + start
		if _, err := builder.Push(batchStart, hashes); err != nil {
			return nil, fmt.Errorf("Merkle push failed at height %d: %w", batchStart, err)
		}
		processed += uint64(len(blocks))
		log.Printf("[merkle] processed %d / %d blocks", processed, head+1)
	}

	root, err := builder.Finalize()
	if err != nil {
		return nil, fmt.Errorf("Merkle finalize failed: %w", err)
	}

	return &Result{
		Root:  root,
		Head:  head,
		Total: processed,
	}, nil
}
