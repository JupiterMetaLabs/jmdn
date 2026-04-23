package Sequencer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

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

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"go.opentelemetry.io/otel/attribute"
)

// ConnectedNessCheck checks connectedness of candidates and returns connected peers
// It stops checking once it has found enough peers (maxPeers), or after checking all candidates
func (consensus *Consensus) ConnectedNessCheck(candidates []PubSubMessages.Buddy_PeerMultiaddr, maxPeers int) (map[peer.ID]multiaddr.Multiaddr, error) {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.ConnectedNessCheck")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.Int("max_peers", maxPeers),
		attribute.Int("candidates_count", len(candidates)),
	)

	logger().Info(trace_ctx, "Checking connectedness of candidates",
		ion.Int("max_peers", maxPeers),
		ion.Int("candidates_count", len(candidates)),
		ion.String("function", "Consensus.ConnectedNessCheck"))

	if candidates == nil {
		err := fmt.Errorf("CONNECTEDNESSCHECK: candidates are nil")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return nil, err
	}
	if maxPeers <= 0 {
		err := fmt.Errorf("CONNECTEDNESSCHECK: maxPeers must be greater than 0")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return nil, err
	}

	reachablePeers := make(map[peer.ID]multiaddr.Multiaddr, maxPeers)
	for _, candidate := range candidates {
		if len(reachablePeers) >= maxPeers {
			break // We have enough peers
		}
		connectedness := consensus.Host.Network().Connectedness(candidate.PeerID)
		if connectedness == network.Connected {
			reachablePeers[candidate.PeerID] = candidate.Multiaddr
			logger().Info(trace_ctx, "Buddy node is actually connected",
				ion.String("peer_id", candidate.PeerID.String()),
				ion.String("connectedness", connectedness.String()),
				ion.String("function", "Consensus.ConnectedNessCheck"))
		}
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Int("reachable_peers_count", len(reachablePeers)),
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "Connectedness check completed",
		ion.Int("reachable_peers", len(reachablePeers)),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.ConnectedNessCheck"))

	return reachablePeers, nil
}

