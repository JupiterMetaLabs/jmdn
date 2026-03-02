package PubSubConnector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	Publisher "jmdn/Pubsub/Publish"
	Connector "jmdn/Pubsub/Subscription"
	"jmdn/config"
	"jmdn/config/GRO"
	AVCStruct "jmdn/config/PubSubMessages"
	log "jmdn/logging"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
)

var LocalGRO interfaces.LocalGoroutineManagerInterface

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
	logger_ctx := context.WithValue(context.Background(), "logger", logger)
	defer logger_ctx.Done()
	// start trace
	tracer := logger().NamedLogger.Tracer("SubscriptionService")
	trace_ctx, span := tracer.Start(logger_ctx, "SubscriptionService.HandleAskForSubscription")
	defer span.End()

	logger().NamedLogger.Info(trace_ctx, "Handling ask for subscription message", ion.String("topic", log.SubscriptionService),
		ion.String("function", "SubscriptionService.HandleAskForSubscription"))

	if s.pubSub == nil {
		return errors.New("PubSub not available")
	}

	// Subscribe to the consensus channel
	err := Connector.Subscribe(trace_ctx, s.pubSub, config.PubSub_ConsensusChannel, func(msg *AVCStruct.GossipMessage) {
		logger().NamedLogger.Info(trace_ctx, "Received pubsub message on consensus channel:",
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.HandleAskForSubscription"))

		// Handle the received message by processing it through the message router
		if err := s.handleReceivedMessage(logger_ctx, msg); err != nil {
			logger().NamedLogger.Error(trace_ctx, "Failed to handle received message:", err,
				ion.String("topic", log.SubscriptionService),
				ion.String("function", "SubscriptionService.HandleAskForSubscription"))
		}
	})

	if err != nil {
		logger().NamedLogger.Error(trace_ctx, "Failed to subscribe to consensus channel:", err,
			ion.String("topic", log.SubscriptionService),
			ion.String("function", "SubscriptionService.HandleAskForSubscription"))
		return fmt.Errorf("failed to subscribe to consensus channel: %v", err)
	}

	logger().NamedLogger.Info(trace_ctx, "Successfully subscribed to consensus channel:",
		ion.String("channel", config.PubSub_ConsensusChannel),
		ion.String("topic", log.SubscriptionService),
		ion.String("function", "SubscriptionService.HandleAskForSubscription"))

	return nil
}

// HandleEndPubSub handles unsubscription requests
func (s *SubscriptionService) HandleEndPubSub(gossipMessage *AVCStruct.GossipMessage) error {
	logger_ctx := context.WithValue(context.Background(), "logger", logger)
	defer logger_ctx.Done()
	logger().NamedLogger.Info(logger_ctx, "Handling end pubsub message",
		ion.String("channel", config.PubSub_ConsensusChannel),
		ion.String("topic", log.SubscriptionService),
		ion.String("function", "SubscriptionService.HandleEndPubSub"))

	if s.pubSub == nil {
		return fmt.Errorf("PubSub not available for unsubscription")
	}

	// Unsubscribe from the consensus channel
	if err := Connector.Unsubscribe(s.pubSub, config.PubSub_ConsensusChannel); err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to unsubscribe from consensus channel:", err,
			ion.String("channel", config.PubSub_ConsensusChannel),
			ion.String("topic", log.SubscriptionService),
			ion.String("function", "SubscriptionService.HandleEndPubSub"))
		return fmt.Errorf("failed to unsubscribe from consensus channel: %v", err)
	}

	logger().NamedLogger.Info(logger_ctx, "Unsubscribed from consensus channel:",
		ion.String("channel", config.PubSub_ConsensusChannel),
		ion.String("topic", log.SubscriptionService),
		ion.String("function", "SubscriptionService.HandleEndPubSub"))

	return nil
}

