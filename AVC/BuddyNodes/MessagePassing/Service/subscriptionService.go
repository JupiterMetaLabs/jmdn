package Service

import (
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// BFTMessageHandler defines the interface for BFT message handling
// This avoids importing the bft package directly
type BFTMessageHandler interface {
	HandleStartPubSub(msg *AVCStruct.GossipMessage) error
	HandleEndPubSub(msg *AVCStruct.GossipMessage) error
	HandlePrepareVote(msg *AVCStruct.GossipMessage) error
	HandleCommitVote(msg *AVCStruct.GossipMessage) error
}

// SubscriptionService handles subscription-related operations
type SubscriptionService struct {
	pubSub     *AVCStruct.GossipPubSub
	bftAdapter BFTMessageHandler // ✅ Use interface instead of concrete type
}

// NewSubscriptionService creates a new subscription service
func NewSubscriptionService(pubSub *AVCStruct.GossipPubSub) *SubscriptionService {
	return &SubscriptionService{
		pubSub: pubSub,
	}
}

// SetBFTAdapter sets the BFT adapter for handling consensus messages
func (s *SubscriptionService) SetBFTAdapter(adapter BFTMessageHandler) {
	s.bftAdapter = adapter
}

// HandleAskForSubscription handles subscription requests
func (s *SubscriptionService) HandleAskForSubscription(gossipMessage *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling ask for subscription message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleAskForSubscription"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available")
	}

	// Subscribe to the consensus channel
	err := s.subscribeToTopic(config.PubSub_ConsensusChannel, func(msg *AVCStruct.GossipMessage) {
		log.LogConsensusInfo(fmt.Sprintf("Received pubsub message on consensus channel: %s from %s", msg.ID, msg.Sender),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.HandleAskForSubscription"))

		// Handle the received message by processing it through the message router
		if err := s.handleReceivedMessage(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle received message: %v", err), err,
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "SubscriptionService.handleReceivedMessage"))
		}
	})

	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to subscribe to consensus channel: %v", err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.HandleAskForSubscription"))
		return fmt.Errorf("failed to subscribe to consensus channel: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Successfully subscribed to consensus channel: %s", config.PubSub_ConsensusChannel),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleAskForSubscription"))

	return nil
}

// HandleEndPubSub handles unsubscription requests
func (s *SubscriptionService) HandleEndPubSub(gossipMessage *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling end pubsub message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleEndPubSub"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available for unsubscription")
	}

	// Unsubscribe from the consensus channel
	if err := s.unsubscribeFromTopic(config.PubSub_ConsensusChannel); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to unsubscribe from consensus channel: %v", err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.HandleEndPubSub"))
		return fmt.Errorf("failed to unsubscribe from consensus channel: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Unsubscribed from consensus channel: %s", config.PubSub_ConsensusChannel),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleEndPubSub"))

	return nil
}

// handleReceivedMessage processes received pubsub messages
func (s *SubscriptionService) handleReceivedMessage(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Processing received pubsub message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("message_id", msg.ID),
		zap.String("sender", string(msg.Sender)),
		zap.String("function", "SubscriptionService.handleReceivedMessage"))

	// Check if the message has valid data
	if msg.Data == nil {
		return fmt.Errorf("received message has no data")
	}

	// Process the message based on its type
	switch msg.Data.ACK.Stage {

	// ========== BFT CONSENSUS MESSAGES ==========
	case config.Type_StartPubSub:
		log.LogConsensusInfo("Processing START_PUBSUB from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("round_id", msg.Data.RoundID),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		if s.bftAdapter != nil {
			return s.bftAdapter.HandleStartPubSub(msg)
		}
		return nil

	case config.Type_EndPubSub:
		log.LogConsensusInfo("Processing END_PUBSUB from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("round_id", msg.Data.RoundID),
			zap.Bool("success", msg.Data.ConsensusSuccess),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		if s.bftAdapter != nil {
			return s.bftAdapter.HandleEndPubSub(msg)
		}
		return nil

	case config.Type_SubmitVote:
		log.LogConsensusInfo("Processing SUBMIT_VOTE from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("round_id", msg.Data.RoundID),
			zap.String("phase", msg.Data.Phase),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		return s.handleVoteSubmission(msg)

	case config.Type_Publish:
		// Check if it's a BFT vote (has Phase and RoundID)
		if msg.Data.Phase != "" && msg.Data.RoundID != "" {
			log.LogConsensusInfo("Processing BFT vote via PUBLISH",
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("round_id", msg.Data.RoundID),
				zap.String("phase", msg.Data.Phase),
				zap.String("function", "SubscriptionService.handleReceivedMessage"))

			return s.handleVoteSubmission(msg)
		}

		// Regular publish message
		log.LogConsensusInfo("Processing publish message from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		return nil

	case config.Type_AskForSubscription:
		log.LogConsensusInfo("Processing subscription request from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		return s.handleSubscriptionRequest(msg)

	case config.Type_ToBeProcessed:
		log.LogConsensusInfo("Processing TO_BE_PROCESSED message",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("message_id", msg.ID),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		return nil

	default:
		log.LogConsensusInfo(fmt.Sprintf("Received message with unknown stage: %s", msg.Data.ACK.Stage),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return nil
	}
}

// ========== BFT HANDLER METHODS ==========

func (s *SubscriptionService) handleVoteSubmission(msg *AVCStruct.GossipMessage) error {
	if s.bftAdapter == nil {
		log.LogConsensusInfo("BFT adapter not set, ignoring vote",
			zap.String("topic", log.Consensus_TOPIC),
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
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("round_id", msg.Data.RoundID),
		zap.String("function", "SubscriptionService.handleVoteSubmission"))
	return nil
}

func (s *SubscriptionService) handlePrepareVote(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Processing PREPARE vote",
		zap.String("topic", log.Consensus_TOPIC),
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
		zap.String("topic", log.Consensus_TOPIC),
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
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("sender", string(msg.Sender)),
		zap.String("function", "SubscriptionService.handleSubscriptionRequest"))

	// In a real implementation, you would:
	// 1. Validate the requesting node
	// 2. Check if the node is authorized to subscribe
	// 3. Add the node to your buddy list
	// 4. Send a response back to the requesting node

	// For now, we'll just log the request
	log.LogConsensusInfo(fmt.Sprintf("Subscription request received from %s", msg.Sender),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.handleSubscriptionRequest"))

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
