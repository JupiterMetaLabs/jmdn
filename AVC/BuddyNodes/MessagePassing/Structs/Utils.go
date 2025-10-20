package Structs

import (
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/Pubsub"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/peer"
)

// GetBuddyNodes returns a copy of the current buddy nodes list
func (buddy *BuddyNode) GetBuddyNodes() []peer.ID {
	buddy.Mutex.RLock()
	defer buddy.Mutex.RUnlock()

	nodes := make([]peer.ID, len(buddy.BuddyNodes.Buddies_Nodes))
	copy(nodes, buddy.BuddyNodes.Buddies_Nodes)
	return nodes
}

// GetBuddyNodesCount returns the number of buddy nodes (excluding self)
func (buddy *BuddyNode) GetBuddyNodesCount() int {
	buddy.Mutex.RLock()
	defer buddy.Mutex.RUnlock()

	count := 0
	for _, peerID := range buddy.BuddyNodes.Buddies_Nodes {
		if peerID != buddy.PeerID {
			count++
		}
	}
	return count
}

// GetMetadata returns a copy of the current metadata
func (buddy *BuddyNode) GetMetadata() MetaData {
	buddy.Mutex.RLock()
	defer buddy.Mutex.RUnlock()
	return MetaData{
		Received:  buddy.MetaData.Received,
		Sent:      buddy.MetaData.Sent,
		Total:     buddy.MetaData.Total,
		UpdatedAt: buddy.MetaData.UpdatedAt,
	}
}

func SubmitMessage(msg *Pubsub.Message, PubSub *Pubsub.GossipPubSub, ListenerNode *BuddyNode) error {
	OP := &Types.OP{}
	if err := json.Unmarshal([]byte(msg.Message), OP); err != nil {
		return fmt.Errorf("failed to unmarshal message: %v", err)
	}
	// Adding data to the CRDT First - Before PubSub
	if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
		return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
	}

	// Now Submit to the publish function in the pubsub
	if err := PubSub.Publish(config.PubSub_ConsensusChannel, msg, nil); err != nil {
		return fmt.Errorf("failed to publish message to pubsub: %v", err)
	}
	return nil
}

func SubmitMessageToCRDT(msg string, ListenerNode *BuddyNode) error {
	OP := &Types.OP{}
	if err := json.Unmarshal([]byte(msg), OP); err != nil {
		return fmt.Errorf("failed to unmarshal message: %v", err)
	}
	// Adding data to the CRDT First - Before PubSub
	if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
		return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
	}
	return nil
}


// Implement PubSubHandler interface
func (buddy *BuddyNode) Subscribe(topic string, handler func(*Pubsub.GossipMessage)) error {
	if buddy.PubSub != nil {
		return buddy.PubSub.Subscribe(topic, handler)
	}
	return fmt.Errorf("PubSub not available")
}

func (buddy *BuddyNode) Unsubscribe(topic string) error {
	if buddy.PubSub != nil {
		return buddy.PubSub.Unsubscribe(topic)
	}
	return fmt.Errorf("PubSub not available")
}

// Implement BuddyNodeHandler interface
func (buddy *BuddyNode) GetPeerID() string {
	return buddy.PeerID.String()
}

func (buddy *BuddyNode) GetHostID() string {
	return buddy.Host.ID().String()
}

func (buddy *BuddyNode) SubmitToCRDT(message string) error {
	return SubmitMessageToCRDT(message, buddy)
}

func (buddy *BuddyNode) GetPubSub() *Pubsub.GossipPubSub{
	return buddy.PubSub
}