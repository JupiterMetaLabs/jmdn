package Sequencer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/NodeSelection/Router"
	"gossipnode/Pubsub"
	"gossipnode/Sequencer/Metadata"
	"gossipnode/Sequencer/Triggers/Maps"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/config/PubSubMessages/Cache"
	"gossipnode/messaging"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

type PeerList struct {
	MainPeers   []peer.ID
	BackupPeers []peer.ID
}
type Consensus struct {
	Channel          string
	PeerList         PeerList
	Host             host.Host
	gossipnode       *Pubsub.StructGossipPubSub
	ListenerNode     *MessagePassing.StructListener
	ResponseHandler  *ResponseHandler
	DiscoveryService *Service.NodeDiscoveryService
	ZKBlockData      *PubSubMessages.ConsensusMessage
}

func NewConsensus(peerList PeerList, host host.Host) *Consensus {
	responseHandler := NewResponseHandler()
	return &Consensus{
		PeerList:        peerList,
		Host:            host,
		Channel:         config.PubSub_ConsensusChannel,
		ResponseHandler: responseHandler,
	}
}

// With the block you will attack the metadata to it before the propagation of the block
// QUERY BUDDY NODES FUNCTIONALITY (we would need this to get the buddy nodes prompted)
func (consensus *Consensus) QueryBuddyNodes() ([]PubSubMessages.Buddy_PeerMultiaddr, error) {
	router := Router.NewNodeselectionRouter()
	buddies, err := router.GetBuddyNodes(config.MaxMainPeers + config.MaxBackupPeers)
	if err != nil {
		return nil, fmt.Errorf("failed to get buddy nodes: %v", err)
	}

	GetPeerIDFromBuddy, errMSG := router.GetBuddyNodesFromList(buddies)
	if errMSG != nil {
		return nil, fmt.Errorf("failed to get buddy nodes from list: %v", errMSG)
	}

	fmt.Printf("Queried Buddies: %+v\n", GetPeerIDFromBuddy)
	return GetPeerIDFromBuddy, nil
}

func (consensus *Consensus) GetOnlyPeerIDs(buddies []PubSubMessages.Buddy_PeerMultiaddr) ([]peer.ID, error) {
	peerIDs := make([]peer.ID, 0)
	for _, buddy := range buddies {
		peerIDs = append(peerIDs, buddy.PeerID)
	}
	return peerIDs, nil
}

func (consensus *Consensus) AddBuddyNodesToPeerList(zkBlock *config.ZKBlock, buddies []PubSubMessages.Buddy_PeerMultiaddr) (*PubSubMessages.ConsensusMessage, error) {

	ZKBlock := Metadata.ZKBlockMetadata(zkBlock, buddies).SetEndTimeoutMetadata(time.Now().Add(config.ConsensusTimeout)).SetStartTimeMetadata(time.Now())
	if ZKBlock == nil {
		return nil, fmt.Errorf("failed to create ZKBlock metadata")
	}
	return ZKBlock.GetConsensusMessage(), nil
}

func (Consensus *Consensus) AddBuddyNodesTemporarily(buddies []PubSubMessages.Buddy_PeerMultiaddr) {
	Cache.AddPeersTemporary(buddies)
}

