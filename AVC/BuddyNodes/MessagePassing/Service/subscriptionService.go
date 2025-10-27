package Service

import (
	"context"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Decision represents a BFT vote decision (avoid importing bft package)
type Decision string

const (
	Accept Decision = "ACCEPT"
	Reject Decision = "REJECT"
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

// HandleAskForSubscription handles subscription requests
func (s *SubscriptionService) HandleAskForSubscription(gossipMessage *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling ask for subscription message",
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("function", "SubscriptionService.HandleAskForSubscription"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available")
	}

	// Subscribe to the consensus channel
	err := s.subscribeToTopic(config.PubSub_ConsensusChannel, func(msg *AVCStruct.GossipMessage) {
		log.LogConsensusInfo(fmt.Sprintf("Received pubsub message on consensus channel: %s from %s", msg.ID, msg.Sender),
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.HandleAskForSubscription"))

		// Handle the received message by processing it through the message router
		if err := s.handleReceivedMessage(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle received message: %v", err), err,
				zap.String("topic", config.PubSub_ConsensusChannel),
				zap.String("function", "SubscriptionService.handleReceivedMessage"))
		}
	})

	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to subscribe to consensus channel: %v", err), err,
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.HandleAskForSubscription"))
		return fmt.Errorf("failed to subscribe to consensus channel: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Successfully subscribed to consensus channel: %s", config.PubSub_ConsensusChannel),
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("function", "SubscriptionService.HandleAskForSubscription"))

	return nil
}

// HandleEndPubSub handles unsubscription requests
func (s *SubscriptionService) HandleEndPubSub(gossipMessage *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling end pubsub message",
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("function", "SubscriptionService.HandleEndPubSub"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available for unsubscription")
	}

	// Unsubscribe from the consensus channel
	if err := s.unsubscribeFromTopic(config.PubSub_ConsensusChannel); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to unsubscribe from consensus channel: %v", err), err,
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.HandleEndPubSub"))
		return fmt.Errorf("failed to unsubscribe from consensus channel: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Unsubscribed from consensus channel: %s", config.PubSub_ConsensusChannel),
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("function", "SubscriptionService.HandleEndPubSub"))

	return nil
}

