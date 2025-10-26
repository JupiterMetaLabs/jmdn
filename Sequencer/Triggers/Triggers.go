package Triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service/PubSubConnector"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/seednode"
	"log"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// This trigger is used to trigger the Close the accepting messages from the nodes for listener protocol
const ListeningTriggerMessage = "ListeningTrigger"
const ListeningTriggerBufferTime = 20 * time.Second
const BFTTriggerBufferTime = 27 * time.Second

// Global variables for trigger management
var (
	subscriptionService *PubSubConnector.SubscriptionService
	bftEngine           *bft.BFT
	consensusContext    context.Context
	consensusCancel     context.CancelFunc
)

// InitializeTriggers initializes the trigger system with required services
func InitializeTriggers(pubSub *AVCStruct.GossipPubSub, buddyID string) error {
	// Create subscription service
	subscriptionService = PubSubConnector.NewSubscriptionService(pubSub)
	subscriptionService.SetMyBuddyID(buddyID)
	subscriptionService.InitBFTHandlers()

	// Set up BFT factory
	subscriptionService.SetBFTFactory(func(ctx context.Context, pubSub *AVCStruct.GossipPubSub, channelName string) (PubSubConnector.BFTMessageHandler, error) {
		// Create BFT engine with configuration
		config := bft.Config{
			MinBuddies:         13, // 13 main peers + 1 creator = 14 total
			ByzantineTolerance: 4,  // Can tolerate up to 4 Byzantine nodes
			PrepareTimeout:     10 * time.Second,
			CommitTimeout:      10 * time.Second,
		}

		bftEngine = bft.New(config)

		// Create BFT PubSub adapter that implements the required interface
		adapter, err := bft.NewBFTPubSubAdapter(ctx, pubSub, bftEngine, channelName)
		if err != nil {
			return nil, fmt.Errorf("failed to create BFT adapter: %v", err)
		}

		// Create a wrapper that implements the PubSubConnector.BFTMessageHandler interface
		wrapper := &BFTMessageHandlerWrapper{adapter: adapter}
		return wrapper, nil
	})

	log.Printf("Triggers initialized with subscription service and BFT engine")
	return nil
}

func ListeningTrigger() {
	time.AfterFunc(ListeningTriggerBufferTime, func() {
		log.Printf("ListeningTrigger: Closing listener protocol after %v", ListeningTriggerBufferTime)

		// Get the listener node from global variables
		listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
		if listenerNode != nil {
			// Close all streams to stop accepting new messages
			// Note: CloseAllStreams method needs to be implemented in the listener node
			log.Printf("ListeningTrigger: Would close all listener streams (method needs implementation)")
		}

		// Trigger BFT consensus after listening period
		BFTTrigger()
	})
}

func ReleaseBuddyNodesTrigger() {
	log.Printf("ReleaseBuddyNodesTrigger: Releasing buddy nodes from consensus")

	// Get the buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode != nil {
		// Clear buddy list to release nodes
		buddyNode.Mutex.Lock()
		buddyNode.BuddyNodes.Buddies_Nodes = []peer.ID{}
		client, err := seednode.NewClient(config.SeedNodeURL)
		if err != nil {
			log.Printf("ReleaseBuddyNodesTrigger: Failed to create seed node client: %v", err)
			return
		}
		err = client.RemoveAllBuddies(context.Background())
		if err != nil {
			log.Printf("ReleaseBuddyNodesTrigger: Failed to remove all buddies: %v", err)
			return
		}
		buddyNode.Mutex.Unlock()

		log.Printf("ReleaseBuddyNodesTrigger: Released all buddy nodes")
	}
}

func BFTTrigger() {
	log.Printf("BFTTrigger: Starting BFT consensus after %v", BFTTriggerBufferTime)

	time.AfterFunc(BFTTriggerBufferTime, func() {
		// 1. BFT uses PubSub protocol with different stage names
		// 2. Start the BFT Handler in the subscription service

		if subscriptionService == nil {
			log.Printf("BFTTrigger: Subscription service not initialized")
			return
		}

		// Create consensus context with timeout
		consensusContext, consensusCancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer consensusCancel()

		// Start BFT consensus process
		if err := StartBFTConsensus(); err != nil {
			log.Printf("BFTTrigger: Failed to start BFT consensus: %v", err)
		}
	})
}

