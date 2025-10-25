// =============================================================================
// FILE: pkg/network/libp2p_setup.go
// =============================================================================
package network

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// SetupLibp2pHost creates a libp2p host and GossipSub for BFT
func SetupLibp2pHost(ctx context.Context, port int) (host.Host, *pubsub.PubSub, error) {
	// Create libp2p host
	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.DefaultSecurity,
		libp2p.DefaultMuxers,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	// Create GossipSub
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		h.Close()
		return nil, nil, fmt.Errorf("failed to create gossipsub: %w", err)
	}

	fmt.Printf("✅ libp2p host created\n")
	fmt.Printf("   Peer ID: %s\n", h.ID())
	fmt.Printf("   Listening on: %s\n", listenAddr)

	return h, ps, nil
}

// ConnectToPeers connects to bootstrap/peer nodes
func ConnectToPeers(ctx context.Context, h host.Host, peerAddrs []string) error {
	if len(peerAddrs) == 0 {
		fmt.Println("⚠️  No peers to connect to")
		return nil
	}

	fmt.Printf("🔗 Connecting to %d peers...\n", len(peerAddrs))

	for _, addrStr := range peerAddrs {
		// Parse multiaddr
		maddr, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			fmt.Printf("❌ Invalid peer address %s: %v\n", addrStr, err)
			continue
		}

		// Extract peer info
		peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			fmt.Printf("❌ Failed to parse peer info from %s: %v\n", addrStr, err)
			continue
		}

		// Connect
		if err := h.Connect(ctx, *peerInfo); err != nil {
			fmt.Printf("❌ Failed to connect to %s: %v\n", peerInfo.ID, err)
			continue
		}

		fmt.Printf("✅ Connected to peer: %s\n", peerInfo.ID)
	}

	return nil
}

// SetupSimpleNetwork creates a local test network
func SetupSimpleNetwork(ctx context.Context, numNodes int, startPort int) ([]host.Host, []*pubsub.PubSub, error) {
	fmt.Printf("🚀 Setting up local test network with %d nodes\n", numNodes)

	hosts := make([]host.Host, numNodes)
	pubsubs := make([]*pubsub.PubSub, numNodes)

	// Create all hosts
	for i := 0; i < numNodes; i++ {
		h, ps, err := SetupLibp2pHost(ctx, startPort+i)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to setup node %d: %w", i, err)
		}
		hosts[i] = h
		pubsubs[i] = ps
	}

	// Connect them all together (full mesh for testing)
	fmt.Println("\n🔗 Connecting nodes in full mesh...")
	for i := 0; i < numNodes; i++ {
		for j := i + 1; j < numNodes; j++ {
			// Connect i to j
			peerInfo := peer.AddrInfo{
				ID:    hosts[j].ID(),
				Addrs: hosts[j].Addrs(),
			}

			if err := hosts[i].Connect(ctx, peerInfo); err != nil {
				fmt.Printf("⚠️  Failed to connect node %d to node %d: %v\n", i, j, err)
			}
		}
	}

	fmt.Printf("\n✅ Network setup complete! %d nodes connected\n", numNodes)
	return hosts, pubsubs, nil
}
