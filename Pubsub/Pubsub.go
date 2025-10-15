package Pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/config"
	"log"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// NewGossipPubSub creates a new gossip pub/sub instance
func NewGossipPubSub(host host.Host, Protocol protocol.ID) (*GossipPubSub, error) {
	gps := &GossipPubSub{
		Host:          host,
		topics:        make(map[string]bool),
		handlers:      make(map[string]func(*GossipMessage)),
		messageCache:  make(map[string]bool),
		channelAccess: make(map[string]*ChannelAccess),
		peers:         make([]peer.ID, 0),
		Protocol:      Protocol,
	}

	// Set up stream handler for gossip messages
	host.SetStreamHandler(Protocol, gps.handleGossipStream)

	log.Printf("Gossip pub/sub initialized for host: %s", host.ID())
	return gps, nil
}

// CreateChannel creates a new channel with access control and self-subscription
func (gps *GossipPubSub) CreateChannel(channelName string, isPublic bool, allowedPeers []peer.ID) error {
	gps.mutex.Lock()
	defer gps.mutex.Unlock()

	if gps.channelAccess[channelName] != nil {
		return fmt.Errorf("channel %s already exists", channelName)
	}

	// Create allowed peers map
	allowedMap := make(map[peer.ID]bool)
	for _, peerID := range allowedPeers {
		// Convert multiaddr to string
		allowedMap[peerID] = true
	}

	// Add creator to allowed peers
	allowedMap[gps.Host.ID()] = true

	// Create channel access control
	gps.channelAccess[channelName] = &ChannelAccess{
		ChannelName:  channelName,
		AllowedPeers: allowedMap,
		IsPublic:     isPublic,
		Creator:      gps.Host.ID(),
		CreatedAt:    time.Now().Unix(),
	}

	gps.topics[channelName] = true
	log.Printf("Created channel: %s (public: %v, allowed peers: %d)", channelName, isPublic, len(allowedPeers))
	return nil
}

// AddPeerToChannel adds a peer to the allowed list of a channel
func (gps *GossipPubSub) AddPeerToChannel(channelName string, peerID peer.ID) error {
	gps.mutex.Lock()
	defer gps.mutex.Unlock()

	access, exists := gps.channelAccess[channelName]
	if !exists {
		return fmt.Errorf("channel %s does not exist", channelName)
	}

	if gps.Host.ID() != access.Creator {
		return fmt.Errorf("only channel creator can add peers")
	}

	access.AllowedPeers[peerID] = true
	log.Printf("Added peer %s to channel %s", peerID, channelName)
	return nil
}

// RemovePeerFromChannel removes a peer from the allowed list of a channel
func (gps *GossipPubSub) RemovePeerFromChannel(channelName string, peerID peer.ID) error {
	gps.mutex.Lock()
	defer gps.mutex.Unlock()

	access, exists := gps.channelAccess[channelName]
	if !exists {
		return fmt.Errorf("channel %s does not exist", channelName)
	}

	// Only creator can remove peers
	if gps.Host.ID() != access.Creator {
		return fmt.Errorf("only channel creator can remove peers")
	}

	delete(access.AllowedPeers, peerID)
	log.Printf("Removed peer %s from channel %s", peerID, channelName)
	return nil
}

// CanSubscribe checks if a peer can subscribe to a channel
func (gps *GossipPubSub) CanSubscribe(channelName string, peerID peer.ID) bool {
	gps.mutex.RLock()
	defer gps.mutex.RUnlock()

	access, exists := gps.channelAccess[channelName]
	if !exists {
		return false // Channel doesn't exist
	}

	// Public channels allow anyone
	if access.IsPublic {
		return true
	}

	// Check if peer is in allowed list
	return access.AllowedPeers[peerID]
}

// Subscribe subscribes to a topic with access control
func (gps *GossipPubSub) Subscribe(topic string, handler func(*GossipMessage)) error {
	// Check if we can subscribe to this channel
	hostMultiAddr := gps.Host.ID()
	if !gps.CanSubscribe(topic, hostMultiAddr) {
		return fmt.Errorf("access denied: not authorized to subscribe to channel %s", topic)
	}

	gps.mutex.Lock()
	defer gps.mutex.Unlock()

	gps.topics[topic] = true
	gps.handlers[topic] = handler

	log.Printf("Subscribed to topic: %s", topic)
	return nil
}

// Publish publishes a message to a topic
func (gps *GossipPubSub) Publish(topic string, data interface{}, metadata map[string]interface{}) error {
	// Create message
	message := &GossipMessage{
		ID:        fmt.Sprintf("%s-%d", gps.Host.ID().String(), gps.messageID),
		Topic:     topic,
		Data:      data,
		Sender:    gps.Host.ID(),
		Timestamp: time.Now().Unix(),
		TTL:       10, // Default TTL
		Metadata:  metadata,
	}
	gps.messageID++

	// Serialize message
	messageBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	// Add to message cache to prevent loops
	gps.mutex.Lock()
	gps.messageCache[message.ID] = true
	gps.mutex.Unlock()

	// Gossip to all connected peers
	gps.gossipMessage(messageBytes)

	log.Printf("Published message to topic %s: %s", topic, message.ID)
	return nil
}

