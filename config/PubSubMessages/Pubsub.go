package PubSubMessages

import (
	"context"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// GossipMessage represents a message in the gossip pub/sub system
type GossipMessage struct {
	ID        string            `json:"id"`                 // Unique message identifier
	Topic     string            `json:"topic"`              // Channel/topic name
	Data      *Message          `json:"data"`               // Message payload
	Sender    peer.ID           `json:"sender"`             // Sender's peer ID
	Timestamp int64             `json:"timestamp"`          // Unix timestamp
	TTL       int               `json:"ttl"`                // Time to live (hop count)
	Metadata  map[string]string `json:"metadata,omitempty"` // Additional metadata
}

// ChannelAccess represents access control for a channel
type ChannelAccess struct {
	ChannelName  string           `json:"channelName"`  // Name of the channel
	AllowedPeers map[peer.ID]bool `json:"allowedPeers"` // Peers allowed to subscribe
	IsPublic     bool             `json:"isPublic"`     // If true, anyone can join
	Creator      peer.ID          `json:"creator"`      // Who created the channel
	CreatedAt    int64            `json:"createdAt"`    // When channel was created
}

// GossipPubSub handles gossip-based pub/sub messaging
type GossipPubSub struct {
	Host             host.Host                       // libp2p host instance
	Topics           map[string]bool                 // Subscribed topics
	Handlers         map[string]func(*GossipMessage) // Topic -> handler function
	MessageCache     map[string]bool                 // Message deduplication
	ChannelAccess    map[string]*ChannelAccess       // Channel access control
	Peers            []peer.ID                       // Connected peers
	TopicSubscribers map[string]map[peer.ID]bool     // Topic -> map of subscriber peer IDs
	Mutex            sync.RWMutex                    // Read-write mutex for thread safety
	MessageID        uint64                          // Counter for message IDs
	Protocol         protocol.ID                     // Protocol ID
	GossipSubPS      *pubsub.PubSub                  // libp2p GossipSub instance (NEW)
	TopicsMap        map[string]*pubsub.Topic        // Topic name -> GossipSub topic (NEW)

	// Subscriptions holds active GossipSub subscriptions per topic.
	// Unsubscribe must Cancel() these to avoid leaking goroutines.
	Subscriptions map[string]*pubsub.Subscription

	// SubscriptionCancels allows cancelling the message-consumer goroutine per topic.
	SubscriptionCancels map[string]context.CancelFunc
}

// Message represents the data payload of a gossip message
type Message struct {
	Sender    peer.ID
	Message   string // json string of the json message - it would be a OP of struct Types.OP with the Vote of struct Vote
	Timestamp int64
	ACK       *ACK

	// ============================================================================
	// BFT Consensus Fields (NEW)
	// ============================================================================
	RoundID          string    `json:"round_id,omitempty"`          // Consensus round identifier
	Phase            string    `json:"phase,omitempty"`             // "PREPARE" or "COMMIT"
	VoteData         []byte    `json:"vote_data,omitempty"`         // Serialized vote (PrepareMessage or CommitMessage)
	VoteValue        bool      `json:"vote_value,omitempty"`        // true = Accept, false = Reject
	BuddyNodeIDs     []peer.ID `json:"buddy_node_ids,omitempty"`    // List of buddy nodes in consensus
	ConsensusSuccess bool      `json:"consensus_success,omitempty"` // Final consensus result
	ProposalData     []byte    `json:"proposal_data,omitempty"`     // Proposal payload
	ProposalHash     string    `json:"proposal_hash,omitempty"`     // Hash of proposal
}

// Vote represents a vote on a block
type Vote struct {
	Vote      int8   `json:"vote"`       // 1 for yes, -1 for no
	BlockHash string `json:"block_hash"` // hash of the block
}

// BlockResult represents the result of block validation
type BlockResult struct {
	BlockHash string `json:"block_hash"`
	Result    bool   `json:"result"`
}

// ACK represents acknowledgment/status of a message
type ACK struct {
	Status string `json:"status"`
	PeerID string `json:"peer_id"`
	Stage  string `json:"stage"` // Message type: START_PUBSUB, END_PUBSUB, PUBLISH, etc.
}
