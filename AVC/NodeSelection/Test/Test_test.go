package test

import (
	"context"
	"fmt"
	"testing"

	"gossipnode/AVC/NodeSelection/pkg/selection"
)

// TestNodeSelectionWith16Nodes tests the NodeSelection package with 16 mock nodes
func TestNodeSelectionWith16Nodes(t *testing.T) {
	// Generate keys from mnemonic
	_, privateKey, err := selection.GenerateKeysFromMnemonic("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	// Select 3 buddies from 16 nodes
	Num := 10
	ctx := context.Background()
	buddies, err := selection.GetBuddyNodes(
		ctx ,
		"test-node-001",
		privateKey,
		[]byte("test-salt"),
		"34.174.233.203:17002",
		Num,
	)
	if err != nil {
		t.Fatalf("Failed to select buddies: %v", err)
	}

	// Check we got 3 buddies
	if len(buddies) != Num {
		t.Fatalf("Expected 3 buddies, got %d", len(buddies))
	}

	// Print results
	fmt.Printf("Selected %d buddies from %d nodes:\n", len(buddies), 0)
	for i, buddy := range buddies {
		fmt.Printf("%d. %s at %s\n", i+1, buddy.Node.ID, buddy.Node.Address)
	}
}

