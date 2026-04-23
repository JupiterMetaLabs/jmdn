package Service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	common "gossipnode/AVC/BuddyNodes/common"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
)

var LocalGRO interfaces.LocalGoroutineManagerInterface

// Decision represents a BFT vote decision (avoid importing bft package)
type Decision string

const (
	Accept Decision = "ACCEPT"
	Reject Decision = "REJECT"
)

var (
	voteProcessingTriggered = false
	voteProcessingMutex     sync.Mutex
)

// BuddyInput represents buddy input data (avoid importing bft package)
type BuddyInput struct {
	ID        string
	Decision  Decision
	PublicKey []byte
}

// Result represents consensus result (avoid importing bft package)
type Result struct {
	Success       bool
	BlockAccepted bool
	Decision      Decision
}

// BFTMessageHandler defines the interface for BFT message handling
type BFTMessageHandler interface {
	HandleStartPubSub(msg *AVCStruct.GossipMessage) error
	HandleEndPubSub(msg *AVCStruct.GossipMessage) error
	HandlePrepareVote(msg *AVCStruct.GossipMessage) error
	HandleCommitVote(msg *AVCStruct.GossipMessage) error
	ProposeConsensus(ctx context.Context, round uint64, blockHash string, myBuddyID string, allBuddies []BuddyInput) (*Result, error)
}

// BFTAdapterFactory is a function type for creating BFT adapters
type BFTAdapterFactory func(
	ctx context.Context,
	pubSub *AVCStruct.GossipPubSub,
	channelName string,
) (BFTMessageHandler, error)

// SubscriptionService handles subscription-related operations
type SubscriptionService struct {
	pubSub         *AVCStruct.GossipPubSub
	bftAdapter     BFTMessageHandler
	myBuddyID      string
	adapterFactory BFTAdapterFactory
}

// NewSubscriptionService creates a new subscription service (BACKWARD COMPATIBLE)
func NewSubscriptionService(pubSub *AVCStruct.GossipPubSub, optionalParams ...interface{}) *SubscriptionService {
	service := &SubscriptionService{
		pubSub: pubSub,
	}

	// Parse optional parameters
	for _, param := range optionalParams {
		switch v := param.(type) {
		case string:
			service.myBuddyID = v
		case BFTAdapterFactory:
			service.adapterFactory = v
		}
	}

	// Set default buddy ID if not provided
	if service.myBuddyID == "" && pubSub != nil {
		service.myBuddyID = pubSub.Host.ID().String()
	}

	return service
}

// SetBFTAdapterFactory allows setting the factory after creation
func (s *SubscriptionService) SetBFTAdapterFactory(factory BFTAdapterFactory) {
	s.adapterFactory = factory
}

// SetBFTAdapter sets the BFT adapter for handling consensus messages
func (s *SubscriptionService) SetBFTAdapter(adapter BFTMessageHandler) {
	s.bftAdapter = adapter
}

