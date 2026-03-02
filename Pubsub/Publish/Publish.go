package Publish

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"jmdn/config"
	"jmdn/config/GRO"
	"jmdn/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
)

// Publish publishes a message to a topic (now uses enhanced implementation)
func Publish(logger_ctx context.Context, gps *PubSubMessages.GossipPubSub, topic string, message *PubSubMessages.Message, metadata map[string]string) error {
	// Start trace span
	tracer := logger().NamedLogger.Tracer("Publish")
	trace_ctx, span := tracer.Start(logger_ctx, "Publish.Publish")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("topic", topic),
		attribute.String("sender", gps.Host.ID().String()),
	)

	logger().NamedLogger.Info(trace_ctx, "Publishing message to topic",
		ion.String("topic", topic),
		ion.String("sender", gps.Host.ID().String()),
		ion.String("function", "Publish.Publish"))

	// Use enhanced publishing if available, fall back to original implementation
	var err error
	if gps.GossipSubPS != nil {
		err = PublishEnhanced(trace_ctx, gps, topic, message, metadata)
	} else {
		// Fall back to original implementation for custom gossip
		err = publishOriginal(trace_ctx, gps, topic, message, metadata)
	}

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(trace_ctx, "Failed to publish message",
			err,
			ion.String("topic", topic),
			ion.Float64("duration", duration),
			ion.String("function", "Publish.Publish"))
		return err
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(trace_ctx, "Successfully published message to topic",
		ion.String("topic", topic),
		ion.Float64("duration", duration),
		ion.String("function", "Publish.Publish"))

	return nil
}

// publishOriginal is the original publish implementation (renamed for clarity)
func publishOriginal(logger_ctx context.Context, gps *PubSubMessages.GossipPubSub, topic string, message *PubSubMessages.Message, metadata map[string]string) error {
	// Start trace span
	tracer := logger().NamedLogger.Tracer("Publish")
	trace_ctx, span := tracer.Start(logger_ctx, "Publish.publishOriginal")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("topic", topic),
		attribute.String("sender", gps.Host.ID().String()),
	)

	// Validate input parameters
	if gps == nil {
		err := fmt.Errorf("GossipPubSub cannot be nil")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return err
	}
	if message == nil {
		err := fmt.Errorf("message cannot be nil")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return err
	}
	if topic == "" {
		err := fmt.Errorf("topic cannot be empty")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return err
	}

	// Validate that message has ACK
	if message.GetACK() == nil {
		err := fmt.Errorf("message cannot be published without ACK - message type: %T", message)
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return err
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

	span.SetAttributes(
		attribute.String("message_id", messageGossip.ID),
		attribute.Int64("message_timestamp", messageGossip.Timestamp),
	)

	// Need to change the message type to Processmessage type from config.Type_ToBeProcessed if the current type is Publish @config.Type_Publish
	if messageGossip.Data.GetACK() != nil && messageGossip.Data.GetACK().GetStage() == config.Type_Publish {
		messageGossip.Data.GetACK().SetStage(config.Type_ToBeProcessed)
	}

	// Serialize message - for GossipSub, publish only the Data (Message) part
	var messageBytes []byte
	var err error

	if gps.GossipSubPS != nil {
		// For GossipSub, marshal only the Data part
		messageBytes, err = json.Marshal(messageGossip.Data)
	} else {
		// For custom gossip, marshal the entire GossipMessage
		messageBytes, err = json.Marshal(messageGossip)
	}

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "marshal_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(trace_ctx, "Failed to marshal message",
			err,
			ion.String("topic", topic),
			ion.String("message_id", messageGossip.ID),
			ion.String("function", "Publish.publishOriginal"))
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	span.SetAttributes(attribute.Int("message_size_bytes", len(messageBytes)))

	// Add to message cache to prevent loops
	gps.Mutex.Lock()
	gps.MessageCache[messageGossip.ID] = time.Now()
	gps.Mutex.Unlock()

	// Publish using GossipSub if available, otherwise use custom gossip
	if gps.GossipSubPS != nil {
		if err := publishViaGossipSub(gps, topic, messageBytes); err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("fallback", "custom_gossip"))
			logger().NamedLogger.Warn(trace_ctx, "Failed to publish via GossipSub, falling back to custom gossip",
				ion.String("error", err.Error()),
				ion.String("topic", topic),
				ion.String("function", "Publish.publishOriginal"))
			// Fall back to custom gossip on error
			GossipMessage(gps, messageBytes)
		} else {
			span.SetAttributes(attribute.String("method", "gossipsub"))
		}
	} else {
		// Use custom gossip when GossipSub is not available
		span.SetAttributes(attribute.String("method", "custom_gossip"))
		GossipMessage(gps, messageBytes)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(trace_ctx, "Published message to topic",
		ion.String("topic", topic),
		ion.String("message_id", messageGossip.ID),
		ion.Float64("duration", duration),
		ion.String("function", "Publish.publishOriginal"))

	return nil
}

