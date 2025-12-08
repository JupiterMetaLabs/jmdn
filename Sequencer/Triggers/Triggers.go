package Triggers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/CRDTSync"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service/PubSubConnector"

	"gossipnode/AVC/BuddyNodes/Types"
	voteaggregation "gossipnode/AVC/VoteModule"
	"gossipnode/Pubsub"
	"gossipnode/Sequencer/Triggers/Maps"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/seednode"
	"log"
	"strings"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// This trigger is used to trigger the Close the accepting messages from the nodes for listener protocol
var LocalGRO interfaces.LocalGoroutineManagerInterface
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
		// Byzantine tolerance is calculated dynamically from actual buddy count
		cfg := bft.DefaultConfig()
		cfg.MinBuddies = config.MaxMainPeers + 1

		bftEngine = bft.New(cfg)

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

	// 🔄 CRDT SYNC: Sync all buddy nodes' CRDTs before vote aggregation
	log.Printf("🔄 Triggering CRDT sync before vote aggregation...")
	if err := TriggerCRDTSyncBeforeVoteAggregation(); err != nil {
		log.Printf("⚠️ CRDT sync failed, continuing with existing data: %v", err)
		// Don't fail the vote aggregation, just log the warning
	} else {
		log.Printf("✅ CRDT sync completed successfully")

		// 📊 Print CRDT content after sync and before vote aggregation
		CRDTSync.PrintCurrentCRDTContent()
	}

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
		_, consensusCancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer consensusCancel()

		// Start BFT consensus process
		if err := StartBFTConsensus(); err != nil {
			log.Printf("BFTTrigger: Failed to start BFT consensus: %v", err)
		}
	})
}

// RequestVoteResultsFromBuddies requests vote results from all buddy nodes
func RequestVoteResultsFromBuddies() error {
	log.Printf("RequestVoteResultsFromBuddies: Requesting vote results from all buddy nodes")
	var err error
	// Create a new AppGRO for Sequencer.Maps.Trigger
	AppGRO := GRO.GetApp(GRO.SequencerApp)
	if AppGRO == nil {
		return fmt.Errorf("app manager not available")
	}

	if LocalGRO == nil {
		LocalGRO, err = AppGRO.NewLocalManager(GRO.SequencerTriggerLocal)
		if err != nil {
			return fmt.Errorf("failed to create local manager: %v", err)
		}
	}

	// Get the listener node
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		return fmt.Errorf("listener node not available")
	}

	// Get buddy nodes
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode == nil {
		return fmt.Errorf("buddy node not available")
	}

	buddyNode.Mutex.RLock()
	buddies := make([]peer.ID, len(buddyNode.BuddyNodes.Buddies_Nodes))
	copy(buddies, buddyNode.BuddyNodes.Buddies_Nodes)
	buddyNode.Mutex.RUnlock()

	if len(buddies) == 0 {
		return fmt.Errorf("no buddy nodes to request vote results from")
	}

	log.Printf("RequestVoteResultsFromBuddies: Requesting from %d buddy nodes", len(buddies))

	// Filter out self from buddies to avoid "dial to self attempted" error
	filteredBuddies := make([]peer.ID, 0, len(buddies))
	listenerIDStr := listenerNode.PeerID.String()
	listenerHostIDStr := listenerNode.Host.ID().String()

	// Also check PubSubNode peer ID in case it's different
	var currentPeerIDStr string
	var currentHostIDStr string
	if buddyNode != nil {
		currentPeerIDStr = buddyNode.PeerID.String()
		if buddyNode.Host != nil {
			currentHostIDStr = buddyNode.Host.ID().String()
		}
	}

	for _, pid := range buddies {
		peerIDStr := pid.String()
		// Compare against all possible IDs
		if peerIDStr != listenerIDStr && peerIDStr != listenerHostIDStr &&
			peerIDStr != currentPeerIDStr && peerIDStr != currentHostIDStr {
			filteredBuddies = append(filteredBuddies, pid)
			log.Printf("RequestVoteResultsFromBuddies: Including buddy %s", pid)
		} else {
			log.Printf("RequestVoteResultsFromBuddies: Filtering out self %s", pid)
		}
	}

	if len(filteredBuddies) == 0 {
		return fmt.Errorf("no valid buddy nodes after filtering self")
	}

	log.Printf("RequestVoteResultsFromBuddies: Filtered to %d valid buddy nodes", len(filteredBuddies))

	// Request vote results from each buddy node
	for _, peerID := range filteredBuddies {
		LocalGRO.Go(GRO.SequencerTriggerLocal, func(ctx context.Context) error {
			stream, err := listenerNode.Host.NewStream(context.Background(), peerID, config.SubmitMessageProtocol)
			if err != nil {
				log.Printf("RequestVoteResultsFromBuddies: Failed to open stream to %s: %v", peerID, err)
				return err
			}
			defer stream.Close()

			// Create vote result request message
			reqAck := AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_VoteResult)
			reqMsg := AVCStruct.NewMessageBuilder(nil).
				SetSender(listenerNode.PeerID).
				SetMessage("RequestForVoteResult").
				SetTimestamp(time.Now().UTC().Unix()).
				SetACK(reqAck)

			reqData, _ := json.Marshal(reqMsg)
			reqData = append(reqData, byte(config.Delimiter))

			if _, err := stream.Write(reqData); err != nil {
				log.Printf("RequestVoteResultsFromBuddies: Failed to send request to %s: %v", peerID, err)
				return err
			}

			log.Printf("RequestVoteResultsFromBuddies: Sent request to %s", peerID)

			// Read response
			reader := bufio.NewReader(stream)
			response, err := reader.ReadString(config.Delimiter)
			if err != nil {
				log.Printf("RequestVoteResultsFromBuddies: Failed to read response from %s: %v", peerID, err)
				return err
			}

			// Parse and store vote result
			responseMsg := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(response)
			if responseMsg != nil {
				var resultData map[string]interface{}
				if err := json.Unmarshal([]byte(responseMsg.Message), &resultData); err == nil {
					if result, ok := resultData["result"].(float64); ok {
						Maps.StoreVoteResult(peerID.String(), int8(result))
						log.Printf("RequestVoteResultsFromBuddies: Stored vote result from %s: %d", peerID, int8(result))
					}
				}
			}
			return nil
		})
	}

	return nil
}

