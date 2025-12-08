package Subscription

import (
	"gossipnode/config/PubSubMessages"
	"sync"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

var (
	LocalGRO interfaces.LocalGoroutineManagerInterface
)

// EnhancedSubscriberMetrics holds metrics for the enhanced subscriber
type EnhancedSubscriberMetrics struct {
	MessagesReceived int64
	ReceiveErrors    int64
	ValidationErrors int64
	lastReceiveTime  int64 // Unix nano
	uniquePeersMu    sync.RWMutex
	uniquePeers      map[string]int64
}

// EnhancedSubscriber represents an enhanced message subscriber
type EnhancedSubscriber struct {
	subscription *pubsub.Subscription
	metrics      *EnhancedSubscriberMetrics
	gps          *PubSubMessages.GossipPubSub
	handler      func(*PubSubMessages.GossipMessage)
}
