package MessagePassing

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	Router "jmdn/Pubsub/Router"

	"time"

	"jmdn/config"
	AVCStruct "jmdn/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"go.opentelemetry.io/otel/attribute"
)

const (
	LOG_FILE = ""
	TOPIC    = "MessagePassing"
)

// < -- CRDT Utils -- >
type StructBuddyNode struct {
	BuddyNode     *AVCStruct.BuddyNode
	logger_ctx    context.Context
	logger_cancel context.CancelFunc
}

// Abstraction function to start the handleBuddynode message stream
func HandleBuddyNodeStream(host host.Host, s network.Stream) {
	NewStructBuddyNode(AVCStruct.PubSub_BuddyNode).HandleBuddyNodesMessageStream(host, s)
}

func NewStructBuddyNode(buddy *AVCStruct.BuddyNode) *StructBuddyNode {
	if buddy == nil {
		return &StructBuddyNode{
			BuddyNode: &AVCStruct.BuddyNode{},
		}
	}
	logger_ctx, logger_cancel := context.WithCancel(context.Background())
	return &StructBuddyNode{
		BuddyNode:     buddy,
		logger_ctx:    logger_ctx,
		logger_cancel: logger_cancel,
	}
}

// HandleBuddyNodesMessageStream handles incoming messages on the buddy nodes protocol
func (StructBuddyNode *StructBuddyNode) HandleBuddyNodesMessageStream(host host.Host, s network.Stream) {
	defer s.Close()

	// Create context for this stream handler
	spanCtx := StructBuddyNode.logger_ctx
	if spanCtx == nil {
		spanCtx = context.Background()
	}

	// Record trace span and close it
	streamSpanCtx, streamSpan := logger().NamedLogger.Tracer("MessagePassing").Start(spanCtx, "MessagePassing.HandleBuddyNodesMessageStream")
	defer streamSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	streamSpan.SetAttributes(
		attribute.String("remote_peer_id", remotePeer.String()),
		attribute.String("local_peer_id", host.ID().String()),
	)

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		streamSpan.RecordError(err)
		streamSpan.SetAttributes(attribute.String("status", "read_error"))
		duration := time.Since(startTime).Seconds()
		streamSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(streamSpanCtx, "Error reading message from peer",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleBuddyNodesMessageStream"),
		)
		return
	}

	streamSpan.SetAttributes(attribute.String("message_received", "true"), attribute.Int("message_length", len(msg)))

	message := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(msg)

	logger().NamedLogger.Info(streamSpanCtx, "Received buddy message from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", msg),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleBuddyNodesMessageStream"),
	)

	// Check if message was successfully parsed
	if message == nil {
		streamSpan.RecordError(errors.New("failed to parse message"))
		streamSpan.SetAttributes(attribute.String("status", "parse_failed"))
		duration := time.Since(startTime).Seconds()
		streamSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(streamSpanCtx, "Failed to parse message - malformed JSON or invalid structure",
			errors.New("failed to parse message"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("raw_message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleBuddyNodesMessageStream"),
		)
		return
	}

	// Handle special cases that need direct response
	// Check if ACK is not nil before accessing it
	if message.GetACK() == nil {
		streamSpan.RecordError(errors.New("received message with nil ACK"))
		streamSpan.SetAttributes(attribute.String("status", "nil_ack"))
		duration := time.Since(startTime).Seconds()
		streamSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(streamSpanCtx, "Received message with nil ACK",
			errors.New("received message with nil ACK"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("raw_message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleBuddyNodesMessageStream"),
		)
		return
	}

	ackStage := message.GetACK().GetStage()
	streamSpan.SetAttributes(attribute.String("ack_stage", ackStage))

	switch ackStage {
	case config.Type_StartPubSub:
		StructBuddyNode.handleStartPubSub(streamSpanCtx, s)
		duration := time.Since(startTime).Seconds()
		streamSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "handled_start_pubsub"))
		return
	case config.Type_SubscriptionResponse:
		StructBuddyNode.handleSubscriptionResponse(streamSpanCtx, s, message)
		duration := time.Since(startTime).Seconds()
		streamSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "handled_subscription_response"))
		return
	}

	// For all other message types, delegate to the service layer via Router
	// Convert Message to GossipMessage for Router
	gossipMessage := AVCStruct.NewGossipMessageBuilder(nil).SetMesssage(message)
	if err := Router.Router(gossipMessage); err != nil {
		streamSpan.RecordError(err)
		streamSpan.SetAttributes(attribute.String("status", "router_failed"))
		logger().NamedLogger.Error(streamSpanCtx, "Failed to handle message via service layer",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", msg),
			ion.String("ack_stage", ackStage),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleBuddyNodesMessageStream"),
		)

		// Send ACK_FALSE response for service layer failures
		StructBuddyNode.sendACKResponse(streamSpanCtx, s, false, ackStage)
		duration := time.Since(startTime).Seconds()
		streamSpan.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	// Send ACK_TRUE response for successful service layer processing
	StructBuddyNode.sendACKResponse(streamSpanCtx, s, true, ackStage)
	duration := time.Since(startTime).Seconds()
	streamSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// handleStartPubSub handles the StartPubSub message type with direct logic
func (StructBuddyNode *StructBuddyNode) handleStartPubSub(spanCtx context.Context, s network.Stream) {
	// Record trace span and close it
	startPubSubSpanCtx, startPubSubSpan := logger().NamedLogger.Tracer("MessagePassing").Start(spanCtx, "MessagePassing.handleStartPubSub")
	defer startPubSubSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	startPubSubSpan.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	// If node is okay to listen for subscriptions, then return ACK True
	if AVCStruct.PubSub_BuddyNode != nil && AVCStruct.PubSub_BuddyNode.Host != nil && AVCStruct.PubSub_BuddyNode.Network != nil {
		// Node is ready to listen for subscriptions
		startPubSubSpan.SetAttributes(attribute.String("node_status", "ready"))
		ackBuilder := AVCStruct.NewACKBuilder().True_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, config.Type_StartPubSub)
		message := AVCStruct.NewMessageBuilder(nil).
			SetSender(AVCStruct.PubSub_BuddyNode.PeerID).
			SetMessage("ACK_TRUE for StartPubSub").
			SetTimestamp(time.Now().UTC().Unix()).
			SetACK(ackBuilder)

		// Marshal the message to JSON
		messageBytes, err := json.Marshal(message)
		if err != nil {
			startPubSubSpan.RecordError(err)
			startPubSubSpan.SetAttributes(attribute.String("status", "marshal_failed"))
			duration := time.Since(startTime).Seconds()
			startPubSubSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(startPubSubSpanCtx, "Failed to marshal ACK message",
				err,
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("ack_type", "ACK_TRUE"),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleStartPubSub"),
			)
			return
		}

		if err := StructBuddyNode.SendMessageToPeer(startPubSubSpanCtx, remotePeer, string(messageBytes)); err != nil {
			startPubSubSpan.RecordError(err)
			startPubSubSpan.SetAttributes(attribute.String("status", "send_failed"))
			duration := time.Since(startTime).Seconds()
			startPubSubSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(startPubSubSpanCtx, "Failed to send ACK to peer",
				err,
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("ack_type", "ACK_TRUE"),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleStartPubSub"),
			)
		} else {
			duration := time.Since(startTime).Seconds()
			startPubSubSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"), attribute.String("ack_sent", "ACK_TRUE"))
			logger().NamedLogger.Info(startPubSubSpanCtx, "Sent ACK_TRUE to peer for pubsub subscription",
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("ack_type", "ACK_TRUE"),
				ion.Float64("duration", duration),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleStartPubSub"),
			)
		}
	} else {
		// Node is not ready to listen for subscriptions
		startPubSubSpan.SetAttributes(attribute.String("node_status", "not_ready"))
		ackBuilder := AVCStruct.NewACKBuilder().False_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, config.Type_StartPubSub)
		message := AVCStruct.NewMessageBuilder(nil).
			SetSender(AVCStruct.PubSub_BuddyNode.PeerID).
			SetMessage("ACK_FALSE for StartPubSub").
			SetTimestamp(time.Now().UTC().Unix()).
			SetACK(ackBuilder)

		// Marshal the message to JSON
		messageBytes, err := json.Marshal(message)
		if err != nil {
			startPubSubSpan.RecordError(err)
			startPubSubSpan.SetAttributes(attribute.String("status", "marshal_failed"))
			duration := time.Since(startTime).Seconds()
			startPubSubSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(startPubSubSpanCtx, "Failed to marshal ACK message",
				err,
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("ack_type", "ACK_FALSE"),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleStartPubSub"),
			)
			return
		}

		if err := StructBuddyNode.SendMessageToPeer(startPubSubSpanCtx, remotePeer, string(messageBytes)); err != nil {
			startPubSubSpan.RecordError(err)
			startPubSubSpan.SetAttributes(attribute.String("status", "send_failed"))
			duration := time.Since(startTime).Seconds()
			startPubSubSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(startPubSubSpanCtx, "Failed to send ACK to peer",
				err,
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("ack_type", "ACK_FALSE"),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleStartPubSub"),
			)
		} else {
			duration := time.Since(startTime).Seconds()
			startPubSubSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"), attribute.String("ack_sent", "ACK_FALSE"))
			logger().NamedLogger.Info(startPubSubSpanCtx, "Sent ACK_FALSE to peer - node not ready for pubsub",
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("ack_type", "ACK_FALSE"),
				ion.Float64("duration", duration),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleStartPubSub"),
			)
		}
	}
}

