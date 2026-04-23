package Subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"go.opentelemetry.io/otel/attribute"
)

// ManagedSubscription represents a subscription managed by the SubscriptionManager
type ManagedSubscription struct {
	topic       string
	pubsubTopic *pubsub.Topic // Track the topic for cleanup
	sub         *pubsub.Subscription
	cancel      context.CancelFunc
	handlers    []func(*PubSubMessages.GossipMessage) // Support multiple handlers
	refCount    int                                   // Track how many times this topic was subscribed
	createdAt   time.Time
}

// SubscriptionManager manages all active subscriptions globally
type SubscriptionManager struct {
	subscriptions map[string]*ManagedSubscription
	mutex         sync.RWMutex
	gps           *PubSubMessages.GossipPubSub
}

// Global singleton instance
var (
	globalSubManager      *SubscriptionManager
	globalSubManagerOnce  sync.Once
	globalSubManagerMutex sync.Mutex
)

// GetSubscriptionManager returns the global subscription manager singleton
func GetSubscriptionManager(gps *PubSubMessages.GossipPubSub) *SubscriptionManager {
	globalSubManagerOnce.Do(func() {
		globalSubManager = &SubscriptionManager{
			subscriptions: make(map[string]*ManagedSubscription),
			gps:           gps,
		}
		fmt.Println("🎯 Initialized global SubscriptionManager (singleton)")
	})

	// Update gps reference if needed (in case it changes)
	globalSubManagerMutex.Lock()
	if globalSubManager.gps != gps {
		globalSubManager.gps = gps
	}
	globalSubManagerMutex.Unlock()

	return globalSubManager
}

