package MessagePassing

import (
	"bufio"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/Pubsub"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/network"
	"go.uber.org/zap"
)



// HandleBuddyNodesMessageStream handles incoming messages on the buddy nodes protocol
func (buddy *BuddyNode) HandleBuddyNodesMessageStream(s network.Stream) {
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
	NewGlobalVariables().Set_PubSubNode(buddy)

	message := NewMessageProcessor().DeferenceMessage(msg)

	log.LogConsensusInfo(fmt.Sprintf("Received buddy message from %s: %s", s.Conn().RemotePeer(), msg),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("message", msg),
		zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))

	switch message.ACK.Stage {
	case config.Type_StartPubSub:
		// If node is okay to listen for subscriptions, then return ACK True
		if PubSub_BuddyNode != nil && PubSub_BuddyNode.Host != nil && PubSub_BuddyNode.Network != nil {
			// Node is ready to listen for subscriptions
			ackMessage := NewACKBuilder().True_ACK_Message(PubSub_BuddyNode.PeerID, config.Type_StartPubSub).ToString()
			if ackMessage == "" {
				log.LogConsensusError("Failed to create ACK_TRUE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to create ACK_TRUE message")
				return
			}

			if err := PubSub_BuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Sent ACK_TRUE to %s for pubsub subscription", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Sent ACK_TRUE to %s for pubsub subscription", s.Conn().RemotePeer())
			}
		} else {
			// Node is not ready to listen for subscriptions
			ackMessage := NewACKBuilder().False_ACK_Message(PubSub_BuddyNode.PeerID, config.Type_StartPubSub).ToString()
			if ackMessage == "" {
				log.LogConsensusError("Failed to create ACK_FALSE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to create ACK_FALSE message")
				return
			}
			if err := PubSub_BuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Sent ACK_FALSE to %s - node not ready for pubsub", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Sent ACK_FALSE to %s - node not ready for pubsub", s.Conn().RemotePeer())
			}
		}
	case config.Type_AskForSubscription:
		// Handle subscription request
		if PubSub_BuddyNode != nil && PubSub_BuddyNode.Host != nil && PubSub_BuddyNode.Network != nil && PubSub_BuddyNode.PubSub != nil {
			// Subscribe to the consensus channel
			err := PubSub_BuddyNode.PubSub.Subscribe(config.PubSub_ConsensusChannel, func(msg *Pubsub.GossipMessage) {
				log.LogConsensusInfo(fmt.Sprintf("Received pubsub message on consensus channel: %s from %s", msg.ID, msg.Sender), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Received pubsub message on consensus channel: %s from %s", msg.ID, msg.Sender)
				// Handle the received message here if needed - TODO
			})

			if err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to subscribe to consensus channel: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				ackMessage := NewACKBuilder().False_ACK_Message(PubSub_BuddyNode.PeerID, config.Type_SubscriptionResponse).ToString()
				if ackMessage == "" {
					log.LogConsensusError("Failed to create ACK_FALSE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
					fmt.Println("Failed to create ACK_FALSE message")
					return
				}
				if err := PubSub_BuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
					log.LogConsensusError(fmt.Sprintf("Failed to send ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				} else {
					log.LogConsensusInfo(fmt.Sprintf("Sent ACK_FALSE to %s - subscription failed", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
					fmt.Printf("Sent ACK_FALSE to %s - subscription failed", s.Conn().RemotePeer())
				}
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Successfully subscribed to consensus channel: %s", config.PubSub_ConsensusChannel), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Successfully subscribed to consensus channel: %s", config.PubSub_ConsensusChannel)
				ackMessage := NewACKBuilder().True_ACK_Message(PubSub_BuddyNode.PeerID, config.Type_SubscriptionResponse).ToString()
				if ackMessage == "" {
					log.LogConsensusError("Failed to create ACK_TRUE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
					fmt.Printf("Failed to create ACK_TRUE message")
					return
				}

				if err := PubSub_BuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
					log.LogConsensusError(fmt.Sprintf("Failed to send ACK_TRUE to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				} else {
					log.LogConsensusInfo(fmt.Sprintf("Sent ACK_TRUE to %s for subscription request", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
					fmt.Printf("Sent ACK_TRUE to %s for subscription request", s.Conn().RemotePeer())
				}
			}
		} else {
			// Node is not ready to accept subscription
			ackMessage := NewACKBuilder().False_ACK_Message(PubSub_BuddyNode.PeerID, config.Type_SubscriptionResponse).ToString()
			if ackMessage == "" {
				log.LogConsensusError("Failed to create ACK_FALSE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to create ACK_FALSE message")
				return
			}

			if err := PubSub_BuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to send ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Sent ACK_FALSE to %s - node not ready for subscription", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Sent ACK_FALSE to %s - node not ready for subscription", s.Conn().RemotePeer())
			}
		}
	case config.Type_VerifySubscription:
		// Handle subscription verification request
		log.LogConsensusInfo(fmt.Sprintf("Received VERIFY_SUBSCRIPTION request from %s", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		if PubSub_BuddyNode != nil && PubSub_BuddyNode.Host != nil && PubSub_BuddyNode.Network != nil && PubSub_BuddyNode.PubSub != nil {
			// Node is ready and subscribed, respond with ACK_TRUE + PeerID
			ackMessage := NewACKBuilder().True_ACK_Message(PubSub_BuddyNode.PeerID, config.Type_VerifySubscription).ToString()
			if ackMessage == "" {
				log.LogConsensusError("Failed to create ACK_TRUE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to create ACK_TRUE message")
				return
			}

			if err := PubSub_BuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to send ACK_TRUE with PeerID to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Sent ACK_TRUE with PeerID %s to %s for subscription verification", PubSub_BuddyNode.Host.ID(), s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Sent ACK_TRUE with PeerID %s to %s for subscription verification", PubSub_BuddyNode.Host.ID(), s.Conn().RemotePeer())
			}
		} else {
			// Node is not ready, respond with ACK_FALSE
			ackMessage := NewACKBuilder().False_ACK_Message(PubSub_BuddyNode.PeerID, config.Type_VerifySubscription).ToString()

			if ackMessage == "" {
				log.LogConsensusError("Failed to create ACK_FALSE message", err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Failed to create ACK_FALSE message")
				return
			}

			if err := PubSub_BuddyNode.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to send ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			} else {
				log.LogConsensusInfo(fmt.Sprintf("Sent ACK_FALSE to %s - node not ready for subscription verification", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Sent ACK_FALSE to %s - node not ready for subscription verification", s.Conn().RemotePeer())
			}
		}
	case config.Type_SubscriptionResponse:
		// Try to parse as JSON ACK_Message
		message := NewMessageProcessor().DeferenceMessage(msg)
		if message.ACK != nil {
			switch message.ACK.Status {
			case config.Type_ACK_True:
				if PubSub_BuddyNode.ResponseHandler != nil {
					PubSub_BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), true)
				} else {
					// Fallback to regular response handling
					PubSub_BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), true)
				}
			case config.Type_ACK_False:
				// Handle ACK_FALSE response
				if PubSub_BuddyNode.ResponseHandler != nil {
					PubSub_BuddyNode.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), false)
				}
			default:
				log.LogConsensusError(fmt.Sprintf("Unknown status in ACK_Message: %s", message.ACK.Status), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
				fmt.Printf("Unknown status in ACK_Message: %s", message.ACK.Status)
			}
		} else {
			log.LogConsensusError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			fmt.Printf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg)
			return
		}
	case config.Type_EndPubSub:
		// Handle end pubsub request
		log.LogConsensusInfo(fmt.Sprintf("Received END_PUBSUB request from %s", s.Conn().RemotePeer()), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		// Unsubscribe from the consensus channel
		if err := PubSub_BuddyNode.PubSub.Unsubscribe(config.PubSub_ConsensusChannel); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to unsubscribe from consensus channel: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			fmt.Printf("Failed to unsubscribe from consensus channel: %v", err)
		} else {
			log.LogConsensusInfo(fmt.Sprintf("Unsubscribed from consensus channel: %s", config.PubSub_ConsensusChannel), zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			fmt.Printf("Unsubscribed from consensus channel: %s", config.PubSub_ConsensusChannel)
		}
		return
	case config.Type_Publish:
		// handle the incoming message and add it to the CRDT Engine
		if err := SubmitMessageToCRDT(message.Message, PubSub_BuddyNode); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
			fmt.Printf("Failed to add vote to local CRDT Engine: %v", err)
		}
	default:
		log.LogConsensusError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg), err, zap.String("peer", s.Conn().RemotePeer().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", msg), zap.String("function", "ListenMessages.HandleBuddyNodesMessageStream"))
		fmt.Printf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg)
		return
	}
}
