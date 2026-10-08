package Triggers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"

	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service/PubSubConnector"

	"log"
	"time"

	"gossipnode/Sequencer/Triggers/Maps"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/seednode"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/libp2p/go-libp2p/core/peer"
)

// This trigger is used to trigger the Close the accepting messages from the nodes for listener protocol
var LocalGRO interfaces.LocalGoroutineManagerInterface

const ListeningTriggerMessage = "ListeningTrigger"
const ListeningTriggerBufferTime = 20 * time.Second
const CRDTDataSubmitBufferTime = 25 * time.Second


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


func ListeningTrigger(blockhash string) {
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
		BFTTrigger(blockhash)
	})
}

func ReleaseBuddyNodesTrigger() {
	log.Printf("ReleaseBuddyNodesTrigger: Releasing buddy nodes from consensus")

	// Get the buddy node from global variables
	buddyNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if buddyNode != nil {
		// Clear buddy list to release nodes.
		//
		// DEFERRED UNLOCK: the early returns below used to skip the Unlock, so a
		// seed-client failure left buddyNode.Mutex held FOREVER — every later
		// buddy-list operation on this node would block on it. Deferring makes
		// every exit path release it.
		buddyNode.Mutex.Lock()
		defer buddyNode.Mutex.Unlock()
		buddyNode.BuddyNodes.Buddies_Nodes = []peer.ID{}
		client, err := seednode.NewClient(settings.Get().Network.SeedNode)
		if err != nil {
			log.Printf("ReleaseBuddyNodesTrigger: Failed to create seed node client: %v", err)
			return
		}
		// seednode.Client owns a grpc.ClientConn; without this the connection and
		// its goroutines leak on every invocation.
		defer client.Close()
		err = client.RemoveAllBuddies(context.Background())
		if err != nil {
			log.Printf("ReleaseBuddyNodesTrigger: Failed to remove all buddies: %v", err)
			return
		}

		log.Printf("ReleaseBuddyNodesTrigger: Released all buddy nodes")
	}
}

func BFTTrigger(blockhash string) {
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
		if err := StartBFTConsensus(blockhash); err != nil {
			log.Printf("BFTTrigger: Failed to start BFT consensus: %v", err)
		}
	})
}

// RequestVoteResultsFromBuddies requests vote results from all buddy nodes
func RequestVoteResultsFromBuddies(blockhash string) error {
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

	// TODO - Level 5
	// Resolve block hash from cached consensus messages (best effort)
	// blockHash := getCachedBlockHash()

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
			// Include block hash to scope vote aggregation
			requestPayload := map[string]string{
				"block_hash": blockhash,
			}
			requestPayloadBytes, _ := json.Marshal(requestPayload)

			reqMsg := AVCStruct.NewMessageBuilder(nil).
				SetSender(listenerNode.PeerID).
				SetMessage(string(requestPayloadBytes)).
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

func StartBFTConsensus(blockhash string) error {
	log.Printf("StartBFTConsensus: Initiating BFT consensus process")

	if subscriptionService == nil {
		return fmt.Errorf("subscription service not initialized")
	}

	// First, request vote results from all buddy nodes
	if err := RequestVoteResultsFromBuddies(blockhash); err != nil {
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
		var decision = bft.Reject
		if voteResult > 0 {
			decision = bft.Accept
		}

		// Get actual public key
		var pubKeyBytes []byte

		// Try to extract from PeerID first (if embedded like Ed25519)
		if pubKey, err := peerID.ExtractPublicKey(); err == nil && pubKey != nil {
			if raw, err := pubKey.Raw(); err == nil {
				pubKeyBytes = raw
			}
		}

		// If not found, try peerstore
		if len(pubKeyBytes) == 0 && buddyNode.Host != nil {
			if pubKey := buddyNode.Host.Peerstore().PubKey(peerID); pubKey != nil {
				if raw, err := pubKey.Raw(); err == nil {
					pubKeyBytes = raw
				}
			}
		}

		bi := bft.BuddyInput{
			ID:        peerID.String(),
			Decision:  decision,
			PublicKey: pubKeyBytes,
		}

		// For the LOCAL buddy, attach the raw ed25519 private key so the BFT
		// engine can sign its own PREPARE/COMMIT messages. Peers verify against
		// PublicKey (the peer's raw ed25519 key), so the matching raw private key
		// from the host keystore is what produces valid signatures. Without this
		// the engine has no signer and, under the secure-default
		// RequireSignatures, cannot participate.
		if peerID == buddyNode.PeerID && buddyNode.Host != nil {
			if sk := buddyNode.Host.Peerstore().PrivKey(peerID); sk != nil {
				if raw, err := sk.Raw(); err == nil {
					bi.PrivateKey = raw
				}
			}
		}

		allBuddies[i] = bi
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
	myBuddyID := buddyNode.PeerID.String()

	log.Printf("StartBFTConsensus: Running BFT consensus with %d buddies", len(allBuddies))
	result, err := BFTInstance.RunConsensus(
		context.Background(),
		round,
		blockhash,
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


