package MessagePassing

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/CRDTSync"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	ServiceLayer "gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/AVC/BuddyNodes/common"
	Publisher "gossipnode/Pubsub/Publish"
	"gossipnode/Sequencer/Triggers/Maps"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// BFTContext holds the context for a BFT consensus round
type BFTContext struct {
	Round           uint64
	BlockHash       string
	AllBuddies      []bft.BuddyInput
	SequencerPeerID string
	GossipsubTopic  string
}

// ListenerHandler handles incoming messages on the SubmitMessageProtocol
// This handler processes subscription requests, vote submissions, and subscription responses
type ListenerHandler struct {
	responseHandler AVCStruct.ResponseHandler

	// BFT context storage (keyed by round-blockHash)
	bftContextMutex sync.RWMutex
	bftContexts     map[string]*BFTContext

	// Sequencer peer ID storage
	sequencerPeerID string
	sequencerMutex  sync.RWMutex
}

// NewListenerHandler creates a new ListenerHandler instance
func NewListenerHandler(responseHandler AVCStruct.ResponseHandler) *ListenerHandler {
	return &ListenerHandler{
		responseHandler: responseHandler,
		bftContexts:     make(map[string]*BFTContext),
	}
}

// HandleSubmitMessageStream processes incoming messages on the SubmitMessageProtocol
// This is the main entry point for handling subscription requests, votes, and responses
// Note: Stream closure is handled by the caller to allow response reading
func (lh *ListenerHandler) HandleSubmitMessageStream(s network.Stream) {
	fmt.Println("=== ListenerHandler.HandleSubmitMessageStream CALLED ===")
	fmt.Printf("Received stream from: %s\n", s.Conn().RemotePeer())
	fmt.Printf("🔄 STREAM RECEIVED FROM REMOTE PEER\n")

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
	case config.Type_BFTRequest:
		fmt.Println("Handling Type_BFTRequest")
		lh.handleBFTRequest(s, message)
		defer s.Close()
	case config.Type_SubmitVote:
		fmt.Println("Handling Type_SubmitVote")
		lh.handleSubmitVote(s, message)
		defer s.Close()
	case config.Type_AskForSubscription:
		fmt.Println("Handling Type_AskForSubscription")
		lh.handleAskForSubscription(s, message)
	case config.Type_SubscriptionResponse:
		fmt.Println("Handling Type_SubscriptionResponse")
		lh.handleSubscriptionResponse(s, message)
		defer s.Close()
	case config.Type_VoteResult:
		fmt.Println("🚨🚨🚨 HANDLING Type_VoteResult - VOTE RESULT REQUEST 🚨🚨🚨")
		lh.handleVoteResultRequest(s, message)
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

// handleBFTRequest processes BFT consensus request from Sequencer
func (lh *ListenerHandler) handleBFTRequest(s network.Stream, message *AVCStruct.Message) {
	if ListenerHandlerLocal == nil {
		var err error
		ListenerHandlerLocal, err = common.InitializeGRO(GRO.HandleBFTRequestLocal)
		if err != nil {
			fmt.Printf("❌ Failed to initialize ListenerHandler local manager: %v\n", err)
			return
		}
	}
	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║          RECEIVED BFT REQUEST FROM SEQUENCER               ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("📨 Received from: %s\n", s.Conn().RemotePeer().String())
	fmt.Printf("📋 Message: %s\n", message.Message)
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	log.LogConsensusInfo("Received BFT request from Sequencer",
		zap.String("peer", s.Conn().RemotePeer().String()),
		zap.String("function", "ListenerHandler.handleBFTRequest"))

	// Store Sequencer peer ID
	lh.sequencerMutex.Lock()
	lh.sequencerPeerID = s.Conn().RemotePeer().String()
	lh.sequencerMutex.Unlock()

	// Parse BFT request
	var requestData struct {
		Round          uint64 `json:"round"`
		BlockHash      string `json:"block_hash"`
		GossipsubTopic string `json:"gossipsub_topic"`
		AllBuddies     []struct {
			ID         string `json:"id"`
			Decision   string `json:"decision"`
			PublicKey  []byte `json:"public_key"`
			PrivateKey []byte `json:"private_key,omitempty"`
		} `json:"all_buddies"`
	}

	if err := json.Unmarshal([]byte(message.Message), &requestData); err != nil {
		fmt.Printf("❌ Failed to parse BFT request: %v\n", err)
		log.LogConsensusError("Failed to parse BFT request", err,
			zap.String("function", "ListenerHandler.handleBFTRequest"))
		return
	}

	fmt.Printf("📊 BFT Request Details:\n")
	fmt.Printf("   Round: %d\n", requestData.Round)
	fmt.Printf("   Block Hash: %s\n", requestData.BlockHash)
	fmt.Printf("   Gossipsub Topic: %s\n", requestData.GossipsubTopic)
	fmt.Printf("   Total Buddies: %d\n", len(requestData.AllBuddies))

	// Get listener node
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		fmt.Printf("❌ Listener node not initialized\n")
		return
	}

	myBuddyID := listenerNode.PeerID.String()

	// Check if I'm in the buddy list
	amIaBuddy := false
	buddies := make([]bft.BuddyInput, 0)

	for _, buddy := range requestData.AllBuddies {
		decision := bft.Accept
		if buddy.Decision == "REJECT" {
			decision = bft.Reject
		}

		buddyInput := bft.BuddyInput{
			ID:         buddy.ID,
			Decision:   decision,
			PublicKey:  buddy.PublicKey,
			PrivateKey: nil,
		}

		if buddy.ID == myBuddyID {
			amIaBuddy = true
			buddyInput.PrivateKey = buddy.PrivateKey
			fmt.Printf("✅ I am in the buddy list! My decision: %s\n", decision)
		}

		buddies = append(buddies, buddyInput)
	}

	if !amIaBuddy {
		fmt.Printf("⚠️ I'm not in the buddy list for this round - skipping\n")
		return
	}

	// Store BFT context
	contextKey := fmt.Sprintf("%d-%s", requestData.Round, requestData.BlockHash)
	lh.bftContextMutex.Lock()
	lh.bftContexts[contextKey] = &BFTContext{
		Round:           requestData.Round,
		BlockHash:       requestData.BlockHash,
		AllBuddies:      buddies,
		SequencerPeerID: s.Conn().RemotePeer().String(),
		GossipsubTopic:  requestData.GossipsubTopic,
	}
	lh.bftContextMutex.Unlock()

	fmt.Printf("✅ BFT context stored for round %d\n", requestData.Round)

	// Send acknowledgment back to Sequencer
	lh.sendBFTAcknowledgment(s, requestData.Round, requestData.BlockHash, true)

	// Start BFT consensus in background
	ListenerHandlerLocal.Go(GRO.BFTConsensusThread, func(ctx context.Context) error {
		lh.runBFTConsensusFlow(contextKey)
		return nil
	})
}

