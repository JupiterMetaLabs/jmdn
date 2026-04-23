package DB_OPs_Tests

import (
	"fmt"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/merkletree"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/stretchr/testify/assert"
)

func Test_GenerateMerkleTree(t *testing.T) {
	// Initialize settings
	if _, err := settings.Load(); err != nil {
		t.Logf("Failed to load settings: %v", err)
	}

	// Initialize DB Pool
	if err := DB_OPs.InitMainDBPool(config.DefaultConnectionPoolConfig()); err != nil {
		t.Logf("Pool might be already initialized: %v", err)
	}

	merkleProof := merkletree.NewMerkleProof().GetMainDBConnection()
	defer merkleProof.PutMainDBConnection()

	fmt.Println("=== Testing GenerateMerkleTree ===")

	// Test case: Generate tree for a range
	// We use a range that might be empty or not, but we want to see if it runs.
	startBlock := int64(0)
	endBlock := int64(-1)

	fmt.Printf("Generating Merkle Tree for blocks %d to %d...\n", startBlock, endBlock)
	snapshot, err := merkleProof.GenerateMerkleTree(startBlock, endBlock)

	if err != nil {
		t.Errorf("GenerateMerkleTree failed: %v", err)
	} else {
		fmt.Println("GenerateMerkleTree completed successfully.")

		// Verify Reconstruction
		fmt.Println("Verifying Reconstruction...")
		rebuiltBuilder, err := merkleProof.ReconstructTree(snapshot)
		if err != nil {
			t.Errorf("ReconstructTree failed: %v", err)
		} else {
			rebuiltRoot, err := rebuiltBuilder.Finalize()
			if err != nil {
				t.Errorf("Failed to finalize rebuilt tree: %v", err)
			}
			fmt.Printf("Reconstructed Root: %x\n", rebuiltRoot)
			fmt.Println("Reconstruction successful.")
			rebuiltBuilder.Visualize()
		}
	}

	assert.NoError(t, err)
}
