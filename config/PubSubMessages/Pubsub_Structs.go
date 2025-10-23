package PubSubMessages

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)
// GossipMessage represents a message in the gossip pub/sub system
type GossipMessage struct {
	ID        string                 `json:"id"`                 // Unique message identifier
	Topic     string                 `json:"topic"`              // Channel/topic name
	Data      *Message                 `json:"data"`               // Message payload
	Sender    peer.ID                `json:"sender"`             // Sender's peer ID
	Timestamp int64                  `json:"timestamp"`          // Unix timestamp
	TTL       int                    `json:"ttl"`                // Time to live (hop count)
	Metadata  map[string]string `json:"metadata,omitempty"` // Additional metadata
}

// ChannelAccess represents access control for a channel
type ChannelAccess struct {
	ChannelName  string                `json:"channelName"`  // Name of the channel
	AllowedPeers map[peer.ID]bool       `json:"allowedPeers"` // Peers allowed to subscribe
	IsPublic     bool                  `json:"isPublic"`     // If true, anyone can join
	Creator      peer.ID               `json:"creator"`      // Who created the channel (gave multiple addresses for fallback)
	CreatedAt    int64                 `json:"createdAt"`    // When channel was created
}

// GossipPubSub handles gossip-based pub/sub messaging
type GossipPubSub struct {
	Host          host.Host                       // libp2p host instance
	Topics        map[string]bool                 // Subscribed topics
	Handlers      map[string]func(*GossipMessage) // Topic -> handler function
	MessageCache  map[string]bool                 // Message deduplication
	ChannelAccess map[string]*ChannelAccess       // Channel access control
	Peers         []peer.ID                       // Connected peers
	Mutex         sync.RWMutex                    // Read-write mutex for thread safety
	MessageID     uint64                          // Counter for message IDs
	Protocol      protocol.ID                     // Protocol ID
}

type Message struct {
	Sender    peer.ID
	Message   string // json string of the json message - it could be a vote, a block, a transaction, etc.
	Timestamp int64
	ACK       *ACK_Message
}
type ACK_Message struct {
	Status string `json:"status"`
	PeerID string `json:"peer_id"`
	Stage  string `json:"stage"`
}

type MessageProcessing struct {
	GossipMessage string
	Protocol protocol.ID
}

func ConvertMessageProcessingToInterface(messageProcessing *MessageProcessing) interface{} {
	return messageProcessing
}