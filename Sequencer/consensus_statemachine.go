package Sequencer

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

	// Build the consensus instance first so the eligibility source can read this
	// round's ACTUAL voting committee (the main peers) at call time.
	c := &Consensus{
		PeerList:        peerList,
		Host:            host,
		Channel:         config.PubSub_ConsensusChannel,
		ResponseHandler: responseHandler,
		mu:              &sync.RWMutex{},
	}

	// Wire the committee eligibility source. The messaging verifier
	// subtracts the operator block_buddy blocklist and fails closed if this
	// source is absent or errors.
	//
	// Legacy source: the round's MAIN peers (the peers that actually vote), NOT
	// main+backup. This is critical for the 2f+1 threshold: with MaxMainPeers=7
	// main + MaxBackupPeers=5 backup, sourcing all 12 makes VerifyCertificate
	// require 2f+1 over 12 = 7, but only the 7 main peers vote — so any missing
	// main vote could never reach quorum. Read the live
	// MainPeers at call time (populated during Consensus.Start). Fall back to a
	// main-sized getBuddy query only if MainPeers isn't populated yet.
	// Legacy source carries NO peer_id↔bls_pub binding (empty values), so the
	// verifier enforces peer_id membership only — the key binding is available
	// only via the authenticated snapshot below.
	// (epoch, pinned) are ignored here: the legacy source is a per-node live view
	// with no epoch concept at all. That is exactly why v2 refuses to run on it
	// (ErrLegacySourceUnderV2) — seed-ranking a set the nodes already disagree
	// about would still diverge. A pinned request cannot be honoured by this
	// source, so require_pinned_committee must not be set alongside it.
	legacyBuddySource := func(_ uint64, _ bool) (map[string]string, error) {
		if main := c.PeerList.MainPeers; len(main) > 0 {
			set := make(map[string]string, len(main))
			for _, pid := range main {
				set[pid.String()] = ""
			}
			return set, nil
		}
		buddies, err := helper.QueryBuddyNodes()
		if err != nil {
			return nil, err
		}
		unique := helper.GetUniqueBuddyPeers(buddies)
		set := make(map[string]string)
		for i, b := range unique {
			if i >= config.MaxMainPeers { // scope to the voting-committee size
				break
			}
			set[b.PeerID.String()] = ""
		}
		return set, nil
	}

	// Committee-source: when the operator pins the seed authority key, the
	// eligible set is the seed-AUTHENTICATED epoch snapshot (verified against the
	// pinned authority), not the raw getBuddy list. Because eligibility IS the
	// committee, VerifyCertificate then enforces committee ⊆ snapshot at the
	// tally. Fail-closed: if the seed client can't be built we refuse rather than
	// fall back to an unauthenticated list.
	cfg := settings.Get()

	// Register this node's libp2p identity key to sign committee-selection
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
			messaging.SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
				return nil, fmt.Errorf("committee source: seed client init failed (fail closed): %w", initErr)
			})
		}
	} else {
		messaging.SetCommitteeEligibilitySource(legacyBuddySource)
	}

	return c
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

	// F1: the peers ASKED to vote must be the peers the verifier will COUNT.
	//
	// Without this, `buddies` is whatever the per-node VRF shuffle returned while
	// VerifyCertificateForRound tallies against messaging.SelectCommittee - two
	// sources, and at pool > k they disagree. Every seated member could sign and
	// the certificate would still fall short, because the signers were never the
	// seated ones. That is the observed halt.
	//
	// Under v2 the seated set decides. A seated member with no known multiaddr is
	// dropped from the wire list but still counts toward n, which is the correct
	// BFT reading: quorum is 2f+1 of the committee, not of whoever was reachable.
	if messaging.CommitteeV2Enabled {
		seated, selErr := messaging.SeatedPeerIDs(messaging.RoundContextForBlock(zkblock))
		if selErr != nil {
			// Fail closed. Falling back to the shuffle here would reintroduce the
			// exact two-source split this replaces.
			return fmt.Errorf("committee selection failed (fail closed): %w", selErr)
		}
		kept := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, len(buddies))
		reachable := make(map[string]struct{}, len(buddies))
		for _, b := range buddies {
			pid := b.PeerID.String()
			if _, ok := seated[pid]; !ok {
				continue
			}
			kept = append(kept, b)
			reachable[pid] = struct{}{}
		}
		if len(kept) < len(seated) {
			missing := make([]string, 0, len(seated)-len(reachable))
			for pid := range seated {
				if _, ok := reachable[pid]; !ok {
					missing = append(missing, pid)
				}
			}
			sort.Strings(missing)
			logger().NamedLogger.Warn(context.Background(),
				"committee v2: seated members have no known multiaddr and cannot be asked to vote",
				ion.Int("seated", len(seated)),
				ion.Int("dialable", len(kept)),
				ion.String("missing", strings.Join(missing, ",")),
				ion.String("function", "Consensus.SetZKBlockData"))
		}
		buddies = kept
	}

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

	if consensusReached {
		// Apply the block LOCALLY FIRST, and broadcast to peers only AFTER the
		// sequencer has durably committed it. Broadcasting before a successful
		// local apply let the fleet advance past the producer when the local
		// write failed (e.g. an immudb ExecAll timeout): the sequencer fell
		// behind its own chain and would build the next block on a stale parent,
		// diverging its state from the fleet. Withholding the block from peers on
		// local failure keeps producer and fleet consistent — the round is simply
		// retried at the same height.
		if err := messaging.ProcessBlockLocally(block, blsResults); err != nil {
			Alerts.NewAlertBuilder(alert_ctx).
				AlertName(helper.Alert_Consensus_ProcessBlockFailed_FailedToProcessBlockLocally).
				Status(Alerts.AlertStatusError).
				Severity(Alerts.SeverityError).
				Description("Failed to process block locally — block withheld from peers (not broadcast)").
				Msg(err.Error()).
				Label("block_number", fmt.Sprintf("%d", block.BlockNumber)).
				Label("block_hash", block.BlockHash.Hex()).
				Send()
			return fmt.Errorf("failed to process block locally; withheld from peers: %w", err)
		}

		// Local apply succeeded → propagate to peers. A broadcast failure here is
		// NOT fatal: the block is already committed and finalized on the producer,
		// and peers reconcile via gossip / sync catch-up. This is the safe
		// direction (producer ahead, fleet catches up), unlike the old order that
		// could leave the producer behind.
		if err := messaging.BroadcastBlockToEveryNodeWithExtraData(consensus.Host, block, consensusReached, extraData, blsResults); err != nil {
			logger().NamedLogger.Warn(ctx, "Block applied locally but broadcast to peers failed; peers will reconcile via catch-up",
				ion.String("error", err.Error()),
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.Int64("block_number", int64(block.BlockNumber)),
				ion.String("function", "Consensus.BroadcastAndProcessBlock"))
		}

		logger().NamedLogger.Info(ctx, "Applied block locally and broadcast to peers",
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
		// Broadcast the "rejected" status so nodes discard the block; it is never
		// applied locally. The alert from VerifyConsensusWithBLS already notifies
		// about the failed vote, so a broadcast error here is not propagated.
		if err := messaging.BroadcastBlockToEveryNodeWithExtraData(consensus.Host, block, consensusReached, extraData, blsResults); err != nil {
			logger().NamedLogger.Warn(ctx, "Failed to broadcast rejected-block notice to peers",
				ion.String("error", err.Error()),
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.Int64("block_number", int64(block.BlockNumber)),
				ion.String("function", "Consensus.BroadcastAndProcessBlock"))
		}
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
