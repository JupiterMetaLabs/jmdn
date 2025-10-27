package MessagePassing

import (
	"bufio"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"go.uber.org/zap"
)

// ListenerHandler handles incoming messages on the SubmitMessageProtocol
// This handler processes subscription requests, vote submissions, and subscription responses
type ListenerHandler struct {
	responseHandler AVCStruct.ResponseHandler
}

// NewListenerHandler creates a new ListenerHandler instance
func NewListenerHandler(responseHandler AVCStruct.ResponseHandler) *ListenerHandler {
	return &ListenerHandler{
		responseHandler: responseHandler,
	}
}

// HandleSubmitMessageStream processes incoming messages on the SubmitMessageProtocol
// This is the main entry point for handling subscription requests, votes, and responses
// Note: Stream closure is handled by the caller to allow response reading
func (lh *ListenerHandler) HandleSubmitMessageStream(s network.Stream) {
	fmt.Println("=== ListenerHandler.HandleSubmitMessageStream CALLED ===")
	fmt.Printf("Received stream from: %s\n", s.Conn().RemotePeer())

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		fmt.Printf("Error reading message: %v\n", err)
		log.LogConsensusError(fmt.Sprintf("Error reading message from %s: %v", s.Conn().RemotePeer(), err),
			err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("message", msg),
			zap.String("function", "ListenerHandler.HandleSubmitMessageStream"))
		return
	}

	fmt.Printf("Raw message received: %s\n", msg)

	message := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(msg)
	if message == nil {
		fmt.Println("Failed to parse message - malformed JSON")
		log.LogMessagesError("Failed to parse message - malformed JSON or invalid structure",
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("raw_message", msg),
			zap.String("function", "ListenerHandler.HandleSubmitMessageStream"))
		return
	}

	fmt.Printf("Parsed message: %+v\n", message)
	fmt.Printf("ACK: %+v\n", message.GetACK())

	log.LogMessagesInfo(fmt.Sprintf("Received submit message from %s: %s", s.Conn().RemotePeer(), msg),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", log.Messages_TOPIC),
		zap.String("message", msg),
		zap.String("function", "ListenerHandler.HandleSubmitMessageStream"))

	// Check if ACK is not nil before accessing it
	if message.GetACK() == nil {
		fmt.Println("Received message with nil ACK")
		log.LogMessagesError("Received message with nil ACK",
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("raw_message", msg),
			zap.String("function", "ListenerHandler.HandleSubmitMessageStream"))
		return
	}

	fmt.Printf("ACK Stage: %s\n", message.GetACK().GetStage())

	// Route message based on ACK stage
	switch message.GetACK().GetStage() {
	case config.Type_SubmitVote:
		fmt.Println("Handling Type_SubmitVote")
		lh.handleSubmitVote(s, message)
	case config.Type_AskForSubscription:
		fmt.Println("Handling Type_AskForSubscription")
		lh.handleAskForSubscription(s, message)
	case config.Type_SubscriptionResponse:
		fmt.Println("Handling Type_SubscriptionResponse")
		lh.handleSubscriptionResponse(s, message)
	default:
		fmt.Printf("Unknown message type: %s\n", message.GetACK().GetStage())
		log.LogMessagesError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg),
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("message", msg),
			zap.String("function", "ListenerHandler.HandleSubmitMessageStream"))
	}
}

