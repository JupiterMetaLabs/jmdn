package Sequencer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/Pubsub"
	"gossipnode/Sequencer/Alerts"
	"gossipnode/Sequencer/Triggers/Maps"
	"gossipnode/Sequencer/common"
	"gossipnode/Sequencer/helper"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/messaging"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// ConnectedNessCheck checks connectedness of candidates and returns connected peers
// It stops checking once it has found enough peers (maxPeers), or after checking all candidates
func (consensus *Consensus) ConnectedNessCheck(candidates []PubSubMessages.Buddy_PeerMultiaddr, maxPeers int) (map[peer.ID]multiaddr.Multiaddr, error) {
	if candidates == nil {
		return nil, fmt.Errorf("CONNECTEDNESSCHECK: candidates are nil")
	}
	if maxPeers <= 0 {
		return nil, fmt.Errorf("CONNECTEDNESSCHECK: maxPeers must be greater than 0")
	}

	reachablePeers := make(map[peer.ID]multiaddr.Multiaddr, maxPeers)
	for _, candidate := range candidates {
		if len(reachablePeers) >= maxPeers {
			break // We have enough peers
		}
		connectedness := consensus.Host.Network().Connectedness(candidate.PeerID)
		if connectedness == network.Connected {
			reachablePeers[candidate.PeerID] = candidate.Multiaddr
			log.Printf("✅ Buddy node %s is actually connected (status: %v)", candidate.PeerID.String()[:16], connectedness)
		}
	}
	return reachablePeers, nil
}

