package CRDTSync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"gossipnode/config"
	AppContext "gossipnode/config/Context"
	"gossipnode/crdt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
)

const (
	SyncServiceAppContext = "avc.crdtsync.service"
)

// SyncService provides CRDT synchronization for buddy nodes
type SyncService struct {
	nodeID   string
	host     host.Host
	engine   *crdt.MemStore
	topic    *pubsub.Topic
	sub      *pubsub.Subscription
	msgStore *MessageStore
	config   *SyncConfig
	ctx      context.Context
	cancel   context.CancelFunc
}

// SyncConfig holds configuration for CRDT synchronization
type SyncConfig struct {
	Mode              SyncMode
	SyncInterval      time.Duration
	TopicName         string
	PrintStore        bool
	HeartbeatInterval time.Duration
}

// SyncMode represents the synchronization mode
type SyncMode string

const (
	ModePublish   SyncMode = "publish"
	ModeSubscribe SyncMode = "subscribe"
	ModeBoth      SyncMode = "both"
)

// DefaultSyncConfig returns a default configuration
func DefaultSyncConfig() *SyncConfig {
	return &SyncConfig{
		Mode:              ModeBoth,
		SyncInterval:      3 * time.Second, // Faster sync for vote aggregation
		TopicName:         config.Pubsub_CRDTSync,
		PrintStore:        false,
		HeartbeatInterval: 10 * time.Second, // More frequent heartbeat
	}
}

// NewSyncService creates a new CRDT synchronization service
func NewSyncService(
	host host.Host,
	engine *crdt.MemStore,
	topic *pubsub.Topic,
	config *SyncConfig,
) *SyncService {
	ctx, cancel := AppContext.GetAppContext(SyncServiceAppContext).NewChildContext()

	return &SyncService{
		nodeID:   host.ID().String(),
		host:     host,
		engine:   engine,
		topic:    topic,
		msgStore: NewMessageStore(),
		config:   config,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start begins the synchronization process
func (s *SyncService) Start() error {
	// Subscribe to topic
	sub, err := s.topic.Subscribe()
	if err != nil {
		return fmt.Errorf("failed to subscribe to topic: %w", err)
	}
	s.sub = sub

	// Start subscriber goroutine
	go s.startSubscriber()

	// Send initial sync request
	if err := s.sendSyncRequest(); err != nil {
		log.Printf("⚠️  Failed to send initial sync request: %v", err)
	}

	return nil
}

// Stop gracefully stops the synchronization
func (s *SyncService) Stop() error {
	s.cancel()

	if s.sub != nil {
		s.sub.Cancel()
	}

	return nil
}

// SyncNow forces an immediate synchronization
func (s *SyncService) SyncNow() error {
	return s.syncCRDTState()
}

// GetStats returns synchronization statistics
func (s *SyncService) GetStats() map[string]interface{} {
	peers := s.host.Network().Peers()
	allCRDTs := s.engine.GetAllCRDTs()

	stats := map[string]interface{}{
		"connected_peers":   len(peers),
		"crdt_objects":      len(allCRDTs),
		"messages_received": s.msgStore.Count(),
		"mode":              string(s.config.Mode),
		"sync_interval":     s.config.SyncInterval.String(),
	}

	return stats
}

// startSubscriber starts the subscriber goroutine
func (s *SyncService) startSubscriber() {
	log.Printf("📨 CRDT Subscriber started for node %s", s.nodeID[:8])

	for {
		select {
		case <-s.ctx.Done():
			log.Printf("📨 CRDT Subscriber stopping...")
			return
		default:
			if err := s.receiveCRDTMessage(); err != nil {
				if s.ctx.Err() != nil {
					return
				}
				// Only log real errors, not timeouts
				log.Printf("❌ CRDT Subscription error: %v", err)
				time.Sleep(time.Second)
			}
		}
	}
}

// syncCRDTState publishes the current CRDT state
func (s *SyncService) syncCRDTState() error {
	// Get all CRDTs from local engine
	allCRDTs := s.engine.GetAllCRDTs()

	// Convert CRDTs to JSON RawMessage for proper serialization
	syncData := make(map[string]json.RawMessage)
	for key, crdt := range allCRDTs {
		data, err := json.Marshal(crdt)
		if err != nil {
			log.Printf("⚠️  Failed to marshal CRDT %s: %v", key, err)
			continue
		}
		syncData[key] = data
	}

	// Create sync message
	msg := Message{
		Type:      config.Type_CRDT_SYNC,
		NodeID:    s.nodeID,
		Key:       "all-crdts",
		SyncData:  syncData,
		Timestamp: time.Now().UTC(),
	}

	// Serialize and publish
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal CRDT sync message: %w", err)
	}

	if err := s.topic.Publish(s.ctx, data); err != nil {
		return fmt.Errorf("failed to publish CRDT sync message: %w", err)
	}

	log.Printf("📤 Synced CRDT state: %d objects", len(allCRDTs))
	return nil
}

// receiveCRDTMessage receives and processes CRDT messages
func (s *SyncService) receiveCRDTMessage() error {
	// Create a context with timeout to avoid blocking indefinitely
	ctx, cancel := AppContext.GetAppContext(SyncServiceAppContext).NewChildContextWithTimeout(1*time.Second)
	defer cancel()

	msg, err := s.sub.Next(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// This is a normal timeout, not an error
			return nil
		}
		return fmt.Errorf("failed to receive CRDT message: %w", err)
	}

	// Parse CRDT message
	var crdtMsg Message
	if err := json.Unmarshal(msg.Data, &crdtMsg); err != nil {
		return fmt.Errorf("failed to unmarshal CRDT message: %w", err)
	}

	// Skip our own messages
	if crdtMsg.NodeID == s.nodeID {
		return nil
	}

	// Store message
	key := fmt.Sprintf("%s-%d", crdtMsg.NodeID[:8], crdtMsg.Timestamp.UnixNano())
	s.msgStore.Add(key, crdtMsg)

	// Handle sync messages
	if crdtMsg.Type == config.Type_CRDT_SYNC && crdtMsg.SyncData != nil {
		if err := s.applyCRDTSync(crdtMsg); err != nil {
			log.Printf("⚠️  Failed to apply CRDT sync: %v", err)
		}
	}

	// Handle sync request: reply with full state
	if crdtMsg.Type == "sync_request" {
		if err := s.syncCRDTState(); err != nil {
			log.Printf("⚠️  Failed to respond to sync request: %v", err)
		} else {
			log.Printf("📤 Responded to sync request from %s", crdtMsg.NodeID[:8])
		}
	}

	log.Printf("📨 Received CRDT message from %s: %s", crdtMsg.NodeID[:8], crdtMsg.Type)
	return nil
}