// handleReceivedMessage processes received pubsub messages
func (s *SubscriptionService) handleReceivedMessage(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Processing received pubsub message",
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("message_id", msg.ID),
		zap.String("sender", string(msg.Sender)),
		zap.String("function", "SubscriptionService.handleReceivedMessage"))

	// Check if the message has valid data
	if msg.Data == nil {
		return fmt.Errorf("received message has no data")
	}
	fmt.Printf("==============================================\n")
	fmt.Printf("Message: %+v\n", msg)

	// Attach ACK if missing
	if msg.Data.ACK == nil {
		fmt.Printf("Received message with nil ACK - attaching default ACK\n")
		log.LogConsensusError("Received message with nil ACK - attaching default ACK", nil, zap.String("function", "SubscriptionService.handleReceivedMessage"))

		// Create a default ACK with Type_Publish stage
		ack := AVCStruct.NewACKBuilder().
			True_ACK_Message(msg.Sender, config.Type_Publish)

		msg.Data.SetACK(ack)
	}

	// Process the message based on its type
	switch msg.Data.ACK.Stage {

	// ========== BFT CONSENSUS MESSAGES ==========
	case config.Type_BFTRequest:
		log.LogConsensusInfo("Processing BFT_REQUEST from pubsub",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTRequest(msg)

	case config.Type_StartPubSub:
		log.LogConsensusInfo("Processing START_PUBSUB from pubsub",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("round_id", msg.Data.RoundID),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		if s.bftAdapter != nil {
			return s.bftAdapter.HandleStartPubSub(msg)
		}
		return nil

	case config.Type_EndPubSub:
		log.LogConsensusInfo("Processing END_PUBSUB from pubsub",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("round_id", msg.Data.RoundID),
			zap.Bool("success", msg.Data.ConsensusSuccess),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		if s.bftAdapter != nil {
			return s.bftAdapter.HandleEndPubSub(msg)
		}
		return nil

	case config.Type_SubmitVote:
		log.LogConsensusInfo("Processing SUBMIT_VOTE from pubsub",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("round_id", msg.Data.RoundID),
			zap.String("phase", msg.Data.Phase),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		// Add vote to local CRDT for this buddy node
		fmt.Printf("\n[BUDDY] Processing vote submission via pubsub\n")
		fmt.Printf("Message: %s\n", msg.Data.Message)
		fmt.Printf("From: %s\n", msg.Sender)

		// Get the global ForListner
		globalVars := AVCStruct.NewGlobalVariables()
		listenerNode := globalVars.Get_ForListner()

		if listenerNode == nil || listenerNode.CRDTLayer == nil {
			fmt.Printf("[BUDDY] ✗ Listener node or CRDT layer not initialized\n")
			log.LogConsensusError("Listener node or CRDT layer not initialized", nil,
				zap.String("function", "SubscriptionService.handleReceivedMessage"))
			return fmt.Errorf("listener node or CRDT layer not initialized")
		}

		// Add vote to CRDT directly
		voteData := make(map[string]interface{})
		if err := json.Unmarshal([]byte(msg.Data.Message), &voteData); err != nil {
			fmt.Printf("[BUDDY] ✗ Failed to unmarshal vote message: %v\n", err)
			return fmt.Errorf("failed to unmarshal vote message: %v", err)
		}

		if vote, exists := voteData["vote"]; exists {
			voteValue, ok := vote.(float64)
			if !ok {
				return fmt.Errorf("invalid vote value type")
			}

			OP := &Types.OP{
				NodeID: msg.Data.Sender,
				OpType: int8(voteValue),
				KeyValue: Types.KeyValue{
					Key:   "vote",
					Value: msg.Data.Message,
				},
			}

			if err := ServiceLayer.Controller(listenerNode.CRDTLayer, OP); err != nil {
				fmt.Printf("[BUDDY] ✗ Failed to add vote to CRDT: %v\n", err)
				return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
			}

			fmt.Printf("[BUDDY] ✓ Successfully added vote to CRDT\n")
		}

		return nil

	case config.Type_Publish:
		// Check if it's a BFT vote (has Phase and RoundID)
		if msg.Data.Phase != "" && msg.Data.RoundID != "" {
			log.LogConsensusInfo("Processing BFT vote via PUBLISH",
				zap.String("topic", config.PubSub_ConsensusChannel),
				zap.String("round_id", msg.Data.RoundID),
				zap.String("phase", msg.Data.Phase),
				zap.String("function", "SubscriptionService.handleReceivedMessage"))

			return s.handleVoteSubmission(msg)
		}

		// Regular publish message
		log.LogConsensusInfo("Processing publish message from pubsub",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		return nil

	case config.Type_AskForSubscription:
		log.LogConsensusInfo("Processing subscription request from pubsub",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		return s.handleSubscriptionRequest(msg)

	case config.Type_ToBeProcessed:
		log.LogConsensusInfo("Processing TO_BE_PROCESSED message",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("message_id", msg.ID),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		return nil

	default:
		// Debugging in the default case
		fmt.Printf("==============================================\n")
		fmt.Printf("[BUDDY] Received message with unknown stage: %s\n", msg.Data.ACK.Stage)
		fmt.Printf("Message: %s\n", msg.Data.Message)
		fmt.Printf("==============================================\n")
		log.LogConsensusInfo(fmt.Sprintf("Received message with unknown stage: %s", msg.Data.ACK.Stage),
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return nil
	}
}

// ========== BFT HANDLER METHODS ==========

func (s *SubscriptionService) handleVoteSubmission(msg *AVCStruct.GossipMessage) error {
	if s.bftAdapter == nil {
		log.LogConsensusInfo("BFT adapter not set, ignoring vote",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("round_id", msg.Data.RoundID),
			zap.String("function", "SubscriptionService.handleVoteSubmission"))
		return nil
	}

	// Route to appropriate handler based on phase
	switch msg.Data.Phase {
	case "PREPARE":
		return s.handlePrepareVote(msg)
	case "COMMIT":
		return s.handleCommitVote(msg)
	}

	log.LogConsensusInfo(fmt.Sprintf("Unknown vote phase: %s", msg.Data.Phase),
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("round_id", msg.Data.RoundID),
		zap.String("function", "SubscriptionService.handleVoteSubmission"))
	return nil
}

func (s *SubscriptionService) handlePrepareVote(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Processing PREPARE vote",
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("round_id", msg.Data.RoundID),
		zap.String("sender", msg.Sender.String()),
		zap.String("function", "SubscriptionService.handlePrepareVote"))

	if s.bftAdapter != nil {
		return s.bftAdapter.HandlePrepareVote(msg)
	}

	return nil
}

func (s *SubscriptionService) handleCommitVote(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Processing COMMIT vote",
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("round_id", msg.Data.RoundID),
		zap.String("sender", msg.Sender.String()),
		zap.String("function", "SubscriptionService.handleCommitVote"))

	if s.bftAdapter != nil {
		return s.bftAdapter.HandleCommitVote(msg)
	}

	return nil
}

// ========== EXISTING METHODS ==========
// handleSubscriptionRequest processes subscription requests from other nodes
func (s *SubscriptionService) handleSubscriptionRequest(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling subscription request from pubsub",
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("sender", string(msg.Sender)),
		zap.String("function", "SubscriptionService.handleSubscriptionRequest"))

	// In a real implementation, you would:
	// 1. Validate the requesting node
	// 2. Check if the node is authorized to subscribe
	// 3. Add the node to your buddy list
	// 4. Send a response back to the requesting node

	// For now, we'll just log the request
	log.LogConsensusInfo(fmt.Sprintf("Subscription request received from %s", msg.Sender),
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("function", "SubscriptionService.handleSubscriptionRequest"))

	return nil
}

// HandleStreamSubscriptionRequest handles subscription requests received via stream
// This method can be called from ListenerHandler to delegate subscription processing
func (s *SubscriptionService) HandleStreamSubscriptionRequest(channelName string) error {
	if s.pubSub == nil {
		return fmt.Errorf("pubsub not available")
	}

	log.LogConsensusInfo("Handling stream subscription request",
		zap.String("channel", channelName),
		zap.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))

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
		fmt.Printf("Created %s channel locally for peer %s\n", channelName, s.pubSub.Host.ID())
	}
	s.pubSub.Mutex.Unlock()

	// Use the Connector.Subscribe to handle the subscription properly with GossipSub
	// This ensures messages are received via GossipSub
	fmt.Printf("About to call Connector.Subscribe for %s\n", channelName)
	err := Connector.Subscribe(s.pubSub, channelName, func(msg *AVCStruct.GossipMessage) {
		fmt.Printf("\n[BUDDY NODE PUBSUB HANDLER] Received message on %s\n", channelName)
		fmt.Printf("Message ID: %s\n", msg.ID)
		fmt.Printf("From: %s\n", msg.Sender)
		fmt.Printf("Topic: %s\n", msg.Topic)

		log.LogConsensusInfo(fmt.Sprintf("Received message on %s: %s", channelName, msg.ID),
			zap.String("channel", channelName),
			zap.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))

		// Handle the received message by processing it through the message router
		if err := s.handleReceivedMessage(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle received message: %v", err), err,
				zap.String("channel", channelName),
				zap.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))
		}
	})
	fmt.Printf("Connector.Subscribe returned with err: %v\n", err)

	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to subscribe to %s: %v", channelName, err), err,
			zap.String("channel", channelName),
			zap.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))
		return fmt.Errorf("failed to subscribe to %s: %v", channelName, err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Successfully subscribed to %s", channelName),
		zap.String("channel", channelName),
		zap.String("function", "SubscriptionService.HandleStreamSubscriptionRequest"))

	return nil
}

