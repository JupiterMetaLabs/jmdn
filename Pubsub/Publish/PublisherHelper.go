package Publish

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"gossipnode/config"
	"gossipnode/config/PubSubMessages"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// EnhancedPublisherMetrics holds metrics for the enhanced publisher
type EnhancedPublisherMetrics struct {
	MessagesPublished int64
	PublishErrors     int64
	lastPublishTime   int64 // Unix nano
	BytesPublished    int64
}

// EnhancedPublisher represents an enhanced message publisher
type EnhancedPublisher struct {
	topic     *pubsub.Topic
	topicName string // retained for re-join on closed-topic errors
	metrics   *EnhancedPublisherMetrics
	gps       *PubSubMessages.GossipPubSub
}

// NewEnhancedPublisher creates a new EnhancedPublisher instance
func NewEnhancedPublisher(topicName string, topic *pubsub.Topic, gps *PubSubMessages.GossipPubSub) *EnhancedPublisher {
	return &EnhancedPublisher{
		topic:     topic,
		topicName: topicName,
		metrics:   &EnhancedPublisherMetrics{},
		gps:       gps,
	}
}

// PublishEnhanced publishes a message using the enhanced publisher
func PublishEnhanced(logger_ctx context.Context, gps *PubSubMessages.GossipPubSub, topic string, message *PubSubMessages.Message, metadata map[string]string) error {
	// Validate input parameters
	if gps == nil {
		return fmt.Errorf("GossipPubSub cannot be nil")
	}
	if message == nil {
		return fmt.Errorf("message cannot be nil")
	}
	if topic == "" {
		return fmt.Errorf("topic cannot be empty")
	}

	// Validate that message has ACK
	if message.GetACK() == nil {
		return fmt.Errorf("message cannot be published without ACK - message type: %T", message)
	}

	// Create message
	messageGossip := &PubSubMessages.GossipMessage{
		ID:        fmt.Sprintf("%s-%d", gps.Host.ID().String(), gps.MessageID),
		Topic:     topic,
		Data:      message,
		Sender:    message.GetSender(),
		Timestamp: time.Now().UTC().Unix(),
		TTL:       30, // Default TTL
		Metadata:  metadata,
	}
	gps.MessageID++

	// Change message type to ProcessMessage type from config.Type_ToBeProcessed if current type is Publish
	if messageGossip.Data.GetACK() != nil && messageGossip.Data.GetACK().GetStage() == config.Type_Publish {
		messageGossip.Data.GetACK().SetStage(config.Type_ToBeProcessed)
	}

	// Serialize message
	messageBytes, err := json.Marshal(messageGossip.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	// Add to message cache to prevent loops
	gps.Mutex.Lock()
	gps.MessageCache[messageGossip.ID] = time.Now()
	gps.Mutex.Unlock()

	// Use enhanced publishing if GossipSub is available
	if gps.GossipSubPS != nil {
		// Get or join the topic — use a separate variable to avoid shadowing
		// the `topic` string parameter above.
		joinedTopic, err := gps.GetOrJoinTopic(topic)
		if err != nil {
			return fmt.Errorf("failed to get or join topic: %w", err)
		}

		enhancedPublisher := NewEnhancedPublisher(topic, joinedTopic, gps)
		return enhancedPublisher.publishWithRetry(context.Background(), messageBytes, 3)
	} else {
		// Fall back to custom gossip
		GossipMessage(gps, messageBytes)
	}

	log.Printf("📤 Published message to topic %s: %s", topic, messageGossip.ID)
	return nil
}

// publishWithRetry publishes a message with retry logic.
// On "topic is closed" errors it evicts the stale handle and re-joins the
// topic immediately — exponential backoff is pointless against a dead handle.
func (ep *EnhancedPublisher) publishWithRetry(ctx context.Context, messageBytes []byte, maxRetries int) error {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ep.topic.Publish(ctx, messageBytes); err != nil {
			lastErr = err
			log.Printf("⚠️ Publish attempt %d/%d failed: %v", attempt+1, maxRetries+1, err)

			// A closed topic will never self-recover — evict and re-join.
			if strings.Contains(err.Error(), "closed") && ep.gps != nil && ep.topicName != "" {
				_ = ep.gps.CloseTopic(ep.topicName) // remove stale handle from cache
				if fresh, joinErr := ep.gps.GetOrJoinTopic(ep.topicName); joinErr == nil {
					ep.topic = fresh
					log.Printf("🔄 Re-joined topic %q after closed error, retrying immediately", ep.topicName)
					continue // retry at once with the live handle
				}
			}

			if attempt < maxRetries {
				// Exponential backoff for transient (non-closed) errors
				backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
					continue
				}
			}
			continue
		}

		// Success - update metrics atomically
		atomic.AddInt64(&ep.metrics.MessagesPublished, 1)
		atomic.AddInt64(&ep.metrics.BytesPublished, int64(len(messageBytes)))
		atomic.StoreInt64(&ep.metrics.lastPublishTime, time.Now().UTC().UnixNano())

		if attempt > 0 {
			log.Printf("✅ Published after %d retries", attempt)
		}
		return nil
	}

	atomic.AddInt64(&ep.metrics.PublishErrors, 1)
	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}