func (consensus *Consensus) Start(zkblock *config.ZKBlock) error {
	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			return fmt.Errorf("CONSENSUSERROR.START: failed to initialize local gro: %v", err)
		}
	}
	// Context for the alerts
	alert_ctx := context.Background()
	defer alert_ctx.Done()

	// Warmup the consensus
	/*
		What it does:
		- Check the issues with Consensus:host, peerlist.mainpeers, peerlist.backuppeers
		- Initlize the loggers
		- Clear the vote cache
		- Query the buddy nodes from the NodeSelectionRouter
		- Deduplicate by Buddy_PeerMultiaddr
	*/
	candidates, errMSG := consensus.warmup()
	if errMSG != nil {
		return fmt.Errorf("CONSENSUSERROR.WARMUP: failed to warmup consensus: %v", errMSG)
	}

	// Connect to the candidates first via AddPeerCache
	stats := helper.AddBuddyNodesTemporarily(candidates)
	if stats.GetReachablePeers() == nil {
		return fmt.Errorf("CONSENSUSERROR.ADDPEERSCACHE: failed to add peers to cache: %v", stats.GetUnreachablePeers())
	}

	// Debugging
	log.Printf("CONSENSUSINFO.ADDPEERSCACHE: reachable peers: %v", stats.GetReachablePeers())
	log.Printf("CONSENSUSINFO.ADDPEERSCACHE: unreachable peers: %v", stats.GetUnreachablePeers())
	log.Printf("CONSENSUSINFO.ADDPEERSCACHE: total peers: %d", stats.GetTotalPeers())
	log.Printf("CONSENSUSINFO.ADDPEERSCACHE: time taken: %v", stats.GetTimeTaken())

	// Now connect to final buddy nodes - ONLY main candidates first, backup only if needed
	// This prevents excessive connections to backup nodes when we already have enough main peers
	// IMPORTANT: We must also discover backup candidates. If we only check up to
	// `MaxMainPeers`, `BackupCandidates` will always be empty and the subscription
	// fallback will log "No backup peers available" even when extra candidates exist.
	maxPeersToCheck := config.MaxMainPeers + config.MaxBackupPeers
	reachablePeers, errMSG := consensus.ConnectedNessCheck(
		helper.ConvertMapToSlice(stats.GetReachablePeers()),
		maxPeersToCheck,
	)
	if errMSG != nil {
		return fmt.Errorf("CONSENSUSERROR.CONNECTEDNESSCHECK: failed to verify connectedness: %v", errMSG)
	}

	log.Printf("Verified: %d peers are actually connected out of %d reachable candidates",
		len(reachablePeers), len(candidates))

	// Step 3: Split into Main and Backup based on first MaxMainPeers connected peers
	// Preserve order from original candidates list (first-come-first-served)
	MainCandidates, BackupCandidates := helper.InitCandidateLists(len(candidates))
	mainCount := 0

	// Iterate through original candidates to preserve order, but only include connected ones
	for _, candidate := range candidates {
		if _, isConnected := reachablePeers[candidate.PeerID]; isConnected {
			if mainCount < config.MaxMainPeers {
				MainCandidates = append(MainCandidates, candidate)
				mainCount++
			} else {
				BackupCandidates = append(BackupCandidates, candidate)
			}
		}
	}

	if len(MainCandidates) < config.MaxMainPeers {

		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.POPULATEPEERLIST: insufficient actually connected peers: got %d, need exactly %d (MaxMainPeers). Connected: %d, Unreachable: %d",
			len(MainCandidates), config.MaxMainPeers, len(reachablePeers), len(stats.GetUnreachablePeers()))

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_InsufficientPeers).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()

		return fmt.Errorf("%s", ErrorMessage)
	}

	log.Printf("Split into %d main candidates and %d backup candidates",
		len(MainCandidates), len(BackupCandidates))

	// Populate consensus.PeerList directly from MainCandidates and BackupCandidates
	errMSG = consensus.PopulatePeerList(MainCandidates, BackupCandidates)
	if errMSG != nil {
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.POPULATEPEERLIST: failed to populate peer list: %v", errMSG)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToPopulatePeerList).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	// add apeer ids, to message
	peerIDs := make([]string, 0, len(consensus.PeerList.MainPeers))
	for _, peerID := range consensus.PeerList.MainPeers {
		peerIDs = append(peerIDs, fmt.Sprintf("  - %s", peerID.String()))
	}
	msg := fmt.Sprintf("Final buddy nodes: %d MaxMainPeers actually connected peers with peerids:\n%s",
		len(consensus.PeerList.MainPeers), strings.Join(peerIDs, "\n"))

	Alerts.NewAlertBuilder(alert_ctx).
		AlertName(helper.Alert_Consensus_BuiltFinalBuddiesList).
		Status(Alerts.AlertStatusInfo).
		Severity(Alerts.SeverityInfo).
		Description(msg).
		Send()

	log.Printf("%s", msg)

	// Create ConsensusMessage with ONLY the final connected buddy nodes
	errMSG = consensus.SetZKBlockData(zkblock, MainCandidates)
	if errMSG != nil {
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.SETZKBLOCKDATA: failed to set zkblock data: %v", errMSG)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToSetZKBlockData).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	// Validate consensus configuration
	if err := ValidateConsensusConfiguration(consensus); err != nil {
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.VALIDATECONSENSUSCONFIGURATION: invalid consensus configuration: %v", err)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToValidateConsensusConfiguration).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	// Create allowed peers list (1 creator + MaxMainPeers main peers + backup peers for fallback)
	// Include backup peers so they can subscribe if main peers fail to respond
	allowedPeers := make([]peer.ID, 0, config.MaxMainPeers+config.MaxBackupPeers+1)
	allowedPeers = append(allowedPeers, consensus.Host.ID())
	allowedPeers = append(allowedPeers, consensus.PeerList.MainPeers...)
	allowedPeers = append(allowedPeers, consensus.PeerList.BackupPeers...)

	log.Printf("Creating pubsub channel with %d allowed peers (1 creator + %d main + %d backup buddy nodes)",
		len(allowedPeers), len(consensus.PeerList.MainPeers), len(consensus.PeerList.BackupPeers))

	err := consensus.SetGossipnode(config.PubSub_ConsensusChannel)
	if err != nil {
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.SETGOSSIPNODE: failed to set gossipnode: %v", err)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToSetGossipnode).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.PubSub_ConsensusChannel, false, allowedPeers); err != nil {
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.CREATECHANNEL: failed to create pubsub channel: %v", err)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToCreatePubsubChannel).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}
	log.Printf("Successfully created pubsub channel: %s", config.PubSub_ConsensusChannel)

	// Create the CRDT sync channel ONLY for final buddy nodes (vote aggregators) to synchronize their CRDTs
	// This channel is restricted to only the final connected buddy nodes
	// Regular network nodes cannot join - only the 4 buddy nodes performing vote aggregation can access it
	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.Pubsub_CRDTSync, false, allowedPeers); err != nil {
		if err.Error() != fmt.Sprintf("channel %s already exists", config.Pubsub_CRDTSync) {
			log.Printf("⚠️ Failed to create CRDT sync channel: %v (continuing anyway)", err)
		} else {
			log.Printf("✅ CRDT sync channel already exists: %s", config.Pubsub_CRDTSync)
		}
	} else {
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
		log.Printf("⚠️ Failed to subscribe sequencer to consensus channel: %v", err)
	} else {
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

	// Event-driven flow: Request subscriptions → Verify → Broadcast votes → Process CRDT
	// This avoids race conditions by ensuring each step completes before the next starts
	// Step 1 MUST be synchronous so upstream APIs can return 500 on failure instead
	// of returning 200 while consensus fails asynchronously.
	if err := consensus.RequestSubscriptionPermission(); err != nil {
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.REQUESTSUBSCRIPTIONPERMISSION: Failed to request subscription permission: %v", err)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToRequestSubscriptionPermission).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		log.Printf("%s", ErrorMessage)
		return fmt.Errorf("%s", ErrorMessage)
	}

	common.LocalGRO.Go(GRO.SequencerConsensusThread, func(ctx context.Context) error {
		consensus.startEventDrivenFlowAfterSubscriptionPermission()
		return nil
	})

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

	log.Printf("Successfully obtained subscription permission: 1 creator + %d MaxMainPeers subscribers", config.MaxMainPeers)
	return nil
}

// startEventDrivenFlowAfterSubscriptionPermission orchestrates the consensus flow after
// subscription permission has already been granted.
//
// Flow: Verify subscriptions → Broadcast vote trigger → Process CRDT
func (consensus *Consensus) startEventDrivenFlowAfterSubscriptionPermission() {
	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			log.Printf("CONSENSUSERROR.START: failed to initialize local gro: %v", err)
			return
		}
	}
	// Context for the alerts
	alert_ctx := context.Background()
	defer alert_ctx.Done()

	log.Printf("=== [Event-Driven Flow] Starting consensus flow ===\n")

	// Step 2: Verify subscriptions (with retry mechanism)
	log.Printf("=== [Event-Driven Flow] Step 2: Verifying subscriptions ===\n")
	maxRetries := 3
	retryDelay := 2 * time.Second
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := consensus.VerifySubscriptions(); err != nil {
			if attempt < maxRetries {
				log.Printf("=== [Event-Driven Flow] Verification attempt %d/%d failed, retrying in %v: %v ===\n",
					attempt, maxRetries, retryDelay, err)
				time.Sleep(retryDelay)
				continue
			}
			log.Printf("=== [Event-Driven Flow] ERROR: Subscription verification failed after %d attempts: %v ===\n",
				maxRetries, err)
			// Continue anyway - some peers might still be subscribing
			log.Printf("=== [Event-Driven Flow] Continuing with vote trigger despite verification issues ===\n")
		} else {
			log.Printf("=== [Event-Driven Flow] Step 2: Subscriptions verified successfully ===\n")
			break
		}
	}

	// Step 3: Broadcast vote trigger (only after subscriptions are verified/attempted)
	log.Printf("=== [Event-Driven Flow] Step 3: Broadcasting vote trigger ===\n")
	if consensus.ZKBlockData == nil {
		ErrorMessage := "CONSENSUSERROR.BROADCASTVOTETRIGGER: ZKBlockData not set, cannot broadcast vote trigger"
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_ZKBlockDataNotSet).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()

		log.Printf("%s", ErrorMessage)
		return
	}

	if err := consensus.BroadcastVoteTrigger(); err != nil {
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.BROADCASTVOTETRIGGER: BroadcastVoteTrigger failed: %v", err)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToBroadcastVoteTrigger).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		log.Printf("%s", ErrorMessage)

		return
	}
	log.Printf("=== [Event-Driven Flow] Step 3: Vote trigger broadcast successfully ===\n")

	// Step 4: Wait for votes to be collected and processed, then print CRDT state and process votes
	// This is triggered after vote collection completes (handled by vote collection mechanism)
	// For now, we'll use a reasonable delay but this should ideally be event-driven from vote collection
	log.Printf("=== [Event-Driven Flow] Step 4: Waiting for votes to be collected and processed ===\n")
	log.Printf("=== [Event-Driven Flow] Note: CRDT print and vote processing will be triggered after vote collection completes ===\n")

	// TODO: Replace this with actual event-driven trigger from vote collection completion
	// For now, use a delay but log that it should be event-driven
	common.LocalGRO.Go(GRO.SequencerRequestEventDrivenFlowThread, func(ctx context.Context) error {
		// Wait for votes to be collected (this should be replaced with event-driven trigger)
		time.Sleep(30 * time.Second)
		log.Printf("=== [Event-Driven Flow] Step 4a: Triggering CRDT state print ===\n")
		if err := consensus.PrintCRDTState(); err != nil {
			log.Printf("=== [Event-Driven Flow] ERROR: PrintCRDTState failed: %v ===\n", err)
		} else {
			log.Printf("=== [Event-Driven Flow] Step 4a: CRDT state printed successfully ===\n")
		}

		log.Printf("=== [Event-Driven Flow] Step 4b: Triggering vote collection and processing ===\n")
		if err := consensus.ProcessVoteCollection(); err != nil {
			log.Printf("=== [Event-Driven Flow] ERROR: ProcessVoteCollection failed: %v ===\n", err)
		} else {
			log.Printf("=== [Event-Driven Flow] Step 4b: Vote collection and processing initiated successfully ===\n")
		}
		return nil
	})
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

