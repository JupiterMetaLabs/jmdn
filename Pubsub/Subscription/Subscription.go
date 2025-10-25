package Subscription

import (
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Subscribe subscribes to a topic with access control
func Subscribe(gps *PubSubMessages.GossipPubSub, topic string, handler func(*PubSubMessages.GossipMessage)) error {
	// Check if we can subscribe to this channel
	if !CanSubscribe(gps, topic, gps.Host.ID()) {
		log.LogConsensusError(fmt.Sprintf("Access denied: not authorized to subscribe to channel %s", topic), nil, zap.String("topic", topic), zap.String("function", "Subscription.Subscribe"))
		return fmt.Errorf("access denied: not authorized to subscribe to channel %s", topic)
	}

	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	gps.Topics[topic] = true
	gps.Handlers[topic] = handler

	log.LogConsensusInfo(fmt.Sprintf("Subscribed to topic: %s", topic), zap.String("topic", topic), zap.String("function", "Subscription.Subscribe"))
	return nil
}

// CanSubscribe checks if a peer can subscribe to a channel
func CanSubscribe(gps *PubSubMessages.GossipPubSub, channelName string, peerID peer.ID) bool {
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()

	access, exists := gps.ChannelAccess[channelName]
	if !exists {
		return false // Channel doesn't exist
	}

	// Public channels allow anyone
	if access.IsPublic {
		return true
	}

	// Check if peer is in allowed list
	return access.AllowedPeers[peerID]
}

// Unsubscribe unsubscribes from a topic
func Unsubscribe(gps *PubSubMessages.GossipPubSub, topic string) error {
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	delete(gps.Topics, topic)
	delete(gps.Handlers, topic)

	log.LogConsensusInfo(fmt.Sprintf("Unsubscribed from topic: %s", topic), zap.String("topic", topic), zap.String("function", "Subscription.Unsubscribe"))
	return nil
}
