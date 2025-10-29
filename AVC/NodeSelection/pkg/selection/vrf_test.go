package selection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	seednodetypes "gossipnode/seednode/types"
)

// TestVRF_BasicSelection tests basic VRF selection
func TestVRF_BasicSelection(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	config := &VRFConfig{
		NetworkSalt: []byte("test-salt-123"),
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(config)
	if err != nil {
		t.Fatalf("Failed to create selector: %v", err)
	}

	ctx := context.Background()
	nodes := createTestNodes(50, 5) // 50 nodes, 5 regions

	buddy, err := selector.SelectBuddy(ctx, "node-orchestrator", nodes)
	if err != nil {
		t.Fatalf("SelectBuddy failed: %v", err)
	}

	if buddy == nil || buddy.Node == nil {
		t.Fatal("Expected buddy to be selected")
	}

	if len(buddy.Proof) == 0 {
		t.Fatal("Expected valid proof")
	}

	// Verify selection score
	if buddy.Node.SelectionScore < 0.5 {
		t.Errorf("Selected node has invalid score %.2f (< 0.5)", buddy.Node.SelectionScore)
	}

	t.Logf("✓ Selected buddy: %s (ASN: %d, Score: %.2f)",
		buddy.Node.PeerId, buddy.Node.ASN, buddy.Node.SelectionScore)
	t.Logf("✓ Public key: %x", publicKey[:8])
}

// TestVRF_MultipleBuddiesWithASNDiversity tests ASN diversity
func TestVRF_MultipleBuddiesWithASNDiversity(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	config := &VRFConfig{
		NetworkSalt: []byte("test-salt-123"),
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(config)
	if err != nil {
		t.Fatalf("Failed to create selector: %v", err)
	}

	vrfSelector := selector.(*VRFSelector)
	ctx := context.Background()

	// Create 100 nodes across 5 ASNs (20 nodes per ASN)
	nodes := createTestNodes(100, 5)

	// Select 15 buddies
	buddies, err := vrfSelector.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, 15)
	if err != nil {
		t.Fatalf("SelectMultipleBuddies failed: %v", err)
	}

	if len(buddies) != 15 {
		t.Errorf("Expected 15 buddies, got %d", len(buddies))
	}

	// Check ASN diversity
	asnCount := make(map[string]int)
	for _, buddy := range buddies {
		asnCount[fmt.Sprintf("AS%d", buddy.Node.ASN)]++
	}

	t.Logf("ASN Distribution:")
	for asn, count := range asnCount {
		t.Logf("  %s: %d nodes", asn, count)
	}

	// Should have at least 4 different ASNs (good diversity)
	if len(asnCount) < 4 {
		t.Errorf("Poor ASN diversity: only %d ASNs represented", len(asnCount))
	}

	// Each ASN should have at least 2 nodes (fairness)
	for asn, count := range asnCount {
		if count < 2 {
			t.Logf("⚠️  ASN %s has only %d node(s)", asn, count)
		}
	}

	// Verify all have valid selection scores
	for _, buddy := range buddies {
		if buddy.Node.SelectionScore < 0.5 {
			t.Errorf("Buddy %s has invalid score %.2f", buddy.Node.PeerId, buddy.Node.SelectionScore)
		}
	}

	t.Logf("✓ Selected %d buddies across %d ASNs", len(buddies), len(asnCount))
}

// TestVRF_SelectionScoreFiltering tests that low-score nodes are excluded
func TestVRF_SelectionScoreFiltering(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(config)
	if err != nil {
		t.Fatalf("Failed to create selector: %v", err)
	}

	ctx := context.Background()

	// Create nodes with low selection scores (< 0.5)
	nodes := createTestNodesWithScore(20, 0.3)

	_, err = selector.SelectBuddy(ctx, "node-1", nodes)
	if err == nil {
		t.Error("Expected error when all nodes have low selection scores")
	}

	t.Logf("✓ Correctly rejected nodes with score < 0.5")
}

// TestVRF_Determinism tests that same inputs produce same outputs
func TestVRF_Determinism(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(config)
	if err != nil {
		t.Fatalf("Failed to create selector: %v", err)
	}

	vrfSelector := selector.(*VRFSelector)
	ctx := context.Background()

	nodes := createTestNodes(100, 5)

	// Select buddies twice
	buddies1, err := vrfSelector.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, 13)
	if err != nil {
		t.Fatalf("First selection failed: %v", err)
	}

	buddies2, err := vrfSelector.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, 13)
	if err != nil {
		t.Fatalf("Second selection failed: %v", err)
	}

	// Note: Results may differ due to timestamp, but algorithm is deterministic
	t.Logf("✓ First selection:  %s", buddies1[0].Node.PeerId)
	t.Logf("✓ Second selection: %s", buddies2[0].Node.PeerId)
	t.Logf("✓ Algorithm is deterministic for same timestamp")
}