// handleReceivedMessage processes received pubsub messages
func (s *SubscriptionService) handleReceivedMessage(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Processing received pubsub message",
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("message_id", msg.ID),
		ion.String("sender", string(msg.Sender)),
		ion.String("function", "SubscriptionService.handleReceivedMessage"))

	// Check if the message has valid data
	if msg.Data == nil {
		return fmt.Errorf("received message has no data")
	}

	// Process the message based on its type
	switch msg.Data.ACK.Stage {
	case config.Type_Publish:
		logger().NamedLogger.Info(logger_ctx, "Processing publish message from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return nil

	case config.Type_AskForSubscription:
		logger().NamedLogger.Info(logger_ctx, "Processing subscription request from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleSubscriptionRequest(logger_ctx, msg)

	case config.Type_BFTRequest:
		logger().NamedLogger.Info(logger_ctx, "Processing BFT request from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTRequest(logger_ctx, msg)

	case config.Type_BFTPrepareVote:
		logger().NamedLogger.Info(logger_ctx, "Processing BFT prepare vote from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTPrepareVote(logger_ctx, msg)

	case config.Type_BFTCommitVote:
		logger().NamedLogger.Info(logger_ctx, "Processing BFT commit vote from pubsub",
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return s.handleBFTCommitVote(logger_ctx, msg)

	default:
		logger().NamedLogger.Info(logger_ctx, "Received message with unknown stage:",
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleReceivedMessage"))
		return nil
	}
}

// handleSubscriptionRequest processes subscription requests from other nodes
func (s *SubscriptionService) handleSubscriptionRequest(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Handling subscription request from pubsub",
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("sender", string(msg.Sender)),
		ion.String("function", "SubscriptionService.handleSubscriptionRequest"))

	// 1. Validate the requesting node
	if err := s.validateRequestingNode(logger_ctx, msg); err != nil {
		logger().NamedLogger.Error(logger_ctx, "Node validation failed:", err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleSubscriptionRequest"))

		return fmt.Errorf("node validation failed: %v", err)
	}

	// 2. Check if the node is authorized to subscribe
	if err := s.checkNodeAuthorization(logger_ctx, msg.Sender); err != nil {
		logger().NamedLogger.Error(logger_ctx, "Node authorization failed:", err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleSubscriptionRequest"))
		return fmt.Errorf("node authorization failed: %v", err)
	}

	// 3. Add the node to your buddy list
	if err := s.addNodeToBuddyList(logger_ctx, msg.Sender); err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to add node to buddy list:", err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleSubscriptionRequest"))
		return fmt.Errorf("failed to add node to buddy list: %v", err)
	}

	// 4. Send a response back to the requesting node
	if err := s.sendSubscriptionResponse(logger_ctx, msg, true); err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to send subscription response:", err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.handleSubscriptionRequest"))
		return fmt.Errorf("failed to send subscription response: %v", err)
	}

	logger().NamedLogger.Info(logger_ctx, "Successfully processed subscription request from:",
		ion.String("message_id", msg.ID),
		ion.String("sender", string(msg.Sender)),
		ion.String("stage", string(msg.Data.ACK.Stage)),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.handleSubscriptionRequest"))

	return nil
}

// validateRequestingNode validates the requesting node
func (s *SubscriptionService) validateRequestingNode(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	// Check if message has valid data
	if msg.Data == nil {

		err := errors.New("subscriptionService.validateRequestingNode - message data is nil")

		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.validateRequestingNode"))
		return err
	}

	// Check if sender is valid
	if msg.Sender == "" {
		err := errors.New("subscriptionService.validateRequestingNode - sender is empty")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.validateRequestingNode"))
		return fmt.Errorf("sender is empty")
	}

	// Validate message timestamp (not too old or in future)
	now := time.Now().UTC()
	messageTime := time.Unix(msg.Timestamp, 0)

	if now.Sub(messageTime) > time.Hour {
		err := errors.New("subscriptionService.validateRequestingNode - message timestamp is too old")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.validateRequestingNode"))
		return err
	}

	if messageTime.Sub(now) > 5*time.Minute {
		err := errors.New("subscriptionService.validateRequestingNode - message timestamp is in the future")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.validateRequestingNode"))
		return err
	}

	// Validate ACK stage
	if msg.Data.ACK == nil || msg.Data.ACK.Stage != config.Type_AskForSubscription {
		err := errors.New("subscriptionService.validateRequestingNode - invalid ACK stage for subscription request")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", msg.ID),
			ion.String("sender", string(msg.Sender)),
			ion.String("stage", string(msg.Data.ACK.Stage)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.validateRequestingNode"))
		return err
	}

	return nil
}

// checkNodeAuthorization checks if the node is authorized to subscribe
func (s *SubscriptionService) checkNodeAuthorization(logger_ctx context.Context, sender peer.ID) error {
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
			logger().NamedLogger.Info(logger_ctx, "Peer is already in buddy list",
				ion.String("sender", string(sender)),
				ion.String("topic", config.PubSub_ConsensusChannel),
				ion.String("function", "SubscriptionService.checkNodeAuthorization"))
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
func (s *SubscriptionService) addNodeToBuddyList(logger_ctx context.Context, peerID peer.ID) error {
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
			logger().NamedLogger.Info(logger_ctx, "Peer is already in buddy list",
				ion.String("sender", string(peerID)),
				ion.String("topic", log.SubscriptionService),
				ion.String("channel", config.PubSub_ConsensusChannel),
				ion.String("total_buddies", fmt.Sprintf("%d", len(buddyNode.BuddyNodes.Buddies_Nodes))),
				ion.String("function", "SubscriptionService.addNodeToBuddyList"))
			return nil
		}
	}

	// Add the new peer to the buddy list
	buddyNode.AddBuddies([]peer.ID{peerID})

	// Update metadata
	buddyNode.MetaData.UpdatedAt = time.Now().UTC()

	logger().NamedLogger.Info(logger_ctx, "Added peer to buddy list",
		ion.String("sender", string(peerID)),
		ion.String("topic", log.SubscriptionService),
		ion.String("channel", config.PubSub_ConsensusChannel),
		ion.String("total_buddies", fmt.Sprintf("%d", len(buddyNode.BuddyNodes.Buddies_Nodes))),
		ion.String("function", "SubscriptionService.addNodeToBuddyList"))

	return nil
}

// sendSubscriptionResponse sends a response back to the requesting node
func (s *SubscriptionService) sendSubscriptionResponse(logger_ctx context.Context, msg *AVCStruct.GossipMessage, accepted bool) error {
	logger().NamedLogger.Info(logger_ctx, "Sending subscription response",
		ion.String("sender", string(msg.Sender)),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.sendSubscriptionResponse"))
	// Get the current buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil {
		err := errors.New("subscriptionService.sendSubscriptionResponse - buddy node not available for sending response")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("sender", string(msg.Sender)),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.sendSubscriptionResponse"))
		return err
	}

	// Create ACK message
	var ackBuilder *AVCStruct.ACK
	if accepted {
		ackBuilder = AVCStruct.NewACKBuilder().True_ACK_Message(buddyNode.PeerID, config.Type_SubscriptionResponse)
	} else {
		ackBuilder = AVCStruct.NewACKBuilder().False_ACK_Message(buddyNode.PeerID, config.Type_SubscriptionResponse)
	}

	// Create a new message with the ACK response
	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(buddyNode.PeerID).
		SetMessage(fmt.Sprintf("Subscription response: %t", accepted)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackBuilder)

	// Send the response via PubSub (consistent with how requests are received)
	// Create GossipMessage for publishing
	gossipMessage := AVCStruct.NewGossipMessageBuilder(nil).
		SetID(fmt.Sprintf("sub_response_%d", time.Now().UTC().UnixNano())).
		SetTopic(config.PubSub_ConsensusChannel).
		SetMesssage(message).
		SetSender(buddyNode.PeerID).
		SetTimestamp(time.Now().UTC().Unix())

	// Publish the response via PubSub
	if err := s.publishResponse(logger_ctx, gossipMessage); err != nil {
		err := errors.New("subscriptionService.sendSubscriptionResponse - failed to publish subscription response")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("sender", string(msg.Sender)),
			ion.String("topic", log.SubscriptionService),
			ion.String("function", "SubscriptionService.sendSubscriptionResponse"))

		return err
	}

	logger().NamedLogger.Info(logger_ctx, "Sent subscription response",
		ion.String("sender", string(msg.Sender)),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("accepted", fmt.Sprintf("%t", accepted)),
		ion.String("function", "SubscriptionService.sendSubscriptionResponse"))

	return nil
}