// handleReceivedMessage processes received pubsub messages
func (s *SubscriptionService) handleReceivedMessage(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = common.InitializeGRO(GRO.CRDTSyncLocal)
		if err != nil {
			return errors.New("failed to initialize local gro: " + err.Error())
		}
	}

	logger().NamedLogger.Info(logger_ctx, "Processing received pubsub message",
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("message_id", msg.ID),
		ion.String("sender", string(msg.Sender)),
		ion.String("function", "SubscriptionService.handleReceivedMessage"))

	// Check if the message has valid data
	if msg.Data == nil {
		return errors.New("received message has no data")
	}
	logger().NamedLogger.Info(logger_ctx, "Message",
		ion.String("message", string(msg.Data.Message)),
		ion.String("function", "SubscriptionService.handleReceivedMessage"))

	// Attach ACK if missing
	if msg.Data.ACK == nil {
		logger().NamedLogger.Error(logger_ctx,
			"Received message with nil ACK - attaching default ACK",
			nil,
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		// Detect if this is a vote message by checking the message content
		ackStage := config.Type_Publish
		if msg.Data.Message != "" {
			var voteData map[string]interface{}
			if err := json.Unmarshal([]byte(msg.Data.Message), &voteData); err == nil {
				if _, isVote := voteData["vote"]; isVote {
					ackStage = config.Type_SubmitVote
					logger().NamedLogger.Info(logger_ctx, "Detected vote message - setting ACK stage to Type_SubmitVote",
						ion.String("topic", config.PubSub_ConsensusChannel),
						ion.String("function", "SubscriptionService.handleReceivedMessage"))
				}
			}
		}

		// Create ACK with appropriate stage
		ack := AVCStruct.NewACKBuilder().
			True_ACK_Message(msg.Sender, ackStage)

		msg.Data.SetACK(ack)
	}

	// Process the message based on its type
	switch msg.Data.ACK.Stage {

	// ========== BFT CONSENSUS MESSAGES ==========
	case config.Type_BFTRequest:
		logger().NamedLogger.Info(logger_ctx, "Processing BFT_REQUEST from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTRequest(logger_ctx, msg)

	case config.Type_StartPubSub:
		logger().NamedLogger.Info(logger_ctx, "Processing START_PUBSUB from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("round_id", msg.Data.RoundID),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		if s.bftAdapter != nil {
			return s.bftAdapter.HandleStartPubSub(msg)
		}
		return nil

	case config.Type_EndPubSub:
		logger().NamedLogger.Info(logger_ctx, "Processing END_PUBSUB from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("round_id", msg.Data.RoundID),
			ion.Bool("success", msg.Data.ConsensusSuccess),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		// CRITICAL FIX: Always unsubscribe when receiving END_PUBSUB to prevent resource accumulation
		// This MUST happen regardless of whether bftAdapter exists
		if err := Connector.Unsubscribe(s.pubSub, config.PubSub_ConsensusChannel); err != nil {
			logger().NamedLogger.Warn(logger_ctx, "Failed to unsubscribe from consensus channel (non-fatal)",
				ion.String("error", err.Error()),
				ion.String("topic", config.PubSub_ConsensusChannel),
				ion.String("function", "SubscriptionService.handleReceivedMessage"))
			// Don't return error - cleanup failure shouldn't stop processing
		} else {
			logger().NamedLogger.Info(logger_ctx, "Successfully unsubscribed from consensus channel",
				ion.String("topic", config.PubSub_ConsensusChannel),
				ion.String("round_id", msg.Data.RoundID),
				ion.String("function", "SubscriptionService.handleReceivedMessage"))
		}

		if s.bftAdapter != nil {
			return s.bftAdapter.HandleEndPubSub(msg)
		}
		return nil

	case config.Type_SubmitVote:
		logger().NamedLogger.Info(logger_ctx, "Processing SUBMIT_VOTE from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("round_id", msg.Data.RoundID),
			ion.String("phase", msg.Data.Phase),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		// Check if this is our own vote to prevent self-loops
		globalVars := AVCStruct.NewGlobalVariables()
		listenerNode := globalVars.Get_ForListner()

		if listenerNode != nil && msg.Data.Sender == listenerNode.PeerID {
			logger().NamedLogger.Info(logger_ctx, "Skipping own vote (self-loop prevention)",
				ion.String("topic", config.PubSub_ConsensusChannel),
				ion.String("vote_from", msg.Data.Sender.String()),
				ion.String("vote_message", msg.Data.Message),
				ion.String("function", "SubscriptionService.handleReceivedMessage"))
			return nil // Don't process own vote from pubsub
		}

		logger().NamedLogger.Info(logger_ctx, "Received vote message via pubsub",
			ion.String("to_buddy_node", listenerNode.PeerID.String()),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("message_id", msg.ID),
			ion.String("sender", msg.Data.Sender.String()),
			ion.String("vote_message", msg.Data.Message),
			ion.String("channel", msg.Topic),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		// listenerNode was already retrieved above for self-loop check
		if listenerNode == nil || listenerNode.CRDTLayer == nil {
			logger().NamedLogger.Error(logger_ctx, "Listener node or CRDT layer not initialized", nil,
				ion.String("function", "SubscriptionService.handleReceivedMessage"))
			return errors.New("listener node or CRDT layer not initialized")
		}

		// Add vote to CRDT directly
		voteData := make(map[string]interface{})
		if err := json.Unmarshal([]byte(msg.Data.Message), &voteData); err != nil {
			logger().NamedLogger.Error(logger_ctx, "Failed to unmarshal vote message", err,
				ion.String("function", "SubscriptionService.handleReceivedMessage"))
			return errors.New("failed to unmarshal vote message: " + err.Error())
		}

		if _, exists := voteData["vote"]; exists {
			// Extract block hash from vote for proper scoping
			blockHashRaw, hasBlockHash := voteData["block_hash"]
			blockHash, _ := blockHashRaw.(string)
			if !hasBlockHash || blockHash == "" {
				logger().NamedLogger.Error(logger_ctx, "Vote missing block_hash; skipping vote processing trigger", nil,
					ion.String("function", "SubscriptionService.handleReceivedMessage"))
				blockHash = "" // Will cause processVotesAndTriggerBFT to skip processing
			}

			// Use the sender's peer ID as the CRDT set key to separate votes by sender
			OP := &Types.OP{
				NodeID: msg.Data.Sender,
				OpType: int8(1), // 1 for add, -1 for remove
				KeyValue: Types.KeyValue{
					Key:   msg.Data.Sender.String(), // Use peer ID as the key to separate votes by sender
					Value: msg.Data.Message,
				},
			}

			result := ServiceLayer.Controller(listenerNode.CRDTLayer, OP)
			if err, ok := result.(error); ok && err != nil {
				logger().NamedLogger.Error(logger_ctx, "Failed to add vote to CRDT", err,
					ion.String("function", "SubscriptionService.handleReceivedMessage"))
				return errors.New("failed to add vote to local CRDT Engine: " + err.Error())
			}

			logger().NamedLogger.Info(logger_ctx, "Successfully added vote to CRDT",
				ion.String("topic", config.PubSub_ConsensusChannel),
				ion.String("sender", msg.Data.Sender.String()),
				ion.String("function", "SubscriptionService.handleReceivedMessage"))

			// Only trigger vote processing once (check if already triggered)
			voteProcessingMutex.Lock()
			if !voteProcessingTriggered {
				voteProcessingTriggered = true
				voteProcessingMutex.Unlock()
				// Trigger vote processing after a delay to collect more votes
				// Increased from 10s to 30s to handle network delays and ensure votes are collected
				LocalGRO.Go(GRO.BuddyNodesMessageProtocolThread, func(ctx context.Context) error {
					time.Sleep(30 * time.Second) // Wait 30 seconds to collect more votes
					processVotesAndTriggerBFT(logger_ctx, listenerNode, blockHash)
					voteProcessingMutex.Lock()
					voteProcessingTriggered = false // Reset flag after processing
					voteProcessingMutex.Unlock()
					return nil
				})
			} else {
				voteProcessingMutex.Unlock()
			}
		}

		return nil

	case config.Type_Publish:
		// Check if it's a BFT vote (has Phase and RoundID)
		if msg.Data.Phase != "" && msg.Data.RoundID != "" {
			logger().NamedLogger.Info(logger_ctx, "Processing BFT vote via PUBLISH",
				ion.String("topic", config.PubSub_ConsensusChannel),
				ion.String("round_id", msg.Data.RoundID),
				ion.String("phase", msg.Data.Phase),
				ion.String("function", "SubscriptionService.handleReceivedMessage"))

			return s.handleVoteSubmission(logger_ctx, msg)
		}

		// Regular publish message
		logger().NamedLogger.Info(logger_ctx, "Processing publish message from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		return nil

	case config.Type_AskForSubscription:
		logger().NamedLogger.Info(logger_ctx, "Processing subscription request from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		return s.handleSubscriptionRequest(logger_ctx, msg)

	case config.Type_ToBeProcessed:
		logger().NamedLogger.Info(logger_ctx, "Processing TO_BE_PROCESSED message",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("message_id", msg.ID),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		return nil

	case config.Type_VerifySubscription:
		// Prevent self-loops
		if msg.Sender == s.pubSub.Host.ID() {
			return nil
		}

		// Prevent responding to other nodes' responses
		// The Sequencer sends "Verify Subscription", we reply with "Subscription verified"
		// We only want to answer the request, not other responses
		if msg.Data.Message == "Subscription verified" {
			return nil
		}

		logger().NamedLogger.Info(logger_ctx, "Processing verify subscription request from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("sender", msg.Sender.String()),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))

		return s.handleVerifySubscriptionRequest(logger_ctx, msg)

	default:
		// Debugging in the default case
		logger().NamedLogger.Info(logger_ctx, "Received message with unknown stage",
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return nil
	}
}

// ========== BFT HANDLER METHODS ==========

func (s *SubscriptionService) handleVoteSubmission(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	if s.bftAdapter == nil {
		logger().NamedLogger.Info(logger_ctx, "BFT adapter not set, ignoring vote",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleVoteSubmission"))
		return nil
	}

	// Route to appropriate handler based on phase
	switch msg.Data.Phase {
	case "PREPARE":
		return s.handlePrepareVote(logger_ctx, msg)
	case "COMMIT":
		return s.handleCommitVote(logger_ctx, msg)
	}

	logger().NamedLogger.Info(logger_ctx, "Unknown vote phase",
		ion.String("phase", msg.Data.Phase),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.handleVoteSubmission"))
	return nil
}

func (s *SubscriptionService) handlePrepareVote(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Processing PREPARE vote",
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("round_id", msg.Data.RoundID),
		ion.String("sender", msg.Sender.String()),
		ion.String("function", "SubscriptionService.handlePrepareVote"))

	if s.bftAdapter != nil {
		return s.bftAdapter.HandlePrepareVote(msg)
	}

	return nil
}

func (s *SubscriptionService) handleCommitVote(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Processing COMMIT vote",
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("round_id", msg.Data.RoundID),
		ion.String("sender", msg.Sender.String()),
		ion.String("function", "SubscriptionService.handleCommitVote"))

	if s.bftAdapter != nil {
		return s.bftAdapter.HandleCommitVote(msg)
	}

	return nil
}

// ========== EXISTING METHODS ==========
// handleSubscriptionRequest processes subscription requests from other nodes
func (s *SubscriptionService) handleSubscriptionRequest(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Handling subscription request from pubsub",
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("sender", string(msg.Sender)),
		ion.String("function", "SubscriptionService.handleSubscriptionRequest"))

	// In a real implementation, you would:
	// 1. Validate the requesting node
	// 2. Check if the node is authorized to subscribe
	// 3. Add the node to your buddy list
	// 4. Send a response back to the requesting node

	// For now, we'll just log the request
	logger().NamedLogger.Info(logger_ctx, "Subscription request received from",
		ion.String("sender", string(msg.Sender)),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.handleSubscriptionRequest"))

	return nil
}

// HandleStreamSubscriptionRequest handles subscription requests received via stream
// This method can be called from ListenerHandler to delegate subscription processing
func (s *SubscriptionService) HandleStreamSubscriptionRequest(logger_ctx context.Context, channelName string) error {
	if s.pubSub == nil {
		err := errors.New("SubscriptionService.HandleStreamSubscriptionRequest - pubsub not available")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("channel", channelName),
			ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))
		return err
	}

	// start trace
	tracer := logger().NamedLogger.Tracer("SubscriptionService")
	logger_ctx, span := tracer.Start(logger_ctx, "SubscriptionService.HandleStreamSubscriptionRequest")
	defer span.End()

	logger().NamedLogger.Info(logger_ctx, "Handling stream subscription request",
		ion.String("channel", channelName),
		ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))

	// Define the consensus channel in this node's pubsub instance if it doesn't exist
	s.pubSub.Mutex.Lock()
	if _, exists := s.pubSub.ChannelAccess[channelName]; !exists {
		// Channel doesn't exist, create it as public so this node can subscribe
		allowedMap := make(map[peer.ID]bool)
		allowedMap[s.pubSub.Host.ID()] = true

		s.pubSub.ChannelAccess[channelName] = &AVCStruct.ChannelAccess{
			ChannelName:  channelName,
			AllowedPeers: allowedMap,
			IsPublic:     true, // Make it public so node can subscribe
			Creator:      s.pubSub.Host.ID(),
		}

		logger().NamedLogger.Info(logger_ctx, "Created channel locally for peer",
			ion.String("channel", channelName),
			ion.String("peer", s.pubSub.Host.ID().String()),
			ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))
	}
	s.pubSub.Mutex.Unlock()

	// Use the Connector.Subscribe to handle the subscription properly with GossipSub
	// This ensures messages are received via GossipSub
	logger().NamedLogger.Info(logger_ctx, "About to call Connector.Subscribe for",
		ion.String("channel", channelName),
		ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))
	err := Connector.Subscribe(logger_ctx, s.pubSub, channelName, func(msg *AVCStruct.GossipMessage) {
		logger().NamedLogger.Info(logger_ctx, "Received message on consensus channel",
			ion.String("channel", channelName),
			ion.String("message_id", msg.ID),
			ion.String("sender", msg.Sender.String()),
			ion.String("topic", msg.Topic),
			ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))

		logger().NamedLogger.Info(logger_ctx, "Received message on consensus channel",
			ion.String("channel", channelName),
			ion.String("message_id", msg.ID),
			ion.String("sender", msg.Sender.String()),
			ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))

		// Handle the received message by processing it through the message router
		if err := s.handleReceivedMessage(logger_ctx, msg); err != nil {
			logger().NamedLogger.Error(logger_ctx, "Failed to handle received message", err,
				ion.String("channel", channelName),
				ion.String("message_id", msg.ID),
				ion.String("sender", msg.Sender.String()),
				ion.String("topic", msg.Topic),
				ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))
		}
	})

	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to subscribe to consensus channel", err,
			ion.String("channel", channelName),
			ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))
		return errors.New("failed to subscribe to " + channelName + ": " + err.Error())
	}

	logger().NamedLogger.Info(logger_ctx, "Successfully subscribed to consensus channel",
		ion.String("channel", channelName),
		ion.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))

	return nil
}

