package MessagePassing

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"jmdn/AVC/BFT/bft"
	"jmdn/AVC/BuddyNodes/CRDTSync"
	"jmdn/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"jmdn/AVC/BuddyNodes/MessagePassing/Service"
	"jmdn/AVC/BuddyNodes/MessagePassing/Structs"
	ServiceLayer "jmdn/AVC/BuddyNodes/ServiceLayer"
	"jmdn/AVC/BuddyNodes/Types"
	"jmdn/AVC/BuddyNodes/common"
	Publisher "jmdn/Pubsub/Publish"
	"jmdn/Sequencer/Triggers/Maps"
	"jmdn/config"
	GRO "jmdn/config/GRO"
	AVCStruct "jmdn/config/PubSubMessages"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
)

// BFTContext holds the context for a BFT consensus round
type BFTContext struct {
	Round           uint64
	BlockHash       string
	AllBuddies      []bft.BuddyInput
	SequencerPeerID string
	GossipsubTopic  string
}

// ListenerHandler handles incoming messages on the SubmitMessageProtocol
// This handler processes subscription requests, vote submissions, and subscription responses
type ListenerHandler struct {
	responseHandler AVCStruct.ResponseHandler

	// BFT context storage (keyed by round-blockHash)
	bftContextMutex sync.RWMutex
	bftContexts     map[string]*BFTContext

	// Sequencer peer ID storage
	sequencerPeerID string
	sequencerMutex  sync.RWMutex
}

// NewListenerHandler creates a new ListenerHandler instance
func NewListenerHandler(responseHandler AVCStruct.ResponseHandler) *ListenerHandler {
	return &ListenerHandler{
		responseHandler: responseHandler,
		bftContexts:     make(map[string]*BFTContext),
	}
}

