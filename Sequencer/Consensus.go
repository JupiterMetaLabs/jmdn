package Sequencer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BFT/bft"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/AVC/NodeSelection/Router"
	"gossipnode/Pubsub"
	"gossipnode/Sequencer/Alerts"
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
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
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
	AlertHandlers    *Alerts.ConsensusAlertHandlers
	// Guards to prevent infinite loops
	voteProcessingMu   sync.Mutex
	isProcessingVotes  bool
	processedBlockHash string
}

func NewConsensus(peerList PeerList, host host.Host) *Consensus {
	responseHandler := NewResponseHandler()
	return &Consensus{
		PeerList:        peerList,
		Host:            host,
		Channel:         config.PubSub_ConsensusChannel,
		ResponseHandler: responseHandler,
		AlertHandlers:   Alerts.NewConsensusAlertHandlers(),
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

	// fmt.Printf("Queried Buddies: %+v\n", GetPeerIDFromBuddy)
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

	ZKBlock := Metadata.ZKBlockMetadata(zkBlock, buddies).SetEndTimeoutMetadata(time.Now().UTC().Add(config.ConsensusTimeout)).SetStartTimeMetadata(time.Now().UTC())
	if ZKBlock == nil {
		return nil, fmt.Errorf("failed to create ZKBlock metadata")
	}
	return ZKBlock.GetConsensusMessage(), nil
}

func (Consensus *Consensus) AddBuddyNodesTemporarily(buddies []PubSubMessages.Buddy_PeerMultiaddr) {
	Cache.AddPeersTemporary(buddies)
}

// pingAndAddToCache pings a buddy node and adds it to cache if reachable
// Does NOT connect - connection is handled separately by AddPeerCache.ConnectToTemporaryPeers
func (consensus *Consensus) pingAndAddToCache(buddy PubSubMessages.Buddy_PeerMultiaddr) error {
	nodeManager := Cache.GetNodeManager()
	if nodeManager == nil {
		return fmt.Errorf("NodeManager not available")
	}

	addrStr := buddy.Multiaddr.String()

	// Ping to check reachability
	reachable, _, err := nodeManager.PingMultiaddrWithRetries(addrStr, 3)
	if err != nil {
		return fmt.Errorf("ping failed: %v", err)
	}

	if !reachable {
		return fmt.Errorf("peer not reachable at %s", addrStr)
	}

	// Add to cache (connection will be done by AddPeerCache.ConnectToTemporaryPeers)
	Cache.AddPeer(buddy.PeerID, buddy.Multiaddr)

	return nil
}

func (consensus *Consensus) InitCandidateLists() ([]PubSubMessages.Buddy_PeerMultiaddr, []PubSubMessages.Buddy_PeerMultiaddr) {
	MainCandidates := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, config.MaxMainPeers)
	BackupCandidates := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, config.MaxBackupPeers)
	return MainCandidates, BackupCandidates
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

	// Always start a new round with a clean vote results map
	Maps.ClearVoteResults()
	log.Printf("Cleared previous round vote results at start of consensus round")

	// 1. Pull the buddies from the NodeSelectionRouter (all candidates)
	buddies, errMSG := consensus.QueryBuddyNodes()
	if errMSG != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("failed to query buddy nodes: %v", errMSG))
		}
		return fmt.Errorf("failed to query buddy nodes: %v", errMSG)
	}

	log.Printf("Queried %d buddy node candidates from NodeSelectionRouter", len(buddies))

	// 2. Split buddies into main candidates (first MaxMainPeers) and backup candidates (next MaxBackupPeers)
	// Main peers are primary - we try to connect to these first
	// Backup peers are fallback - we use these if main peer connections fail
	candidates := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, config.MaxMainPeers+config.MaxBackupPeers)
	// Deduplicate buddies by peer.ID (buddies may have multiple multiaddrs per peer)
	seenPeers := make(map[string]bool)
	for _, buddy := range buddies {
		pidStr := buddy.PeerID.String()
		if seenPeers[pidStr] {
			continue // Skip duplicates
		}
		seenPeers[pidStr] = true

		// Add the reachable peers to the candidates list to check the reachability - after deduplication
		candidates = append(candidates, buddy)
	}

	log.Printf("got: %d candidates after deduplication", len(candidates))

	// 3. Try main peers first (primary) - ping and add to cache (no connection yet)
	MainCandidates, BackupCandidates := consensus.InitCandidateLists()

	// Clear cache before starting
	Cache.ClearCache()

	// Try main candidates first - ping and add to cache
	log.Printf("Checking reachability for %d all peers candidates...", len(candidates))
	for _, candidate := range candidates {
		// Ping and add to cache (no connection - that will be done by AddPeerCache)
		if err := consensus.pingAndAddToCache(candidate); err != nil {
			log.Printf("Main peer %s ping failed: %v (will try backup if needed)",
				candidate.PeerID.String()[:16], err)
			continue
		}

		if len(MainCandidates) >= config.MaxMainPeers {
			BackupCandidates = append(BackupCandidates, candidate)
			log.Printf("✓ Backup peer %d/%d reachable: %s",
				len(BackupCandidates), config.MaxBackupPeers, candidate.PeerID.String()[:16])
		} else {
			// Successfully pinged and added to cache
			MainCandidates = append(MainCandidates, candidate)
			log.Printf("✓ Main peer %d/%d reachable: %s",
				len(MainCandidates), config.MaxMainPeers, candidate.PeerID.String()[:16])
		}
	}

	// 5. Verify we have exactly MaxMainPeers reachable buddy nodes
	if len(MainCandidates) < config.MaxMainPeers {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("not enough reachable peers: got %d, need exactly %d (MaxMainPeers). Main candidates: %d, Backup candidates: %d",
				len(MainCandidates), config.MaxMainPeers, len(MainCandidates), len(BackupCandidates)))
		}
		return fmt.Errorf("not enough reachable peers: got %d, need exactly %d (MaxMainPeers). Main candidates: %d, Backup candidates: %d",
			len(MainCandidates), config.MaxMainPeers, len(MainCandidates), len(BackupCandidates))
	}

	// 6. Now connect to final buddy nodes - ONLY main candidates first, backup only if needed
	// This prevents excessive connections to backup nodes when we already have enough main peers
	reachablePeers := make(map[peer.ID]multiaddr.Multiaddr, config.MaxMainPeers)

	// First, try to connect to only MainCandidates (up to MaxMainPeers)
	for _, buddy := range MainCandidates {
		if len(reachablePeers) >= config.MaxMainPeers {
			break // We have enough main peers, no need to check more
		}
		connectedness := consensus.Host.Network().Connectedness(buddy.PeerID)
		if connectedness == network.Connected {
			reachablePeers[buddy.PeerID] = buddy.Multiaddr
			log.Printf("✅ Main buddy node %s is actually connected (status: %v)", buddy.PeerID.String()[:16], connectedness)
		}
	}

	// Only if we don't have enough main peers, try backup candidates
	if len(reachablePeers) < config.MaxMainPeers {
		log.Printf("⚠️ Only %d/%d main peers connected, trying backup candidates...", len(reachablePeers), config.MaxMainPeers)
		for _, buddy := range BackupCandidates {
			if len(reachablePeers) >= config.MaxMainPeers {
				break // We have enough peers now
			}
			connectedness := consensus.Host.Network().Connectedness(buddy.PeerID)
			if connectedness == network.Connected {
				reachablePeers[buddy.PeerID] = buddy.Multiaddr
				log.Printf("✅ Backup buddy node %s is actually connected (status: %v)", buddy.PeerID.String()[:16], connectedness)
			}
		}
	} else {
		log.Printf("✅ Have enough main peers (%d/%d), skipping backup candidates", len(reachablePeers), config.MaxMainPeers)
	}

	// Post consensus we have to clear this cache to avoid any memory leaks
	log.Printf("Connecting to %d final buddy nodes via AddPeerCache (only main peers, no backup unless needed)...", len(reachablePeers))

	if err := Cache.ConnectToTemporaryPeers(reachablePeers); err != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("failed to connect to final buddy nodes: %v", err))
		}
		return fmt.Errorf("failed to connect to final buddy nodes: %v", err)
	}

	// Verify we have exactly MaxMainPeers actually connected peers
	if len(reachablePeers) < config.MaxMainPeers {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("insufficient actually connected peers: got %d, need exactly %d (MaxMainPeers). Main candidates: %d, Backup candidates: %d",
				len(reachablePeers), config.MaxMainPeers, len(MainCandidates), len(BackupCandidates)))
		}
		return fmt.Errorf("insufficient actually connected peers: got %d, need exactly %d (MaxMainPeers). Main candidates: %d, Backup candidates: %d",
			len(reachablePeers), config.MaxMainPeers, len(MainCandidates), len(BackupCandidates))
	}

	// 7. Set final buddy nodes - these are the only peers we use (no backup peers)
	// Use only the actually connected peers (the keys of reachablePeers)
	consensus.PeerList.MainPeers = make([]peer.ID, 0, len(MainCandidates))
	consensus.PeerList.BackupPeers = make([]peer.ID, 0, len(BackupCandidates))

	// clear the maincandidates and backupcandidates lists
	MainCandidates, BackupCandidates = consensus.InitCandidateLists()

	// go through the map and get the first config.MaxMainPeers peers
	for id, addr := range reachablePeers {
		if len(consensus.PeerList.MainPeers) >= config.MaxMainPeers {
			BackupCandidates = append(BackupCandidates, PubSubMessages.Buddy_PeerMultiaddr{PeerID: id, Multiaddr: addr})
			consensus.PeerList.BackupPeers = append(consensus.PeerList.BackupPeers, id)
		} else {
			MainCandidates = append(MainCandidates, PubSubMessages.Buddy_PeerMultiaddr{PeerID: id, Multiaddr: addr})
			consensus.PeerList.MainPeers = append(consensus.PeerList.MainPeers, id)
		}
	}

	log.Printf("✅ Final buddy nodes: %d actually connected peers (these are responsible for votes, CRDT sync, pubsub sync, vote aggregation)",
		len(consensus.PeerList.MainPeers))
	// ALERTS_TODO: Remove alert after monitoring period (temporary)
	// Send the success alert
	if consensus.AlertHandlers != nil {
		consensus.AlertHandlers.SendConnectionSuccess(context.Background(), fmt.Sprintf("Final buddy nodes: %d actually connected peers (these are responsible for votes, CRDT sync, pubsub sync, vote aggregation)", len(consensus.PeerList.MainPeers)))
	}
	log.Printf("Built final buddies list: %d peers (all actually connected and ready for consensus)", len(consensus.PeerList.MainPeers))

	// 8. Create ConsensusMessage with ONLY the final connected buddy nodes
	consensus.ZKBlockData, errMSG = consensus.AddBuddyNodesToPeerList(zkblock, MainCandidates)
	if errMSG != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("failed to add buddy nodes to peer list: %v", errMSG))
		}
		return fmt.Errorf("failed to add buddy nodes to peer list: %v", errMSG)
	}

	// Validate consensus configuration
	if err := ValidateConsensusConfiguration(consensus); err != nil {
		// ALERTS_TODO
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("invalid consensus configuration: %v", err))
		}
		return fmt.Errorf("invalid consensus configuration: %w", err)
	}

	// 9. Create allowed peers list (1 creator + MaxMainPeers main peers + backup peers for fallback)
	// Include backup peers so they can subscribe if main peers fail to respond
	allowedPeers := make([]peer.ID, 0, config.MaxMainPeers+config.MaxBackupPeers+1)

	// Add the creator (host) to the allowed list
	allowedPeers = append(allowedPeers, consensus.Host.ID())

	// Add main buddy nodes (primary subscribers)
	allowedPeers = append(allowedPeers, consensus.PeerList.MainPeers...)

	// Add backup buddy nodes (fallback subscribers - will be used if main peers don't respond)
	allowedPeers = append(allowedPeers, consensus.PeerList.BackupPeers...)

	log.Printf("Creating pubsub channel with %d allowed peers (1 creator + %d main + %d backup buddy nodes)",
		len(allowedPeers), len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))

	// First create the pubsub channel
	var err error
	consensus.gossipnode, err = Pubsub.NewGossipPubSub(consensus.Host, config.PubSub_ConsensusChannel)
	if err != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("failed to create pubsub: %v", err))
		}
		return fmt.Errorf("failed to create pubsub: %v", err)
	}

	// Create the consensus channel for message passing
	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.PubSub_ConsensusChannel, false, allowedPeers); err != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendInitFailure(context.Background(), fmt.Sprintf("failed to create pubsub channel: %v", err))
		}
		return fmt.Errorf("failed to create pubsub channel: %v", err)
	}

	log.Printf("Successfully created pubsub channel: %s", config.PubSub_ConsensusChannel)

	// Create the CRDT sync channel ONLY for final buddy nodes (vote aggregators) to synchronize their CRDTs
	// This channel is restricted to only the final connected buddy nodes
	// Regular network nodes cannot join - only the 4 buddy nodes performing vote aggregation can access it
	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.Pubsub_CRDTSync, false, allowedPeers); err != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendCRDTSyncFailure(context.Background(), fmt.Sprintf("failed to create CRDT sync channel: %v", err))
		}
		if err.Error() != fmt.Sprintf("channel %s already exists", config.Pubsub_CRDTSync) {
			log.Printf("⚠️ Failed to create CRDT sync channel: %v (continuing anyway)", err)
		} else {
			log.Printf("✅ CRDT sync channel already exists: %s", config.Pubsub_CRDTSync)
		}
	} else {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		// Send the success alert
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendCRDTSyncSuccess(context.Background(), fmt.Sprintf("Successfully created CRDT sync channel: %s (private, %d allowed peers - sequencer + %d buddy nodes only)",
				config.Pubsub_CRDTSync, len(allowedPeers), len(consensus.PeerList.MainPeers)))
		}
		log.Printf("✅ Successfully created CRDT sync channel: %s (private, %d allowed peers - sequencer + %d buddy nodes only)",
			config.Pubsub_CRDTSync, len(allowedPeers), len(consensus.PeerList.MainPeers))
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
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendSubscriptionFailure(context.Background(), fmt.Sprintf("failed to subscribe sequencer to consensus channel: %v", err))
		}
		log.Printf("⚠️ Failed to subscribe sequencer to consensus channel: %v", err)
	} else {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendSubscriptionSuccess(context.Background(), "Successfully subscribed to consensus channel for vote collection")
		}
		log.Printf("✅ Successfully subscribed to consensus channel for vote collection")
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
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendSubscriptionFailure(context.Background(), fmt.Sprintf("failed to request subscription permission: %v", err))
		}
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
			// ALERTS_TODO: Remove alert after monitoring period (temporary)
			if consensus.AlertHandlers != nil {
				consensus.AlertHandlers.SendBroadcastVoteTriggerFailure(context.Background(), fmt.Sprintf("failed to broadcast vote trigger: %v", err))
			}
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

	// Verify that nodes are actually subscribed (non-blocking, with delay to allow subscription)
	// Buddy nodes need time to receive subscription request, subscribe to channel, and be ready
	go func() {
		fmt.Printf("=== [Consensus.Start] Starting subscription verification goroutine with 3-second delay ===\n")
		time.Sleep(3 * time.Second) // Give buddy nodes time to subscribe

		fmt.Printf("=== [Consensus.Start] About to verify subscriptions ===\n")
		if err := consensus.VerifySubscriptions(); err != nil {
			fmt.Printf("=== [Consensus.Start] VerifySubscriptions FAILED: %v ===\n", err)
			// Don't return error, let vote trigger run anyway
			fmt.Printf("=== [Consensus.Start] Continuing despite verification failure ===\n")
		} else {
			fmt.Printf("=== [Consensus.Start] VerifySubscriptions PASSED ===\n")
		}
	}()

	return nil
}

