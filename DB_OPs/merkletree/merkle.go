package merkletree

import (
	log "gossipnode/logging"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/JMDN_Merkletree/merkletree"
	"github.com/JupiterMetaLabs/ion"
)

type MerkleProof struct {
	mainDBClient *config.PooledConnection
}

type MerkleProofInterface interface {
	GenerateMerkleTree(ctx context.Context, startBlock, endBlock int64) (*merkletree.MerkleTreeSnapshot, error)
	ReconstructTree(snap *merkletree.MerkleTreeSnapshot) (*merkletree.Builder, error)
	GetMainDBConnection() *MerkleProof
	PutMainDBConnection()
}

func NewMerkleProof() MerkleProofInterface {
	return &MerkleProof{}
}

func (m *MerkleProof) GetMainDBConnection() *MerkleProof {
	// Pool acquisition removed — getHandle(nil) uses the global ThebeDB handle.
	return &MerkleProof{}
}

func (m *MerkleProof) PutMainDBConnection() {
	// No-op: connection lifecycle is now managed by the global ThebeDB handle.
}

func (m *MerkleProof) GenerateMerkleTree(ctx context.Context, startBlock, endBlock int64) (*merkletree.MerkleTreeSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if endBlock == -1 {
		// If the endBlock is -1, then we need to get the latest block number from the db.
		latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(ctx, m.mainDBClient)
		if err != nil {
			return nil, fmt.Errorf("failed to get latest block number: %w", err)
		}
		logger(log.DB_OPs_MerkleTree).Debug(ctx, "Latest block number", ion.Int64("latest_block_number", int64(latestBlockNumber)), ion.String("function", "DB_OPs.merkletree.GenerateMerkleTree"))
		endBlock = int64(latestBlockNumber)
	} else if endBlock < startBlock {
		str := fmt.Sprintf("endBlock (%d) cannot be less than startBlock (%d)", endBlock, startBlock)
		err := errors.New(str)

		logger(log.DB_OPs_MerkleTree).Error(ctx, "GenerateMerkleTree", err,
			ion.Int64("start_block", startBlock),
			ion.Int64("end_block", endBlock),
		)
		return nil, err
	} else if endBlock < -1 {
		str := fmt.Sprintf("endBlock (%d) cannot be less than -1", endBlock)
		err := errors.New(str)

		logger(log.DB_OPs_MerkleTree).Error(ctx, "GenerateMerkleTree", err,
			ion.Int64("start_block", startBlock),
			ion.Int64("end_block", endBlock),
		)
		return nil, err
	}

	cfg := merkletree.Config{
		ExpectedTotal: uint64(endBlock - startBlock + 1),
		BlockMerge:    int(math.Ceil(float64(endBlock-startBlock+1) * 0.005)),
	}

	logger(log.DB_OPs_MerkleTree).Debug(context.Background(), "Block merge configuration", ion.Int("block_merge", cfg.BlockMerge))

	Builder, err := merkletree.NewBuilder(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create builder: %w", err)
	}

	iterator := DB_OPs.NewBlockIterator(m.mainDBClient, uint64(startBlock), uint64(endBlock), 1000)

	logger(log.DB_OPs_MerkleTree).Info(ctx, "Starting Merkle Tree generation",
		ion.Int64("start_block", startBlock),
		ion.Int64("end_block", endBlock),
	)

	expectedBlockNumber := uint64(startBlock)

	for {
		blocks, err := iterator.Next()
		if err != nil {
			logger(log.DB_OPs_MerkleTree).Error(ctx, "Failed to retrieve block batch",
				err,
				ion.String("function", "DB_OPs.merkletree.GenerateMerkleTree"),
			)
			return nil, fmt.Errorf("failed to retrieve blocks: %w", err)
		}

		if blocks == nil {
			break
		}

		for _, block := range blocks {
			if block.BlockNumber > expectedBlockNumber {
				gapSize := block.BlockNumber - expectedBlockNumber
				logger(log.DB_OPs_MerkleTree).Warn(ctx, "Detected missing blocks, filling with empty hashes",
					ion.Uint64("gap_start", expectedBlockNumber),
					ion.Uint64("gap_end", block.BlockNumber-1),
					ion.Uint64("gap_size", gapSize),
				)

				padding := make([]merkletree.Hash32, gapSize)
				_, err = Builder.Push(expectedBlockNumber, padding)
				if err != nil {
					return nil, fmt.Errorf("failed to push padding for gap: %w", err)
				}
			}

			hashe := merkletree.Hash32(block.BlockHash)
			_, err = Builder.Push(block.BlockNumber, []merkletree.Hash32{hashe})
			if err != nil {
				logger(log.DB_OPs_MerkleTree).Error(ctx, "Failed to push block to merkle builder",
					err,
					ion.String("function", "DB_OPs.merkletree.GenerateMerkleTree"),
				)
				return nil, fmt.Errorf("failed to push block %d: %w", block.BlockNumber, err)
			}

			expectedBlockNumber = block.BlockNumber + 1
		}
	}

	if expectedBlockNumber <= uint64(endBlock) {
		gapSize := uint64(endBlock) - expectedBlockNumber + 1
		logger(log.DB_OPs_MerkleTree).Warn(ctx, "Detected missing trailing blocks, filling with empty hashes",
			ion.Uint64("gap_start", expectedBlockNumber),
			ion.Uint64("gap_end", uint64(endBlock)),
			ion.Uint64("gap_size", gapSize),
		)
		padding := make([]merkletree.Hash32, gapSize)
		_, err = Builder.Push(expectedBlockNumber, padding)
		if err != nil {
			return nil, fmt.Errorf("failed to push trailing padding: %w", err)
		}
	}

	root, err := Builder.Finalize()
	if err != nil {
		return nil, fmt.Errorf("failed to finalize merkle tree: %w", err)
	}

	logger(log.DB_OPs_MerkleTree).Info(ctx, "Merkle Tree generation completed",
		ion.String("root", hex.EncodeToString(root[:])),
	)

	Builder.Visualize()

	snapshot := Builder.ToSnapshot()
	return snapshot, nil
}

// ReconstructTree restores a Merkle Builder from a MerkleTreeSnapshot.
func (m *MerkleProof) ReconstructTree(snap *merkletree.MerkleTreeSnapshot) (*merkletree.Builder, error) {
	builder, err := snap.FromSnapshot(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to restore builder from snapshot: %w", err)
	}
	return builder, nil
}