// HandleSubmitMessageStream processes incoming messages on the SubmitMessageProtocol
// This is the main entry point for handling subscription requests, votes, and responses
// Note: Stream is explicitly closed via defer to prevent resource leaks (MaxStream exhaustion).
func (lh *ListenerHandler) HandleSubmitMessageStream(logger_ctx context.Context, s network.Stream) {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.HandleSubmitMessageStream")
	defer span.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	span.SetAttributes(
		attribute.String("remote_peer_id", remotePeer.String()),
	)
	// Ensure stream is closed to prevent resource leaks
	defer s.Close()

	logger().NamedLogger.Info(spanCtx, "ListenerHandler.HandleSubmitMessageStream CALLED",
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
	if message == nil {
		span.RecordError(fmt.Errorf("failed to parse message"))
		span.SetAttributes(attribute.String("status", "parse_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to parse message - malformed JSON or invalid structure",
			fmt.Errorf("failed to parse message"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		return
	}

	logger().NamedLogger.Info(spanCtx, "Received submit message from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", msg),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

	// Check if ACK is not nil before accessing it
	if message.GetACK() == nil {
		span.RecordError(fmt.Errorf("received message with nil ACK"))
		span.SetAttributes(attribute.String("status", "nil_ack"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Received message with nil ACK",
			fmt.Errorf("received message with nil ACK"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", msg),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		return
	}

	ackStage := message.GetACK().GetStage()
	span.SetAttributes(attribute.String("ack_stage", ackStage))

	logger().NamedLogger.Info(spanCtx, "ACK Stage",
		ion.String("stage", ackStage),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.HandleSubmitMessageStream"))

	// Route message based on ACK stage
	switch ackStage {
	case config.Type_BFTRequest:
		logger().NamedLogger.Info(spanCtx, "Handling Type_BFTRequest",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		lh.handleBFTRequest(spanCtx, s, message)
		defer s.Close()
	case config.Type_SubmitVote:
		logger().NamedLogger.Info(spanCtx, "Handling Type_SubmitVote",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		lh.handleSubmitVote(spanCtx, s, message)
		defer s.Close()
	case config.Type_AskForSubscription:
		logger().NamedLogger.Info(spanCtx, "Handling Type_AskForSubscription",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		lh.handleAskForSubscription(spanCtx, s, message)
	case config.Type_SubscriptionResponse:
		logger().NamedLogger.Info(spanCtx, "Handling Type_SubscriptionResponse",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		lh.handleSubscriptionResponse(spanCtx, s, message)
		defer s.Close()
	case config.Type_VoteResult:
		logger().NamedLogger.Info(spanCtx, "Handling Type_VoteResult",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		lh.handleVoteResultRequest(spanCtx, s, message)
		defer s.Close()
	default:
		span.SetAttributes(attribute.String("status", "unknown_message_type"))
		logger().NamedLogger.Error(spanCtx, "Unknown message type",
			fmt.Errorf("unknown message type: %s", ackStage),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("ack_stage", ackStage),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.HandleSubmitMessageStream"))
		defer s.Close()
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// handleBFTRequest processes BFT consensus request from Sequencer
func (lh *ListenerHandler) handleBFTRequest(logger_ctx context.Context, s network.Stream, message *AVCStruct.Message) {
	// Record trace span and close it
	bftRequestSpanCtx, bftRequestSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.handleBFTRequest")
	defer bftRequestSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	bftRequestSpan.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	if ListenerHandlerLocal == nil {
		var err error
		ListenerHandlerLocal, err = common.InitializeGRO(GRO.HandleBFTRequestLocal)
		if err != nil {
			bftRequestSpan.RecordError(err)
			bftRequestSpan.SetAttributes(attribute.String("status", "init_failed"))
			duration := time.Since(startTime).Seconds()
			bftRequestSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(bftRequestSpanCtx, "Failed to initialize ListenerHandler local manager",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleBFTRequest"))
			return
		}
	}

	logger().NamedLogger.Info(bftRequestSpanCtx, "Received BFT request from Sequencer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", message.Message),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleBFTRequest"))

	// Store Sequencer peer ID
	lh.sequencerMutex.Lock()
	lh.sequencerPeerID = s.Conn().RemotePeer().String()
	lh.sequencerMutex.Unlock()

	// Parse BFT request
	var requestData struct {
		Round          uint64 `json:"round"`
		BlockHash      string `json:"block_hash"`
		GossipsubTopic string `json:"gossipsub_topic"`
		AllBuddies     []struct {
			ID         string `json:"id"`
			Decision   string `json:"decision"`
			PublicKey  []byte `json:"public_key"`
			PrivateKey []byte `json:"private_key,omitempty"`
		} `json:"all_buddies"`
	}

	if err := json.Unmarshal([]byte(message.Message), &requestData); err != nil {
		bftRequestSpan.RecordError(err)
		bftRequestSpan.SetAttributes(attribute.String("status", "parse_failed"))
		duration := time.Since(startTime).Seconds()
		bftRequestSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(bftRequestSpanCtx, "Failed to parse BFT request",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleBFTRequest"))
		return
	}

	bftRequestSpan.SetAttributes(
		attribute.Int64("round", int64(requestData.Round)),
		attribute.String("block_hash", requestData.BlockHash),
		attribute.String("gossipsub_topic", requestData.GossipsubTopic),
		attribute.Int("buddy_count", len(requestData.AllBuddies)),
	)

	// Get listener node
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		bftRequestSpan.RecordError(fmt.Errorf("listener node not initialized"))
		bftRequestSpan.SetAttributes(attribute.String("status", "node_not_initialized"))
		duration := time.Since(startTime).Seconds()
		bftRequestSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(bftRequestSpanCtx, "Listener node not initialized",
			fmt.Errorf("listener node not initialized"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleBFTRequest"))
		return
	}

	myBuddyID := listenerNode.PeerID.String()
	bftRequestSpan.SetAttributes(attribute.String("my_buddy_id", myBuddyID))

	// Check if I'm in the buddy list
	amIaBuddy := false
	buddies := make([]bft.BuddyInput, 0)

	for _, buddy := range requestData.AllBuddies {
		decision := bft.Accept
		if buddy.Decision == "REJECT" {
			decision = bft.Reject
		}

		buddyInput := bft.BuddyInput{
			ID:         buddy.ID,
			Decision:   decision,
			PublicKey:  buddy.PublicKey,
			PrivateKey: nil,
		}

		if buddy.ID == myBuddyID {
			amIaBuddy = true
			buddyInput.PrivateKey = buddy.PrivateKey
			bftRequestSpan.SetAttributes(attribute.String("my_decision", string(decision)))
			logger().NamedLogger.Info(bftRequestSpanCtx, "I am in the buddy list! My decision",
				ion.String("decision", string(decision)),
				ion.String("my_buddy_id", myBuddyID),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleBFTRequest"))
		}

		buddies = append(buddies, buddyInput)
	}

	bftRequestSpan.SetAttributes(attribute.Bool("am_i_buddy", amIaBuddy))

	if !amIaBuddy {
		bftRequestSpan.SetAttributes(attribute.String("status", "not_in_buddy_list"))
		duration := time.Since(startTime).Seconds()
		bftRequestSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(bftRequestSpanCtx, "I'm not in the buddy list for this round - skipping",
			fmt.Errorf("not in buddy list"),
			ion.String("my_buddy_id", myBuddyID),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleBFTRequest"))
		return
	}

	// Store BFT context
	contextKey := fmt.Sprintf("%d-%s", requestData.Round, requestData.BlockHash)
	lh.bftContextMutex.Lock()
	lh.bftContexts[contextKey] = &BFTContext{
		Round:           requestData.Round,
		BlockHash:       requestData.BlockHash,
		AllBuddies:      buddies,
		SequencerPeerID: s.Conn().RemotePeer().String(),
		GossipsubTopic:  requestData.GossipsubTopic,
	}
	lh.bftContextMutex.Unlock()

	logger().NamedLogger.Info(bftRequestSpanCtx, "BFT context stored for round",
		ion.Int64("round", int64(requestData.Round)),
		ion.String("block_hash", requestData.BlockHash),
		ion.String("context_key", contextKey),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleBFTRequest"))

	// Send acknowledgment back to Sequencer
	lh.sendBFTAcknowledgment(bftRequestSpanCtx, s, requestData.Round, requestData.BlockHash, true)

	// Start BFT consensus in background
	ListenerHandlerLocal.Go(GRO.BFTConsensusThread, func(ctx context.Context) error {
		lh.runBFTConsensusFlow(bftRequestSpanCtx, contextKey)
		return nil
	})

	duration := time.Since(startTime).Seconds()
	bftRequestSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// sendBFTAcknowledgment sends ACK back to Sequencer
func (lh *ListenerHandler) sendBFTAcknowledgment(logger_ctx context.Context, s network.Stream, round uint64, blockHash string, accepted bool) {
	ackSpanCtx, ackSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.sendBFTAcknowledgment")
	defer ackSpan.End()

	startTime := time.Now().UTC()
	ackSpan.SetAttributes(
		attribute.Int64("round", int64(round)),
		attribute.String("block_hash", blockHash),
		attribute.Bool("accepted", accepted),
	)

	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		ackSpan.RecordError(fmt.Errorf("listener node not initialized"))
		ackSpan.SetAttributes(attribute.String("status", "node_not_initialized"))
		duration := time.Since(startTime).Seconds()
		ackSpan.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	ackData := map[string]interface{}{
		"round":      round,
		"block_hash": blockHash,
		"accepted":   accepted,
		"buddy_id":   listenerNode.PeerID.String(),
		"message":    "BFT request accepted, starting consensus",
	}

	ackJSON, _ := json.Marshal(ackData)

	ack := AVCStruct.NewACKBuilder().
		True_ACK_Message(listenerNode.PeerID, config.Type_ACK_True)

	response := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(string(ackJSON)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ack)

	responseBytes, err := json.Marshal(response)
	if err != nil {
		ackSpan.RecordError(err)
		ackSpan.SetAttributes(attribute.String("status", "marshal_failed"))
		duration := time.Since(startTime).Seconds()
		ackSpan.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	_, err = s.Write([]byte(string(responseBytes) + string(rune(config.Delimiter))))
	if err != nil {
		ackSpan.RecordError(err)
		ackSpan.SetAttributes(attribute.String("status", "write_failed"))
		duration := time.Since(startTime).Seconds()
		ackSpan.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	duration := time.Since(startTime).Seconds()
	ackSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(ackSpanCtx, "Sent BFT acknowledgment to Sequencer",
		ion.Int64("round", int64(round)),
		ion.String("block_hash", blockHash),
		ion.Bool("accepted", accepted),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.sendBFTAcknowledgment"))
}

// runBFTConsensusFlow executes the full BFT consensus flow
func (lh *ListenerHandler) runBFTConsensusFlow(logger_ctx context.Context, contextKey string) {
	// Record trace span and close it
	consensusSpanCtx, consensusSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.runBFTConsensusFlow")
	defer consensusSpan.End()

	startTime := time.Now().UTC()
	consensusSpan.SetAttributes(attribute.String("context_key", contextKey))

	logger().NamedLogger.Info(consensusSpanCtx, "Starting BFT consensus flow",
		ion.String("context_key", contextKey),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.runBFTConsensusFlow"))

	// Retrieve BFT context
	lh.bftContextMutex.RLock()
	bftCtx, exists := lh.bftContexts[contextKey]
	lh.bftContextMutex.RUnlock()

	if !exists {
		consensusSpan.RecordError(fmt.Errorf("BFT context not found"))
		consensusSpan.SetAttributes(attribute.String("status", "context_not_found"))
		duration := time.Since(startTime).Seconds()
		consensusSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(consensusSpanCtx, "BFT context not found for key",
			fmt.Errorf("BFT context not found for key: %s", contextKey),
			ion.String("context_key", contextKey),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.runBFTConsensusFlow"))
		return
	}

	consensusSpan.SetAttributes(
		attribute.Int64("round", int64(bftCtx.Round)),
		attribute.String("block_hash", bftCtx.BlockHash),
		attribute.Int("buddy_count", len(bftCtx.AllBuddies)),
		attribute.String("gossipsub_topic", bftCtx.GossipsubTopic),
		attribute.String("sequencer_peer_id", bftCtx.SequencerPeerID),
	)

	logger().NamedLogger.Info(consensusSpanCtx, "BFT context retrieved",
		ion.Int64("round", int64(bftCtx.Round)),
		ion.String("block_hash", bftCtx.BlockHash),
		ion.Int("buddy_count", len(bftCtx.AllBuddies)),
		ion.String("gossipsub_topic", bftCtx.GossipsubTopic),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.runBFTConsensusFlow"))

	// Get listener node
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		consensusSpan.RecordError(fmt.Errorf("listener node not initialized"))
		consensusSpan.SetAttributes(attribute.String("status", "node_not_initialized"))
		duration := time.Since(startTime).Seconds()
		consensusSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(consensusSpanCtx, "Listener node not initialized",
			fmt.Errorf("listener node not initialized"),
			ion.String("context_key", contextKey),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.runBFTConsensusFlow"))
		return
	}

	// Get PubSub node
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if pubSubNode == nil || pubSubNode.PubSub == nil {
		consensusSpan.RecordError(fmt.Errorf("pubsub node not initialized"))
		consensusSpan.SetAttributes(attribute.String("status", "pubsub_not_initialized"))
		duration := time.Since(startTime).Seconds()
		consensusSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(consensusSpanCtx, "PubSub node not initialized",
			fmt.Errorf("pubsub node not initialized"),
			ion.String("context_key", contextKey),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.runBFTConsensusFlow"))
		return
	}

	myBuddyID := listenerNode.PeerID.String()
	consensusSpan.SetAttributes(attribute.String("my_buddy_id", myBuddyID))

	// Create BFT engine with UPDATED config including activity-based settings
	bftConfig := bft.DefaultConfig()
	bftConfig.PrepareTimeout = 15 * time.Second
	bftConfig.CommitTimeout = 15 * time.Second
	bftConfig.InactivityTimeout = 5 * time.Second // 5 seconds of silence

	consensusSpan.SetAttributes(
		attribute.Float64("prepare_timeout_seconds", bftConfig.PrepareTimeout.Seconds()),
		attribute.Float64("commit_timeout_seconds", bftConfig.CommitTimeout.Seconds()),
		attribute.Float64("inactivity_timeout_seconds", bftConfig.InactivityTimeout.Seconds()),
	)

	logger().NamedLogger.Info(consensusSpanCtx, "BFT Config",
		ion.Float64("prepare_timeout_seconds", bftConfig.PrepareTimeout.Seconds()),
		ion.Float64("commit_timeout_seconds", bftConfig.CommitTimeout.Seconds()),
		ion.Float64("inactivity_timeout_seconds", bftConfig.InactivityTimeout.Seconds()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.runBFTConsensusFlow"))

	bftEngine := bft.New(bftConfig)

	// Create BFT adapter
	ctx := consensusSpanCtx
	adapter, err := bft.NewBFTPubSubAdapter(
		ctx,
		pubSubNode.PubSub,
		bftEngine,
		bftCtx.GossipsubTopic,
	)
	if err != nil {
		consensusSpan.RecordError(err)
		consensusSpan.SetAttributes(attribute.String("status", "adapter_creation_failed"))
		duration := time.Since(startTime).Seconds()
		consensusSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(consensusSpanCtx, "Failed to create BFT adapter",
			err,
			ion.String("context_key", contextKey),
			ion.String("gossipsub_topic", bftCtx.GossipsubTopic),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.runBFTConsensusFlow"))
		lh.sendBFTResultToSequencer(consensusSpanCtx, bftCtx.Round, bftCtx.BlockHash, myBuddyID, false, "REJECT", fmt.Sprintf("Failed to create adapter: %v", err))
		return
	}
	defer adapter.Close()

	logger().NamedLogger.Info(consensusSpanCtx, "BFT adapter created successfully",
		ion.String("context_key", contextKey),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.runBFTConsensusFlow"))

	// Small delay to ensure all buddies are ready
	time.Sleep(2 * time.Second)

	// Run BFT consensus
	logger().NamedLogger.Info(consensusSpanCtx, "Running BFT consensus",
		ion.String("context_key", contextKey),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.runBFTConsensusFlow"))

	consensusStartTime := time.Now().UTC()
	result, err := adapter.ProposeConsensus(
		ctx,
		bftCtx.Round,
		bftCtx.BlockHash,
		myBuddyID,
		bftCtx.AllBuddies,
	)

	if err != nil {
		consensusSpan.RecordError(err)
		consensusSpan.SetAttributes(attribute.String("status", "consensus_failed"))
		consensusDuration := time.Since(consensusStartTime).Seconds()
		consensusSpan.SetAttributes(attribute.Float64("consensus_duration", consensusDuration))
		duration := time.Since(startTime).Seconds()
		consensusSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(consensusSpanCtx, "BFT consensus failed",
			err,
			ion.String("context_key", contextKey),
			ion.Float64("consensus_duration", consensusDuration),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.runBFTConsensusFlow"))
		lh.sendBFTResultToSequencer(consensusSpanCtx, bftCtx.Round, bftCtx.BlockHash, myBuddyID, false, "REJECT", fmt.Sprintf("Consensus failed: %v", err))
		return
	}

	consensusDuration := time.Since(consensusStartTime).Seconds()
	consensusSpan.SetAttributes(
		attribute.Bool("consensus_success", result.Success),
		attribute.String("consensus_decision", string(result.Decision)),
		attribute.Bool("block_accepted", result.BlockAccepted),
		attribute.Float64("consensus_duration", consensusDuration),
		attribute.Int("prepare_count", result.PrepareCount),
		attribute.Int("commit_count", result.CommitCount),
		attribute.Int("byzantine_count", len(result.ByzantineDetected)),
	)

	if len(result.ByzantineDetected) > 0 {
		consensusSpan.SetAttributes(attribute.String("byzantine_nodes", fmt.Sprintf("%v", result.ByzantineDetected)))
	}

	logger().NamedLogger.Info(consensusSpanCtx, "BFT consensus completed successfully",
		ion.Bool("success", result.Success),
		ion.String("decision", string(result.Decision)),
		ion.Bool("block_accepted", result.BlockAccepted),
		ion.Float64("consensus_duration", consensusDuration),
		ion.Int("prepare_count", result.PrepareCount),
		ion.Int("commit_count", result.CommitCount),
		ion.Int("byzantine_count", len(result.ByzantineDetected)),
		ion.String("context_key", contextKey),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.runBFTConsensusFlow"))

	// Send result to Sequencer
	lh.sendBFTResultToSequencer(
		consensusSpanCtx,
		bftCtx.Round,
		bftCtx.BlockHash,
		myBuddyID,
		result.Success,
		string(result.Decision),
		result.FailureReason,
	)

	// Cleanup context
	lh.bftContextMutex.Lock()
	delete(lh.bftContexts, contextKey)
	lh.bftContextMutex.Unlock()

	duration := time.Since(startTime).Seconds()
	consensusSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// sendBFTResultToSequencer reports BFT consensus result back to Sequencer
func (lh *ListenerHandler) sendBFTResultToSequencer(
	logger_ctx context.Context,
	round uint64,
	blockHash string,
	buddyID string,
	success bool,
	decision string,
	failureReason string,
) {
	// Record trace span and close it
	resultSpanCtx, resultSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.sendBFTResultToSequencer")
	defer resultSpan.End()

	startTime := time.Now().UTC()
	resultSpan.SetAttributes(
		attribute.Int64("round", int64(round)),
		attribute.String("block_hash", blockHash),
		attribute.String("buddy_id", buddyID),
		attribute.Bool("success", success),
		attribute.String("decision", decision),
	)

	if failureReason != "" {
		resultSpan.SetAttributes(attribute.String("failure_reason", failureReason))
	}

	logger().NamedLogger.Info(resultSpanCtx, "Sending BFT result to Sequencer",
		ion.Int64("round", int64(round)),
		ion.String("block_hash", blockHash),
		ion.String("buddy_id", buddyID),
		ion.Bool("success", success),
		ion.String("decision", decision),
		ion.String("failure_reason", failureReason),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.sendBFTResultToSequencer"))

	// Get listener node
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		resultSpan.RecordError(fmt.Errorf("listener node not initialized"))
		resultSpan.SetAttributes(attribute.String("status", "node_not_initialized"))
		duration := time.Since(startTime).Seconds()
		resultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(resultSpanCtx, "Listener node not initialized",
			fmt.Errorf("listener node not initialized"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendBFTResultToSequencer"))
		return
	}

	// Get Sequencer peer ID
	lh.sequencerMutex.RLock()
	sequencerPeerIDStr := lh.sequencerPeerID
	lh.sequencerMutex.RUnlock()

	if sequencerPeerIDStr == "" {
		resultSpan.RecordError(fmt.Errorf("sequencer peer ID not found"))
		resultSpan.SetAttributes(attribute.String("status", "sequencer_id_not_found"))
		duration := time.Since(startTime).Seconds()
		resultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(resultSpanCtx, "Sequencer peer ID not found",
			fmt.Errorf("sequencer peer ID not found"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendBFTResultToSequencer"))
		return
	}

	resultSpan.SetAttributes(attribute.String("sequencer_peer_id", sequencerPeerIDStr))

	// Derive vote from decision and prepare BLS signature over blockHash
	var vote int8 = -1
	if success && decision == "ACCEPT" {
		vote = 1
	}
	resultSpan.SetAttributes(attribute.Int("vote", int(vote)))

	// Create result message (include BLS payload)
	resultData := map[string]interface{}{
		"round":          round,
		"block_hash":     blockHash,
		"buddy_id":       buddyID,
		"success":        success,
		"decision":       decision,
		"block_accepted": success && decision == "ACCEPT",
		"failure_reason": failureReason,
		"timestamp":      time.Now().UTC().Unix(),
		"vote":           vote,
	}
	resultJSON, err := json.Marshal(resultData)
	if err != nil {
		resultSpan.RecordError(err)
		resultSpan.SetAttributes(attribute.String("status", "marshal_failed"))
		duration := time.Since(startTime).Seconds()
		resultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(resultSpanCtx, "Failed to marshal result",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendBFTResultToSequencer"))
		return
	}

	// Create ACK for BFT result
	ack := AVCStruct.NewACKBuilder().
		SetTrueStatus().
		SetPeerID(listenerNode.PeerID).
		SetStage(config.Type_BFTResult)

	// Create message
	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(string(resultJSON)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ack)

	// Decode Sequencer peer ID
	sequencerPeerID, err := peer.Decode(sequencerPeerIDStr)
	if err != nil {
		resultSpan.RecordError(err)
		resultSpan.SetAttributes(attribute.String("status", "decode_failed"))
		duration := time.Since(startTime).Seconds()
		resultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(resultSpanCtx, "Failed to decode sequencer peer ID",
			err,
			ion.String("sequencer_peer_id", sequencerPeerIDStr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendBFTResultToSequencer"))
		return
	}

	// Open stream to Sequencer
	stream, err := listenerNode.Host.NewStream(
		resultSpanCtx,
		sequencerPeerID,
		config.BuddyNodesMessageProtocol,
	)
	if err != nil {
		resultSpan.RecordError(err)
		resultSpan.SetAttributes(attribute.String("status", "stream_open_failed"))
		duration := time.Since(startTime).Seconds()
		resultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(resultSpanCtx, "Failed to open stream to Sequencer",
			err,
			ion.String("sequencer_peer_id", sequencerPeerIDStr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendBFTResultToSequencer"))
		return
	}
	defer stream.Close()

	// Send message
	messageBytes, err := json.Marshal(message)
	if err != nil {
		resultSpan.RecordError(err)
		resultSpan.SetAttributes(attribute.String("status", "message_marshal_failed"))
		duration := time.Since(startTime).Seconds()
		resultSpan.SetAttributes(attribute.Float64("duration", duration))
		return
	}

	_, err = stream.Write([]byte(string(messageBytes) + string(rune(config.Delimiter))))
	if err != nil {
		resultSpan.RecordError(err)
		resultSpan.SetAttributes(attribute.String("status", "write_failed"))
		duration := time.Since(startTime).Seconds()
		resultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(resultSpanCtx, "Failed to send result to Sequencer",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendBFTResultToSequencer"))
		return
	}

	duration := time.Since(startTime).Seconds()
	resultSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(resultSpanCtx, "Successfully sent BFT result to Sequencer",
		ion.Int64("round", int64(round)),
		ion.String("decision", decision),
		ion.Bool("success", success),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.sendBFTResultToSequencer"))
}

// handleSubmitVote processes vote submission messages
func (lh *ListenerHandler) handleSubmitVote(logger_ctx context.Context, s network.Stream, message *AVCStruct.Message) {
	// Record trace span and close it
	voteSpanCtx, voteSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.handleSubmitVote")
	defer voteSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	voteSpan.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	logger().NamedLogger.Info(voteSpanCtx, "Received submit vote from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", message.Message),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleSubmitVote"))

	// Check if PubSubNode and ForListner are initialized
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()

	// Initialize PubSub node if not already done
	if pubSubNode == nil || pubSubNode.PubSub == nil {
		logger().NamedLogger.Info(voteSpanCtx, "Initializing PubSub_BuddyNode for vote submission",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
		// Get the ForListner (which should be initialized)
		if listenerNode == nil {
			voteSpan.RecordError(fmt.Errorf("ForListner not initialized"))
			voteSpan.SetAttributes(attribute.String("status", "listener_not_initialized"))
			duration := time.Since(startTime).Seconds()
			voteSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(voteSpanCtx, "ForListner not initialized - cannot process vote",
				fmt.Errorf("ForListner not initialized"),
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleSubmitVote"))
			return
		}

		// Create GossipPubSub for this node
		gps := AVCStruct.NewGossipPubSubBuilder(nil).
			SetHost(listenerNode.Host).
			SetProtocol(config.BuddyNodesMessageProtocol).
			Build()

		// Create and set PubSub_BuddyNode
		pubSubBuddyNode := NewBuddyNode(voteSpanCtx, listenerNode.Host, &listenerNode.BuddyNodes, nil, gps)
		AVCStruct.NewGlobalVariables().Set_PubSubNode(pubSubBuddyNode)
		pubSubNode = pubSubBuddyNode
		logger().NamedLogger.Info(voteSpanCtx, "PubSub_BuddyNode initialized successfully",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
	}

	if listenerNode == nil {
		voteSpan.RecordError(fmt.Errorf("ForListner not initialized"))
		voteSpan.SetAttributes(attribute.String("status", "listener_not_initialized"))
		duration := time.Since(startTime).Seconds()
		voteSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteSpanCtx, "ForListner not initialized - cannot process vote",
			fmt.Errorf("ForListner not initialized"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
		return
	}

	// Add vote to local CRDT Engine WITHOUT republishing to pubsub (to avoid loops)
	// Parse the vote message
	var voteData map[string]interface{}
	if err := json.Unmarshal([]byte(message.Message), &voteData); err != nil {
		voteSpan.RecordError(err)
		voteSpan.SetAttributes(attribute.String("status", "unmarshal_failed"))
		duration := time.Since(startTime).Seconds()
		voteSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteSpanCtx, "Failed to unmarshal vote message",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("message", message.Message),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
		return
	}

	// Validate sender authenticity
	if message.Sender != s.Conn().RemotePeer() {
		voteSpan.RecordError(fmt.Errorf("sender mismatch"))
		voteSpan.SetAttributes(attribute.String("status", "sender_mismatch"))
		duration := time.Since(startTime).Seconds()
		voteSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteSpanCtx, "Sender mismatch - dropping vote",
			fmt.Errorf("sender mismatch: declared %s, connection %s", message.Sender, s.Conn().RemotePeer()),
			ion.String("declared_sender", message.Sender.String()),
			ion.String("connection_peer", s.Conn().RemotePeer().String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
		return
	}

	// Validate vote payload fields
	voteValueRaw, hasVote := voteData["vote"]
	blockHashRaw, hasBlockHash := voteData["block_hash"]
	if !hasVote || !hasBlockHash {
		voteSpan.RecordError(fmt.Errorf("missing vote or block_hash"))
		voteSpan.SetAttributes(attribute.String("status", "invalid_payload"))
		duration := time.Since(startTime).Seconds()
		voteSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteSpanCtx, "Missing vote or block_hash in payload - dropping vote",
			fmt.Errorf("missing vote or block_hash"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
		return
	}
	voteValue, ok := voteValueRaw.(float64)
	if !ok || (voteValue != 1 && voteValue != -1) {
		voteSpan.RecordError(fmt.Errorf("invalid vote value"))
		voteSpan.SetAttributes(attribute.String("status", "invalid_vote_value"))
		duration := time.Since(startTime).Seconds()
		voteSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteSpanCtx, "Invalid vote value - dropping vote",
			fmt.Errorf("invalid vote value: %v", voteValueRaw),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("vote_value", fmt.Sprintf("%v", voteValueRaw)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
		return
	}
	blockHash, ok := blockHashRaw.(string)
	if !ok || blockHash == "" {
		voteSpan.RecordError(fmt.Errorf("invalid block_hash"))
		voteSpan.SetAttributes(attribute.String("status", "invalid_block_hash"))
		duration := time.Since(startTime).Seconds()
		voteSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteSpanCtx, "Invalid block_hash - dropping vote",
			fmt.Errorf("invalid block_hash: %v", blockHashRaw),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("block_hash", fmt.Sprintf("%v", blockHashRaw)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))
		return
	}

	voteSpan.SetAttributes(
		attribute.Float64("vote_value", voteValue),
		attribute.String("block_hash", blockHash),
	)

	if _, exists := voteData["vote"]; exists {
		OP := &Types.OP{
			NodeID: message.Sender,
			OpType: int8(1), // 1 for add, -1 for remove
			KeyValue: Types.KeyValue{
				Key:   message.Sender.String(), // key would be the peer id of the sender
				Value: message.Message,
			},
		}

		result := ServiceLayer.Controller(listenerNode.CRDTLayer, OP)
		if err, ok := result.(error); ok && err != nil {
			voteSpan.RecordError(err)
			voteSpan.SetAttributes(attribute.String("status", "crdt_add_failed"))
			duration := time.Since(startTime).Seconds()
			voteSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(voteSpanCtx, "Failed to add vote to CRDT",
				err,
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleSubmitVote"))
			return
		}

		logger().NamedLogger.Info(voteSpanCtx, "Successfully added vote to CRDT",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("block_hash", blockHash),
			ion.Float64("vote_value", voteValue),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubmitVote"))

		// Now publish the vote to pubsub so ALL other buddy nodes can receive it
		if pubSubNode != nil && pubSubNode.PubSub != nil {
			logger().NamedLogger.Info(voteSpanCtx, "Republishing vote to pubsub for all buddy nodes",
				ion.String("republisher_peer_id", listenerNode.PeerID.String()),
				ion.String("original_sender", message.Sender.String()),
				ion.String("channel", config.PubSub_ConsensusChannel),
				ion.Int64("timestamp", message.Timestamp),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleSubmitVote"))

			// This is necessary because the vote was sent via direct stream to ONE node
			// We need to republish it to pubsub so ALL buddy nodes receive it
			if err := Publisher.Publish(voteSpanCtx, pubSubNode.PubSub, config.PubSub_ConsensusChannel, message, map[string]string{}); err != nil {
				voteSpan.RecordError(err)
				voteSpan.SetAttributes(attribute.String("status", "republish_failed"))
				logger().NamedLogger.Error(voteSpanCtx, "Failed to republish vote to pubsub",
					err,
					ion.String("remote_peer_id", remotePeer.String()),
					ion.String("channel", config.PubSub_ConsensusChannel),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "MessagePassing.handleSubmitVote"))
			} else {
				voteSpan.SetAttributes(attribute.String("republish_status", "success"))
				logger().NamedLogger.Info(voteSpanCtx, "Successfully republished vote to pubsub",
					ion.String("remote_peer_id", remotePeer.String()),
					ion.String("channel", config.PubSub_ConsensusChannel),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "MessagePassing.handleSubmitVote"))
			}
		} else {
			voteSpan.SetAttributes(attribute.String("status", "pubsub_not_available"))
			logger().NamedLogger.Warn(voteSpanCtx, "Cannot republish vote - pubSubNode or pubSubNode.PubSub is nil",
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.handleSubmitVote"))
		}
	}

	duration := time.Since(startTime).Seconds()
	voteSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(voteSpanCtx, "Successfully processed vote from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleSubmitVote"))

	// NOTE: Vote aggregation and BFT triggering is now handled separately
	// when BFT request is received from Sequencer
}

// RequestForVoteResult is now handled by handleVoteResultRequest
// This function was kept for backward compatibility but is deprecated

// handleAskForSubscription processes subscription request messages
func (lh *ListenerHandler) handleAskForSubscription(logger_ctx context.Context, s network.Stream, message *AVCStruct.Message) {
	// Record trace span and close it
	subSpanCtx, subSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.handleAskForSubscription")
	defer subSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	subSpan.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	logger().NamedLogger.Info(subSpanCtx, "Received subscription request from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", message.Message),
		ion.String("ack_stage", message.GetACK().GetStage()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleAskForSubscription"))

	// Check if ForListner is initialized
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.Host == nil {
		subSpan.RecordError(fmt.Errorf("ForListner not initialized"))
		subSpan.SetAttributes(attribute.String("status", "listener_not_initialized"))
		duration := time.Since(startTime).Seconds()
		subSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(subSpanCtx, "ForListner not initialized - sending rejection response",
			fmt.Errorf("ForListner not initialized"),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleAskForSubscription"))
		lh.sendSubscriptionResponse(subSpanCtx, s, false)
		return
	}

	logger().NamedLogger.Info(subSpanCtx, "ForListner is initialized - processing subscription request",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleAskForSubscription"))

	// Use config.PubSub_ConsensusChannel as the GossipSub topic
	topicToSubscribe := config.PubSub_ConsensusChannel
	subSpan.SetAttributes(attribute.String("topic_to_subscribe", topicToSubscribe))

	logger().NamedLogger.Info(subSpanCtx, "Subscribing to GossipSub topic",
		ion.String("topic", topicToSubscribe),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleAskForSubscription"))

	// CRITICAL FIX: Reuse existing GossipNode/PubSub if available
	// Creating a new GossipPubSub for every request creates a new subscription to libp2p
	// without cancelling the old one, leading to a resource leak (thousands of goroutines).
	var gps *AVCStruct.GossipPubSub
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()

	if pubSubNode != nil && pubSubNode.PubSub != nil {
		logger().NamedLogger.Info(subSpanCtx, "Reusing existing GossipPubSub instance",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleAskForSubscription"))
		gps = pubSubNode.PubSub
	} else {
		logger().NamedLogger.Info(subSpanCtx, "Creating NEW GossipPubSub instance (First time initialization)",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleAskForSubscription"))
		// Create GossipPubSub using Pubsub_Builder.go
		gps = AVCStruct.NewGossipPubSubBuilder(nil).
			SetHost(listenerNode.Host).
			SetProtocol(config.BuddyNodesMessageProtocol).
			Build()

		// Create default Buddies instance
		defaultBuddies := AVCStruct.NewBuddiesBuilder(nil)
		buddy := NewBuddyNode(subSpanCtx, listenerNode.Host, defaultBuddies, nil, gps)
		AVCStruct.NewGlobalVariables().Set_PubSubNode(buddy)
	}

	// Delegate subscription logic to SubscriptionService with config.PubSub_ConsensusChannel
	service := Service.NewSubscriptionService(gps)

	err := service.HandleStreamSubscriptionRequest(subSpanCtx, topicToSubscribe)

	if err != nil {
		subSpan.RecordError(err)
		subSpan.SetAttributes(attribute.String("status", "subscription_failed"))
		duration := time.Since(startTime).Seconds()
		subSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(subSpanCtx, "Failed to subscribe to consensus channel via SubscriptionService",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("topic", topicToSubscribe),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleAskForSubscription"))
		lh.sendSubscriptionResponse(subSpanCtx, s, false)
		return
	}

	logger().NamedLogger.Info(subSpanCtx, "Successfully subscribed to consensus channel via SubscriptionService",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("topic", topicToSubscribe),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleAskForSubscription"))

	// IMPORTANT: sendSubscriptionResponse will close the stream
	// Make sure we send the response before the stream is closed
	lh.sendSubscriptionResponse(subSpanCtx, s, true)

	duration := time.Since(startTime).Seconds()
	subSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// handleSubscriptionResponse processes subscription response messages
func (lh *ListenerHandler) handleSubscriptionResponse(logger_ctx context.Context, s network.Stream, message *AVCStruct.Message) {
	// Record trace span and close it
	responseSpanCtx, responseSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.handleSubscriptionResponse")
	defer responseSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	responseSpan.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	logger().NamedLogger.Info(responseSpanCtx, "Received subscription response from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", message.Message),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleSubscriptionResponse"))

	accepted := message.GetACK().GetStatus() == "ACK_TRUE"
	responseSpan.SetAttributes(attribute.Bool("accepted", accepted))

	logger().NamedLogger.Info(responseSpanCtx, "Subscription response from peer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.Bool("accepted", accepted),
		ion.String("status", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted]),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleSubscriptionResponse"))

	// Route the response to the ResponseHandler if available
	if lh.responseHandler != nil {
		lh.responseHandler.HandleResponse(s.Conn().RemotePeer(), accepted, "main")
		responseSpan.SetAttributes(attribute.String("response_handler", "routed"))
		logger().NamedLogger.Info(responseSpanCtx, "Successfully routed subscription response to ResponseHandler",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubscriptionResponse"))
	} else {
		responseSpan.SetAttributes(attribute.String("response_handler", "none"))
		logger().NamedLogger.Info(responseSpanCtx, "No ResponseHandler set - subscription response logged only",
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleSubscriptionResponse"))
	}

	duration := time.Since(startTime).Seconds()
	responseSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// sendSubscriptionResponse sends ACK response for subscription requests
func (lh *ListenerHandler) sendSubscriptionResponse(logger_ctx context.Context, s network.Stream, accepted bool) {
	// Record trace span and close it
	sendSpanCtx, sendSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.sendSubscriptionResponse")
	defer sendSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	sendSpan.SetAttributes(
		attribute.String("remote_peer_id", remotePeer.String()),
		attribute.Bool("accepted", accepted),
	)

	logger().NamedLogger.Info(sendSpanCtx, "Sending subscription response",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.Bool("accepted", accepted),
		ion.String("status", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted]),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.sendSubscriptionResponse"))

	host := s.Conn().LocalPeer()
	var ackBuilder *AVCStruct.ACK
	if accepted {
		ackBuilder = AVCStruct.NewACKBuilder().True_ACK_Message(host, config.Type_SubscriptionResponse)
	} else {
		ackBuilder = AVCStruct.NewACKBuilder().False_ACK_Message(host, config.Type_SubscriptionResponse)
	}

	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(host).
		SetMessage(fmt.Sprintf("Subscription %s", map[bool]string{true: "accepted", false: "rejected"}[accepted])).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackBuilder)

	messageBytes, err := json.Marshal(message)
	if err != nil {
		sendSpan.RecordError(err)
		sendSpan.SetAttributes(attribute.String("status", "marshal_failed"))
		duration := time.Since(startTime).Seconds()
		sendSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(sendSpanCtx, "Failed to marshal response",
			err,
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendSubscriptionResponse"))
		s.Close()
		return
	}

	// Send response back through the SAME stream (SubmitMessageProtocol)
	bytesWritten, err := s.Write([]byte(string(messageBytes) + string(rune(config.Delimiter))))
	if err != nil {
		sendSpan.RecordError(err)
		sendSpan.SetAttributes(attribute.String("status", "write_failed"), attribute.Int("bytes_written", bytesWritten))
		duration := time.Since(startTime).Seconds()
		sendSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(sendSpanCtx, "Failed to write response",
			err,
			ion.Int("bytes_written", bytesWritten),
			ion.String("remote_peer_id", remotePeer.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.sendSubscriptionResponse"))
		s.Close()
		return
	}

	// Use CloseWrite to signal we're done writing but allow the sender to finish reading
	// This prevents "stream reset (remote)" errors caused by closing while sender is still reading
	if closeWriter, ok := s.(interface{ CloseWrite() error }); ok {
		if err := closeWriter.CloseWrite(); err != nil {
			// If CloseWrite fails, log and close the whole stream
			logger().NamedLogger.Warn(sendSpanCtx, "CloseWrite failed, closing stream directly",
				ion.String("error", err.Error()),
				ion.String("remote_peer_id", remotePeer.String()),
				ion.String("function", "MessagePassing.sendSubscriptionResponse"))
			s.Close()
			return
		}
	}

	// Give more time for the response to be received before full close
	// 200ms accounts for network latency and sender processing time
	// This is critical for preventing stream reset errors after prolonged runtime
	time.Sleep(200 * time.Millisecond)

	s.Close()

	duration := time.Since(startTime).Seconds()
	sendSpan.SetAttributes(attribute.Float64("duration", duration), attribute.Int("bytes_written", bytesWritten), attribute.String("status", "success"))
	logger().NamedLogger.Info(sendSpanCtx, "Successfully sent subscription response",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.Bool("accepted", accepted),
		ion.Int("bytes_written", bytesWritten),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.sendSubscriptionResponse"))
}

// GetResponseHandler returns the current ResponseHandler
func (lh *ListenerHandler) GetResponseHandler() AVCStruct.ResponseHandler {
	return lh.responseHandler
}

// handleVoteResultRequest handles request for vote aggregation result from a buddy node
func (lh *ListenerHandler) handleVoteResultRequest(logger_ctx context.Context, s network.Stream, message *AVCStruct.Message) {
	// Record trace span and close it
	voteResultSpanCtx, voteResultSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.handleVoteResultRequest")
	defer voteResultSpan.End()

	startTime := time.Now().UTC()
	remotePeer := s.Conn().RemotePeer()
	voteResultSpan.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	logger().NamedLogger.Info(voteResultSpanCtx, "Received vote result request from Sequencer",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("message", message.Message),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleVoteResultRequest"))

	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		voteResultSpan.RecordError(fmt.Errorf("listener node or CRDT layer not initialized"))
		voteResultSpan.SetAttributes(attribute.String("status", "node_not_initialized"))
		duration := time.Since(startTime).Seconds()
		voteResultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteResultSpanCtx, "Listener node or CRDT layer not initialized",
			fmt.Errorf("listener node or CRDT layer not initialized"),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
		return
	}

	// Parse optional request payload (e.g., block hash scoping)
	var targetBlockHash string
	var voteResultReq struct {
		BlockHash string `json:"block_hash"`
	}
	// functions which retuning the response should return the same format
	if err := json.Unmarshal([]byte(message.Message), &voteResultReq); err == nil {
		targetBlockHash = voteResultReq.BlockHash
		if targetBlockHash != "" {
			fmt.Printf("🎯 Target block hash from request: %s\n", targetBlockHash)
		}
	} else {
		fmt.Printf("DEBUG: Vote result request payload not JSON or missing block_hash: %v\n", err)
		// If no valid JSON payload, reject to avoid mixing blocks
		ackMessage := AVCStruct.NewACKBuilder().False_ACK_Message(listenerNode.PeerID, config.Type_VoteResult)
		response := AVCStruct.NewMessageBuilder(nil).
			SetSender(listenerNode.PeerID).
			SetMessage(fmt.Sprintf(`{"error":"invalid vote result request: %v"}`, err)).
			SetTimestamp(time.Now().UTC().Unix()).
			SetACK(ackMessage)
		responseBytes, _ := json.Marshal(response)
		_, _ = s.Write([]byte(string(responseBytes) + string(rune(config.Delimiter))))
		fmt.Printf("❌ Invalid vote result request payload; rejecting\n")
		return
	}

	// Ensure buddy nodes are populated from the cached consensus message
	// This guards cases where the broadcast handler didn't run yet on this node
	if len(listenerNode.BuddyNodes.Buddies_Nodes) == 0 {
		fmt.Printf("⚠️ Buddy list empty at vote result request; attempting to populate from consensus cache\n")
		buddyIDs := make([]peer.ID, 0, config.MaxMainPeers)
		count := 0
		for _, consensusMsg := range AVCStruct.CacheConsensuMessage {
			if consensusMsg == nil || consensusMsg.Buddies == nil {
				continue
			}
			for i := 0; i < config.MaxMainPeers && i < len(consensusMsg.Buddies); i++ {
				if b, ok := consensusMsg.Buddies[i]; ok {
					if b.PeerID != listenerNode.PeerID {
						buddyIDs = append(buddyIDs, b.PeerID)
						count++
						if count >= config.MaxMainPeers {
							break
						}
					}
				}
			}
			if count >= config.MaxMainPeers {
				break
			}
		}
		if len(buddyIDs) > 0 {
			listenerNode.BuddyNodes.Buddies_Nodes = buddyIDs
			fmt.Printf("✅ Populated buddy nodes from cache: %d peers (MaxMainPeers=%d)\n", len(buddyIDs), config.MaxMainPeers)
		} else {
			fmt.Printf("⚠️ Could not populate buddy nodes from cache\n")
		}
	}
	fmt.Printf("✅ Buddy nodes populated: %v\n", listenerNode.BuddyNodes.Buddies_Nodes)

	// 🔄 CRDT SYNC: Sync CRDT data before processing votes
	fmt.Printf("🔄 Triggering CRDT sync before processing votes...\n")
	if err := TriggerCRDTSyncForBuddyNode(logger_ctx, listenerNode); err != nil {
		fmt.Printf("⚠️ CRDT sync failed, continuing with existing data: %v\n", err)
		// Don't fail the vote processing, just log the warning
	} else {
		fmt.Printf("✅ CRDT sync completed successfully\n")
		// Print CRDT content after sync
		CRDTSync.PrintCurrentCRDTContent()
	}

	// Process votes from CRDT
	result, err := Structs.ProcessVotesFromCRDT(voteResultSpanCtx, listenerNode, targetBlockHash)
	if err != nil {
		voteResultSpan.RecordError(err)
		voteResultSpan.SetAttributes(attribute.String("status", "process_votes_failed"))
		duration := time.Since(startTime).Seconds()
		voteResultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteResultSpanCtx, "Failed to process votes from CRDT",
			err,
			ion.String("target_block_hash", targetBlockHash),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
		return
	}

	voteResultSpan.SetAttributes(
		attribute.Int("vote_result", int(result)),
		attribute.String("target_block_hash", targetBlockHash),
	)

	blsResp, status, err := BLS_Signer.SignMessage(result)
	if err != nil || !status {
		voteResultSpan.RecordError(err)
		voteResultSpan.SetAttributes(attribute.String("bls_signature_status", "failed"))
		logger().NamedLogger.Warn(voteResultSpanCtx, "Failed to create BLS signature for BFT result",
			ion.String("error", err.Error()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
	} else {
		voteResultSpan.SetAttributes(attribute.String("bls_signature_status", "success"))
		logger().NamedLogger.Info(voteResultSpanCtx, "BLS signature created successfully",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
	}
	// Attach local PeerID into BLS payload
	blsResp.SetPeerID(listenerNode.PeerID.String())

	logger().NamedLogger.Info(voteResultSpanCtx, "Vote aggregation result",
		ion.Int("result", int(result)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleVoteResultRequest"))

	// Send the result back
	resultData := map[string]interface{}{
		"result": result,
		"bls":    blsResp,
	}

	resultJSON, err := json.Marshal(resultData)
	if err != nil {
		voteResultSpan.RecordError(err)
		voteResultSpan.SetAttributes(attribute.String("status", "marshal_failed"))
		duration := time.Since(startTime).Seconds()
		voteResultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteResultSpanCtx, "Failed to marshal result data",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
		return
	}

	ackMessage := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_ACK_True)
	response := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(string(resultJSON)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackMessage)

	responseBytes, err := json.Marshal(response)
	if err != nil {
		voteResultSpan.RecordError(err)
		voteResultSpan.SetAttributes(attribute.String("status", "response_marshal_failed"))
		duration := time.Since(startTime).Seconds()
		voteResultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteResultSpanCtx, "Failed to marshal response",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
		return
	}

	// Write response with delimiter
	responseWithDelimiter := string(responseBytes) + string(rune(config.Delimiter))
	n, err := s.Write([]byte(responseWithDelimiter))
	if err != nil {
		voteResultSpan.RecordError(err)
		voteResultSpan.SetAttributes(attribute.String("status", "write_failed"))
		duration := time.Since(startTime).Seconds()
		voteResultSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(voteResultSpanCtx, "Failed to write response",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
		return
	}

	// Force flush the stream
	if err := s.CloseWrite(); err != nil {
		voteResultSpan.RecordError(err)
		logger().NamedLogger.Warn(voteResultSpanCtx, "Failed to close write side",
			ion.String("error", err.Error()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.handleVoteResultRequest"))
	}

	duration := time.Since(startTime).Seconds()
	voteResultSpan.SetAttributes(attribute.Float64("duration", duration), attribute.Int("bytes_written", n), attribute.String("status", "success"))
	logger().NamedLogger.Info(voteResultSpanCtx, "Successfully sent vote result to Sequencer",
		ion.Int("result", int(result)),
		ion.String("remote_peer_id", remotePeer.String()),
		ion.Int("bytes_written", n),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.handleVoteResultRequest"))
}

func (lh *ListenerHandler) TriggerForBFTFromSequencer(s network.Stream, message *AVCStruct.Message, buddies []peer.ID) {
	if ListenerHandlerLocal == nil {
		var err error
		ListenerHandlerLocal, err = common.InitializeGRO(GRO.HandleBFTRequestLocal)
		if err != nil {
			fmt.Printf("❌ Failed to initialize ListenerHandler local manager: %v\n", err)
			return
		}
	}
	defer s.Close()

	fmt.Println("📩 Received BFT trigger from Sequencer:", message)

	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		fmt.Println("❌ Listener node not initialized")
		return
	}

	// Get buddy list from global config or BFT context
	if len(buddies) == 0 {
		fmt.Println("⚠️ No buddies found to request vote results")
		return
	}

	fmt.Printf("🚀 Triggering BFT across %d buddy nodes\n", len(buddies))
	fmt.Printf("📍 Listener PeerID: %s\n", listenerNode.PeerID.String())
	fmt.Printf("📍 Listener Host ID: %s\n", listenerNode.Host.ID().String())
	fmt.Printf("📋 All buddies received: %v\n", buddies)

	// Filter out self from buddies to avoid "dial to self attempted" error
	filteredBuddies := make([]peer.ID, 0, len(buddies))
	listenerIDStr := listenerNode.PeerID.String()
	listenerHostIDStr := listenerNode.Host.ID().String()

	// Also check PubSubNode peer ID in case it's different
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	var currentPeerID peer.ID
	var currentPeerIDStr string
	if pubSubNode != nil {
		currentPeerID = pubSubNode.PeerID
		currentPeerIDStr = currentPeerID.String()
		fmt.Printf("📍 PubSub PeerID: %s\n", currentPeerIDStr)
		if pubSubNode.Host != nil {
			fmt.Printf("📍 PubSub Host ID: %s\n", pubSubNode.Host.ID().String())
		}
	} else {
		currentPeerID = listenerNode.PeerID
		currentPeerIDStr = currentPeerID.String()
	}

	for _, b := range buddies {
		buddyIDStr := b.String()
		// Compare against all possible IDs (listener peer, host, pubsub peer and host)
		isListener := buddyIDStr == listenerIDStr
		isListenerHost := buddyIDStr == listenerHostIDStr
		isPubSub := buddyIDStr == currentPeerIDStr

		if !isListener && !isListenerHost && !isPubSub {
			// Also check if it matches PubSub host ID
			isPubSubHost := false
			if pubSubNode != nil && pubSubNode.Host != nil {
				isPubSubHost = buddyIDStr == pubSubNode.Host.ID().String()
			}

			if !isPubSubHost {
				filteredBuddies = append(filteredBuddies, b)
				fmt.Printf("✅ Including buddy: %s\n", buddyIDStr)
			} else {
				fmt.Printf("⚠️ Filtering out self: %s (matches PubSub host)\n", buddyIDStr)
			}
		} else {
			matched := ""
			if isListener {
				matched = "listener peer"
			}
			if isListenerHost {
				if matched != "" {
					matched += ", "
				}
				matched += "listener host"
			}
			if isPubSub {
				if matched != "" {
					matched += ", "
				}
				matched += "pubsub"
			}
			fmt.Printf("⚠️ Filtering out self: %s (matches %s)\n", buddyIDStr, matched)
		}
	}

	if len(filteredBuddies) == 0 {
		fmt.Println("⚠️ No valid buddy nodes (all are self)")
		return
	}

	fmt.Printf("📊 Filtered to %d valid buddy nodes (excluding self)\n", len(filteredBuddies))

	// Send acknowledgment to sequencer
	ack := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_SubmitVote)
	response := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage("BFT started across buddies").
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ack)
	data, _ := json.Marshal(response)
	data = append(data, byte(config.Delimiter))
	s.Write(data)

	// ✅ Send RequestForVoteResult to all buddies in parallel
	// Track successful responses to ensure we meet the minimum requirement
	responsesReceived := 0
	var responsesMutex sync.Mutex

	responseCh := make(chan bool, len(filteredBuddies))
	wg, err := ListenerHandlerLocal.NewFunctionWaitGroup(context.Background(), GRO.BFTWaitGroup)
	if err != nil {
		fmt.Printf("❌ Failed to create waitgroup: %v\n", err)
		return
	}

	for _, b := range filteredBuddies {
		buddyID := b
		if err := ListenerHandlerLocal.Go(GRO.BFTSendRequestThread, func(ctx context.Context) error {
			// Use SubmitMessageProtocol because HandleSubmitMessageStream routes Type_VoteResult
			stream, err := listenerNode.Host.NewStream(ctx, buddyID, config.SubmitMessageProtocol)
			if err != nil {
				fmt.Printf("❌ Failed to open stream to %s: %v\n", buddyID, err)
				responseCh <- false
				return nil
			}
			defer func() {
				stream.Close()
				fmt.Printf("🔌 Closed stream to %s\n", buddyID)
			}()

			reqAck := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_VoteResult)
			reqMsg := AVCStruct.NewMessageBuilder(nil).
				SetSender(listenerNode.PeerID).
				SetMessage("RequestForVoteResult").
				SetTimestamp(time.Now().UTC().Unix()).
				SetACK(reqAck)

			reqData, _ := json.Marshal(reqMsg)
			reqData = append(reqData, byte(config.Delimiter))
			if _, err := stream.Write(reqData); err != nil {
				fmt.Printf("❌ Failed to send RequestForVoteResult to %s: %v\n", buddyID, err)
				responseCh <- false
				return nil
			}
			fmt.Printf("📨 Sent RequestForVoteResult to %s\n", buddyID)

			// Wait for the vote result
			readCh := make(chan []byte, 1)
			readErrCh := make(chan error, 1)

			// Read from stream in a goroutine (can't use LocalGRO here as it's a blocking read)
			ListenerHandlerLocal.Go(GRO.BFTSendRequestThread, func(ctx context.Context) error {
				buf := make([]byte, 0)
				tmp := make([]byte, 1024)
				for {
					n, err := stream.Read(tmp)
					if err != nil {
						readErrCh <- err
						close(readCh)
						return nil
					}
					buf = append(buf, tmp[:n]...)
					if bytes.Contains(buf, []byte{byte(config.Delimiter)}) {
						readCh <- bytes.TrimSuffix(buf, []byte{byte(config.Delimiter)})
						return nil
					}
				}
			})

			timeoutTimer := time.NewTimer(5 * time.Second)
			defer timeoutTimer.Stop()

			select {
			case <-ctx.Done():
				fmt.Printf("⏳ Context cancelled while waiting for vote result from %s\n", buddyID)
				responseCh <- false
				return ctx.Err()
			case payload := <-readCh:
				if payload == nil {
					fmt.Printf("⚠️ No response from %s (nil payload)\n", buddyID)
					responseCh <- false
					return nil
				}

				fmt.Printf("📥 Received payload from %s: %d bytes\n", buddyID, len(payload))
				fmt.Printf("📝 Payload content: %s\n", string(payload))

				var msg AVCStruct.Message
				if err := json.Unmarshal(payload, &msg); err == nil {
					fmt.Printf("✅ Parsed vote result message from %s\n", buddyID)
					fmt.Printf("   Message content: %s\n", msg.Message)

					// Parse and store the vote result directly
					var resultData map[string]interface{}
					if err := json.Unmarshal([]byte(msg.Message), &resultData); err == nil {
						if result, ok := resultData["result"].(float64); ok {
							voteResult := int8(result)
							Maps.StoreVoteResult(buddyID.String(), voteResult)
							fmt.Printf("✅ Stored vote result for peer %s: %d\n", buddyID.String(), voteResult)
							responsesMutex.Lock()
							responsesReceived++
							count := responsesReceived
							responsesMutex.Unlock()

							// Check if we've reached the minimum requirement
							if count >= config.MaxMainPeers {
								fmt.Printf("✅ Reached minimum requirement: %d/%d responses\n", count, config.MaxMainPeers)
							}
							responseCh <- true
							return nil
						}
					}
					fmt.Printf("⚠️ Failed to parse vote result from %s\n", buddyID)
					responseCh <- false
				} else {
					fmt.Printf("⚠️ Invalid response from %s: %s\n", buddyID, string(payload))
					responseCh <- false
				}
			case <-readErrCh:
				fmt.Printf("⚠️ Error reading from stream for %s\n", buddyID)
				responseCh <- false
			case <-timeoutTimer.C:
				fmt.Printf("⏳ Timeout waiting for vote result from %s\n", buddyID)
				responseCh <- false
			}
			return nil
		}, local.AddToWaitGroup(GRO.BFTWaitGroup)); err != nil {
			fmt.Printf("❌ Failed to start goroutine for buddy %s: %v\n", buddyID, err)
		}
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(responseCh)

	// Wait for responses and check if we have enough
	for success := range responseCh {
		if !success {
			continue
		}
	}

	responsesMutex.Lock()
	finalCount := responsesReceived
	responsesMutex.Unlock()

	fmt.Printf("✅ Collected vote results from %d/%d nodes\n", finalCount, len(filteredBuddies))

	// Check if we have enough responses for consensus
	if finalCount < config.MaxMainPeers {
		fmt.Printf("⚠️ WARNING: Only received %d responses, but need at least %d for consensus\n", finalCount, config.MaxMainPeers)
		fmt.Printf("⚠️ This may cause consensus failures. Consider increasing backup nodes.\n")
	}
}