// PrintCRDTState prints the current state of the CRDT (read-only operation)
func (consensus *Consensus) PrintCRDTState() error {
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		return fmt.Errorf("listener node or CRDT layer not initialized")
	}

	if consensus.ZKBlockData == nil || consensus.ZKBlockData.GetZKBlock() == nil {
		return fmt.Errorf("ZKBlockData not initialized")
	}

	consensus.printCRDTHeader(listenerNode)
	consensus.printCRDTVotes(listenerNode)
	consensus.printCRDTFooter()

	return nil
}

// printCRDTHeader prints the header information for CRDT state
func (consensus *Consensus) printCRDTHeader(listenerNode *PubSubMessages.BuddyNode) {
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
}

// printCRDTVotes prints vote information from CRDT
func (consensus *Consensus) printCRDTVotes(listenerNode *PubSubMessages.BuddyNode) {
	votes, exists := MessagePassing.GetVotesFromCRDT(listenerNode.CRDTLayer, "vote")
	if !exists || len(votes) == 0 {
		fmt.Printf("\n📊 Votes in CRDT: 0 (no votes collected yet)\n")
		return
	}

	fmt.Printf("\n📊 Total Votes in CRDT: %d\n", len(votes))
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	yesVotes := 0
	noVotes := 0

	for i, vote := range votes {
		var voteData map[string]interface{}
		if err := json.Unmarshal([]byte(vote), &voteData); err != nil {
			fmt.Printf("  Vote %d: [PARSING ERROR] %s\n", i+1, vote)
			continue
		}

		voteValue := voteData["vote"]
		blockHash := voteData["block_hash"]

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

// printCRDTFooter prints the footer for CRDT state
func (consensus *Consensus) printCRDTFooter() {
	fmt.Printf("╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")
}

// ProcessVoteCollection orchestrates the vote collection and processing flow
// This manages the state flag and coordinates vote collection, verification, and block processing
func (consensus *Consensus) ProcessVoteCollection() error {
	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			log.Printf("CONSENSUSERROR.PROCESSVOTECOLLECTION: failed to initialize local gro: %v", err)
			return fmt.Errorf("CONSENSUSERROR.PROCESSVOTECOLLECTION: failed to initialize local gro: %v", err)
		}
	}
	if consensus.ZKBlockData == nil || consensus.ZKBlockData.GetZKBlock() == nil {
		return fmt.Errorf("ZKBlockData not initialized")
	}

	// Guard against concurrent calls for the same block (single atomic check)
	currentBlockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
	consensus.mu.Lock()
	if consensus.isProcessingVotes && consensus.processedBlockHash == currentBlockHash {
		consensus.mu.Unlock()
		fmt.Printf("⚠️ ProcessVoteCollection: Vote processing already in progress for block %s - skipping duplicate call\n", currentBlockHash)
		return nil
	}
	consensus.isProcessingVotes = true
	consensus.processedBlockHash = currentBlockHash
	consensus.mu.Unlock()

	// Ensure we unlock even if function returns early
	defer func() {
		consensus.mu.Lock()
		consensus.isProcessingVotes = false
		consensus.mu.Unlock()
	}()

	// Process vote collection asynchronously
	common.LocalGRO.Go(GRO.SequencerVoteCollectionThread, func(ctx context.Context) error {
		listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
		if listenerNode == nil {
			fmt.Printf("❌ Listener node not available for vote collection\n")
			return nil
		}

		// Step 1: Collect vote results from buddy nodes
		blsResults := consensus.CollectVoteResultsFromBuddies(listenerNode)

		// Step 2: Verify consensus with BLS signatures
		consensusReached := consensus.VerifyConsensusWithBLS(blsResults)

		// Step 3: Broadcast and process block (state-changing operation)
		if err := consensus.BroadcastAndProcessBlock(blsResults, consensusReached); err != nil {
			ErrorMessage := fmt.Sprintf("CONSENSUSERROR.BROADCASTANDPROCESSBLOCK: Failed to broadcast and process block: %v", err)
			fmt.Printf("%s", ErrorMessage)
			return nil
		}
		return nil
	})

	return nil
}