// gossipMessage forwards a message to connected peers
func GossipMessage(gps *PubSubMessages.GossipPubSub, messageBytes []byte) {
	logger_ctx := context.Background()
	tracer := logger().NamedLogger.Tracer("Publish")
	trace_ctx, span := tracer.Start(logger_ctx, "Publish.GossipMessage")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("sender", gps.Host.ID().String()),
		attribute.Int("message_size_bytes", len(messageBytes)),
	)

	if LocalGRO == nil {
		var err error
		LocalGRO, err = GRO.GetApp(GRO.PubsubApp).NewLocalManager(GRO.PubsubPublishLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			logger().NamedLogger.Error(trace_ctx, "Failed to create local manager",
				err,
				ion.String("function", "Publish.GossipMessage"))
			return
		}
	}
	fmt.Printf("=== Publish.GossipMessage CALLED ===\n")
	fmt.Printf("Message Bytes: %s\n", string(messageBytes))
	fmt.Printf("From Peer: %s\n", gps.Host.ID())
	fmt.Printf("Message ID: %d\n", gps.MessageID)
	fmt.Printf("Protocol: %s\n", gps.Protocol)
	fmt.Printf("Message Cache: %v\n", gps.MessageCache)

	// Parse the message to get the topic
	var gossipMsg PubSubMessages.GossipMessage
	if err := json.Unmarshal(messageBytes, &gossipMsg); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "parse_failed"))
		logger().NamedLogger.Error(trace_ctx, "Failed to parse message for topic routing",
			err,
			ion.String("function", "Publish.GossipMessage"))
		return
	}

	topic := gossipMsg.Topic
	span.SetAttributes(attribute.String("topic", topic))

	gps.Mutex.RLock()
	// Get topic subscribers for this topic
	topicSubscribers := gps.TopicSubscribers[topic]
	gps.Mutex.RUnlock()

	span.SetAttributes(attribute.Int("topic_subscribers_count", len(topicSubscribers)))

	// Use topic subscribers if available, otherwise fall back to all connected peers
	connectedPeers := gps.Host.Network().Peers()
	span.SetAttributes(attribute.Int("connected_peers_count", len(connectedPeers)))

	// Build connected peer map once for efficient lookup
	connectedPeerMap := make(map[peer.ID]bool)
	for _, cp := range connectedPeers {
		connectedPeerMap[cp] = true
	}

	// Determine which peers to send to
	peersToSend := make([]peer.ID, 0)
	if len(topicSubscribers) > 0 {
		// Send to topic subscribers
		for peerID := range topicSubscribers {
			if peerID != gps.Host.ID() {
				// Check if peer is actually connected
				if connectedPeerMap[peerID] {
					peersToSend = append(peersToSend, peerID)
				}
			}
		}
		span.SetAttributes(attribute.String("routing_method", "topic_based"))
		logger().NamedLogger.Info(trace_ctx, "Using topic-based routing",
			ion.Int("subscribers", len(peersToSend)),
			ion.String("function", "Publish.GossipMessage"))
	} else {
		// Fall back to all connected peers
		for _, peerID := range connectedPeers {
			if peerID != gps.Host.ID() {
				peersToSend = append(peersToSend, peerID)
			}
		}
		span.SetAttributes(attribute.String("routing_method", "broadcast"))
		logger().NamedLogger.Info(trace_ctx, "Using broadcast fallback",
			ion.Int("peers", len(peersToSend)),
			ion.String("function", "Publish.GossipMessage"))
	}

	span.SetAttributes(attribute.Int("peers_to_send", len(peersToSend)))

	// Send to filtered peers
	for _, peerID := range peersToSend {
		// Capture peerID in closure to avoid race condition
		peerID := peerID
		if err := LocalGRO.Go(GRO.PubsubPublishThread, func(ctx context.Context) error {
			peerSpanCtx, peerSpan := tracer.Start(trace_ctx, "Publish.GossipMessage.sendToPeer")
			peerSpan.SetAttributes(attribute.String("peer", peerID.String()))
			defer peerSpan.End()

			if err := sendToPeer(gps, peerID, messageBytes); err != nil {
				peerSpan.RecordError(err)
				peerSpan.SetAttributes(attribute.String("status", "send_failed"))
				logger().NamedLogger.Error(peerSpanCtx, "Failed to gossip message to peer",
					err,
					ion.String("peer", peerID.String()),
					ion.String("topic", topic),
					ion.String("function", "Publish.GossipMessage"))
			} else {
				peerSpan.SetAttributes(attribute.String("status", "success"))
				logger().NamedLogger.Info(peerSpanCtx, "Sent message to peer",
					ion.String("peer", peerID.String()),
					ion.String("topic", topic),
					ion.String("function", "Publish.GossipMessage"))
			}
			return nil
		}); err != nil {
			span.RecordError(err)
			logger().NamedLogger.Error(trace_ctx, "Failed to start goroutine for peer",
				err,
				ion.String("peer", peerID.String()),
				ion.String("function", "Publish.GossipMessage"))
		}
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(trace_ctx, "Gossip message completed",
		ion.Int("peers_sent", len(peersToSend)),
		ion.Float64("duration", duration),
		ion.String("function", "Publish.GossipMessage"))
}

