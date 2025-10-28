package Pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	Channel "gossipnode/Pubsub/DataProcessing/Channel"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"log"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type StructGossipPubSub struct {
	GossipPubSub *PubSubMessages.GossipPubSub
}

func (sgps *StructGossipPubSub) GetGossipPubSub() *PubSubMessages.GossipPubSub {
	return sgps.GossipPubSub
}

// NewGossipPubSub creates a new gossip pub/sub instance
func NewGossipPubSub(host host.Host, Protocol protocol.ID) (*StructGossipPubSub, error) {
	// Validate input parameters
	if host == nil {
		return nil, fmt.Errorf("host cannot be nil")
	}
	if Protocol == "" {
		return nil, fmt.Errorf("protocol cannot be empty")
	}

	GossipPubSubInput := PubSubMessages.NewGossipPubSubBuilder(nil).SetHost(host).SetProtocol(Protocol)
	gps := &StructGossipPubSub{
		GossipPubSub: GossipPubSubInput.Build(),
	}
	if gps.GossipPubSub == nil {
		return nil, fmt.Errorf("failed to create gossip pub/sub")
	}
	// Set up stream handler for gossip messages
	host.SetStreamHandler(Protocol, func(s network.Stream) {
		handleGossipStream(gps.GossipPubSub, s)
	})

	log.Printf("Gossip pub/sub initialized for host: %s", host.ID())
	return gps, nil
}

// CreateChannel creates a new channel with access control and self-subscription
func CreateChannel(gps *PubSubMessages.GossipPubSub, channelName string, isPublic bool, allowedPeers []peer.ID) error {

	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	if gps.ChannelAccess[channelName] != nil {
		return fmt.Errorf("channel %s already exists", channelName)
	}

	// Create allowed peers map
	allowedMap := make(map[peer.ID]bool)
	for _, peerID := range allowedPeers {
		// Convert peer ID to string
		allowedMap[peerID] = true
	}

	// Add creator to allowed peers
	allowedMap[gps.Host.ID()] = true

	// Create channel access control
	gps.ChannelAccess[channelName] = &PubSubMessages.ChannelAccess{
		ChannelName:  channelName,
		AllowedPeers: allowedMap,
		IsPublic:     isPublic,
		Creator:      gps.Host.ID(),
		CreatedAt:    time.Now().Unix(),
	}

	gps.Topics[channelName] = true
	log.Printf("Created channel: %s (public: %v, allowed peers: %d)", channelName, isPublic, len(allowedPeers))
	return nil
}

// RemovePeerFromChannel removes a peer from the allowed list of a channel
func RemovePeerFromChannel(gps *PubSubMessages.GossipPubSub, channelName string, peerID peer.ID) error {
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	access, exists := gps.ChannelAccess[channelName]
	if !exists {
		return fmt.Errorf("channel %s does not exist", channelName)
	}

	// Only creator can remove peers
	if gps.Host.ID() != access.Creator {
		return fmt.Errorf("only channel creator can remove peers")
	}

	delete(gps.ChannelAccess[channelName].AllowedPeers, peerID)
	log.Printf("Removed peer %s from channel %s", peerID, channelName)
	return nil
}

