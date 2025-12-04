package Service

import (
	AppContext "gossipnode/config/Context"
	"context"
	"fmt"
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/Types"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

const (
	NodeDiscoveryServiceAppContext = "avc.node.discovery.service"
)

// NodeDiscoveryService handles peer discovery and CRDT synchronization
type NodeDiscoveryService struct {
	buddyNode     *PubSubMessages.BuddyNode
	knownPeers    map[peer.ID]*PeerInfo
	peerMutex     sync.RWMutex
	discoveryChan chan peer.ID
	stopChan      chan struct{}
}

// PeerInfo contains information about a discovered peer
type PeerInfo struct {
	PeerID      peer.ID
	LastSeen    time.Time
	IsConnected bool
	CRDTState   *Types.Controller
}

// NewNodeDiscoveryService creates a new node discovery service
func NewNodeDiscoveryService(buddyNode *PubSubMessages.BuddyNode) *NodeDiscoveryService {
	return &NodeDiscoveryService{
		buddyNode:     buddyNode,
		knownPeers:    make(map[peer.ID]*PeerInfo),
		discoveryChan: make(chan peer.ID, 100),
		stopChan:      make(chan struct{}),
	}
}

// StartDiscovery starts the peer discovery process
func (nds *NodeDiscoveryService) StartDiscovery() {
	log.LogConsensusInfo("Starting node discovery service",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "NodeDiscoveryService.StartDiscovery"))

	go nds.discoveryLoop()
	go nds.syncLoop()
}

// StopDiscovery stops the peer discovery process
func (nds *NodeDiscoveryService) StopDiscovery() {
	close(nds.stopChan)
	log.LogConsensusInfo("Stopped node discovery service",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "NodeDiscoveryService.StopDiscovery"))
}

// AddPeer adds a new peer to the discovery service
func (nds *NodeDiscoveryService) AddPeer(peerID peer.ID) {
	nds.peerMutex.Lock()
	defer nds.peerMutex.Unlock()

	if _, exists := nds.knownPeers[peerID]; !exists {
		nds.knownPeers[peerID] = &PeerInfo{
			PeerID:      peerID,
			LastSeen:    time.Now().UTC(),
			IsConnected: true,
			CRDTState:   nil, // Will be populated during sync
		}

		log.LogConsensusInfo(fmt.Sprintf("Added new peer to discovery: %s", peerID),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("peer", peerID.String()),
			zap.String("function", "NodeDiscoveryService.AddPeer"))
	}
}

// RemovePeer removes a peer from the discovery service
func (nds *NodeDiscoveryService) RemovePeer(peerID peer.ID) {
	nds.peerMutex.Lock()
	defer nds.peerMutex.Unlock()

	if peerInfo, exists := nds.knownPeers[peerID]; exists {
		peerInfo.IsConnected = false
		log.LogConsensusInfo(fmt.Sprintf("Marked peer as disconnected: %s", peerID),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("peer", peerID.String()),
			zap.String("function", "NodeDiscoveryService.RemovePeer"))
	}
}

// GetConnectedPeers returns a list of currently connected peers
func (nds *NodeDiscoveryService) GetConnectedPeers() []peer.ID {
	nds.peerMutex.RLock()
	defer nds.peerMutex.RUnlock()

	var connectedPeers []peer.ID
	for peerID, peerInfo := range nds.knownPeers {
		if peerInfo.IsConnected && peerID != nds.buddyNode.PeerID {
			connectedPeers = append(connectedPeers, peerID)
		}
	}
	return connectedPeers
}

// GetPeerCount returns the number of known peers
func (nds *NodeDiscoveryService) GetPeerCount() int {
	nds.peerMutex.RLock()
	defer nds.peerMutex.RUnlock()

	count := 0
	for _, peerInfo := range nds.knownPeers {
		if peerInfo.IsConnected {
			count++
		}
	}
	return count
}