/* UNUSED
func (s *SubscriptionService) subscribeToTopic(topic string, handler func(*AVCStruct.GossipMessage)) error {
	if s.pubSub == nil {
		return errors.New("SubscriptionService.subscribeToTopic - pubsub not available")
	}

	hostID := s.pubSub.Host.ID()
	if !s.canSubscribe(topic, hostID) {
		return errors.New("SubscriptionService.subscribeToTopic - access denied: not authorized to subscribe to channel " + topic)
	}

	s.pubSub.Mutex.Lock()
	defer s.pubSub.Mutex.Unlock()

	s.pubSub.Topics[topic] = true
	s.pubSub.Handlers[topic] = handler
	return nil
}

func (s *SubscriptionService) unsubscribeFromTopic(topic string) error {
	if s.pubSub == nil {
		return errors.New("SubscriptionService.unsubscribeFromTopic - pubsub not available")
	}

	s.pubSub.Mutex.Lock()
	defer s.pubSub.Mutex.Unlock()

	delete(s.pubSub.Topics, topic)
	delete(s.pubSub.Handlers, topic)
	return nil
}

func (s *SubscriptionService) canSubscribe(channelName string, peerID peer.ID) bool {
	if s.pubSub == nil {
		return false
	}

	s.pubSub.Mutex.RLock()
	defer s.pubSub.Mutex.RUnlock()

	access, exists := s.pubSub.ChannelAccess[channelName]
	if !exists {
		return false
	}

	if access.IsPublic {
		return true
	}

	return access.AllowedPeers[peerID]
}
*/

