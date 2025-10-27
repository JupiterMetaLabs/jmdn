package MessagePassing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	ServiceLayer "gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	Publisher "gossipnode/Pubsub/Publish"
	"gossipnode/Sequencer/Triggers"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"go.uber.org/zap"
)

// ListenerHandler handles incoming messages on the SubmitMessageProtocol
// This handler processes subscription requests, vote submissions, and subscription responses
type ListenerHandler struct {
	responseHandler AVCStruct.ResponseHandler
}

type VoteResult map[string]int8

var (
	voteResultTimer      *time.Timer
	voteResultTimerMutex sync.Mutex
	TIMER                = 5 * time.Second
)

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
		// Close stream after handling vote
		defer s.Close()
	case config.Type_AskForSubscription:
		fmt.Println("Handling Type_AskForSubscription")
		lh.handleAskForSubscription(s, message)
		// Stream is closed in sendSubscriptionResponse after sending the response
	case config.Type_SubscriptionResponse:
		fmt.Println("Handling Type_SubscriptionResponse")
		lh.handleSubscriptionResponse(s, message)
		defer s.Close()
	case config.Type_VoteResult:
		fmt.Println("Handling Type_VoteResult")
		lh.handleVoteResult(s, message)
		defer s.Close()
	default:
		fmt.Printf("Unknown message type: %s\n", message.GetACK().GetStage())
		log.LogMessagesError(fmt.Sprintf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg),
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", log.Messages_TOPIC),
			zap.String("message", msg),
			zap.String("function", "ListenerHandler.HandleSubmitMessageStream"))
		defer s.Close()
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

	// Add vote to local CRDT Engine WITHOUT republishing to pubsub (to avoid loops)
	// Parse the vote message
	var voteData map[string]interface{}
	if err := json.Unmarshal([]byte(message.Message), &voteData); err != nil {
		fmt.Printf("=== THIS IS BUDDY NODE HANDLER FUNCTION - Failed to unmarshal vote message: %v ===\n", err)
		return
	}

	if _, exists := voteData["vote"]; exists {
		OP := &Types.OP{
			NodeID: message.Sender,
			OpType: int8(1), // 1 for add, -1 for remove
			KeyValue: Types.KeyValue{
				Key:   message.Sender.String(), // key would be the peer id of the sender
				Value: message.Message,
			},
		}

		result := ServiceLayer.Controller(listenerNode.CRDTLayer, OP)
		if err, ok := result.(error); ok && err != nil {
			fmt.Printf("=== THIS IS BUDDY NODE HANDLER FUNCTION - Failed to add vote to CRDT: %v ===\n", err)
			return
		}

		fmt.Printf("=== THIS IS BUDDY NODE HANDLER FUNCTION - Successfully added vote to CRDT ===\n")

		// Now publish the vote to pubsub so ALL other buddy nodes can receive it
		if pubSubNode != nil && pubSubNode.PubSub != nil {
			fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
			fmt.Printf("║  REPUBLISHING VOTE TO PUBSUB FOR ALL BUDDY NODES         ║\n")
			fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
			fmt.Printf("📤 Republishing from Buddy Node: %s\n", listenerNode.PeerID.String())
			fmt.Printf("📝 Original Vote Message: %s\n", message.Message)
			fmt.Printf("🆔 Original Sender: %s\n", message.Sender.String())
			fmt.Printf("📡 Republishing to Channel: %s\n", config.PubSub_ConsensusChannel)
			fmt.Printf("⏰ Timestamp: %d\n", message.Timestamp)
			fmt.Printf("═══════════════════════════════════════════════════════════\n")

			// This is necessary because the vote was sent via direct stream to ONE node
			// We need to republish it to pubsub so ALL buddy nodes receive it
			if err := Publisher.Publish(pubSubNode.PubSub, config.PubSub_ConsensusChannel, message, map[string]string{}); err != nil {
				fmt.Printf("❌ Failed to republish vote to pubsub: %v\n", err)
			} else {
				fmt.Printf("✅ Successfully republished vote to pubsub - ALL buddy nodes will now receive this vote\n\n")
			}
		} else {
			fmt.Printf("⚠️ Cannot republish vote - pubSubNode or pubSubNode.PubSub is nil\n")
		}
	}

	// Debugging
	fmt.Printf("Successfully processed vote from %s\n", s.Conn().RemotePeer())

	log.LogMessagesInfo(fmt.Sprintf("Successfully processed vote from %s", s.Conn().RemotePeer()),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("function", "ListenerHandler.handleSubmitVote"))

	// Print CRDT state after processing the vote (non-blocking)
	go func() {
		// Wait for more votes to be processed (increased from 5s to 15s)
		time.Sleep(5 * time.Second)
		if err := Structs.PrintCRDTState(listenerNode); err != nil {
			fmt.Printf("Failed to print CRDT state: %v\n", err)
		}

		// Process votes from CRDT and get the voting result
		result, err := Structs.ProcessVotesFromCRDT(listenerNode)
		if err != nil {
			fmt.Printf("❌ Failed to process votes from CRDT: %v\n", err)
			return
		}

		// Trigger to send the vote to the sequencer
		// Fixed: Add nil check for Triggers, and error handling if SendVoteResultToSequencer is not defined
		// SUbmit to the handler function to send the vote to the sequencer
		message := AVCStruct.NewMessageBuilder(nil).
			SetSender(listenerNode.PeerID).
			SetMessage(fmt.Sprintf("Vote result: %d", result)).
			SetTimestamp(time.Now().Unix()).
			SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_VoteResult))

		lh.sendVoteResultToSequencer(s, message)
		fmt.Printf("✅ Vote result sent to sequencer successfully: %s\n", message.Message)
	}()

}

