package Service

import (
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/Pubsub"
	"gossipnode/config"

	"go.uber.org/zap"
)

// SubscriptionService handles subscription-related operations
type SubscriptionService struct {
	pubSub *Pubsub.GossipPubSub
}

// NewSubscriptionService creates a new subscription service
func NewSubscriptionService(pubSub *Pubsub.GossipPubSub) *SubscriptionService {
	return &SubscriptionService{
		pubSub: pubSub,
	}
}

// HandleAskForSubscription handles subscription requests
func (s *SubscriptionService) HandleAskForSubscription(gossipMessage *Pubsub.GossipMessage) error {
	log.LogConsensusInfo("Handling ask for subscription message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleAskForSubscription"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available")
	}

	// Subscribe to the consensus channel
	err := s.pubSub.Subscribe(config.PubSub_ConsensusChannel, func(msg *Pubsub.GossipMessage) {
		log.LogConsensusInfo(fmt.Sprintf("Received pubsub message on consensus channel: %s from %s", msg.ID, msg.Sender),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.HandleAskForSubscription"))
		// Handle the received message here if needed - TODO
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
func (s *SubscriptionService) HandleEndPubSub(gossipMessage *Pubsub.GossipMessage) error {
	log.LogConsensusInfo("Handling end pubsub message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleEndPubSub"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available for unsubscription")
	}

	// Unsubscribe from the consensus channel
	if err := s.pubSub.Unsubscribe(config.PubSub_ConsensusChannel); err != nil {
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
