// package Sequencer

// import (
// 	"context"
// 	"gossipnode/AVC/BFT/bft"
// 	"log"
// )

// // TriggerConsensus is called by sequencer to start consensus
// // Assumes: PubSub already running, nodes already subscribed
// func TriggerConsensus(
// 	ctx context.Context,
// 	adapter *bft.BFTPubSubAdapter,
// 	round uint64,
// 	blockHash string,
// 	myBuddyID string,
// 	buddies []bft.BuddyInput,
// ) (*bft.Result, error) {

// 	log.Printf("🎯 Triggering consensus for round %d, block %s", round, blockHash)

// 	// Just call the adapter - it uses existing PubSub infrastructure
// 	result, err := adapter.ProposeConsensus(
// 		ctx,
// 		round,
// 		blockHash,
// 		myBuddyID,
// 		buddies,
// 	)

// 	if err != nil {
// 		log.Printf("❌ Consensus failed: %v", err)
// 		return nil, err
// 	}

// 	log.Printf("✅ Consensus completed: %s (accepted: %v)",
// 		result.Decision, result.BlockAccepted)

// 	return result, nil
// }