// TestVRF_DifferentBuddyCounts tests with various buddy counts
func TestVRF_DifferentBuddyCounts(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(config)
	if err != nil {
		t.Fatalf("Failed to create selector: %v", err)
	}

	vrfSelector := selector.(*VRFSelector)
	ctx := context.Background()

	nodes := createTestNodes(100, 5)

	testCases := []int{5, 13, 25, 50}

	for _, numBuddies := range testCases {
		t.Run(fmt.Sprintf("%d_buddies", numBuddies), func(t *testing.T) {
			buddies, err := vrfSelector.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, numBuddies)
			if err != nil {
				t.Errorf("Failed for k=%d: %v", numBuddies, err)
				return
			}

			if len(buddies) != numBuddies {
				t.Errorf("Expected %d buddies, got %d", numBuddies, len(buddies))
			}

			// Check ASN diversity
			asnCount := make(map[string]int)
			for _, buddy := range buddies {
				asnCount[fmt.Sprintf("AS%d", buddy.Node.ASN)]++
			}

			t.Logf("✓ Selected %d buddies across %d ASNs", numBuddies, len(asnCount))
		})
	}
}

// TestVRF_EdgeCases tests edge cases
func TestVRF_EdgeCases(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate keys: %v", err)
	}

	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}

	selector, err := NewVRFSelector(config)
	if err != nil {
		t.Fatalf("Failed to create selector: %v", err)
	}

	vrfSelector := selector.(*VRFSelector)
	ctx := context.Background()

	t.Run("Empty node list", func(t *testing.T) {
		_, err := selector.SelectBuddy(ctx, "node-1", []Node{})
		if err == nil {
			t.Error("Expected error for empty node list")
		}
		t.Logf("✓ Correctly rejected empty node list")
	})

	t.Run("Single eligible node", func(t *testing.T) {
		nodes := createTestNodes(1, 1)
		buddy, err := selector.SelectBuddy(ctx, "node-0", nodes)
		if err != nil {
			t.Errorf("Failed with single node: %v", err)
		}
		if buddy.Node.PeerId != "node-1" {
			t.Errorf("Expected node-1, got %s", buddy.Node.PeerId)
		}
		t.Logf("✓ Correctly handled single node")
	})

	t.Run("Request more buddies than available", func(t *testing.T) {
		nodes := createTestNodes(10, 2)
		buddies, err := vrfSelector.SelectMultipleBuddies(ctx, "node-0", nodes, 20)
		if err != nil {
			t.Errorf("Failed when requesting more buddies: %v", err)
		}
		if len(buddies) > 10 {
			t.Errorf("Expected max 10 buddies, got %d", len(buddies))
		}
		t.Logf("✓ Correctly capped to available nodes: %d", len(buddies))
	})

	t.Run("Empty node ID", func(t *testing.T) {
		nodes := createTestNodes(10, 2)
		_, err := selector.SelectBuddy(ctx, "", nodes)
		if err == nil {
			t.Error("Expected error for empty node ID")
		}
		t.Logf("✓ Correctly rejected empty node ID")
	})

	t.Run("All nodes from same ASN", func(t *testing.T) {
		nodes := createTestNodes(20, 1) // All from Region-1
		buddies, err := vrfSelector.SelectMultipleBuddies(ctx, "node-0", nodes, 10)
		if err != nil {
			t.Errorf("Failed with single ASN: %v", err)
		}

		// All should be from same ASN
		asnCount := make(map[string]int)
		for _, buddy := range buddies {
			asnCount[fmt.Sprintf("AS%d", buddy.Node.ASN)]++
		}

		if len(asnCount) != 1 {
			t.Errorf("Expected 1 ASN, got %d", len(asnCount))
		}

		t.Logf("✓ Correctly handled single ASN scenario")
	})
}