func (consensus *Consensus) Start(zkblock *config.ZKBlock) error {
	// Create root context for the entire consensus process
	rootCtx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, rootSpan := tracer.Start(rootCtx, "Consensus.Start")
	// NOTE: We do NOT defer rootSpan.End() here because the goroutine needs to end it
	// when it completes. The root span will be ended in startEventDrivenFlowAfterSubscriptionPermission

	startTime := time.Now().UTC()
	rootSpan.SetAttributes(
		attribute.Int64("block_number", int64(zkblock.BlockNumber)),
		attribute.String("block_hash", zkblock.BlockHash.Hex()),
		attribute.Int("tx_count", len(zkblock.Transactions)),
	)

	logger().Info(trace_ctx, "Starting consensus process",
		ion.Int64("block_number", int64(zkblock.BlockNumber)),
		ion.String("block_hash", zkblock.BlockHash.Hex()),
		ion.Int("tx_count", len(zkblock.Transactions)),
		ion.String("function", "Consensus.Start"))

	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			rootSpan.RecordError(err)
			rootSpan.SetAttributes(attribute.String("status", "gro_init_failed"))
			logger().Error(trace_ctx, "Failed to initialize local gro",
				err,
				ion.String("function", "Consensus.Start"))
			return fmt.Errorf("CONSENSUSERROR.START: failed to initialize local gro: %v", err)
		}
		rootSpan.SetAttributes(attribute.Bool("gro_initialized", true))
	}

	// Context for the alerts - will be cancelled when Start() returns
	alert_ctx, alert_cancel := context.WithCancel(trace_ctx)
	defer alert_cancel()

	// Create logger_ctx for synchronous operations in Start()
	// This is used for synchronous operations that complete before Start() returns
	logger_ctx := trace_ctx

	// Warmup the consensus
	warmupCtx, warmupSpan := tracer.Start(trace_ctx, "Consensus.Start.warmup")
	warmupStartTime := time.Now().UTC()
	logger().Info(warmupCtx, "Starting consensus warmup",
		ion.String("function", "Consensus.Start.warmup"))

	candidates, errMSG := consensus.warmup()
	if errMSG != nil {
		warmupSpan.RecordError(errMSG)
		warmupSpan.SetAttributes(attribute.String("status", "failed"))
		warmupDuration := time.Since(warmupStartTime).Seconds()
		warmupSpan.SetAttributes(attribute.Float64("duration", warmupDuration))
		logger().Error(warmupCtx, "Failed to warmup consensus",
			errMSG,
			ion.Float64("duration", warmupDuration),
			ion.String("function", "Consensus.Start.warmup"))
		warmupSpan.End()
		return fmt.Errorf("CONSENSUSERROR.WARMUP: failed to warmup consensus: %v", errMSG)
	}
	warmupDuration := time.Since(warmupStartTime).Seconds()
	warmupSpan.SetAttributes(
		attribute.Int("candidates_count", len(candidates)),
		attribute.Float64("duration", warmupDuration),
		attribute.String("status", "success"),
	)
	logger().Info(warmupCtx, "Consensus warmup completed",
		ion.Int("candidates_count", len(candidates)),
		ion.Float64("duration", warmupDuration),
		ion.String("function", "Consensus.Start.warmup"))
	warmupSpan.End()

	// Connect to the candidates first via AddPeerCache
	addPeersCtx, addPeersSpan := tracer.Start(trace_ctx, "Consensus.Start.addPeersToCache")
	addPeersStartTime := time.Now().UTC()
	logger().Info(addPeersCtx, "Adding peers to cache",
		ion.Int("candidates_count", len(candidates)),
		ion.String("function", "Consensus.Start.addPeersToCache"))

	stats := helper.AddBuddyNodesTemporarily(candidates)
	if stats.GetReachablePeers() == nil {
		err := fmt.Errorf("CONSENSUSERROR.ADDPEERSCACHE: failed to add peers to cache: %v", stats.GetUnreachablePeers())
		addPeersSpan.RecordError(err)
		addPeersSpan.SetAttributes(attribute.String("status", "failed"))
		addPeersDuration := time.Since(addPeersStartTime).Seconds()
		addPeersSpan.SetAttributes(attribute.Float64("duration", addPeersDuration))
		logger().Error(addPeersCtx, "Failed to add peers to cache",
			err,
			ion.Float64("duration", addPeersDuration),
			ion.String("function", "Consensus.Start.addPeersToCache"))
		addPeersSpan.End()
		return err
	}

	addPeersDuration := time.Since(addPeersStartTime).Seconds()
	addPeersSpan.SetAttributes(
		attribute.Int("reachable_peers_count", len(stats.GetReachablePeers())),
		attribute.Int("unreachable_peers_count", len(stats.GetUnreachablePeers())),
		attribute.Int("total_peers", stats.GetTotalPeers()),
		attribute.Float64("duration", addPeersDuration),
		attribute.String("status", "success"),
	)
	logger().Info(addPeersCtx, "Peers added to cache",
		ion.Int("reachable_peers", len(stats.GetReachablePeers())),
		ion.Int("unreachable_peers", len(stats.GetUnreachablePeers())),
		ion.Int("total_peers", stats.GetTotalPeers()),
		ion.Float64("time_taken_seconds", stats.GetTimeTaken().Seconds()),
		ion.Float64("duration", addPeersDuration),
		ion.String("function", "Consensus.Start.addPeersToCache"))
	addPeersSpan.End()

	// Now connect to final buddy nodes - ONLY main candidates first, backup only if needed
	connectednessCtx, connectednessSpan := tracer.Start(trace_ctx, "Consensus.Start.verifyConnectedness")
	connectednessStartTime := time.Now().UTC()
	maxPeersToCheck := config.MaxMainPeers + config.MaxBackupPeers
	logger().Info(connectednessCtx, "Verifying connectedness of peers",
		ion.Int("max_peers_to_check", maxPeersToCheck),
		ion.String("function", "Consensus.Start.verifyConnectedness"))

	reachablePeers, errMSG := consensus.ConnectedNessCheck(
		helper.ConvertMapToSlice(stats.GetReachablePeers()),
		maxPeersToCheck,
	)
	if errMSG != nil {
		connectednessSpan.RecordError(errMSG)
		connectednessSpan.SetAttributes(attribute.String("status", "failed"))
		connectednessDuration := time.Since(connectednessStartTime).Seconds()
		connectednessSpan.SetAttributes(attribute.Float64("duration", connectednessDuration))
		logger().Error(connectednessCtx, "Failed to verify connectedness",
			errMSG,
			ion.Float64("duration", connectednessDuration),
			ion.String("function", "Consensus.Start.verifyConnectedness"))
		connectednessSpan.End()
		return fmt.Errorf("CONSENSUSERROR.CONNECTEDNESSCHECK: failed to verify connectedness: %v", errMSG)
	}

	connectednessDuration := time.Since(connectednessStartTime).Seconds()
	connectednessSpan.SetAttributes(
		attribute.Int("connected_peers_count", len(reachablePeers)),
		attribute.Int("candidates_count", len(candidates)),
		attribute.Float64("duration", connectednessDuration),
		attribute.String("status", "success"),
	)
	logger().Info(connectednessCtx, "Verified connected peers",
		ion.Int("connected_peers", len(reachablePeers)),
		ion.Int("candidates", len(candidates)),
		ion.Float64("duration", connectednessDuration),
		ion.String("function", "Consensus.Start.verifyConnectedness"))
	connectednessSpan.End()

	// Step 3: Split into Main and Backup based on first MaxMainPeers connected peers
	splitCtx, splitSpan := tracer.Start(trace_ctx, "Consensus.Start.splitCandidates")
	splitStartTime := time.Now().UTC()
	logger().Info(splitCtx, "Splitting candidates into main and backup",
		ion.String("function", "Consensus.Start.splitCandidates"))

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

		splitSpan.RecordError(fmt.Errorf("%s", ErrorMessage))
		splitSpan.SetAttributes(
			attribute.String("status", "insufficient_peers"),
			attribute.Int("main_candidates", len(MainCandidates)),
			attribute.Int("required", config.MaxMainPeers),
		)
		splitDuration := time.Since(splitStartTime).Seconds()
		splitSpan.SetAttributes(attribute.Float64("duration", splitDuration))
		logger().Error(splitCtx, "Insufficient connected peers",
			fmt.Errorf("%s", ErrorMessage),
			ion.Int("main_candidates", len(MainCandidates)),
			ion.Int("required", config.MaxMainPeers),
			ion.Int("connected", len(reachablePeers)),
			ion.Int("unreachable", len(stats.GetUnreachablePeers())),
			ion.String("function", "Consensus.Start.splitCandidates"))
		splitSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_InsufficientPeers).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()

		return fmt.Errorf("%s", ErrorMessage)
	}

	splitDuration := time.Since(splitStartTime).Seconds()
	splitSpan.SetAttributes(
		attribute.Int("main_candidates_count", len(MainCandidates)),
		attribute.Int("backup_candidates_count", len(BackupCandidates)),
		attribute.Float64("duration", splitDuration),
		attribute.String("status", "success"),
	)
	logger().Info(splitCtx, "Split candidates into main and backup",
		ion.Int("main_candidates", len(MainCandidates)),
		ion.Int("backup_candidates", len(BackupCandidates)),
		ion.Float64("duration", splitDuration),
		ion.String("function", "Consensus.Start.splitCandidates"))
	splitSpan.End()

	// Populate consensus.PeerList directly from MainCandidates and BackupCandidates
	populateCtx, populateSpan := tracer.Start(trace_ctx, "Consensus.Start.populatePeerList")
	populateStartTime := time.Now().UTC()
	logger().Info(populateCtx, "Populating peer list",
		ion.Int("main_candidates", len(MainCandidates)),
		ion.Int("backup_candidates", len(BackupCandidates)),
		ion.String("function", "Consensus.Start.populatePeerList"))

	errMSG = consensus.PopulatePeerList(MainCandidates, BackupCandidates)
	if errMSG != nil {
		populateSpan.RecordError(errMSG)
		populateSpan.SetAttributes(attribute.String("status", "failed"))
		populateDuration := time.Since(populateStartTime).Seconds()
		populateSpan.SetAttributes(attribute.Float64("duration", populateDuration))
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.POPULATEPEERLIST: failed to populate peer list: %v", errMSG)
		logger().Error(populateCtx, "Failed to populate peer list",
			errMSG,
			ion.Float64("duration", populateDuration),
			ion.String("function", "Consensus.Start.populatePeerList"))
		populateSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToPopulatePeerList).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	populateDuration := time.Since(populateStartTime).Seconds()
	populateSpan.SetAttributes(
		attribute.Int("main_peers_count", len(consensus.PeerList.MainPeers)),
		attribute.Int("backup_peers_count", len(consensus.PeerList.BackupPeers)),
		attribute.Float64("duration", populateDuration),
		attribute.String("status", "success"),
	)
	logger().Info(populateCtx, "Peer list populated",
		ion.Int("main_peers", len(consensus.PeerList.MainPeers)),
		ion.Int("backup_peers", len(consensus.PeerList.BackupPeers)),
		ion.Float64("duration", populateDuration),
		ion.String("function", "Consensus.Start.populatePeerList"))
	populateSpan.End()

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

	logger().Info(trace_ctx, "Final buddy nodes list built",
		ion.Int("main_peers_count", len(consensus.PeerList.MainPeers)),
		ion.String("peer_ids", strings.Join(peerIDs, ", ")),
		ion.String("function", "Consensus.Start"))

	// Create ConsensusMessage with ONLY the final connected buddy nodes
	setZKBlockCtx, setZKBlockSpan := tracer.Start(trace_ctx, "Consensus.Start.setZKBlockData")
	setZKBlockStartTime := time.Now().UTC()
	logger().Info(setZKBlockCtx, "Setting ZKBlock data",
		ion.String("function", "Consensus.Start.setZKBlockData"))

	errMSG = consensus.SetZKBlockData(zkblock, MainCandidates)
	if errMSG != nil {
		setZKBlockSpan.RecordError(errMSG)
		setZKBlockSpan.SetAttributes(attribute.String("status", "failed"))
		setZKBlockDuration := time.Since(setZKBlockStartTime).Seconds()
		setZKBlockSpan.SetAttributes(attribute.Float64("duration", setZKBlockDuration))
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.SETZKBLOCKDATA: failed to set zkblock data: %v", errMSG)
		logger().Error(setZKBlockCtx, "Failed to set ZKBlock data",
			errMSG,
			ion.Float64("duration", setZKBlockDuration),
			ion.String("function", "Consensus.Start.setZKBlockData"))
		setZKBlockSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToSetZKBlockData).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	setZKBlockDuration := time.Since(setZKBlockStartTime).Seconds()
	setZKBlockSpan.SetAttributes(
		attribute.Float64("duration", setZKBlockDuration),
		attribute.String("status", "success"),
	)
	logger().Info(setZKBlockCtx, "ZKBlock data set successfully",
		ion.Float64("duration", setZKBlockDuration),
		ion.String("function", "Consensus.Start.setZKBlockData"))
	setZKBlockSpan.End()

	// Set sequencer identity and round ID so voters know where to send votes
	consensus.ZKBlockData.SetSequencerID(consensus.Host.ID().String())
	consensus.ZKBlockData.SetRoundID(zkblock.BlockHash.Hex())

	// Validate consensus configuration
	validateCtx, validateSpan := tracer.Start(trace_ctx, "Consensus.Start.validateConfiguration")
	validateStartTime := time.Now().UTC()
	logger().Info(validateCtx, "Validating consensus configuration",
		ion.String("function", "Consensus.Start.validateConfiguration"))

	if err := ValidateConsensusConfiguration(consensus); err != nil {
		validateSpan.RecordError(err)
		validateSpan.SetAttributes(attribute.String("status", "validation_failed"))
		validateDuration := time.Since(validateStartTime).Seconds()
		validateSpan.SetAttributes(attribute.Float64("duration", validateDuration))
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.VALIDATECONSENSUSCONFIGURATION: invalid consensus configuration: %v", err)
		logger().Error(validateCtx, "Invalid consensus configuration",
			err,
			ion.Float64("duration", validateDuration),
			ion.String("function", "Consensus.Start.validateConfiguration"))
		validateSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToValidateConsensusConfiguration).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	validateDuration := time.Since(validateStartTime).Seconds()
	validateSpan.SetAttributes(
		attribute.Float64("duration", validateDuration),
		attribute.String("status", "success"),
	)
	logger().Info(validateCtx, "Consensus configuration validated",
		ion.Float64("duration", validateDuration),
		ion.String("function", "Consensus.Start.validateConfiguration"))
	validateSpan.End()

	// Create allowed peers list (1 creator + MaxMainPeers main peers + backup peers for fallback)
	// Include backup peers so they can subscribe if main peers fail to respond
	allowedPeers := make([]peer.ID, 0, config.MaxMainPeers+config.MaxBackupPeers+1)
	allowedPeers = append(allowedPeers, consensus.Host.ID())
	allowedPeers = append(allowedPeers, consensus.PeerList.MainPeers...)
	allowedPeers = append(allowedPeers, consensus.PeerList.BackupPeers...)

	// Setup pubsub channels
	setupPubsubCtx, setupPubsubSpan := tracer.Start(trace_ctx, "Consensus.Start.setupPubsubChannels")
	setupPubsubStartTime := time.Now().UTC()
	logger().Info(setupPubsubCtx, "Setting up pubsub channels",
		ion.Int("allowed_peers", len(allowedPeers)),
		ion.Int("main_peers", len(consensus.PeerList.MainPeers)),
		ion.Int("backup_peers", len(consensus.PeerList.BackupPeers)),
		ion.String("function", "Consensus.Start.setupPubsubChannels"))

	err := consensus.SetGossipnode(config.PubSub_ConsensusChannel)
	if err != nil {
		setupPubsubSpan.RecordError(err)
		setupPubsubSpan.SetAttributes(attribute.String("status", "set_gossipnode_failed"))
		setupPubsubDuration := time.Since(setupPubsubStartTime).Seconds()
		setupPubsubSpan.SetAttributes(attribute.Float64("duration", setupPubsubDuration))
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.SETGOSSIPNODE: failed to set gossipnode: %v", err)
		logger().Error(setupPubsubCtx, "Failed to set gossipnode",
			err,
			ion.Float64("duration", setupPubsubDuration),
			ion.String("function", "Consensus.Start.setupPubsubChannels"))
		setupPubsubSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToSetGossipnode).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.PubSub_ConsensusChannel, false, allowedPeers); err != nil {
		setupPubsubSpan.RecordError(err)
		setupPubsubSpan.SetAttributes(attribute.String("status", "create_channel_failed"))
		setupPubsubDuration := time.Since(setupPubsubStartTime).Seconds()
		setupPubsubSpan.SetAttributes(attribute.Float64("duration", setupPubsubDuration))
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.CREATECHANNEL: failed to create pubsub channel: %v", err)
		logger().Error(setupPubsubCtx, "Failed to create pubsub channel",
			err,
			ion.String("channel", config.PubSub_ConsensusChannel),
			ion.Float64("duration", setupPubsubDuration),
			ion.String("function", "Consensus.Start.setupPubsubChannels"))
		setupPubsubSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToCreatePubsubChannel).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	setupPubsubSpan.SetAttributes(
		attribute.String("consensus_channel", config.PubSub_ConsensusChannel),
		attribute.Bool("consensus_channel_created", true),
	)
	logger().Info(setupPubsubCtx, "Successfully created pubsub channel",
		ion.String("channel", config.PubSub_ConsensusChannel),
		ion.String("function", "Consensus.Start.setupPubsubChannels"))

	// Create the CRDT sync channel ONLY for final buddy nodes (vote aggregators) to synchronize their CRDTs
	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.Pubsub_CRDTSync, false, allowedPeers); err != nil {
		if err.Error() != fmt.Sprintf("channel %s already exists", config.Pubsub_CRDTSync) {
			setupPubsubSpan.RecordError(err)
			logger().Warn(setupPubsubCtx, "Failed to create CRDT sync channel, continuing anyway",
				ion.Err(err),
				ion.String("channel", config.Pubsub_CRDTSync),
				ion.String("function", "Consensus.Start.setupPubsubChannels"))
		} else {
			setupPubsubSpan.SetAttributes(attribute.Bool("crdt_channel_already_exists", true))
			logger().Info(setupPubsubCtx, "CRDT sync channel already exists",
				ion.String("channel", config.Pubsub_CRDTSync),
				ion.String("function", "Consensus.Start.setupPubsubChannels"))
		}
	} else {
		setupPubsubSpan.SetAttributes(
			attribute.String("crdt_channel", config.Pubsub_CRDTSync),
			attribute.Bool("crdt_channel_created", true),
		)
		logger().Info(setupPubsubCtx, "Successfully created CRDT sync channel",
			ion.String("channel", config.Pubsub_CRDTSync),
			ion.Int("allowed_peers", len(allowedPeers)),
			ion.Int("buddy_nodes", len(consensus.PeerList.MainPeers)),
			ion.String("function", "Consensus.Start.setupPubsubChannels"))
	}

	setupPubsubDuration := time.Since(setupPubsubStartTime).Seconds()
	setupPubsubSpan.SetAttributes(
		attribute.Float64("duration", setupPubsubDuration),
		attribute.String("status", "success"),
	)
	setupPubsubSpan.End()

	// Subscribe the sequencer to its own channel to receive votes from buddy nodes
	subscribeCtx, subscribeSpan := tracer.Start(trace_ctx, "Consensus.Start.subscribeToChannel")
	subscribeStartTime := time.Now().UTC()
	logger().Info(subscribeCtx, "Subscribing sequencer to consensus channel",
		ion.String("function", "Consensus.Start.subscribeToChannel"))

	globalVars := PubSubMessages.NewGlobalVariables()

	// Initialize PubSub BuddyNode for sequencer if not already done
	if !globalVars.IsPubSubNodeInitialized() {
		defaultBuddies := PubSubMessages.NewBuddiesBuilder(nil)
		pubSubBuddyNode := MessagePassing.NewBuddyNode(logger_ctx, consensus.Host, defaultBuddies, nil, consensus.gossipnode.GetGossipPubSub())
		globalVars.Set_PubSubNode(pubSubBuddyNode)
		subscribeSpan.SetAttributes(attribute.Bool("pubsub_node_initialized", true))
		logger().Info(subscribeCtx, "Initialized PubSubNode for sequencer",
			ion.String("function", "Consensus.Start.subscribeToChannel"))
	}

	// Now subscribe to the channel
	service := Service.NewSubscriptionService(consensus.gossipnode.GetGossipPubSub())
	if err := service.HandleStreamSubscriptionRequest(logger_ctx, config.PubSub_ConsensusChannel); err != nil {
		subscribeSpan.RecordError(err)
		logger().Warn(subscribeCtx, "Failed to subscribe sequencer to consensus channel",
			ion.Err(err),
			ion.String("channel", config.PubSub_ConsensusChannel),
			ion.String("function", "Consensus.Start.subscribeToChannel"))
	} else {
		subscribeSpan.SetAttributes(attribute.Bool("subscribed_to_consensus_channel", true))
		logger().Info(subscribeCtx, "Successfully subscribed to consensus channel for vote collection",
			ion.String("channel", config.PubSub_ConsensusChannel),
			ion.String("function", "Consensus.Start.subscribeToChannel"))
	}

	// Initialize listener node for vote collection
	listenerCtx, listenerSpan := tracer.Start(trace_ctx, "Consensus.Start.initializeListener")
	listenerStartTime := time.Now().UTC()
	logger().Info(listenerCtx, "Initializing listener node for vote collection",
		ion.String("function", "Consensus.Start.initializeListener"))

	consensus.ListenerNode = MessagePassing.NewListenerNode(logger_ctx, consensus.Host, consensus.ResponseHandler)
	listenerSpan.SetAttributes(
		attribute.String("protocol", string(config.SubmitMessageProtocol)),
		attribute.Bool("listener_initialized", true),
	)
	logger().Info(listenerCtx, "Listener node initialized for vote collection",
		ion.String("protocol", string(config.SubmitMessageProtocol)),
		ion.String("function", "Consensus.Start.initializeListener"))

	// Populate buddy nodes list for the global listener node
	allPeerIDs := make([]peer.ID, 0, len(consensus.PeerList.MainPeers)+len(consensus.PeerList.BackupPeers))
	allPeerIDs = append(allPeerIDs, consensus.PeerList.MainPeers...)
	allPeerIDs = append(allPeerIDs, consensus.PeerList.BackupPeers...)

	// Update the global listener node with buddy nodes
	globalListenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if globalListenerNode != nil {
		globalListenerNode.BuddyNodes.Buddies_Nodes = allPeerIDs
		listenerSpan.SetAttributes(
			attribute.Int("buddy_nodes_count", len(allPeerIDs)),
			attribute.Int("main_peers", len(consensus.PeerList.MainPeers)),
			attribute.Int("backup_peers", len(consensus.PeerList.BackupPeers)),
		)
		logger().Info(listenerCtx, "Populated listener node with buddy nodes",
			ion.Int("total_buddies", len(allPeerIDs)),
			ion.Int("main_peers", len(consensus.PeerList.MainPeers)),
			ion.Int("backup_peers", len(consensus.PeerList.BackupPeers)),
			ion.String("function", "Consensus.Start.initializeListener"))
	}

	listenerDuration := time.Since(listenerStartTime).Seconds()
	listenerSpan.SetAttributes(
		attribute.Float64("duration", listenerDuration),
		attribute.String("status", "success"),
	)
	listenerSpan.End()

	subscribeDuration := time.Since(subscribeStartTime).Seconds()
	subscribeSpan.SetAttributes(
		attribute.Float64("duration", subscribeDuration),
		attribute.String("status", "success"),
	)
	subscribeSpan.End()

	// Event-driven flow: Request subscriptions → Verify → Broadcast votes → Process CRDT
	requestSubCtx, requestSubSpan := tracer.Start(trace_ctx, "Consensus.Start.requestSubscriptionPermission")
	requestSubStartTime := time.Now().UTC()
	logger().Info(requestSubCtx, "Requesting subscription permission",
		ion.String("function", "Consensus.Start.requestSubscriptionPermission"))

	if err := consensus.RequestSubscriptionPermission(); err != nil {
		requestSubSpan.RecordError(err)
		requestSubSpan.SetAttributes(attribute.String("status", "failed"))
		requestSubDuration := time.Since(requestSubStartTime).Seconds()
		requestSubSpan.SetAttributes(attribute.Float64("duration", requestSubDuration))
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.REQUESTSUBSCRIPTIONPERMISSION: Failed to request subscription permission: %v", err)
		logger().Error(requestSubCtx, "Failed to request subscription permission",
			err,
			ion.Float64("duration", requestSubDuration),
			ion.String("function", "Consensus.Start.requestSubscriptionPermission"))
		requestSubSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToRequestSubscriptionPermission).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("%s", ErrorMessage)
	}

	requestSubDuration := time.Since(requestSubStartTime).Seconds()
	requestSubSpan.SetAttributes(
		attribute.Float64("duration", requestSubDuration),
		attribute.String("status", "success"),
	)
	logger().Info(requestSubCtx, "Subscription permission granted",
		ion.Float64("duration", requestSubDuration),
		ion.String("function", "Consensus.Start.requestSubscriptionPermission"))
	requestSubSpan.End()

	// CRITICAL FIX: Update the listener node to ONLY include the finalized MainPeers
	// This ensures that BFT consensus only waits for/accepts votes from the actual committee
	// and excludes any backup nodes that were connected but not selected as main peers.
	startUpdateListenerCtx, startUpdateListenerSpan := tracer.Start(trace_ctx, "Consensus.Start.updateListenerNode")
	logger().Info(startUpdateListenerCtx, "Updating listener node with finalized consensus committee",
		ion.Int("committee_size", len(consensus.PeerList.MainPeers)),
		ion.String("function", "Consensus.Start.updateListenerNode"))

	globalListenerNode = PubSubMessages.NewGlobalVariables().Get_ForListner()
	if globalListenerNode != nil {
		// Replace the previous "all candidates" list with the finalized "committee" list
		globalListenerNode.BuddyNodes.Buddies_Nodes = consensus.PeerList.MainPeers

		startUpdateListenerSpan.SetAttributes(
			attribute.Int("final_committee_size", len(globalListenerNode.BuddyNodes.Buddies_Nodes)),
			attribute.Bool("backup_peers_excluded", true),
		)

		// Log the final committee for debugging
		var peerIDStrings []string
		for _, peerID := range consensus.PeerList.MainPeers {
			peerIDStrings = append(peerIDStrings, peerID.String())
		}

		logger().Info(startUpdateListenerCtx, "Listener node updated with final committee",
			ion.Int("count", len(consensus.PeerList.MainPeers)),
			ion.String("committee_peers", strings.Join(peerIDStrings, ", ")),
			ion.String("function", "Consensus.Start.updateListenerNode"))
	}
	startUpdateListenerSpan.End()

	// Start event-driven flow in background
	// CRITICAL: Pass both trace_ctx and rootSpan to the goroutine
	// trace_ctx contains the trace information, and rootSpan needs to be ended
	// when the goroutine completes to ensure the complete trace is recorded
	common.LocalGRO.Go(GRO.SequencerConsensusThread, func(ctx context.Context) error {
		// Pass trace_ctx which contains the active trace context from the root span
		// Also pass rootSpan so it can be ended when the async flow completes
		// This ensures the root span stays open until all child spans are created and recorded
		consensus.startEventDrivenFlowAfterSubscriptionPermission(trace_ctx, rootSpan)
		return nil
	})

	// Finalize root span - but note: child spans in the goroutine will still be recorded
	// even though this span ends, because they're already created and linked to the trace
	totalDuration := time.Since(startTime).Seconds()
	rootSpan.SetAttributes(
		attribute.Float64("duration", totalDuration),
		attribute.String("status", "async_flow_started"),
		attribute.Bool("async_flow_running", true),
	)
	logger().Info(trace_ctx, "Consensus Start completed, async flow started",
		ion.Float64("total_duration", totalDuration),
		ion.String("function", "Consensus.Start"))

	// Note: We don't cancel trace_ctx here because the goroutine needs it
	// The trace will remain active until all child spans complete
	return nil
}

