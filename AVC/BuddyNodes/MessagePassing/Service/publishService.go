package Service

import (
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"go.uber.org/zap"
)

// PublishService handles publish operations
type PublishService struct {
	buddyNode *PubSubMessages.BuddyNode
}

// NewPublishService creates a new publish service
func NewPublishService(buddyNode *PubSubMessages.BuddyNode) *PublishService {
	return &PublishService{
		buddyNode: buddyNode,
	}
}

// HandlePublish handles incoming publish messages
func (s *PublishService) HandlePublish(gossipMessage *PubSubMessages.GossipMessage) error {
	log.LogConsensusInfo("Handling publish message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "PublishService.HandlePublish"))

	if s.buddyNode == nil {
		return fmt.Errorf("BuddyNode not available")
	}

	// Handle the incoming message and add it to the CRDT Engine
	if err := SubmitMessageToCRDT(gossipMessage.Data.Message, s.buddyNode); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "PublishService.HandlePublish"))
		return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
	}

	return nil
}

func SubmitMessageToCRDT(msg string, ListenerNode *PubSubMessages.BuddyNode) error {
	OP := &Types.OP{}
	Vote := &PubSubMessages.Vote{}
	if err := json.Unmarshal([]byte(msg), Vote); err != nil {
		return fmt.Errorf("failed to unmarshal message: %v", err)
	}
	OP.NodeID = ListenerNode.PeerID
	OP.OpType = Types.ADD
	OP.KeyValue = *Vote.ReturnOP(ListenerNode.PeerID)

	// Adding data to the CRDT First - Before PubSub
	if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
		return fmt.Errorf("failed to add vote to local CRDT Engine: %v", err)
	}
	return nil
}
