package MessagePassing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"log"
	"strings"
	"time"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// HandleBuddyNodesMessageStream handles incoming messages on the buddy nodes protocol
func (buddy *BuddyNode) HandleBuddyNodesMessageStream(s network.Stream) {
	defer s.Close()

	// Update metadata
	buddy.Mutex.Lock()
	buddy.MetaData.Received++
	buddy.MetaData.Total++
	buddy.MetaData.UpdatedAt = time.Now()
	buddy.Mutex.Unlock()

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		log.Printf("Error reading message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	// Remove the delimiter from the message
	msg = strings.TrimSuffix(msg, string(rune(config.Delimiter)))

	log.Printf("Received buddy message from %s: %s", s.Conn().RemotePeer(), msg)

	switch msg {
	case config.Type_StartPubSub:
		// If node is okay to listen for subscriptions, then return ACK True
		if buddy.Host != nil && buddy.Network != nil {
			// Node is ready to listen for subscriptions
			ackMessage := config.Type_ACK_True
			if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.Printf("Sent ACK_TRUE to %s for pubsub subscription", s.Conn().RemotePeer())
			}
		} else {
			// Node is not ready to listen for subscriptions
			ackMessage := config.Type_ACK_False
			if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.Printf("Sent ACK_FALSE to %s - node not ready for pubsub", s.Conn().RemotePeer())
			}
		}
	case config.Type_AskForSubscription:
		// Handle subscription request
		if buddy.Host != nil && buddy.Network != nil && buddy.PubSub != nil {
			// Try to subscribe to the consensus pubsub channel
			pubsub, ok := buddy.PubSub.(*Pubsub.GossipPubSub)
			if !ok {
				log.Printf("PubSub instance is not of correct type")
				ackMessage := config.Consensus_Response{
					Status: config.Type_ACK_False,
					PeerID: buddy.PeerID.String(),
					Stage:  config.Type_SubscriptionResponse,
				}
				ackMessageJSON, err := json.Marshal(ackMessage)
				if err != nil {
					log.Printf("Failed to marshal ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err)
					return
				}
				if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), string(ackMessageJSON)); err != nil {
					log.Printf("Failed to send ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err)
				} else {
					log.Printf("Sent ACK_FALSE to %s - invalid pubsub instance", s.Conn().RemotePeer())
				}
				return
			}

			// Subscribe to the consensus channel
			err := pubsub.Subscribe(config.PubSub_ConsensusChannel, func(msg *Pubsub.GossipMessage) {
				log.Printf("Received pubsub message on consensus channel: %s from %s", msg.ID, msg.Sender)
				// Handle the received message here if needed
			})

			if err != nil {
				log.Printf("Failed to subscribe to consensus channel: %v", err)
				ackMessage := config.Consensus_Response{
					Status: config.Type_ACK_False,
					PeerID: buddy.PeerID.String(),
					Stage:  config.Type_SubscriptionResponse,
				}
				ackMessageJSON, err := json.Marshal(ackMessage)
				if err != nil {
					log.Printf("Failed to marshal ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err)
					return
				}
				if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), string(ackMessageJSON)); err != nil {
					log.Printf("Failed to send ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err)
				} else {
					log.Printf("Sent ACK_FALSE to %s - subscription failed", s.Conn().RemotePeer())
				}
			} else {
				log.Printf("Successfully subscribed to consensus channel: %s", config.PubSub_ConsensusChannel)
				ackMessage := config.Consensus_Response{
					Status: config.Type_ACK_True,
					PeerID: buddy.PeerID.String(),
					Stage:  config.Type_SubscriptionResponse,
				}
				ackMessageJSON, err := json.Marshal(ackMessage)
				if err != nil {
					log.Printf("Failed to marshal ACK_TRUE to %s: %v", s.Conn().RemotePeer(), err)
					return
				}
				if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), string(ackMessageJSON)); err != nil {
					log.Printf("Failed to send ACK_TRUE to %s: %v", s.Conn().RemotePeer(), err)
				} else {
					log.Printf("Sent ACK_TRUE to %s for subscription request", s.Conn().RemotePeer())
				}
			}
		} else {
			// Node is not ready to accept subscription
			ackMessage := config.Consensus_Response{
				Status: config.Type_ACK_False,
				PeerID: buddy.PeerID.String(),
				Stage:  config.Type_SubscriptionResponse,
			}
			ackMessageJSON, err := json.Marshal(ackMessage)
			if err != nil {
				log.Printf("Failed to marshal ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err)
				return
			}
			if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), string(ackMessageJSON)); err != nil {
				log.Printf("Failed to send ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.Printf("Sent ACK_FALSE to %s - node not ready for subscription", s.Conn().RemotePeer())
			}
		}
	case config.Type_VerifySubscription:
		// Handle subscription verification request
		log.Printf("Received VERIFY_SUBSCRIPTION request from %s", s.Conn().RemotePeer())
		if buddy.Host != nil && buddy.Network != nil {
			// Node is ready and subscribed, respond with ACK_TRUE + PeerID
			ackMessage := config.Consensus_Response{
				Status: config.Type_ACK_True,
				PeerID: buddy.PeerID.String(),
				Stage:  config.Type_VerifySubscription,
			}

			ackMessageJSON, err := json.Marshal(ackMessage)
			if err != nil {
				log.Printf("Failed to marshal ACK_TRUE with PeerID to %s: %v", s.Conn().RemotePeer(), err)
				return
			}

			if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), string(ackMessageJSON)); err != nil {
				log.Printf("Failed to send ACK_TRUE with PeerID to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.Printf("Sent ACK_TRUE with PeerID %s to %s for subscription verification", buddy.Host.ID(), s.Conn().RemotePeer())
			}
		} else {
			// Node is not ready, respond with ACK_FALSE
			ackMessage := config.Consensus_Response{
				Status: config.Type_ACK_False,
				PeerID: buddy.PeerID.String(), // Return This Node's PeerID
				Stage:  config.Type_VerifySubscription,
			}

			ackMessageJSON, err := json.Marshal(ackMessage)
			if err != nil {
				log.Printf("Failed to marshal ACK_FALSE with PeerID to %s: %v", s.Conn().RemotePeer(), err)
				return
			}

			if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), string(ackMessageJSON)); err != nil {
				log.Printf("Failed to send ACK_FALSE to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.Printf("Sent ACK_FALSE to %s - node not ready for subscription verification", s.Conn().RemotePeer())
			}
		}
	case config.Type_SubscriptionResponse:
		// Try to parse as JSON Consensus_Response
		var consensusResponse config.Consensus_Response
		if err := json.Unmarshal([]byte(msg), &consensusResponse); err == nil {
			// Successfully parsed as JSON Consensus_Response
			log.Printf("Received JSON consensus response from %s: Status=%s, PeerID=%s, Stage=%s",
				s.Conn().RemotePeer(), consensusResponse.Status, consensusResponse.PeerID, consensusResponse.Stage)

			// Handle the response based on status
			switch consensusResponse.Status {
			case config.Type_ACK_True:
				// Notify the response handler with PeerID information
				if buddy.ResponseHandler != nil {
					// Check if the response handler supports PeerID handling
					if handlerWithPeerID, ok := buddy.ResponseHandler.(interface {
						HandleResponseWithPeerID(peer.ID, bool, string)
					}); ok {
						handlerWithPeerID.HandleResponseWithPeerID(s.Conn().RemotePeer(), true, consensusResponse.PeerID)
					} else {
						// Fallback to regular response handling
						buddy.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), true)
					}
				}
			case config.Type_ACK_False:
				// Handle ACK_FALSE response
				if buddy.ResponseHandler != nil {
					buddy.ResponseHandler.HandleResponse(s.Conn().RemotePeer(), false)
				}
			default:
				log.Printf("Unknown status in consensus response: %s", consensusResponse.Status)
			}
		} else {
			log.Printf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg)
			return
		}
	default:
		log.Printf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg)
		return
	}
}

