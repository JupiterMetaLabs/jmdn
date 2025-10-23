package PubSubConnector

import (
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// SubscriptionService handles subscription-related operations
type SubscriptionService struct {
	pubSub *AVCStruct.GossipPubSub
}

// NewSubscriptionService creates a new subscription service
func NewSubscriptionService(pubSub *AVCStruct.GossipPubSub) *SubscriptionService {
	return &SubscriptionService{
		pubSub: pubSub,
	}
}

// HandleAskForSubscription handles subscription requests
func (s *SubscriptionService) HandleAskForSubscription(gossipMessage *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling ask for subscription message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleAskForSubscription"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available")
	}

	// Subscribe to the consensus channel
	err := Connector.Subscribe(s.pubSub, config.PubSub_ConsensusChannel, func(msg *AVCStruct.GossipMessage) {
		log.LogConsensusInfo(fmt.Sprintf("Received pubsub message on consensus channel: %s from %s", msg.ID, msg.Sender),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.HandleAskForSubscription"))

		// Handle the received message by processing it through the message router
		if err := s.handleReceivedMessage(msg); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to handle received message: %v", err), err,
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("function", "SubscriptionService.handleReceivedMessage"))
		}
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
func (s *SubscriptionService) HandleEndPubSub(gossipMessage *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling end pubsub message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("function", "SubscriptionService.HandleEndPubSub"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available for unsubscription")
	}

	// Unsubscribe from the consensus channel
	if err := Connector.Unsubscribe(s.pubSub, config.PubSub_ConsensusChannel); err != nil {
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

// handleReceivedMessage processes received pubsub messages
func (s *SubscriptionService) handleReceivedMessage(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Processing received pubsub message",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("message_id", msg.ID),
		zap.String("sender", string(msg.Sender)),
		zap.String("function", "SubscriptionService.handleReceivedMessage"))

	// Check if the message has valid data
	if msg.Data == nil {
		return fmt.Errorf("received message has no data")
	}

	// Process the message based on its type
	switch msg.Data.ACK.Stage {
	case config.Type_Publish:
		log.LogConsensusInfo("Processing publish message from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		// The message will be handled by the publish service
		// This is just for logging and validation
		return nil

	case config.Type_AskForSubscription:
		log.LogConsensusInfo("Processing subscription request from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))

		// Handle subscription request
		return s.handleSubscriptionRequest(msg)

	default:
		log.LogConsensusInfo(fmt.Sprintf("Received message with unknown stage: %s", msg.Data.ACK.Stage),
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return nil
	}
}

// handleSubscriptionRequest processes subscription requests from other nodes
func (s *SubscriptionService) handleSubscriptionRequest(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling subscription request from pubsub",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("sender", string(msg.Sender)),
		zap.String("function", "SubscriptionService.handleSubscriptionRequest"))

	// 1. Validate the requesting node
	if err := s.validateRequestingNode(msg); err != nil {
		log.LogConsensusError(fmt.Sprintf("Node validation failed for %s: %v", msg.Sender, err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("sender", string(msg.Sender)),
			zap.String("function", "SubscriptionService.handleSubscriptionRequest"))
		return fmt.Errorf("node validation failed: %v", err)
	}

	// 2. Check if the node is authorized to subscribe
	if err := s.checkNodeAuthorization(msg.Sender); err != nil {
		log.LogConsensusError(fmt.Sprintf("Node authorization failed for %s: %v", msg.Sender, err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("sender", string(msg.Sender)),
			zap.String("function", "SubscriptionService.handleSubscriptionRequest"))
		return fmt.Errorf("node authorization failed: %v", err)
	}

	// 3. Add the node to your buddy list
	if err := s.addNodeToBuddyList(msg.Sender); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to add node to buddy list: %s: %v", msg.Sender, err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("sender", string(msg.Sender)),
			zap.String("function", "SubscriptionService.handleSubscriptionRequest"))
		return fmt.Errorf("failed to add node to buddy list: %v", err)
	}

	// 4. Send a response back to the requesting node
	if err := s.sendSubscriptionResponse(msg, true); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to send subscription response to %s: %v", msg.Sender, err), err,
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("sender", string(msg.Sender)),
			zap.String("function", "SubscriptionService.handleSubscriptionRequest"))
		return fmt.Errorf("failed to send subscription response: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Successfully processed subscription request from %s", msg.Sender),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("sender", string(msg.Sender)),
		zap.String("function", "SubscriptionService.handleSubscriptionRequest"))

	return nil
}

// validateRequestingNode validates the requesting node
func (s *SubscriptionService) validateRequestingNode(msg *AVCStruct.GossipMessage) error {
	// Check if message has valid data
	if msg.Data == nil {
		return fmt.Errorf("message data is nil")
	}

	// Check if sender is valid
	if msg.Sender == "" {
		return fmt.Errorf("sender is empty")
	}

	// Validate message timestamp (not too old or in future)
	now := time.Now()
	messageTime := time.Unix(msg.Timestamp, 0)

	if now.Sub(messageTime) > time.Hour {
		return fmt.Errorf("message timestamp is too old")
	}

	if messageTime.Sub(now) > 5*time.Minute {
		return fmt.Errorf("message timestamp is in the future")
	}

	// Validate ACK stage
	if msg.Data.ACK == nil || msg.Data.ACK.Stage != config.Type_AskForSubscription {
		return fmt.Errorf("invalid ACK stage for subscription request")
	}

	return nil
}

// checkNodeAuthorization checks if the node is authorized to subscribe
func (s *SubscriptionService) checkNodeAuthorization(sender peer.ID) error {
	// Get the current buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil {
		return fmt.Errorf("buddy node not available for authorization check")
	}

	// Check if we already have this peer in our buddy list
	buddyNode.Mutex.RLock()
	defer buddyNode.Mutex.RUnlock()

	for _, existingPeer := range buddyNode.BuddyNodes.Buddies_Nodes {
		if existingPeer == sender {
			log.LogConsensusInfo(fmt.Sprintf("Peer %s is already in buddy list", sender),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("sender", string(sender)),
				zap.String("function", "SubscriptionService.checkNodeAuthorization"))
			return nil // Already authorized
		}
	}

	// Check if the peer have the access in the channel access list
	if !s.hasChannelAccess(sender) {
		return fmt.Errorf("peer %s does not have access to the channel", sender)
	}

	// Check if the peer is connected to our network
	if !s.isPeerConnected(sender) {
		return fmt.Errorf("peer %s is not connected to our network", sender)
	}

	return nil
}

// isPeerConnected checks if a peer is connected to our network
func (s *SubscriptionService) isPeerConnected(peerID peer.ID) bool {
	// Get the current buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil || buddyNode.Host == nil {
		return false
	}

	// Check if peer is in our network's peer list
	connectedPeers := buddyNode.Host.Network().Peers()
	for _, connectedPeer := range connectedPeers {
		if connectedPeer == peerID {
			return true
		}
	}

	return false
}

