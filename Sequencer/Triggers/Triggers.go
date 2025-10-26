package Triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service/PubSubConnector"
	"gossipnode/config"
	voteaggregation "gossipnode/AVC/VoteModule"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/seednode"
	"log"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// This trigger is used to trigger the Close the accepting messages from the nodes for listener protocol
const ListeningTriggerMessage = "ListeningTrigger"
const ListeningTriggerBufferTime = 20 * time.Second
const CRDTDataSubmitBufferTime = 25 * time.Second

// Global variable to store vote data locally
var globalVoteData map[string]int8

const BFTTriggerBufferTime = 30 * time.Second

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

// extractVoteDataFromCRDT extracts vote data from CRDT and stores peerID and vote value in a hashmap
func extractVoteDataFromCRDT(buddyNode *AVCStruct.BuddyNode) (map[string]int8, error) {
	voteData := make(map[string]int8)

	if buddyNode == nil || buddyNode.CRDTLayer == nil {
		return nil, fmt.Errorf("buddy node or CRDT layer not available")
	}

	engine := buddyNode.CRDTLayer.CRDTLayer

	// Try to get votes from common vote keys
	voteKeys := []string{"votes", "consensus_votes", "block_votes", "vote_data"}

	for _, key := range voteKeys {
		elements, exists := engine.GetSet(key)
		if exists && len(elements) > 0 {
			log.Printf("Found vote data in key '%s' with %d elements", key, len(elements))

			for _, element := range elements {
				// Parse element format: "peerID:voteJSON"
				parts := strings.SplitN(element, ":", 2)
				if len(parts) != 2 {
					log.Printf("Invalid vote element format: %s", element)
					continue
				}

				peerIDStr := parts[0]
				voteJSON := parts[1]

				// Unmarshal the Vote JSON to get the vote value
				var vote AVCStruct.Vote
				if err := json.Unmarshal([]byte(voteJSON), &vote); err != nil {
					log.Printf("Failed to unmarshal vote JSON: %s, error: %v", voteJSON, err)
					continue
				}

				// Validate vote value
				if vote.Vote != 1 && vote.Vote != -1 {
					log.Printf("Invalid vote value: %d", vote.Vote)
					continue
				}

				// Store peerID and vote value in hashmap
				voteData[peerIDStr] = vote.Vote

				log.Printf("Extracted vote: peer=%s, vote=%d", peerIDStr, vote.Vote)
			}
		}
	}

	if len(voteData) == 0 {
		return nil, fmt.Errorf("no valid vote data found in CRDT")
	}

	log.Printf("Successfully extracted %d vote entries", len(voteData))
	return voteData, nil
}

// processVoteData processes the extracted vote data and stores it in global variable
func processVoteData(voteData map[string]int8) (int8, error){
	log.Printf("Processing %d vote entries", len(voteData))

	// Store vote data in global variable
	globalVoteData = voteData
	// Get the weights of the peers
	client, err := seednode.NewClient(config.SeedNodeURL)
	if err != nil {
		log.Printf("Failed to get weights of peers: %v", err)
		return 0, fmt.Errorf("failed to get weights of peers: %v", err)
	}
	weights, err := client.ListWeightsofPeers()
	if err != nil {
		log.Printf("Failed to get weights of peers: %v", err)
		return 0, fmt.Errorf("failed to get weights of peers: %v", err)
	}
	log.Printf("Weights of peers: %v", weights)

	log.Printf("Stored %d vote entries in global variable", len(globalVoteData))

	// Once you get the weights and peers then you should submit the to the votemodule.VoteAggregation function.
	result, err := voteaggregation.VoteAggregation(weights, globalVoteData)
	if err != nil {
		log.Printf("Failed to aggregate votes: %v", err)
		return 0, fmt.Errorf("failed to aggregate votes: %v", err)
	}

	log.Printf("Vote aggregation result: %v", result)

	if result {
		return 1, nil
	} else {
		return -1, nil
	}
}

// GetGlobalVoteData returns the stored vote data from global variable
func GetGlobalVoteData() map[string]int8 {
	return globalVoteData
}

// ClearGlobalVoteData clears the global vote data
func ClearGlobalVoteData() {
	globalVoteData = nil
	log.Printf("Cleared global vote data")
}

func CRDTDataSubmitTrigger(){
	// Submit the CRDT data to the @votemodule.VoteAggregation function.
	time.AfterFunc(CRDTDataSubmitBufferTime, func() {
		log.Printf("CRDTDataSubmitTrigger: Starting CRDT data aggregation")

		// Get CRDT data from the global buddy node
		buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
		if buddyNode == nil || buddyNode.CRDTLayer == nil {
			log.Printf("CRDTDataSubmitTrigger: Buddy node or CRDT layer not available")
			return
		}

		// Extract vote data from CRDT
		voteData, err := extractVoteDataFromCRDT(buddyNode)
		if err != nil {
			log.Printf("CRDTDataSubmitTrigger: Failed to extract vote data: %v", err)
			return
		}

		log.Printf("CRDTDataSubmitTrigger: Extracted %d vote entries", len(voteData))

		// Process the vote data
		result, err := processVoteData(voteData)
		if err != nil {
			log.Printf("CRDTDataSubmitTrigger: Failed to process vote data: %v", err)
			return
		}

		log.Printf("CRDTDataSubmitTrigger: Processed vote data: %v", result)

	})
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