func (consensus *Consensus) Start(zkblock *config.ZKBlock) error {
	// Validate consensus configuration
	if consensus.Host == nil {
		return fmt.Errorf("consensus host is nil - call SetHostInstance() first")
	}
	if consensus.PeerList.MainPeers == nil {
		return fmt.Errorf("main peers list is nil")
	}
	if consensus.PeerList.BackupPeers == nil {
		return fmt.Errorf("backup peers list is nil")
	}

	// Start the Loggers in the Streaming.go

	// Attach the metadata to the block
	// 1. Pull the buddies from the NodeSelectionRouter
	buddies, errMSG := consensus.QueryBuddyNodes()
	if errMSG != nil {
		return fmt.Errorf("failed to query buddy nodes: %v", errMSG)
	}

	peerIDs, errMSG := consensus.GetOnlyPeerIDs(buddies)
	if errMSG != nil {
		return fmt.Errorf("failed to get only peer IDs: %v", errMSG)
	}

	// Debugging
	for _, buddy := range buddies {
		fmt.Println("buddy", buddy)
	}

	// 2. Attach the buddies to the zkblock as metadata
	consensus.ZKBlockData, errMSG = consensus.AddBuddyNodesToPeerList(zkblock, buddies)
	if errMSG != nil {
		return fmt.Errorf("failed to add buddy nodes to peer list: %v", errMSG)
	}

	// Split peerIDs into 13 main and 3 backup peers
	if len(peerIDs) < config.MaxMainPeers+config.MaxBackupPeers {
		return fmt.Errorf("not enough peers returned for consensus: got %d, need at least %d", len(peerIDs), config.MaxMainPeers+config.MaxBackupPeers)
	}
	consensus.PeerList.MainPeers = make([]peer.ID, config.MaxMainPeers)
	consensus.PeerList.BackupPeers = make([]peer.ID, config.MaxBackupPeers)
	copy(consensus.PeerList.MainPeers, peerIDs[:config.MaxMainPeers])
	copy(consensus.PeerList.BackupPeers, peerIDs[config.MaxMainPeers:config.MaxMainPeers+config.MaxBackupPeers])

	MessagePassing.Init_Loggers(config.LOKI_URL != "")
	// Validate consensus configuration first
	if err := ValidateConsensusConfiguration(consensus); err != nil {
		return fmt.Errorf("invalid consensus configuration: %w", err)
	}

	// 3. Add the buddies to the temporary cache
	consensus.AddBuddyNodesTemporarily(buddies)

	// Create allowed peers list (1 creator + 13 main + 3 backup = 17 total)
	allowedPeers := make([]peer.ID, 0, config.MaxMainPeers+config.MaxBackupPeers+1)

	// Add the creator (host) to the allowed list
	allowedPeers = append(allowedPeers, consensus.Host.ID())

	// Add main peers to the allowed list
	allowedPeers = append(allowedPeers, consensus.PeerList.MainPeers...)

	// Add backup peers to the allowed list
	allowedPeers = append(allowedPeers, consensus.PeerList.BackupPeers...)

	log.Printf("Creating pubsub channel with %d allowed peers (1 creator + %d main + %d backup)",
		len(allowedPeers), len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))

	// First create the pubsub channel
	var err error
	consensus.gossipnode, err = Pubsub.NewGossipPubSub(consensus.Host, config.PubSub_ConsensusChannel)
	if err != nil {
		return fmt.Errorf("failed to create pubsub: %v", err)
	}

	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.PubSub_ConsensusChannel, true, allowedPeers); err != nil {
		return fmt.Errorf("failed to create pubsub channel: %v", err)
	}

	log.Printf("Successfully created pubsub channel: %s", config.PubSub_ConsensusChannel)

	// Initialize listener node for vote collection
	consensus.ListenerNode = MessagePassing.NewListenerNode(consensus.Host, consensus.ResponseHandler)
	log.Printf("Listener node initialized for vote collection on protocol: %s", config.SubmitMessageProtocol)
	log.Printf("SubmitMessageProtocol handler is already set up by NewListenerNode()")

	// Populate buddy nodes list for the global listener node
	allPeerIDs := make([]peer.ID, 0, len(consensus.PeerList.MainPeers)+len(consensus.PeerList.BackupPeers))
	allPeerIDs = append(allPeerIDs, consensus.PeerList.MainPeers...)
	allPeerIDs = append(allPeerIDs, consensus.PeerList.BackupPeers...)

	// Update the global listener node with buddy nodes
	globalListenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if globalListenerNode != nil {
		globalListenerNode.BuddyNodes.Buddies_Nodes = allPeerIDs
		log.Printf("Populated listener node with %d buddy nodes (%d main + %d backup)",
			len(allPeerIDs), len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))
	}

	// After creating the channel, ask peers to subscribe to the channel
	if err := consensus.RequestSubscriptionPermission(); err != nil {
		return fmt.Errorf("failed to request subscription permission: %v", err)
	}

	// Start vote trigger in a goroutine with 5-second delay
	go func() {
		fmt.Printf("=== [Consensus.Start] Starting vote trigger goroutine with 5-second delay ===\n")
		time.Sleep(5 * time.Second)

		fmt.Printf("=== [Consensus.Start] About to call BroadcastVoteTrigger ===\n")
		fmt.Printf("=== [Consensus.Start] consensus.ZKBlockData: %+v ===\n", consensus.ZKBlockData)

		// Broadcast vote trigger to all subscribed peers
		if err := consensus.BroadcastVoteTrigger(); err != nil {
			fmt.Printf("=== [Consensus.Start] BroadcastVoteTrigger FAILED: %v ===\n", err)
		} else {
			fmt.Printf("=== [Consensus.Start] BroadcastVoteTrigger completed successfully ===\n")
		}
	}()

	// Start CRDT print trigger in a goroutine with 15-second delay after broadcast trigger
	// This gives time for votes to propagate through the network
	go func() {
		fmt.Printf("=== [Consensus.Start] Starting CRDT print trigger goroutine with 15-second delay ===\n")
		time.Sleep(15 * time.Second)

		fmt.Printf("\n=== [CRDT PRINT TRIGGER] Printing CRDT state after 15 seconds ===\n")
		if err := consensus.PrintCRDTState(); err != nil {
			fmt.Printf("=== [CRDT PRINT TRIGGER] Failed to print CRDT state: %v ===\n", err)
		}
	}()

	// Verify that nodes are actually subscribed (non-blocking for vote trigger)
	fmt.Printf("=== [Consensus.Start] About to verify subscriptions ===\n")
	if err := consensus.VerifySubscriptions(); err != nil {
		fmt.Printf("=== [Consensus.Start] VerifySubscriptions FAILED: %v ===\n", err)
		// Don't return error, let vote trigger run anyway
		fmt.Printf("=== [Consensus.Start] Continuing despite verification failure ===\n")
	} else {
		fmt.Printf("=== [Consensus.Start] VerifySubscriptions PASSED ===\n")
	}

	return nil
}

