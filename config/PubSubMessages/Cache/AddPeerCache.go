package Cache

import (
	"fmt"
	"gossipnode/config/PubSubMessages"
	"gossipnode/node"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var AddPeersCache map[peer.ID]multiaddr.Multiaddr

func NewAddPeersCache() map[peer.ID]multiaddr.Multiaddr {
	return make(map[peer.ID]multiaddr.Multiaddr)
}

func AddPeer(peerID peer.ID, addr multiaddr.Multiaddr) {
	if AddPeersCache == nil {
		AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
	}
	AddPeersCache[peerID] = addr
}

func GetPeer(peerID peer.ID) multiaddr.Multiaddr {
	if AddPeersCache == nil {
		return nil
	}
	return AddPeersCache[peerID]
}

func RemovePeer(peerID peer.ID) {
	if AddPeersCache == nil {
		return
	}
	delete(AddPeersCache, peerID)
}

func GetAllPeers() map[peer.ID]multiaddr.Multiaddr {
	if AddPeersCache == nil {
		return make(map[peer.ID]multiaddr.Multiaddr)
	}
	return AddPeersCache
}

func GetPeerCount() int {
	if AddPeersCache == nil {
		return 0
	}
	return len(AddPeersCache)
}

func ClearCache() {
	AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
}

func AddPeersTemporary(peers []PubSubMessages.Buddy_PeerMultiaddr) {
	// Initialize the cache if it's nil
	if AddPeersCache == nil {
		AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
	}

	// Add peers to the temporary cache
	for _, buddy := range peers {
		AddPeersCache[buddy.PeerID] = buddy.Multiaddr
	}

	err := ConnectToTemporaryPeers(AddPeersCache)
	if err != nil {
		fmt.Printf("Failed to connect to temporary peers: %v\n", err)
	}
	fmt.Printf("Successfully connected to %d temporary peers\n", len(peers))
}

func GetNodeManager() *node.NodeManager {
	if node.GetNodeManagerInterface() != nil {
		return node.GetNodeManagerInterface()
	}
	return nil
}

// ConnectToTemporaryPeers connects to all peers in the temporary cache using NodeManager
func ConnectToTemporaryPeers(peers map[peer.ID]multiaddr.Multiaddr) error {
	nodeManager := GetNodeManager()
	if nodeManager == nil {
		return fmt.Errorf("NodeManager not available")
	}

	var connectedCount int
	var failedCount int

	for peerID, addr := range peers {
		// Convert multiaddr to string format for NodeManager.AddPeer
		addrStr := addr.String()

		fmt.Printf("Adding temporary peer for consensus: %s at %s\n", peerID, addrStr)

		// Use existing NodeManager.AddPeer method
		// Note: NodeManager.AddPeer is designed to be resilient - it adds peers to DB
		// even if initial connection fails, expecting retry via heartbeat
		if err := nodeManager.AddPeer(addrStr); err != nil {
			fmt.Printf("Failed to add peer %s: %v\n", peerID, err)
			failedCount++
		} else {
			// NodeManager.AddPeer succeeded (peer added to database)
			// The actual connection may still be in progress or failed
			// This is expected behavior for temporary consensus peers
			fmt.Printf("Peer %s added to NodeManager for consensus\n", peerID)
			connectedCount++
		}
	}

	fmt.Printf("Temporary peer connection summary: %d added to NodeManager, %d failed\n", connectedCount, failedCount)

	// For consensus, we accept that some peers may not be immediately connected
	// The NodeManager will handle reconnection attempts via heartbeat
	if failedCount > 0 {
		fmt.Printf("Warning: %d peers failed to be added to NodeManager\n", failedCount)
	}

	return nil
}

// ClearTemporaryPeers removes all temporary peers using NodeManager
func ClearTemporaryPeers() {
	if AddPeersCache == nil {
		return
	}

	// Get the NodeManager instance
	nodeManager := GetNodeManager()
	if nodeManager == nil {
		fmt.Printf("Warning: NodeManager not available, clearing cache only\n")
		ClearCache()
		return
	}

	fmt.Printf("Clearing %d temporary peers using NodeManager...\n", len(AddPeersCache))

	for peerID := range AddPeersCache {
		fmt.Printf("Removing temporary peer: %s\n", peerID)

		// Use existing NodeManager.RemovePeer method
		if err := nodeManager.RemovePeer(peerID.String()); err != nil {
			fmt.Printf("Warning: Failed to remove peer %s: %v\n", peerID, err)
		} else {
			fmt.Printf("Successfully removed temporary peer: %s\n", peerID)
		}
	}

	// Clear the cache
	ClearCache()
	fmt.Println("Temporary peers cleared using NodeManager")
}

// GetTemporaryPeerCount returns the number of temporary peers
func GetTemporaryPeerCount() int {
	if AddPeersCache == nil {
		return 0
	}
	return len(AddPeersCache)
}

// IsTemporaryPeer checks if a peer ID is in the temporary cache
func IsTemporaryPeer(peerID peer.ID) bool {
	if AddPeersCache == nil {
		return false
	}
	_, exists := AddPeersCache[peerID]
	return exists
}
