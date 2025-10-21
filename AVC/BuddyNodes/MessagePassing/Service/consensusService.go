package Service

import (
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"

	"go.uber.org/zap"
)

// ConsensusService handles consensus-related operations
type ConsensusService struct {
	buddyNode *Structs.BuddyNode
}

// NewConsensusService creates a new consensus service
func NewConsensusService(buddyNode *Structs.BuddyNode) *ConsensusService {
	return &ConsensusService{
		buddyNode: buddyNode,
	}
}

// HandleVerifySubscription handles subscription verification
func (s *ConsensusService) HandleVerifySubscription(gossipMessage *Struct.GossipMessage) error {
	log.LogConsensusInfo("Handling verify subscription message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "ConsensusService.HandleVerifySubscription"))

	if s.buddyNode == nil {
		log.LogConsensusInfo("Created ACK_FALSE - node not ready for subscription verification",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "ConsensusService.HandleVerifySubscription"))
		return nil
	}

	// Node is ready and subscribed, respond with ACK_TRUE + PeerID
	hostID := s.buddyNode.Host.ID().String()
	log.LogConsensusInfo(fmt.Sprintf("Created ACK_TRUE with PeerID %s for subscription verification", hostID),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "ConsensusService.HandleVerifySubscription"))

	return nil
}