// RequestSubscriptionPermission asks all buddy nodes for permission to subscribe to the consensus channel
// Ensures: 1 creator + 13 subscribers = 14 total nodes
// Maximum 3 main nodes can fail, use backup nodes as replacements
func (consensus *Consensus) RequestSubscriptionPermission() error {
	if consensus.gossipnode == nil {
		return fmt.Errorf("GossipPubSub not initialized")
	}

	// Validate consensus configuration first
	if err := ValidateConsensusConfiguration(consensus); err != nil {
		return fmt.Errorf("invalid consensus configuration: %w", err)
	}

	log.Printf("Requesting subscription permission from buddy nodes for channel: %s", consensus.Channel)

	// Use the AskForSubscription function from Communication.go
	err := AskForSubscription(consensus.ListenerNode, consensus.Channel, consensus)
	if err != nil {
		return fmt.Errorf("failed to get subscription permission: %w", err)
	}

	log.Printf("Successfully obtained subscription permission: 1 creator + 13 subscribers = 14 total nodes")
	return nil
}

// VerifySubscriptions checks if nodes are actually subscribed to the pubsub channel
// This method now uses the new pubsub-based verification system
func (consensus *Consensus) VerifySubscriptions() error {
	if consensus.gossipnode == nil {
		return fmt.Errorf("GossipPubSub not initialized for consensus")
	}

	log.Printf("Starting subscription verification using pubsub messaging...")

	// Use the new VerifySubscriptions function from Communication.go
	verifiedPeerIDs, err := VerifySubscriptions(consensus.gossipnode.GetGossipPubSub(), consensus)
	if err != nil {
		return fmt.Errorf("failed to verify subscriptions: %v", err)
	}

	log.Printf("Received verification responses from %d peers", len(verifiedPeerIDs))
	fmt.Printf("=== [VerifySubscriptions] Got %d verified peers, expecting %d ===\n", len(verifiedPeerIDs), config.MaxMainPeers)

	// Verify that we have the expected number of subscribers (13)
	if len(verifiedPeerIDs) != config.MaxMainPeers {
		return fmt.Errorf("incorrect number of verified peers: got %d, expected %d", len(verifiedPeerIDs), config.MaxMainPeers)
	}

	fmt.Printf("=== [VerifySubscriptions] Verification count check PASSED ===\n")

	// Log all verified PeerIDs
	for connectionPeerID, responsePeerID := range verifiedPeerIDs {
		log.Printf("Verified subscription: connection peer %s -> response peer %s", connectionPeerID, responsePeerID)
	}

	log.Printf("Subscription verification successful: %d peers properly verified via pubsub messaging", len(verifiedPeerIDs))
	return nil
}

