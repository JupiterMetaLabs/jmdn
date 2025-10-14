package Pubsub

import (
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type Pubsub_Util interface{
	GetLargestMultiaddr(multiaddrs []multiaddr.Multiaddr) multiaddr.Multiaddr
	GetPingableMultiaddr(host host.Host, multiaddrs []multiaddr.Multiaddr) (multiaddr.Multiaddr, error)
	GetPeerIDFromMultiaddr(multiaddr multiaddr.Multiaddr) (peer.ID, error)
}

func GetLargestMultiaddr(multiaddrs []multiaddr.Multiaddr) multiaddr.Multiaddr {
	return getLargestMultiaddr(multiaddrs)
}

func GetPingableMultiaddr(host host.Host, multiaddrs []multiaddr.Multiaddr) (multiaddr.Multiaddr, error) {
	return getPingableMultiaddr(host, multiaddrs)
}

func GetPeerIDFromMultiaddr(multiaddr multiaddr.Multiaddr) (peer.ID, error) {
	return getPeerIDFromMultiaddr(multiaddr)
}