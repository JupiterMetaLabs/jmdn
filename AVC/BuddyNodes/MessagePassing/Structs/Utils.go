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

// ProcessVotesFromCRDT extracts votes from CRDT and returns them as map[string]int8 (block_hash -> vote)
func ProcessVotesFromCRDT(listenerNode *PubSubMessages.BuddyNode) (map[string]int8, error) {
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		return nil, fmt.Errorf("listener node or CRDT layer not initialized")
	}

	fmt.Printf("\n=== Processing votes from CRDT for voting ===\n")

	// Get all connected peers or buddy nodes
	var allPeers []peer.ID
	if listenerNode.Host != nil {
		allPeers = listenerNode.Host.Network().Peers()
	} else if listenerNode.BuddyNodes.Buddies_Nodes != nil {
		allPeers = listenerNode.BuddyNodes.Buddies_Nodes
	}

	fmt.Printf("DEBUG: Querying votes for %d peers\n", len(allPeers))

	// Map to store block_hash -> vote value
	voteData := make(map[string]int8)

	for _, peerID := range allPeers {
		votes, exists := DataLayer.GetSet(listenerNode.CRDTLayer, peerID.String())
		fmt.Printf("DEBUG: Peer %s - exists=%t, votes=%v\n", peerID, exists, votes)

		if !exists || len(votes) == 0 {
			continue
		}

		// Parse each vote and extract block_hash and vote value
		for _, voteStr := range votes {
			var voteDataObj map[string]interface{}
			if err := json.Unmarshal([]byte(voteStr), &voteDataObj); err != nil {
				fmt.Printf("DEBUG: Failed to parse vote: %s\n", voteStr)
				continue
			}

			blockHash, ok1 := voteDataObj["block_hash"].(string)
			voteValue, ok2 := voteDataObj["vote"].(float64)

			if !ok1 || !ok2 {
				continue
			}

			// Store block_hash -> vote, latest vote wins if multiple exist
			voteData[blockHash] = int8(voteValue)
			fmt.Printf("DEBUG: Added vote for block %s: %d\n", blockHash, int8(voteValue))
		}
	}

	fmt.Printf("DEBUG: Extracted %d votes from CRDT (unique block hashes)\n", len(voteData))
	fmt.Printf("DEBUG: Vote data map: %v\n", voteData)

	return voteData, nil
}