func (lh *ListenerHandler) RequestForVoteResult(s network.Stream, message *AVCStruct.Message) {
	fmt.Println("=== ListenerHandler.RequestForVoteResult CALLED ===")
	fmt.Printf("Received request for vote result from: %s\n", s.Conn().RemotePeer())
	fmt.Printf("Message: %s\n", message.Message)
	fmt.Printf("ACK Stage: %s\n", message.GetACK().GetStage())
	
	// Check if ForListner is initialized
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.Host == nil {
		fmt.Println("ForListner not initialized - sending rejection response")
		log.LogMessagesError("ForListner not initialized - cannot process request for vote result",
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "ListenerHandler.RequestForVoteResult"))
		
			return
	}

	// Write a message to the node to send the vote result

	fmt.Println("ForListner is initialized - processing request for vote result")

}

// handleAskForSubscription processes subscription request messages
func (lh *ListenerHandler) handleAskForSubscription(s network.Stream, message *AVCStruct.Message) {
	fmt.Println("=== ListenerHandler.handleAskForSubscription CALLED ===")
	fmt.Printf("Received subscription request from: %s\n", s.Conn().RemotePeer())
	fmt.Printf("Message: %s\n", message.Message)
	fmt.Printf("ACK Stage: %s\n", message.GetACK().GetStage())

	log.LogMessagesInfo(fmt.Sprintf("Received subscription request from %s", s.Conn().RemotePeer()),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", config.PubSub_ConsensusChannel),
		zap.String("function", "ListenerHandler.handleAskForSubscription"))

	// Check if ForListner is initialized
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.Host == nil {
		fmt.Println("ForListner not initialized - sending rejection response")
		log.LogMessagesError("ForListner not initialized - cannot process subscription request",
			nil,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "ListenerHandler.handleAskForSubscription"))
		lh.sendSubscriptionResponse(s, false)
		return
	}

	fmt.Println("ForListner is initialized - processing subscription request")

	// Use config.PubSub_ConsensusChannel as the GossipSub topic
	topicToSubscribe := config.PubSub_ConsensusChannel
	fmt.Printf("Subscribing to GossipSub topic: %s\n", topicToSubscribe)

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

	// Delegate subscription logic to SubscriptionService with config.PubSub_ConsensusChannel
	fmt.Println("About to call HandleStreamSubscriptionRequest...")
	service := Service.NewSubscriptionService(gps)
	fmt.Println("Service created, calling HandleStreamSubscriptionRequest...")

	err := service.HandleStreamSubscriptionRequest(topicToSubscribe)
	fmt.Printf("HandleStreamSubscriptionRequest returned, err=%v\n", err)

	if err != nil {
		fmt.Printf("❌ Failed to subscribe to consensus channel via SubscriptionService: %v\n", err)
		log.LogMessagesError(fmt.Sprintf("Failed to subscribe to consensus channel: %v", err),
			err,
			zap.String("peer", s.Conn().RemotePeer().String()),
			zap.String("topic", config.PubSub_ConsensusChannel),
			zap.String("function", "ListenerHandler.handleAskForSubscription"))
		lh.sendSubscriptionResponse(s, false)
		return
	}

	fmt.Println("✅ Successfully subscribed to consensus channel via SubscriptionService")
	fmt.Println("📤 Sending subscription response: true")

	// IMPORTANT: sendSubscriptionResponse will close the stream
	// Make sure we send the response before the stream is closed
	lh.sendSubscriptionResponse(s, true)

	fmt.Println("✅ sendSubscriptionResponse completed")
}