// sendBFTAcknowledgment sends ACK back to Sequencer
func (lh *ListenerHandler) sendBFTAcknowledgment(s network.Stream, round uint64, blockHash string, accepted bool) {
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		return
	}

	ackData := map[string]interface{}{
		"round":      round,
		"block_hash": blockHash,
		"accepted":   accepted,
		"buddy_id":   listenerNode.PeerID.String(),
		"message":    "BFT request accepted, starting consensus",
	}

	ackJSON, _ := json.Marshal(ackData)

	ack := AVCStruct.NewACKBuilder().
		True_ACK_Message(listenerNode.PeerID, config.Type_ACK_True)

	response := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(string(ackJSON)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ack)

	responseBytes, _ := json.Marshal(response)
	s.Write([]byte(string(responseBytes) + string(rune(config.Delimiter))))

	fmt.Printf("✅ Sent BFT acknowledgment to Sequencer\n")
}

// runBFTConsensusFlow executes the full BFT consensus flow
func (lh *ListenerHandler) runBFTConsensusFlow(contextKey string) {
	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║          STARTING BFT CONSENSUS FLOW                       ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")

	// Retrieve BFT context
	lh.bftContextMutex.RLock()
	bftCtx, exists := lh.bftContexts[contextKey]
	lh.bftContextMutex.RUnlock()

	if !exists {
		fmt.Printf("❌ BFT context not found for key: %s\n", contextKey)
		return
	}

	fmt.Printf("📊 Round: %d\n", bftCtx.Round)
	fmt.Printf("📦 Block Hash: %s\n", bftCtx.BlockHash)
	fmt.Printf("👥 Buddies: %d\n", len(bftCtx.AllBuddies))
	fmt.Printf("📡 Topic: %s\n", bftCtx.GossipsubTopic)
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	// Get listener node
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		fmt.Printf("❌ Listener node not initialized\n")
		return
	}

	// Get PubSub node
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if pubSubNode == nil || pubSubNode.PubSub == nil {
		fmt.Printf("❌ PubSub node not initialized\n")
		return
	}

	myBuddyID := listenerNode.PeerID.String()

	// Create BFT engine with UPDATED config including activity-based settings
	bftConfig := bft.DefaultConfig()
	bftConfig.PrepareTimeout = 15 * time.Second
	bftConfig.CommitTimeout = 15 * time.Second

	// ✅ ADD ACTIVITY-BASED SETTINGS HERE
	bftConfig.InactivityTimeout = 5 * time.Second // 5 seconds of silence

	fmt.Printf("🔧 BFT Config:\n")
	fmt.Printf("   Prepare Timeout: %v\n", bftConfig.PrepareTimeout)
	fmt.Printf("   Commit Timeout: %v\n", bftConfig.CommitTimeout)
	fmt.Printf("   Inactivity Timeout: %v\n", bftConfig.InactivityTimeout)

	bftEngine := bft.New(bftConfig)

	// Create BFT adapter
	ctx := context.Background()
	adapter, err := bft.NewBFTPubSubAdapter(
		ctx,
		pubSubNode.PubSub,
		bftEngine,
		bftCtx.GossipsubTopic,
	)
	if err != nil {
		fmt.Printf("❌ Failed to create BFT adapter: %v\n", err)
		lh.sendBFTResultToSequencer(bftCtx.Round, bftCtx.BlockHash, myBuddyID, false, "REJECT", fmt.Sprintf("Failed to create adapter: %v", err))
		return
	}
	defer adapter.Close()

	fmt.Printf("✅ BFT adapter created successfully\n")

	// Small delay to ensure all buddies are ready
	time.Sleep(2 * time.Second)

	// Run BFT consensus
	fmt.Printf("🚀 Running BFT consensus...\n")
	result, err := adapter.ProposeConsensus(
		ctx,
		bftCtx.Round,
		bftCtx.BlockHash,
		myBuddyID,
		bftCtx.AllBuddies,
	)

	if err != nil {
		fmt.Printf("\n❌ BFT CONSENSUS FAILED\n")
		fmt.Printf("   Error: %v\n", err)
		lh.sendBFTResultToSequencer(bftCtx.Round, bftCtx.BlockHash, myBuddyID, false, "REJECT", fmt.Sprintf("Consensus failed: %v", err))
		return
	}

	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║          BFT CONSENSUS COMPLETED SUCCESSFULLY              ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("✅ Success: %v\n", result.Success)
	fmt.Printf("📊 Decision: %s\n", result.Decision)
	fmt.Printf("✓ Block Accepted: %v\n", result.BlockAccepted)
	fmt.Printf("⏱️  Duration: %v\n", result.TotalDuration)
	fmt.Printf("📥 Prepare Count: %d\n", result.PrepareCount)
	fmt.Printf("📤 Commit Count: %d\n", result.CommitCount)
	if len(result.ByzantineDetected) > 0 {
		fmt.Printf("⚠️  Byzantine Nodes: %v\n", result.ByzantineDetected)
	}
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	// Send result to Sequencer
	lh.sendBFTResultToSequencer(
		bftCtx.Round,
		bftCtx.BlockHash,
		myBuddyID,
		result.Success,
		string(result.Decision),
		result.FailureReason,
	)

	// Cleanup context
	lh.bftContextMutex.Lock()
	delete(lh.bftContexts, contextKey)
	lh.bftContextMutex.Unlock()
}

