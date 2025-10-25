// =============================================================================
// EXAMPLE: How to use GossipSubMessenger in production
// =============================================================================
// package main

// import (
// 	"context"
// 	"fmt"
// 	"log"
// 	"time"

// 	"github.com/JupiterMetaLabs/Asynchronous-Validation-Consensus/pkg/bft"
// 	pubsub "github.com/libp2p/go-libp2p-pubsub"
// 	"github.com/libp2p/go-libp2p/core/host"
// )

// // ===========================================================================
// // SCENARIO: Buddy Node Running BFT Consensus
// // ===========================================================================

// func RunBuddyBFTConsensus(
// 	ctx context.Context,
// 	host host.Host,
// 	ps *pubsub.PubSub,
// 	roundNumber uint64,
// 	blockHash string,
// 	myBuddyID string,
// 	myDecision bft.Decision,
// 	allBuddies []bft.BuddyInput,
// 	myPrivateKey []byte,
// ) error {

// 	log.Printf("🚀 Buddy %s starting BFT consensus for round %d", myBuddyID, roundNumber)

// 	// Step 1: Create and start GossipSub messenger
// 	messenger, err := bft.CreateBFTMessenger(ctx, host, ps, roundNumber)
// 	if err != nil {
// 		return fmt.Errorf("failed to create messenger: %w", err)
// 	}
// 	defer messenger.Stop()

// 	log.Printf("✅ Joined GossipSub topic: bft-round-%d", roundNumber)

// 	// Give time for all buddies to join the topic
// 	time.Sleep(2 * time.Second)

// 	// Step 2: Prepare BFT inputs
// 	myBuddyInput := bft.BuddyInput{
// 		ID:         myBuddyID,
// 		Decision:   myDecision,
// 		PublicKey:  nil, // Should be set from buddy data
// 		PrivateKey: myPrivateKey,
// 	}

// 	// Step 3: Run BFT consensus
// 	bftEngine := bft.New(bft.DefaultConfig())

// 	result, err := bftEngine.RunConsensus(
// 		ctx,
// 		roundNumber,
// 		blockHash,
// 		myBuddyID,
// 		allBuddies,
// 		messenger, // Using real GossipSub messenger!
// 	)

// 	if err != nil {
// 		return fmt.Errorf("BFT consensus failed: %w", err)
// 	}

// 	// Step 4: Handle result
// 	if result.Success {
// 		log.Printf("✅ Consensus reached!")
// 		log.Printf("   Decision: %s", result.Decision)
// 		log.Printf("   Block Accepted: %v", result.BlockAccepted)
// 		log.Printf("   Duration: %v", result.TotalDuration)
// 	} else {
// 		log.Printf("❌ Consensus failed: %s", result.FailureReason)
// 	}

// 	return nil
// }

// // ===========================================================================
// // SCENARIO: Complete Flow from Sequencer Announcement to BFT
// // ===========================================================================

// func BuddyNodeCompleteFlow(
// 	ctx context.Context,
// 	host host.Host,
// 	ps *pubsub.PubSub,
// ) {

// 	log.Println("🎯 Buddy Node Complete Flow Example")
// 	log.Println("====================================")

// 	// Phase 1: Receive round announcement from Sequencer (via gRPC or direct message)
// 	roundNumber := uint64(1)
// 	blockHash := "0xabc123def456"
// 	myBuddyID := "buddy-5"

// 	log.Printf("\n📨 Phase 1: Received round announcement")
// 	log.Printf("   Round: %d", roundNumber)
// 	log.Printf("   Block: %s", blockHash)

// 	// Phase 2: Collect votes from regular nodes (87 nodes send votes directly to me)
// 	log.Printf("\n📥 Phase 2: Collecting votes from regular nodes")
// 	votes := collectVotesFromRegularNodes() // 87 votes sent directly to this buddy
// 	log.Printf("   Collected %d votes", len(votes))

// 	// Phase 3: Publish votes to GossipSub (so other 12 buddies can collect)
// 	log.Printf("\n📤 Phase 3: Publishing votes to GossipSub")
// 	publishVotesToGossipSub(ctx, ps, roundNumber, votes)
// 	log.Printf("   Published %d votes to topic", len(votes))