// handleSubscriptionResponse processes subscription response messages
func (lh *ListenerHandler) handleSubscriptionResponse(s network.Stream, message *AVCStruct.Message) {
	log.LogMessagesInfo(fmt.Sprintf("Received subscription response from %s: %s", s.Conn().RemotePeer(), message.Message),
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("topic", config.PubSub_ConsensusChannel),
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
		fmt.Printf("❌ Failed to marshal response: %v\n", err)
		log.LogMessagesError(fmt.Sprintf("Failed to marshal response: %v", err), err)
		s.Close()
		return
	}

	fmt.Printf("Response message: %s\n", string(messageBytes))

	// Send response back through the SAME stream (SubmitMessageProtocol)
	bytesWritten, err := s.Write([]byte(string(messageBytes) + string(rune(config.Delimiter))))
	if err != nil {
		fmt.Printf("❌ Failed to write response: %v (wrote %d bytes)\n", err, bytesWritten)
		log.LogMessagesError(fmt.Sprintf("Failed to send response: %v", err), err)
		s.Close()
		return
	}

	fmt.Printf("✅ Successfully wrote %d bytes to stream\n", bytesWritten)
	fmt.Printf("✅ Successfully sent subscription response: %s\n", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted])
	log.LogMessagesInfo(fmt.Sprintf("Sent subscription response: %s", map[bool]string{true: "ACCEPTED", false: "REJECTED"}[accepted]))

	// Give a small delay to ensure the data is sent before closing
	time.Sleep(50 * time.Millisecond)

	fmt.Printf("Closing stream now...\n")
	s.Close()
	fmt.Printf("Stream closed\n")
}

// GetResponseHandler returns the current ResponseHandler
func (lh *ListenerHandler) GetResponseHandler() AVCStruct.ResponseHandler {
	return lh.responseHandler
}