// sendToPeer sends a message to a specific peer
func sendToPeer(gps *PubSubMessages.GossipPubSub, peerID peer.ID, messageBytes []byte) error {
	logger_ctx := context.Background()
	tracer := logger().NamedLogger.Tracer("Publish")
	trace_ctx, span := tracer.Start(logger_ctx, "Publish.sendToPeer")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("peer", peerID.String()),
		attribute.String("protocol", string(gps.Protocol)),
		attribute.Int("message_size_bytes", len(messageBytes)),
	)

	stream, err := gps.Host.NewStream(trace_ctx, peerID, gps.Protocol)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "stream_creation_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(trace_ctx, "Failed to create stream to peer",
			err,
			ion.String("peer", peerID.String()),
			ion.String("function", "Publish.sendToPeer"))
		return err
	}
	defer stream.Close()

	err = writeMessage(stream, messageBytes)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "write_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(trace_ctx, "Failed to write message to peer",
			err,
			ion.String("peer", peerID.String()),
			ion.String("function", "Publish.sendToPeer"))
		return err
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(trace_ctx, "Successfully sent message to peer",
		ion.String("peer", peerID.String()),
		ion.Float64("duration", duration),
		ion.String("function", "Publish.sendToPeer"))
	return nil
}

// writeMessage writes a message to the stream using delimiter
func writeMessage(stream network.Stream, message []byte) error {
	_, err := stream.Write(append(message, config.Delimiter))
	return err
}

// publishViaGossipSub publishes a message using libp2p GossipSub
func publishViaGossipSub(gps *PubSubMessages.GossipPubSub, topicName string, messageBytes []byte) error {
	logger_ctx := context.Background()
	tracer := logger().NamedLogger.Tracer("Publish")
	trace_ctx, span := tracer.Start(logger_ctx, "Publish.publishViaGossipSub")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("topic", topicName),
		attribute.Int("message_size_bytes", len(messageBytes)),
	)

	// Get or join the topic
	topic, err := gps.GetOrJoinTopic(topicName)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "topic_join_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(trace_ctx, "Failed to get or join topic",
			err,
			ion.String("topic", topicName),
			ion.String("function", "Publish.publishViaGossipSub"))
		return fmt.Errorf("failed to get or join topic %s: %w", topicName, err)
	}

	// Publish the message
	err = topic.Publish(trace_ctx, messageBytes)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "publish_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(trace_ctx, "Failed to publish message to topic",
			err,
			ion.String("topic", topicName),
			ion.String("function", "Publish.publishViaGossipSub"))
		return fmt.Errorf("failed to publish message to topic %s: %w", topicName, err)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(trace_ctx, "Published via GossipSub to topic",
		ion.String("topic", topicName),
		ion.Float64("duration", duration),
		ion.String("function", "Publish.publishViaGossipSub"))
	return nil
}
