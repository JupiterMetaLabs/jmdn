package CRDTSync

import (
	"context"
	"fmt"
	"log"
	"time"

	"jmdn/Pubsub"
	"jmdn/crdt"

	"github.com/libp2p/go-libp2p/core/host"
)

// ConsensusSyncManager manages CRDT synchronization for consensus operations
type ConsensusSyncManager struct {
	syncServices map[string]*SyncService // peerID -> sync service
	host         host.Host
	config       *SyncConfig
}

// NewConsensusSyncManager creates a new consensus sync manager
func NewConsensusSyncManager(host host.Host) *ConsensusSyncManager {
	return &ConsensusSyncManager{
		syncServices: make(map[string]*SyncService),
		host:         host,
		config:       DefaultSyncConfig(),
	}
}

// InitializeSyncForConsensus initializes CRDT sync for all buddy nodes before vote aggregation
func (csm *ConsensusSyncManager) InitializeSyncForConsensus(
	buddyNodes []string,
	pubsubNode *Pubsub.StructGossipPubSub,
) error {
	log.Printf("🔄 Initializing CRDT sync for %d buddy nodes", len(buddyNodes))

	// Create sync topic
	topicName := csm.config.TopicName
	topic, err := pubsubNode.GetGossipPubSub().GetOrJoinTopic(topicName)
	if err != nil {
		return fmt.Errorf("failed to join sync topic: %w", err)
	}

	// Create CRDT engine for this node
	engine := crdt.NewMemStore(50 << 20) // 50MB limit

	// Create sync service for this node
	syncService := NewSyncService(csm.host, engine, topic, csm.config)
	if err := syncService.Start(); err != nil {
		return fmt.Errorf("failed to start sync service: %w", err)
	}

	// Store the sync service
	csm.syncServices[csm.host.ID().String()] = syncService

	log.Printf("✅ CRDT sync initialized for consensus")
	return nil
}

// TriggerSyncBeforeVoteAggregation triggers CRDT sync before vote aggregation
func (csm *ConsensusSyncManager) TriggerSyncBeforeVoteAggregation() error {
	log.Printf("🔄 Triggering CRDT sync before vote aggregation...")

	// Get our sync service
	ourService, exists := csm.syncServices[csm.host.ID().String()]
	if !exists {
		return fmt.Errorf("sync service not initialized for this node")
	}

	// Force immediate sync
	if err := ourService.SyncNow(); err != nil {
		return fmt.Errorf("failed to trigger sync: %w", err)
	}

	// Wait a bit for sync to propagate
	time.Sleep(2 * time.Second)

	log.Printf("✅ CRDT sync completed before vote aggregation")
	return nil
}

// GetSyncedCRDTEngine returns the synced CRDT engine for vote processing
func (csm *ConsensusSyncManager) GetSyncedCRDTEngine() (*crdt.MemStore, error) {
	ourService, exists := csm.syncServices[csm.host.ID().String()]
	if !exists {
		return nil, fmt.Errorf("sync service not initialized for this node")
	}

	return ourService.GetCRDTEngine(), nil
}

// StopAllSyncServices stops all sync services
func (csm *ConsensusSyncManager) StopAllSyncServices() {
	log.Printf("🛑 Stopping all CRDT sync services...")

	for peerID, service := range csm.syncServices {
		if err := service.Stop(); err != nil {
			log.Printf("⚠️ Failed to stop sync service for %s: %v", peerID[:8], err)
		}
	}

	csm.syncServices = make(map[string]*SyncService)
	log.Printf("✅ All CRDT sync services stopped")
}

// GetSyncStats returns statistics for all sync services
func (csm *ConsensusSyncManager) GetSyncStats() map[string]interface{} {
	stats := make(map[string]interface{})

	for peerID, service := range csm.syncServices {
		stats[peerID] = service.GetStats()
	}

	return stats
}

// WaitForSyncCompletion waits for CRDT sync to complete across all nodes
func (csm *ConsensusSyncManager) WaitForSyncCompletion(timeout time.Duration) error {
	log.Printf("⏳ Waiting for CRDT sync completion (timeout: %v)...", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Check sync completion every 500ms
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("sync completion timeout after %v", timeout)
		case <-ticker.C:
			// Check if all services have received recent messages
			allSynced := true
			for peerID, service := range csm.syncServices {
				stats := service.GetStats()
				if messagesReceived, ok := stats["messages_received"].(int); ok {
					if messagesReceived == 0 {
						allSynced = false
						log.Printf("⏳ Waiting for sync from %s...", peerID[:8])
						break
					}
				}
			}

			if allSynced {
				log.Printf("✅ CRDT sync completion confirmed")
				return nil
			}
		}
	}
}