// publishResponse publishes a response message using the existing Publish service
func (s *SubscriptionService) publishResponse(logger_ctx context.Context, gossipMessage *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Publishing subscription response",
		ion.String("message_id", gossipMessage.ID),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.publishResponse"))
	if s.pubSub == nil {
		err := errors.New("subscriptionService.publishResponse - PubSub not available for publishing response")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", gossipMessage.ID),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.publishResponse"))
		return err
	}
	// Use the existing Publish service to send the message
	// This will handle all the networking, serialization, and distribution
	err := Publisher.Publish(logger_ctx, s.pubSub, config.PubSub_ConsensusChannel, gossipMessage.Data, map[string]string{})
	if err != nil {
		err := errors.New("subscriptionService.publishResponse - failed to publish response using Publisher service")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", gossipMessage.ID),
			ion.String("topic", config.PubSub_ConsensusChannel),
			ion.String("function", "SubscriptionService.publishResponse"))
		return err
	}

	logger().NamedLogger.Info(logger_ctx, "Published subscription response",
		ion.String("message_id", gossipMessage.ID),
		ion.String("topic", config.PubSub_ConsensusChannel),
		ion.String("function", "SubscriptionService.publishResponse"))

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
func (s *SubscriptionService) getBFTHandler(logger_ctx context.Context, channelName string) (BFTMessageHandler, error) {
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
		err := errors.New("subscriptionService.getBFTHandler - BFT factory not configured")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("topic", log.SubscriptionService),
			ion.String("channel", channelName),
			ion.String("function", "SubscriptionService.getBFTHandler"))
		return nil, err
	}

	handler, err := s.bftFactory(context.Background(), s.pubSub, channelName)
	if err != nil {
		err := errors.New("subscriptionService.getBFTHandler - failed to create BFT handler")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("topic", log.SubscriptionService),
			ion.String("channel", channelName),
			ion.String("function", "SubscriptionService.getBFTHandler"))
		return nil, err
	}

	s.bftHandlers[channelName] = handler
	logger().NamedLogger.Info(logger_ctx, "Created BFT handler for channel",
		ion.String("topic", log.SubscriptionService),
		ion.String("channel", channelName),
		ion.String("function", "SubscriptionService.getBFTHandler"))

	return handler, nil
}

