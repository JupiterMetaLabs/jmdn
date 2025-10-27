package Triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service/PubSubConnector"
	voteaggregation "gossipnode/AVC/VoteModule"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/seednode"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// This trigger is used to trigger the Close the accepting messages from the nodes for listener protocol
const ListeningTriggerMessage = "ListeningTrigger"
const ListeningTriggerBufferTime = 20 * time.Second
const CRDTDataSubmitBufferTime = 25 * time.Second

// Global variable to store vote data locally
var globalVoteData map[string]int8

// Global map to store vote results from buddy nodes: map[peerID]voteResult
var voteResultsMap = make(map[string]int8)

const BFTTriggerBufferTime = 30 * time.Second

// Global variables for trigger management
var (
	subscriptionService *PubSubConnector.SubscriptionService
	bftEngine           *bft.BFT
	consensusContext    context.Context
	consensusCancel     context.CancelFunc
	voteResultsMutex    sync.Mutex // Mutex to protect voteResultsMap
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
			MinBuddies:         config.MaxMainPeers + 1, // 13 main peers + 1 creator = 14 total
			ByzantineTolerance: 4,                       // Can tolerate up to 4 Byzantine nodes
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
func ProcessVoteData(voteData map[string]int8) (int8, error) {
	log.Printf("Processing %d vote entries", len(voteData))
	log.Printf("Vote data: %v", voteData)

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

func CRDTDataSubmitTrigger() {
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
		result, err := ProcessVoteData(voteData)
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

	// Wait for vote results to be collected (poll for up to 60 seconds)
	maxWait := 35 * time.Second
	checkInterval := 2 * time.Second
	elapsed := time.Duration(0)

	for elapsed < maxWait {
		voteResultsMutex.Lock()
		hasResults := len(voteResultsMap) > 0
		voteResultsMutex.Unlock()

		if hasResults {
			log.Printf("StartBFTConsensus: Found %d vote results, proceeding with BFT", len(voteResultsMap))
			break
		}

		log.Printf("StartBFTConsensus: Waiting for vote results... (elapsed: %v)", elapsed)
		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	// Get buddy nodes for consensus
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil {
		return fmt.Errorf("buddy node not available")
	}

	// Prepare buddy input data for BFT using vote results
	buddyNode.Mutex.RLock()
	voteResultsMutex.Lock()
	allBuddies := make([]bft.BuddyInput, len(buddyNode.BuddyNodes.Buddies_Nodes))
	for i, peerID := range buddyNode.BuddyNodes.Buddies_Nodes {
		voteResult := voteResultsMap[peerID.String()]

		// Convert vote result to decision: >0 = Accept, <=0 = Reject
		var decision bft.Decision = bft.Reject
		if voteResult > 0 {
			decision = bft.Accept
		}

		allBuddies[i] = bft.BuddyInput{
			ID:        peerID.String(),
			Decision:  decision,
			PublicKey: []byte{}, // TODO: Get actual public key
		}
	}
	voteResultsMutex.Unlock()
	buddyNode.Mutex.RUnlock()

	// Create BFT instance
	BFTInstance := bft.New(bft.Config{
		MinBuddies:         config.MaxMainPeers,
		ByzantineTolerance: 4,
		PrepareTimeout:     10 * time.Second,
		CommitTimeout:      10 * time.Second,
	})

	// Create BFT adapter
	adapter, err := bft.NewBFTPubSubAdapter(
		context.Background(),
		buddyNode.PubSub,
		BFTInstance,
		config.PubSub_ConsensusChannel,
	)
	if err != nil {
		return fmt.Errorf("failed to create BFT adapter: %v", err)
	}

	// Create messenger
	roundID := fmt.Sprintf("%d", time.Now().Unix())
	messenger := bft.Return_pubsubMessenger(adapter, roundID)

	// Run BFT consensus
	round := uint64(1)
	blockHash := "consensus_block_hash" // TODO: Get actual block hash
	myBuddyID := buddyNode.PeerID.String()

	log.Printf("StartBFTConsensus: Running BFT consensus with %d buddies", len(allBuddies))
	result, err := BFTInstance.RunConsensus(
		context.Background(),
		round,
		blockHash,
		myBuddyID,
		allBuddies,
		messenger,
		nil, // signer
	)

	if err != nil {
		return fmt.Errorf("BFT consensus failed: %v", err)
	}

	log.Printf("StartBFTConsensus: BFT consensus completed - Success: %v, Decision: %s",
		result.Success, result.Decision)

	return nil
}

// StoreVoteResult stores a vote result from a buddy node
func StoreVoteResult(peerID string, result int8) {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	voteResultsMap[peerID] = result
	log.Printf("Stored vote result for peer %s: %d", peerID, result)
}

// GetVoteResult retrieves a vote result for a peer
func GetVoteResult(peerID string) (int8, bool) {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	result, exists := voteResultsMap[peerID]
	return result, exists
}

// GetAllVoteResults retrieves all vote results
func GetAllVoteResults() map[string]int8 {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	result := make(map[string]int8)
	for k, v := range voteResultsMap {
		result[k] = v
	}
	return result
}

// ClearVoteResults clears all vote results
func ClearVoteResults() {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	voteResultsMap = make(map[string]int8)
	log.Printf("Cleared all vote results")
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
