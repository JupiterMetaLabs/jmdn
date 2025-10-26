package Sequencer

import (
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/AVC/NodeSelection/Router"
	"gossipnode/Pubsub"
	"gossipnode/Sequencer/Metadata"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"log"
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
func (consensus *Consensus) QueryBuddyNodes() ([]peer.ID, error) {
	router := Router.NewNodeselectionRouter()
	buddies, err := router.GetBuddyNodes(config.MaxMainPeers + config.MaxBackupPeers)
	if err != nil {
		return nil, fmt.Errorf("failed to get buddy nodes: %v", err)
	}

	fmt.Printf("Queried Buddies: %+v\n", buddies)


	for _, buddy := range buddies {
		consensus.PeerList.MainPeers = append(consensus.PeerList.MainPeers, peer.ID(buddy.Node.ID))
	}
	return consensus.PeerList.MainPeers, nil
}

func (consensus *Consensus) AddBuddyNodesToPeerList(zkBlock *config.ZKBlock, buddies []peer.ID) (*PubSubMessages.ConsensusMessage, error) {

	ZKBlock := Metadata.ZKBlockMetadata(zkBlock, PubSubMessages.NewBuddiesBuilder(buddies)).SetEndTimeoutMetadata(time.Now().Add(config.ConsensusTimeout)).SetStartTimeMetadata(time.Now())
	if ZKBlock == nil {
		return nil, fmt.Errorf("failed to create ZKBlock metadata")
	}
	return ZKBlock.GetConsensusMessage(), nil
}

func (consensus *Consensus) Start(zkblock *config.ZKBlock) error {
	// Start the Loggers in the Streaming.go


	// Attach the metadata to the block
	// 1. Pull the buddies from the NodeSelectionRouter
	peerIDs, errMSG := consensus.QueryBuddyNodes()
	if errMSG != nil {
		return fmt.Errorf("failed to query buddy nodes: %v", errMSG)
	}

	// 2. Attach the buddies to the zkblock as metadata
	consensus.ZKBlockData, errMSG = consensus.AddBuddyNodesToPeerList(zkblock, peerIDs)
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
	// First create the pubsub channel
	var err error
	consensus.gossipnode, err = Pubsub.NewGossipPubSub(consensus.Host, config.PubSub_ConsensusChannel)
	if err != nil {
		return fmt.Errorf("failed to create pubsub: %v", err)
	}

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

	if err := Pubsub.CreateChannel(consensus.gossipnode.GetGossipPubSub(), config.PubSub_ConsensusChannel, true, allowedPeers); err != nil {
		return fmt.Errorf("failed to create pubsub channel: %v", err)
	}

	log.Printf("Successfully created pubsub channel: %s", config.PubSub_ConsensusChannel)

	// Initialize listener node for vote collection
	consensus.ListenerNode = MessagePassing.NewListenerNode(consensus.Host, consensus.ResponseHandler)
	log.Printf("Listener node initialized for vote collection on protocol: %s", config.SubmitMessageProtocol)

	// After creating the channel, ask peers to subscribe to the channel
	if err := consensus.RequestSubscriptionPermission(); err != nil {
		return fmt.Errorf("failed to request subscription permission: %v", err)
	}

	// Verify that nodes are actually subscribed
	if err := consensus.VerifySubscriptions(); err != nil {
		return fmt.Errorf("subscription verification failed: %w", err)
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
	err := AskForSubscription(consensus.gossipnode.GetGossipPubSub(), consensus.Channel, consensus)
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

	// Verify that we have the expected number of subscribers (13)
	if len(verifiedPeerIDs) != config.MaxMainPeers {
		return fmt.Errorf("incorrect number of verified peers: got %d, expected %d", len(verifiedPeerIDs), config.MaxMainPeers)
	}

	// Log all verified PeerIDs
	for connectionPeerID, responsePeerID := range verifiedPeerIDs {
		log.Printf("Verified subscription: connection peer %s -> response peer %s", connectionPeerID, responsePeerID)
	}

	log.Printf("Subscription verification successful: %d peers properly verified via pubsub messaging", len(verifiedPeerIDs))
	return nil
}

// GetVoteStats returns statistics about votes collected by the listener node
func (consensus *Consensus) GetVoteStats() map[string]interface{} {
	// Use global singleton to get listener node
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()

	if listenerNode == nil {
		return map[string]interface{}{
			"error": "listener node not initialized in global singleton",
		}
	}

	// Get metadata from the global listener node
	metadata := listenerNode.MetaData

	return map[string]interface{}{
		"votes_received":   metadata.Received,
		"votes_sent":       metadata.Sent,
		"total_messages":   metadata.Total,
		"last_updated":     metadata.UpdatedAt,
		"listener_peer_id": listenerNode.PeerID.String(),
	}
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