// RequestSubscriptionPermission asks all buddy nodes for permission to subscribe to the consensus channel
// Ensures: 1 creator + MaxMainPeers subscribers = MaxMainPeers + 1 total nodes
func (consensus *Consensus) RequestSubscriptionPermission() error {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.RequestSubscriptionPermission")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("channel", consensus.Channel))

	logger().Info(trace_ctx, "Requesting subscription permission from buddy nodes",
		ion.String("channel", consensus.Channel),
		ion.String("function", "Consensus.RequestSubscriptionPermission"))

	if consensus.gossipnode == nil {
		err := fmt.Errorf("GossipPubSub not initialized")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return err
	}

	// Validate consensus configuration first
	if err := ValidateConsensusConfiguration(consensus); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(trace_ctx, "Invalid consensus configuration",
			err,
			ion.String("function", "Consensus.RequestSubscriptionPermission"))
		return fmt.Errorf("invalid consensus configuration: %w", err)
	}

	// Use the AskForSubscription function from Communication.go
	err := AskForSubscription(consensus.ListenerNode, consensus.Channel, consensus)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Error(trace_ctx, "Failed to get subscription permission",
			err,
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.RequestSubscriptionPermission"))
		return fmt.Errorf("failed to get subscription permission: %w", err)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("expected_subscribers", config.MaxMainPeers),
	)
	logger().Info(trace_ctx, "Successfully obtained subscription permission",
		ion.Int("expected_subscribers", config.MaxMainPeers),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.RequestSubscriptionPermission"))
	return nil
}

