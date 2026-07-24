package Sequencer

import (
	"context"
	"fmt"
	"sync"

	"gossipnode/AVC/BuddyNodes/MessagePassing"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/Pubsub"
	"gossipnode/Pubsub/Subscription"
	"gossipnode/Sequencer/Alerts"
	"gossipnode/Sequencer/Triggers/Maps"
	"gossipnode/Sequencer/helper"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/config/PubSubMessages/Cache"
	"gossipnode/config/settings"
	"gossipnode/messaging"
	"gossipnode/seednode"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// This file will maintain the state machine for the consensus
// this help to make code maintainable, understandable and durable in changing the states
// also help to make consensus to be idiomatic and properly locked and unlocked
// Use state machine design pattern to make the code more maintainable and understandable
type PeerList struct {
	MainPeers   []peer.ID
	BackupPeers []peer.ID
}
type Consensus struct {
	mu               *sync.RWMutex
	Channel          string
	PeerList         PeerList
	Host             host.Host
	gossipnode       *Pubsub.StructGossipPubSub
	ListenerNode     *MessagePassing.StructListener
	ResponseHandler  *ResponseHandler
	DiscoveryService *Service.NodeDiscoveryService
	ZKBlockData      *PubSubMessages.ConsensusMessage
	// Guards to prevent infinite loops
	isProcessingVotes  bool
	processedBlockHash string

	// lastRejectSummary is a SHORT one-line reason the most recent round failed
	// consensus; lastRejectDetail is an optional compact secondary line (e.g.
	// per-buddy rejection reasons). Both are set by VerifyConsensusWithBLS and
	// surfaced in the "block rejected" alert. Guarded by rejectMu.
	rejectMu          sync.Mutex
	lastRejectSummary string
	lastRejectDetail  string
}

// setRejectSummary records a short one-line reason (and optional compact detail)
// for why the current round failed consensus.
func (c *Consensus) setRejectSummary(summary, detail string) {
	c.rejectMu.Lock()
	c.lastRejectSummary = summary
	c.lastRejectDetail = detail
	c.rejectMu.Unlock()
}

// takeRejectSummary returns and clears the last recorded reason + detail.
func (c *Consensus) takeRejectSummary() (summary, detail string) {
	c.rejectMu.Lock()
	defer c.rejectMu.Unlock()
	summary, detail = c.lastRejectSummary, c.lastRejectDetail
	c.lastRejectSummary, c.lastRejectDetail = "", ""
	return
}

// @constructor function
/*
This function creates a new consensus instance.
What it does:
- Creates a new response handler
- Sets the peer list, host, channel, and response handler
*/
func NewConsensus(peerList PeerList, host host.Host) *Consensus {
	responseHandler := NewResponseHandler()

	// Wire the committee eligibility source (P1). The messaging verifier
	// subtracts the operator block_buddy blocklist and fails closed if this
	// source is absent or errors.
	//
	// Legacy source: the live seedNode buddy set (getBuddy/ListBuddy). Used when
	// no seed authority key is pinned (pre-committee-source deployments).
	// Legacy source carries NO peer_id↔bls_pub binding (empty values), so the
	// verifier enforces peer_id membership only — the M1 key binding is available
	// only via the authenticated snapshot below.
	legacyBuddySource := func() (map[string]string, error) {
		buddies, err := helper.QueryBuddyNodes()
		if err != nil {
			return nil, err
		}
		unique := helper.GetUniqueBuddyPeers(buddies)
		set := make(map[string]string, len(unique))
		for _, b := range unique {
			set[b.PeerID.String()] = ""
		}
		return set, nil
	}

	// Committee-source (O4): when the operator pins the seed authority key, the
	// eligible set is the seed-AUTHENTICATED epoch snapshot (verified against the
	// pinned authority), not the raw getBuddy list. Because eligibility IS the
	// committee, VerifyCertificate then enforces committee ⊆ snapshot at the
	// tally. Fail-closed: if the seed client can't be built we refuse rather than
	// fall back to an unauthenticated list.
	cfg := settings.Get()

	// S4: register this node's libp2p identity key to sign committee-selection
	// (ListBuddy) requests. NewConsensus is only invoked on the sequencer (the
	// Block server paths), so this node is the sequencer; the seed serves
	// selection only to the sequencer PeerID it has configured and refuses all
	// other callers.
	if host != nil {
		if sk := host.Peerstore().PrivKey(host.ID()); sk != nil {
			seednode.SetSequencerSignKey(sk)
		}
	}

	if pinned := cfg.Consensus.SeedAuthorityBLSPub; pinned != "" && cfg.Network.SeedNode != "" {
		if sc, err := seednode.NewClient(cfg.Network.SeedNode); err == nil {
			messaging.SetCommitteeEligibilitySource(sc.CommitteeEligibility(pinned, cfg.Consensus.CommitteeEpochSeconds))
		} else {
			initErr := err
			messaging.SetCommitteeEligibilitySource(func() (map[string]string, error) {
				return nil, fmt.Errorf("committee source: seed client init failed (fail closed): %w", initErr)
			})
		}
	} else {
		messaging.SetCommitteeEligibilitySource(legacyBuddySource)
	}

	return &Consensus{
		PeerList:        peerList,
		Host:            host,
		Channel:         config.PubSub_ConsensusChannel,
		ResponseHandler: responseHandler,
		mu:              &sync.RWMutex{},
	}
}

/*
This function warms up the consensus.
What it does:
- Check the issues with Consensus:host, peerlist.mainpeers, peerlist.backuppeers
- Initlize the loggers
- Clear the vote cache
- Clear the cache
- Query the buddy nodes from the NodeSelectionRouter
- Deduplicate by Buddy_PeerMultiaddr
*/
func (consensus *Consensus) warmup(ctx context.Context) ([]PubSubMessages.Buddy_PeerMultiaddr, error) {

	if consensus.Host == nil {
		return nil, fmt.Errorf("host is nil")
	}

	if consensus.PeerList.MainPeers == nil {
		return nil, fmt.Errorf("main peers list is nil")
	}

	if consensus.PeerList.BackupPeers == nil {
		return nil, fmt.Errorf("backup peers list is nil")
	}

	Maps.ClearVoteResults()
	Cache.ClearCache()

	logger().NamedLogger.Info(ctx, "Cleared previous round vote results at start of consensus round",
		ion.String("function", "Consensus.warmup"))

	buddies, errMSG := helper.QueryBuddyNodes()
	if errMSG != nil {
		return nil, fmt.Errorf("failed to query buddy nodes: %v", errMSG)
	}

	logger().NamedLogger.Info(ctx, "Queried buddy node candidates from NodeSelectionRouter",
		ion.Int("candidates", len(buddies)),
		ion.String("function", "Consensus.warmup"))

	// Deduplicate buddies by peer.ID (buddies may have multiple multiaddrs per peer)
	candidates := helper.GetUniqueBuddyPeers(buddies)

	logger().NamedLogger.Info(ctx, "got candidates after deduplication",
		ion.Int("candidates", len(candidates)),
		ion.String("function", "Consensus.warmup"))

	return candidates, nil
}

// State change function
/*
This function populates the peer list.
What it does:
- Populates the peer list with the main candidates and backup candidates
*/
func (consensus *Consensus) PopulatePeerList(MainCandidates []PubSubMessages.Buddy_PeerMultiaddr, BackupCandidates []PubSubMessages.Buddy_PeerMultiaddr) error {
	consensus.mu.Lock()
	defer consensus.mu.Unlock()

	// Clear the peer list
	consensus.PeerList.MainPeers = make([]peer.ID, 0, len(MainCandidates))
	consensus.PeerList.BackupPeers = make([]peer.ID, 0, len(BackupCandidates))

	for _, candidate := range MainCandidates {
		consensus.PeerList.MainPeers = append(consensus.PeerList.MainPeers, candidate.PeerID)
	}

	for _, candidate := range BackupCandidates {
		consensus.PeerList.BackupPeers = append(consensus.PeerList.BackupPeers, candidate.PeerID)
	}

	return nil
}

// State change function
/*
This function sets the gossipnode.
What it does:
- Sets the gossipnode with the channel
*/
func (consensus *Consensus) SetGossipnode(channel protocol.ID) error {
	consensus.mu.Lock()
	defer consensus.mu.Unlock()

	// Clear the gossipnode
	consensus.gossipnode = nil

	var err error
	consensus.gossipnode, err = Pubsub.NewGossipPubSub(consensus.Host, channel)
	if err != nil {
		return fmt.Errorf("failed to create pubsub: %v", err)
	}

	return nil
}

// State change function
/*
This function sets the zkblock data.
What it does:
- Sets the zkblock data with the zkblock and buddies
*/
func (consensus *Consensus) SetZKBlockData(zkblock *config.ZKBlock, buddies []PubSubMessages.Buddy_PeerMultiaddr) error {
	consensus.mu.Lock()
	defer consensus.mu.Unlock()

	// Clear the zkblock data
	consensus.ZKBlockData = nil

	var err error
	consensus.ZKBlockData, err = helper.AddBuddyNodesToPeerList(zkblock, buddies)
	if err != nil {
		return fmt.Errorf("failed to add buddy nodes to peer list: %v", err)
	}

	return nil
}

// State change function
/*
This function broadcasts the block with BLS results and processes it locally if consensus was reached.
What it does:
- Broadcasts block with attached BLS results to all nodes
- Processes block locally (updates account balances) if consensus was reached
- This is a state-changing operation as it modifies the blockchain state
- IMPORTANT: Cleans up subscriptions after processing to prevent resource leaks
*/
func (consensus *Consensus) BroadcastAndProcessBlock(ctx context.Context, blsResults []BLS_Signer.BLSresponse, consensusReached bool) error {
	// Context for the alerts
	alert_ctx := ctx
	defer alert_ctx.Done()

	// CRITICAL FIX: Clean up subscriptions when consensus round completes (success or failure)
	// This prevents subscription accumulation over long-running consensus operations
	defer consensus.CleanupSubscriptions(ctx)

	consensus.mu.Lock()
	defer consensus.mu.Unlock()

	if consensus.ZKBlockData == nil || consensus.ZKBlockData.GetZKBlock() == nil {
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_ProcessBlockFailed_ZKBlockDataNotSet).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description("ZKBlockData not initialized when attempting to broadcast and process block").
			Send()
		return fmt.Errorf("ZKBlockData not initialized")
	}

	block := consensus.ZKBlockData.GetZKBlock()

	// Determine extra data for broadcast
	extraData := map[string]string{}
	if !consensusReached {
		extraData["status"] = "rejected"
	} else {
		extraData["status"] = "accepted"
	}

	// Broadcast block with BLS results (if any)
	// If consensusReached is false, we send "rejected" status so nodes can discard the block
	if err := messaging.BroadcastBlockToEveryNodeWithExtraData(consensus.Host, block, consensusReached, extraData, blsResults); err != nil {
		return fmt.Errorf("failed to broadcast block with BLS results: %w", err)
	}

	if consensusReached {
		// Only process block locally if consensus was reached
		if err := messaging.ProcessBlockLocally(block, blsResults); err != nil {
			Alerts.NewAlertBuilder(alert_ctx).
				AlertName(helper.Alert_Consensus_ProcessBlockFailed_FailedToProcessBlockLocally).
				Status(Alerts.AlertStatusError).
				Severity(Alerts.SeverityError).
				Description("Failed to process block locally after successful broadcast").
				Msg(err.Error()).
				Label("block_number", fmt.Sprintf("%d", block.BlockNumber)).
				Label("block_hash", block.BlockHash.Hex()).
				Send()
			return fmt.Errorf("failed to process block locally after broadcast: %w", err)
		}

		logger().NamedLogger.Info(ctx, "Broadcasted block",
			ion.Int("bls_results", len(blsResults)),
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int64("block_number", int64(block.BlockNumber)),
			ion.String("function", "Consensus.BroadcastAndProcessBlock"))

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_ProcessBlockSuccess_BlockProcessedLocally).
			Status(Alerts.AlertStatusSuccess).
			Severity(Alerts.SeveritySuccess).
			Description("Block processed locally - account balances updated").
			Label("block_number", fmt.Sprintf("%d", block.BlockNumber)).
			Label("block_hash", block.BlockHash.Hex()).
			Send()
	} else {
		// Consensus not reached is a valid BFT outcome, not an infrastructure error.
		// The alert from VerifyConsensusWithBLS already notifies about the failed vote.
		// We broadcast with "rejected" status so nodes discard — no error to propagate.
		logger().NamedLogger.Info(ctx, "Broadcasted rejected block",
			ion.Int("bls_results", len(blsResults)),
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int64("block_number", int64(block.BlockNumber)),
			ion.String("function", "Consensus.BroadcastAndProcessBlock"))

		reason, detail := consensus.takeRejectSummary()
		if reason == "" {
			reason = "consensus not reached (no reason captured)"
		}
		// Consensus failure halts block production — this is an ERROR, not a
		// warning. Keep the Description a short headline and put the specifics in
		// labels (no Description+Msg concatenation, no duplicated reason).
		ab := Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_BlockRejectedByConsensus).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description("Consensus failed — block rejected (quorum not reached)").
			Label("block_number", fmt.Sprintf("%d", block.BlockNumber)).
			Label("block_hash", block.BlockHash.Hex()).
			Label("bls_results", fmt.Sprintf("%d", len(blsResults))).
			Label("reason", reason)
		if detail != "" {
			ab = ab.Label("buddy_rejections", detail)
		}
		ab.Send()
	}

	return nil
}