// handleGossipStream handles incoming gossip messages
func handleGossipStream(gps *PubSubMessages.GossipPubSub, s network.Stream) {
	defer s.Close()

	// Read message using delimiter
	messageBytes, err := readMessage(s)
	if err != nil {
		log.Printf("Error reading gossip message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	// Deserialize message
	var gossipMsg PubSubMessages.GossipMessage
	if err := json.Unmarshal(messageBytes, &gossipMsg); err != nil {
		log.Printf("Failed to unmarshal gossip message: %v", err)
		return
	}

	// Attach ACK if missing or if Data is nil
	if gossipMsg.Data == nil {
		fmt.Printf("Received message with nil Data - initializing new Message\n")
		log.Printf("Received message with nil Data - initializing new Message\n")
		gossipMsg.Data = PubSubMessages.NewMessageBuilder(nil).SetSender(gossipMsg.Sender)
	}

	if gossipMsg.Data.GetACK() == nil {
		fmt.Printf("Received message with nil ACK - attaching default ACK\n")
		log.Printf("Received message with nil ACK - attaching default ACK\n")

		// Create a default ACK with Type_Publish stage
		ack := PubSubMessages.NewACKBuilder().
			True_ACK_Message(gossipMsg.Sender, config.Type_Publish)

		gossipMsg.Data.SetACK(ack)
	}
	fmt.Printf("Received message with ACK: %+v\n", gossipMsg.Data.GetACK())
	fmt.Printf("==============================================\n")
	// Check if we've already seen this message
	gps.Mutex.Lock()
	if gps.MessageCache[gossipMsg.ID] {
		gps.Mutex.Unlock()
		return // Already processed
	}
	gps.MessageCache[gossipMsg.ID] = true
	gps.Mutex.Unlock()

	// Skip TTL checks for direct messages (TTL = 0)
	// TTL is only needed for multi-hop gossip propagation
	if gossipMsg.TTL == 0 {
		log.Printf("Processing direct message (no TTL needed): %s", gossipMsg.ID)
	} else if gossipMsg.TTL < 0 {
		log.Printf("Gossip message TTL expired: %s", gossipMsg.ID)
		return
	} else {
		// Only decrement TTL for actual gossip messages
		gossipMsg.TTL--
		log.Printf("Forwarding gossip message (TTL=%d): %s", gossipMsg.TTL, gossipMsg.ID)
	}

	log.Printf("Received gossip message from %s on topic %s: %s", gossipMsg.Sender, gossipMsg.Topic, gossipMsg.ID)
	// <-- Write the logic to check the processing of the message based on the message type --> TODO

	Channel.AppendMessage(&gossipMsg)
	// Call handler if we're subscribed to this topic
	gps.Mutex.RLock()
	handler, subscribed := gps.Handlers[gossipMsg.Topic]
	gps.Mutex.RUnlock()

	if subscribed && handler != nil {
		handler(&gossipMsg)
	}

	// Forward message to other peers (gossip)
	if gossipMsg.TTL > 0 {
		Publisher.GossipMessage(gps, messageBytes)
	}
}

// readMessage reads a message from the stream using delimiter
func readMessage(s network.Stream) ([]byte, error) {
	var message []byte
	buffer := make([]byte, 1)

	for {
		_, err := s.Read(buffer)
		if err != nil {
			return nil, err
		}

		if buffer[0] == config.Delimiter {
			break
		}

		message = append(message, buffer[0])
	}

	return message, nil
}

// GetTopics returns a list of subscribed topics
func GetTopics(gps *PubSubMessages.GossipPubSub) []string {
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()

	topics := make([]string, 0, len(gps.Topics))
	for topic := range gps.Topics {
		topics = append(topics, topic)
	}
	return topics
}

// GetPeerCount returns the number of connected peers
func GetPeerCount(gps *PubSubMessages.GossipPubSub) int {
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()
	return len(gps.Peers)
}

// GetPeers returns a list of connected peers
func GetPeers(gps *PubSubMessages.GossipPubSub) []peer.ID {
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()

	peers := make([]peer.ID, 0, len(gps.Peers))
	peers = append(peers, gps.Peers...)

	return peers
}

// Close closes the gossip pub/sub instance
func Close(gps *PubSubMessages.GossipPubSub) error {
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	for topic := range gps.Topics {
		Connector.Unsubscribe(gps, topic)
	}

	return nil
}

// HandlePeerFound is called when a new peer is discovered
func HandlePeerFound(gps *PubSubMessages.GossipPubSub, pi peer.AddrInfo) {
	log.Printf("Peer discovered: %s", pi.ID)

	// Don't add ourselves
	if pi.ID == gps.Host.ID() {
		log.Printf("Skipping self-discovery for peer: %s", pi.ID)
		return
	}

	// Validate that peer has addresses
	if len(pi.Addrs) == 0 {
		log.Printf("Peer %s has no addresses, skipping", pi.ID)
		return
	}

	// Check for duplicate peers before adding
	gps.Mutex.Lock()
	peerAlreadyExists := false
	for _, existingPeer := range gps.Peers {

		if existingPeer == pi.ID {
			peerAlreadyExists = true
			break
		}
	}

	if !peerAlreadyExists {
		// Use the largest multiaddr (following the pattern from utils.go)
		if pi.ID != "" {
			gps.Peers = append(gps.Peers, pi.ID)
			log.Printf("Added peer %s to peers list with address: %s", pi.ID, pi.Addrs)
		}
	} else {
		log.Printf("Peer %s already exists in peers list", pi.ID)
	}
	gps.Mutex.Unlock()

	// Connect to the discovered peer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := gps.Host.Connect(ctx, pi); err != nil {
		log.Printf("Failed to connect to discovered peer %s: %v", pi.ID, err)
	} else {
		log.Printf("Connected to peer: %s", pi.ID)
	}
}