// handleSubmitVote processes vote submission messages
func (lh *ListenerHandler) handleSubmitVote(s network.Stream, message *AVCStruct.Message) {
	log.LogMessagesInfo(fmt.Sprintf("Received submit vote from %s: %s", s.Conn().RemotePeer(), message.Message),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", log.Messages_TOPIC),
		zap.String("function", "ListenerHandler.handleSubmitVote"))

	// Debugging
	fmt.Printf("=== THIS IS BUDDY NODE HANDLER FUNCTION - ListenerHandler.handleSubmitVote CALLED ===\n")
	fmt.Printf("message: %+v\n", message)
	fmt.Printf("From Peer: %s\n", s.Conn().RemotePeer())

	// Check if PubSubNode and ForListner are initialized
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()

	// Initialize PubSub node if not already done
	if pubSubNode == nil || pubSubNode.PubSub == nil {
		fmt.Printf("=== Initializing PubSub_BuddyNode for vote submission in ListenerHandler ===\n")
		// Get the ForListner (which should be initialized)
		if listenerNode == nil {
			fmt.Printf("=== THIS IS BUDDY NODE HANDLER FUNCTION - ForListner not initialized - cannot process vote ===\n")
			log.LogMessagesError("ForListner not initialized - cannot process vote",
				nil,
				zap.String("peer", s.Conn().RemotePeer().String()),
				zap.String("topic", log.Messages_TOPIC),
				zap.String("function", "ListenerHandler.handleSubmitVote"))
			return
		}

		// Create GossipPubSub for this node
		gps := AVCStruct.NewGossipPubSubBuilder(nil).
			SetHost(listenerNode.Host).
			SetProtocol(config.BuddyNodesMessageProtocol).
			Build()

		// Create and set PubSub_BuddyNode
		pubSubBuddyNode := NewBuddyNode(listenerNode.Host, &listenerNode.BuddyNodes, nil, gps)
		AVCStruct.NewGlobalVariables().Set_PubSubNode(pubSubBuddyNode)
		pubSubNode = pubSubBuddyNode
		fmt.Printf("=== PubSub_BuddyNode initialized successfully in ListenerHandler ===\n")
	}

	if listenerNode == nil {
		fmt.Printf("=== THIS IS BUDDY NODE HANDLER FUNCTION - ForListner not initialized - cannot process vote ===\n")
		log.LogMessagesError("ForListner not initialized - cannot process vote",
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("function", "ListenerHandler.handleSubmitVote"))
		return
	}

	// Add vote to local CRDT Engine
	if err := Structs.SubmitMessage(message, pubSubNode.PubSub, listenerNode); err != nil {
		fmt.Printf("=== THIS IS BUDDY NODE HANDLER FUNCTION - Failed to add vote to local CRDT Engine: %v ===\n", err)
		log.LogMessagesError(fmt.Sprintf("Failed to add vote to local CRDT Engine: %v", err),
			err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("function", "ListenerHandler.handleSubmitVote"))
		return
	}

	// Debugging
	fmt.Printf("Successfully processed vote from %s\n", s.Conn().RemotePeer())

	log.LogMessagesInfo(fmt.Sprintf("Successfully processed vote from %s", s.Conn().RemotePeer()),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("function", "ListenerHandler.handleSubmitVote"))
}

// handleAskForSubscription processes subscription request messages
func (lh *ListenerHandler) handleAskForSubscription(s network.Stream, message *AVCStruct.Message) {
	fmt.Println("=== ListenerHandler.handleAskForSubscription CALLED ===")
	fmt.Printf("Received subscription request from: %s\n", s.Conn().RemotePeer())
	fmt.Printf("Message: %s\n", message.Message)
	fmt.Printf("ACK Stage: %s\n", message.GetACK().GetStage())

	log.LogMessagesInfo(fmt.Sprintf("Received subscription request from %s", s.Conn().RemotePeer()),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", log.Messages_TOPIC),
		zap.String("function", "ListenerHandler.handleAskForSubscription"))

	// Check if ForListner is initialized
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.Host == nil {
		fmt.Println("ForListner not initialized - sending rejection response")
		log.LogMessagesError("ForListner not initialized - cannot process subscription request",
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("function", "ListenerHandler.handleAskForSubscription"))
		lh.sendSubscriptionResponse(s, false)
		return
	}

	fmt.Println("ForListner is initialized - processing subscription request")

	// Create GossipPubSub using Pubsub_Builder.go
	gps := AVCStruct.NewGossipPubSubBuilder(nil).
		SetHost(listenerNode.Host).
		SetProtocol(config.BuddyNodesMessageProtocol).
		Build()

	// Initialize PubSub BuddyNode if not already done
	if AVCStruct.NewGlobalVariables().Get_PubSubNode() == nil {
		// Create default Buddies instance
		defaultBuddies := AVCStruct.NewBuddiesBuilder(nil)
		buddy := NewBuddyNode(listenerNode.Host, defaultBuddies, nil, gps)
		AVCStruct.NewGlobalVariables().Set_PubSubNode(buddy)
	}

	// Delegate subscription logic to SubscriptionService
	service := Service.NewSubscriptionService(gps)
	err := service.HandleStreamSubscriptionRequest(config.PubSub_ConsensusChannel)
	if err != nil {
		fmt.Printf("Failed to subscribe to consensus channel via SubscriptionService: %v\n", err)
		log.LogMessagesError(fmt.Sprintf("Failed to subscribe to consensus channel: %v", err),
			err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "ListenerHandler.handleAskForSubscription"))
		lh.sendSubscriptionResponse(s, false)
		return
	}

	fmt.Println("Successfully subscribed to consensus channel via SubscriptionService")
	fmt.Println("Sending subscription response: true")
	lh.sendSubscriptionResponse(s, true)
}