func (s *SubscriptionService) GetMyBuddyID() string {
	if s.myBuddyID != "" {
		return s.myBuddyID
	}
	return s.pubSub.Host.ID().String()
}

func (s *SubscriptionService) handleBFTRequest(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	// If no factory is set, just log and return
	if s.adapterFactory == nil {
		logger().NamedLogger.Info(logger_ctx, "BFT adapter factory not configured, ignoring BFT request",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleBFTRequest"))
		return nil
	}

	var reqData struct {
		Round          uint64
		BlockHash      string
		GossipsubTopic string
		AllBuddies     []struct {
			ID        string
			Decision  string
			PublicKey []byte
		}
	}

	if err := json.Unmarshal([]byte(msg.Data.Message), &reqData); err != nil {
		return errors.New("failed to parse BFT request: " + err.Error())
	}

	myBuddyID := s.GetMyBuddyID()
	amIaBuddy := false

	buddies := make([]BuddyInput, 0)
	for _, buddy := range reqData.AllBuddies {
		decision := Accept
		if buddy.Decision == "REJECT" {
			decision = Reject
		}

		buddies = append(buddies, BuddyInput{
			ID:        buddy.ID,
			Decision:  decision,
			PublicKey: buddy.PublicKey,
		})

		if buddy.ID == myBuddyID {
			amIaBuddy = true
		}
	}

	if !amIaBuddy {
		logger().NamedLogger.Info(logger_ctx, "Not in buddy list, skipping consensus",
			ion.Int64("round", int64(reqData.Round)),
			ion.String("function", "SubscriptionService.handleBFTRequest"))
		return nil
	}

	logger().NamedLogger.Info(logger_ctx, "I'm a buddy! Starting consensus",
		ion.Int64("round", int64(reqData.Round)),
		ion.String("function", "SubscriptionService.handleBFTRequest"))

	LocalGRO.Go(GRO.BuddyNodesMessageProtocolThread, func(ctx context.Context) error {
		if s.bftAdapter == nil {
			adapter, err := s.adapterFactory(
				context.Background(),
				s.pubSub,
				reqData.GossipsubTopic,
			)
			if err != nil {
				logger().NamedLogger.Error(logger_ctx, "Failed to create BFT adapter", err,
					ion.String("function", "SubscriptionService.handleBFTRequest"))
				return errors.New("failed to create BFT adapter: " + err.Error())
			}
			s.bftAdapter = adapter
		}

		result, err := s.bftAdapter.ProposeConsensus(
			context.Background(),
			reqData.Round,
			reqData.BlockHash,
			myBuddyID,
			buddies,
		)

		if err != nil {
			logger().NamedLogger.Error(logger_ctx, "Consensus failed", err,
				ion.Int64("round", int64(reqData.Round)),
				ion.String("function", "SubscriptionService.handleBFTRequest"))
		} else {
			logger().NamedLogger.Info(logger_ctx, "Consensus completed successfully",
				ion.Int64("round", int64(reqData.Round)),
				ion.String("decision", string(result.Decision)),
				ion.Bool("accepted", result.BlockAccepted),
				ion.String("function", "SubscriptionService.handleBFTRequest"))
		}
		return err
	})

	return nil
}

// processVotesAndTriggerBFT processes votes from CRDT and triggers BFT consensus
// If blockHash is empty, processing is skipped to avoid mixing votes from different blocks
func processVotesAndTriggerBFT(logger_ctx context.Context, listenerNode *AVCStruct.BuddyNode, blockHash string) {
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		err := errors.New("cannot process votes - listener node or CRDT layer not initialized")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("function", "SubscriptionService.processVotesAndTriggerBFT"))
		return
	}

	if blockHash == "" {
		logger().NamedLogger.Error(logger_ctx, "Cannot Scope Votes",
			errors.New("skipping vote processing - block hash not available (cannot scope votes)"),
			ion.String("function", "SubscriptionService.processVotesAndTriggerBFT"))
		return
	}

	logger().NamedLogger.Info(logger_ctx, "Processing votes and triggering BFT",
		ion.String("block_hash", blockHash),
		ion.String("function", "SubscriptionService.processVotesAndTriggerBFT"))

	// Process votes from CRDT with block hash filtering
	result, err := Structs.ProcessVotesFromCRDT(logger_ctx, listenerNode, blockHash)
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to process votes from CRDT", err,
			ion.String("function", "SubscriptionService.processVotesAndTriggerBFT"))
		return
	}

	// Get the vote result and use it for BFT consensus
	// The result is: 1 for accept, -1 for reject
	bftDecision := "REJECT"
	if result > 0 {
		bftDecision = "ACCEPT"

	}

	logger().NamedLogger.Info(logger_ctx, "Vote Result from VoteAggregation",
		ion.Int64("result", int64(result)),
		ion.String("bft_decision", bftDecision),
		ion.String("function", "SubscriptionService.processVotesAndTriggerBFT"))

	// Send vote result back to the sequencer
	// sendVoteResultToSequencer(listenerNode, result)

	// BFT will be triggered elsewhere (from ListenerHandler)
	logger().NamedLogger.Info(logger_ctx, "Vote processing completed, BFT will be triggered",
		ion.String("function", "SubscriptionService.processVotesAndTriggerBFT"))
}