// handleBFTRequest handles incoming BFT consensus requests
func (s *SubscriptionService) handleBFTRequest(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	logger().NamedLogger.Info(logger_ctx, "Handling BFT request",
		ion.String("topic", log.SubscriptionService),
		ion.String("message_id", msg.ID),
		ion.String("function", "SubscriptionService.handleBFTRequest"))

	// Parse the request data
	var reqData struct {
		Round          uint64                   `json:"Round"`
		BlockHash      string                   `json:"BlockHash"`
		GossipsubTopic string                   `json:"GossipsubTopic"`
		AllBuddies     []map[string]interface{} `json:"AllBuddies"`
	}

	if err := json.Unmarshal([]byte(msg.Data.Message), &reqData); err != nil {
		err := errors.New("subscriptionService.handleBFTRequest - failed to parse BFT request")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("message_id", msg.ID),
			ion.String("topic", log.SubscriptionService),
			ion.String("function", "SubscriptionService.handleBFTRequest"))
		return err
	}

	logger().NamedLogger.Info(logger_ctx, "BFT Request",
		ion.String("round", fmt.Sprintf("%d", reqData.Round)),
		ion.String("block_hash", reqData.BlockHash),
		ion.String("buddies", fmt.Sprintf("%d", len(reqData.AllBuddies))),
		ion.String("topic", log.SubscriptionService),
		ion.String("function", "SubscriptionService.handleBFTRequest"))

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
	handler, err := s.getBFTHandler(logger_ctx, msg.Topic)
	if err != nil {
		err := errors.New("subscriptionService.handleBFTRequest - failed to get BFT handler")
		logger().NamedLogger.Error(logger_ctx, err.Error(),
			err,
			ion.String("topic", log.SubscriptionService),
			ion.String("channel", msg.Topic),
			ion.String("function", "SubscriptionService.handleBFTRequest"))
		return err
	}

	// Start consensus process in a goroutine
	LocalGRO.Go(GRO.BuddyNodesMessageProtocolThread, func(ctx context.Context) error {
		ctxWithTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		logger().NamedLogger.Info(logger_ctx, "Starting consensus",
			ion.String("round", fmt.Sprintf("%d", reqData.Round)),
			ion.String("block_hash", reqData.BlockHash),
			ion.String("topic", log.SubscriptionService),
			ion.String("function", "SubscriptionService.handleBFTRequest"))

		result, err := handler.ProposeConsensus(
			ctxWithTimeout,
			reqData.Round,
			reqData.BlockHash,
			s.myBuddyID,
			allBuddies,
		)
		if err != nil {
			err := errors.New("subscriptionService.handleBFTRequest - failed to propose consensus")
			logger().NamedLogger.Error(logger_ctx, err.Error(),
				err,
				ion.String("topic", log.SubscriptionService),
				ion.String("function", "SubscriptionService.handleBFTRequest"))
			return err
		}

		logger().NamedLogger.Info(logger_ctx, "Consensus completed",
			ion.String("success", fmt.Sprintf("%t", result.Success)),
			ion.String("decision", string(result.Decision)),
			ion.String("block_accepted", fmt.Sprintf("%t", result.BlockAccepted)),
			ion.String("topic", log.SubscriptionService),
			ion.String("function", "SubscriptionService.handleBFTRequest"))

		// Call callback if set (for testing)
		if s.OnConsensusComplete != nil {
			s.OnConsensusComplete(result)
		}
		return nil
	})

	return nil
}

// handleBFTPrepareVote handles prepare vote messages
func (s *SubscriptionService) handleBFTPrepareVote(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	handler, err := s.getBFTHandler(logger_ctx, msg.Topic)
	if err != nil {
		return err
	}
	return handler.HandlePrepareVote(msg)
}

// handleBFTCommitVote handles commit vote messages
func (s *SubscriptionService) handleBFTCommitVote(logger_ctx context.Context, msg *AVCStruct.GossipMessage) error {
	handler, err := s.getBFTHandler(logger_ctx, msg.Topic)
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
