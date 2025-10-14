package Pubsub

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// GossipMessage represents a message in the gossip pub/sub system
type GossipMessage struct {
	ID        string                 `json:"id"`                 // Unique message identifier
	Topic     string                 `json:"topic"`              // Channel/topic name
	Data      interface{}            `json:"data"`               // Message payload
	Sender    peer.ID                `json:"sender"`             // Sender's peer ID
	Timestamp int64                  `json:"timestamp"`          // Unix timestamp
	TTL       int                    `json:"ttl"`                // Time to live (hop count)
	Metadata  map[string]interface{} `json:"metadata,omitempty"` // Additional metadata
}

// ChannelAccess represents access control for a channel
type ChannelAccess struct {
	ChannelName  string                `json:"channelName"`  // Name of the channel
	AllowedPeers map[string]bool       `json:"allowedPeers"` // Peers allowed to subscribe
	IsPublic     bool                  `json:"isPublic"`     // If true, anyone can join
	Creator      peer.ID               `json:"creator"`      // Who created the channel (gave multiple addresses for fallback)
	CreatedAt    int64                 `json:"createdAt"`    // When channel was created
}

// GossipPubSub handles gossip-based pub/sub messaging
type GossipPubSub struct {
	Host          host.Host                       // libp2p host instance
	topics        map[string]bool                 // Subscribed topics
	handlers      map[string]func(*GossipMessage) // Topic -> handler function
	messageCache  map[string]bool                 // Message deduplication
	channelAccess map[string]*ChannelAccess       // Channel access control
	peers         []peer.ID                       // Connected peers
	mutex         sync.RWMutex                    // Read-write mutex for thread safety
	messageID     uint64                          // Counter for message IDs
}