// sendBFTResultToSequencer reports BFT consensus result back to Sequencer
func (lh *ListenerHandler) sendBFTResultToSequencer(
	round uint64,
	blockHash string,
	buddyID string,
	success bool,
	decision string,
	failureReason string,
) {
	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║       SENDING BFT RESULT TO SEQUENCER                     ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("📤 Round: %d\n", round)
	fmt.Printf("📦 Block Hash: %s\n", blockHash)
	fmt.Printf("🆔 Buddy ID: %s\n", buddyID)
	fmt.Printf("✅ Success: %v\n", success)
	fmt.Printf("📊 Decision: %s\n", decision)
	if failureReason != "" {
		fmt.Printf("❌ Failure Reason: %s\n", failureReason)
	}
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	// Get listener node
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		fmt.Printf("❌ Listener node not initialized\n")
		return
	}

	// Get Sequencer peer ID
	lh.sequencerMutex.RLock()
	sequencerPeerIDStr := lh.sequencerPeerID
	lh.sequencerMutex.RUnlock()

	if sequencerPeerIDStr == "" {
		fmt.Printf("❌ Sequencer peer ID not found\n")
		return
	}

	// Derive vote from decision and prepare BLS signature over blockHash
	var vote int8 = -1
	if success && decision == "ACCEPT" {
		vote = 1
	}

	// Create result message (include BLS payload)
	resultData := map[string]interface{}{
		"round":          round,
		"block_hash":     blockHash,
		"buddy_id":       buddyID,
		"success":        success,
		"decision":       decision,
		"block_accepted": success && decision == "ACCEPT",
		"failure_reason": failureReason,
		"timestamp":      time.Now().UTC().Unix(),
		"vote":           vote,
	}
	resultJSON, err := json.Marshal(resultData)
	if err != nil {
		fmt.Printf("❌ Failed to marshal result: %v\n", err)
		return
	}

	// Create ACK for BFT result
	ack := AVCStruct.NewACKBuilder().
		SetTrueStatus().
		SetPeerID(listenerNode.PeerID).
		SetStage(config.Type_BFTResult)

	// Create message
	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(string(resultJSON)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ack)

	// Decode Sequencer peer ID
	sequencerPeerID, err := peer.Decode(sequencerPeerIDStr)
	if err != nil {
		fmt.Printf("❌ Failed to decode sequencer peer ID: %v\n", err)
		return
	}

	// Open stream to Sequencer
	stream, err := listenerNode.Host.NewStream(
		context.Background(),
		sequencerPeerID,
		config.BuddyNodesMessageProtocol,
	)
	if err != nil {
		fmt.Printf("❌ Failed to open stream to Sequencer: %v\n", err)
		return
	}
	defer stream.Close()

	// Send message
	messageBytes, _ := json.Marshal(message)
	_, err = stream.Write([]byte(string(messageBytes) + string(rune(config.Delimiter))))
	if err != nil {
		fmt.Printf("❌ Failed to send result to Sequencer: %v\n", err)
		return
	}

	fmt.Printf("✅ Successfully sent BFT result to Sequencer\n")
	log.LogConsensusInfo("Sent BFT result to Sequencer",
		zap.Uint64("round", round),
		zap.String("decision", decision),
		zap.Bool("success", success))
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

	// Validate sender authenticity
	if message.Sender != s.Conn().RemotePeer() {
		fmt.Printf("❌ Sender mismatch: declared %s, connection %s. Dropping vote.\n", message.Sender, s.Conn().RemotePeer())
		return
	}

	// Validate vote payload fields
	voteValueRaw, hasVote := voteData["vote"]
	blockHashRaw, hasBlockHash := voteData["block_hash"]
	if !hasVote || !hasBlockHash {
		fmt.Printf("❌ Missing vote or block_hash in payload; dropping vote\n")
		return
	}
	voteValue, ok := voteValueRaw.(float64)
	if !ok || (voteValue != 1 && voteValue != -1) {
		fmt.Printf("❌ Invalid vote value: %v; dropping vote\n", voteValueRaw)
		return
	}
	blockHash, ok := blockHashRaw.(string)
	if !ok || blockHash == "" {
		fmt.Printf("❌ Invalid block_hash: %v; dropping vote\n", blockHashRaw)
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

	// NOTE: Vote aggregation and BFT triggering is now handled separately
	// when BFT request is received from Sequencer
}