// RequestSubscriptionPermission asks all buddy nodes for permission to subscribe to the consensus channel
// Ensures: 1 creator + MaxMainPeers subscribers = MaxMainPeers + 1 total nodes
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

	log.Printf("Successfully obtained subscription permission: 1 creator + MaxMainPeers subscribers = MaxMainPeers + 1 total nodes")
	return nil
}

// VerifySubscriptions checks if nodes are actually subscribed to the pubsub channel
// This method now uses the new pubsub-based verification system
func (consensus *Consensus) VerifySubscriptions() error {
	if consensus.gossipnode == nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendSubscriptionFailure(context.Background(), "GossipPubSub not initialized for consensus")
		}
		return fmt.Errorf("GossipPubSub not initialized for consensus")
	}

	log.Printf("Starting subscription verification using pubsub messaging...")

	// Use the new VerifySubscriptions function from Communication.go
	verifiedPeerIDs, err := VerifySubscriptions(consensus.gossipnode.GetGossipPubSub(), consensus)
	if err != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendSubscriptionFailure(context.Background(), fmt.Sprintf("failed to verify subscriptions: %v", err))
		}
		return fmt.Errorf("failed to verify subscriptions: %v", err)
	}

	log.Printf("Received verification responses from %d peers", len(verifiedPeerIDs))
	fmt.Printf("=== [VerifySubscriptions] Got %d verified peers, expecting %d ===\n", len(verifiedPeerIDs), config.MaxMainPeers)

	// Verify that we have the expected number of subscribers (13)
	if len(verifiedPeerIDs) != config.MaxMainPeers {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendSubscriptionFailure(context.Background(), fmt.Sprintf("incorrect number of verified peers: got %d, expected %d", len(verifiedPeerIDs), config.MaxMainPeers))
		}
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
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendBroadcastVoteTriggerFailure(context.Background(), fmt.Sprintf("failed to broadcast vote trigger: %v", err))
		}
		return fmt.Errorf("failed to broadcast vote trigger: %w", err)
	}

	fmt.Printf("=== [BroadcastVoteTrigger] SUCCESS ===\n")
	log.Printf("Vote trigger broadcast completed successfully")
	return nil
}