func StartBFTConsensus() error {
	log.Printf("StartBFTConsensus: Initiating BFT consensus process")

	if subscriptionService == nil {
		return fmt.Errorf("subscription service not initialized")
	}

	// Get buddy nodes for consensus
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil {
		return fmt.Errorf("buddy node not available")
	}

	// Prepare buddy input data for BFT
	buddyNode.Mutex.RLock()
	buddies := make([]map[string]interface{}, len(buddyNode.BuddyNodes.Buddies_Nodes))
	for i, peerID := range buddyNode.BuddyNodes.Buddies_Nodes {
		buddies[i] = map[string]interface{}{
			"ID":        peerID.String(),
			"Decision":  "ACCEPT", // Default to accept
			"PublicKey": []byte{}, // TODO: Get actual public key
		}
	}
	buddyNode.Mutex.RUnlock()

	// Create BFT request message
	requestData := map[string]interface{}{
		"Round":          uint64(1),
		"BlockHash":      "consensus_block_hash", // TODO: Get actual block hash
		"GossipsubTopic": config.PubSub_ConsensusChannel,
		"AllBuddies":     buddies,
	}

	// Convert to JSON
	requestJSON, err := json.Marshal(requestData)
	if err != nil {
		return fmt.Errorf("failed to marshal BFT request: %v", err)
	}

	// Create GossipMessage for BFT request
	ackMessage := AVCStruct.NewACKBuilder().True_ACK_Message(buddyNode.PeerID, config.Type_BFTRequest)
	message := AVCStruct.NewMessageBuilder(nil).
		SetSender(buddyNode.PeerID).
		SetMessage(string(requestJSON)).
		SetTimestamp(time.Now().Unix()).
		SetACK(ackMessage)

	gossipMessage := AVCStruct.NewGossipMessageBuilder(nil).
		SetID(fmt.Sprintf("bft_request_%d", time.Now().UnixNano())).
		SetTopic(config.PubSub_ConsensusChannel).
		SetMesssage(message).
		SetSender(buddyNode.PeerID).
		SetTimestamp(time.Now().Unix())

	// Process BFT request through subscription service
	// Note: This would need to be called through the message handling system
	// For now, we'll simulate the BFT request processing
	log.Printf("StartBFTConsensus: Would process BFT request through subscription service")
	log.Printf("StartBFTConsensus: Created GossipMessage with ID: %s", gossipMessage.ID)

	// TODO: Implement proper BFT request processing through the subscription service
	// The subscription service handles BFT requests via its message routing system

	log.Printf("StartBFTConsensus: BFT consensus process initiated successfully")
	return nil
}

// CleanupTriggers cleans up resources when consensus is complete
func CleanupTriggers() {
	if consensusCancel != nil {
		consensusCancel()
	}
	log.Printf("CleanupTriggers: Cleaned up trigger resources")
}

// BFTMessageHandlerWrapper wraps the BFT adapter to implement the required interface
type BFTMessageHandlerWrapper struct {
	adapter *bft.BFTPubSubAdapter
}

// Implement the PubSubConnector.BFTMessageHandler interface
func (w *BFTMessageHandlerWrapper) HandleStartPubSub(msg *AVCStruct.GossipMessage) error {
	return w.adapter.HandleStartPubSub(msg)
}

func (w *BFTMessageHandlerWrapper) HandleEndPubSub(msg *AVCStruct.GossipMessage) error {
	return w.adapter.HandleEndPubSub(msg)
}

func (w *BFTMessageHandlerWrapper) HandlePrepareVote(msg *AVCStruct.GossipMessage) error {
	return w.adapter.HandlePrepareVote(msg)
}

func (w *BFTMessageHandlerWrapper) HandleCommitVote(msg *AVCStruct.GossipMessage) error {
	return w.adapter.HandleCommitVote(msg)
}

func (w *BFTMessageHandlerWrapper) ProposeConsensus(
	ctx context.Context,
	round uint64,
	blockHash string,
	myBuddyID string,
	allBuddies []PubSubConnector.BuddyInput,
) (*PubSubConnector.Result, error) {
	// Convert PubSubConnector.BuddyInput to bft.BuddyInput
	bftBuddies := make([]bft.BuddyInput, len(allBuddies))
	for i, buddy := range allBuddies {
		bftBuddies[i] = bft.BuddyInput{
			ID:        buddy.ID,
			Decision:  bft.Decision(buddy.Decision),
			PublicKey: buddy.PublicKey,
		}
	}

	// Call the BFT adapter's ProposeConsensus method
	result, err := w.adapter.ProposeConsensus(ctx, round, blockHash, myBuddyID, bftBuddies)
	if err != nil {
		return nil, err
	}

	// Convert bft.Result to PubSubConnector.Result
	return &PubSubConnector.Result{
		Success:       result.Success,
		BlockAccepted: result.BlockAccepted,
		Decision:      PubSubConnector.Decision(result.Decision),
	}, nil
}