// RequestForVoteResult is now handled by handleVoteResultRequest
// This function was kept for backward compatibility but is deprecated

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

	// CRITICAL FIX: Reuse existing GossipNode/PubSub if available
	// Creating a new GossipPubSub for every request creates a new subscription to libp2p
	// without cancelling the old one, leading to a resource leak (thousands of goroutines).
	var gps *AVCStruct.GossipPubSub
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()

	if pubSubNode != nil && pubSubNode.PubSub != nil {
		fmt.Println("♻️ Reusing existing GossipPubSub instance")
		gps = pubSubNode.PubSub
	} else {
		fmt.Println("🆕 Creating NEW GossipPubSub instance (First time initialization)")
		// Create GossipPubSub using Pubsub_Builder.go
		gps = AVCStruct.NewGossipPubSubBuilder(nil).
			SetHost(listenerNode.Host).
			SetProtocol(config.BuddyNodesMessageProtocol).
			Build()

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
		SetTimestamp(time.Now().UTC().Unix()).
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
	peerID := s.Conn().RemotePeer()
	voteResult := int8(resultValue)

	// Import Triggers package to use StoreVoteResult
	// Note: We need to add the import at the top of the file
	Maps.StoreVoteResult(peerID.String(), voteResult)
	fmt.Printf("✅ Stored vote result for peer %s: %d\n", peerID.String(), voteResult)

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
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackMessage)

	responseBytes, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("❌ Failed to marshal response: %v\n", err)
		return
	}

	s.Write([]byte(string(responseBytes) + string(rune(config.Delimiter))))
	fmt.Printf("✅ Acknowledgment sent to peer %s\n", peerID.String())
}

