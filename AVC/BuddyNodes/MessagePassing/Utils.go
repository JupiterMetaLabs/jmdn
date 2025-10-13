package MessagePassing

import (
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
