package MessagePassing

import (
	"bufio"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	Router "gossipnode/AVC/BuddyNodes/MessagePassing/Router"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/network"
	"go.uber.org/zap"
)

// < -- CRDT Utils -- >
type StructBuddyNode struct{
	BuddyNode *Structs.BuddyNode
}

func NewStructBuddyNode(buddy *Structs.BuddyNode) *StructBuddyNode {
	if buddy == nil{
		return &StructBuddyNode{
			BuddyNode: &Structs.BuddyNode{},
		}
	}
	return &StructBuddyNode{
		BuddyNode: buddy,
	}
}

// HandleBuddyNodesMessageStream handles incoming messages on the buddy nodes protocol
func (StructBuddyNode *StructBuddyNode) HandleBuddyNodesMessageStream(s network.Stream) {
	buddy := StructBuddyNode.BuddyNode
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
	// Add the buddy Node to the Pubsub node for singleton instance
	Structs.NewGlobalVariables().Set_PubSubNode(buddy)

	message := Structs.NewMessageProcessor().DeferenceMessage(msg)

	log.LogConsensusInfo(fmt.Sprintf("Received buddy message from %s: %s", s.Conn().RemotePeer(), msg),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("message", msg),
		zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))

	switch message.Message.ACK.Stage {
	case config.Type_StartPubSub:
		// If node is okay to listen for subscriptions, then return ACK True
		if Structs.PubSub_BuddyNode != nil && Structs.PubSub_BuddyNode.Host != nil && Structs.PubSub_BuddyNode.Network != nil {
			// Node is ready to listen for subscriptions
			ackMessage := Structs.NewACKBuilder().True_ACK_Message(Structs.PubSub_BuddyNode.PeerID, config.Type_StartPubSub).ToString()
			if ackMessage == "" {
				log.LogConsensusError("Failed to create ACK_TRUE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to create ACK_TRUE message")
				return
			}

			if err := StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Sent ACK_TRUE to %s for pubsub subscription", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Sent ACK_TRUE to %s for pubsub subscription", s.Conn().RemotePeer())
			}
		} else {
			// Node is not ready to listen for subscriptions
			ackMessage := Structs.NewACKBuilder().False_ACK_Message(StructBuddyNode.BuddyNode.PeerID, config.Type_StartPubSub).ToString()
			if ackMessage == "" {
				log.LogConsensusError("Failed to create ACK_FALSE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to create ACK_FALSE message")
				return
			}
			if err := StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Sent ACK_FALSE to %s - node not ready for pubsub", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Sent ACK_FALSE to %s - node not ready for pubsub", s.Conn().RemotePeer())
			}
		}
	case config.Type_AskForSubscription:
		// Route to MessageParsing for PubSub subscription handling
		if err := Router.Router(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle subscription request: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			// Send ACK_FALSE response
			ackMessage := Structs.NewACKBuilder().False_ACK_Message(StructBuddyNode.BuddyNode.PeerID, config.Type_SubscriptionResponse).ToString()
			if ackMessage != "" {
				StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage)
			}
		} else {
			// Send ACK_TRUE response
			ackMessage := Structs.NewACKBuilder().True_ACK_Message(StructBuddyNode.BuddyNode.PeerID, config.Type_SubscriptionResponse).ToString()
			if ackMessage != "" {
				StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage)
			}
		}
	case config.Type_VerifySubscription:
		// Route to MessageParsing for PubSub verification handling
		log.LogConsensusInfo(fmt.Sprintf("Received VERIFY_SUBSCRIPTION request from %s", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		if err := Router.Router(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle verification request: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			// Send ACK_FALSE response
			ackMessage := Structs.NewACKBuilder().False_ACK_Message(StructBuddyNode.BuddyNode.PeerID, config.Type_VerifySubscription).ToString()
			if ackMessage != "" {
				StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage)
			}
		} else {
			// Send ACK_TRUE response
			ackMessage := Structs.NewACKBuilder().True_ACK_Message(StructBuddyNode.BuddyNode.PeerID, config.Type_VerifySubscription).ToString()
			if ackMessage != "" {
				StructBuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage)
			}
		}
	case config.Type_SubscriptionResponse:
		// Try to parse as JSON ACK_Message
		message := Structs.NewMessageProcessor().DeferenceMessage(msg)
		if message.Message.ACK != nil {
			switch message.Message.ACK.Status {
			case config.Type_ACK_True:
				if StructBuddyNode.BuddyNode.ResponseHandler != nil {
					StructBuddyNode.BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), true)
				} else {
					// Fallback to regular response handling
					StructBuddyNode.BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), true)
				}
			case config.Type_ACK_False:
				// Handle ACK_FALSE response
				if StructBuddyNode.BuddyNode.ResponseHandler != nil {
					StructBuddyNode.BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), false)
				}
			default:
				log.LogConsensusError(fmt.Sprintf("Unknown status in ACK_Message: %s", message.Message.ACK.Status), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Unknown status in ACK_Message: %s", message.Message.ACK.Status)
			}
		} else {
			log.LogConsensusError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			fmt.Printf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg)
			return
		}
	case config.Type_EndPubSub:
		// Route to MessageParsing for PubSub unsubscription handling
		log.LogConsensusInfo(fmt.Sprintf("Received END_PUBSUB request from %s", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		if err := Router.Router(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle end pubsub request: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		}
		return
	case config.Type_Publish:
		// Route to MessageParsing for PubSub message publishing handling
		if err := Router.Router(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle publish message: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		}
	default:
		log.LogConsensusError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		fmt.Printf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg)
		return
	}
}
