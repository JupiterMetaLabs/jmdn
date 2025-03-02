package node

import (
	"context"
	"fmt"

	"gossipnode/config"
	"gossipnode/messaging"
	"gossipnode/transfer"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	tcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

var localNode config.Node

// NewNode creates and starts a libp2p node
func NewNode() (*config.Node, error) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			config.IP6QUIC, // QUIC over IPv6
			config.IP6TCP,  // TCP over IPv6
			config.IP4QUIC, // QUIC over IPv4
			config.IP4TCP,  // TCP over IPv4
		),
		libp2p.Transport(tcp.NewTCPTransport),      // TCP transport
		libp2p.Transport(quic.NewTransport),        // QUIC transport
		libp2p.NATPortMap(),                        // NAT traversal
		libp2p.ForceReachabilityPublic(),           // Force public reachability
	)

	if err != nil {
		return nil, fmt.Errorf("failed to start libp2p: %v", err)
	}

	// Create the node
	localNode = config.Node{
		Host:       h,
		EnableQUIC: true,
	}

	// Set up stream handlers for messages (TCP) and files (QUIC)
	h.SetStreamHandler(config.MessageProtocol, messaging.HandleMessageStream)
	h.SetStreamHandler(config.FileProtocol, transfer.HandleFileStream)

	// Start peer discovery using mDNS (optional)
	go StartDiscovery(h)

	fmt.Printf("Node started with ID: %s\n", h.ID().String())
	fmt.Printf("Addresses: %v\n", h.Addrs())
	return &localNode, nil
}

// SendMessage sends a message to a peer (uses TCP)
func SendMessage(n *config.Node, target string, message string) error {
	peerInfo, err := getPeerInfo(target)
	if err != nil {
		return err
	}

	// Connect to the peer
	if err := n.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	return messaging.SendMessage(n , peerInfo.ID.String(), message)
}

// SendFile sends a file to a peer (uses QUIC)
func SendFile(n *config.Node,target string, filepath string) error {
	peerInfo, err := getPeerInfo(target)
	if err != nil {
		return err
	}

	// Connect to the peer
	if err := n.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	return transfer.SendFile(n.Host, peerInfo.ID, filepath)
}

// getPeerInfo extracts peer information from a multiaddress
func getPeerInfo(target string) (*peer.AddrInfo, error) {
	maddr, err := ma.NewMultiaddr(target)
	if err != nil {
		return nil, fmt.Errorf("invalid multiaddr: %v", err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return nil, fmt.Errorf("invalid peer address: %v", err)
	}

	return peerInfo, nil
}