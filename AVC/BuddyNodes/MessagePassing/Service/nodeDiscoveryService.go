package Service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"jmdn/AVC/BuddyNodes/DataLayer"
	"jmdn/AVC/BuddyNodes/Types"
	"jmdn/AVC/BuddyNodes/common"
	GRO "jmdn/config/GRO"
	PubSubMessages "jmdn/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
)

// NodeDiscoveryService handles peer discovery and CRDT synchronization
type NodeDiscoveryService struct {
	buddyNode              *PubSubMessages.BuddyNode
	knownPeers             map[peer.ID]*PeerInfo
	peerMutex              sync.RWMutex
	discoveryChan          chan peer.ID
	stopChan               chan struct{}
	discoveryLoggerContext discoveryLoggerContext
}

type discoveryLoggerContext struct {
	discovery_logger_ctx    context.Context
	discovery_logger_cancel context.CancelFunc
	discovery_span          ion.Span
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
	// Create base context with cancel function
	nds.discoveryLoggerContext.discovery_logger_ctx, nds.discoveryLoggerContext.discovery_logger_cancel = context.WithCancel(context.Background())

	// Start a trace span for the entire service lifecycle
	// The span will be ended in StopDiscovery() when the service shuts down
	tracer := logger().NamedLogger.Tracer("NodeDiscoveryService")
	nds.discoveryLoggerContext.discovery_logger_ctx, nds.discoveryLoggerContext.discovery_span = tracer.Start(
		nds.discoveryLoggerContext.discovery_logger_ctx,
		"NodeDiscoveryService.StartDiscovery",
	)

	if NodeDiscoveryLocal == nil {
		var err error
		NodeDiscoveryLocal, err = common.InitializeGRO(GRO.NodeDiscoveryLocal)
		if err != nil {
			fmt.Printf("❌ Failed to initialize NodeDiscovery local manager: %v\n", err)
			// End span if initialization fails
			if nds.discoveryLoggerContext.discovery_span != nil {
				nds.discoveryLoggerContext.discovery_span.End()
			}
			return
		}
	}
	logger().NamedLogger.Info(nds.discoveryLoggerContext.discovery_logger_ctx, "Starting node discovery service",
		ion.String("topic", "NodeDiscoveryService"),
		ion.String("description", "Starting node discovery service"),
		ion.String("function", "NodeDiscoveryService.StartDiscovery"))

	NodeDiscoveryLocal.Go(GRO.NodeDiscoveryDiscoveryLoopThread, func(ctx context.Context) error {
		nds.discoveryLoop()
		return nil
	})
	NodeDiscoveryLocal.Go(GRO.NodeDiscoverySyncLoopThread, func(ctx context.Context) error {
		nds.syncLoop()
		return nil
	})
}

// StopDiscovery stops the peer discovery process
func (nds *NodeDiscoveryService) StopDiscovery() {
	// End the trace span before cancelling context
	if nds.discoveryLoggerContext.discovery_span != nil {
		nds.discoveryLoggerContext.discovery_span.End()
	}
	defer nds.discoveryLoggerContext.discovery_logger_cancel()
	close(nds.stopChan)
	logger().NamedLogger.Info(nds.discoveryLoggerContext.discovery_logger_ctx, "Stopped node discovery service",
		ion.String("topic", "NodeDiscoveryService"),
		ion.String("description", "Stopped node discovery service"),
		ion.String("function", "NodeDiscoveryService.StopDiscovery"))
}

// AddPeer adds a new peer to the discovery service
func (nds *NodeDiscoveryService) AddPeer(logger_ctx context.Context, peerID peer.ID) {
	nds.peerMutex.Lock()
	defer nds.peerMutex.Unlock()

	if _, exists := nds.knownPeers[peerID]; !exists {
		nds.knownPeers[peerID] = &PeerInfo{
			PeerID:      peerID,
			LastSeen:    time.Now().UTC(),
			IsConnected: true,
			CRDTState:   nil, // Will be populated during sync
		}

		logger().NamedLogger.Info(logger_ctx, "Added new peer to discovery",
			ion.String("topic", "NodeDiscoveryService"),
			ion.String("peer", peerID.String()),
			ion.String("function", "NodeDiscoveryService.AddPeer"))
	}
}

