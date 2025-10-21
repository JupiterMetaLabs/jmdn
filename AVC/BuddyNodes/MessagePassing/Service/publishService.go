package Service

import (
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"

	"go.uber.org/zap"
)

// PublishService handles publish operations
type PublishService struct {
	buddyNode *Structs.BuddyNode
}

// NewPublishService creates a new publish service
func NewPublishService(buddyNode *Structs.BuddyNode) *PublishService {
	return &PublishService{
		buddyNode: buddyNode,
	}
}

// HandlePublish handles incoming publish messages
func (s *PublishService) HandlePublish(gossipMessage *Struct.GossipMessage) error {
	log.LogConsensusInfo("Handling publish message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "PublishService.HandlePublish"))

	if s.buddyNode == nil {
		return fmt.Errorf("BuddyNode not available")
	}

	// Handle the incoming message and add it to the CRDT Engine
	if err := s.buddyNode.SubmitToCRDT(gossipMessage.Data.Message); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "PublishService.HandlePublish"))
		return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
	}

	return nil
}
