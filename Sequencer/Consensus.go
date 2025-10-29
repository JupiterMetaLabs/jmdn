package Sequencer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
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
	seenPeers := make(map[string]bool) // Track seen peer IDs to avoid duplicates

	for _, buddy := range buddies {
		peerIDStr := buddy.PeerID.String()
		if !seenPeers[peerIDStr] {
			peerIDs = append(peerIDs, buddy.PeerID)
			seenPeers[peerIDStr] = true
		}
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

	MessagePassing.Init_Loggers(config.LOKI_URL != "")

	// 1. Pull the buddies from the NodeSelectionRouter (all candidates)
	buddies, errMSG := consensus.QueryBuddyNodes()
	if errMSG != nil {
		return fmt.Errorf("failed to query buddy nodes: %v", errMSG)
	}

	log.Printf("Queried %d buddy node candidates from NodeSelectionRouter", len(buddies))

	// 2. Ping buddies and add ONLY reachable ones to cache (stops at config.MaxMainPeers)
	// This will connect to reachable peers and add them to AddPeersCache
	consensus.AddBuddyNodesTemporarily(buddies)

	// 3. Get ONLY the reachable peers from cache (these are the ones that passed ping test)
	// AddPeersTemporary stops at config.MaxMainPeers, so this will have at most MaxMainPeers entries
	buddiesReachableMap := Cache.GetAllPeers()

	if len(buddiesReachableMap) < config.MaxMainPeers {
		return fmt.Errorf("not enough reachable peers: got %d, need at least %d (MaxMainPeers)",
			len(buddiesReachableMap), config.MaxMainPeers)
	}

	log.Printf("Found %d reachable peers in cache (will use first %d as main peers)",
		len(buddiesReachableMap), config.MaxMainPeers)

	// 4. Convert reachable peers map to PeerList (only MaxMainPeers for main peers)
	// Extract peer IDs from cache and limit to MaxMainPeers
	reachablePeerIDs := make([]peer.ID, 0, config.MaxMainPeers)
	count := 0
	for peerID := range buddiesReachableMap {
		if count >= config.MaxMainPeers {
			break
		}
		reachablePeerIDs = append(reachablePeerIDs, peerID)
		count++
		log.Printf("Main peer %d: %s", count, peerID.String()[:16])
	}

	// Set main peers to the reachable ones (exactly MaxMainPeers)
	consensus.PeerList.MainPeers = reachablePeerIDs

	// For backup peers, we can use remaining buddies from original query (or empty for now)
	// Backup peers are standby replacements, not critical for initial setup
	consensus.PeerList.BackupPeers = make([]peer.ID, 0)
	if len(buddies) > config.MaxMainPeers {
		// Use remaining buddies as backup (but don't require them to be reachable)
		backupCount := config.MaxBackupPeers
		if len(buddies)-config.MaxMainPeers < backupCount {
			backupCount = len(buddies) - config.MaxMainPeers
		}
		// Get peer IDs from original buddies list (excluding ones we used for main)
		remainingPeerIDs := make([]peer.ID, 0)
		usedPeers := make(map[string]bool)
		for _, pid := range reachablePeerIDs {
			usedPeers[pid.String()] = true
		}
		for _, buddy := range buddies {
			if !usedPeers[buddy.PeerID.String()] && len(remainingPeerIDs) < backupCount {
				remainingPeerIDs = append(remainingPeerIDs, buddy.PeerID)
			}
		}
		consensus.PeerList.BackupPeers = remainingPeerIDs
		log.Printf("Set %d backup peers from remaining candidates", len(consensus.PeerList.BackupPeers))
	}

	// 5. Filter original buddies list to match reachable peers (for ConsensusMessage)
	// Only include buddies that are in the reachable cache
	reachableBuddies := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, config.MaxMainPeers)
	reachableSet := make(map[string]bool)
	for _, pid := range reachablePeerIDs {
		reachableSet[pid.String()] = true
	}

	// Find matching buddies from original list
	for _, buddy := range buddies {
		if reachableSet[buddy.PeerID.String()] && len(reachableBuddies) < config.MaxMainPeers {
			// Use the multiaddr from cache (it's the reachable one)
			if cachedAddr := Cache.GetPeer(buddy.PeerID); cachedAddr != nil {
				reachableBuddies = append(reachableBuddies, PubSubMessages.Buddy_PeerMultiaddr{
					PeerID:    buddy.PeerID,
					Multiaddr: cachedAddr,
				})
			}
		}
	}

	if len(reachableBuddies) < config.MaxMainPeers {
		return fmt.Errorf("failed to build reachable buddies list: got %d, need %d",
			len(reachableBuddies), config.MaxMainPeers)
	}

	log.Printf("Built reachable buddies list: %d peers (all reachable and added as neighbors)", len(reachableBuddies))

	// 6. Create ConsensusMessage with ONLY reachable buddies
	consensus.ZKBlockData, errMSG = consensus.AddBuddyNodesToPeerList(zkblock, reachableBuddies)
	if errMSG != nil {
		return fmt.Errorf("failed to add buddy nodes to peer list: %v", errMSG)
	}

	// Validate consensus configuration
	if err := ValidateConsensusConfiguration(consensus); err != nil {
		return fmt.Errorf("invalid consensus configuration: %w", err)
	}

	// 7. Create allowed peers list (1 creator + MaxMainPeers reachable main peers + backup)
	allowedPeers := make([]peer.ID, 0, config.MaxMainPeers+config.MaxBackupPeers+1)

	// Add the creator (host) to the allowed list
	allowedPeers = append(allowedPeers, consensus.Host.ID())

	// Add main peers (only reachable ones) to the allowed list
	allowedPeers = append(allowedPeers, consensus.PeerList.MainPeers...)

	// Add backup peers to the allowed list (if any)
	allowedPeers = append(allowedPeers, consensus.PeerList.BackupPeers...)

	log.Printf("Creating pubsub channel with %d allowed peers (1 creator + %d main + %d backup)",
		len(allowedPeers), len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))

	// First create the pubsub channel
	var err error
	consensus.gossipnode, err = Pubsub.NewGossipPubSub(consensus.Host, config.PubSub_ConsensusChannel)
	if err != nil {
		return fmt.Errorf("failed to create pubsub: %v", err)
	}

	// Create the consensus channel for message passing
	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.PubSub_ConsensusChannel, false, allowedPeers); err != nil {
		return fmt.Errorf("failed to create pubsub channel: %v", err)
	}

	log.Printf("Successfully created pubsub channel: %s", config.PubSub_ConsensusChannel)

	// Create the CRDT sync channel ONLY for buddy nodes (vote aggregators) to synchronize their CRDTs
	// This channel is restricted to only the selected buddy nodes (main + backup peers)
	// Regular network nodes cannot join - only nodes performing vote aggregation can access it
	// Using the same allowed peers list as the consensus channel (sequencer + main buddies + backup buddies)
	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.Pubsub_CRDTSync, false, allowedPeers); err != nil {
		if err.Error() != fmt.Sprintf("channel %s already exists", config.Pubsub_CRDTSync) {
			log.Printf("⚠️ Failed to create CRDT sync channel: %v (continuing anyway)", err)
		} else {
			log.Printf("✅ CRDT sync channel already exists: %s", config.Pubsub_CRDTSync)
		}
	} else {
		log.Printf("✅ Successfully created CRDT sync channel: %s (private, %d allowed peers - sequencer + %d main + %d backup buddies only)",
			config.Pubsub_CRDTSync, len(allowedPeers), len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))
	}

	// Subscribe the sequencer to its own channel to receive votes from buddy nodes
	// This is critical - without this, the sequencer won't receive any vote messages
	globalVars := PubSubMessages.NewGlobalVariables()

	// Initialize PubSub BuddyNode for sequencer if not already done
	if !globalVars.IsPubSubNodeInitialized() {
		defaultBuddies := PubSubMessages.NewBuddiesBuilder(nil)
		pubSubBuddyNode := MessagePassing.NewBuddyNode(consensus.Host, defaultBuddies, nil, consensus.gossipnode.GetGossipPubSub())
		globalVars.Set_PubSubNode(pubSubBuddyNode)
		log.Printf("✅ Initialized PubSubNode for sequencer")
	}

	// Now subscribe to the channel
	service := Service.NewSubscriptionService(consensus.gossipnode.GetGossipPubSub())
	if err := service.HandleStreamSubscriptionRequest(config.PubSub_ConsensusChannel); err != nil {
		log.Printf("⚠️ Failed to subscribe sequencer to consensus channel: %v", err)
	} else {
		log.Printf("✅ Sequencer subscribed to consensus channel for vote collection")
	}

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

	// Process votes from CRDT and call processVoteData
	_, err := Structs.ProcessVotesFromCRDT(listenerNode)
	if err != nil {
		fmt.Printf("❌ Failed to process votes from CRDT: %v\n", err)
	}

	// Collect vote aggregation results from buddy nodes
	// Note: Votes are stored on buddy nodes' CRDTs, not on the sequencer
	go func() {
		// Get all vote results from Triggers
		voteResults := Maps.GetAllVoteResults()
		fmt.Printf("📊 Vote results from buddy nodes: %v\n", voteResults)

		// Convert []peer.ID to []BuddyInput with decisions from vote results
		// Only include buddies that actually participated (have vote results)
		buddyInputs := make([]bft.BuddyInput, 0, len(listenerNode.BuddyNodes.Buddies_Nodes))
		for _, buddy := range listenerNode.BuddyNodes.Buddies_Nodes {
			buddyID := buddy.String()
			voteResult, hasVote := voteResults[buddyID]

			// Only include buddies that participated (have a vote)
			if !hasVote {
				fmt.Printf("⚠️ Skipping buddy %s - no vote received\n", buddyID)
				continue
			}

			// Convert vote result to decision: >0 = Accept, <=0 = Reject
			decision := bft.Decision("REJECT")
			if voteResult > 0 {
				decision = bft.Decision("ACCEPT")
			}

			buddyInputs = append(buddyInputs, bft.BuddyInput{
				ID:        buddyID,
				Decision:  decision,
				PublicKey: []byte{}, // TODO: Get actual public key
			})
		}

		// Check if we have enough responses for consensus
		fmt.Printf("🔔 Starting BFT consensus with %d buddies that participated\n", len(buddyInputs))

		if len(buddyInputs) < config.MaxMainPeers {
			fmt.Printf("⚠️ CRITICAL: Only %d buddies participated, but need at least %d (MaxMainPeers) for consensus\n", len(buddyInputs), config.MaxMainPeers)
			fmt.Printf("⚠️ Current buddy pool: %d total (should have %d main + %d backup)\n",
				len(listenerNode.BuddyNodes.Buddies_Nodes), config.MaxMainPeers, config.MaxBackupPeers)
			fmt.Printf("⚠️ Consensus may fail due to insufficient participation. Consider:\n")
			fmt.Printf("   1. Increasing backup peers (MaxBackupPeers)\n")
			fmt.Printf("   2. Checking network connectivity\n")
			fmt.Printf("   3. Extending timeout for vote collection\n")
			// Note: We continue anyway, but BFT may fail
		} else {
			fmt.Printf("✅ Sufficient participation: %d/%d minimum required for consensus\n", len(buddyInputs), config.MaxMainPeers)
		}

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