// startEventDrivenFlowAfterSubscriptionPermission orchestrates the consensus flow after
// subscription permission has already been granted.
//
// Flow: Verify subscriptions → Broadcast vote trigger → Process CRDT
// Note: This runs in a goroutine. The traceCtx parameter should be the trace context
// from the parent span (trace_ctx from Start()). The parentRootSpan is the root span
// from Start() that needs to be ended when this function completes to ensure the complete
// trace is recorded with all child spans.
func (consensus *Consensus) startEventDrivenFlowAfterSubscriptionPermission(traceCtx context.Context, parentRootSpan ion.Span) {
	tracer := logger().Tracer("Consensus")
	// Create a child span from the parent trace context
	// traceCtx contains the trace information, so this span will be linked to the parent
	trace_ctx, asyncFlowSpan := tracer.Start(traceCtx, "Consensus.startEventDrivenFlowAfterSubscriptionPermission")
	// IMPORTANT: defer ensures both spans are recorded when this function completes
	// First end the async flow span, then end the parent root span
	defer func() {
		asyncFlowSpan.End()
		// End the parent root span after all child spans are complete
		// This ensures the complete trace is recorded with proper parent-child relationships
		parentRootSpan.End()
	}()

	startTime := time.Now().UTC()
	logger().Info(trace_ctx, "Starting event-driven consensus flow",
		ion.String("function", "Consensus.startEventDrivenFlowAfterSubscriptionPermission"))

	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			asyncFlowSpan.RecordError(err)
			asyncFlowSpan.SetAttributes(attribute.String("status", "gro_init_failed"))
			logger().Error(trace_ctx, "Failed to initialize local gro",
				err,
				ion.String("function", "Consensus.startEventDrivenFlowAfterSubscriptionPermission"))
			// End spans before returning
			asyncFlowSpan.End()
			parentRootSpan.End()
			return
		}
	}
	// Context for the alerts
	alert_ctx := trace_ctx

	// Step 2: Verify subscriptions (with retry mechanism)
	verifyCtx, verifySpan := tracer.Start(trace_ctx, "Consensus.startEventDrivenFlow.verifySubscriptions")
	verifyStartTime := time.Now().UTC()
	logger().Info(verifyCtx, "Verifying subscriptions",
		ion.String("function", "Consensus.startEventDrivenFlow.verifySubscriptions"))

	// Optimization: Since we now wait for the mesh to form inside VerifySubscriptions,
	// we can reduce the retry parameters significantly.
	maxRetries := 2
	retryDelay := 50 * time.Millisecond
	verifySpan.SetAttributes(
		attribute.Int("max_retries", maxRetries),
		attribute.Float64("retry_delay_seconds", retryDelay.Seconds()),
	)

	for attempt := 1; attempt <= maxRetries; attempt++ {
		verifySpan.SetAttributes(attribute.Int("attempt", attempt))
		if err := consensus.VerifySubscriptions(trace_ctx); err != nil {
			if attempt < maxRetries {
				logger().Warn(verifyCtx, "Verification attempt failed, retrying",
					ion.Err(err),
					ion.Int("attempt", attempt),
					ion.Int("max_retries", maxRetries),
					ion.Float64("retry_delay_seconds", retryDelay.Seconds()),
					ion.String("function", "Consensus.startEventDrivenFlow.verifySubscriptions"))
				time.Sleep(retryDelay)
				continue
			}
			verifySpan.RecordError(err)
			logger().Warn(verifyCtx, "Subscription verification failed after all retries, continuing anyway",
				ion.Err(err),
				ion.Int("attempts", maxRetries),
				ion.String("function", "Consensus.startEventDrivenFlow.verifySubscriptions"))
		} else {
			verifySpan.SetAttributes(attribute.String("status", "success"))
			logger().Info(verifyCtx, "Subscriptions verified successfully",
				ion.Int("attempt", attempt),
				ion.String("function", "Consensus.startEventDrivenFlow.verifySubscriptions"))
			break
		}
	}

	verifyDuration := time.Since(verifyStartTime).Seconds()
	verifySpan.SetAttributes(attribute.Float64("duration", verifyDuration))
	verifySpan.End()

	// Step 3: Broadcast vote trigger (only after subscriptions are verified/attempted)
	broadcastCtx, broadcastSpan := tracer.Start(trace_ctx, "Consensus.startEventDrivenFlow.broadcastVoteTrigger")
	broadcastStartTime := time.Now().UTC()
	logger().Info(broadcastCtx, "Broadcasting vote trigger",
		ion.String("function", "Consensus.startEventDrivenFlow.broadcastVoteTrigger"))

	if consensus.ZKBlockData == nil {
		err := fmt.Errorf("CONSENSUSERROR.BROADCASTVOTETRIGGER: ZKBlockData not set, cannot broadcast vote trigger")
		broadcastSpan.RecordError(err)
		broadcastSpan.SetAttributes(attribute.String("status", "zkblockdata_not_set"))
		broadcastDuration := time.Since(broadcastStartTime).Seconds()
		broadcastSpan.SetAttributes(attribute.Float64("duration", broadcastDuration))
		ErrorMessage := err.Error()
		logger().Error(broadcastCtx, "ZKBlockData not set, cannot broadcast vote trigger",
			err,
			ion.Float64("duration", broadcastDuration),
			ion.String("function", "Consensus.startEventDrivenFlow.broadcastVoteTrigger"))
		broadcastSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_ZKBlockDataNotSet).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return
	}

	if err := consensus.BroadcastVoteTrigger(); err != nil {
		broadcastSpan.RecordError(err)
		broadcastSpan.SetAttributes(attribute.String("status", "failed"))
		broadcastDuration := time.Since(broadcastStartTime).Seconds()
		broadcastSpan.SetAttributes(attribute.Float64("duration", broadcastDuration))
		ErrorMessage := fmt.Sprintf("CONSENSUSERROR.BROADCASTVOTETRIGGER: BroadcastVoteTrigger failed: %v", err)
		logger().Error(broadcastCtx, "BroadcastVoteTrigger failed",
			err,
			ion.Float64("duration", broadcastDuration),
			ion.String("function", "Consensus.startEventDrivenFlow.broadcastVoteTrigger"))
		broadcastSpan.End()

		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_FailedToBroadcastVoteTrigger).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return
	}

	broadcastDuration := time.Since(broadcastStartTime).Seconds()
	broadcastSpan.SetAttributes(
		attribute.Float64("duration", broadcastDuration),
		attribute.String("status", "success"),
	)
	logger().Info(broadcastCtx, "Vote trigger broadcast successfully",
		ion.Float64("duration", broadcastDuration),
		ion.String("function", "Consensus.startEventDrivenFlow.broadcastVoteTrigger"))
	broadcastSpan.End()

	// Step 4: Event-driven vote collection
	// Create a round context with ConsensusTimeout deadline
	roundCtx, roundCancel := context.WithTimeout(trace_ctx, config.ConsensusTimeout)
	consensus.roundCtx = roundCtx
	consensus.roundCancel = roundCancel

	// Create vote notification channel and register it so handleSubmitVote can push votes
	voteNotifyCh := make(chan PubSubMessages.VoteNotification, config.MaxMainPeers)
	consensus.voteNotifyCh = voteNotifyCh
	MessagePassing.RegisterVoteCollector(voteNotifyCh)

	processVotesCtx, processVotesSpan := tracer.Start(trace_ctx, "Consensus.startEventDrivenFlow.processVotes")
	processVotesStartTime := time.Now().UTC()

	blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.Hex()
	requiredVotes := config.MaxMainPeers
	collectedVotes := make(map[string]int8) // peerID -> vote

	logger().NamedLogger.Info(processVotesCtx, "Starting event-driven vote collection",
		ion.Int("required_votes", requiredVotes),
		ion.String("block_hash", blockHash),
		ion.Float64("timeout_seconds", config.ConsensusTimeout.Seconds()),
		ion.String("function", "Consensus.startEventDrivenFlow.processVotes"))

	// Event loop: wait for votes or timeout
	for {
		select {
		case notification := <-voteNotifyCh:
			// Only accept votes for this round's block hash
			if notification.BlockHash != blockHash {
				logger().NamedLogger.Warn(processVotesCtx, "Ignoring vote for different block hash",
					ion.String("expected", blockHash),
					ion.String("got", notification.BlockHash),
					ion.String("peer", notification.PeerID),
					ion.String("function", "Consensus.startEventDrivenFlow.processVotes"))
				continue
			}

			// Only accept votes from committee members
			if !isCommitteeMember(notification.PeerID, consensus.PeerList.MainPeers) {
				logger().NamedLogger.Warn(processVotesCtx, "Ignoring vote from non-committee peer",
					ion.String("peer", notification.PeerID),
					ion.String("function", "Consensus.startEventDrivenFlow.processVotes"))
				continue
			}

			// Store vote (idempotent)
			collectedVotes[notification.PeerID] = notification.Vote
			Maps.StoreVoteResult(blockHash, notification.PeerID, notification.Vote)

			logger().NamedLogger.Info(processVotesCtx, "Vote received via push notification",
				ion.String("peer", notification.PeerID),
				ion.Int("vote", int(notification.Vote)),
				ion.Int("collected", len(collectedVotes)),
				ion.Int("required", requiredVotes),
				ion.String("function", "Consensus.startEventDrivenFlow.processVotes"))

			fmt.Printf("📥 Vote received: peer=%s vote=%d (%d/%d)\n",
				notification.PeerID[:16], notification.Vote, len(collectedVotes), requiredVotes)

			// Exit early if we have all votes (quorum)
			if len(collectedVotes) >= requiredVotes {
				logger().NamedLogger.Info(processVotesCtx, "All votes collected - quorum reached",
					ion.Int("collected", len(collectedVotes)),
					ion.Int("required", requiredVotes),
					ion.String("function", "Consensus.startEventDrivenFlow.processVotes"))
				goto VOTES_COLLECTED
			}

		case <-roundCtx.Done():
			logger().NamedLogger.Warn(processVotesCtx, "Round deadline reached, proceeding with partial votes",
				ion.Int("collected", len(collectedVotes)),
				ion.Int("required", requiredVotes),
				ion.String("function", "Consensus.startEventDrivenFlow.processVotes"))
			fmt.Printf("⏰ Consensus timeout: collected %d/%d votes\n", len(collectedVotes), requiredVotes)
			goto VOTES_COLLECTED
		}
	}

