package messaging

import (
	"bufio"
	AppContext "gossipnode/config/Context"
	"fmt"
	"gossipnode/metrics"
	"time"

	"gossipnode/config"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)
const(
	MessageAppContext = "message"
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

	remotePeer := s.Conn().RemotePeer().String()
	metrics.MessagesReceivedCounter.WithLabelValues("message", remotePeer).Inc()
	metrics.MessageSizeHistogram.WithLabelValues("message", "received").Observe(float64(len(msg)))

	fmt.Printf("\n\033[1;42m                               \033[0m\n")
	fmt.Printf("\033[1;42m  MESSAGE RECEIVED              \033[0m\n")
	fmt.Printf("\033[1;42m                               \033[0m\n\n")
	fmt.Printf("From: \033[1;36m%s\033[0m\n", remotePeer)
	fmt.Printf("Time: \033[1;33m%s\033[0m\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Printf("\nContent:\n\033[1;37m%s\033[0m\n\n", msg)

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
	ctx, cancel := AppContext.GetAppContext(MessageAppContext).NewChildContext()
	defer cancel()
	if err := n.Host.Connect(ctx, *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	// Open a stream with MessageProtocol (TCP)
	s, err := n.Host.NewStream(ctx, peerInfo.ID, config.MessageProtocol)
	if err != nil {
		return fmt.Errorf("stream failed: %v", err)
	}
	defer s.Close()

	// Record message metrics
	metrics.MessagesSentCounter.WithLabelValues("message", peerInfo.ID.String()).Inc()
	metrics.MessageSizeHistogram.WithLabelValues("message", "sent").Observe(float64(len(message)))

	// Send the message
	_, err = fmt.Fprintf(s, "%s\n", message)
	if err != nil {
		return fmt.Errorf("send failed: %v", err)
	}
	fmt.Printf("Sent message to %s: %s", peerInfo.ID.String(), message)
	return nil
}