// applyCRDTSync applies received CRDT state to local engine
func (s *SyncService) applyCRDTSync(msg Message) error {
	log.Printf("🔄 Applying CRDT sync from %s with %d objects", msg.NodeID[:8], len(msg.SyncData))

	// Get our current CRDTs
	ourCRDTs := s.engine.GetAllCRDTs()

	// Merge each CRDT from the sync message
	for key, rawData := range msg.SyncData {
		// Try to unmarshal as different CRDT types
		var remoteCRDT crdt.CRDT

		// Try LWWSet first
		var lwwSet crdt.LWWSet
		if err := json.Unmarshal(rawData, &lwwSet); err == nil {
			remoteCRDT = &lwwSet
		} else {
			// Try Counter
			var counter crdt.Counter
			if err := json.Unmarshal(rawData, &counter); err == nil {
				remoteCRDT = &counter
			} else {
				log.Printf("⚠️  Failed to unmarshal CRDT %s: %v", key, err)
				continue
			}
		}

		if ourCRDT, exists := ourCRDTs[key]; exists {
			// Both nodes have this CRDT - merge them
			_, err := ourCRDT.Merge(remoteCRDT)
			if err != nil {
				log.Printf("⚠️  Failed to merge CRDT %s: %v", key, err)
				continue
			}

			// Apply the merged result by creating a new operation
			op := &crdt.Op{
				Key:      key,
				Kind:     crdt.OpAdd, // Use appropriate operation kind
				NodeID:   s.nodeID,
				WallTime: time.Now().UTC(),
			}

			if err := s.engine.AppendOp(op); err != nil {
				log.Printf("⚠️  Failed to apply merged CRDT %s: %v", key, err)
				continue
			}

			log.Printf("✅ Merged CRDT %s", key)
		} else {
			// We don't have this CRDT - just apply it
			op := &crdt.Op{
				Key:      key,
				Kind:     crdt.OpAdd, // Use appropriate operation kind
				NodeID:   s.nodeID,
				WallTime: time.Now().UTC(),
			}

			if err := s.engine.AppendOp(op); err != nil {
				log.Printf("⚠️  Failed to apply new CRDT %s: %v", key, err)
				continue
			}

			log.Printf("✅ Applied new CRDT %s", key)
		}
	}

	return nil
}

// sendSyncRequest asks peers to publish their full CRDT state immediately
func (s *SyncService) sendSyncRequest() error {
	msg := Message{
		Type:      "sync_request",
		NodeID:    s.nodeID,
		Key:       "request",
		Timestamp: time.Now().UTC(),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal sync request: %w", err)
	}
	return s.topic.Publish(s.ctx, data)
}

// GetMessageStore returns the message store
func (s *SyncService) GetMessageStore() *MessageStore {
	return s.msgStore
}

// GetCRDTEngine returns the underlying CRDT engine
func (s *SyncService) GetCRDTEngine() *crdt.MemStore {
	return s.engine
}