VOTES_COLLECTED:
	// Unregister the vote collector now that collection is done
	MessagePassing.UnregisterVoteCollector()
	roundCancel()

	processVotesDuration := time.Since(processVotesStartTime).Seconds()
	processVotesSpan.SetAttributes(
		attribute.Int("votes_collected", len(collectedVotes)),
		attribute.Int("votes_required", requiredVotes),
		attribute.Float64("duration", processVotesDuration),
	)
	processVotesSpan.End()

	// Print CRDT state
	consensus.PrintCRDTState(trace_ctx)

	// Collect BLS results from buddy nodes (pull-based for BLS signatures)
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	blsResults := consensus.CollectVoteResultsFromBuddies(listenerNode)

	// Verify consensus with BLS signatures
	consensusReached := consensus.VerifyConsensusWithBLS(blsResults)

	// Broadcast and process the block
	if err := consensus.BroadcastAndProcessBlock(blsResults, consensusReached); err != nil {
		logger().NamedLogger.Error(trace_ctx, "Failed to broadcast and process block",
			err,
			ion.String("function", "Consensus.startEventDrivenFlowAfterSubscriptionPermission"))
	}

	totalDuration := time.Since(startTime).Seconds()
	asyncFlowSpan.SetAttributes(
		attribute.Float64("duration", totalDuration),
		attribute.String("status", "success"),
		attribute.Int("votes_collected", len(collectedVotes)),
		attribute.Bool("consensus_reached", consensusReached),
	)
	logger().Info(trace_ctx, "Event-driven consensus flow completed",
		ion.Float64("total_duration", totalDuration),
		ion.Int("votes_collected", len(collectedVotes)),
		ion.Bool("consensus_reached", consensusReached),
		ion.String("function", "Consensus.startEventDrivenFlowAfterSubscriptionPermission"))
}

// VerifySubscriptions checks if nodes are actually subscribed to the pubsub channel
// This method now uses the new pubsub-based verification system
// isCommitteeMember checks if a peer ID string is in the committee (MainPeers list)
func isCommitteeMember(peerIDStr string, mainPeers []peer.ID) bool {
	for _, p := range mainPeers {
		if p.String() == peerIDStr {
			return true
		}
	}
	return false
}

