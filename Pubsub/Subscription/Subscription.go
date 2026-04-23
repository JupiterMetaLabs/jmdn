package Subscription

import (
	"context"
	"time"

	"gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
)

// Subscribe subscribes to a topic with access control (now uses SubscriptionManager)
func Subscribe(logger_ctx context.Context, gps *PubSubMessages.GossipPubSub, topic string, handler func(*PubSubMessages.GossipMessage)) error {
	// Start trace span
	tracer := logger().Tracer("Subscription")
	trace_ctx, span := tracer.Start(logger_ctx, "Subscription.Subscribe")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("topic", topic),
		attribute.String("peer_id", gps.Host.ID().String()),
	)

	logger().Info(trace_ctx, "Subscribing to topic via SubscriptionManager",
		ion.String("topic", topic),
		ion.String("peer_id", gps.Host.ID().String()),
		ion.String("function", "Subscription.Subscribe"))

	// Use centralized SubscriptionManager
	manager := GetSubscriptionManager(gps)
	err := manager.Subscribe(logger_ctx, topic, handler)

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Error(trace_ctx, "Failed to subscribe to topic",
			err,
			ion.String("topic", topic),
			ion.Float64("duration", duration),
			ion.String("function", "Subscription.Subscribe"))
		return err
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().Info(trace_ctx, "Successfully subscribed to topic",
		ion.String("topic", topic),
		ion.Float64("duration", duration),
		ion.String("function", "Subscription.Subscribe"))

	return nil
}

// CanSubscribe checks if a peer can subscribe to a channel
func CanSubscribe(gps *PubSubMessages.GossipPubSub, channelName string, peerID peer.ID) bool {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Subscription")
	trace_ctx, span := tracer.Start(logger_ctx, "Subscription.CanSubscribe")
	defer span.End()

	span.SetAttributes(
		attribute.String("channel", channelName),
		attribute.String("peer_id", peerID.String()),
	)

	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()

	access, exists := gps.ChannelAccess[channelName]
	if !exists {
		span.SetAttributes(attribute.Bool("can_subscribe", false), attribute.String("reason", "channel_not_exists"))
		logger().Info(trace_ctx, "Channel does not exist",
			ion.String("channel", channelName),
			ion.String("function", "Subscription.CanSubscribe"))
		return false // Channel doesn't exist
	}

	// Public channels allow anyone
	if access.IsPublic {
		span.SetAttributes(attribute.Bool("can_subscribe", true), attribute.String("reason", "public_channel"))
		logger().Info(trace_ctx, "Public channel - access granted",
			ion.String("channel", channelName),
			ion.String("function", "Subscription.CanSubscribe"))
		return true
	}

	// Check if peer is in allowed list
	canSubscribe := access.AllowedPeers[peerID]
	if canSubscribe {
		span.SetAttributes(attribute.Bool("can_subscribe", true), attribute.String("reason", "peer_in_allowed_list"))
		logger().Info(trace_ctx, "Peer in allowed list - access granted",
			ion.String("channel", channelName),
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Subscription.CanSubscribe"))
	} else {
		span.SetAttributes(attribute.Bool("can_subscribe", false), attribute.String("reason", "peer_not_in_allowed_list"))
		logger().Info(trace_ctx, "Peer not in allowed list - access denied",
			ion.String("channel", channelName),
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Subscription.CanSubscribe"))
	}
	return canSubscribe
}

// Unsubscribe unsubscribes from a topic (now uses SubscriptionManager)
func Unsubscribe(gps *PubSubMessages.GossipPubSub, topic string) error {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Subscription")
	trace_ctx, span := tracer.Start(logger_ctx, "Subscription.Unsubscribe")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("topic", topic),
		attribute.String("peer_id", gps.Host.ID().String()),
	)

	logger().Info(trace_ctx, "Unsubscribing from topic via SubscriptionManager",
		ion.String("topic", topic),
		ion.String("function", "Subscription.Unsubscribe"))

	// Use centralized SubscriptionManager
	manager := GetSubscriptionManager(gps)
	err := manager.Unsubscribe(topic)

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Error(trace_ctx, "Failed to unsubscribe from topic",
			err,
			ion.String("topic", topic),
			ion.Float64("duration", duration),
			ion.String("function", "Subscription.Unsubscribe"))
		return err
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().Info(trace_ctx, "Successfully unsubscribed from topic",
		ion.String("topic", topic),
		ion.Float64("duration", duration),
		ion.String("function", "Subscription.Unsubscribe"))

	return nil
}