// BroadcastVoteTrigger broadcasts a vote trigger message to all subscribed peers
// This initiates the voting process by sending vote trigger broadcasts
func (consensus *Consensus) BroadcastVoteTrigger() error {
	fmt.Printf("=== [BroadcastVoteTrigger] ENTRY ===\n")

	if consensus.gossipnode == nil {
		fmt.Printf("=== [BroadcastVoteTrigger] ERROR: GossipPubSub not initialized ===\n")
		return fmt.Errorf("GossipPubSub not initialized for consensus")
	}

	if consensus.ZKBlockData == nil {
		fmt.Printf("=== [BroadcastVoteTrigger] ERROR: ZKBlockData not set ===\n")
		return fmt.Errorf("ZKBlockData not set - cannot trigger voting")
	}

	fmt.Printf("=== [BroadcastVoteTrigger] GossipPubSub and ZKBlockData are valid ===\n")
	fmt.Printf("=== [BroadcastVoteTrigger] About to call messaging.BroadcastVoteTrigger ===\n")
	fmt.Printf("=== [BroadcastVoteTrigger] consensus.Host.ID(): %s ===\n", consensus.Host.ID())

	log.Printf("Starting vote trigger broadcast to all connected peers...")

	// Use the messaging.BroadcastVoteTrigger function to broadcast the vote trigger
	if err := messaging.BroadcastVoteTrigger(consensus.Host, consensus.ZKBlockData); err != nil {
		fmt.Printf("=== [BroadcastVoteTrigger] messaging.BroadcastVoteTrigger FAILED: %v ===\n", err)
		return fmt.Errorf("failed to broadcast vote trigger: %w", err)
	}

	fmt.Printf("=== [BroadcastVoteTrigger] SUCCESS ===\n")
	log.Printf("Vote trigger broadcast completed successfully")
	return nil
}