func (Listener *BuddyNode) HandleSubmitMessageStream(s network.Stream) {
	defer s.Close()

	Controller := ServiceLayer.GetServiceController()
	if Controller == nil {
		log.Printf("Service controller is not initialized")
		return
	}
	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		log.Printf("Error reading message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}
	
	msg = strings.TrimSuffix(msg, string(rune(config.Delimiter)))

	log.Printf("Received submit message from %s: %s", s.Conn().RemotePeer(), msg)

	switch msg {
	case config.Type_SubmitVote:
		log.Printf("Received submit vote from %s: %s", s.Conn().RemotePeer(), msg)
		// First Add to local CRDT Engine
		
	}

}

// NewBuddyNode creates a new BuddyNode instance from an existing host
func NewBuddyNode(h host.Host, buddies *Buddies, responseHandler ResponseHandler, pubsub *Pubsub.GossipPubSub) *BuddyNode {
	buddy := &BuddyNode{
		Host:            h,
		Network:         h.Network(),
		PeerID:          h.ID(),
		BuddyNodes:      *buddies,
		ResponseHandler: responseHandler,
		PubSub:          pubsub,
		MetaData: MetaData{
			Received:  0,
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now(),
		},
	}

	// Set up the stream handler for the buddy nodes message protocol
	h.SetStreamHandler(config.BuddyNodesMessageProtocol, buddy.HandleBuddyNodesMessageStream)

	log.Printf("BuddyNode initialized with ID: %s", h.ID())
	log.Printf("Listening for buddy messages on protocol: %s", config.BuddyNodesMessageProtocol)

	return buddy
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
func (buddy *BuddyNode) SendMessageToPeer(peerID peer.ID, message string) error {
	// Create a stream to the peer
	stream, err := buddy.Host.NewStream(context.Background(), peerID, config.BuddyNodesMessageProtocol)
	if err != nil {
		return fmt.Errorf("failed to create stream to %s: %v", peerID, err)
	}
	defer stream.Close()

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	if message != config.Type_ACK_True && message != config.Type_ACK_False {
		// Update metadata
		buddy.Mutex.Lock()
		buddy.MetaData.Sent++
		buddy.MetaData.Total++
		buddy.MetaData.UpdatedAt = time.Now()
		buddy.Mutex.Unlock()

		log.Printf("Sent buddy message to %s: %s", peerID, message)
	}

	return nil
}