// handleSubscriptionResponse handles subscription response messages
func (StructBuddyNode *StructBuddyNode) handleSubscriptionResponse(spanCtx context.Context, s network.Stream, message *AVCStruct.Message) {
	// Record trace span and close it
	subResponseSpanCtx, subResponseSpan := logger().NamedLogger.Tracer("MessagePassing").Start(spanCtx, "MessagePassing.handleSubscriptionResponse")
	defer subResponseSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	subResponseSpan.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	if message.ACK != nil {
		ackStatus := message.ACK.Status
		subResponseSpan.SetAttributes(attribute.String("ack_status", ackStatus))
		switch ackStatus {
		case config.Type_ACK_True:
			subResponseSpan.SetAttributes(attribute.String("status", "ack_true"))
			if StructBuddyNode.BuddyNode.ResponseHandler != nil {
				StructBuddyNode.BuddyNode.ResponseHandler.HandleResponse(remotePeer, true, "main")
				logger().NamedLogger.Info(subResponseSpanCtx, "Handled ACK_TRUE subscription response",
					ion.String("remote_peer_id", remotePeer.String()),
					ion.String("ack_status", "ACK_TRUE"),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "MessagePassing.handleSubscriptionResponse"),
				)
			}
		case config.Type_ACK_False:
			subResponseSpan.SetAttributes(attribute.String("status", "ack_false"))
			if StructBuddyNode.BuddyNode.ResponseHandler != nil {
				StructBuddyNode.BuddyNode.ResponseHandler.HandleResponse(remotePeer, false, "main")
				logger().NamedLogger.Info(subResponseSpanCtx, "Handled ACK_FALSE subscription response",
					ion.String("remote_peer_id", remotePeer.String()),
					ion.String("ack_status", "ACK_FALSE"),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "MessagePassing.handleSubscriptionResponse"),
				)
			}
		default:
			subResponseSpan.RecordError(errors.New("unknown status in ACK_Message"))
			subResponseSpan.SetAttributes(attribute.String("status", "unknown_ack_status"))
			duration := time.Since(startTime).Seconds()
			subResponseSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(subResponseSpanCtx, "Unknown status in ACK_Message",
				errors.New("unknown status in ACK_Message"),
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("ack_status", ackStatus),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleSubscriptionResponse"),
			)
		}
	} else {
		subResponseSpan.RecordError(errors.New("unknown message type received"))
		subResponseSpan.SetAttributes(attribute.String("status", "nil_ack"))
		duration := time.Since(startTime).Seconds()
		subResponseSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(subResponseSpanCtx, "Unknown message type received from peer",
			errors.New("unknown message type received"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubscriptionResponse"),
		)
	}
}

