package Subscription

import (
	"context"
	AppContext "gossipnode/config/Context"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)


// Subscribe subscribes to a topic with access control (now uses enhanced implementation)
func Subscribe(gps *PubSubMessages.GossipPubSub, topic string, handler func(*PubSubMessages.GossipMessage)) error {
	// Use enhanced subscription if available, fall back to original implementation
	if gps.GossipSubPS != nil {
		return SubscribeEnhanced(gps, topic, handler)
	}

	// Fall back to original implementation for custom gossip
	return subscribeOriginal(gps, topic, handler)
}

// subscribeOriginal is the original subscribe implementation (renamed for clarity)
func subscribeOriginal(gps *PubSubMessages.GossipPubSub, topic string, handler func(*PubSubMessages.GossipMessage)) error {
	fmt.Printf("About to call Subscribe for %s\n", topic)
	// Check if we can subscribe to this channel
	if !CanSubscribe(gps, topic, gps.Host.ID()) {
		log.LogConsensusError(fmt.Sprintf("Access denied: not authorized to subscribe to channel %s", topic), nil, zap.String("topic", topic), zap.String("function", "Subscription.Subscribe"))
		return fmt.Errorf("access denied: not authorized to subscribe to channel %s", topic)
	}
	fmt.Printf("CanSubscribe returned true for %s\n", topic)
	gps.Mutex.Lock()
	gps.Topics[topic] = true
	gps.Handlers[topic] = handler
	// Initialize TopicSubscribers map if not exists
	if gps.TopicSubscribers == nil {
		gps.TopicSubscribers = make(map[string]map[peer.ID]bool)
	}
	fmt.Printf("TopicSubscribers map initialized for %s\n", topic)
	if gps.TopicSubscribers[topic] == nil {
		gps.TopicSubscribers[topic] = make(map[peer.ID]bool)
	}

	// Add this peer as a subscriber to the topic
	gps.TopicSubscribers[topic][gps.Host.ID()] = true
	fmt.Printf("TopicSubscribers map updated for %s\n", topic)
	gps.Mutex.Unlock() // Unlock before calling subscribeViaGossipSub to avoid deadlock

	// Subscribe using GossipSub if available
	if gps.GossipSubPS != nil {
		if err := subscribeViaGossipSub(gps, topic, handler); err != nil {
			return fmt.Errorf("failed to subscribe via GossipSub: %w", err)
		}
	}
	fmt.Printf("subscribeViaGossipSub returned successfully for %s\n", topic)
	log.LogConsensusInfo(fmt.Sprintf("Subscribed to topic: %s", topic), zap.String("topic", topic), zap.String("function", "Subscription.Subscribe"))
	return nil
}

// CanSubscribe checks if a peer can subscribe to a channel
func CanSubscribe(gps *PubSubMessages.GossipPubSub, channelName string, peerID peer.ID) bool {
	fmt.Printf("About to call CanSubscribe for %s\n", channelName)
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
	fmt.Printf("About to call GetOrJoinTopic for %s\n", topicName)
	// Get or join the topic
	topic, err := gps.GetOrJoinTopic(topicName)
	if err != nil {
		return fmt.Errorf("failed to get or join topic %s: %w", topicName, err)
	}
	fmt.Printf("GetOrJoinTopic returned successfully for %s\n", topicName)
	// Subscribe to the topic
	sub, err := topic.Subscribe()
	if err != nil {
		return fmt.Errorf("failed to subscribe to topic %s: %w", topicName, err)
	}
	fmt.Printf("Subscribe returned successfully for %s\n", topicName)
	// Start a goroutine to handle incoming messages with proper context
	ctx, cancel := AppContext.GetAppContext(SubscriptionAppContext).NewChildContext()

	fmt.Printf("Context set for %s\n", topicName)
	go func() {
		defer cancel()
		for {
			fmt.Printf("About to call Next for %s\n", topicName)
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
			fmt.Printf("Next returned successfully for %s\n", topicName)
			// Parse the actual message data from raw bytes
			var messageData PubSubMessages.Message
			if err := json.Unmarshal(msg.Data, &messageData); err != nil {
				fmt.Printf("Failed to unmarshal message data: %v\n", err)
				fmt.Printf("Raw bytes: %v\n", msg.Data)
				// Continue to next message
				continue
			}
			fmt.Printf("Message unmarshalled successfully for %s\n", topicName)
			fmt.Printf("Unmarshalled messageData: %+v\n", messageData)

			// Attach ACK if missing
			if messageData.ACK == nil {
				fmt.Printf("Received message with nil ACK - attaching default ACK\n")

				// Create a default ACK with Type_Publish stage
				ack := PubSubMessages.NewACKBuilder().
					True_ACK_Message(msg.GetFrom(), config.Type_Publish)

				messageData.SetACK(ack)
			}

			// Convert to our GossipMessage format
			gossipMsg := &PubSubMessages.GossipMessage{
				ID:        msg.ID,
				Topic:     topicName,
				Data:      &messageData,
				Sender:    msg.GetFrom(),
				Timestamp: int64(time.Now().UTC().Unix()),
				TTL:       0,
			}
			fmt.Printf("GossipMessage created successfully for %s\n", topicName)
			// Call the handler
			if handler != nil {
				handler(gossipMsg)
			}
		}
	}()
	fmt.Printf("subscribeViaGossipSub returned successfully for %s\n", topicName)
	return nil
}