// addNodeToBuddyList adds a node to the buddy list
func (s *SubscriptionService) addNodeToBuddyList(peerID peer.ID) error {
	// Get the current buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil {
		return fmt.Errorf("buddy node not available")
	}

	// Add peer to buddy list
	buddyNode.Mutex.Lock()
	defer buddyNode.Mutex.Unlock()

	// Check again if peer is already in the list (double-check for race conditions)
	for _, existingPeer := range buddyNode.BuddyNodes.Buddies_Nodes {
		if existingPeer == peerID {
			log.LogConsensusInfo(fmt.Sprintf("Peer %s already in buddy list", peerID),
				zap.String("topic", log.Consensus_TOPIC),
				zap.String("peer", string(peerID)),
				zap.String("function", "SubscriptionService.addNodeToBuddyList"))
			return nil
		}
	}

	// Add the new peer to the buddy list
	buddyNode.AddBuddies([]peer.ID{peerID})

	// Update metadata
	buddyNode.MetaData.UpdatedAt = time.Now()

	log.LogConsensusInfo(fmt.Sprintf("Added peer %s to buddy list. Total buddies: %d",
		peerID, len(buddyNode.BuddyNodes.Buddies_Nodes)),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("peer", string(peerID)),
		zap.String("function", "SubscriptionService.addNodeToBuddyList"))

	return nil
}

// sendSubscriptionResponse sends a response back to the requesting node
func (s *SubscriptionService) sendSubscriptionResponse(msg *AVCStruct.GossipMessage, accepted bool) error {
	// Get the current buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil {
		return fmt.Errorf("buddy node not available for sending response")
	}

	// Create ACK message
	var ackMessage string
	if accepted {
		ackMessage = AVCStruct.NewACKBuilder().True_ACK_Message(buddyNode.PeerID, config.Type_SubscriptionResponse).ToString()
	} else {
		ackMessage = AVCStruct.NewACKBuilder().False_ACK_Message(buddyNode.PeerID, config.Type_SubscriptionResponse).ToString()
	}

	if ackMessage == "" {
		return fmt.Errorf("failed to create ACK message")
	}

	// Create a new message with the ACK response
	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(buddyNode.PeerID).
		SetMessage(ackMessage).
		SetTimestamp(time.Now().Unix()).
		SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(buddyNode.PeerID, config.Type_SubscriptionResponse))

	// Convert to GossipMessage for publishing
	gossipMessage := AVCStruct.NewGossipMessageBuilder(nil).
		SetID(fmt.Sprintf("sub_response_%d", time.Now().UnixNano())).
		SetTopic(config.PubSub_ConsensusChannel).
		SetMesssage(message).
		SetSender(buddyNode.PeerID).
		SetTimestamp(time.Now().Unix())

	// Publish the response message
	if err := s.publishResponse(gossipMessage); err != nil {
		return fmt.Errorf("failed to publish response: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Sent subscription response to %s: %t", msg.Sender, accepted),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("peer", string(msg.Sender)),
		zap.String("accepted", fmt.Sprintf("%t", accepted)),
		zap.String("function", "SubscriptionService.sendSubscriptionResponse"))

	return nil
}

// publishResponse publishes a response message using the existing Publish service
func (s *SubscriptionService) publishResponse(gossipMessage *AVCStruct.GossipMessage) error {
	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available for publishing response")
	}
	// Use the existing Publish service to send the message
	// This will handle all the networking, serialization, and distribution
	err := Publisher.Publish(s.pubSub, config.PubSub_ConsensusChannel, gossipMessage.Data, map[string]string{})
	if err != nil {
		return fmt.Errorf("failed to publish response using Publisher service: %v", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("Published subscription response: %s", gossipMessage.ID),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("message_id", gossipMessage.ID),
		zap.String("function", "SubscriptionService.publishResponse"))

	return nil
}

// hasChannelAccess checks if a peer has access to the channel
func (s *SubscriptionService) hasChannelAccess(peerID peer.ID) bool {
	// Get the current buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil || buddyNode.PubSub == nil {
		return false
	}
	// Check if channel is public or peer is in allowed list
	return buddyNode.IsPublicChannel(peerID) || buddyNode.IsAllowed(peerID)
}