/* UNUSED
// sendVoteResultToSequencer sends the vote result back to the sequencer via SubmitMessageProtocol
func sendVoteResultToSequencer(logger_ctx context.Context, listenerNode *AVCStruct.BuddyNode, result int8) {
	logger().NamedLogger.Info(logger_ctx, "Sending vote result to sequencer",
		ion.String("listener_node", listenerNode.PeerID.String()),
		ion.Int64("result", int64(result)),
		ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))

	if listenerNode == nil || listenerNode.PeerID == "" {
		logger().NamedLogger.Error(logger_ctx, "Cannot send vote result - listener node not initialized",
			errors.New("Cannot send vote result - listener node not initialized"),
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	// Get the host from the current buddy node
	if listenerNode.Host == nil {
		logger().NamedLogger.Error(logger_ctx, "Cannot send vote result - buddy node host not initialized",
			errors.New("Cannot send vote result - buddy node host not initialized"),
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	// Get the sequencer peer ID from the subscription cache
	// The sequencer is the first peer in the cache (from when we subscribed)
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if pubSubNode == nil || len(pubSubNode.BuddyNodes.Buddies_Nodes) == 0 {
		logger().NamedLogger.Error(logger_ctx, "Cannot send vote result - no sequencer peer found",
			errors.New("Cannot send vote result - no sequencer peer found"),
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	// Find a valid sequencer peer (not self)
	var sequencerPeerID peer.ID
	found := false
	currentPeerID := listenerNode.PeerID
	currentHostID := listenerNode.Host.ID()

	for _, peerID := range pubSubNode.BuddyNodes.Buddies_Nodes {
		// Skip if this is our own peer ID
		if peerID == currentPeerID || peerID == currentHostID {
			logger().NamedLogger.Info(logger_ctx, "Skipping self-peer",
				ion.String("peer_id", peerID.String()),
				ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
			continue
		}
		sequencerPeerID = peerID
		found = true
		break
	}

	if !found {
		logger().NamedLogger.Error(logger_ctx, "Cannot send vote result - no valid sequencer peer found (all are self)",
			errors.New("Cannot send vote result - no valid sequencer peer found (all are self)"),
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	host := listenerNode.Host

	logger().NamedLogger.Info(logger_ctx, "Sending vote result",
		ion.Int64("result", int64(result)),
		ion.String("sequencer_peer_id", sequencerPeerID.String()),
		ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))

	// Create vote result message
	resultMessageBytes, err := json.Marshal(map[string]interface{}{
		"result":    result,
		"timestamp": time.Now().UTC().Unix(),
		"node":      listenerNode.PeerID.String(),
	})

	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to marshal result message", err,
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	resultMessage := string(resultMessageBytes)

	ackMessage := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_VoteResult)
	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(resultMessage).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackMessage)

	// Open a stream to the sequencer
	stream, err := host.NewStream(context.Background(), sequencerPeerID, config.SubmitMessageProtocol)
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to open stream to sequencer", err,
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}
	defer stream.Close()

	// Serialize and send the message
	messageBytes, err := json.Marshal(message)
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to marshal message", err,
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	writer := bufio.NewWriter(stream)
	_, err = writer.WriteString(string(messageBytes) + string(rune(config.Delimiter)))
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to write message", err,
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	err = writer.Flush()
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to flush message", err,
			ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
		return
	}

	logger().NamedLogger.Info(logger_ctx, "Vote result sent to sequencer successfully",
		ion.Int64("result", int64(result)),
		ion.String("message", resultMessage),
		ion.String("function", "SubscriptionService.sendVoteResultToSequencer"))
}
*/

