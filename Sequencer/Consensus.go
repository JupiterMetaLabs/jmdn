package Sequencer

import (
	"fmt"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"log"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	MaxMainPeers   = 13
	MaxBackupPeers = 3
)

type PeerList struct {
	MainPeers   []peer.ID
	BackupPeers []peer.ID
}
type Consensus struct {
	Channel  string
	PeerList PeerList
	Host     host.Host
	gps      *Pubsub.GossipPubSub
}

func NewConsensus(peerList PeerList, host host.Host) *Consensus {
	return &Consensus{
		PeerList: peerList,
		Host:     host,
		Channel:  config.PubSub_ConsensusChannel,
	}
}

func (c *Consensus) Start() error {
	// Validate consensus configuration first
	if err := ValidateConsensusConfiguration(c); err != nil {
		return fmt.Errorf("invalid consensus configuration: %w", err)
	}

	// First create the pubsub channel
	var err error
	c.gps, err = Pubsub.NewGossipPubSub(c.Host, config.PubSub_ConsensusChannel)
	if err != nil {
		return fmt.Errorf("failed to create pubsub: %v", err)
	}

	// Create allowed peers list (1 creator + 13 main + 3 backup = 17 total)
	allowedPeers := make([]peer.ID, 0, MaxMainPeers+MaxBackupPeers+1)

	// Add the creator (host) to the allowed list
	allowedPeers = append(allowedPeers, c.Host.ID())

	// Add main peers to the allowed list
	allowedPeers = append(allowedPeers, c.PeerList.MainPeers...)

	// Add backup peers to the allowed list
	allowedPeers = append(allowedPeers, c.PeerList.BackupPeers...)

	log.Printf("Creating pubsub channel with %d allowed peers (1 creator + %d main + %d backup)",
		len(allowedPeers), len(c.PeerList.MainPeers), len(c.PeerList.BackupPeers))

	if err := c.gps.CreateChannel(config.PubSub_ConsensusChannel, true, allowedPeers); err != nil {
		return fmt.Errorf("failed to create pubsub channel: %v", err)
	}

	log.Printf("Successfully created pubsub channel: %s", config.PubSub_ConsensusChannel)

	// After creating the channel, ask peers to subscribe to the channel
	if err := c.RequestSubscriptionPermission(); err != nil {
		return fmt.Errorf("failed to request subscription permission: %v", err)
	}

	// Verify that nodes are actually subscribed
	if err := c.VerifySubscriptions(); err != nil {
		return fmt.Errorf("subscription verification failed: %w", err)
	}

	return nil
}

func (c *Consensus) AddPeersToChannel() {
	if c.gps == nil {
		fmt.Printf("GossipPubSub not initialized, cannot add peers to channel\n")
		return
	}

	// Add main peers
	for _, peerID := range c.PeerList.MainPeers {
		if err := c.gps.AddPeerToChannel(config.PubSub_ConsensusChannel, peerID); err != nil {
			fmt.Printf("Failed to add main peer %s to pubsub channel: %v\n", peerID, err)
		}
	}

	// Add backup peers
	for _, peerID := range c.PeerList.BackupPeers {
		if err := c.gps.AddPeerToChannel(config.PubSub_ConsensusChannel, peerID); err != nil {
			fmt.Printf("Failed to add backup peer %s to pubsub channel: %v\n", peerID, err)
		}
	}
}

// RequestSubscriptionPermission asks all buddy nodes for permission to subscribe to the consensus channel
// Ensures: 1 creator + 13 subscribers = 14 total nodes
// Maximum 3 main nodes can fail, use backup nodes as replacements
func (c *Consensus) RequestSubscriptionPermission() error {
	if c.gps == nil {
		return fmt.Errorf("GossipPubSub not initialized")
	}

	// Validate consensus configuration first
	if err := ValidateConsensusConfiguration(c); err != nil {
		return fmt.Errorf("invalid consensus configuration: %w", err)
	}

	log.Printf("Requesting subscription permission from buddy nodes for channel: %s", c.Channel)

	// Use the AskForSubscription function from Communication.go
	err := AskForSubscription(c.gps, c.Channel, c)
	if err != nil {
		return fmt.Errorf("failed to get subscription permission: %w", err)
	}

	log.Printf("Successfully obtained subscription permission: 1 creator + 13 subscribers = 14 total nodes")
	return nil
}

// VerifySubscriptions checks if nodes are actually subscribed to the pubsub channel
// This method now uses the new pubsub-based verification system
func (c *Consensus) VerifySubscriptions() error {
	if c.gps == nil {
		return fmt.Errorf("GossipPubSub not initialized")
	}

	log.Printf("Starting subscription verification using pubsub messaging...")

	// Use the new VerifySubscriptions function from Communication.go
	verifiedPeerIDs, err := VerifySubscriptions(c.gps, c)
	if err != nil {
		return fmt.Errorf("failed to verify subscriptions: %v", err)
	}

	log.Printf("Received verification responses from %d peers", len(verifiedPeerIDs))

	// Verify that we have the expected number of subscribers (13)
	if len(verifiedPeerIDs) != MaxMainPeers {
		return fmt.Errorf("incorrect number of verified peers: got %d, expected %d", len(verifiedPeerIDs), MaxMainPeers)
	}

	// Log all verified PeerIDs
	for connectionPeerID, responsePeerID := range verifiedPeerIDs {
		log.Printf("Verified subscription: connection peer %s -> response peer %s", connectionPeerID, responsePeerID)
	}

	log.Printf("Subscription verification successful: %d peers properly verified via pubsub messaging", len(verifiedPeerIDs))
	return nil
}
