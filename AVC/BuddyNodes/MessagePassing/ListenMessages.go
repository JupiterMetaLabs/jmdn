package MessagePassing

import (
	"bufio"
	"context"
	"fmt"
	"gossipnode/config"
	"log"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// HandleBuddyNodesMessageStream handles incoming messages on the buddy nodes protocol
func (buddy *BuddyNode) HandleBuddyNodesMessageStream(s network.Stream) {
	defer s.Close()

	// Update metadata
	buddy.Mutex.Lock()
	buddy.MetaData.Received++
	buddy.MetaData.Total++
	buddy.MetaData.UpdatedAt = time.Now()
	buddy.Mutex.Unlock()

	reader := bufio.NewReader(s)
	msg, err := reader.ReadString(config.Delimiter)
	if err != nil {
		log.Printf("Error reading message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	// Remove the delimiter from the message
	msg = strings.TrimSuffix(msg, string(rune(config.Delimiter)))

	log.Printf("Received buddy message from %s: %s", s.Conn().RemotePeer(), msg)

	switch msg {
	case config.Type_StartPubSub:
		// If node is okay to listen for subscriptions, then return ACK True
		if buddy.Host != nil && buddy.Network != nil {
			// Node is ready to listen for subscriptions
			ackMessage := config.Type_ACK_True
			if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.Printf("Sent ACK_TRUE to %s for pubsub subscription", s.Conn().RemotePeer())
			}
		} else {
			// Node is not ready to listen for subscriptions
			ackMessage := config.Type_ACK_False
			if err := buddy.SendMessageToPeer(s.Conn().RemotePeer(), ackMessage); err != nil {
				log.Printf("Failed to send ACK to %s: %v", s.Conn().RemotePeer(), err)
			} else {
				log.Printf("Sent ACK_FALSE to %s - node not ready for pubsub", s.Conn().RemotePeer())
			}
		}
	default:
		log.Printf("Unknown message type received from %s: %s", s.Conn().RemotePeer(), msg)
	}
}

// NewBuddyNode creates a new BuddyNode instance from an existing host
func NewBuddyNode(h host.Host, buddies *Buddies) *BuddyNode {
	buddy := &BuddyNode{
		Host:       h,
		Network:    h.Network(),
		PeerID:     h.ID(),
		BuddyNodes: *buddies,
		MetaData: MetaData{
			Received:  0,
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now(),
		},
	}

	// Set up the stream handler for the buddy nodes message protocol
	h.SetStreamHandler(config.BuddyNodesMessageProtocol, buddy.HandleBuddyNodesMessageStream)

	log.Printf("BuddyNode initialized with ID: %s", h.ID())
	log.Printf("Listening for buddy messages on protocol: %s", config.BuddyNodesMessageProtocol)

	return buddy
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
func (buddy *BuddyNode) SendMessageToPeer(peerID peer.ID, message string) error {
	// Create a stream to the peer
	stream, err := buddy.Host.NewStream(context.Background(), peerID, config.BuddyNodesMessageProtocol)
	if err != nil {
		return fmt.Errorf("failed to create stream to %s: %v", peerID, err)
	}
	defer stream.Close()

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	if message != config.Type_ACK_True && message != config.Type_ACK_False {
		// Update metadata
		buddy.Mutex.Lock()
		buddy.MetaData.Sent++
		buddy.MetaData.Total++
		buddy.MetaData.UpdatedAt = time.Now()
		buddy.Mutex.Unlock()

		log.Printf("Sent buddy message to %s: %s", peerID, message)
	}

	return nil
}
