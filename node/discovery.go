package node

import (
	"context"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

// discoveryHandler handles newly discovered peers
type discoveryHandler struct {
	h host.Host
}

// HandlePeerFound implements the discovery.Notifee interface
func (d *discoveryHandler) HandlePeerFound(pi peer.AddrInfo) {
	logger().Info(context.Background(), "Discovered peer",
		ion.String("peer", pi.ID.String()))
	d.h.Connect(context.Background(), pi)
}

// StartDiscovery sets up mDNS discovery
func StartDiscovery(h host.Host) {
	service := discovery.NewMdnsService(h, "custom-libp2p-network", &discoveryHandler{h})
	if err := service.Start(); err != nil {
		logger().Error(context.Background(), "Discovery error", err)
		return
	}
}