// 	// Phase 4: Listen to GossipSub and collect votes from other buddies
// 	log.Printf("\n🎧 Phase 4: Listening to other buddies' votes via GossipSub")
// 	allVotes := listenAndCollectAllVotes(ctx, ps, roundNumber, votes)
// 	log.Printf("   Total votes collected: %d (should be 87)", len(allVotes))

// 	// Phase 5: Decide based on vote count (all 13 buddies should decide same thing)
// 	log.Printf("\n🤔 Phase 5: Making decision based on votes")
// 	myDecision := decideBasedOnVotes(allVotes)
// 	log.Printf("   My decision: %s", myDecision)

// 	// Phase 6: Join BFT GossipSub topic and run consensus
// 	log.Printf("\n🔧 Phase 6: Running BFT consensus via GossipSub")

// 	// Create messenger
// 	messenger, err := bft.CreateBFTMessenger(ctx, host, ps, roundNumber)
// 	if err != nil {
// 		log.Fatalf("Failed to create messenger: %v", err)
// 	}
// 	defer messenger.Stop()

// 	// Prepare all buddy inputs (in production, this comes from buddy selection)
// 	allBuddies := prepareBuddyInputs(myDecision)

// 	// Run BFT
// 	bftEngine := bft.New(bft.DefaultConfig())
// 	result, err := bftEngine.RunConsensus(
// 		ctx,
// 		roundNumber,
// 		blockHash,
// 		myBuddyID,
// 		allBuddies,
// 		messenger,
// 	)

// 	if err != nil {
// 		log.Fatalf("BFT failed: %v", err)
// 	}

// 	// Phase 7: Send result back to Sequencer
// 	log.Printf("\n📨 Phase 7: Sending result to Sequencer")
// 	sendResultToSequencer(result)

// 	log.Printf("\n✨ Complete flow finished!")
// }

// // ===========================================================================
// // Helper Functions (Mock implementations for example)
// // ===========================================================================

// type Vote struct {
// 	NodeID   string
// 	Decision string
// }

// func collectVotesFromRegularNodes() []Vote {
// 	// In production: Listen for direct gRPC/HTTP calls from 87 nodes
// 	votes := make([]Vote, 87)
// 	for i := 0; i < 87; i++ {
// 		votes[i] = Vote{
// 			NodeID:   fmt.Sprintf("node-%d", i+1),
// 			Decision: "ACCEPT",
// 		}
// 	}
// 	return votes
// }

// func publishVotesToGossipSub(ctx context.Context, ps *pubsub.PubSub, round uint64, votes []Vote) {
// 	// Join vote collection topic
// 	topicName := fmt.Sprintf("votes-round-%d", round)
// 	topic, _ := ps.Join(topicName)
// 	defer topic.Close()

// 	// Publish each vote
// 	for _, vote := range votes {
// 		// Serialize and publish
// 		// topic.Publish(ctx, data)
// 		_ = vote // Placeholder
// 	}
// }

// func listenAndCollectAllVotes(ctx context.Context, ps *pubsub.PubSub, round uint64, myVotes []Vote) []Vote {
// 	// In production: Subscribe to vote topic and collect from all 13 buddies
// 	// Use CRDT to merge all votes
// 	// Wait until all 13 buddies have published their votes

// 	// Mock: return same votes
// 	return myVotes
// }

// func decideBasedOnVotes(votes []Vote) bft.Decision {
// 	acceptCount := 0
// 	for _, vote := range votes {
// 		if vote.Decision == "ACCEPT" {
// 			acceptCount++
// 		}
// 	}

// 	if acceptCount > len(votes)/2 {
// 		return bft.Accept
// 	}
// 	return bft.Reject
// }

// func prepareBuddyInputs(myDecision bft.Decision) []bft.BuddyInput {
// 	// In production: Get from buddy selection
// 	// All 13 buddies with their keys and decisions
// 	buddies := make([]bft.BuddyInput, 13)
// 	for i := 0; i < 13; i++ {
// 		buddies[i] = bft.BuddyInput{
// 			ID:       fmt.Sprintf("buddy-%d", i),
// 			Decision: myDecision, // All should have same decision
// 		}
// 	}
// 	return buddies
// }

//	func sendResultToSequencer(result *bft.Result) {
//		// In production: Send gRPC call to Sequencer with result
//		log.Printf("Sending to Sequencer: Decision=%s, Accepted=%v", result.Decision, result.BlockAccepted)
//	}
package bft