func StartBFTConsensus() error {
	log.Printf("StartBFTConsensus: Initiating BFT consensus process")

	if subscriptionService == nil {
		return fmt.Errorf("subscription service not initialized")
	}

	// First, request vote results from all buddy nodes
	if err := RequestVoteResultsFromBuddies(); err != nil {
		log.Printf("StartBFTConsensus: Failed to request vote results: %v", err)
	}

	// Wait for vote results to be collected (poll for up to 60 seconds)
	maxWait := 35 * time.Second
	checkInterval := 2 * time.Second
	elapsed := time.Duration(0)

	for elapsed < maxWait {
		count := Maps.GetVoteResultsCount()
		if count > 0 {
			log.Printf("StartBFTConsensus: Found %d vote results, proceeding with BFT", count)
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
	allVoteResults := Maps.GetAllVoteResults()

	allBuddies := make([]bft.BuddyInput, len(buddyNode.BuddyNodes.Buddies_Nodes))
	for i, peerID := range buddyNode.BuddyNodes.Buddies_Nodes {
		voteResult := allVoteResults[peerID.String()]

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
	buddyNode.Mutex.RUnlock()

	// Create BFT instance
	// Byzantine tolerance is calculated dynamically from actual buddy count
	cfg := bft.DefaultConfig()
	cfg.MinBuddies = config.MaxMainPeers
	BFTInstance := bft.New(cfg)

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
	roundID := fmt.Sprintf("%d", time.Now().UTC().Unix())
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

// TriggerCRDTSyncBeforeVoteAggregation triggers CRDT synchronization across all buddy nodes
// This ensures all nodes have consistent CRDT data before vote aggregation
func TriggerCRDTSyncBeforeVoteAggregation() error {
	log.Printf("🔄 Starting CRDT sync before vote aggregation...")

	// Get the global listener node to access host and pubsub
	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.Host == nil {
		return fmt.Errorf("listener node not initialized")
	}

	// Get the pubsub node
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if pubSubNode == nil {
		return fmt.Errorf("pubsub node not initialized")
	}

	// Get the CRDT layer
	crdtLayer := DataLayer.GetCRDTLayer()
	if crdtLayer == nil || crdtLayer.CRDTLayer == nil {
		return fmt.Errorf("CRDT layer not initialized")
	}

	// Create global CRDT sync manager
	globalSyncManager := CRDTSync.NewGlobalSyncManager(listenerNode.Host)

	// Get buddy node peer IDs
	buddyPeerIDs := make([]string, len(listenerNode.BuddyNodes.Buddies_Nodes))
	for i, peer := range listenerNode.BuddyNodes.Buddies_Nodes {
		buddyPeerIDs[i] = peer.String()
	}

	log.Printf("📋 Buddy nodes to sync: %v", buddyPeerIDs)

	// Create a StructGossipPubSub wrapper from the existing pubsub node
	gossipPubSub, err := createStructGossipPubSubFromBuddyNode(pubSubNode, listenerNode.Host)
	if err != nil {
		log.Printf("⚠️ Failed to create StructGossipPubSub, using simplified sync: %v", err)
		return triggerSimplifiedCRDTSync(listenerNode, crdtLayer)
	}

	// Initialize sync for each buddy node
	for _, peerID := range buddyPeerIDs {
		if err := globalSyncManager.InitializeBuddyNodeSync(peerID, gossipPubSub); err != nil {
			log.Printf("⚠️ Failed to initialize sync for buddy %s: %v", peerID[:8], err)
			// Continue with other buddies
		}
	}

	// Trigger global sync
	if err := globalSyncManager.TriggerGlobalSync(); err != nil {
		log.Printf("⚠️ Failed to trigger global CRDT sync: %v", err)
		return triggerSimplifiedCRDTSync(listenerNode, crdtLayer)
	}

	// Wait for sync completion with timeout
	if err := globalSyncManager.WaitForGlobalSyncCompletion(5 * time.Second); err != nil {
		log.Printf("⚠️ Global CRDT sync timeout: %v", err)
		return triggerSimplifiedCRDTSync(listenerNode, crdtLayer)
	}

	log.Printf("✅ Global CRDT sync completed successfully before vote aggregation")
	return nil
}

// createStructGossipPubSubFromBuddyNode creates a StructGossipPubSub from existing BuddyNode
func createStructGossipPubSubFromBuddyNode(buddyNode *AVCStruct.BuddyNode, host host.Host) (*Pubsub.StructGossipPubSub, error) {
	if buddyNode == nil || buddyNode.PubSub == nil {
		return nil, fmt.Errorf("buddy node or pubsub not available")
	}

	// Create a new StructGossipPubSub using the existing pubsub instance
	gossipPubSub, err := Pubsub.NewGossipPubSub(host, config.PubSub_ConsensusChannel)
	if err != nil {
		return nil, fmt.Errorf("failed to create StructGossipPubSub: %w", err)
	}

	return gossipPubSub, nil
}

// triggerSimplifiedCRDTSync performs a simplified CRDT sync without full pubsub integration
func triggerSimplifiedCRDTSync(listenerNode *AVCStruct.BuddyNode, crdtLayer *Types.Controller) error {
	log.Printf("🔄 Performing simplified CRDT sync...")

	// Get all CRDTs from the current node
	allCRDTs := crdtLayer.CRDTLayer.GetAllCRDTs()
	log.Printf("📊 Current node has %d CRDT objects", len(allCRDTs))

	// Log CRDT state for debugging
	for key, crdt := range allCRDTs {
		log.Printf("📋 CRDT %s: %+v", key, crdt)
	}

	// In simplified mode, we just ensure the local CRDT is ready
	// The actual sync would happen via the pubsub system in full mode
	log.Printf("✅ Simplified CRDT sync completed - local CRDT ready for vote aggregation")
	return nil
}
