package Subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"gossipnode/config"
	"gossipnode/config/PubSubMessages"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
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

// NewEnhancedSubscriber creates a new EnhancedSubscriber instance
func NewEnhancedSubscriber(subscription *pubsub.Subscription, gps *PubSubMessages.GossipPubSub, handler func(*PubSubMessages.GossipMessage)) *EnhancedSubscriber {
	return &EnhancedSubscriber{
		subscription: subscription,
		metrics: &EnhancedSubscriberMetrics{
			uniquePeers: make(map[string]int64),
		},
		gps:     gps,
		handler: handler,
	}
}

// SubscribeEnhanced subscribes to a topic with enhanced reliability
func SubscribeEnhanced(gps *PubSubMessages.GossipPubSub, topic string, handler func(*PubSubMessages.GossipMessage)) error {
	fmt.Printf("About to call SubscribeEnhanced for %s\n", topic)

	// Check if we can subscribe to this channel
	if !CanSubscribe(gps, topic, gps.Host.ID()) {
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
	if gps.TopicSubscribers[topic] == nil {
		gps.TopicSubscribers[topic] = make(map[peer.ID]bool)
	}

	// Add this peer as a subscriber to the topic
	gps.TopicSubscribers[topic][gps.Host.ID()] = true
	gps.Mutex.Unlock()

	// Subscribe using enhanced GossipSub if available
	if gps.GossipSubPS != nil {
		if err := subscribeEnhancedViaGossipSub(gps, topic, handler); err != nil {
			return fmt.Errorf("failed to subscribe via enhanced GossipSub: %w", err)
		}
	}

	fmt.Printf("SubscribeEnhanced completed successfully for %s\n", topic)
	log.Printf("📨 Enhanced subscription to topic: %s", topic)
	return nil
}

// subscribeEnhancedViaGossipSub subscribes to a topic using enhanced libp2p GossipSub
func subscribeEnhancedViaGossipSub(gps *PubSubMessages.GossipPubSub, topicName string, handler func(*PubSubMessages.GossipMessage)) error {
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

	// Create enhanced subscriber
	enhancedSubscriber := NewEnhancedSubscriber(sub, gps, handler)

	// Start enhanced message processing
	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel // Store cancel function for cleanup

	fmt.Printf("Context set for %s\n", topicName)
	go enhancedSubscriber.runEnhanced(ctx)

	fmt.Printf("subscribeEnhancedViaGossipSub returned successfully for %s\n", topicName)
	return nil
}

// runEnhanced executes the enhanced subscriber loop with better error handling
func (es *EnhancedSubscriber) runEnhanced(ctx context.Context) {
	log.Printf("📨 Enhanced subscriber started, listening for messages...")

	for {
		select {
		case <-ctx.Done():
			log.Printf("📨 Enhanced subscriber stopping...")
			return
		default:
			if err := es.receiveMessageEnhanced(ctx); err != nil {
				if ctx.Err() != nil {
					// Context cancelled, exit gracefully
					return
				}
				log.Printf("❌ Enhanced subscription error: %v", err)
				atomic.AddInt64(&es.metrics.ReceiveErrors, 1)

				// Add exponential backoff for errors
				time.Sleep(time.Second)
			}
		}
	}
}

// receiveMessageEnhanced receives and processes a single message with validation
func (es *EnhancedSubscriber) receiveMessageEnhanced(ctx context.Context) error {
	msg, err := es.subscription.Next(ctx)
	if err != nil {
		return fmt.Errorf("failed to receive message: %w", err)
	}

	// Validate BEFORE processing
	if err := es.validateMessage(msg); err != nil {
		atomic.AddInt64(&es.metrics.ValidationErrors, 1)
		log.Printf("⚠️ Validation failed: %v", err)
		return nil // Don't treat as fatal error, continue processing
	}

	// Update metrics (thread-safe)
	atomic.AddInt64(&es.metrics.MessagesReceived, 1)
	atomic.StoreInt64(&es.metrics.lastReceiveTime, time.Now().UTC().UnixNano())

	// Track unique peers (protected by mutex)
	peerID := msg.ReceivedFrom.String()
	es.metrics.uniquePeersMu.Lock()
	es.metrics.uniquePeers[peerID] = time.Now().UTC().UnixNano()
	es.metrics.uniquePeersMu.Unlock()

	// Process the message
	return es.processMessageEnhanced(msg)
}

// validateMessage validates the received message
func (es *EnhancedSubscriber) validateMessage(msg *pubsub.Message) error {
	// Basic validation
	if msg == nil {
		return fmt.Errorf("received nil message")
	}

	if len(msg.Data) == 0 {
		return fmt.Errorf("received empty message")
	}

	if len(msg.Data) > 1024*1024 { // 1MB limit
		return fmt.Errorf("message too large: %d bytes", len(msg.Data))
	}

	// Validate peer ID
	if msg.ReceivedFrom == "" {
		return fmt.Errorf("message has no sender peer ID")
	}

	return nil
}

// processMessageEnhanced processes a received message with enhanced handling
func (es *EnhancedSubscriber) processMessageEnhanced(msg *pubsub.Message) error {
	// Parse the actual message data from raw bytes
	var messageData PubSubMessages.Message
	if err := json.Unmarshal(msg.Data, &messageData); err != nil {
		log.Printf("Failed to unmarshal message data: %v", err)
		log.Printf("Raw bytes: %v", msg.Data)
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}

	// Attach ACK if missing
	if messageData.ACK == nil {
		log.Printf("Received message with nil ACK - attaching default ACK")

		// Create a default ACK with Type_Publish stage
		ack := PubSubMessages.NewACKBuilder().
			True_ACK_Message(msg.GetFrom(), config.Type_Publish)

		messageData.SetACK(ack)
	}

	// Convert to our GossipMessage format
	gossipMsg := &PubSubMessages.GossipMessage{
		ID:        msg.ID,
		Topic:     "", // Will be set by caller
		Data:      &messageData,
		Sender:    msg.GetFrom(),
		Timestamp: time.Now().UTC().UnixNano(),
		TTL:       0,
	}

	// Call the handler
	if es.handler != nil {
		es.handler(gossipMsg)
	}

	log.Printf("📨 Enhanced processing completed for message from %s", msg.ReceivedFrom.String())
	return nil
}

// GetMetrics returns current subscriber metrics (thread-safe)
func (es *EnhancedSubscriber) GetMetrics() EnhancedSubscriberMetrics {
	es.metrics.uniquePeersMu.RLock()
	// Create a copy of unique peers map
	uniquePeers := make(map[string]int64, len(es.metrics.uniquePeers))
	for peer, timestamp := range es.metrics.uniquePeers {
		uniquePeers[peer] = timestamp
	}
	es.metrics.uniquePeersMu.RUnlock()

	lastRecvNano := atomic.LoadInt64(&es.metrics.lastReceiveTime)

	return EnhancedSubscriberMetrics{
		MessagesReceived: atomic.LoadInt64(&es.metrics.MessagesReceived),
		ReceiveErrors:    atomic.LoadInt64(&es.metrics.ReceiveErrors),
		ValidationErrors: atomic.LoadInt64(&es.metrics.ValidationErrors),
		lastReceiveTime:  lastRecvNano,
		uniquePeers:      uniquePeers,
	}
}

// GetUniquePeerCount returns the number of unique peers seen (thread-safe)
func (es *EnhancedSubscriber) GetUniquePeerCount() int {
	es.metrics.uniquePeersMu.RLock()
	defer es.metrics.uniquePeersMu.RUnlock()
	return len(es.metrics.uniquePeers)
}

// GetRecentPeers returns peers that have sent messages recently (thread-safe)
func (es *EnhancedSubscriber) GetRecentPeers(since time.Duration) []string {
	var recentPeers []string
	cutoff := time.Now().UTC().Add(-since).UnixNano()

	es.metrics.uniquePeersMu.RLock()
	defer es.metrics.uniquePeersMu.RUnlock()

	for peer, timestamp := range es.metrics.uniquePeers {
		if timestamp > cutoff {
			recentPeers = append(recentPeers, peer)
		}
	}

	return recentPeers
}
