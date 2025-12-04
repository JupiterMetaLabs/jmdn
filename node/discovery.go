package node

import (
	AppContext "gossipnode/config/Context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

const(
	DiscoveryAppContext = "discovery"
)

// discoveryHandler handles newly discovered peers
type discoveryHandler struct {
	h host.Host
}

// HandlePeerFound implements the discovery.Notifee interface
func (d *discoveryHandler) HandlePeerFound(pi peer.AddrInfo) {
	fmt.Printf("Discovered peer: %s\n", pi.ID.String())
	ctx, cancel := AppContext.GetAppContext(DiscoveryAppContext).NewChildContext()
	defer cancel()
	d.h.Connect(ctx, pi)
}

// StartDiscovery sets up mDNS discovery
func StartDiscovery(h host.Host) {
	service := discovery.NewMdnsService(h, "custom-libp2p-network", &discoveryHandler{h})
	if err := service.Start(); err != nil {
		fmt.Println("Discovery error:", err)
		return
	}
}