// SyncWithPeer performs CRDT synchronization with a specific peer
func (nds *NodeDiscoveryService) SyncWithPeer(ctx context.Context, peerID peer.ID) error {
	nds.peerMutex.RLock()
	peerInfo, exists := nds.knownPeers[peerID]
	nds.peerMutex.RUnlock()

	if !exists || !peerInfo.IsConnected {
		return fmt.Errorf("peer %s not found or not connected", peerID)
	}

	log.LogConsensusInfo(fmt.Sprintf("Starting CRDT sync with peer: %s", peerID),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("peer", peerID.String()),
		zap.String("function", "NodeDiscoveryService.SyncWithPeer"))

	// For now, we'll use a placeholder remote controller
	// In a real implementation, this would fetch the remote peer's CRDT state
	remoteController := &Types.Controller{
		CRDTLayer: nds.buddyNode.CRDTLayer.CRDTLayer, // Placeholder - should be remote peer's state
	}

	// Perform the sync operation using DataLayer
	if err := DataLayer.SyncWithNode(ctx, nds.buddyNode.CRDTLayer, remoteController,
		nds.buddyNode.PeerID.String(), peerID.String()); err != nil {
		return fmt.Errorf("failed to sync with peer %s: %v", peerID, err)
	}

	// Update peer info
	nds.peerMutex.Lock()
	peerInfo.LastSeen = time.Now().UTC()
	nds.peerMutex.Unlock()

	log.LogConsensusInfo(fmt.Sprintf("Successfully synced with peer: %s", peerID),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("peer", peerID.String()),
		zap.String("function", "NodeDiscoveryService.SyncWithPeer"))

	return nil
}

// SyncWithAllPeers performs CRDT synchronization with all connected peers
func (nds *NodeDiscoveryService) SyncWithAllPeers(ctx context.Context) error {
	connectedPeers := nds.GetConnectedPeers()

	if len(connectedPeers) == 0 {
		log.LogConsensusInfo("No connected peers to sync with",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "NodeDiscoveryService.SyncWithAllPeers"))
		return nil
	}

	log.LogConsensusInfo(fmt.Sprintf("Starting sync with %d peers", len(connectedPeers)),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "NodeDiscoveryService.SyncWithAllPeers"))

	var syncErrors []error
	for _, peerID := range connectedPeers {
		if err := nds.SyncWithPeer(ctx, peerID); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("sync with %s failed: %v", peerID, err))
		}
	}

	if len(syncErrors) > 0 {
		return fmt.Errorf("sync completed with %d errors: %v", len(syncErrors), syncErrors)
	}

	log.LogConsensusInfo("Successfully synced with all peers",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "NodeDiscoveryService.SyncWithAllPeers"))

	return nil
}

// discoveryLoop continuously monitors for new peers
func (nds *NodeDiscoveryService) discoveryLoop() {
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-nds.stopChan:
			return
		case <-ticker.C:
			nds.discoverNewPeers()
		}
	}
}

// syncLoop periodically syncs with all peers
func (nds *NodeDiscoveryService) syncLoop() {
	ticker := time.NewTicker(7 * time.Second) // Sync every 2 minutes
	defer ticker.Stop()

	for {
		select {
		case <-nds.stopChan:
			return
		case <-ticker.C:
			ctx, cancel := AppContext.GetAppContext(NodeDiscoveryServiceAppContext).NewChildContextWithTimeout(30*time.Second)
			if err := nds.SyncWithAllPeers(ctx); err != nil {
				log.LogConsensusError(fmt.Sprintf("Periodic sync failed: %v", err), err,
					zap.String("topic", log.Consensus_TOPIC),
					zap.String("function", "NodeDiscoveryService.syncLoop"))
			}
			cancel()
		}
	}
}

// discoverNewPeers discovers new peers from the network
func (nds *NodeDiscoveryService) discoverNewPeers() {
	// Get current connected peers from the host
	connectedPeers := nds.buddyNode.Host.Network().Peers()

	for _, peerID := range connectedPeers {
		if peerID != nds.buddyNode.PeerID {
			nds.AddPeer(peerID)
		}
	}
}

// GetDiscoveryStats returns statistics about the discovery service
func (nds *NodeDiscoveryService) GetDiscoveryStats() map[string]interface{} {
	nds.peerMutex.RLock()
	defer nds.peerMutex.RUnlock()

	stats := map[string]interface{}{
		"total_peers":        len(nds.knownPeers),
		"connected_peers":    0,
		"disconnected_peers": 0,
	}

	for _, peerInfo := range nds.knownPeers {
		if peerInfo.IsConnected {
			stats["connected_peers"] = stats["connected_peers"].(int) + 1
		} else {
			stats["disconnected_peers"] = stats["disconnected_peers"].(int) + 1
		}
	}

	return stats
}