// PrintCRDTState prints the current state of the CRDT
func (consensus *Consensus) PrintCRDTState() error {
	// Get the listener node
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		return fmt.Errorf("listener node or CRDT layer not initialized")
	}

	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║             CRDT STATE - SEQUENCER                         ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("Peer ID: %s\n", listenerNode.PeerID.String())
	fmt.Printf("Timestamp: %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("Block Hash: %s\n", consensus.ZKBlockData.GetZKBlock().BlockHash.String())
	fmt.Printf("Messages Received: %d | Sent: %d | Total: %d\n",
		listenerNode.MetaData.Received,
		listenerNode.MetaData.Sent,
		listenerNode.MetaData.Total)

	// Get all votes from CRDT
	votes, exists := MessagePassing.GetVotesFromCRDT(listenerNode.CRDTLayer, "vote")
	if !exists || len(votes) == 0 {
		fmt.Printf("\n📊 Votes in CRDT: 0 (no votes collected yet)\n")
	} else {
		fmt.Printf("\n📊 Total Votes in CRDT: %d\n", len(votes))
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		// Parse and count votes
		yesVotes := 0
		noVotes := 0

		for i, vote := range votes {
			// Parse the vote JSON
			var voteData map[string]interface{}
			if err := json.Unmarshal([]byte(vote), &voteData); err != nil {
				fmt.Printf("  Vote %d: [PARSING ERROR] %s\n", i+1, vote)
				continue
			}

			// Extract vote details
			voteValue := voteData["vote"]
			blockHash := voteData["block_hash"]

			// Count vote types
			if voteValue == float64(1) {
				yesVotes++
			} else if voteValue == float64(-1) {
				noVotes++
			}

			fmt.Printf("  ✓ Vote %d:\n", i+1)
			fmt.Printf("    - Value: %v\n", voteValue)
			fmt.Printf("    - Block Hash: %v\n", blockHash)
			if i < len(votes)-1 {
				fmt.Printf("    ─────────────────────────────────────────────\n")
			}
		}

		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("📊 Vote Summary: YES=%d, NO=%d, Total=%d\n", yesVotes, noVotes, len(votes))
	}

	fmt.Printf("╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")

	metadata := listenerNode.MetaData
	fmt.Println("metadata", metadata)

	// Collect vote aggregation results from buddy nodes
	// Note: Votes are stored on buddy nodes' CRDTs, not on the sequencer
	go func() {
		// Get all vote results from Triggers
		voteResults := Maps.GetAllVoteResults()
		fmt.Printf("📊 Vote results from buddy nodes: %v\n", voteResults)

		// Convert []peer.ID to []BuddyInput with decisions from vote results
		buddyInputs := make([]bft.BuddyInput, len(listenerNode.BuddyNodes.Buddies_Nodes))
		for i, buddy := range listenerNode.BuddyNodes.Buddies_Nodes {
			buddyID := buddy.String()
			voteResult := voteResults[buddyID]

			// Convert vote result to decision: >0 = Accept, <=0 = Reject
			decision := bft.Decision("REJECT")
			if voteResult > 0 {
				decision = bft.Decision("ACCEPT")
			}

			buddyInputs[i] = bft.BuddyInput{
				ID:        buddyID,
				Decision:  decision,
				PublicKey: []byte{}, // TODO: Get actual public key
			}
		}

		fmt.Printf("🔔 Starting BFT consensus with %d buddies\n", len(buddyInputs))

		// BFT := bft.New(bft.Config{
		// 	MinBuddies:         config.MaxMainPeers,
		// 	ByzantineTolerance: 4,
		// 	PrepareTimeout:     10 * time.Second,
		// 	CommitTimeout:      10 * time.Second,
		// })

		// adapter, err := bft.NewBFTPubSubAdapter(context.Background(), consensus.gossipnode.GetGossipPubSub(), BFT, config.PubSub_ConsensusChannel)
		// if err != nil {
		// 	fmt.Printf("❌ Failed to create BFT adapter: %v\n", err)
		// }
		// messenger := bft.Return_pubsubMessenger(adapter, consensus.ZKBlockData.GetZKBlock().BlockHash.String())

		// result, err := BFT.RunConsensus(context.Background(), 1, consensus.ZKBlockData.GetZKBlock().BlockHash.String(), listenerNode.PeerID.String(), buddyInputs, messenger, nil)
		// if err != nil {
		// 	fmt.Printf("❌ Failed to run BFT consensus: %v\n", err)
		// }

		// Request vote aggregation results from all buddy nodes
		fmt.Println("Requesting vote results from all buddy nodes:", listenerNode.BuddyNodes.Buddies_Nodes)

		var wg sync.WaitGroup
		for _, buddyID := range listenerNode.BuddyNodes.Buddies_Nodes {
			wg.Add(1)
			go func(peerID peer.ID) {
				defer wg.Done()
				stream, err := consensus.Host.NewStream(context.Background(), peerID, config.SubmitMessageProtocol)
				if err != nil {
					fmt.Printf("❌ Failed to open stream to %s: %v\n", peerID, err)
					return
				}
				defer stream.Close()

				// Request vote aggregation result from this buddy node
				reqAck := PubSubMessages.NewACKBuilder().True_ACK_Message(consensus.Host.ID(), config.Type_VoteResult)
				reqMsg := PubSubMessages.NewMessageBuilder(nil).
					SetSender(consensus.Host.ID()).
					SetMessage("RequestForVoteAggregationResult").
					SetTimestamp(time.Now().Unix()).
					SetACK(reqAck)

				reqData, _ := json.Marshal(reqMsg)
				stream.Write([]byte(string(reqData) + string(rune(config.Delimiter))))
				fmt.Printf("📨 Sent vote result request to %s\n", peerID)

				// Read the result with timeout
				responseCh := make(chan string, 1)
				errCh := make(chan error, 1)
				reader := bufio.NewReader(stream)

				go func() {
					response, err := reader.ReadString(config.Delimiter)
					if err != nil {
						errCh <- err
						return
					}
					responseCh <- response
				}()

				var response string
				select {
				case resp := <-responseCh:
					response = resp
				case err := <-errCh:
					fmt.Printf("⚠️ Failed to read response from %s: %v\n", peerID, err)
					return
				case <-time.After(3 * time.Second):
					fmt.Printf("⏱️ Timeout waiting for response from %s\n", peerID)
					return
				}

				responseMsg := PubSubMessages.NewMessageBuilder(nil).DeferenceMessage(response)
				if responseMsg != nil {
					var resultData map[string]interface{}
					if err := json.Unmarshal([]byte(responseMsg.Message), &resultData); err == nil {
						if result, ok := resultData["result"].(float64); ok {
							Maps.StoreVoteResult(peerID.String(), int8(result))
							fmt.Printf("✅ Received vote result from %s: %d\n", peerID, int8(result))
						}
					}
				}
			}(buddyID)
		}
		wg.Wait()
		fmt.Printf("✅ Collected vote results from all buddy nodes\n")
	}()

	// Get metadata from the global listener node
	return nil
}

// IsListenerActive checks if the listener node is active and ready to collect votes
func (consensus *Consensus) IsListenerActive() bool {
	// Check global singleton
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	return listenerNode != nil
}

// StartVoteCollection starts the vote collection process
// This method demonstrates how the consensus system is ready to collect votes
func (consensus *Consensus) StartVoteCollection(blockHash string) error {
	if !consensus.IsListenerActive() {
		return fmt.Errorf("listener node not active - cannot collect votes")
	}

	log.Printf("Vote collection started for block hash: %s", blockHash)
	log.Printf("Listener node is active and ready to receive votes via %s protocol", config.SubmitMessageProtocol)
	log.Printf("Normal nodes can now send votes with stage: %s", config.Type_SubmitVote)

	return nil
}
