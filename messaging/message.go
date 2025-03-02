package messaging

import (
	"bufio"
	"context"
	"fmt"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"	
	"gossipnode/config"
)

// HandleMessageStream processes incoming messages (TCP)
func HandleMessageStream(s network.Stream) {
	defer s.Close()
	reader := bufio.NewReader(s)
	msg, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Error reading message:", err)
		return
	}
	fmt.Printf("Received message from %s: %s", s.Conn().RemotePeer().String(), msg)
}

// SendMessage sends a message to a peer (uses TCP)
func SendMessage(n *config.Node, target string, message string) error {
	maddr, err := ma.NewMultiaddr(target)
	if err != nil {
		return fmt.Errorf("invalid multiaddr: %v", err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("invalid peer address: %v", err)
	}

	// Connect to the peer
	if err := n.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	// Open a stream with MessageProtocol (TCP)
	s, err := n.Host.NewStream(context.Background(), peerInfo.ID, config.MessageProtocol)
	if err != nil {
		return fmt.Errorf("stream failed: %v", err)
	}
	defer s.Close()

	// Send the message
	_, err = fmt.Fprintf(s, "%s\n", message)
	if err != nil {
		return fmt.Errorf("send failed: %v", err)
	}
	fmt.Printf("Sent message to %s: %s", peerInfo.ID.String(), message)
	return nil
}