func (s *SubscriptionService) subscribeToTopic(topic string, handler func(*AVCStruct.GossipMessage)) error {
	if s.pubSub == nil {
		return fmt.Errorf("pubsub not available")
	}

	hostID := s.pubSub.Host.ID()
	if !s.canSubscribe(topic, hostID) {
		return fmt.Errorf("access denied: not authorized to subscribe to channel %s", topic)
	}

	s.pubSub.Mutex.Lock()
	defer s.pubSub.Mutex.Unlock()

	s.pubSub.Topics[topic] = true
	s.pubSub.Handlers[topic] = handler
	return nil
}

func (s *SubscriptionService) unsubscribeFromTopic(topic string) error {
	if s.pubSub == nil {
		return fmt.Errorf("pubsub not available")
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

func (s *SubscriptionService) GetMyBuddyID() string {
	if s.myBuddyID != "" {
		return s.myBuddyID
	}
	return s.pubSub.Host.ID().String()
}

func (s *SubscriptionService) handleBFTRequest(msg *AVCStruct.GossipMessage) error {
	// If no factory is set, just log and return
	if s.adapterFactory == nil {
		log.LogConsensusInfo("BFT adapter factory not configured, ignoring BFT request",
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "SubscriptionService.handleBFTRequest"))
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
		return fmt.Errorf("failed to parse BFT request: %w", err)
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
		log.LogConsensusInfo("Not in buddy list, skipping consensus",
			zap.String("round", fmt.Sprintf("%d", reqData.Round)),
			zap.String("function", "SubscriptionService.handleBFTRequest"))
		return nil
	}

	log.LogConsensusInfo("I'm a buddy! Starting consensus",
		zap.String("round", fmt.Sprintf("%d", reqData.Round)),
		zap.String("function", "SubscriptionService.handleBFTRequest"))

	go func() {
		if s.bftAdapter == nil {
			adapter, err := s.adapterFactory(
				context.Background(),
				s.pubSub,
				reqData.GossipsubTopic,
			)
			if err != nil {
				log.LogConsensusError("Failed to create BFT adapter", err,
					zap.String("function", "SubscriptionService.handleBFTRequest"))
				return
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
			log.LogConsensusError("Consensus failed", err,
				zap.String("round", fmt.Sprintf("%d", reqData.Round)),
				zap.String("function", "SubscriptionService.handleBFTRequest"))
		} else {
			log.LogConsensusInfo("Consensus completed successfully",
				zap.String("round", fmt.Sprintf("%d", reqData.Round)),
				zap.String("decision", string(result.Decision)),
				zap.Bool("accepted", result.BlockAccepted),
				zap.String("function", "SubscriptionService.handleBFTRequest"))
		}
	}()

	return nil
}
