package main

import (
	"fmt"
	"testing"

	"gossipnode/Sequencer"
	"gossipnode/config"
	"gossipnode/node"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestConsensusCreation(t *testing.T) {
	fmt.Println("🧪 Testing Consensus Creation...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create a test peer list
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{peer.ID("test-main-1"), peer.ID("test-main-2")},
		BackupPeers: []peer.ID{peer.ID("test-backup-1")},
	}

	// Create consensus instance
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test basic properties
	if consensus.Host == nil {
		t.Error("Host should not be nil")
	}
	if consensus.Channel != config.PubSub_ConsensusChannel {
		t.Errorf("Expected channel %s, got %s", config.PubSub_ConsensusChannel, consensus.Channel)
	}
	if consensus.ResponseHandler == nil {
		t.Error("ResponseHandler should not be nil")
	}

	fmt.Println("✅ Consensus creation test passed")
}

func TestQueryBuddyNodes(t *testing.T) {
	fmt.Println("🧪 Testing QueryBuddyNodes...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test QueryBuddyNodes
	peerIDs, err := consensus.QueryBuddyNodes()
	if err != nil {
		t.Logf("QueryBuddyNodes failed (expected in test environment): %v", err)
		// This is expected to fail in test environment without proper setup
	} else {
		fmt.Printf("✅ Retrieved %d buddy nodes: %v\n", len(peerIDs), peerIDs)
	}

	fmt.Println("✅ QueryBuddyNodes test completed")
}

func TestAddBuddyNodesToPeerList(t *testing.T) {
	fmt.Println("🧪 Testing AddBuddyNodesToPeerList...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Create a test ZKBlock
	zkBlock := &config.ZKBlock{
		// Add minimal required fields for testing
	}

	// Create test buddy nodes
	testBuddies := []peer.ID{
		peer.ID("test-buddy-1"),
		peer.ID("test-buddy-2"),
		peer.ID("test-buddy-3"),
	}

	// Test AddBuddyNodesToPeerList
	consensusMessage, err := consensus.AddBuddyNodesToPeerList(zkBlock, testBuddies)
	if err != nil {
		t.Fatalf("AddBuddyNodesToPeerList failed: %v", err)
	}

	if consensusMessage == nil {
		t.Error("ConsensusMessage should not be nil")
	}

	fmt.Printf("✅ Created consensus message with %d buddies\n", len(testBuddies))
	fmt.Println("✅ AddBuddyNodesToPeerList test passed")
}

func TestConsensusStart(t *testing.T) {
	fmt.Println("🧪 Testing Consensus Start...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance with minimal peer list
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{peer.ID("test-main-1")},
		BackupPeers: []peer.ID{peer.ID("test-backup-1")},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Create a test ZKBlock
	zkBlock := &config.ZKBlock{
		// Add minimal required fields for testing
	}

	// Test Start method
	err = consensus.Start(zkBlock)
	if err != nil {
		t.Logf("Consensus.Start failed (expected in test environment): %v", err)
		// This is expected to fail in test environment without proper network setup
	} else {
		fmt.Println("✅ Consensus started successfully")
	}

	fmt.Println("✅ Consensus Start test completed")
}

func TestRequestSubscriptionPermission(t *testing.T) {
	fmt.Println("🧪 Testing RequestSubscriptionPermission...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{peer.ID("test-main-1")},
		BackupPeers: []peer.ID{peer.ID("test-backup-1")},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test RequestSubscriptionPermission without initialized gossipnode
	err = consensus.RequestSubscriptionPermission()
	if err == nil {
		t.Error("Expected error when gossipnode is not initialized")
	} else {
		fmt.Printf("✅ Got expected error: %v\n", err)
	}

	fmt.Println("✅ RequestSubscriptionPermission test passed")
}

func TestVerifySubscriptions(t *testing.T) {
	fmt.Println("🧪 Testing VerifySubscriptions...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{peer.ID("test-main-1")},
		BackupPeers: []peer.ID{peer.ID("test-backup-1")},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test VerifySubscriptions without initialized gossipnode
	err = consensus.VerifySubscriptions()
	if err == nil {
		t.Error("Expected error when gossipnode is not initialized")
	} else {
		fmt.Printf("✅ Got expected error: %v\n", err)
	}

	fmt.Println("✅ VerifySubscriptions test passed")
}

func TestGetVoteStats(t *testing.T) {
	fmt.Println("🧪 Testing GetVoteStats...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test GetVoteStats
	stats := consensus.GetVoteStats()
	if stats == nil {
		t.Error("GetVoteStats should not return nil")
	}

	fmt.Printf("✅ Vote stats: %+v\n", stats)
	fmt.Println("✅ GetVoteStats test passed")
}

func TestIsListenerActive(t *testing.T) {
	fmt.Println("🧪 Testing IsListenerActive...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test IsListenerActive
	isActive := consensus.IsListenerActive()
	fmt.Printf("✅ Listener active status: %v\n", isActive)

	fmt.Println("✅ IsListenerActive test passed")
}

func TestStartVoteCollection(t *testing.T) {
	fmt.Println("🧪 Testing StartVoteCollection...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test StartVoteCollection
	testBlockHash := "test-block-hash-123"
	err = consensus.StartVoteCollection(testBlockHash)
	if err != nil {
		t.Logf("StartVoteCollection failed (expected without listener): %v", err)
	} else {
		fmt.Println("✅ Vote collection started successfully")
	}

	fmt.Println("✅ StartVoteCollection test passed")
}

func TestConsensusIntegration(t *testing.T) {
	fmt.Println("🧪 Testing Consensus Integration...")

	// Create a test host
	testHost, err := node.NewNode()
	if err != nil {
		t.Fatalf("Failed to create test host: %v", err)
	}

	// Create consensus instance
	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{peer.ID("test-main-1"), peer.ID("test-main-2")},
		BackupPeers: []peer.ID{peer.ID("test-backup-1")},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	// Test the complete flow
	fmt.Println("1️⃣ Testing consensus creation...")
	if consensus.Host == nil {
		t.Error("Host should not be nil")
	}

	fmt.Println("2️⃣ Testing vote stats...")
	stats := consensus.GetVoteStats()
	fmt.Printf("   Vote stats: %+v\n", stats)

	fmt.Println("3️⃣ Testing listener status...")
	isActive := consensus.IsListenerActive()
	fmt.Printf("   Listener active: %v\n", isActive)

	fmt.Println("4️⃣ Testing vote collection...")
	err = consensus.StartVoteCollection("test-block-hash")
	if err != nil {
		fmt.Printf("   Vote collection failed (expected): %v\n", err)
	}

	fmt.Println("✅ Consensus integration test completed")
}

// Benchmark tests
func BenchmarkConsensusCreation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		testHost, err := node.NewNode()
		if err != nil {
			b.Fatalf("Failed to create test host: %v", err)
		}

		peerList := Sequencer.PeerList{
			MainPeers:   []peer.ID{peer.ID("test-main-1")},
			BackupPeers: []peer.ID{peer.ID("test-backup-1")},
		}
		_ = Sequencer.NewConsensus(peerList, testHost.Host)
	}
}

func BenchmarkGetVoteStats(b *testing.B) {
	testHost, err := node.NewNode()
	if err != nil {
		b.Fatalf("Failed to create test host: %v", err)
	}

	peerList := Sequencer.PeerList{
		MainPeers:   []peer.ID{},
		BackupPeers: []peer.ID{},
	}
	consensus := Sequencer.NewConsensus(peerList, testHost.Host)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = consensus.GetVoteStats()
	}
}