// RemovePeer removes a peer from the discovery service
func (nds *NodeDiscoveryService) RemovePeer(logger_ctx context.Context, peerID peer.ID) {
	nds.peerMutex.Lock()
	defer nds.peerMutex.Unlock()

	if peerInfo, exists := nds.knownPeers[peerID]; exists {
		peerInfo.IsConnected = false
		logger().NamedLogger.Info(logger_ctx, "Marked peer as disconnected",
			ion.String("topic", "NodeDiscoveryService"),
			ion.String("peer", peerID.String()),
			ion.String("function", "NodeDiscoveryService.RemovePeer"))
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
func (nds *NodeDiscoveryService) SyncWithPeer(logger_ctx context.Context, ctx context.Context, peerID peer.ID) error {
	nds.peerMutex.RLock()
	peerInfo, exists := nds.knownPeers[peerID]
	nds.peerMutex.RUnlock()

	if !exists || !peerInfo.IsConnected {
		return fmt.Errorf("peer %s not found or not connected", peerID)
	}

	logger().NamedLogger.Info(logger_ctx, "Starting CRDT sync with peer",
		ion.String("topic", "NodeDiscoveryService"),
		ion.String("peer", peerID.String()),
		ion.String("function", "NodeDiscoveryService.SyncWithPeer"))

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

	logger().NamedLogger.Info(logger_ctx, "Successfully synced with peer",
		ion.String("topic", "NodeDiscoveryService"),
		ion.String("peer", peerID.String()),
		ion.String("function", "NodeDiscoveryService.SyncWithPeer"))

	return nil
}

// SyncWithAllPeers performs CRDT synchronization with all connected peers
func (nds *NodeDiscoveryService) SyncWithAllPeers(logger_ctx context.Context, ctx context.Context) error {
	connectedPeers := nds.GetConnectedPeers()

	if len(connectedPeers) == 0 {
		logger().NamedLogger.Info(logger_ctx, "No connected peers to sync with",
			ion.String("topic", "NodeDiscoveryService"),
			ion.String("function", "NodeDiscoveryService.SyncWithAllPeers"))
		return nil
	}

	logger().NamedLogger.Info(logger_ctx, "Starting sync with peers",
		ion.Int("peers_count", len(connectedPeers)),
		ion.String("topic", "NodeDiscoveryService"),
		ion.String("function", "NodeDiscoveryService.SyncWithAllPeers"))

	var syncErrors []error
	for _, peerID := range connectedPeers {
		if err := nds.SyncWithPeer(logger_ctx, ctx, peerID); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("sync with %s failed: %v", peerID, err))
		}
	}

	if len(syncErrors) > 0 {
		return fmt.Errorf("sync completed with %d errors: %v", len(syncErrors), syncErrors)
	}

	logger().NamedLogger.Info(logger_ctx, "Successfully synced with all peers",
		ion.String("topic", "NodeDiscoveryService"),
		ion.String("function", "NodeDiscoveryService.SyncWithAllPeers"))

	return nil
}

// discoveryLoop continuously monitors for new peers
func (nds *NodeDiscoveryService) discoveryLoop() {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	link := ion.LinkFromContext(nds.discoveryLoggerContext.discovery_logger_ctx)

	//start the trace as the child trace of the parent trace
	tracer := logger().NamedLogger.Tracer("NodeDiscoveryService")
	logger_ctx, span := tracer.Start(nds.discoveryLoggerContext.discovery_logger_ctx,
		"NodeDiscoveryService.StartDiscovery.discoveryLoop",
		ion.WithLinks(link))
	defer span.End()

	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-nds.stopChan:
			logger().NamedLogger.Info(logger_ctx, "Node discovery service stopped",
				ion.String("topic", "NodeDiscoveryService"),
				ion.String("description", "Node discovery service stopped"),
				ion.String("function", "NodeDiscoveryService.discoveryLoop"))
			return
		case <-ticker.C:
			nds.discoverNewPeers(logger_ctx)
		}
	}
}

// syncLoop periodically syncs with all peers
func (nds *NodeDiscoveryService) syncLoop() {
	ticker := time.NewTicker(7 * time.Second) // Sync every 2 minutes
	defer ticker.Stop()

	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	link := ion.LinkFromContext(nds.discoveryLoggerContext.discovery_logger_ctx)

	//start the trace as the child trace of the parent trace
	tracer := logger().NamedLogger.Tracer("NodeDiscoveryService")
	logger_ctx, span := tracer.Start(nds.discoveryLoggerContext.discovery_logger_ctx,
		"NodeDiscoveryService.StartDiscovery.syncLoop",
		ion.WithLinks(link))
	defer span.End()

	for {
		select {
		case <-nds.stopChan:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := nds.SyncWithAllPeers(logger_ctx, ctx); err != nil {
				logger().NamedLogger.Error(logger_ctx, "Periodic sync failed",
					err,
					ion.String("topic", "NodeDiscoveryService"),
					ion.String("function", "NodeDiscoveryService.syncLoop"))
			}
			cancel()
		}
	}
}

// discoverNewPeers discovers new peers from the network
func (nds *NodeDiscoveryService) discoverNewPeers(logger_ctx context.Context) {
	// Get current connected peers from the host
	connectedPeers := nds.buddyNode.Host.Network().Peers()

	for _, peerID := range connectedPeers {
		if peerID != nds.buddyNode.PeerID {
			nds.AddPeer(logger_ctx, peerID)
			logger().NamedLogger.Info(logger_ctx, "Added new peer to discovery",
				ion.String("topic", "NodeDiscoveryService"),
				ion.String("peer", peerID.String()),
				ion.String("function", "NodeDiscoveryService.discoverNewPeers"))
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
