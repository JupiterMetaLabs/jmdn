package Test

import (
	"encoding/json"
	"fmt"
	"log"
	"testing"
	"time"

	Publish "gossipnode/Pubsub/Publish"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// testPubSubPublish tests publishing a message to the consensus channel
func testPubSubPublish(consensusHost host.Host, consensusGps *PubSubMessages.GossipPubSub) error {
	log.Println("=== Starting PubSub Publish Test ===")

	// Create a test consensus message
	testMessage := createTestConsensusMessage(consensusHost.ID())

	// Publish to consensus channel
	log.Printf("Publishing test message to channel: %s", config.PubSub_ConsensusChannel)
	err := Publish.Publish(consensusGps, config.PubSub_ConsensusChannel, testMessage, map[string]string{
		"test":      "true",
		"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		"host":      consensusHost.ID().String(),
	})

	if err != nil {
		log.Printf("Failed to publish test message: %v", err)
		return err
	}

	log.Println("Successfully published test message to consensus channel")
	log.Println("Waiting for 5 seconds to see if message is received by buddy nodes...")
	time.Sleep(5 * time.Second)

	return nil
}

// createTestConsensusMessage creates a test consensus message
func createTestConsensusMessage(hostID peer.ID) *PubSubMessages.Message {
	// Create a test vote/proposal message
	ack := PubSubMessages.NewACKBuilder().
		True_ACK_Message(hostID, config.Type_SubmitVote)

	message := PubSubMessages.NewMessageBuilder(nil).
		SetSender(hostID).
		SetMessage("Test consensus message from sequencer").
		SetTimestamp(time.Now().Unix()).
		SetACK(ack)

	return message
}

// testPublishProposalMessage publishes a proposal message for consensus
func testPublishProposalMessage(consensusHost host.Host, consensusGps *PubSubMessages.GossipPubSub, proposalData map[string]interface{}) error {
	log.Println("=== Publishing Proposal Message ===")

	// Create proposal message
	ack := PubSubMessages.NewACKBuilder().
		True_ACK_Message(consensusHost.ID(), "PROPOSAL")

	// Convert proposal data to JSON
	proposalBytes, err := json.Marshal(proposalData)
	if err != nil {
		return fmt.Errorf("failed to marshal proposal data: %v", err)
	}

	message := PubSubMessages.NewMessageBuilder(nil).
		SetSender(consensusHost.ID()).
		SetMessage(string(proposalBytes)).
		SetTimestamp(time.Now().Unix()).
		SetACK(ack)

	// Publish to consensus channel
	err = Publish.Publish(consensusGps, config.PubSub_ConsensusChannel, message, map[string]string{
		"type":      "proposal",
		"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		"host":      consensusHost.ID().String(),
	})

	if err != nil {
		log.Printf("Failed to publish proposal message: %v", err)
		return err
	}

	log.Println("Successfully published proposal message")
	return nil
}

// testPublishVoteRequest publishes a vote request message
func testPublishVoteRequest(consensusHost host.Host, consensusGps *PubSubMessages.GossipPubSub, roundID string) error {
	log.Println("=== Publishing Vote Request Message ===")

	// Create vote request message
	ack := PubSubMessages.NewACKBuilder().
		True_ACK_Message(consensusHost.ID(), "VOTE_REQUEST")

	message := PubSubMessages.NewMessageBuilder(nil).
		SetSender(consensusHost.ID()).
		SetMessage(fmt.Sprintf("Vote request for round: %s", roundID)).
		SetTimestamp(time.Now().Unix()).
		SetACK(ack)

	// Publish to consensus channel
	err := Publish.Publish(consensusGps, config.PubSub_ConsensusChannel, message, map[string]string{
		"type":      "vote_request",
		"round_id":  roundID,
		"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		"host":      consensusHost.ID().String(),
	})

	if err != nil {
		log.Printf("Failed to publish vote request: %v", err)
		return err
	}

	log.Println("Successfully published vote request message")
	return nil
}

// testConsensusFlow tests a complete consensus flow
func testConsensusFlow(consensusHost host.Host, consensusGps *PubSubMessages.GossipPubSub) error {
	log.Println("=== Starting Consensus Flow Test ===")

	// Print current buddy nodes
	printBuddyNodes()

	// Step 1: Publish initial consensus message
	log.Println("\nStep 1: Publishing initial consensus message...")
	if err := testPubSubPublish(consensusHost, consensusGps); err != nil {
		return err
	}

	// Wait for buddy nodes to process
	time.Sleep(2 * time.Second)

	// Step 2: Publish a proposal
	log.Println("\nStep 2: Publishing proposal...")
	proposalData := map[string]interface{}{
		"action": "TRANSFER",
		"from":   consensusHost.ID().String(),
		"to":     "target_peer",
		"amount": 100,
	}
	if err := testPublishProposalMessage(consensusHost, consensusGps, proposalData); err != nil {
		return err
	}

	// Wait for buddy nodes to receive proposal
	time.Sleep(2 * time.Second)

	// Step 3: Publish vote request
	log.Println("\nStep 3: Publishing vote request...")
	roundID := fmt.Sprintf("round-%d", time.Now().Unix())
	if err := testPublishVoteRequest(consensusHost, consensusGps, roundID); err != nil {
		return err
	}

	// Wait for buddy nodes to respond with votes
	time.Sleep(2 * time.Second)

	log.Println("\n=== Consensus Flow Test Complete ===")
	return nil
}

// printBuddyNodes prints the current active buddy nodes (renamed from PrintBuddyNodes)
func printBuddyNodes() {
	tracker := PubSubMessages.GetSubscriptionTracker()
	buddyNodes := tracker.GetBuddyNodes()

	log.Println("=== Current Active Buddy Nodes ===")
	log.Printf("Total active nodes: %d", tracker.GetActiveCount())

	for peerID, role := range buddyNodes {
		log.Printf("  - %s (role: %s)", peerID, role)
	}

	mainPeers := tracker.GetMainPeers()
	backupPeers := tracker.GetBackupPeers()

	log.Printf("\nMain peers (%d):", len(mainPeers))
	for _, peerID := range mainPeers {
		log.Printf("  - %s", peerID)
	}

	log.Printf("\nBackup peers (%d):", len(backupPeers))
	for _, peerID := range backupPeers {
		log.Printf("  - %s", peerID)
	}
	log.Println("==================================")
}

// TestPubSubPublish tests publishing to the consensus pubsub channel
// This test will run if a consensus instance is active
func TestPubSubPublish(t *testing.T) {
	// Get the global tracker to check if consensus is active
	tracker := PubSubMessages.GetSubscriptionTracker()
	activeCount := tracker.GetActiveCount()

	if activeCount == 0 {
		t.Skip("No active subscriptions found - consensus not started")
		return
	}

	t.Logf("Testing pubsub with %d active subscriptions", activeCount)

	// Print active buddy nodes
	t.Log("\n=== Active Buddy Nodes ===")
	buddyNodes := tracker.GetBuddyNodes()
	for peerID, role := range buddyNodes {
		t.Logf("  - %s (role: %s)", peerID, role)
	}

	// Note: To actually publish messages, we need access to:
	// - consensus.Host
	// - consensus.gossipnode.GetGossipPubSub()
	//
	// These should be passed from your application code that calls these functions
	t.Log("\n=== Test Complete ===")
	t.Log("Use the helper functions from your consensus instance:")
	t.Log("  Test.testPubSubPublish(consensus.Host, consensus.gossipnode.GetGossipPubSub())")
	t.Log("  Test.testConsensusFlow(consensus.Host, consensus.gossipnode.GetGossipPubSub())")
}

// Test functions for integration testing (use these from your application after consensus starts):
//
// Example usage after consensus.Start() succeeds:
//   Test.testPubSubPublish(consensus.Host, consensus.gossipnode.GetGossipPubSub())
//   Test.testConsensusFlow(consensus.Host, consensus.gossipnode.GetGossipPubSub())
//   Test.printBuddyNodes()
//
// Available helper functions:
//   - testPubSubPublish(host, gps): Publish a test message to consensus channel
//   - testConsensusFlow(host, gps): Run full consensus flow test
//   - testPublishProposalMessage(host, gps, data): Publish a proposal
//   - testPublishVoteRequest(host, gps, roundID): Publish a vote request
//   - printBuddyNodes(): Print active buddy nodes from global tracker
//   - createTestConsensusMessage(hostID): Create test message for consensus
