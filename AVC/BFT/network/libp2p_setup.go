// =============================================================================
// FILE: pkg/network/libp2p_setup.go
// =============================================================================
package network

import (
	"context"
	"fmt"

	"github.com/JupiterMetaLabs/ion"
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

	logger().Info(context.Background(), "✅ libp2p host created\n")
	logger().Info(context.Background(), "   Peer ID: %s", h.ID())
	logger().Info(context.Background(), "   Listening on: %s", listenAddr)

	return h, ps, nil
}

// ConnectToPeers connects to bootstrap/peer nodes
func ConnectToPeers(ctx context.Context, h host.Host, peerAddrs []string) error {
	if len(peerAddrs) == 0 {
		logger().Warn(context.Background(), "No peers to connect to")
		return nil
	}

	logger().Info(context.Background(), "🔗 Connecting to %d peers...", len(peerAddrs))

	for _, addrStr := range peerAddrs {
		// Parse multiaddr
		maddr, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			logger().Info(context.Background(), "❌ Invalid peer address %s: %v", addrStr, err)
			continue
		}

		// Extract peer info
		peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			logger().Info(context.Background(), "❌ Failed to parse peer info from %s: %v", addrStr, err)
			continue
		}

		// Check if this is a self-connection attempt
		if peerInfo.ID == h.ID() {
			logger().Info(context.Background(), "🚫 Skipping self-connection attempt: %s", addrStr)
			continue
		}

		// Connect
		if err := h.Connect(ctx, *peerInfo); err != nil {
			logger().Info(context.Background(), "❌ Failed to connect to %s: %v", peerInfo.ID, err)
			continue
		}

		logger().Info(context.Background(), "✅ Connected to peer: %s", peerInfo.ID)
	}

	return nil
}

// SetupSimpleNetwork creates a local test network
func SetupSimpleNetwork(ctx context.Context, numNodes int, startPort int) ([]host.Host, []*pubsub.PubSub, error) {
	logger().Info(context.Background(), "🚀 Setting up local test network with %d nodes", numNodes)

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
	logger().Info(context.Background(), "Connecting nodes in full mesh")
	for i := 0; i < numNodes; i++ {
		for j := i + 1; j < numNodes; j++ {
			// Connect i to j
			peerInfo := peer.AddrInfo{
				ID:    hosts[j].ID(),
				Addrs: hosts[j].Addrs(),
			}

			if err := hosts[i].Connect(ctx, peerInfo); err != nil {
				logger().Info(context.Background(), "⚠️  Failed to connect node %d to node %d: %v", i, j, err)
			}
		}
	}

	logger().Info(context.Background(), "\n✅ Network setup complete! %d nodes connected", numNodes)
	return hosts, pubsubs, nil
}
