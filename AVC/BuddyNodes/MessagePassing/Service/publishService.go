package Service

import (
	"context"
	"encoding/json"
	"errors"

	"jmdn/AVC/BuddyNodes/ServiceLayer"
	"jmdn/AVC/BuddyNodes/Types"
	PubSubMessages "jmdn/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
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
func (s *PublishService) HandlePublish(logger_ctx context.Context, gossipMessage *PubSubMessages.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Handling publish message",
		ion.String("topic", "PublishService"),
		ion.String("function", "PublishService.HandlePublish"))

	if s.buddyNode == nil {
		return errors.New("BuddyNode not available")
	}

	// Handle the incoming message and add it to the CRDT Engine
	if err := SubmitMessageToCRDT(gossipMessage.Data.Message, s.buddyNode); err != nil {
		err := errors.New("failed to add vote to local CRDT Engine: %v")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("topic", "PublishService"),
			ion.String("function", "PublishService.HandlePublish"))
		return err
	}

	return nil
}

func SubmitMessageToCRDT(msg string, ListenerNode *PubSubMessages.BuddyNode) error {
	OP := &Types.OP{}
	Vote := &PubSubMessages.Vote{}
	if err := json.Unmarshal([]byte(msg), Vote); err != nil {
		return errors.New("failed to unmarshal message: %v")
	}
	OP.NodeID = ListenerNode.PeerID
	OP.OpType = Types.ADD
	OP.KeyValue = *Vote.ReturnOP(ListenerNode.PeerID)

	// Adding data to the CRDT First - Before PubSub
	if err := ServiceLayer.Controller(ListenerNode.CRDTLayer, OP); err != nil {
		return errors.New("failed to add vote to local CRDT Engine: %v")
	}
	return nil
}
