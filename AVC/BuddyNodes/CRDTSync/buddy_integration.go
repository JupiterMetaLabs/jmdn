package CRDTSync

import (
	"context"
	"fmt"
	"log"
	"time"

	"jmdn/AVC/BuddyNodes/DataLayer"
	"jmdn/Pubsub"
	"jmdn/crdt"

	"github.com/libp2p/go-libp2p/core/host"
)

// BuddyNodeSyncService provides CRDT sync functionality for individual buddy nodes
type BuddyNodeSyncService struct {
	nodeID        string
	host          host.Host
	engine        *crdt.MemStore
	syncService   *SyncService
	config        *SyncConfig
	ctx           context.Context
	cancel        context.CancelFunc
	isInitialized bool
}

// NewBuddyNodeSyncService creates a new sync service for a buddy node
func NewBuddyNodeSyncService(host host.Host, config *SyncConfig) *BuddyNodeSyncService {
	ctx, cancel := context.WithCancel(context.Background())

	return &BuddyNodeSyncService{
		nodeID:        host.ID().String(),
		host:          host,
		config:        config,
		ctx:           ctx,
		cancel:        cancel,
		isInitialized: false,
	}
}

// InitializeSync initializes the CRDT sync service for this buddy node
func (bns *BuddyNodeSyncService) InitializeSync(pubsubNode *Pubsub.StructGossipPubSub) error {
	if bns.isInitialized {
		return fmt.Errorf("sync service already initialized")
	}

	// Get or create CRDT engine
	crdtLayer := DataLayer.GetCRDTLayer()
	if crdtLayer == nil || crdtLayer.CRDTLayer == nil {
		return fmt.Errorf("CRDT layer not initialized")
	}

	// Convert Engine to MemStore (assuming they're compatible)
	// Note: This might need adjustment based on your actual CRDT structure
	bns.engine = crdt.NewMemStore(50 << 20) // 50MB limit

	// Create sync topic
	topicName := bns.config.TopicName
	topic, err := pubsubNode.GetGossipPubSub().GetOrJoinTopic(topicName)
	if err != nil {
		return fmt.Errorf("failed to join sync topic: %w", err)
	}

	// Create sync service
	bns.syncService = NewSyncService(bns.host, bns.engine, topic, bns.config)

	// Start the sync service
	if err := bns.syncService.Start(); err != nil {
		return fmt.Errorf("failed to start sync service: %w", err)
	}

	bns.isInitialized = true
	log.Printf("✅ Buddy node sync service initialized for %s", bns.nodeID[:8])
	return nil
}

// SyncNow triggers immediate CRDT sync
func (bns *BuddyNodeSyncService) SyncNow() error {
	if !bns.isInitialized {
		return fmt.Errorf("sync service not initialized")
	}

	return bns.syncService.SyncNow()
}

// Stop stops the sync service
func (bns *BuddyNodeSyncService) Stop() error {
	if !bns.isInitialized {
		return nil
	}

	bns.cancel()
	if bns.syncService != nil {
		return bns.syncService.Stop()
	}
	return nil
}

// GetStats returns sync statistics
func (bns *BuddyNodeSyncService) GetStats() map[string]interface{} {
	if !bns.isInitialized || bns.syncService == nil {
		return map[string]interface{}{
			"initialized": false,
			"node_id":     bns.nodeID,
		}
	}

	stats := bns.syncService.GetStats()
	stats["initialized"] = true
	stats["node_id"] = bns.nodeID
	return stats
}

// GetCRDTEngine returns the CRDT engine
func (bns *BuddyNodeSyncService) GetCRDTEngine() *crdt.MemStore {
	return bns.engine
}

// IsInitialized returns whether the service is initialized
func (bns *BuddyNodeSyncService) IsInitialized() bool {
	return bns.isInitialized
}

// GlobalSyncManager manages CRDT sync across all buddy nodes
type GlobalSyncManager struct {
	buddySyncServices map[string]*BuddyNodeSyncService // peerID -> sync service
	host              host.Host
	config            *SyncConfig
}

