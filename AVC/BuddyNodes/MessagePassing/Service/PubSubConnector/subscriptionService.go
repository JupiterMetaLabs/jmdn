package PubSubConnector

import (
	"context"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// BFTMessageHandler defines the interface for BFT message handling
type BFTMessageHandler interface {
	HandleStartPubSub(msg *AVCStruct.GossipMessage) error
	HandleEndPubSub(msg *AVCStruct.GossipMessage) error
	HandlePrepareVote(msg *AVCStruct.GossipMessage) error
	HandleCommitVote(msg *AVCStruct.GossipMessage) error
	ProposeConsensus(
		ctx context.Context,
		round uint64,
		blockHash string,
		myBuddyID string,
		allBuddies []BuddyInput,
	) (*Result, error)
}

// BFTFactory creates BFT handlers for channels
type BFTFactory func(ctx context.Context, pubSub *AVCStruct.GossipPubSub, channelName string) (BFTMessageHandler, error)

// Decision represents consensus decision
type Decision string

const (
	Accept Decision = "ACCEPT"
	Reject Decision = "REJECT"
)

// BuddyInput represents buddy node information
type BuddyInput struct {
	ID        string
	Decision  Decision
	PublicKey []byte
}

// Result represents consensus result
type Result struct {
	Success       bool
	BlockAccepted bool
	Decision      Decision
}

// SubscriptionService handles subscription-related operations
type SubscriptionService struct {
	pubSub      *AVCStruct.GossipPubSub
	myBuddyID   string
	bftFactory  BFTFactory
	bftHandlers map[string]BFTMessageHandler // channelName -> handler
	handlersMux sync.RWMutex

	// Callback to store consensus results (for testing)
	OnConsensusComplete func(result *Result)
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
		return nil

	case config.Type_AskForSubscription:
		log.LogConsensusInfo("Processing subscription request from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleSubscriptionRequest(msg)

	case config.Type_BFTRequest:
		log.LogConsensusInfo("Processing BFT request from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTRequest(msg)

	case config.Type_BFTPrepareVote:
		log.LogConsensusInfo("Processing BFT prepare vote from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTPrepareVote(msg)

	case config.Type_BFTCommitVote:
		log.LogConsensusInfo("Processing BFT commit vote from pubsub",
			zap.String("topic", log.Consensus_TOPIC),
			zap.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTCommitVote(msg)

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

// ============================================================================
// BFT MESSAGE HANDLING
// ============================================================================

// getBFTHandler gets or creates a BFT handler for a channel
func (s *SubscriptionService) getBFTHandler(channelName string) (BFTMessageHandler, error) {
	s.handlersMux.RLock()
	handler, exists := s.bftHandlers[channelName]
	s.handlersMux.RUnlock()

	if exists {
		return handler, nil
	}

	// Create new handler
	s.handlersMux.Lock()
	defer s.handlersMux.Unlock()

	// Double-check after acquiring write lock
	if handler, exists := s.bftHandlers[channelName]; exists {
		return handler, nil
	}

	// Use factory to create handler
	if s.bftFactory == nil {
		return nil, fmt.Errorf("BFT factory not configured")
	}

	handler, err := s.bftFactory(context.Background(), s.pubSub, channelName)
	if err != nil {
		return nil, fmt.Errorf("failed to create BFT handler: %w", err)
	}

	s.bftHandlers[channelName] = handler
	log.LogConsensusInfo(fmt.Sprintf("Created BFT handler for channel: %s", channelName),
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("channel", channelName))

	return handler, nil
}

// handleBFTRequest handles incoming BFT consensus requests
func (s *SubscriptionService) handleBFTRequest(msg *AVCStruct.GossipMessage) error {
	log.LogConsensusInfo("Handling BFT request",
		zap.String("topic", log.Consensus_TOPIC),
		zap.String("message_id", msg.ID))

	// Parse the request data
	var reqData struct {
		Round          uint64                   `json:"Round"`
		BlockHash      string                   `json:"BlockHash"`
		GossipsubTopic string                   `json:"GossipsubTopic"`
		AllBuddies     []map[string]interface{} `json:"AllBuddies"`
	}

	if err := json.Unmarshal([]byte(msg.Data.Message), &reqData); err != nil {
		log.LogConsensusError("Failed to parse BFT request", err,
			zap.String("topic", log.Consensus_TOPIC))
		return fmt.Errorf("failed to parse BFT request: %w", err)
	}

	log.LogConsensusInfo(fmt.Sprintf("BFT Request - Round: %d, Block: %s, Buddies: %d",
		reqData.Round, reqData.BlockHash, len(reqData.AllBuddies)),
		zap.String("topic", log.Consensus_TOPIC))

	// Convert buddies to BuddyInput format
	allBuddies := make([]BuddyInput, len(reqData.AllBuddies))
	for i, buddyMap := range reqData.AllBuddies {
		id, _ := buddyMap["ID"].(string)
		decisionStr, _ := buddyMap["Decision"].(string)

		// Handle public key - it might be a slice of float64 from JSON unmarshaling
		var pubKeyBytes []byte
		if pubKeyInterface, ok := buddyMap["PublicKey"]; ok {
			switch v := pubKeyInterface.(type) {
			case []byte:
				pubKeyBytes = v
			case []interface{}:
				pubKeyBytes = make([]byte, len(v))
				for j, b := range v {
					if floatVal, ok := b.(float64); ok {
						pubKeyBytes[j] = byte(floatVal)
					}
				}
			}
		}

		decision := Accept
		if decisionStr == "REJECT" {
			decision = Reject
		}

		allBuddies[i] = BuddyInput{
			ID:        id,
			Decision:  decision,
			PublicKey: pubKeyBytes,
		}
	}

	// Get BFT handler
	handler, err := s.getBFTHandler(msg.Topic)
	if err != nil {
		log.LogConsensusError("Failed to get BFT handler", err,
			zap.String("topic", log.Consensus_TOPIC))
		return err
	}

	// Start consensus process in a goroutine
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		log.LogConsensusInfo(fmt.Sprintf("Starting consensus for round %d, block %s", reqData.Round, reqData.BlockHash),
			zap.String("topic", log.Consensus_TOPIC))

		result, err := handler.ProposeConsensus(ctx, reqData.Round, reqData.BlockHash, s.myBuddyID, allBuddies)
		if err != nil {
			log.LogConsensusError(fmt.Sprintf("Consensus failed: %v", err), err,
				zap.String("topic", log.Consensus_TOPIC))
			return
		}

		log.LogConsensusInfo(fmt.Sprintf("Consensus completed - Success: %v, Decision: %s, BlockAccepted: %v",
			result.Success, result.Decision, result.BlockAccepted),
			zap.String("topic", log.Consensus_TOPIC))

		// Call callback if set (for testing)
		if s.OnConsensusComplete != nil {
			s.OnConsensusComplete(result)
		}
	}()

	return nil
}

// handleBFTPrepareVote handles prepare vote messages
func (s *SubscriptionService) handleBFTPrepareVote(msg *AVCStruct.GossipMessage) error {
	handler, err := s.getBFTHandler(msg.Topic)
	if err != nil {
		return err
	}
	return handler.HandlePrepareVote(msg)
}

// handleBFTCommitVote handles commit vote messages
func (s *SubscriptionService) handleBFTCommitVote(msg *AVCStruct.GossipMessage) error {
	handler, err := s.getBFTHandler(msg.Topic)
	if err != nil {
		return err
	}
	return handler.HandleCommitVote(msg)
}

// SetMyBuddyID sets the buddy ID
func (s *SubscriptionService) SetMyBuddyID(id string) {
	s.myBuddyID = id
}

// SetBFTFactory sets the BFT factory
func (s *SubscriptionService) SetBFTFactory(factory BFTFactory) {
	s.bftFactory = factory
}

// InitBFTHandlers initializes the BFT handlers map if not already done
func (s *SubscriptionService) InitBFTHandlers() {
	if s.bftHandlers == nil {
		s.bftHandlers = make(map[string]BFTMessageHandler)
	}
}