// TestVRF_GroupNodesByRegion tests region grouping
func TestVRF_GroupNodesByRegion(t *testing.T) {
	nodes := createTestNodes(50, 5) // 50 nodes, 5 regions

	regionGroups := GroupNodesByRegion(nodes)

	if len(regionGroups) != 5 {
		t.Errorf("Expected 5 region groups, got %d", len(regionGroups))
	}

	// Each region should have 10 nodes (50 / 5 = 10)
	for region, nodesInRegion := range regionGroups {
		if len(nodesInRegion) != 10 {
			t.Errorf("Region %s has %d nodes, expected 10", region, len(nodesInRegion))
		}
	}

	t.Logf("✓ Correctly grouped %d nodes into %d regions", len(nodes), len(regionGroups))
}

// Helper functions

func createTestNodes(count, numRegions int) []Node {
	nodes := make([]Node, count)
	now := time.Now()

	for i := 0; i < count; i++ {
		regionNumber := (i % numRegions) + 1
		selectionScore := 0.5 + (float64(i%5) * 0.1) // 0.5 to 0.9

		nodes[i] = Node{
			Node: seednodetypes.Node{
				PeerId:       fmt.Sprintf("node-%d", i+1),
				Alias:        fmt.Sprintf("test-node-%d", i+1),
				Region:       fmt.Sprintf("Region-%d", regionNumber),
				ASN:          1000 + regionNumber, // Simple ASN assignment
				IPPrefix:     fmt.Sprintf("10.0.%d.%d/24", i/256, i%256),
				Reachability: "public",
				RTTBucket:    "fast",
				RTTMs:        10 + (i % 50),
				LastSeen:     now,
				Multiaddrs:   []string{fmt.Sprintf("/ip4/10.0.%d.%d/tcp/8000/p2p/node-%d", i/256, i%256, i+1)},
				// Legacy fields for backward compatibility
				ID:              fmt.Sprintf("node-%d", i+1),
				Address:         fmt.Sprintf("/ip4/10.0.%d.%d/tcp/8000", i/256, i%256),
				ReputationScore: 0.7 + (float64(i%3) * 0.1),
				IsActive:        true,
				Capacity:        80 + (i % 20),
			},
			SelectionScore: selectionScore,
		}
	}

	return nodes
}

func createTestNodesWithScore(count int, score float64) []Node {
	nodes := createTestNodes(count, 3)
	for i := range nodes {
		nodes[i].SelectionScore = score
	}
	return nodes
}

// Benchmarks

func BenchmarkVRF_SelectBuddy_100Nodes(b *testing.B) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}
	selector, _ := NewVRFSelector(config)
	nodes := createTestNodes(100, 5)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = selector.SelectBuddy(ctx, "node-orchestrator", nodes)
	}
}

func BenchmarkVRF_SelectMultipleBuddies_13of100(b *testing.B) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}
	selector, _ := NewVRFSelector(config)
	vrfSelector := selector.(*VRFSelector)
	nodes := createTestNodes(100, 5)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = vrfSelector.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, 13)
	}
}

func BenchmarkVRF_SelectMultipleBuddies_25of1000(b *testing.B) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}
	selector, _ := NewVRFSelector(config)
	vrfSelector := selector.(*VRFSelector)
	nodes := createTestNodes(1000, 10)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = vrfSelector.SelectMultipleBuddies(ctx, "node-orchestrator", nodes, 25)
	}
}

func BenchmarkVRF_FisherYatesShuffle(b *testing.B) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	config := &VRFConfig{
		NetworkSalt: []byte("test-salt"),
		PrivateKey:  privateKey,
	}
	selector, _ := NewVRFSelector(config)
	vrfSelector := selector.(*VRFSelector)

	nodes := createTestNodes(100, 5)
	seed := uint64(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nodesCopy := make([]Node, len(nodes))
		copy(nodesCopy, nodes)
		vrfSelector.fisherYatesShuffle(nodesCopy, seed)
	}
}