// PrintCRDTState prints the current state of the CRDT
func (consensus *Consensus) PrintCRDTState() error {
	// Get the listener node first to validate
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		return fmt.Errorf("listener node or CRDT layer not initialized")
	}

	// Guard against concurrent calls for the same block
	currentBlockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
	consensus.voteProcessingMu.Lock()
	if consensus.isProcessingVotes && consensus.processedBlockHash == currentBlockHash {
		consensus.voteProcessingMu.Unlock()
		fmt.Printf("⚠️ PrintCRDTState: Vote processing already in progress for block %s - skipping duplicate call\n", currentBlockHash)
		return nil
	}
	consensus.voteProcessingMu.Unlock()

	fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║             CRDT STATE - SEQUENCER                         ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("Peer ID: %s\n", listenerNode.PeerID.String())
	fmt.Printf("Timestamp: %s\n", time.Now().UTC().Format(time.RFC3339))
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
	blockHashForProcessing := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
	_, err := Structs.ProcessVotesFromCRDT(listenerNode, blockHashForProcessing)
	if err != nil {
		// ALERTS_TODO: Remove alert after monitoring period (temporary)
		if consensus.AlertHandlers != nil {
			consensus.AlertHandlers.SendBroadcastVoteTriggerFailure(context.Background(), fmt.Sprintf("failed to process votes from CRDT: %v", err))
		}
		fmt.Printf("❌ Failed to process votes from CRDT: %v\n", err)
	}

	// Collect vote aggregation results from buddy nodes
	// Note: Votes are stored on buddy nodes' CRDTs, not on the sequencer

	// Guard against multiple concurrent executions for the same block
	currentBlockHash = consensus.ZKBlockData.GetZKBlock().BlockHash.String()
	consensus.voteProcessingMu.Lock()
	if consensus.isProcessingVotes && consensus.processedBlockHash == currentBlockHash {
		consensus.voteProcessingMu.Unlock()
		fmt.Printf("⚠️ Vote processing already in progress for block %s - skipping duplicate call\n", currentBlockHash)
		return nil
	}
	consensus.isProcessingVotes = true
	consensus.processedBlockHash = currentBlockHash
	consensus.voteProcessingMu.Unlock()

	go func() {
		// Ensure we unlock even if goroutine panics
		defer func() {
			consensus.voteProcessingMu.Lock()
			consensus.isProcessingVotes = false
			consensus.voteProcessingMu.Unlock()
		}()

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
			// ALERTS_TODO: Remove alert after monitoring period (temporary)
			if consensus.AlertHandlers != nil && consensus.ZKBlockData != nil && consensus.ZKBlockData.GetZKBlock() != nil {
				blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
				consensus.AlertHandlers.SendInsufficientParticipation(context.Background(), len(buddyInputs), config.MaxMainPeers, blockHash)
			}
			// Note: We continue anyway, but BFT may fail
		} else {
			fmt.Printf("✅ Sufficient participation: %d/%d minimum required for consensus\n", len(buddyInputs), config.MaxMainPeers)
		}

		// Request vote aggregation results from all buddy nodes
		fmt.Println("Requesting vote results from all buddy nodes:", listenerNode.BuddyNodes.Buddies_Nodes)

		var wg sync.WaitGroup
		// collect BLS responses from buddies to include in block broadcast
		var blsResultsMu sync.Mutex
		blsResults := make([]BLS_Signer.BLSresponse, 0, config.MaxMainPeers)
		for _, buddyID := range listenerNode.BuddyNodes.Buddies_Nodes {
			wg.Add(1)
			go func(peerID peer.ID) {
				defer wg.Done()
				stream, err := consensus.Host.NewStream(context.Background(), peerID, config.SubmitMessageProtocol)
				if err != nil {
					// ALERTS_TODO: Remove alert after monitoring period (temporary)
					if consensus.AlertHandlers != nil {
						consensus.AlertHandlers.SendBroadcastVoteTriggerFailure(context.Background(), fmt.Sprintf("failed to open stream to %s: %v", peerID, err))
					}
					fmt.Printf("❌ Failed to open stream to %s: %v\n", peerID, err)
					return
				}
				defer stream.Close()

				// Request vote aggregation result from this buddy node
				reqAck := PubSubMessages.NewACKBuilder().True_ACK_Message(consensus.Host.ID(), config.Type_VoteResult)
				// Include block hash to scope vote aggregation
				requestPayload := map[string]string{
					"block_hash": consensus.ZKBlockData.GetZKBlock().BlockHash.String(),
				}
				requestPayloadBytes, _ := json.Marshal(requestPayload)

				reqMsg := PubSubMessages.NewMessageBuilder(nil).
					SetSender(consensus.Host.ID()).
					SetMessage(string(requestPayloadBytes)).
					SetTimestamp(time.Now().UTC().Unix()).
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
				case <-time.After(20 * time.Second):
					fmt.Printf("⏱️ Timeout waiting for response from %s\n", peerID)
					return
				}

				responseMsg := PubSubMessages.NewMessageBuilder(nil).DeferenceMessage(response)
				if responseMsg != nil {
					var resultData map[string]interface{}
					if err := json.Unmarshal([]byte(responseMsg.Message), &resultData); err == nil {
						// Pretty print full payload for debugging (includes BLS if present)
						if pretty, err := json.MarshalIndent(resultData, "", "  "); err == nil {
							fmt.Printf("📦 Full vote payload from %s:\n%s\n", peerID, string(pretty))
						}

						// Extract and store numeric vote
						if result, ok := resultData["result"].(float64); ok {
							Maps.StoreVoteResult(peerID.String(), int8(result))
							fmt.Printf("✅ Received vote result from %s: %d\n", peerID, int8(result))
						}

						// If BLS present, log key fields
						if blsAny, ok := resultData["bls"].(map[string]interface{}); ok {
							sig := ""
							if v, ok := blsAny["Signature"].(string); ok {
								sig = v
							}
							agree := false
							if v, ok := blsAny["Agree"].(bool); ok {
								agree = v
							}
							pub := ""
							if v, ok := blsAny["PubKey"].(string); ok {
								pub = v
							}
							pid := ""
							if v, ok := blsAny["PeerID"].(string); ok {
								pid = v
							}
							fmt.Printf("🔐 BLS from %s | peer=%s agree=%t pubkey_len=%d sig_len=%d\n", peerID, pid, agree, len(pub), len(sig))

							// append typed response
							blsResultsMu.Lock()
							blsResults = append(blsResults, *BLS_Signer.NewBLSresponseBuilder(nil).
								SetSignature(sig).
								SetAgree(agree).
								SetPubKey(pub).
								SetPeerID(pid).
								Build())
							blsResultsMu.Unlock()
						}
					}
				}
			}(buddyID)
		}
		wg.Wait()
		fmt.Printf("✅ Collected vote results from all buddy nodes\n")

		// Verify consensus: Check BLS signatures and ensure majority agree before processing
		consensusReached := false
		if len(blsResults) > 0 {
			validYes := 0
			validTotal := 0
			for _, r := range blsResults {
				// Verify signature for stated vote (+1 if Agree else -1)
				vote := int8(-1)
				if r.Agree {
					vote = 1
				}
				if err := BLS_Verifier.Verify(r, vote); err != nil {
					fmt.Printf("⚠️ BLS verification failed for peer %s: %v\n", r.PeerID, err)
					continue
				}
				validTotal++
				if vote == 1 {
					validYes++
				}
			}

			if validTotal == 0 {
				fmt.Printf("❌ No valid BLS signatures - consensus failed, skipping block processing\n")
				// ALERTS_TODO: Remove alert after monitoring period (temporary)
				if consensus.AlertHandlers != nil && consensus.ZKBlockData != nil && consensus.ZKBlockData.GetZKBlock() != nil {
					blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
					consensus.AlertHandlers.SendBLSFailure(context.Background(), blockHash, "No valid BLS signatures")
				}
			} else {
				needed := (validTotal / 2) + 1
				validNo := validTotal - validYes
				if validYes >= needed {
					consensusReached = true
					fmt.Printf("✅ Consensus reached: %d/%d votes in favor (needed: %d)\n", validYes, validTotal, needed)
					// ALERTS_TODO: Remove alert after monitoring period (temporary)
					if consensus.AlertHandlers != nil && consensus.ZKBlockData != nil && consensus.ZKBlockData.GetZKBlock() != nil {
						blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
						consensus.AlertHandlers.SendConsensusReachedAccept(context.Background(), blockHash, validYes, validNo, validTotal)
					}
				} else {
					fmt.Printf("❌ Consensus failed: %d/%d votes in favor (needed: %d) - skipping block processing\n", validYes, validTotal, needed)
					// ALERTS_TODO: Remove alert after monitoring period (temporary)
					if consensus.AlertHandlers != nil && consensus.ZKBlockData != nil && consensus.ZKBlockData.GetZKBlock() != nil {
						blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
						errMsg := fmt.Sprintf("Block %s: Only %d/%d votes in favor (needed: %d)", blockHash, validYes, validTotal, needed)
						consensus.AlertHandlers.SendConsensusFailed(context.Background(), errMsg)
					}
				}
			}
		} else {
			fmt.Printf("⚠️ No BLS results collected - cannot verify consensus, skipping block processing\n")
			// ALERTS_TODO: Remove alert after monitoring period (temporary)
			if consensus.AlertHandlers != nil && consensus.ZKBlockData != nil && consensus.ZKBlockData.GetZKBlock() != nil {
				blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
				consensus.AlertHandlers.SendBLSFailure(context.Background(), blockHash, "No BLS results collected")
			}
		}

		// After collecting votes, broadcast block with attached BLS results
		if consensus.ZKBlockData != nil && consensus.ZKBlockData.GetZKBlock() != nil {
			block := consensus.ZKBlockData.GetZKBlock()
			if err := messaging.BroadcastBlockToEveryNodeWithExtraData(consensus.Host, block, false, map[string]string{}, blsResults); err != nil {
				fmt.Printf("❌ Failed to broadcast block with BLS results: %v\n", err)
			} else {
				fmt.Printf("✅ Broadcasted block with %d BLS results\n", len(blsResults))

				// Only process block locally if consensus was reached
				// Pass BLS results for validation (ProcessBlockLocally will verify consensus)
				if consensusReached {
					if err := messaging.ProcessBlockLocally(block, blsResults); err != nil {
						fmt.Printf("❌ Failed to process block locally after broadcast: %v\n", err)
					} else {
						fmt.Printf("✅ Processed block locally - account balances updated\n")
					}
				} else {
					fmt.Printf("⚠️ Skipping local block processing - consensus not reached\n")
				}
			}
		} else {
			fmt.Printf("⚠️ Cannot broadcast block: ZKBlockData missing\n")
		}
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
