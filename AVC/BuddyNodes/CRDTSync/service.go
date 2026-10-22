package CRDTSync

import (
	"time"

	"gossipnode/config"
)

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
	ModeBoth SyncMode = "both"
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