// handleSubscriptionResponse processes subscription response messages
func (lh *ListenerHandler) handleSubscriptionResponse(s network.Stream, message *AVCStruct.Message) {
	log.LogMessagesInfo(fmt.Sprintf("Received subscription response from %s: %s", s.Conn().RemotePeer(), message.Message),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", log.Messages_TOPIC),
		zap.String("function", "ListenerHandler.handleSubscriptionResponse"))

	accepted := message.GetACK().GetStatus() == "ACK_TRUE"
	log.LogMessagesInfo(fmt.Sprintf("Subscription response from %s: %s", s.Conn().RemotePeer(),
		map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted]),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("accepted", fmt.Sprintf("%t", accepted)),
		zap.String("function", "ListenerHandler.handleSubscriptionResponse"))

	// Route the response to the ResponseHandler if available
	if lh.responseHandler != nil {
		lh.responseHandler.HandleResponse(s.Conn().RemotePeer(), accepted, "main")
		log.LogMessagesInfo("Successfully routed subscription response to ResponseHandler",
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("function", "ListenerHandler.handleSubscriptionResponse"))
	} else {
		log.LogMessagesInfo("No ResponseHandler set - subscription response logged only",
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("function", "ListenerHandler.handleSubscriptionResponse"))
	}
}

// sendSubscriptionResponse sends ACK response for subscription requests
func (lh *ListenerHandler) sendSubscriptionResponse(s network.Stream, accepted bool) {
	fmt.Printf("=== sendSubscriptionResponse called: %s ===\n", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted])
	fmt.Printf("Sending response to: %s\n", s.Conn().RemotePeer())

	host := s.Conn().LocalPeer()
	var ackBuilder *AVCStruct.ACK
	if accepted {
		ackBuilder = AVCStruct.NewACKBuilder().True_ACK_Message(host, config.Type_SubscriptionResponse)
	} else {
		ackBuilder = AVCStruct.NewACKBuilder().False_ACK_Message(host, config.Type_SubscriptionResponse)
	}

	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(host).
		SetMessage(fmt.Sprintf("Subscription %s", map[bool]string{true: "accepted", false: "rejected"}[accepted])).
		SetTimestamp(time.Now().Unix()).
		SetACK(ackBuilder)

	messageBytes, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("Failed to marshal response: %v\n", err)
		log.LogMessagesError(fmt.Sprintf("Failed to marshal response: %v", err), err)
		return
	}

	fmt.Printf("Response message: %s\n", string(messageBytes))

	// Send response back through the SAME stream (SubmitMessageProtocol)
	_, err = s.Write([]byte(string(messageBytes) + string(rune(config.Delimiter))))
	if err != nil {
		fmt.Printf("Failed to send response: %v\n", err)
		log.LogMessagesError(fmt.Sprintf("Failed to send response: %v", err), err)
	} else {
		fmt.Printf("Successfully sent subscription response: %s\n", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted])
		log.LogMessagesInfo(fmt.Sprintf("Sent subscription response: %s", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted]))
	}
}

// GetResponseHandler returns the current ResponseHandler
func (lh *ListenerHandler) GetResponseHandler() AVCStruct.ResponseHandler {
	return lh.responseHandler
}

// SetResponseHandler sets a new ResponseHandler
func (lh *ListenerHandler) SetResponseHandler(responseHandler AVCStruct.ResponseHandler) {
	lh.responseHandler = responseHandler
}