// Subscribe subscribes to a topic with duplicate prevention
func (sm *SubscriptionManager) Subscribe(logger_ctx context.Context, topic string, handler func(*PubSubMessages.GossipMessage)) error {
	tracer := logger().NamedLogger.Tracer("Subscription")
	trace_ctx, span := tracer.Start(logger_ctx, "SubscriptionManager.Subscribe")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("topic", topic),
		attribute.String("peer_id", sm.gps.Host.ID().String()),
	)

	logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Subscribe request",
		ion.String("topic", topic),
		ion.String("function", "SubscriptionManager.Subscribe"))

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Check if subscription already exists
	if existing, exists := sm.subscriptions[topic]; exists {
		existing.refCount++
		// Add the new handler to the list
		if handler != nil {
			existing.handlers = append(existing.handlers, handler)
		}

		span.SetAttributes(
			attribute.Bool("already_subscribed", true),
			attribute.Int("ref_count", existing.refCount),
			attribute.Int("handlers_count", len(existing.handlers)),
		)
		logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Topic already subscribed, added handler",
			ion.String("topic", topic),
			ion.Int("ref_count", existing.refCount),
			ion.Int("handlers_count", len(existing.handlers)),
			ion.String("function", "SubscriptionManager.Subscribe"))

		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "reused_added_handler"))
		return nil
	}

	// Check access control
	if !CanSubscribe(sm.gps, topic, sm.gps.Host.ID()) {
		err := fmt.Errorf("access denied: not authorized to subscribe to channel %s", topic)
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "access_denied"))
		logger().NamedLogger.Error(trace_ctx, "SubscriptionManager: Access denied",
			err,
			ion.String("topic", topic),
			ion.String("function", "SubscriptionManager.Subscribe"))
		return err
	}

	// Create new subscription
	span.SetAttributes(attribute.Bool("creating_new_subscription", true))

	// Initialize LocalGRO if needed
	if LocalGRO == nil {
		if app := GRO.GetApp(GRO.PubsubApp); app != nil {
			lm, err := app.NewLocalManager(GRO.PubsubSubscribeLocal)
			if err == nil {
				LocalGRO = lm
			}
		}
	}

	// Get or join the topic
	pubsubTopic, err := sm.gps.GetOrJoinTopic(topic)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "topic_join_failed"))
		logger().NamedLogger.Error(trace_ctx, "SubscriptionManager: Failed to get or join topic",
			err,
			ion.String("topic", topic),
			ion.String("function", "SubscriptionManager.Subscribe"))
		return fmt.Errorf("failed to get or join topic %s: %w", topic, err)
	}

	// Subscribe to the topic
	sub, err := pubsubTopic.Subscribe()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "subscribe_failed"))
		logger().NamedLogger.Error(trace_ctx, "SubscriptionManager: Failed to subscribe to topic",
			err,
			ion.String("topic", topic),
			ion.String("function", "SubscriptionManager.Subscribe"))
		return fmt.Errorf("failed to subscribe to topic %s: %w", topic, err)
	}

	// Create context for this subscription
	subCtx, cancel := context.WithCancel(context.Background())

	// Init handlers slice
	handlers := make([]func(*PubSubMessages.GossipMessage), 0)
	if handler != nil {
		handlers = append(handlers, handler)
	}

	// Create managed subscription
	managed := &ManagedSubscription{
		topic:       topic,
		pubsubTopic: pubsubTopic, // Store topic reference for cleanup
		sub:         sub,
		cancel:      cancel,
		handlers:    handlers,
		refCount:    1,
		createdAt:   time.Now().UTC(),
	}

	// Store in map
	sm.subscriptions[topic] = managed

	// Start goroutine to handle messages
	run := func(ctx context.Context) error {
		ctxNext, cancelNext := context.WithCancel(ctx)
		defer cancelNext()

		// Monitor both contexts without nested goroutine
		go func() {
			defer cancelNext()
			select {
			case <-subCtx.Done():
				return
			case <-ctx.Done():
				return
			}
		}()

		messageCount := 0
		for {
			msg, err := sub.Next(ctxNext)
			if err != nil {
				if ctxNext.Err() != nil || err == context.Canceled || err == context.DeadlineExceeded {
					logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Subscription cancelled",
						ion.String("topic", topic),
						ion.Int("messages_processed", messageCount),
						ion.String("function", "SubscriptionManager.Subscribe"))
					return nil
				}
				logger().NamedLogger.Error(trace_ctx, "SubscriptionManager: Error reading message",
					err,
					ion.String("topic", topic),
					ion.Int("messages_processed", messageCount),
					ion.String("function", "SubscriptionManager.Subscribe"))
				return err
			}

			// Parse the actual message data from raw bytes
			var messageData PubSubMessages.Message
			if err := json.Unmarshal(msg.Data, &messageData); err != nil {
				logger().NamedLogger.Warn(trace_ctx, "SubscriptionManager: Failed to unmarshal message data, skipping",
					ion.String("error", err.Error()),
					ion.String("topic", topic),
					ion.Int("message_size_bytes", len(msg.Data)),
					ion.String("function", "SubscriptionManager.Subscribe"))
				// Continue to next message
				continue
			}

			// Attach ACK if missing
			if messageData.ACK == nil {
				// Create a default ACK with Type_Publish stage
				ack := PubSubMessages.NewACKBuilder().
					True_ACK_Message(msg.GetFrom(), config.Type_Publish)

				messageData.SetACK(ack)
				logger().NamedLogger.Debug(trace_ctx, "SubscriptionManager: Attached default ACK to message",
					ion.String("topic", topic),
					ion.String("sender", msg.GetFrom().String()),
					ion.String("function", "SubscriptionManager.Subscribe"))
			}

			// Convert to GossipMessage format
			gossipMsg := &PubSubMessages.GossipMessage{
				ID:        msg.ID,
				Topic:     topic,
				Data:      &messageData,
				Sender:    msg.GetFrom(),
				Timestamp: time.Now().UTC().UnixNano(),
				TTL:       0,
			}

			messageCount++

			// Call ALL registered handlers
			// We need to lock while reading handlers because they can be appended/removed safely
			// Actually subscription manager calls are mutex protected, but 'run' is concurrent
			// We should probably protect 'managed.handlers' access.
			// Ideally we use a RWMutex for handlers list specifically or just use the manager mutex?
			// Using manager mutex here might be heavy - let's trust that append/slice changes happen
			// in Subscribe/Unsubscribe which are locked, but reading here is not locked.
			// To be safe, let's grab a copy of handlers under lock.
			sm.mutex.RLock()
			currentHandlers := make([]func(*PubSubMessages.GossipMessage), len(managed.handlers))
			copy(currentHandlers, managed.handlers)
			sm.mutex.RUnlock()

			for _, h := range currentHandlers {
				if h != nil {
					h(gossipMsg)
				}
			}
		}
	}

	if LocalGRO != nil {
		LocalGRO.Go(GRO.PubsubSubscriptionThread, run)
		span.SetAttributes(attribute.Bool("using_gro", true))
	} else {
		go func() { _ = run(subCtx) }()
		span.SetAttributes(attribute.Bool("using_gro", false))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("total_subscriptions", len(sm.subscriptions)),
	)

	logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Successfully subscribed to topic",
		ion.String("topic", topic),
		ion.Int("total_subscriptions", len(sm.subscriptions)),
		ion.Float64("duration", duration),
		ion.String("function", "SubscriptionManager.Subscribe"))

	return nil
}