// handleGossipStream handles incoming gossip messages
func (gps *GossipPubSub) handleGossipStream(s network.Stream) {
	defer s.Close()

	// Read message using delimiter
	messageBytes, err := gps.readMessage(s)
	if err != nil {
		log.Printf("Error reading gossip message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	// Deserialize message
	var gossipMsg GossipMessage
	if err := json.Unmarshal(messageBytes, &gossipMsg); err != nil {
		log.Printf("Failed to unmarshal gossip message: %v", err)
		return
	}

	// Check if we've already seen this message
	gps.mutex.Lock()
	if gps.messageCache[gossipMsg.ID] {
		gps.mutex.Unlock()
		return // Already processed
	}
	gps.messageCache[gossipMsg.ID] = true
	gps.mutex.Unlock()

	// Check TTL
	if gossipMsg.TTL <= 0 {
		log.Printf("Gossip message TTL expired: %s", gossipMsg.ID)
		return
	}

	// Decrement TTL for forwarding
	gossipMsg.TTL--

	log.Printf("Received gossip message from %s on topic %s: %s", gossipMsg.Sender, gossipMsg.Topic, gossipMsg.ID)

	// Call handler if we're subscribed to this topic
	gps.mutex.RLock()
	handler, subscribed := gps.handlers[gossipMsg.Topic]
	gps.mutex.RUnlock()

	if subscribed && handler != nil {
		handler(&gossipMsg)
	}

	// Forward message to other peers (gossip)
	if gossipMsg.TTL > 0 {
		gps.gossipMessage(messageBytes)
	}
}

// readMessage reads a message from the stream using delimiter
func (gps *GossipPubSub) readMessage(s network.Stream) ([]byte, error) {
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

// writeMessage writes a message to the stream using delimiter
func (gps *GossipPubSub) writeMessage(s network.Stream, message []byte) error {
	_, err := s.Write(append(message, config.Delimiter))
	return err
}

// gossipMessage forwards a message to connected peers
func (gps *GossipPubSub) gossipMessage(messageBytes []byte) {
	gps.mutex.RLock()

	for _, peerID := range gps.peers {
		// Don't send to ourselves
		if peerID == gps.Host.ID() {
			continue
		}

		go func(p peer.ID) {
			if err := gps.sendToPeer(p, messageBytes); err != nil {
				log.Printf("Failed to gossip message to %s: %v", p, err)
			}
		}(peerID)
	}
}

// sendToPeer sends a message to a specific peer
func (gps *GossipPubSub) sendToPeer(peerID peer.ID, messageBytes []byte) error {
	stream, err := gps.Host.NewStream(context.Background(), peerID, gps.Protocol)
	if err != nil {
		return err
	}
	defer stream.Close()

	return gps.writeMessage(stream, messageBytes)
}

// Unsubscribe unsubscribes from a topic
func (gps *GossipPubSub) Unsubscribe(topic string) error {
	gps.mutex.Lock()
	defer gps.mutex.Unlock()

	delete(gps.topics, topic)
	delete(gps.handlers, topic)

	log.Printf("Unsubscribed from topic: %s", topic)
	return nil
}

// GetTopics returns a list of subscribed topics
func (gps *GossipPubSub) GetTopics() []string {
	gps.mutex.RLock()
	defer gps.mutex.RUnlock()

	topics := make([]string, 0, len(gps.topics))
	for topic := range gps.topics {
		topics = append(topics, topic)
	}
	return topics
}

// GetPeerCount returns the number of connected peers
func (gps *GossipPubSub) GetPeerCount() int {
	gps.mutex.RLock()
	defer gps.mutex.RUnlock()
	return len(gps.peers)
}

// GetPeers returns a list of connected peers
func (gps *GossipPubSub) GetPeers() []peer.ID {
	gps.mutex.RLock()
	defer gps.mutex.RUnlock()

	peers := make([]peer.ID, 0, len(gps.peers))
	for _, peerID := range gps.peers {
		peers = append(peers, peerID)
	}
	return peers
}

// Close closes the gossip pub/sub instance
func (gps *GossipPubSub) Close() error {
	gps.mutex.Lock()
	defer gps.mutex.Unlock()

	for topic := range gps.topics {
		gps.Unsubscribe(topic)
	}

	return nil
}

// HandlePeerFound is called when a new peer is discovered
func (gps *GossipPubSub) HandlePeerFound(pi peer.AddrInfo) {
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
	gps.mutex.Lock()
	peerAlreadyExists := false
	for _, existingPeer := range gps.peers {

		if existingPeer == pi.ID {
			peerAlreadyExists = true
			break
		}
	}

	if !peerAlreadyExists {
		// Use the largest multiaddr (following the pattern from utils.go)
		if pi.ID != "" {
			gps.peers = append(gps.peers, pi.ID)
			log.Printf("Added peer %s to peers list with address: %s", pi.ID, pi.Addrs)
		}
	} else {
		log.Printf("Peer %s already exists in peers list", pi.ID)
	}
	gps.mutex.Unlock()

	// Connect to the discovered peer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := gps.Host.Connect(ctx, pi); err != nil {
		log.Printf("Failed to connect to discovered peer %s: %v", pi.ID, err)
	} else {
		log.Printf("Connected to peer: %s", pi.ID)
	}
}
