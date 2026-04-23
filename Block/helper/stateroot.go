package helper

import (
	"fmt"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Compute_stateroot computes the new state root by hashing the previous state root with the new block hash
// It validates that the computed state root matches the state root in the payload
// Returns the computed state root if validation passes, otherwise returns an error
func Compute_stateroot(payload *config.ZKBlock) (common.Hash, error) {
	// Get the latest block number from the database
	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(nil)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to get latest block number: %w", err)
	}

	// If this is the genesis block (block 0), there's no previous state root
	if latestBlockNumber == 0 {
		// For genesis block, we might want to use a zero hash or accept the payload's state root
		// This depends on your blockchain's genesis logic
		return payload.StateRoot, nil
	}

	// Get the latest block from the database
	latestBlock, err := DB_OPs.GetZKBlockByNumber(nil, latestBlockNumber)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to get latest block: %w", err)
	}

	if latestBlock == nil {
		return common.Hash{}, fmt.Errorf("latest block not found")
	}

	// Compute the new state root by hashing the previous state root with the new block hash
	// This creates a chain of state roots that depends on both the previous state and the new block
	computedStateroot := crypto.Keccak256Hash(latestBlock.StateRoot.Bytes(), payload.BlockHash.Bytes())

	// Validate that the computed state root matches the payload's state root
	if computedStateroot != payload.StateRoot {
		return common.Hash{}, fmt.Errorf("stateroot mismatch: computed=%s, payload=%s",
			computedStateroot.Hex(), payload.StateRoot.Hex())
	}

	return computedStateroot, nil
}