// Unsubscribe unsubscribes from a topic with reference counting
func (sm *SubscriptionManager) Unsubscribe(topic string) error {
	logger_ctx := context.Background()
	tracer := logger().NamedLogger.Tracer("Subscription")
	trace_ctx, span := tracer.Start(logger_ctx, "SubscriptionManager.Unsubscribe")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("topic", topic))

	logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Unsubscribe request",
		ion.String("topic", topic),
		ion.String("function", "SubscriptionManager.Unsubscribe"))

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	managed, exists := sm.subscriptions[topic]
	if !exists {
		span.SetAttributes(attribute.String("status", "not_found"))
		logger().NamedLogger.Warn(trace_ctx, "SubscriptionManager: Topic not found",
			ion.String("topic", topic),
			ion.String("function", "SubscriptionManager.Unsubscribe"))
		return fmt.Errorf("topic %s not subscribed", topic)
	}

	// Decrement reference count
	managed.refCount--

	// Remove the last handler if any exist
	if len(managed.handlers) > 0 {
		managed.handlers = managed.handlers[:len(managed.handlers)-1]
	}

	span.SetAttributes(attribute.Int("ref_count_after", managed.refCount))

	if managed.refCount > 0 {
		// Still have references, keep subscription active
		logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Decremented refCount, keeping subscription active",
			ion.String("topic", topic),
			ion.Int("ref_count", managed.refCount),
			ion.Int("handlers_remaining", len(managed.handlers)),
			ion.String("function", "SubscriptionManager.Unsubscribe"))

		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "ref_count_decremented"))
		return nil
	}

	// No more references, clean up
	if managed.sub != nil {
		managed.sub.Cancel()
	}
	if managed.cancel != nil {
		managed.cancel()
	}
	// Close the topic to free resources
	if managed.pubsubTopic != nil {
		if err := managed.pubsubTopic.Close(); err != nil {
			logger().NamedLogger.Warn(trace_ctx, "SubscriptionManager: Failed to close topic",
				ion.String("topic", topic),
				ion.String("error", err.Error()),
				ion.String("function", "SubscriptionManager.Unsubscribe"))
		}
	}
	delete(sm.subscriptions, topic)

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "cleaned_up"),
		attribute.Int("total_subscriptions", len(sm.subscriptions)),
	)

	logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Successfully unsubscribed and cleaned up",
		ion.String("topic", topic),
		ion.Int("total_subscriptions", len(sm.subscriptions)),
		ion.Float64("duration", duration),
		ion.String("function", "SubscriptionManager.Unsubscribe"))

	return nil
}

// Shutdown cancels all active subscriptions
func (sm *SubscriptionManager) Shutdown() {
	logger_ctx := context.Background()
	tracer := logger().NamedLogger.Tracer("Subscription")
	trace_ctx, span := tracer.Start(logger_ctx, "SubscriptionManager.Shutdown")
	defer span.End()

	logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Shutting down all subscriptions",
		ion.Int("total_subscriptions", len(sm.subscriptions)),
		ion.String("function", "SubscriptionManager.Shutdown"))

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	for topic, managed := range sm.subscriptions {
		if managed.sub != nil {
			managed.sub.Cancel()
		}
		if managed.cancel != nil {
			managed.cancel()
		}
		// Close the topic to free resources
		if managed.pubsubTopic != nil {
			if err := managed.pubsubTopic.Close(); err != nil {
				logger().NamedLogger.Warn(trace_ctx, "SubscriptionManager: Failed to close topic during shutdown",
					ion.String("topic", topic),
					ion.String("error", err.Error()),
					ion.String("function", "SubscriptionManager.Shutdown"))
			}
		}
		logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Cancelled subscription and closed topic",
			ion.String("topic", topic),
			ion.String("function", "SubscriptionManager.Shutdown"))
	}

	sm.subscriptions = make(map[string]*ManagedSubscription)

	// Shutdown the GossipPubSub instance to cleanup remaining resources
	if sm.gps != nil {
		if err := sm.gps.Shutdown(context.Background()); err != nil {
			logger().NamedLogger.Warn(trace_ctx, "SubscriptionManager: Failed to shutdown GossipPubSub",
				ion.String("error", err.Error()),
				ion.String("function", "SubscriptionManager.Shutdown"))
		}
	}

	logger().NamedLogger.Info(trace_ctx, "SubscriptionManager: Shutdown complete",
		ion.String("function", "SubscriptionManager.Shutdown"))
}

// GetStats returns statistics about active subscriptions
func (sm *SubscriptionManager) GetStats() map[string]interface{} {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	topics := make([]string, 0, len(sm.subscriptions))
	totalRefCount := 0

	for topic, managed := range sm.subscriptions {
		topics = append(topics, topic)
		totalRefCount += managed.refCount
	}

	return map[string]interface{}{
		"total_subscriptions": len(sm.subscriptions),
		"total_ref_count":     totalRefCount,
		"topics":              topics,
	}
}