// handleVerifySubscriptionRequest processes verification requests from the sequencer
func (s *SubscriptionService) handleVerifySubscriptionRequest(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Handling verify subscription request from sequencer",
		ion.String("sender", msg.Sender.String()),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.handleVerifySubscriptionRequest"))

	// Verify that we are indeed subscribed (sanity check)
	// We are processing this message, so we must be subscribed, but good to check state
	// In a real implementation, you might check internal state or config

	// Create a response message
	// The stage must be Type_VerifySubscription as expected by the Sequencer's verification handler
	ack := AVCStruct.NewACKBuilder().
		True_ACK_Message(s.pubSub.Host.ID(), config.Type_VerifySubscription)

	responseMsg := AVCStruct.NewMessageBuilder(nil).
		SetSender(s.pubSub.Host.ID()).
		SetMessage("Subscription verified").
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ack)

	// Publish the response back to the channel
	logger().NamedLogger.Info(logger_ctx, "Sending verification response",
		ion.String("sender", s.pubSub.Host.ID().String()),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.handleVerifySubscriptionRequest"))

	if err := Publisher.Publish(logger_ctx, s.pubSub, config.PubSub_ConsensusChannel, responseMsg, nil); err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to publish verification response", err,
			ion.String("function", "SubscriptionService.handleVerifySubscriptionRequest"))
		return err
	}

	return nil
}