// sendACKResponse sends ACK response based on success/failure
func (StructBuddyNode *StructBuddyNode) sendACKResponse(spanCtx context.Context, s network.Stream, success bool, stage string) {
	// Record trace span and close it
	ackResponseSpanCtx, ackResponseSpan := logger().NamedLogger.Tracer("MessagePassing").Start(spanCtx, "MessagePassing.sendACKResponse")
	defer ackResponseSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	ackResponseSpan.SetAttributes(
		attribute.String("remote_peer_id", remotePeer.String()),
		attribute.Bool("success", success),
		attribute.String("stage", stage),
	)

	var ackBuilder *AVCStruct.ACK
	var ackType string
	if success {
		ackBuilder = AVCStruct.NewACKBuilder().True_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, stage)
		ackType = "ACK_TRUE"
	} else {
		ackBuilder = AVCStruct.NewACKBuilder().False_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, stage)
		ackType = "ACK_FALSE"
	}

	ackResponseSpan.SetAttributes(attribute.String("ack_type", ackType))

	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(AVCStruct.PubSub_BuddyNode.PeerID).
		SetMessage(fmt.Sprintf("ACK response for %s", stage)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackBuilder)

	// Marshal the message to JSON
	messageBytes, err := json.Marshal(message)
	if err != nil {
		ackResponseSpan.RecordError(err)
		ackResponseSpan.SetAttributes(attribute.String("status", "marshal_failed"))
		duration := time.Since(startTime).Seconds()
		ackResponseSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(ackResponseSpanCtx, "Failed to marshal ACK response message",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("ack_type", ackType),
			ion.String("stage", stage),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendACKResponse"),
		)
		return
	}

	if err := StructBuddyNode.SendMessageToPeer(ackResponseSpanCtx, remotePeer, string(messageBytes)); err != nil {
		ackResponseSpan.RecordError(err)
		ackResponseSpan.SetAttributes(attribute.String("status", "send_failed"))
		duration := time.Since(startTime).Seconds()
		ackResponseSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(ackResponseSpanCtx, "Failed to send ACK response to peer",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("ack_type", ackType),
			ion.String("stage", stage),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendACKResponse"),
		)
	} else {
		duration := time.Since(startTime).Seconds()
		ackResponseSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
		logger().NamedLogger.Info(ackResponseSpanCtx, "Sent ACK response to peer",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("ack_type", ackType),
			ion.String("stage", stage),
			ion.Float64("duration", duration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendACKResponse"),
		)
	}
}
