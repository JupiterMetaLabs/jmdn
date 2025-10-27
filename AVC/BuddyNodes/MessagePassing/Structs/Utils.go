package Structs

import (
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	Publisher "gossipnode/Pubsub/Publish"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"

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
	if vote, exists := voteData["vote"]; exists {
		// This is a vote message, create proper OP struct
		voteValue, ok := vote.(float64)
		if !ok {
			return fmt.Errorf("invalid vote value type")
		}

		// Create OP struct for vote
		OP := &Types.OP{
			NodeID: msg.Sender,
			OpType: int8(voteValue), // vote = 1 (ADD) or -1 (REMOVE)
			KeyValue: Types.KeyValue{
				Key:   "vote",
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

	// Now Submit to the publish function in the pubsub
	if err := Publisher.Publish(PubSub, config.PubSub_ConsensusChannel, msg, map[string]string{}); err != nil {
		return fmt.Errorf("failed to publish message to pubsub: %v", err)
	}
	return nil
}