// handleVoteResult handles incoming vote result messages from buddy nodes
func (lh *ListenerHandler) handleVoteResult(s network.Stream, message *AVCStruct.Message) {
	// if new vote comes then the timer would start, if new vote comes then the timer would restart
	// if the timer reaches 0 then you should give the result to the BFT Decision
	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║            RECEIVED VOTE RESULT                            ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("📨 Received from: %s\n", s.Conn().RemotePeer().String())
	fmt.Printf("📊 Vote Result: %s\n", message.Message)
	fmt.Printf("⏰ Timestamp: %d\n", message.Timestamp)
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	// Parse the vote result message
	var resultData map[string]interface{}
	if err := json.Unmarshal([]byte(message.Message), &resultData); err != nil {
		fmt.Printf("❌ Failed to parse vote result: %v\n", err)
		return
	}

	// Extract the result value
	resultValue, ok := resultData["result"].(float64)
	if !ok {
		fmt.Printf("❌ Invalid vote result format\n")
		return
	}

	// Store the result in the global vote results map
	// Use the message sender's peer ID as the key (this is the buddy node)
	peerID := message.Sender
	voteResult := int8(resultValue)

	// Store in global hashmap
	Triggers.StoreVoteResult(peerID.String(), voteResult)
	fmt.Printf("✅ Stored vote result in global map for peer %s: %d\n", peerID.String(), voteResult)
	fmt.Printf("📊 Current vote results map: %v\n", Triggers.GetAllVoteResults())

	// Timer logic: if new vote comes then timer restarts
	voteResultTimerMutex.Lock()

	// If timer already exists, stop it and restart
	if voteResultTimer != nil {
		voteResultTimer.Stop()
		fmt.Printf("🔄 Restarting vote result timer due to new vote from %s\n", peerID.String())
	}

	// Create new timer
	voteResultTimer = time.AfterFunc(TIMER, func() {
		fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
		fmt.Printf("║     VOTE RESULT TIMER EXPIRED - RUNNING BFT                ║\n")
		fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")

		// Get all vote results
		voteResults := Triggers.GetAllVoteResults()
		fmt.Printf("📊 Collected vote results: %v\n", voteResults)

		if len(voteResults) == 0 {
			fmt.Printf("⚠️ No vote results collected\n")
			return
		}

		// Get listener node
		listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
		if listenerNode == nil || len(listenerNode.BuddyNodes.Buddies_Nodes) == 0 {
			fmt.Printf("❌ Listener node or buddies not initialized\n")
			return
		}

		// Convert vote results to BFT input
		allBuddies := make([]bft.BuddyInput, 0)
		for _, buddy := range listenerNode.BuddyNodes.Buddies_Nodes {
			buddyID := buddy.String()
			result, hasResult := voteResults[buddyID]

			// Only include buddies that have submitted their vote results
			if hasResult {
				decision := bft.Reject
				if result > 0 {
					decision = bft.Accept
				}

				allBuddies = append(allBuddies, bft.BuddyInput{
					ID:         buddyID,
					Decision:   decision,
					PublicKey:  []byte(buddyID), // Use buddy ID as placeholder public key
					PrivateKey: []byte{},        // Empty for non-local nodes
				})

				fmt.Printf("  ✓ Buddy %s: Decision=%s (vote_result=%d)\n", buddyID, decision, result)
			} else {
				fmt.Printf("  ✗ Buddy %s: No vote result available (excluded from BFT)\n", buddyID)
			}
		}

		if len(allBuddies) == 0 {
			fmt.Printf("⚠️ No valid buddies for BFT consensus\n")
			return
		}

		fmt.Printf("🔔 Starting BFT consensus with %d buddies (out of %d total)\n",
			len(allBuddies), len(listenerNode.BuddyNodes.Buddies_Nodes))

		// Create BFT config
		bftConfig := bft.Config{
			MinBuddies:         4,
			ByzantineTolerance: (len(allBuddies) - 1) / 3,
		}

		fmt.Printf("✅ BFT config created with Byzantine tolerance: %d\n", bftConfig.ByzantineTolerance)
		fmt.Printf("   - MinBuddies: %d\n", bftConfig.MinBuddies)
		fmt.Printf("   - All buddies mapped successfully\n")
		fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

		// Create BFT instance
		bftInstance := bft.New(bftConfig)

		// Get context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Create a simple in-memory messenger for the consensus
		// Determine my buddy ID (the sequencer's peer ID)
		adapter, err := bft.NewBFTPubSubAdapter(context.Background(), AVCStruct.NewGlobalVariables().Get_PubSubNode().PubSub, bftInstance, config.PubSub_ConsensusChannel)
		if err != nil {
			fmt.Printf("❌ Failed to create BFT adapter: %v\n", err)
			return
		}
		messenger := bft.Return_pubsubMessenger(adapter, listenerNode.PeerID.String())
		myBuddyID := listenerNode.PeerID.String()

		// Generate round and block hash
		round := uint64(time.Now().Unix())
		blockHash := "consensus_block_" + fmt.Sprintf("%d", time.Now().Unix())

		fmt.Printf("🚀 Starting BFT Consensus\n")
		fmt.Printf("   - Round: %d\n", round)
		fmt.Printf("   - Block Hash: %s\n", blockHash)
		fmt.Printf("   - My Buddy ID: %s\n", myBuddyID)
		fmt.Printf("   - Total Buddies: %d\n", len(allBuddies))

		// Run BFT consensus
		result, err := bftInstance.RunConsensus(ctx, round, blockHash, myBuddyID, allBuddies, messenger, nil)

		if err != nil {
			fmt.Printf("❌ BFT Consensus Failed: %v\n", err)
			fmt.Printf("   - Failure Reason: %s\n", result.FailureReason)
			fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")
			return
		}

		// Log consensus result
		fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
		fmt.Printf("║           BFT CONSENSUS COMPLETED                        ║\n")
		fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
		fmt.Printf("✅ Success: %v\n", result.Success)
		fmt.Printf("📊 Decision: %s\n", result.Decision)
		fmt.Printf("📦 Block Accepted: %v\n", result.BlockAccepted)
		fmt.Printf("⏱️  Total Duration: %v\n", result.TotalDuration)
		fmt.Printf("   - Prepare: %v\n", result.PrepareDuration)
		fmt.Printf("   - Commit: %v\n", result.CommitDuration)
		fmt.Printf("📈 Votes: %d prepare, %d commit\n", result.PrepareCount, result.CommitCount)
		fmt.Printf("👥 Participants: %d total buddies\n", result.TotalBuddies)

		if len(result.ByzantineDetected) > 0 {
			fmt.Printf("⚠️  Byzantine nodes detected: %v\n", result.ByzantineDetected)
		}

		fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")
	})

	fmt.Printf("⏰ Started/restarted vote result timer for %v\n", TIMER)

	// Unlock the mutex here (defer was in the wrong scope)
	voteResultTimerMutex.Unlock()

	// Get listener node from global variables
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		fmt.Printf("❌ Listener node not initialized\n")
		return
	}

	// Send acknowledgment back
	ackMessage := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_ACK_True)
	response := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage("Vote result received").
		SetTimestamp(time.Now().Unix()).
		SetACK(ackMessage)

	responseBytes, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("❌ Failed to marshal response: %v\n", err)
		return
	}

	s.Write([]byte(string(responseBytes) + string(rune(config.Delimiter))))
	fmt.Printf("✅ Acknowledgment sent to peer %s\n", peerID.String())
}

