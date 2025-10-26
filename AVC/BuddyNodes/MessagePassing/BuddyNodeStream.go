package MessagePassing

import (
	"bufio"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	Router "gossipnode/Pubsub/Router"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"go.uber.org/zap"
)

// < -- CRDT Utils -- >
type StructBuddyNode struct {
	BuddyNode *AVCStruct.BuddyNode
}

// Abstraction function to start the handleBuddynode message stream
func HandleBuddyNodeStream(s network.Stream) {
	NewStructBuddyNode(AVCStruct.PubSub_BuddyNode).HandleBuddyNodesMessageStream(s)
}

func NewStructBuddyNode(buddy *AVCStruct.BuddyNode) *StructBuddyNode {
	if buddy == nil {
		return &StructBuddyNode{
			BuddyNode: &AVCStruct.BuddyNode{},
		}
	}
	return &StructBuddyNode{
		BuddyNode: buddy,
	}
}

// HandleBuddyNodesMessageStream handles incoming messages on the buddy nodes protocol
func (StructBuddyNode *StructBuddyNode) HandleBuddyNodesMessageStream(s network.Stream) {
	defer s.Close()

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Error reading message from %s: %v", s.Conn().RemotePeer(), err),
			err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("message", msg),
			zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		return
	}

	message := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(msg)

	log.LogConsensusInfo(fmt.Sprintf("Received buddy message from %s: %s", s.Conn().RemotePeer(), msg),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("message", msg),
		zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))

	// Check if message was successfully parsed
	if message == nil {
		log.LogConsensusError("Failed to parse message - malformed JSON or invalid structure", nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("raw_message", msg),
			zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		return
	}

	// Handle special cases that need direct response
	// Check if ACK is not nil before accessing it
	if message.GetACK() == nil {
		log.LogConsensusError("Received message with nil ACK", nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("raw_message", msg),
			zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		return
	}

	switch message.GetACK().GetStage() {
	case config.Type_StartPubSub:
		StructBuddyNode.handleStartPubSub(s)
		return
	case config.Type_SubscriptionResponse:
		StructBuddyNode.handleSubscriptionResponse(s, message)
		return
	}

	// For all other message types, delegate to the service layer via Router
	// Convert Message to GossipMessage for Router
	gossipMessage := AVCStruct.NewGossipMessageBuilder(nil).SetMesssage(message)
	if err := Router.Router(gossipMessage); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to handle message via service layer: %v", err), err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("message", msg),
			zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))

		// Send ACK_FALSE response for service layer failures
		StructBuddyNode.sendACKResponse(s, false, message.GetACK().GetStage())
	} else {
		// Send ACK_TRUE response for successful service layer processing
		StructBuddyNode.sendACKResponse(s, true, message.GetACK().GetStage())
	}
}

// handleStartPubSub handles the StartPubSub message type with direct logic
func (StructBuddyNode *StructBuddyNode) handleStartPubSub(s network.Stream) {
	// If node is okay to listen for subscriptions, then return ACK True
	if AVCStruct.PubSub_BuddyNode != nil && AVCStruct.PubSub_BuddyNode.Host != nil && AVCStruct.PubSub_BuddyNode.Network != nil {
		// Node is ready to listen for subscriptions
		ackBuilder := AVCStruct.NewACKBuilder().True_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, config.Type_StartPubSub)
		message := AVCStruct.NewMessageBuilder(nil).
			SetSender(AVCStruct.PubSub_BuddyNode.PeerID).
			SetMessage("ACK_TRUE for StartPubSub").
			SetTimestamp(time.Now().Unix()).
			SetACK(ackBuilder)

		// Marshal the message to JSON
		messageBytes, err := json.Marshal(message)
		if err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to marshal ACK message: %v", err), err,
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "ListenMessages.handleStartPubSub"))
			return
		}

		if err := StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), string(messageBytes)); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err), err,
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "ListenMessages.handleStartPubSub"))
		} else {
			log.LogConsensusInfo(fmt.Sprintf("Sent ACK_TRUE to %s for pubsub subscription", s.Conn().RemotePeer()),
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "ListenMessages.handleStartPubSub"))
		}
	} else {
		// Node is not ready to listen for subscriptions
		ackBuilder := AVCStruct.NewACKBuilder().False_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, config.Type_StartPubSub)
		message := AVCStruct.NewMessageBuilder(nil).
			SetSender(AVCStruct.PubSub_BuddyNode.PeerID).
			SetMessage("ACK_FALSE for StartPubSub").
			SetTimestamp(time.Now().Unix()).
			SetACK(ackBuilder)

		// Marshal the message to JSON
		messageBytes, err := json.Marshal(message)
		if err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to marshal ACK message: %v", err), err,
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "ListenMessages.handleStartPubSub"))
			return
		}

		if err := StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), string(messageBytes)); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err), err,
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "ListenMessages.handleStartPubSub"))
		} else {
			log.LogConsensusInfo(fmt.Sprintf("Sent ACK_FALSE to %s - node not ready for pubsub", s.Conn().RemotePeer()),
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "ListenMessages.handleStartPubSub"))
		}
	}
}

// handleSubscriptionResponse handles subscription response messages
func (StructBuddyNode *StructBuddyNode) handleSubscriptionResponse(s network.Stream, message *AVCStruct.Message) {
	if message.ACK != nil {
		switch message.ACK.Status {
		case config.Type_ACK_True:
			if StructBuddyNode.BuddyNode.ResponseHandler != nil {
				StructBuddyNode.BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), true)
			}
		case config.Type_ACK_False:
			if StructBuddyNode.BuddyNode.ResponseHandler != nil {
				StructBuddyNode.BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), false)
			}
		default:
			log.LogConsensusError(fmt.Sprintf("Unknown status in ACK_Message: %s", message.ACK.Status), nil,
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "ListenMessages.handleSubscriptionResponse"))
		}
	} else {
		log.LogConsensusError(fmt.Sprintf("Unknown message type received from %s", s.Conn().RemotePeer()), nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "ListenMessages.handleSubscriptionResponse"))
	}
}

// sendACKResponse sends ACK response based on success/failure
func (StructBuddyNode *StructBuddyNode) sendACKResponse(s network.Stream, success bool, stage string) {
	var ackBuilder *AVCStruct.ACK
	if success {
		ackBuilder = AVCStruct.NewACKBuilder().True_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, stage)
	} else {
		ackBuilder = AVCStruct.NewACKBuilder().False_ACK_Message(AVCStruct.PubSub_BuddyNode.PeerID, stage)
	}

	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(AVCStruct.PubSub_BuddyNode.PeerID).
		SetMessage(fmt.Sprintf("ACK response for %s", stage)).
		SetTimestamp(time.Now().Unix()).
		SetACK(ackBuilder)

	// Marshal the message to JSON
	messageBytes, err := json.Marshal(message)
	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to marshal ACK response message: %v", err), err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "ListenMessages.sendACKResponse"))
		return
	}

	if err := StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), string(messageBytes)); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to send ACK response to %s: %v", s.Conn().RemotePeer(), err), err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "ListenMessages.sendACKResponse"))
	}
}
