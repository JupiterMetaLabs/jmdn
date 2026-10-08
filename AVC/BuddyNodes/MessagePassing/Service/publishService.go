package Service

import (
	"context"
	"encoding/json"
	"errors"

	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	PubSubMessages "gossipnode/config/PubSubMessages"

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
	logger().Info(logger_ctx, "Handling publish message",
		ion.String("topic", "PublishService"),
		ion.String("function", "PublishService.HandlePublish"))

	if s.buddyNode == nil {
		return errors.New("BuddyNode not available")
	}

	// W3 (legacy-CRDT migration, Phase 3 — stop legacy writes): this used to
	// call SubmitMessageToCRDT, which unmarshals gossipMessage.Data.Message
	// as a PubSubMessages.Vote and writes it into the legacy CRDT keyed on
	// THIS NODE'S OWN peer ID (ListenerNode.PeerID, not the sender) with
	// value strconv.Itoa(int(vote.Vote)). Votes never reach this function —
	// Router.go's Type_Publish case is commented out, and Vote/Trigger.go
	// sends votes as Type_SubmitVote, not Type_Publish — so whatever does
	// arrive here (confirmed: only the BFT adapter, bft_pubsub_adapter.go)
	// unmarshals into a zero-valued Vote, writing a fake "0" vote under this
	// node's own key into the very store this migration is retiring. Not
	// deleted outright — see W1's comment in Vote/Trigger.go for the general
	// pattern — but there's no vote data here to preserve a fallback for;
	// SubmitMessageToCRDT itself is left defined, just unreferenced.

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