// CollectVoteResultsFromBuddies collects vote aggregation results from all buddy nodes
// Returns BLS results from buddy nodes
func (consensus *Consensus) CollectVoteResultsFromBuddies(listenerNode *PubSubMessages.BuddyNode) []BLS_Signer.BLSresponse {
	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			log.Printf("CONSENSUSERROR.COLLECTVOTERESULTSFROMBUDDIES: failed to initialize local gro: %v", err)
			return nil
		}
	}
	log.Printf("Requesting vote aggregation results from %d buddy nodes", len(listenerNode.BuddyNodes.Buddies_Nodes))

	wg, err := common.LocalGRO.NewFunctionWaitGroup(context.Background(), GRO.SequencerVoteCollectionWaitGroup)
	if err != nil {
		log.Printf("CONSENSUSERROR.COLLECTVOTERESULTSFROMBUDDIES: failed to create function wait group: %v", err)
		return nil
	}

	var blsResultsMu sync.Mutex
	blsResults := make([]BLS_Signer.BLSresponse, 0, config.MaxMainPeers)

	for _, buddyID := range listenerNode.BuddyNodes.Buddies_Nodes {
		common.LocalGRO.Go(GRO.SequencerVoteCollectionThread, func(ctx context.Context) error {
			blsResult := consensus.requestVoteResultFromBuddy(buddyID)
			if blsResult != nil {
				blsResultsMu.Lock()
				blsResults = append(blsResults, *blsResult)
				blsResultsMu.Unlock()
			}
			return nil
		}, local.AddToWaitGroup(GRO.SequencerVoteCollectionWaitGroup))
	}

	wg.Wait()
	fmt.Printf("✅ Collected vote results from all buddy nodes\n")
	return blsResults
}