// handleVoteResultRequest handles request for vote aggregation result from a buddy node
func (lh *ListenerHandler) handleVoteResultRequest(s network.Stream, message *AVCStruct.Message) {
	fmt.Printf("\n\n\n🎯🎯🎯 RECEIVED VOTE RESULT REQUEST FROM SEQUENCER 🎯🎯🎯\n\n\n")
	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║       REQUEST FOR VOTE AGGREGATION RESULT                  ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("📨 Request from: %s\n", s.Conn().RemotePeer().String())
	fmt.Printf("📋 Message: %s\n", message.Message)

	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		fmt.Printf("❌ Listener node or CRDT layer not initialized\n")
		return
	}

	// Parse optional request payload (e.g., block hash scoping)
	var targetBlockHash string
	var voteResultReq struct {
		BlockHash string `json:"block_hash"`
	}
	// functions which retuning the response should return the same format
	if err := json.Unmarshal([]byte(message.Message), &voteResultReq); err == nil {
		targetBlockHash = voteResultReq.BlockHash
		if targetBlockHash != "" {
			fmt.Printf("🎯 Target block hash from request: %s\n", targetBlockHash)
		}
	} else {
		fmt.Printf("DEBUG: Vote result request payload not JSON or missing block_hash: %v\n", err)
		// If no valid JSON payload, reject to avoid mixing blocks
		ackMessage := AVCStruct.NewACKBuilder().False_ACK_Message(listenerNode.PeerID, config.Type_VoteResult)
		response := AVCStruct.NewMessageBuilder(nil).
			SetSender(listenerNode.PeerID).
			SetMessage(fmt.Sprintf(`{"error":"invalid vote result request: %v"}`, err)).
			SetTimestamp(time.Now().UTC().Unix()).
			SetACK(ackMessage)
		responseBytes, _ := json.Marshal(response)
		_, _ = s.Write([]byte(string(responseBytes) + string(rune(config.Delimiter))))
		fmt.Printf("❌ Invalid vote result request payload; rejecting\n")
		return
	}

	// Ensure buddy nodes are populated from the cached consensus message
	// This guards cases where the broadcast handler didn't run yet on this node
	if len(listenerNode.BuddyNodes.Buddies_Nodes) == 0 {
		fmt.Printf("⚠️ Buddy list empty at vote result request; attempting to populate from consensus cache\n")
		buddyIDs := make([]peer.ID, 0, config.MaxMainPeers)
		count := 0
		for _, consensusMsg := range AVCStruct.CacheConsensuMessage {
			if consensusMsg == nil || consensusMsg.Buddies == nil {
				continue
			}
			for i := 0; i < config.MaxMainPeers && i < len(consensusMsg.Buddies); i++ {
				if b, ok := consensusMsg.Buddies[i]; ok {
					if b.PeerID != listenerNode.PeerID {
						buddyIDs = append(buddyIDs, b.PeerID)
						count++
						if count >= config.MaxMainPeers {
							break
						}
					}
				}
			}
			if count >= config.MaxMainPeers {
				break
			}
		}
		if len(buddyIDs) > 0 {
			listenerNode.BuddyNodes.Buddies_Nodes = buddyIDs
			fmt.Printf("✅ Populated buddy nodes from cache: %d peers (MaxMainPeers=%d)\n", len(buddyIDs), config.MaxMainPeers)
		} else {
			fmt.Printf("⚠️ Could not populate buddy nodes from cache\n")
		}
	}
	fmt.Printf("✅ Buddy nodes populated: %v\n", listenerNode.BuddyNodes.Buddies_Nodes)

	// 🔄 CRDT SYNC: Sync CRDT data before processing votes
	fmt.Printf("🔄 Triggering CRDT sync before processing votes...\n")
	if err := TriggerCRDTSyncForBuddyNode(listenerNode); err != nil {
		fmt.Printf("⚠️ CRDT sync failed, continuing with existing data: %v\n", err)
		// Don't fail the vote processing, just log the warning
	} else {
		fmt.Printf("✅ CRDT sync completed successfully\n")
		// Print CRDT content after sync
		CRDTSync.PrintCurrentCRDTContent()
	}

	// Process votes from CRDT
	result, err := Structs.ProcessVotesFromCRDT(listenerNode, targetBlockHash)
	if err != nil {
		fmt.Printf("❌ Failed to process votes from CRDT: %v\n", err)
		return
	}

	blsResp, status, err := BLS_Signer.SignMessage(result)
	if err != nil || status == false {
		fmt.Printf("⚠️ Failed to create BLS signature for BFT result: %v\n", err)
	} else {
		fmt.Printf("✅ BLS signature created successfully: %v\n", blsResp)
	}
	// Attach local PeerID into BLS payload
	blsResp.SetPeerID(listenerNode.PeerID.String())

	fmt.Printf("📊 Vote aggregation result: %d\n", result)

	// Send the result back
	resultData := map[string]interface{}{
		"result": result,
		"bls":    blsResp,
	}

	resultJSON, _ := json.Marshal(resultData)

	ackMessage := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_ACK_True)
	response := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(string(resultJSON)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackMessage)

	responseBytes, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("❌ Failed to marshal response: %v\n", err)
		return
	}

	// Write response with delimiter
	responseWithDelimiter := string(responseBytes) + string(rune(config.Delimiter))
	n, err := s.Write([]byte(responseWithDelimiter))
	if err != nil {
		fmt.Printf("❌ Failed to write response: %v\n", err)
		return
	}

	// Force flush the stream
	if err := s.CloseWrite(); err != nil {
		fmt.Printf("⚠️ Failed to close write side: %v\n", err)
	}

	fmt.Printf("✅ Sent vote result %d to %s (%d bytes written)\n\n", result, s.Conn().RemotePeer().String(), n)
}

