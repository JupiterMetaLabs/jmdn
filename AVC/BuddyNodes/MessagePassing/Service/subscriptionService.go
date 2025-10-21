package Service

import (
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"
	"gossipnode/config"
	"gossipnode/Pubsub"

	"go.uber.org/zap"
)

// SubscriptionService handles subscription-related operations
type SubscriptionService struct {
	pubSub *Struct.GossipPubSub
}

// NewSubscriptionService creates a new subscription service
func NewSubscriptionService(pubSub *Struct.GossipPubSub) *SubscriptionService {
	return &SubscriptionService{
		pubSub: pubSub,
	}
}

// HandleAskForSubscription handles subscription requests
func (s *SubscriptionService) HandleAskForSubscription(gossipMessage *Struct.GossipMessage) error {
	log.LogConsensusInfo("Handling ask for subscription message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleAskForSubscription"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available")
	}

	// Subscribe to the consensus channel

	err := Pubsub.Subscribe(s.pubSub, config.PubSub_ConsensusChannel, func(msg *Struct.GossipMessage) {
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
func (s *SubscriptionService) HandleEndPubSub(gossipMessage *Struct.GossipMessage) error {
	log.LogConsensusInfo("Handling end pubsub message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleEndPubSub"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available for unsubscription")
	}

	// Unsubscribe from the consensus channel
	if err := Pubsub.Unsubscribe(s.pubSub, config.PubSub_ConsensusChannel); err != nil {
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
func (s *SubscriptionService) handleReceivedMessage(msg *Struct.GossipMessage) error {
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
	case config.Type_Publish:
		log.LogConsensusInfo("Processing publish message from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		// The message will be handled by the publish service
		// This is just for logging and validation
		return nil

	case config.Type_AskForSubscription:
		log.LogConsensusInfo("Processing subscription request from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		// Handle subscription request
		return s.handleSubscriptionRequest(msg)

	default:
		log.LogConsensusInfo(fmt.Sprintf("Received message with unknown stage: %s", msg.Data.ACK.Stage),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return nil
	}
}

// handleSubscriptionRequest processes subscription requests from other nodes
func (s *SubscriptionService) handleSubscriptionRequest(msg *Struct.GossipMessage) error {
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