// requestVoteResultFromBuddy requests vote result from a single buddy node
// Returns BLS response if successful, nil otherwise
func (consensus *Consensus) requestVoteResultFromBuddy(peerID peer.ID) *BLS_Signer.BLSresponse {
	stream, err := consensus.Host.NewStream(context.Background(), peerID, config.SubmitMessageProtocol)
	if err != nil {
		fmt.Printf("❌ Failed to open stream to %s: %v\n", peerID, err)
		return nil
	}
	defer stream.Close()

	// Build request message
	reqAck := PubSubMessages.NewACKBuilder().True_ACK_Message(consensus.Host.ID(), config.Type_VoteResult)
	requestPayload := map[string]string{
		"block_hash": consensus.ZKBlockData.GetZKBlock().BlockHash.String(),
	}
	requestPayloadBytes, err := json.Marshal(requestPayload)
	if err != nil {
		fmt.Printf("❌ Failed to marshal request payload for %s: %v\n", peerID, err)
		return nil
	}

	reqMsg := PubSubMessages.NewMessageBuilder(nil).
		SetSender(consensus.Host.ID()).
		SetMessage(string(requestPayloadBytes)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(reqAck)

	reqData, err := json.Marshal(reqMsg)
	if err != nil {
		fmt.Printf("❌ Failed to marshal request message for %s: %v\n", peerID, err)
		return nil
	}

	if _, err := stream.Write([]byte(string(reqData) + string(rune(config.Delimiter)))); err != nil {
		fmt.Printf("❌ Failed to write request to %s: %v\n", peerID, err)
		return nil
	}
	fmt.Printf("📨 Sent vote result request to %s\n", peerID)

	// Read response with timeout
	response := consensus.readVoteResultResponse(stream, peerID)
	if response == "" {
		return nil
	}

	// Parse response and extract BLS result
	return consensus.parseVoteResultResponse(response, peerID)
}

// readVoteResultResponse reads vote result response from stream with timeout
func (consensus *Consensus) readVoteResultResponse(stream network.Stream, peerID peer.ID) string {
	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			log.Printf("CONSENSUSERROR.READVOTERESULTRESPONSE: failed to initialize local gro: %v", err)
			return ""
		}
	}
	responseCh := make(chan string, 1)
	errCh := make(chan error, 1)
	reader := bufio.NewReader(stream)

	common.LocalGRO.Go(GRO.SequencerVoteCollectionThread, func(ctx context.Context) error {
		response, err := reader.ReadString(config.Delimiter)
		if err != nil {
			errCh <- err
			return err
		}
		responseCh <- response
		return nil
	})

	select {
	case resp := <-responseCh:
		return resp
	case err := <-errCh:
		fmt.Printf("⚠️ Failed to read response from %s: %v\n", peerID, err)
		return ""
	case <-time.After(45 * time.Second):
		fmt.Printf("⏱️ Timeout waiting for response from %s\n", peerID)
		return ""
	}
}

