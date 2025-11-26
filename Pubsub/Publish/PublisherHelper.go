package Publish

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"gossipnode/config"
	AppContext "gossipnode/config/Context"
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
	topic   *pubsub.Topic
	metrics *EnhancedPublisherMetrics
	gps     *PubSubMessages.GossipPubSub
}

// NewEnhancedPublisher creates a new EnhancedPublisher instance
func NewEnhancedPublisher(topic *pubsub.Topic, gps *PubSubMessages.GossipPubSub) *EnhancedPublisher {
	return &EnhancedPublisher{
		topic:   topic,
		metrics: &EnhancedPublisherMetrics{},
		gps:     gps,
	}
}

// PublishEnhanced publishes a message using the enhanced publisher
func PublishEnhanced(gps *PubSubMessages.GossipPubSub, topic string, message *PubSubMessages.Message, metadata map[string]string) error {
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
	gps.MessageCache[messageGossip.ID] = true
	gps.Mutex.Unlock()

	// Use enhanced publishing if GossipSub is available
	if gps.GossipSubPS != nil {
		// Get or join the topic first
		topic, err := gps.GetOrJoinTopic(topic)
		if err != nil {
			return fmt.Errorf("failed to get or join topic: %w", err)
		}

		enhancedPublisher := NewEnhancedPublisher(topic, gps)
		return enhancedPublisher.publishWithRetry(context.Background(), messageBytes, 3)
	} else {
		// Fall back to custom gossip
		GossipMessage(gps, messageBytes)
	}

	log.Printf("📤 Published message to topic %s: %s", topic, messageGossip.ID)
	return nil
}

// publishWithRetry publishes a message with retry logic
func (ep *EnhancedPublisher) publishWithRetry(ctx context.Context, messageBytes []byte, maxRetries int) error {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ep.topic.Publish(ctx, messageBytes); err != nil {
			lastErr = err
			log.Printf("⚠️ Publish attempt %d/%d failed: %v", attempt+1, maxRetries+1, err)

			if attempt < maxRetries {
				// Exponential backoff
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

// PublishBatchEnhanced publishes multiple messages in a batch
func PublishBatchEnhanced(gps *PubSubMessages.GossipPubSub, topicName string, messages []*PubSubMessages.Message, metadata map[string]string) error {
	if gps.GossipSubPS == nil {
		return fmt.Errorf("batch publishing requires GossipSub")
	}

	// Get or join the topic first
	topic, err := gps.GetOrJoinTopic(topicName)
	if err != nil {
		return fmt.Errorf("failed to get or join topic: %w", err)
	}

	enhancedPublisher := NewEnhancedPublisher(topic, gps)
	ctx, _ := AppContext.GetAppContext(PublishAppContext).NewChildContext()
	return enhancedPublisher.publishBatch(ctx, messages, metadata)
}

// publishBatch publishes multiple messages in a batch
func (ep *EnhancedPublisher) publishBatch(ctx context.Context, messages []*PubSubMessages.Message, metadata map[string]string) error {
	var errors []error
	successCount := int64(0)
	totalBytes := int64(0)

	for i, message := range messages {
		// Create message
		messageGossip := &PubSubMessages.GossipMessage{
			ID:        fmt.Sprintf("%s-%d", ep.gps.Host.ID().String(), ep.gps.MessageID),
			Topic:     "", // Will be set by caller
			Data:      message,
			Sender:    message.GetSender(),
			Timestamp: time.Now().UTC().Unix(),
			TTL:       30,
			Metadata:  metadata,
		}
		ep.gps.MessageID++

		// Serialize message
		messageBytes, err := json.Marshal(messageGossip.Data)
		if err != nil {
			errors = append(errors, fmt.Errorf("message[%d] marshal error: %w", i, err))
			continue
		}

		// Publish message
		if err := ep.topic.Publish(ctx, messageBytes); err != nil {
			errors = append(errors, fmt.Errorf("message[%d] publish error: %w", i, err))
			atomic.AddInt64(&ep.metrics.PublishErrors, 1)
		} else {
			successCount++
			totalBytes += int64(len(messageBytes))
		}
	}

	// Update metrics for successful publishes only
	if successCount > 0 {
		atomic.AddInt64(&ep.metrics.MessagesPublished, successCount)
		atomic.AddInt64(&ep.metrics.BytesPublished, totalBytes)
		atomic.StoreInt64(&ep.metrics.lastPublishTime, time.Now().UTC().UnixNano())
	}

	if len(errors) > 0 {
		return fmt.Errorf("batch publish: %d/%d succeeded, %d failed: %v",
			successCount, len(messages), len(errors), errors)
	}

	log.Printf("📤 Published batch of %d messages (%d bytes)", len(messages), totalBytes)
	return nil
}

// GetMetrics returns current publisher metrics (thread-safe)
func (ep *EnhancedPublisher) GetMetrics() EnhancedPublisherMetrics {
	lastPubNano := atomic.LoadInt64(&ep.metrics.lastPublishTime)

	return EnhancedPublisherMetrics{
		MessagesPublished: atomic.LoadInt64(&ep.metrics.MessagesPublished),
		PublishErrors:     atomic.LoadInt64(&ep.metrics.PublishErrors),
		lastPublishTime:   lastPubNano,
		BytesPublished:    atomic.LoadInt64(&ep.metrics.BytesPublished),
	}
}

// GetThroughput calculates messages per second (thread-safe)
func (ep *EnhancedPublisher) GetThroughput(duration time.Duration) float64 {
	messages := atomic.LoadInt64(&ep.metrics.MessagesPublished)
	seconds := duration.Seconds()
	if seconds == 0 {
		return 0
	}
	return float64(messages) / seconds
}
