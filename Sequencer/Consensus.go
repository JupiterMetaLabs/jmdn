package Sequencer

import (
	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/multiformats/go-multiaddr"
)

type Consensus struct{
	Channel string
	PeerList []multiaddr.Multiaddr
	Host host.Host
}

func NewConsensus(peerList []multiaddr.Multiaddr, host host.Host) *Consensus {
	return &Consensus{
		PeerList: peerList,
		Host: host,
		Channel: config.PubSub_ConsensusChannel,
	}
}

func (c *Consensus) Start() {
	// First create the pubsub channel
	
}