// CleanupSubscriptions unsubscribes from consensus-related topics to prevent resource leaks
// This should be called after each consensus round completes (success or failure)
func (consensus *Consensus) CleanupSubscriptions(ctx context.Context) {
	if consensus.gossipnode == nil {
		return
	}

	gps := consensus.gossipnode.GetGossipPubSub()
	if gps == nil {
		return
	}

	// Unsubscribe from consensus channel
	if err := Subscription.Unsubscribe(gps, config.PubSub_ConsensusChannel); err != nil {
		logger().NamedLogger.Warn(ctx, "Failed to unsubscribe from consensus channel",
			ion.String("error", err.Error()),
			ion.String("function", "Consensus.CleanupSubscriptions"))
	} else {
		logger().NamedLogger.Info(ctx, "Cleaned up consensus channel subscription",
			ion.String("function", "Consensus.CleanupSubscriptions"))
	}

	// Unsubscribe from CRDT sync channel
	if err := Subscription.Unsubscribe(gps, config.Pubsub_CRDTSync); err != nil {
		// This may fail if we never subscribed - that's OK
		logger().NamedLogger.Debug(ctx, "Failed to unsubscribe from CRDT sync channel (may not have been subscribed)",
			ion.String("error", err.Error()),
			ion.String("function", "Consensus.CleanupSubscriptions"))
	}
}