func (consensus *Consensus) VerifySubscriptions(logger_ctx context.Context) error {
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.VerifySubscriptions")
	defer span.End()

	startTime := time.Now().UTC()
	logger().Info(trace_ctx, "Starting subscription verification using pubsub messaging",
		ion.String("function", "Consensus.VerifySubscriptions"))

	if consensus.gossipnode == nil {
		err := fmt.Errorf("GossipPubSub not initialized for consensus")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return err
	}

	// Use the new VerifySubscriptions function from Communication.go
	verifiedPeerIDs, err := VerifySubscriptions(trace_ctx, consensus.gossipnode.GetGossipPubSub(), consensus)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Error(trace_ctx, "Failed to verify subscriptions",
			err,
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.VerifySubscriptions"))
		return fmt.Errorf("failed to verify subscriptions: %v", err)
	}

	span.SetAttributes(attribute.Int("verified_peers_count", len(verifiedPeerIDs)))
	logger().Info(trace_ctx, "Received verification responses from peers",
		ion.Int("verified_peers", len(verifiedPeerIDs)),
		ion.Int("expected_peers", config.MaxMainPeers),
		ion.String("function", "Consensus.VerifySubscriptions"))

	// Verify that we have the expected number of subscribers
	if len(verifiedPeerIDs) != config.MaxMainPeers {
		err := fmt.Errorf("incorrect number of verified peers: got %d, expected %d", len(verifiedPeerIDs), config.MaxMainPeers)
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "count_mismatch"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Error(trace_ctx, "Incorrect number of verified peers",
			err,
			ion.Int("got", len(verifiedPeerIDs)),
			ion.Int("expected", config.MaxMainPeers),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.VerifySubscriptions"))
		return err
	}

	// Log all verified PeerIDs
	for connectionPeerID, responsePeerID := range verifiedPeerIDs {
		logger().Info(trace_ctx, "Verified subscription",
			ion.String("connection_peer", connectionPeerID.String()),
			ion.String("response_peer", responsePeerID),
			ion.String("function", "Consensus.VerifySubscriptions"))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "Subscription verification successful",
		ion.Int("verified_peers", len(verifiedPeerIDs)),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.VerifySubscriptions"))
	return nil
}

// BroadcastVoteTrigger broadcasts a vote trigger message to all subscribed peers
// This initiates the voting process by sending vote trigger broadcasts
func (consensus *Consensus) BroadcastVoteTrigger() error {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.BroadcastVoteTrigger")
	defer span.End()

	startTime := time.Now().UTC()
	logger().Info(trace_ctx, "Broadcasting vote trigger",
		ion.String("function", "Consensus.BroadcastVoteTrigger"))

	if consensus.gossipnode == nil {
		err := fmt.Errorf("GossipPubSub not initialized for consensus")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(trace_ctx, "GossipPubSub not initialized",
			err,
			ion.String("function", "Consensus.BroadcastVoteTrigger"))
		return err
	}

	if consensus.ZKBlockData == nil {
		err := fmt.Errorf("ZKBlockData not set - cannot trigger voting")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "zkblockdata_not_set"))
		logger().Error(trace_ctx, "ZKBlockData not set",
			err,
			ion.String("function", "Consensus.BroadcastVoteTrigger"))
		return err
	}

	span.SetAttributes(
		attribute.String("host_id", consensus.Host.ID().String()),
		attribute.String("block_hash", consensus.ZKBlockData.GetZKBlock().BlockHash.Hex()),
	)

	// Send vote trigger only to committee members (not all connected peers)
	if err := messaging.BroadcastVoteTriggerToCommittee(consensus.Host, consensus.ZKBlockData, consensus.PeerList.MainPeers); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Error(trace_ctx, "Failed to broadcast vote trigger",
			err,
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.BroadcastVoteTrigger"))
		return fmt.Errorf("failed to broadcast vote trigger: %w", err)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "Vote trigger broadcast completed successfully",
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.BroadcastVoteTrigger"))
	return nil
}

// PrintCRDTState prints the current state of the CRDT (read-only operation)
func (consensus *Consensus) PrintCRDTState(logger_ctx context.Context) error {
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.PrintCRDTState")
	defer span.End()

	startTime := time.Now().UTC()
	logger().Info(trace_ctx, "Printing CRDT state",
		ion.String("function", "Consensus.PrintCRDTState"))

	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.CRDTLayer == nil {
		err := fmt.Errorf("listener node or CRDT layer not initialized")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(trace_ctx, "Listener node or CRDT layer not initialized",
			err,
			ion.String("function", "Consensus.PrintCRDTState"))
		return err
	}

	if consensus.ZKBlockData == nil || consensus.ZKBlockData.GetZKBlock() == nil {
		err := fmt.Errorf("ZKBlockData not initialized")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		logger().Error(trace_ctx, "ZKBlockData not initialized",
			err,
			ion.String("function", "Consensus.PrintCRDTState"))
		return err
	}

	blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.Hex()
	span.SetAttributes(
		attribute.String("block_hash", blockHash),
		attribute.String("peer_id", listenerNode.PeerID.String()),
	)

	consensus.printCRDTHeader(listenerNode)
	consensus.printCRDTVotes(trace_ctx, listenerNode)
	consensus.printCRDTFooter()

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "CRDT state printed successfully",
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.PrintCRDTState"))

	return nil
}

// printCRDTHeader prints the header information for CRDT state
func (consensus *Consensus) printCRDTHeader(listenerNode *PubSubMessages.BuddyNode) {
	logger().Info(context.Background(), "CRDT State - Sequencer - Start")
	logger().Info(context.Background(), "CRDT State", ion.String("peer_id", listenerNode.PeerID.String()), ion.String("timestamp", time.Now().UTC().Format(time.RFC3339)), ion.String("block_hash", consensus.ZKBlockData.GetZKBlock().BlockHash.String()))
}

// printCRDTVotes prints vote information from CRDT
func (consensus *Consensus) printCRDTVotes(logger_ctx context.Context, listenerNode *PubSubMessages.BuddyNode) {
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.printCRDTVotes")
	defer span.End()

	startTime := time.Now().UTC()
	logger().Info(trace_ctx, "Printing CRDT votes",
		ion.String("function", "Consensus.printCRDTVotes"))

	votes, exists := MessagePassing.GetVotesFromCRDT(trace_ctx, listenerNode.CRDTLayer, "vote")
	if !exists || len(votes) == 0 {
		span.SetAttributes(
			attribute.Int("votes_count", 0),
			attribute.Bool("votes_exist", false),
		)
	logger().Info(trace_ctx, "Votes in CRDT", ion.Int("vote_count", 0))
		logger().Info(trace_ctx, "No votes in CRDT yet",
			ion.String("function", "Consensus.printCRDTVotes"))
		return
	}

	span.SetAttributes(attribute.Int("votes_count", len(votes)))
	logger().Info(trace_ctx, "Total votes in CRDT", ion.Int("vote_count", len(votes)))

	yesVotes := 0
	noVotes := 0

	for i, vote := range votes {
		var voteData map[string]interface{}
		if err := json.Unmarshal([]byte(vote), &voteData); err != nil {
			span.RecordError(err)
			logger().Warn(trace_ctx, "Failed to parse vote",
				ion.Err(err),
				ion.Int("vote_index", i+1),
				ion.String("function", "Consensus.printCRDTVotes"))
			logger().Error(trace_ctx, "Vote parsing error", fmt.Errorf("invalid vote"), ion.Int("vote_index", i+1))
			continue
		}

		voteValue := voteData["vote"]
		blockHash := voteData["block_hash"]

		if voteValue == float64(1) {
			yesVotes++
		} else if voteValue == float64(-1) {
			noVotes++
		}

			logger().Debug(trace_ctx, "Processing vote", ion.Int("vote_index", i+1))
			logger().Debug(trace_ctx, "Vote value", ion.String("value", fmt.Sprintf("%v", voteValue)))
			logger().Debug(trace_ctx, "Vote block hash", ion.String("block_hash", fmt.Sprintf("%v", blockHash)))
		if i < len(votes)-1 {
		}
	}

	span.SetAttributes(
		attribute.Int("yes_votes", yesVotes),
		attribute.Int("no_votes", noVotes),
	)

	logger().Info(trace_ctx, "Vote summary", ion.Int("yes_votes", yesVotes), ion.Int("no_votes", noVotes), ion.Int("total_votes", len(votes)))

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "CRDT votes printed",
		ion.Int("total_votes", len(votes)),
		ion.Int("yes_votes", yesVotes),
		ion.Int("no_votes", noVotes),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.printCRDTVotes"))
}

// printCRDTFooter prints the footer for CRDT state
func (consensus *Consensus) printCRDTFooter() {
	logger().Info(context.Background(), "CRDT State - Sequencer - End")
}

// ProcessVoteCollection orchestrates the vote collection and processing flow
// This manages the state flag and coordinates vote collection, verification, and block processing
func (consensus *Consensus) ProcessVoteCollection() error {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.ProcessVoteCollection")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.Float64("duration", startTime.Sub(startTime).Seconds()))
	logger().Info(trace_ctx, "Processing vote collection",
		ion.String("function", "Consensus.ProcessVoteCollection"))

	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			logger().Error(trace_ctx, "Failed to initialize local gro",
				err,
				ion.String("function", "Consensus.ProcessVoteCollection"))
			return fmt.Errorf("CONSENSUSERROR.PROCESSVOTECOLLECTION: failed to initialize local gro: %v", err)
		}
	}
	if consensus.ZKBlockData == nil || consensus.ZKBlockData.GetZKBlock() == nil {
		err := fmt.Errorf("ZKBlockData not initialized")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "validation_failed"))
		return err
	}

	// Guard against concurrent calls for the same block (single atomic check)
	currentBlockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
	span.SetAttributes(attribute.String("block_hash", currentBlockHash))
	consensus.mu.Lock()
	if consensus.isProcessingVotes && consensus.processedBlockHash == currentBlockHash {
		consensus.mu.Unlock()
		span.SetAttributes(attribute.Bool("already_processing", true), attribute.String("status", "skipped"))
		logger().Info(trace_ctx, "Vote processing already in progress, skipping duplicate call",
			ion.String("block_hash", currentBlockHash),
			ion.String("function", "Consensus.ProcessVoteCollection"))
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
		processCtx, processSpan := tracer.Start(trace_ctx, "Consensus.ProcessVoteCollection.process")
		defer processSpan.End()

		listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
		if listenerNode == nil {
			err := fmt.Errorf("listener node not available for vote collection")
			processSpan.RecordError(err)
			processSpan.SetAttributes(attribute.String("status", "listener_not_available"))
			logger().Error(processCtx, "Listener node not available for vote collection",
				err,
				ion.String("function", "Consensus.ProcessVoteCollection.process"))
			return nil
		}

		// Step 1: Collect vote results from buddy nodes
		collectCtx, collectSpan := tracer.Start(processCtx, "Consensus.ProcessVoteCollection.collectVoteResults")
		collectStartTime := time.Now().UTC()
		blsResults := consensus.CollectVoteResultsFromBuddies(listenerNode)
		collectDuration := time.Since(collectStartTime).Seconds()
		collectSpan.SetAttributes(
			attribute.Int("bls_results_count", len(blsResults)),
			attribute.Float64("duration", collectDuration),
		)
		logger().Info(collectCtx, "Collected vote results from buddies",
			ion.Int("bls_results", len(blsResults)),
			ion.Float64("duration", collectDuration),
			ion.String("function", "Consensus.ProcessVoteCollection.collectVoteResults"))
		collectSpan.End()

		// Step 2: Verify consensus with BLS signatures
		verifyCtx, verifySpan := tracer.Start(processCtx, "Consensus.ProcessVoteCollection.verifyConsensus")
		verifyStartTime := time.Now().UTC()
		consensusReached := consensus.VerifyConsensusWithBLS(blsResults)
		verifyDuration := time.Since(verifyStartTime).Seconds()
		verifySpan.SetAttributes(
			attribute.Bool("consensus_reached", consensusReached),
			attribute.Float64("duration", verifyDuration),
		)
		logger().Info(verifyCtx, "Consensus verification completed",
			ion.Bool("consensus_reached", consensusReached),
			ion.Float64("duration", verifyDuration),
			ion.String("function", "Consensus.ProcessVoteCollection.verifyConsensus"))
		verifySpan.End()

		// Step 3: Broadcast and process block (state-changing operation)
		broadcastCtx, broadcastSpan := tracer.Start(processCtx, "Consensus.ProcessVoteCollection.broadcastAndProcess")
		broadcastStartTime := time.Now().UTC()
		if err := consensus.BroadcastAndProcessBlock(blsResults, consensusReached); err != nil {
			broadcastSpan.RecordError(err)
			broadcastSpan.SetAttributes(attribute.String("status", "failed"))
			broadcastDuration := time.Since(broadcastStartTime).Seconds()
			broadcastSpan.SetAttributes(attribute.Float64("duration", broadcastDuration))
			logger().Error(broadcastCtx, "Failed to broadcast and process block",
				err,
				ion.Float64("duration", broadcastDuration),
				ion.String("function", "Consensus.ProcessVoteCollection.broadcastAndProcess"))
			broadcastSpan.End()
			return nil
		}
		broadcastDuration := time.Since(broadcastStartTime).Seconds()
		broadcastSpan.SetAttributes(
			attribute.Float64("duration", broadcastDuration),
			attribute.String("status", "success"),
		)
		logger().Info(broadcastCtx, "Broadcast and process block completed",
			ion.Float64("duration", broadcastDuration),
			ion.String("function", "Consensus.ProcessVoteCollection.broadcastAndProcess"))
		broadcastSpan.End()

		processDuration := time.Since(startTime).Seconds()
		processSpan.SetAttributes(
			attribute.Float64("duration", processDuration),
			attribute.String("status", "success"),
		)
		return nil
	})

	totalDuration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", totalDuration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "Vote collection processing initiated",
		ion.Float64("duration", totalDuration),
		ion.String("function", "Consensus.ProcessVoteCollection"))
	return nil
}