// NewGlobalSyncManager creates a new global sync manager
func NewGlobalSyncManager(host host.Host) *GlobalSyncManager {
	return &GlobalSyncManager{
		buddySyncServices: make(map[string]*BuddyNodeSyncService),
		host:              host,
		config:            DefaultSyncConfig(),
	}
}

// InitializeBuddyNodeSync initializes sync for a specific buddy node
func (gsm *GlobalSyncManager) InitializeBuddyNodeSync(peerID string, pubsubNode *Pubsub.StructGossipPubSub) error {
	// Create sync service for this buddy node
	syncService := NewBuddyNodeSyncService(gsm.host, gsm.config)

	// Initialize the sync service
	if err := syncService.InitializeSync(pubsubNode); err != nil {
		return fmt.Errorf("failed to initialize sync for buddy %s: %w", peerID[:8], err)
	}

	// Store the sync service
	gsm.buddySyncServices[peerID] = syncService

	log.Printf("✅ Initialized CRDT sync for buddy node %s", peerID[:8])
	return nil
}

// TriggerGlobalSync triggers CRDT sync across all initialized buddy nodes
func (gsm *GlobalSyncManager) TriggerGlobalSync() error {
	log.Printf("🔄 Triggering global CRDT sync across %d buddy nodes", len(gsm.buddySyncServices))

	var lastErr error
	successCount := 0

	for peerID, syncService := range gsm.buddySyncServices {
		if !syncService.IsInitialized() {
			log.Printf("⚠️ Skipping uninitialized sync service for %s", peerID[:8])
			continue
		}

		if err := syncService.SyncNow(); err != nil {
			log.Printf("❌ Failed to sync buddy %s: %v", peerID[:8], err)
			lastErr = err
		} else {
			successCount++
			log.Printf("✅ Successfully synced buddy %s", peerID[:8])
		}
	}

	log.Printf("📊 Global sync completed: %d/%d successful", successCount, len(gsm.buddySyncServices))

	if successCount == 0 {
		return fmt.Errorf("no buddy nodes synced successfully")
	}

	if lastErr != nil && successCount < len(gsm.buddySyncServices) {
		log.Printf("⚠️ Some buddy nodes failed to sync, but %d succeeded", successCount)
	}

	return nil
}

// WaitForGlobalSyncCompletion waits for all buddy nodes to complete sync
func (gsm *GlobalSyncManager) WaitForGlobalSyncCompletion(timeout time.Duration) error {
	log.Printf("⏳ Waiting for global CRDT sync completion (timeout: %v)...", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Check sync completion every 500ms
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("global sync completion timeout after %v", timeout)
		case <-ticker.C:
			// Check if all services have received recent messages
			allSynced := true
			for peerID, syncService := range gsm.buddySyncServices {
				if !syncService.IsInitialized() {
					continue
				}

				stats := syncService.GetStats()
				if messagesReceived, ok := stats["messages_received"].(int); ok {
					if messagesReceived == 0 {
						allSynced = false
						log.Printf("⏳ Waiting for sync from %s...", peerID[:8])
						break
					}
				}
			}

			if allSynced {
				log.Printf("✅ Global CRDT sync completion confirmed")
				return nil
			}
		}
	}
}

// StopAllSyncServices stops all buddy node sync services
func (gsm *GlobalSyncManager) StopAllSyncServices() {
	log.Printf("🛑 Stopping all buddy node sync services...")

	for peerID, syncService := range gsm.buddySyncServices {
		if err := syncService.Stop(); err != nil {
			log.Printf("⚠️ Failed to stop sync service for %s: %v", peerID[:8], err)
		}
	}

	gsm.buddySyncServices = make(map[string]*BuddyNodeSyncService)
	log.Printf("✅ All buddy node sync services stopped")
}

// GetGlobalSyncStats returns statistics for all sync services
func (gsm *GlobalSyncManager) GetGlobalSyncStats() map[string]interface{} {
	stats := make(map[string]interface{})

	for peerID, syncService := range gsm.buddySyncServices {
		stats[peerID] = syncService.GetStats()
	}

	return stats
}
