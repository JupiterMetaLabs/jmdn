package MessagePassing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"jmdn/config"
	AVCStruct "jmdn/config/PubSubMessages"
	"jmdn/config/settings"
	"jmdn/seednode"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"go.opentelemetry.io/otel/attribute"
)

type StructListener struct {
	ListenerBuddyNode *AVCStruct.BuddyNode
	ResponseHandler   AVCStruct.ResponseHandler
}

func NewListenerStruct(listner *AVCStruct.BuddyNode) *StructListener {
	return &StructListener{
		ListenerBuddyNode: listner,
		ResponseHandler:   nil, // Will be set by NewListenerNode
	}
}

func (StructListenerNode *StructListener) HandleSubmitMessageStream(logger_ctx context.Context, s network.Stream) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.HandleSubmitMessageStream")
	defer span.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	span.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))
	// Ensure stream is closed to prevent resource leaks
	defer s.Close()

	logger().NamedLogger.Info(spanCtx, "StructListener.HandleSubmitMessageStream called",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "read_error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Error reading message from peer",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		return
	}

	span.SetAttributes(attribute.String("message_received", "true"), attribute.Int("message_length", len(msg)))

	logger().NamedLogger.Info(spanCtx, "Raw message received",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", msg),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

	message := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(msg)

	logger().NamedLogger.Info(spanCtx, "Received submit message from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", msg),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

	// Check if message was successfully parsed
	if message == nil {
		span.RecordError(fmt.Errorf("failed to parse message"))
		span.SetAttributes(attribute.String("status", "parse_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to parse message - malformed JSON or invalid structure",
			fmt.Errorf("failed to parse message"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("raw_message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		return
	}

	// Check if ACK is not nil before accessing it
	if message.GetACK() == nil {
		span.RecordError(fmt.Errorf("received message with nil ACK"))
		span.SetAttributes(attribute.String("status", "nil_ack"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Received message with nil ACK",
			fmt.Errorf("received message with nil ACK"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("raw_message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		return
	}

	ackStage := message.GetACK().GetStage()
	span.SetAttributes(attribute.String("ack_stage", ackStage))

	logger().NamedLogger.Info(spanCtx, "ACK Stage",
		ion.String("ack_stage", ackStage),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

	switch ackStage {
	case config.Type_SubmitVote:
		logger().NamedLogger.Info(spanCtx, "Handling Type_SubmitVote",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", message.Message),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

		// Delegate to ListenerHandler for processing the vote
		listenerHandler := NewListenerHandler(StructListenerNode.ResponseHandler)
		listenerHandler.handleSubmitVote(spanCtx, s, message)
	case config.Type_AskForSubscription:
		logger().NamedLogger.Info(spanCtx, "Handling Type_AskForSubscription",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", message.Message),
			ion.String("ack_stage", message.GetACK().GetStage()),
			ion.String("sender", message.GetSender().String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

		// Delegate subscription handling to ListenerHandler
		listenerHandler := NewListenerHandler(StructListenerNode.ResponseHandler)
		listenerHandler.handleAskForSubscription(spanCtx, s, message)
	case config.Type_SubscriptionResponse:
		// Handle subscription response from buddy nodes
		ackStatus := message.GetACK().GetStatus()
		span.SetAttributes(attribute.String("ack_status", ackStatus))

		logger().NamedLogger.Info(spanCtx, "Received subscription response from peer",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", message.Message),
			ion.String("ack_status", ackStatus),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

		// Route the response to the ResponseHandler if available
		if StructListenerNode.ResponseHandler != nil {
			accepted := ackStatus == "ACK_TRUE"
			span.SetAttributes(attribute.Bool("accepted", accepted))
			StructListenerNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), accepted, "main")
			logger().NamedLogger.Info(spanCtx, "Successfully routed subscription response to ResponseHandler",
				ion.String("remote_peer_id", remotePeer.String()),
				ion.Bool("accepted", accepted),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		} else {
			span.SetAttributes(attribute.String("status", "no_response_handler"))
			logger().NamedLogger.Error(spanCtx, "No ResponseHandler set - subscription response not routed",
				fmt.Errorf("no response handler set"),
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		}
	case config.Type_BFTRequest:
		listenerHandler := NewListenerHandler(StructListenerNode.ResponseHandler)

		// Feature flag for safe rollback: USE_LEGACY_BFT=true reverts to old path
		useLegacyBFT := settings.Get().Features.UseLegacyBFT

		if useLegacyBFT {
			// LEGACY PATH: Manual vote aggregation (no PBFT engine)
			logger().NamedLogger.Info(spanCtx, "Handling Type_BFTRequest -> TriggerForBFTFromSequencer (LEGACY MODE)",
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("bft_mode", "legacy"),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

			listenerHandler.TriggerForBFTFromSequencer(s, message, AVCStruct.NewGlobalVariables().Get_PubSubNode().BuddyNodes.GetBuddies())
		} else {
			// NEW PATH: Full PBFT consensus engine (default)
			logger().NamedLogger.Info(spanCtx, "Handling Type_BFTRequest -> handleBFTRequest (BFT ENGINE)",
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("bft_mode", "engine"),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

			listenerHandler.handleBFTRequest(spanCtx, s, message)
		}
	case config.Type_VoteResult:
		logger().NamedLogger.Info(spanCtx, "Handling Type_VoteResult -> handleVoteResultRequest",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

		// Delegate to ListenerHandler for processing the vote result request
		listenerHandler := NewListenerHandler(StructListenerNode.ResponseHandler)
		listenerHandler.handleVoteResultRequest(spanCtx, s, message)
	case config.Type_BFTResult:
		logger().NamedLogger.Info(spanCtx, "Handling Type_BFTResult -> print buddy result with BLS",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

		// Expected payload shape from sendBFTResultToSequencer
		var payload struct {
			Round         uint64 `json:"round"`
			BlockHash     string `json:"block_hash"`
			BuddyID       string `json:"buddy_id"`
			Success       bool   `json:"success"`
			Decision      string `json:"decision"`
			BlockAccepted bool   `json:"block_accepted"`
			FailureReason string `json:"failure_reason"`
			Timestamp     int64  `json:"timestamp"`
			Vote          int8   `json:"vote"`
			Agree         bool   `json:"agree"`
			BLS           struct {
				Signature string `json:"Signature"`
				Agree     bool   `json:"Agree"`
				PubKey    string `json:"PubKey"`
				PeerID    string `json:"PeerID"`
			} `json:"bls"`
		}

		if err := json.Unmarshal([]byte(message.Message), &payload); err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "parse_bft_result_failed"))
			logger().NamedLogger.Error(spanCtx, "Failed to parse BFTResult payload",
				err,
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
			break
		}

		span.SetAttributes(
			attribute.Int64("round", int64(payload.Round)),
			attribute.String("block_hash", payload.BlockHash),
			attribute.String("buddy_id", payload.BuddyID),
			attribute.Bool("success", payload.Success),
			attribute.String("decision", payload.Decision),
			attribute.Bool("block_accepted", payload.BlockAccepted),
			attribute.Int("vote", int(payload.Vote)),
			attribute.Bool("agree", payload.Agree),
			attribute.String("bls_peer_id", payload.BLS.PeerID),
			attribute.Int("bls_signature_length", len(payload.BLS.Signature)),
		)

		if payload.FailureReason != "" {
			span.SetAttributes(attribute.String("failure_reason", payload.FailureReason))
		}

		logger().NamedLogger.Info(spanCtx, "Received BFT result from buddy (with BLS)",
			ion.String("buddy_id", payload.BuddyID),
			ion.String("block_hash", payload.BlockHash),
			ion.Int64("round", int64(payload.Round)),
			ion.Bool("success", payload.Success),
			ion.String("decision", payload.Decision),
			ion.Bool("block_accepted", payload.BlockAccepted),
			ion.String("failure_reason", payload.FailureReason),
			ion.Int("vote", int(payload.Vote)),
			ion.Bool("agree", payload.Agree),
			ion.String("bls_peer_id", payload.BLS.PeerID),
			ion.String("bls_pubkey", payload.BLS.PubKey),
			ion.Int("bls_signature_length", len(payload.BLS.Signature)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
	default:
		span.SetAttributes(attribute.String("status", "unknown_message_type"))
		logger().NamedLogger.Error(spanCtx, "Unknown message type",
			fmt.Errorf("unknown message type: %s", ackStage),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("ack_stage", ackStage),
			ion.String("message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

func (StructListenerNode *StructListener) HandleSubscriptionResponse(logger_ctx context.Context, s network.Stream, message *AVCStruct.Message, peerID peer.ID) {
	// Record trace span and close it
	responseSpanCtx, responseSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.HandleSubscriptionResponse")
	defer responseSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	responseSpan.SetAttributes(
		attribute.String("remote_peer_id", remotePeer.String()),
		attribute.String("expected_peer_id", peerID.String()),
	)

	// If message is nil, read from the stream
	if message == nil {
		reader := bufio.NewReader(s)
		responseMsg, err := reader.ReadString(config.Delimiter)
		if err != nil {
			responseSpan.RecordError(err)
			responseSpan.SetAttributes(attribute.String("status", "read_error"))
			duration := time.Since(startTime).Seconds()
			responseSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(responseSpanCtx, "Error reading response from stream",
				err,
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("expected_peer_id", peerID.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.HandleSubscriptionResponse"))
			return
		}

		logger().NamedLogger.Info(responseSpanCtx, "Received response from stream",
			ion.String("response", responseMsg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubscriptionResponse"))

		message = AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseMsg)
		if message == nil {
			responseSpan.RecordError(fmt.Errorf("failed to parse response message"))
			responseSpan.SetAttributes(attribute.String("status", "parse_failed"))
			duration := time.Since(startTime).Seconds()
			responseSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(responseSpanCtx, "Failed to parse response message",
				fmt.Errorf("failed to parse response message"),
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("expected_peer_id", peerID.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.HandleSubscriptionResponse"))
			return
		}
	}

	ackStatus := message.GetACK().GetStatus()
	responseSpan.SetAttributes(attribute.String("ack_status", ackStatus))

	logger().NamedLogger.Info(responseSpanCtx, "HandleSubscriptionResponse: Received response from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("expected_peer_id", peerID.String()),
		ion.String("message", message.Message),
		ion.String("ack_status", ackStatus),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleSubscriptionResponse"))

	// Route the response to ResponseHandler if available
	// Use peerID (the peer we sent the request to) not s.Conn().RemotePeer() (the peer responding)
	if StructListenerNode.ResponseHandler != nil {
		accepted := ackStatus == "ACK_TRUE"
		responseSpan.SetAttributes(attribute.Bool("accepted", accepted))

		StructListenerNode.ResponseHandler.HandleResponse(peerID, accepted, "main")

		logger().NamedLogger.Info(responseSpanCtx, "Successfully routed subscription response to ResponseHandler",
			ion.String("peer_id", peerID.String()),
			ion.Bool("accepted", accepted),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubscriptionResponse"))
	} else {
		responseSpan.SetAttributes(attribute.String("status", "no_response_handler"))
		logger().NamedLogger.Error(responseSpanCtx, "No ResponseHandler set - subscription response not routed",
			fmt.Errorf("no response handler set"),
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubscriptionResponse"))
	}

	duration := time.Since(startTime).Seconds()
	responseSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
// Uses LRU cache with TTL for optimal performance and resource efficiency
func (StructListenerNode *StructListener) SendMessageToPeer(logger_ctx context.Context, peerID peer.ID, message string) error {
	// Record trace span and close it
	sendSpanCtx, sendSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.SendMessageToPeer")
	defer sendSpan.End()

	startTime := time.Now().UTC()
	sendSpan.SetAttributes(
		attribute.String("peer_id", peerID.String()),
		attribute.Int("message_length", len(message)),
	)

	logger().NamedLogger.Info(sendSpanCtx, "Sending message to peer",
		ion.String("peer_id", peerID.String()),
		ion.String("message", message),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.SendMessageToPeer"))

	// Get or create a stream from the cache using SubmitMessageProtocol
	StreamCache := NewStreamCacheBuilder(StructListenerNode.ListenerBuddyNode.StreamCache)
	if StreamCache == nil {
		sendSpan.RecordError(fmt.Errorf("failed to get StreamCache"))
		sendSpan.SetAttributes(attribute.String("status", "stream_cache_nil"))
		duration := time.Since(startTime).Seconds()
		sendSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to get the StreamCache, nil StreamCache occurred")
	}

	stream, err := StreamCache.GetSubmitMessageStream(logger_ctx, peerID)
	if err != nil {
		// Direct connection failed, try fallback via seed node
		sendSpan.SetAttributes(attribute.String("connection_method", "seed_node_fallback"))
		logger().NamedLogger.Info(sendSpanCtx, "Direct connection failed, using seed node fallback",
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.SendMessageToPeer"))
		return StructListenerNode.sendViaSeedNode(sendSpanCtx, peerID, message)
	}
	// CRITICAL FIX: Ensure stream is always closed to prevent file descriptor leaks
	// particularly when read timeouts occur (lines 503-517)
	defer stream.Close()

	sendSpan.SetAttributes(attribute.String("connection_method", "direct"))

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		// If write fails, the stream might be invalid, close it and try fallback
		StreamCache.CloseStream(peerID)
		sendSpan.SetAttributes(attribute.String("connection_method", "seed_node_fallback_after_write_fail"))
		logger().NamedLogger.Info(sendSpanCtx, "Stream write failed, using seed node fallback",
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.SendMessageToPeer"))
		return StructListenerNode.sendViaSeedNode(sendSpanCtx, peerID, message)
	}

	// Parse the message to check if it's a vote submission or subscription request
	var voteMessage map[string]interface{}
	json.Unmarshal([]byte(message), &voteMessage)

	var isVote bool
	if ack, ok := voteMessage["ACK"].(map[string]interface{}); ok {
		if stage, ok := ack["stage"].(string); ok && stage == config.Type_SubmitVote {
			isVote = true
		}
	}

	sendSpan.SetAttributes(attribute.Bool("is_vote", isVote))

	// Only wait for response if it's NOT a vote (votes don't expect responses)
	if !isVote {
		// Read response after sending (for subscription requests)
		// Set a timeout for reading the response (20 seconds to allow for processing)
		deadline := time.Now().UTC().Add(20 * time.Second)
		stream.SetReadDeadline(deadline)

		// Also set a write deadline to prevent hung connections
		stream.SetWriteDeadline(time.Now().UTC().Add(10 * time.Second))
		sendSpan.SetAttributes(attribute.String("read_deadline", deadline.Format(time.RFC3339)))

		logger().NamedLogger.Info(sendSpanCtx, "Set read deadline and starting to read response",
			ion.String("peer_id", peerID.String()),
			ion.String("deadline", deadline.Format(time.RFC3339)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.SendMessageToPeer"))

		reader := bufio.NewReader(stream)
		responseMsg, err := reader.ReadString(config.Delimiter)

		if err != nil {
			sendSpan.RecordError(err)
			sendSpan.SetAttributes(attribute.String("status", "read_response_failed"))
			duration := time.Since(startTime).Seconds()
			sendSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(sendSpanCtx, "Failed to read response from peer",
				err,
				ion.String("peer_id", peerID.String()),
				ion.String("deadline", deadline.Format(time.RFC3339)),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.SendMessageToPeer"))
			// Return error so caller knows something went wrong, rather than assuming success and waiting
			// stream.Close()
			return fmt.Errorf("failed to read response from %s: %w", peerID, err)
		} else if responseMsg != "" {
			sendSpan.SetAttributes(attribute.String("response_received", "true"), attribute.Int("response_length", len(responseMsg)))

			logger().NamedLogger.Info(sendSpanCtx, "Received response from peer",
				ion.String("peer_id", peerID.String()),
				ion.String("response", responseMsg),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.SendMessageToPeer"))

			stat := stream.Conn().Stat()
			sendSpan.SetAttributes(
				attribute.String("connection_direction", stat.Direction.String()),
				attribute.String("stream_protocol", string(stream.Protocol())),
			)

			// Parse the response message
			responseMessage := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseMsg)
			if responseMessage != nil && responseMessage.GetACK() != nil {
				// Process the subscription response directly
				if responseMessage.GetACK().GetStage() == config.Type_SubscriptionResponse {
					accepted := responseMessage.GetACK().GetStatus() == "ACK_TRUE"
					sendSpan.SetAttributes(attribute.Bool("subscription_accepted", accepted))

					logger().NamedLogger.Info(sendSpanCtx, "Processing subscription response from peer",
						ion.String("peer_id", peerID.String()),
						ion.Bool("accepted", accepted),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "MessagePassing.SendMessageToPeer"))

					// Route the response to ResponseHandler if available
					if StructListenerNode.ResponseHandler != nil {
						StructListenerNode.ResponseHandler.HandleResponse(peerID, accepted, "main")
						logger().NamedLogger.Info(sendSpanCtx, "Successfully routed subscription response to ResponseHandler",
							ion.String("peer_id", peerID.String()),
							ion.Bool("accepted", accepted),
							ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
							ion.String("log_file", LOG_FILE),
							ion.String("topic", TOPIC),
							ion.String("function", "MessagePassing.SendMessageToPeer"))
					}
				}

				// stream.Close()
				logger().NamedLogger.Info(sendSpanCtx, "Stream closed after receiving response (deferred)",
					ion.String("peer_id", peerID.String()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "MessagePassing.SendMessageToPeer"))
			}
		}
	} else {
		// For votes, just close the stream without waiting for response
		// stream.Close()
		logger().NamedLogger.Info(sendSpanCtx, "Vote submitted - no response expected, closing stream (deferred)",
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.SendMessageToPeer"))
	}

	// Update metadata
	StructListenerNode.ListenerBuddyNode.Mutex.Lock()
	StructListenerNode.ListenerBuddyNode.MetaData.Sent++
	StructListenerNode.ListenerBuddyNode.MetaData.Total++
	StructListenerNode.ListenerBuddyNode.MetaData.UpdatedAt = time.Now().UTC()
	StructListenerNode.ListenerBuddyNode.Mutex.Unlock()

	duration := time.Since(startTime).Seconds()
	sendSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(sendSpanCtx, "Successfully sent listener message to peer",
		ion.String("peer_id", peerID.String()),
		ion.String("message", message),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.SendMessageToPeer"))

	return nil
}

// sendViaSeedNode establishes a quick connection via seed node, sends message, and drops connection
func (StructListenerNode *StructListener) sendViaSeedNode(logger_ctx context.Context, peerID peer.ID, message string) error {
	// Record trace span and close it
	seedSpanCtx, seedSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.sendViaSeedNode")
	defer seedSpan.End()

	startTime := time.Now().UTC()
	seedSpan.SetAttributes(
		attribute.String("peer_id", peerID.String()),
		attribute.Int("message_length", len(message)),
		attribute.String("connection_method", "seed_node"),
	)

	logger().NamedLogger.Info(seedSpanCtx, "Sending message via seed node",
		ion.String("peer_id", peerID.String()),
		ion.String("message", message),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.sendViaSeedNode"))

	// Get peer's multiaddr from seed node
	peerInfo, err := StructListenerNode.getPeerInfoFromSeedNode(seedSpanCtx, peerID)
	if err != nil {
		seedSpan.RecordError(err)
		seedSpan.SetAttributes(attribute.String("status", "get_peer_info_failed"))
		duration := time.Since(startTime).Seconds()
		seedSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to get peer info from seed node: %v", err)
	}

	seedSpan.SetAttributes(attribute.Int("peer_addrs_count", len(peerInfo.Addrs)))

	// Connect directly to the peer
	if err := StructListenerNode.ListenerBuddyNode.Host.Connect(seedSpanCtx, *peerInfo); err != nil {
		seedSpan.RecordError(err)
		seedSpan.SetAttributes(attribute.String("status", "connect_failed"))
		duration := time.Since(startTime).Seconds()
		seedSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to connect to peer %s: %v", peerID, err)
	}

	seedSpan.SetAttributes(attribute.String("connection_status", "connected"))

	// Open stream and send message using SubmitMessageProtocol
	stream, err := StructListenerNode.ListenerBuddyNode.Host.NewStream(seedSpanCtx, peerID, config.SubmitMessageProtocol)
	if err != nil {
		seedSpan.RecordError(err)
		seedSpan.SetAttributes(attribute.String("status", "stream_create_failed"))
		duration := time.Since(startTime).Seconds()
		seedSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to create stream to %s: %v", peerID, err)
	}
	defer stream.Close()

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		seedSpan.RecordError(err)
		seedSpan.SetAttributes(attribute.String("status", "write_failed"))
		duration := time.Since(startTime).Seconds()
		seedSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	seedSpan.SetAttributes(attribute.String("message_sent", "true"))

	// Read response after sending (for subscription requests)
	reader := bufio.NewReader(stream)
	responseMsg, err := reader.ReadString(config.Delimiter)
	if err == nil && responseMsg != "" {
		seedSpan.SetAttributes(attribute.String("response_received", "true"), attribute.Int("response_length", len(responseMsg)))

		logger().NamedLogger.Info(seedSpanCtx, "Received response from peer via seed node",
			ion.String("peer_id", peerID.String()),
			ion.String("response", responseMsg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendViaSeedNode"))

		// Parse the response message
		responseMessage := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseMsg)
		if responseMessage != nil && responseMessage.GetACK() != nil {
			// Process the subscription response directly
			if responseMessage.GetACK().GetStage() == config.Type_SubscriptionResponse {
				accepted := responseMessage.GetACK().GetStatus() == "ACK_TRUE"
				seedSpan.SetAttributes(attribute.Bool("subscription_accepted", accepted))

				logger().NamedLogger.Info(seedSpanCtx, "Processing subscription response from peer via seed node",
					ion.String("peer_id", peerID.String()),
					ion.Bool("accepted", accepted),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "MessagePassing.sendViaSeedNode"))

				// Route the response to ResponseHandler if available
				if StructListenerNode.ResponseHandler != nil {
					StructListenerNode.ResponseHandler.HandleResponse(peerID, accepted, "main")
					logger().NamedLogger.Info(seedSpanCtx, "Successfully routed subscription response to ResponseHandler",
						ion.String("peer_id", peerID.String()),
						ion.Bool("accepted", accepted),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "MessagePassing.sendViaSeedNode"))
				}
			}
		}
	}

	// Update metadata
	StructListenerNode.ListenerBuddyNode.Mutex.Lock()
	StructListenerNode.ListenerBuddyNode.MetaData.Sent++
	StructListenerNode.ListenerBuddyNode.MetaData.Total++
	StructListenerNode.ListenerBuddyNode.MetaData.UpdatedAt = time.Now().UTC()
	StructListenerNode.ListenerBuddyNode.Mutex.Unlock()

	duration := time.Since(startTime).Seconds()
	seedSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(seedSpanCtx, "Successfully sent listener message to peer via seed node",
		ion.String("peer_id", peerID.String()),
		ion.String("message", message),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.sendViaSeedNode"))

	return nil
}

// getPeerInfoFromSeedNode retrieves peer information from seed node
func (StructListenerNode *StructListener) getPeerInfoFromSeedNode(logger_ctx context.Context, peerID peer.ID) (*peer.AddrInfo, error) {
	// Record trace span and close it
	peerInfoSpanCtx, peerInfoSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.getPeerInfoFromSeedNode")
	defer peerInfoSpan.End()

	startTime := time.Now().UTC()
	peerInfoSpan.SetAttributes(attribute.String("peer_id", peerID.String()))

	logger().NamedLogger.Info(peerInfoSpanCtx, "Getting peer info from seed node",
		ion.String("peer_id", peerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.getPeerInfoFromSeedNode"))

	// Create seed node client from unified config
	seedNodeURL := settings.Get().Network.SeedNode

	peerInfoSpan.SetAttributes(attribute.String("seed_node_url", seedNodeURL))

	client, err := seednode.NewClient(seedNodeURL)
	if err != nil {
		peerInfoSpan.RecordError(err)
		peerInfoSpan.SetAttributes(attribute.String("status", "client_creation_failed"))
		duration := time.Since(startTime).Seconds()
		peerInfoSpan.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to create seed node client: %v", err)
	}
	defer client.Close()

	// Get peer record from seed node
	peerRecord, err := client.GetPeer(peerID.String())
	if err != nil {
		peerInfoSpan.RecordError(err)
		peerInfoSpan.SetAttributes(attribute.String("status", "get_peer_failed"))
		duration := time.Since(startTime).Seconds()
		peerInfoSpan.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("failed to get peer from seed node: %v", err)
	}

	peerInfoSpan.SetAttributes(attribute.Int("multiaddrs_count", len(peerRecord.Multiaddrs)))

	// Convert multiaddrs to peer.AddrInfo
	peerInfo := &peer.AddrInfo{ID: peerID}
	validAddrs := 0
	for _, multiaddrStr := range peerRecord.Multiaddrs {
		addr, err := multiaddr.NewMultiaddr(multiaddrStr)
		if err != nil {
			logger().NamedLogger.Warn(peerInfoSpanCtx, "Skipping invalid multiaddr",
				ion.String("multiaddr", multiaddrStr),
				ion.String("error", err.Error()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.getPeerInfoFromSeedNode"))
			continue
		}
		peerInfo.Addrs = append(peerInfo.Addrs, addr)
		validAddrs++
	}

	peerInfoSpan.SetAttributes(attribute.Int("valid_addrs_count", validAddrs))

	if len(peerInfo.Addrs) == 0 {
		peerInfoSpan.RecordError(fmt.Errorf("no valid multiaddrs found"))
		peerInfoSpan.SetAttributes(attribute.String("status", "no_valid_addrs"))
		duration := time.Since(startTime).Seconds()
		peerInfoSpan.SetAttributes(attribute.Float64("duration", duration))
		return nil, fmt.Errorf("no valid multiaddrs found for peer %s", peerID)
	}

	duration := time.Since(startTime).Seconds()
	peerInfoSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(peerInfoSpanCtx, "Successfully retrieved peer info from seed node",
		ion.String("peer_id", peerID.String()),
		ion.Int("valid_addrs_count", validAddrs),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.getPeerInfoFromSeedNode"))

	return peerInfo, nil
}
