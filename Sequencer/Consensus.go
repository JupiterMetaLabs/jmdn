package Sequencer

import (
	"fmt"
	"gossipnode/Pubsub"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"gossipnode/config/utils"
)

const (
	MaxMainPeers   = 13
	MaxBackupPeers = 3
)

type PeerList struct {
	MainPeers   []multiaddr.Multiaddr
	BackupPeers []multiaddr.Multiaddr
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
	// First create the pubsub channel
	var err error
	c.gps, err = Pubsub.NewGossipPubSub(c.Host, config.PubSub_ConsensusChannel)
	if err != nil {
		return fmt.Errorf("failed to create pubsub: %v", err)
	}

	// Create multiaddr with peer ID for access control
	hostAddrs := c.Host.Addrs()
	if len(hostAddrs) == 0 {
		return fmt.Errorf("host has no addresses")
	}

	hostMultiaddr := utils.GetMultiAddrs(c.Host)

	if err := c.gps.CreateChannel(config.PubSub_ConsensusChannel, true, hostMultiaddr); err != nil {
		return fmt.Errorf("failed to create pubsub channel: %v", err)
	}
	return nil
}

func (c *Consensus) addPeersToChannel() {
	if c.gps == nil {
		fmt.Printf("GossipPubSub not initialized, cannot add peers to channel\n")
		return
	}

	// Add main peers
	for _, peerAddr := range c.PeerList.MainPeers {
		// Extract peer ID from multiaddr
		peerInfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			fmt.Printf("Failed to extract peer info from %s: %v\n", peerAddr, err)
			continue
		}

		if err := c.gps.AddPeerToChannel(config.PubSub_ConsensusChannel, peerInfo.ID); err != nil {
			fmt.Printf("Failed to add main peer %s to pubsub channel: %v\n", peerInfo.ID, err)
		}
	}

	// Add backup peers
	for _, peerAddr := range c.PeerList.BackupPeers {
		// Extract peer ID from multiaddr
		peerInfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			fmt.Printf("Failed to extract peer info from %s: %v\n", peerAddr, err)
			continue
		}

		if err := c.gps.AddPeerToChannel(config.PubSub_ConsensusChannel, peerInfo.ID); err != nil {
			fmt.Printf("Failed to add backup peer %s to pubsub channel: %v\n", peerInfo.ID, err)
		}
	}
}