// CollectVoteResultsFromBuddies collects vote aggregation results from all buddy nodes
// Returns BLS results from buddy nodes
func (consensus *Consensus) CollectVoteResultsFromBuddies(listenerNode *PubSubMessages.BuddyNode) []BLS_Signer.BLSresponse {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.CollectVoteResultsFromBuddies")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.Int("buddy_nodes_count", len(listenerNode.BuddyNodes.Buddies_Nodes)))

	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			logger().Error(trace_ctx, "Failed to initialize local gro",
				err,
				ion.String("function", "Consensus.CollectVoteResultsFromBuddies"))
			return nil
		}
	}

	logger().Info(trace_ctx, "Requesting vote aggregation results from buddy nodes",
		ion.Int("buddy_nodes", len(listenerNode.BuddyNodes.Buddies_Nodes)),
		ion.String("function", "Consensus.CollectVoteResultsFromBuddies"))

	wg, err := common.LocalGRO.NewFunctionWaitGroup(trace_ctx, GRO.SequencerVoteCollectionWaitGroup)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "waitgroup_creation_failed"))
		logger().Error(trace_ctx, "Failed to create function wait group",
			err,
			ion.String("function", "Consensus.CollectVoteResultsFromBuddies"))
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
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Int("bls_results_count", len(blsResults)),
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "Collected vote results from all buddy nodes",
		ion.Int("bls_results", len(blsResults)),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.CollectVoteResultsFromBuddies"))
	return blsResults
}