func (lh *ListenerHandler) sendVoteResultToSequencer(s network.Stream, message *AVCStruct.Message) {
	fmt.Printf("Sending vote result to sequencer: %s\n", message.Message)

	// Get the sequencer peer ID from the subscription cache
	// The sequencer is the first peer in the cache (from when we subscribed)
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if pubSubNode == nil || len(pubSubNode.BuddyNodes.Buddies_Nodes) == 0 {
		fmt.Printf("❌ Cannot send vote result - no sequencer peer found\n")
		return
	}

	// The sequencer is typically the first peer we connected to
	sequencerPeerID := pubSubNode.BuddyNodes.Buddies_Nodes[0]
	host := AVCStruct.NewGlobalVariables().Get_ForListner().Host

	// Open a stream to the sequencer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := host.NewStream(ctx, sequencerPeerID, config.SubmitMessageProtocol)
	if err != nil {
		fmt.Printf("❌ Failed to open stream to sequencer: %v\n", err)
		return
	}
	defer stream.Close()

	messageBytes, err := json.Marshal(message)
	if err != nil {
		fmt.Printf("❌ Failed to marshal message: %v\n", err)
		return
	}

	writer := bufio.NewWriter(s)
	_, err = writer.WriteString(string(messageBytes) + string(rune(config.Delimiter)))
	if err != nil {
		fmt.Printf("❌ Failed to write message: %v\n", err)
		return
	}
	err = writer.Flush()
	if err != nil {
		fmt.Printf("❌ Failed to flush message: %v\n", err)
		return
	}

	fmt.Printf("✅ Vote result sent to sequencer successfully\n")
	fmt.Printf("📝 Message sent: %s\n", message.Message)
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")
}

// SetResponseHandler sets a new ResponseHandler
func (lh *ListenerHandler) SetResponseHandler(responseHandler AVCStruct.ResponseHandler) {
	lh.responseHandler = responseHandler
}