// parseVoteResultResponse parses vote result response and extracts BLS result
func (consensus *Consensus) parseVoteResultResponse(response string, peerID peer.ID) *BLS_Signer.BLSresponse {

	responseMsg := PubSubMessages.NewMessageBuilder(nil).DeferenceMessage(response)
	if responseMsg == nil {
		return nil
	}

	var resultData map[string]interface{}
	if err := json.Unmarshal([]byte(responseMsg.Message), &resultData); err != nil {
		return nil
	}

	// Pretty print full payload for debugging
	if pretty, err := json.MarshalIndent(resultData, "", "  "); err == nil {
		fmt.Printf("📦 Full vote payload from %s:\n%s\n", peerID, string(pretty))
	}

	// Extract and store numeric vote
	if result, ok := resultData["result"].(float64); ok {
		Maps.StoreVoteResult(peerID.String(), int8(result))
		fmt.Printf("✅ Received vote result from %s: %d\n", peerID, int8(result))
	}

	// Extract BLS if present
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
		msg := fmt.Sprintf("🔐 BLS from %s | peer=%s agree=%t pubkey_len=%d sig_len=%d", peerID, pid, agree, len(pub), len(sig))
		fmt.Printf("%s", msg)

		return BLS_Signer.NewBLSresponseBuilder(nil).
			SetSignature(sig).
			SetAgree(agree).
			SetPubKey(pub).
			SetPeerID(pid).
			Build()
	}

	return nil
}

// VerifyConsensusWithBLS verifies BLS signatures and determines if consensus was reached
// Returns true if consensus reached (majority agree), false otherwise
func (consensus *Consensus) VerifyConsensusWithBLS(blsResults []BLS_Signer.BLSresponse) bool {
	// Context for the alerts
	alert_ctx := context.Background()
	defer alert_ctx.Done()

	if len(blsResults) == 0 {
		fmt.Printf("⚠️ No BLS results collected - cannot verify consensus, skipping block processing\n")
		return false
	}

	validYes := 0
	validTotal := 0
	var votedPeers []string // Track peer IDs with their votes

	for _, r := range blsResults {
		vote := int8(-1)
		if r.Agree {
			vote = 1
		}
		if err := BLS_Verifier.Verify(r, vote); err != nil {
			fmt.Printf("⚠️ BLS verification failed for peer %s: %v\n", r.PeerID, err)
			continue
		}
		validTotal++
		votedPeers = append(votedPeers, fmt.Sprintf("  - %s (vote: %d)", r.PeerID, vote))
		if vote == 1 {
			validYes++
		}
	}

	if validTotal == 0 {
		msg := "❌ No valid BLS signatures - consensus failed, skipping block processing - No BLS results collected"
		fmt.Printf("%s", msg)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_BFT_Consensus_NoBLSResultsCollected).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(msg).
			Send()
		return false
	}

	needed := (validTotal / 2) + 1
	peerVotesStr := strings.Join(votedPeers, "\n")

	if validYes >= needed {
		msg := fmt.Sprintf("✅ BFT Consensus Reached: %d/%d votes in favor (needed: %d)\nPeer votes:\n%s", validYes, validTotal, needed, peerVotesStr)
		fmt.Printf("%s", msg)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_BFT_Consensus_Reached).
			Status(Alerts.AlertStatusInfo).
			Severity(Alerts.SeverityInfo).
			Description(msg).
			Send()
		return true
	}

	msg := fmt.Sprintf("❌ Consensus failed: %d/%d votes in favor (needed: %d) - skipping block processing\nPeer votes:\n%s", validYes, validTotal, needed, peerVotesStr)
	fmt.Printf("%s", msg)
	Alerts.NewAlertBuilder(alert_ctx).
		AlertName(helper.Alert_BFT_Consensus_Failed).
		Status(Alerts.AlertStatusError).
		Severity(Alerts.SeverityError).
		Description(msg).
		Send()
	return false
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