func (lh *ListenerHandler) TriggerForBFTFromSequencer(s network.Stream, message *AVCStruct.Message, buddies []peer.ID) {
	if ListenerHandlerLocal == nil {
		var err error
		ListenerHandlerLocal, err = common.InitializeGRO(GRO.HandleBFTRequestLocal)
		if err != nil {
			fmt.Printf("❌ Failed to initialize ListenerHandler local manager: %v\n", err)
			return
		}
	}
	defer s.Close()

	fmt.Println("📩 Received BFT trigger from Sequencer:", message)

	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		fmt.Println("❌ Listener node not initialized")
		return
	}

	// Get buddy list from global config or BFT context
	if len(buddies) == 0 {
		fmt.Println("⚠️ No buddies found to request vote results")
		return
	}

	fmt.Printf("🚀 Triggering BFT across %d buddy nodes\n", len(buddies))
	fmt.Printf("📍 Listener PeerID: %s\n", listenerNode.PeerID.String())
	fmt.Printf("📍 Listener Host ID: %s\n", listenerNode.Host.ID().String())
	fmt.Printf("📋 All buddies received: %v\n", buddies)

	// Filter out self from buddies to avoid "dial to self attempted" error
	filteredBuddies := make([]peer.ID, 0, len(buddies))
	listenerIDStr := listenerNode.PeerID.String()
	listenerHostIDStr := listenerNode.Host.ID().String()

	// Also check PubSubNode peer ID in case it's different
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	var currentPeerID peer.ID
	var currentPeerIDStr string
	if pubSubNode != nil {
		currentPeerID = pubSubNode.PeerID
		currentPeerIDStr = currentPeerID.String()
		fmt.Printf("📍 PubSub PeerID: %s\n", currentPeerIDStr)
		if pubSubNode.Host != nil {
			fmt.Printf("📍 PubSub Host ID: %s\n", pubSubNode.Host.ID().String())
		}
	} else {
		currentPeerID = listenerNode.PeerID
		currentPeerIDStr = currentPeerID.String()
	}

	for _, b := range buddies {
		buddyIDStr := b.String()
		// Compare against all possible IDs (listener peer, host, pubsub peer and host)
		isListener := buddyIDStr == listenerIDStr
		isListenerHost := buddyIDStr == listenerHostIDStr
		isPubSub := buddyIDStr == currentPeerIDStr

		if !isListener && !isListenerHost && !isPubSub {
			// Also check if it matches PubSub host ID
			isPubSubHost := false
			if pubSubNode != nil && pubSubNode.Host != nil {
				isPubSubHost = buddyIDStr == pubSubNode.Host.ID().String()
			}

			if !isPubSubHost {
				filteredBuddies = append(filteredBuddies, b)
				fmt.Printf("✅ Including buddy: %s\n", buddyIDStr)
			} else {
				fmt.Printf("⚠️ Filtering out self: %s (matches PubSub host)\n", buddyIDStr)
			}
		} else {
			matched := ""
			if isListener {
				matched = "listener peer"
			}
			if isListenerHost {
				if matched != "" {
					matched += ", "
				}
				matched += "listener host"
			}
			if isPubSub {
				if matched != "" {
					matched += ", "
				}
				matched += "pubsub"
			}
			fmt.Printf("⚠️ Filtering out self: %s (matches %s)\n", buddyIDStr, matched)
		}
	}

	if len(filteredBuddies) == 0 {
		fmt.Println("⚠️ No valid buddy nodes (all are self)")
		return
	}

	fmt.Printf("📊 Filtered to %d valid buddy nodes (excluding self)\n", len(filteredBuddies))

	// Send acknowledgment to sequencer
	ack := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_SubmitVote)
	response := AVCStruct.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage("BFT started across buddies").
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ack)
	data, _ := json.Marshal(response)
	data = append(data, byte(config.Delimiter))
	s.Write(data)

	// ✅ Send RequestForVoteResult to all buddies in parallel
	// Track successful responses to ensure we meet the minimum requirement
	responsesReceived := 0
	var responsesMutex sync.Mutex

	responseCh := make(chan bool, len(filteredBuddies))
	wg, err := ListenerHandlerLocal.NewFunctionWaitGroup(context.Background(), GRO.BFTWaitGroup)
	if err != nil {
		fmt.Printf("❌ Failed to create waitgroup: %v\n", err)
		return
	}

	for _, b := range filteredBuddies {
		buddyID := b
		if err := ListenerHandlerLocal.Go(GRO.BFTSendRequestThread, func(ctx context.Context) error {
			// Use SubmitMessageProtocol because HandleSubmitMessageStream routes Type_VoteResult
			stream, err := listenerNode.Host.NewStream(ctx, buddyID, config.SubmitMessageProtocol)
			if err != nil {
				fmt.Printf("❌ Failed to open stream to %s: %v\n", buddyID, err)
				responseCh <- false
				return nil
			}
			defer func() {
				stream.Close()
				fmt.Printf("🔌 Closed stream to %s\n", buddyID)
			}()

			reqAck := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_VoteResult)
			reqMsg := AVCStruct.NewMessageBuilder(nil).
				SetSender(listenerNode.PeerID).
				SetMessage("RequestForVoteResult").
				SetTimestamp(time.Now().UTC().Unix()).
				SetACK(reqAck)

			reqData, _ := json.Marshal(reqMsg)
			reqData = append(reqData, byte(config.Delimiter))
			if _, err := stream.Write(reqData); err != nil {
				fmt.Printf("❌ Failed to send RequestForVoteResult to %s: %v\n", buddyID, err)
				responseCh <- false
				return nil
			}
			fmt.Printf("📨 Sent RequestForVoteResult to %s\n", buddyID)

			// Wait for the vote result
			readCh := make(chan []byte, 1)
			readErrCh := make(chan error, 1)

			// Read from stream in a goroutine (can't use LocalGRO here as it's a blocking read)
			ListenerHandlerLocal.Go(GRO.BFTSendRequestThread, func(ctx context.Context) error {
				buf := make([]byte, 0)
				tmp := make([]byte, 1024)
				for {
					n, err := stream.Read(tmp)
					if err != nil {
						readErrCh <- err
						close(readCh)
						return nil
					}
					buf = append(buf, tmp[:n]...)
					if bytes.Contains(buf, []byte{byte(config.Delimiter)}) {
						readCh <- bytes.TrimSuffix(buf, []byte{byte(config.Delimiter)})
						return nil
					}
				}
			})

			timeoutTimer := time.NewTimer(5 * time.Second)
			defer timeoutTimer.Stop()

			select {
			case <-ctx.Done():
				fmt.Printf("⏳ Context cancelled while waiting for vote result from %s\n", buddyID)
				responseCh <- false
				return ctx.Err()
			case payload := <-readCh:
				if payload == nil {
					fmt.Printf("⚠️ No response from %s (nil payload)\n", buddyID)
					responseCh <- false
					return nil
				}

				fmt.Printf("📥 Received payload from %s: %d bytes\n", buddyID, len(payload))
				fmt.Printf("📝 Payload content: %s\n", string(payload))

				var msg AVCStruct.Message
				if err := json.Unmarshal(payload, &msg); err == nil {
					fmt.Printf("✅ Parsed vote result message from %s\n", buddyID)
					fmt.Printf("   Message content: %s\n", msg.Message)

					// Parse and store the vote result directly
					var resultData map[string]interface{}
					if err := json.Unmarshal([]byte(msg.Message), &resultData); err == nil {
						if result, ok := resultData["result"].(float64); ok {
							voteResult := int8(result)
							Maps.StoreVoteResult(buddyID.String(), voteResult)
							fmt.Printf("✅ Stored vote result for peer %s: %d\n", buddyID.String(), voteResult)
							responsesMutex.Lock()
							responsesReceived++
							count := responsesReceived
							responsesMutex.Unlock()

							// Check if we've reached the minimum requirement
							if count >= config.MaxMainPeers {
								fmt.Printf("✅ Reached minimum requirement: %d/%d responses\n", count, config.MaxMainPeers)
							}
							responseCh <- true
							return nil
						}
					}
					fmt.Printf("⚠️ Failed to parse vote result from %s\n", buddyID)
					responseCh <- false
				} else {
					fmt.Printf("⚠️ Invalid response from %s: %s\n", buddyID, string(payload))
					responseCh <- false
				}
			case <-readErrCh:
				fmt.Printf("⚠️ Error reading from stream for %s\n", buddyID)
				responseCh <- false
			case <-timeoutTimer.C:
				fmt.Printf("⏳ Timeout waiting for vote result from %s\n", buddyID)
				responseCh <- false
			}
			return nil
		}, local.AddToWaitGroup(GRO.BFTWaitGroup)); err != nil {
			fmt.Printf("❌ Failed to start goroutine for buddy %s: %v\n", buddyID, err)
		}
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(responseCh)

	// Wait for responses and check if we have enough
	for success := range responseCh {
		if !success {
			continue
		}
	}

	responsesMutex.Lock()
	finalCount := responsesReceived
	responsesMutex.Unlock()

	fmt.Printf("✅ Collected vote results from %d/%d nodes\n", finalCount, len(filteredBuddies))

	// Check if we have enough responses for consensus
	if finalCount < config.MaxMainPeers {
		fmt.Printf("⚠️ WARNING: Only received %d responses, but need at least %d for consensus\n", finalCount, config.MaxMainPeers)
		fmt.Printf("⚠️ This may cause consensus failures. Consider increasing backup nodes.\n")
	}
}
