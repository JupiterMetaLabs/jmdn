package Structs

import (
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	Publisher "gossipnode/Pubsub/Publish"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type UtilsBuddyNode struct {
	BuddyNode *PubSubMessages.BuddyNode
}

// GetBuddyNodes returns a copy of the current buddy nodes list
func (buddy *UtilsBuddyNode) GetBuddyNodes() []peer.ID {
	buddy.BuddyNode.Mutex.RLock()
	defer buddy.BuddyNode.Mutex.RUnlock()

	nodes := make([]peer.ID, len(buddy.BuddyNode.BuddyNodes.Buddies_Nodes))
	copy(nodes, buddy.BuddyNode.BuddyNodes.Buddies_Nodes)
	return nodes
}

// GetBuddyNodesCount returns the number of buddy nodes (excluding self)
func (buddy *UtilsBuddyNode) GetBuddyNodesCount() int {
	buddy.BuddyNode.Mutex.RLock()
	defer buddy.BuddyNode.Mutex.RUnlock()

	count := 0
	for _, peerID := range buddy.BuddyNode.BuddyNodes.Buddies_Nodes {
		if peerID != buddy.BuddyNode.PeerID {
			count++
		}
	}
	return count
}

// GetMetadata returns a copy of the current metadata
func (buddy *UtilsBuddyNode) GetMetadata() PubSubMessages.MetaData {
	buddy.BuddyNode.Mutex.RLock()
	defer buddy.BuddyNode.Mutex.RUnlock()
	return PubSubMessages.MetaData{
		Received:  buddy.BuddyNode.MetaData.Received,
		Sent:      buddy.BuddyNode.MetaData.Sent,
		Total:     buddy.BuddyNode.MetaData.Total,
		UpdatedAt: buddy.BuddyNode.MetaData.UpdatedAt,
	}
}

func SubmitMessage(msg *PubSubMessages.Message, PubSub *PubSubMessages.GossipPubSub, ListenerNode *PubSubMessages.BuddyNode) error {
	// Check if this is a vote message
	var voteData map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Message), &voteData); err != nil {
		return fmt.Errorf("failed to unmarshal vote message: %v", err)
	}

	// Check if this is a vote message by looking for vote field
	if _, exists := voteData["vote"]; exists {

		// Create OP struct for vote
		OP := &Types.OP{
			NodeID: msg.Sender,
			OpType: int8(1), // 1 for add, -1 for remove
			KeyValue: Types.KeyValue{
				Key:   msg.Sender.String(), // key would be the peer id of the sender
				Value: msg.Message, // Store the full vote message as value
			},
		}

		// Adding data to the CRDT First - Before PubSub
		if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
			return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
		}
	} else {
		// This is a regular message, try to unmarshal as OP
		OP := &Types.OP{}
		if err := json.Unmarshal([]byte(msg.Message), OP); err != nil {
			return fmt.Errorf("failed to unmarshal message: %v", err)
		}

		// Adding data to the CRDT First - Before PubSub
		if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
			return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
		}
	}

	// Now Submit to the publish function in the pubsub using config.PubSub_ConsensusChannel
	if err := Publisher.Publish(PubSub, config.PubSub_ConsensusChannel, msg, map[string]string{}); err != nil {
		return fmt.Errorf("failed to publish message to pubsub: %v", err)
	}
	return nil
}

// PrintCRDTState prints the current CRDT state for a buddy node
func PrintCRDTState(listenerNode *PubSubMessages.BuddyNode) error {
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		return fmt.Errorf("listener node or CRDT layer not initialized")
	}

	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║             CRDT STATE - BUDDY NODE                      ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("Peer ID: %s\n", listenerNode.PeerID.String())
	fmt.Printf("Timestamp: %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("Messages Received: %d | Sent: %d | Total: %d\n",
		listenerNode.MetaData.Received,
		listenerNode.MetaData.Sent,
		listenerNode.MetaData.Total)

	// Get all votes from CRDT
	fmt.Printf("DEBUG: About to call GetSet with key='vote'\n")
	votes, exists := DataLayer.GetSet(listenerNode.CRDTLayer, "vote")
	fmt.Printf("DEBUG: GetSet returned exists=%t, votes=%v\n", exists, votes)

	if !exists || len(votes) == 0 {
		fmt.Printf("\n📊 Votes in CRDT: 0 (no votes collected yet)\n")
	} else {
		fmt.Printf("\n📊 Total Votes in CRDT: %d\n", len(votes))
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		for i, vote := range votes {
			// Parse the vote JSON
			var voteData map[string]interface{}
			if err := json.Unmarshal([]byte(vote), &voteData); err != nil {
				fmt.Printf("  Vote %d: [PARSING ERROR] %s\n", i+1, vote)
				continue
			}

			// Extract vote details
			voteValue := voteData["vote"]
			blockHash := voteData["block_hash"]

			fmt.Printf("  ✓ Vote %d:\n", i+1)
			fmt.Printf("    - Value: %v\n", voteValue)
			fmt.Printf("    - Block Hash: %v\n", blockHash)
			if i < len(votes)-1 {
				fmt.Printf("    ─────────────────────────────────────────────\n")
			}
		}
	}

	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	return nil
}
