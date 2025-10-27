package Subscription

import (
	"context"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/config/PubSubMessages"
	"time"

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

	// Initialize TopicSubscribers map if not exists
	if gps.TopicSubscribers == nil {
		gps.TopicSubscribers = make(map[string]map[peer.ID]bool)
	}
	if gps.TopicSubscribers[topic] == nil {
		gps.TopicSubscribers[topic] = make(map[peer.ID]bool)
	}

	// Add this peer as a subscriber to the topic
	gps.TopicSubscribers[topic][gps.Host.ID()] = true

	// Subscribe using GossipSub if available
	if gps.GossipSubPS != nil {
		if err := subscribeViaGossipSub(gps, topic, handler); err != nil {
			return fmt.Errorf("failed to subscribe via GossipSub: %w", err)
		}
	}

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

// subscribeViaGossipSub subscribes to a topic using libp2p GossipSub
func subscribeViaGossipSub(gps *PubSubMessages.GossipPubSub, topicName string, handler func(*PubSubMessages.GossipMessage)) error {
	// Get or join the topic
	topic, err := gps.GetOrJoinTopic(topicName)
	if err != nil {
		return fmt.Errorf("failed to get or join topic %s: %w", topicName, err)
	}

	// Subscribe to the topic
	sub, err := topic.Subscribe()
	if err != nil {
		return fmt.Errorf("failed to subscribe to topic %s: %w", topicName, err)
	}

	// Start a goroutine to handle incoming messages with proper context
	ctx, cancel := context.WithCancel(context.Background())
	// Store cancel function for cleanup (in a real implementation, you'd track this)
	_ = cancel

	go func() {
		defer cancel()
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				// Check if context was cancelled
				if err == context.Canceled || err == context.DeadlineExceeded {
					fmt.Printf("GossipSub subscription cancelled for topic: %s\n", topicName)
				} else {
					fmt.Printf("Error reading message from GossipSub: %v\n", err)
				}
				return
			}

			// Parse the actual message data from raw bytes
			var messageData PubSubMessages.Message
			if err := json.Unmarshal(msg.Data, &messageData); err != nil {
				fmt.Printf("Failed to unmarshal message data: %v\n", err)
				// Continue to next message
				continue
			}

			// Convert to our GossipMessage format
			gossipMsg := &PubSubMessages.GossipMessage{
				ID:        msg.ID,
				Topic:     topicName,
				Data:      &messageData,
				Sender:    msg.GetFrom(),
				Timestamp: int64(time.Now().Unix()),
				TTL:       0,
			}

			// Call the handler
			if handler != nil {
				handler(gossipMsg)
			}
		}
	}()

	return nil
}