// requestVoteResultFromBuddy requests vote result from a single buddy node
// Returns BLS response if successful, nil otherwise
func (consensus *Consensus) requestVoteResultFromBuddy(peerID peer.ID) *BLS_Signer.BLSresponse {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.requestVoteResultFromBuddy")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("peer_id", peerID.String()))
	blockHash := consensus.ZKBlockData.GetZKBlock().BlockHash.String()
	span.SetAttributes(attribute.String("block_hash", blockHash))

	logger().Info(trace_ctx, "Requesting vote result from buddy node",
		ion.String("peer_id", peerID.String()),
		ion.String("block_hash", blockHash),
		ion.String("function", "Consensus.requestVoteResultFromBuddy"))

	stream, err := consensus.Host.NewStream(trace_ctx, peerID, config.SubmitMessageProtocol)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "stream_failed"))
		logger().Error(trace_ctx, "Failed to open stream to peer",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Consensus.requestVoteResultFromBuddy"))
		return nil
	}
	defer stream.Close()

	// Build request message
	reqAck := PubSubMessages.NewACKBuilder().True_ACK_Message(consensus.Host.ID(), config.Type_VoteResult)
	requestPayload := map[string]string{
		"block_hash": blockHash,
	}
	requestPayloadBytes, err := json.Marshal(requestPayload)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "marshal_payload_failed"))
		logger().Error(trace_ctx, "Failed to marshal request payload",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Consensus.requestVoteResultFromBuddy"))
		return nil
	}

	reqMsg := PubSubMessages.NewMessageBuilder(nil).
		SetSender(consensus.Host.ID()).
		SetMessage(string(requestPayloadBytes)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(reqAck)

	reqData, err := json.Marshal(reqMsg)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "marshal_message_failed"))
		logger().Error(trace_ctx, "Failed to marshal request message",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Consensus.requestVoteResultFromBuddy"))
		return nil
	}

	if _, err := stream.Write([]byte(string(reqData) + string(rune(config.Delimiter)))); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "write_failed"))
		logger().Error(trace_ctx, "Failed to write request to peer",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Consensus.requestVoteResultFromBuddy"))
		return nil
	}

	logger().Info(trace_ctx, "Sent vote result request to peer",
		ion.String("peer_id", peerID.String()),
		ion.String("function", "Consensus.requestVoteResultFromBuddy"))

	// Read response with timeout
	response := consensus.readVoteResultResponse(stream, peerID)
	if response == "" {
		span.SetAttributes(attribute.String("status", "no_response"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Warn(trace_ctx, "No response received from peer",
			ion.String("peer_id", peerID.String()),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.requestVoteResultFromBuddy"))
		return nil
	}

	// Parse response and extract BLS result
	result := consensus.parseVoteResultResponse(response, peerID)
	if result != nil {
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(
			attribute.Float64("duration", duration),
			attribute.String("status", "success"),
			attribute.Bool("bls_result_received", true),
		)
		logger().Info(trace_ctx, "Successfully received vote result from peer",
			ion.String("peer_id", peerID.String()),
			ion.Bool("bls_agree", result.Agree),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.requestVoteResultFromBuddy"))
	} else {
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(
			attribute.Float64("duration", duration),
			attribute.String("status", "parse_failed"),
		)
		logger().Warn(trace_ctx, "Failed to parse vote result response",
			ion.String("peer_id", peerID.String()),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.requestVoteResultFromBuddy"))
	}
	return result
}

// readVoteResultResponse reads vote result response from stream with timeout
func (consensus *Consensus) readVoteResultResponse(stream network.Stream, peerID peer.ID) string {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.readVoteResultResponse")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("peer_id", peerID.String()))

	if common.LocalGRO == nil {
		var err error
		common.LocalGRO, err = common.InitializeGRO(GRO.SequencerConsensusLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			logger().Error(trace_ctx, "Failed to initialize local gro",
				err,
				ion.String("function", "Consensus.readVoteResultResponse"))
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
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(
			attribute.Int("response_size_bytes", len(resp)),
			attribute.Float64("duration", duration),
			attribute.String("status", "success"),
		)
		logger().Info(trace_ctx, "Successfully read vote result response",
			ion.String("peer_id", peerID.String()),
			ion.Int("response_size_bytes", len(resp)),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.readVoteResultResponse"))
		return resp
	case err := <-errCh:
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "read_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Warn(trace_ctx, "Failed to read response from peer",
			ion.Err(err),
			ion.String("peer_id", peerID.String()),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.readVoteResultResponse"))
		return ""
	case <-time.After(45 * time.Second):
		err := fmt.Errorf("timeout waiting for response")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "timeout"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().Warn(trace_ctx, "Timeout waiting for response from peer",
			ion.String("peer_id", peerID.String()),
			ion.Float64("timeout_seconds", 45.0),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.readVoteResultResponse"))
		return ""
	}
}

// parseVoteResultResponse parses vote result response and extracts BLS result
func (consensus *Consensus) parseVoteResultResponse(response string, peerID peer.ID) *BLS_Signer.BLSresponse {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.parseVoteResultResponse")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("peer_id", peerID.String()),
		attribute.Int("response_size_bytes", len(response)),
	)

	logger().Info(trace_ctx, "Parsing vote result response",
		ion.String("peer_id", peerID.String()),
		ion.String("function", "Consensus.parseVoteResultResponse"))

	responseMsg := PubSubMessages.NewMessageBuilder(nil).DeferenceMessage(response)
	if responseMsg == nil {
		span.SetAttributes(attribute.String("status", "parse_failed"), attribute.String("reason", "response_msg_nil"))
		logger().Warn(trace_ctx, "Failed to deference message",
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Consensus.parseVoteResultResponse"))
		return nil
	}

	var resultData map[string]interface{}
	if err := json.Unmarshal([]byte(responseMsg.Message), &resultData); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "unmarshal_failed"))
		logger().Error(trace_ctx, "Failed to unmarshal response message",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("function", "Consensus.parseVoteResultResponse"))
		return nil
	}

	// Extract and store numeric vote
	if result, ok := resultData["result"].(float64); ok {
		Maps.StoreVoteResult(consensus.ZKBlockData.GetZKBlock().BlockHash.Hex(), peerID.String(), int8(result))
		span.SetAttributes(attribute.Int64("vote_result", int64(result)))
		logger().Info(trace_ctx, "Received vote result from peer",
			ion.String("peer_id", peerID.String()),
			ion.Int64("vote_result", int64(result)),
			ion.String("function", "Consensus.parseVoteResultResponse"))
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

		span.SetAttributes(
			attribute.Bool("bls_present", true),
			attribute.Bool("bls_agree", agree),
			attribute.Int("pubkey_length", len(pub)),
			attribute.Int("signature_length", len(sig)),
		)

		duration := time.Since(startTime).Seconds()
		span.SetAttributes(
			attribute.Float64("duration", duration),
			attribute.String("status", "success"),
		)
		logger().Info(trace_ctx, "BLS result extracted from response",
			ion.String("peer_id", peerID.String()),
			ion.String("bls_peer_id", pid),
			ion.Bool("bls_agree", agree),
			ion.Int("pubkey_length", len(pub)),
			ion.Int("signature_length", len(sig)),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.parseVoteResultResponse"))

		return BLS_Signer.NewBLSresponseBuilder(nil).
			SetSignature(sig).
			SetAgree(agree).
			SetPubKey(pub).
			SetPeerID(pid).
			Build()
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "no_bls_data"),
	)
	logger().Warn(trace_ctx, "No BLS data in response",
		ion.String("peer_id", peerID.String()),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.parseVoteResultResponse"))
	return nil
}

// VerifyConsensusWithBLS verifies BLS signatures and determines if consensus was reached
// Returns true if consensus reached (majority agree), false otherwise
func (consensus *Consensus) VerifyConsensusWithBLS(blsResults []BLS_Signer.BLSresponse) bool {
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.VerifyConsensusWithBLS")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.Int("bls_results_count", len(blsResults)))

	// Context for the alerts
	alert_ctx := trace_ctx

	logger().Info(trace_ctx, "Verifying consensus with BLS signatures",
		ion.Int("bls_results_count", len(blsResults)),
		ion.String("function", "Consensus.VerifyConsensusWithBLS"))

	if len(blsResults) == 0 {
		err := fmt.Errorf("no BLS results collected - cannot verify consensus")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "no_results"))
		logger().Warn(trace_ctx, "No BLS results collected, skipping block processing",
			ion.String("function", "Consensus.VerifyConsensusWithBLS"))
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
			span.RecordError(err)
			logger().Warn(trace_ctx, "BLS verification failed for peer",
				ion.Err(err),
				ion.String("peer_id", r.PeerID),
				ion.Int64("vote", int64(vote)),
				ion.String("function", "Consensus.VerifyConsensusWithBLS"))
			continue
		}
		validTotal++
		votedPeers = append(votedPeers, fmt.Sprintf("  - %s (vote: %d)", r.PeerID, vote))
		if vote == 1 {
			validYes++
		}
	}

	span.SetAttributes(
		attribute.Int("valid_yes_votes", validYes),
		attribute.Int("valid_total_votes", validTotal),
	)

	if validTotal == 0 {
		err := fmt.Errorf("no valid BLS signatures - consensus failed")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "no_valid_signatures"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		msg := "❌ No valid BLS signatures - consensus failed, skipping block processing - No BLS results collected"
		logger().Error(trace_ctx, "No valid BLS signatures, consensus failed",
			err,
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.VerifyConsensusWithBLS"))
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_BFT_Consensus_NoBLSResultsCollected).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(msg).
			Send()
		return false
	}

	// Enforce quorum based on the fixed committee size (MaxMainPeers = 5)
	// We need a majority of the EXPECTED committee, regardless of how many responded.
	// For 5 peers, strict majority is 3.
	needed := (config.MaxMainPeers / 2) + 1
	peerVotesStr := strings.Join(votedPeers, "\n")

	span.SetAttributes(
		attribute.Int("needed_votes", needed),
		attribute.String("peer_votes", peerVotesStr),
	)

	if validYes >= needed {
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(
			attribute.Float64("duration", duration),
			attribute.String("status", "consensus_reached"),
			attribute.Bool("consensus_reached", true),
		)
		msg := fmt.Sprintf("✅ BFT Consensus Reached: %d/%d votes in favor (needed: %d)\nPeer votes:\n%s", validYes, validTotal, needed, peerVotesStr)
		logger().Info(trace_ctx, "BFT Consensus reached",
			ion.Int("yes_votes", validYes),
			ion.Int("total_votes", validTotal),
			ion.Int("needed_votes", needed),
			ion.String("peer_votes", peerVotesStr),
			ion.Float64("duration", duration),
			ion.String("function", "Consensus.VerifyConsensusWithBLS"))
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_BFT_Consensus_Reached).
			Status(Alerts.AlertStatusInfo).
			Severity(Alerts.SeverityInfo).
			Description(msg).
			Send()
		return true
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "consensus_failed"),
		attribute.Bool("consensus_reached", false),
	)
	msg := fmt.Sprintf("❌ Consensus failed: %d/%d votes in favor (needed: %d) - skipping block processing\nPeer votes:\n%s", validYes, validTotal, needed, peerVotesStr)
	logger().Warn(trace_ctx, "Consensus failed",
		ion.Int("yes_votes", validYes),
		ion.Int("total_votes", validTotal),
		ion.Int("needed_votes", needed),
		ion.String("peer_votes", peerVotesStr),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.VerifyConsensusWithBLS"))
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
	logger_ctx := context.Background()
	tracer := logger().Tracer("Consensus")
	trace_ctx, span := tracer.Start(logger_ctx, "Consensus.StartVoteCollection")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.String("block_hash", blockHash),
		attribute.String("protocol", string(config.SubmitMessageProtocol)),
		attribute.String("vote_stage", string(config.Type_SubmitVote)),
	)

	logger().Info(trace_ctx, "Starting vote collection",
		ion.String("block_hash", blockHash),
		ion.String("function", "Consensus.StartVoteCollection"))

	if !consensus.IsListenerActive() {
		err := fmt.Errorf("listener node not active - cannot collect votes")
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "listener_not_active"))
		logger().Error(trace_ctx, "Listener node not active",
			err,
			ion.String("function", "Consensus.StartVoteCollection"))
		return err
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
	)
	logger().Info(trace_ctx, "Vote collection started successfully",
		ion.String("block_hash", blockHash),
		ion.String("protocol", string(config.SubmitMessageProtocol)),
		ion.String("vote_stage", string(config.Type_SubmitVote)),
		ion.Float64("duration", duration),
		ion.String("function", "Consensus.StartVoteCollection"))
	return nil
}
