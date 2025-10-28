package Structs

import (
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	voteaggregation "gossipnode/AVC/VoteModule"
	Publisher "gossipnode/Pubsub/Publish"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/seednode"
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
				Value: msg.Message,         // Store the full vote message as value
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

	// Get all votes from CRDT by querying with peer IDs as keys
	// We need to query all known peers to get their votes
	fmt.Printf("\n=== Collecting votes from CRDT ===\n")

	// Get all connected peers or buddy nodes
	var allPeers []peer.ID
	if listenerNode.Host != nil {
		allPeers = listenerNode.Host.Network().Peers()
	} else {
		// Try to get peers from buddy nodes
		if listenerNode.BuddyNodes.Buddies_Nodes != nil {
			allPeers = listenerNode.BuddyNodes.Buddies_Nodes
		}
	}

	fmt.Printf("DEBUG: Querying votes for %d peers\n", len(allPeers))

	allVotes := make([]string, 0)
	for _, peerID := range allPeers {
		votes, exists := DataLayer.GetSet(listenerNode.CRDTLayer, peerID.String())
		fmt.Printf("DEBUG: Peer %s - exists=%t, votes=%v\n", peerID, exists, votes)
		if exists && len(votes) > 0 {
			allVotes = append(allVotes, votes...)
		}
	}

	// Also query for votes stored with "vote" key (legacy)
	if legacyVotes, exists := DataLayer.GetSet(listenerNode.CRDTLayer, "vote"); exists && len(legacyVotes) > 0 {
		fmt.Printf("DEBUG: Found %d legacy votes with key='vote'\n", len(legacyVotes))
		allVotes = append(allVotes, legacyVotes...)
	}

	fmt.Printf("DEBUG: Total unique votes collected: %d\n", len(allVotes))

	if len(allVotes) == 0 {
		fmt.Printf("\n📊 Votes in CRDT: 0 (no votes collected yet)\n")
	} else {
		fmt.Printf("\n📊 Total Votes in CRDT: %d\n", len(allVotes))
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		for i, vote := range allVotes {
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
			if i < len(allVotes)-1 {
				fmt.Printf("    ─────────────────────────────────────────────\n")
			}
		}
	}

	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	return nil
}

// ProcessVotesFromCRDT extracts votes from CRDT, processes them through votemodule, and returns the result
func ProcessVotesFromCRDT(listenerNode *PubSubMessages.BuddyNode) (int8, error) {
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		return 0, fmt.Errorf("listener node or CRDT layer not initialized")
	}

	fmt.Printf("\n=== Processing votes from CRDT for voting ===\n")

	// Get all CRDTs to find all keys that might contain votes
	allCRDTs := listenerNode.CRDTLayer.CRDTLayer.GetAllCRDTs()
	fmt.Printf("DEBUG: Found %d CRDT keys in storage\n", len(allCRDTs))

	// Map to store peer_id -> vote value
	voteData := make(map[string]int8)

	// Iterate through all CRDT keys
	for key := range allCRDTs {
		votes, exists := DataLayer.GetSet(listenerNode.CRDTLayer, key)
		fmt.Printf("DEBUG: Key '%s' - exists=%t, votes=%v\n", key, exists, votes)

		if !exists || len(votes) == 0 {
			continue
		}

		// Parse each vote and extract vote value
		for _, voteStr := range votes {
			var voteDataObj map[string]interface{}
			if err := json.Unmarshal([]byte(voteStr), &voteDataObj); err != nil {
				fmt.Printf("DEBUG: Failed to parse vote: %s\n", voteStr)
				continue
			}

			// Check if this is a vote message
			voteValueRaw, isVote := voteDataObj["vote"]
			if !isVote {
				continue
			}

			voteValue, ok := voteValueRaw.(float64)
			if !ok {
				fmt.Printf("DEBUG: Invalid vote value type: %v\n", voteValueRaw)
				continue
			}

			// Use the key (which is the peer ID) to store the vote
			voteData[key] = int8(voteValue)
			fmt.Printf("DEBUG: Added vote for peer %s: %d\n", key, int8(voteValue))
		}
	}

	fmt.Printf("DEBUG: Extracted %d votes from CRDT (unique peers)\n", len(voteData))
	fmt.Printf("DEBUG: Vote data map: %v\n", voteData)

	if len(voteData) == 0 {
		fmt.Printf("⚠️ No votes found in CRDT to process\n")
		return 0, fmt.Errorf("no votes found in CRDT")
	}

	// Get peer weights from seed node
	client, err := seednode.NewClient(config.SeedNodeURL)
	if err != nil {
		fmt.Printf("❌ Failed to create seed node client: %v\n", err)
		return 0, fmt.Errorf("failed to create seed node client: %v", err)
	}

	weights, err := client.ListWeightsofPeers()
	if err != nil {
		fmt.Printf("❌ Failed to get peer weights: %v\n", err)
		return 0, fmt.Errorf("failed to get peer weights: %v", err)
	}

	fmt.Printf("DEBUG: Peer weights: %v\n", weights)

	// Filter weights to only include peers that voted
	filteredWeights := make(map[string]float64)
	filteredVoteData := make(map[string]int8)
	for peerID, vote := range voteData {
		if weight, exists := weights[peerID]; exists {
			filteredVoteData[peerID] = vote
			filteredWeights[peerID] = weight
			fmt.Printf("DEBUG: Peer %s has weight %f and vote %d\n", peerID, weight, vote)
		} else {
			fmt.Printf("DEBUG: Peer %s not found in weights, skipping\n", peerID)
		}
	}

	if len(filteredVoteData) == 0 {
		fmt.Printf("⚠️ No votes found after filtering by weights\n")
		return 0, fmt.Errorf("no votes found after filtering by weights")
	}

	fmt.Printf("DEBUG: Filtered vote data (%d peers with votes and weights): %v\n", len(filteredVoteData), filteredVoteData)
	fmt.Printf("DEBUG: Filtered weights map (%d peers): %v\n", len(filteredWeights), filteredWeights)

	// Call votemodule.VoteAggregation with filtered maps
	result, err := voteaggregation.VoteAggregation(filteredWeights, filteredVoteData)
	if err != nil {
		fmt.Printf("❌ Failed to aggregate votes: %v\n", err)
		return 0, fmt.Errorf("failed to aggregate votes: %v", err)
	}

	fmt.Printf("✅ Vote aggregation result: %v\n", result)

	// Convert boolean result to int8
	if result {
		return 1, nil
	} else {
		return -1, nil
	}
}
