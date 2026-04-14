package Sequencer

import (
	"context"
	"fmt"
	"log"
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
	"gossipnode/messaging"

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
func (consensus *Consensus) warmup() ([]PubSubMessages.Buddy_PeerMultiaddr, error) {

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

	log.Printf("Cleared previous round vote results at start of consensus round")

	buddies, errMSG := helper.QueryBuddyNodes()
	if errMSG != nil {
		return nil, fmt.Errorf("failed to query buddy nodes: %v", errMSG)
	}

	log.Printf("Queried %d buddy node candidates from NodeSelectionRouter", len(buddies))

	// Deduplicate buddies by peer.ID (buddies may have multiple multiaddrs per peer)
	candidates := helper.GetUniqueBuddyPeers(buddies)

	log.Printf("got: %d candidates after deduplication", len(candidates))

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
func (consensus *Consensus) BroadcastAndProcessBlock(blsResults []BLS_Signer.BLSresponse, consensusReached bool) error {
	// Context for the alerts
	alert_ctx := context.Background()
	defer alert_ctx.Done()

	// CRITICAL FIX: Clean up subscriptions when consensus round completes (success or failure)
	// This prevents subscription accumulation over long-running consensus operations
	defer consensus.CleanupSubscriptions()

	consensus.mu.Lock()
	defer consensus.mu.Unlock()

	if consensus.ZKBlockData == nil || consensus.ZKBlockData.GetZKBlock() == nil {
		ErrorMessage := "CONSENSUSERROR.BROADCASTANDPROCESSBLOCK: ZKBlockData not initialized"
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_ProcessBlockFailed_ZKBlockDataNotSet).
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(ErrorMessage).
			Send()
		return fmt.Errorf("ZKBlockData not initialized, error: %s", ErrorMessage)
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
		return fmt.Errorf("failed to broadcast block with BLS results: %v", err)
	}

	fmt.Printf("✅ Broadcasted block with %d BLS results\n", len(blsResults))

	// Only process block locally if consensus was reached
	if consensusReached {
		deployments, err := messaging.ProcessBlockLocally(block, blsResults)
		if err != nil {
			ErrorMessage := fmt.Sprintf("CONSENSUSERROR.BROADCASTANDPROCESSBLOCK: Failed to process block locally after broadcast: %v", err)
			Alerts.NewAlertBuilder(alert_ctx).
				AlertName(helper.Alert_Consensus_ProcessBlockFailed_FailedToProcessBlockLocally).
				Status(Alerts.AlertStatusError).
				Severity(Alerts.SeverityError).
				Description(ErrorMessage).
				Send()
			fmt.Printf("%s", ErrorMessage)
			return fmt.Errorf("failed to process block locally after broadcast: %v, error: %s", err, ErrorMessage)
		}
		// Propagate any newly-deployed contracts to peers (sequencer-only, fire-and-forget).
		if len(deployments) > 0 {
			go messaging.PropagateContractDeployments(consensus.Host, deployments)
		}
		msg := fmt.Sprintf("✅ Processed block locally - account balances updated\nBlock #%d\n(hash: %s)", block.BlockNumber, block.BlockHash.Hex())
		fmt.Printf("%s", msg)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_ProcessBlockSuccess_BlockProcessedLocally).
			Status(Alerts.AlertStatusSuccess).
			Severity(Alerts.SeveritySuccess).
			Description(msg).
			Send()
	} else {
		msg := fmt.Sprintf("CONSENSUSERROR.BROADCASTANDPROCESSBLOCK: Consensus not reached\nBlock #%d\n(hash: %s)", block.BlockNumber, block.BlockHash.Hex())
		fmt.Printf("%s", msg)
		Alerts.NewAlertBuilder(alert_ctx).
			AlertName(helper.Alert_Consensus_ProcessBlockFailed_ConsensusNotReached).
			Status(Alerts.AlertStatusWarning).
			Severity(Alerts.SeverityWarning).
			Description(msg).
			Send()
		return fmt.Errorf("consensus not reached, error: %s", msg)
	}

	return nil
}

// CleanupSubscriptions unsubscribes from consensus-related topics to prevent resource leaks
// This should be called after each consensus round completes (success or failure)
func (consensus *Consensus) CleanupSubscriptions() {
	if consensus.gossipnode == nil {
		return
	}

	gps := consensus.gossipnode.GetGossipPubSub()
	if gps == nil {
		return
	}

	// Unsubscribe from consensus channel
	if err := Subscription.Unsubscribe(gps, config.PubSub_ConsensusChannel); err != nil {
		log.Printf("⚠️ Failed to unsubscribe from consensus channel: %v", err)
	} else {
		log.Printf("✅ Cleaned up consensus channel subscription")
	}

	// Unsubscribe from CRDT sync channel
	if err := Subscription.Unsubscribe(gps, config.Pubsub_CRDTSync); err != nil {
		// This may fail if we never subscribed - that's OK
		log.Printf("⚠️ Failed to unsubscribe from CRDT sync channel: %v (may not have been subscribed)", err)
	}
}
