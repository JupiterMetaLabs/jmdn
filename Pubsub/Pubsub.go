package Pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	Channel "gossipnode/Pubsub/DataProcessing/Channel"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"
	"gossipnode/config"
	"log"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type StructGossipPubSub struct {
	GossipPubSub *Struct.GossipPubSub
}

func (sgps *StructGossipPubSub) GetGossipPubSub() *Struct.GossipPubSub {
	return sgps.GossipPubSub
}

// NewGossipPubSub creates a new gossip pub/sub instance
func NewGossipPubSub(host host.Host, Protocol protocol.ID) (*StructGossipPubSub, error) {
	GossipPubSubInput := Struct.NewGossipPubSubBuilder(nil).SetHost(host).SetProtocol(Protocol)
	gps := &StructGossipPubSub{
		GossipPubSub: GossipPubSubInput.Build(),
	}
	if gps.GossipPubSub == nil {
		return nil, fmt.Errorf("failed to create gossip pub/sub")
	}
	// Set up stream handler for gossip messages
	host.SetStreamHandler(Protocol, gps.handleGossipStream)

	log.Printf("Gossip pub/sub initialized for host: %s", host.ID())
	return gps, nil
}

// CreateChannel creates a new channel with access control and self-subscription
func (sgps *StructGossipPubSub) CreateChannel(channelName string, isPublic bool, allowedPeers []peer.ID) error {
	gps := sgps.GetGossipPubSub()

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
	gps.ChannelAccess[channelName] = &Struct.ChannelAccess{
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
func (sgps *StructGossipPubSub) RemovePeerFromChannel(channelName string, peerID peer.ID) error {
	gps := sgps.GetGossipPubSub()
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

// CanSubscribe checks if a peer can subscribe to a channel
func (sgps *StructGossipPubSub) CanSubscribe(channelName string, peerID peer.ID) bool {
	gps := sgps.GetGossipPubSub()
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()

	access, exists := gps.ChannelAccess[channelName]
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
func (sgps *StructGossipPubSub) Subscribe(topic string, handler func(*Struct.GossipMessage)) error {
	gps := sgps.GetGossipPubSub()
	// Check if we can subscribe to this channel
	hostMultiAddr := gps.Host.ID()
	if !sgps.CanSubscribe(topic, hostMultiAddr) {
		return fmt.Errorf("access denied: not authorized to subscribe to channel %s", topic)
	}

	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	gps.Topics[topic] = true
	gps.Handlers[topic] = handler

	log.Printf("Subscribed to topic: %s", topic)
	return nil
}

// Publish publishes a message to a topic
func (sgps *StructGossipPubSub) Publish(topic string, data *Struct.Message, metadata map[string]string) error {
	gps := sgps.GetGossipPubSub()
	// Create message
	message := &Struct.GossipMessage{
		ID:        fmt.Sprintf("%s-%d", gps.Host.ID().String(), gps.MessageID),
		Topic:     topic,
		Data:      data,
		Sender:    gps.Host.ID(),
		Timestamp: time.Now().Unix(),
		TTL:       10, // Default TTL
		Metadata:  metadata,
	}
	gps.MessageID++

	// Serialize message
	messageBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	// Add to message cache to prevent loops
	gps.Mutex.Lock()
	gps.MessageCache[message.ID] = true
	gps.Mutex.Unlock()

	// Gossip to all connected peers
	sgps.gossipMessage(messageBytes)

	log.Printf("Published message to topic %s: %s", topic, message.ID)
	return nil
}

// handleGossipStream handles incoming gossip messages
func (sgps *StructGossipPubSub) handleGossipStream(s network.Stream) {
	gps := sgps.GetGossipPubSub()
	defer s.Close()

	// Read message using delimiter
	messageBytes, err := sgps.readMessage(s)
	if err != nil {
		log.Printf("Error reading gossip message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	// Deserialize message
	var gossipMsg Struct.GossipMessage
	if err := json.Unmarshal(messageBytes, &gossipMsg); err != nil {
		log.Printf("Failed to unmarshal gossip message: %v", err)
		return
	}

	// Check if we've already seen this message
	gps.Mutex.Lock()
	if gps.MessageCache[gossipMsg.ID] {
		gps.Mutex.Unlock()
		return // Already processed
	}
	gps.MessageCache[gossipMsg.ID] = true
	gps.Mutex.Unlock()

	// Check TTL
	if gossipMsg.TTL <= 0 {
		log.Printf("Gossip message TTL expired: %s", gossipMsg.ID)
		return
	}

	// Decrement TTL for forwarding
	gossipMsg.TTL--

	log.Printf("Received gossip message from %s on topic %s: %s", gossipMsg.Sender, gossipMsg.Topic, gossipMsg.ID)
	// <-- Write the logic to check the processing of the message based on the message type --> TODO
	Channel.AppendMessage(&Struct.MessageProcessing{
		GossipMessage: string(messageBytes),
		Protocol:      gps.Protocol,
	})
	// Call handler if we're subscribed to this topic
	gps.Mutex.RLock()
	handler, subscribed := gps.Handlers[gossipMsg.Topic]
	gps.Mutex.RUnlock()

	if subscribed && handler != nil {
		handler(&gossipMsg)
	}

	// Forward message to other peers (gossip)
	if gossipMsg.TTL > 0 {
		sgps.gossipMessage(messageBytes)
	}
}

// readMessage reads a message from the stream using delimiter
func (sgps *StructGossipPubSub) readMessage(s network.Stream) ([]byte, error) {
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
func (sgps *StructGossipPubSub) writeMessage(s network.Stream, message []byte) error {
	_, err := s.Write(append(message, config.Delimiter))
	return err
}

// gossipMessage forwards a message to connected peers
func (sgps *StructGossipPubSub) gossipMessage(messageBytes []byte) {
	gps := sgps.GetGossipPubSub()
	gps.Mutex.RLock()

	for _, peerID := range gps.Peers {
		// Don't send to ourselves
		if peerID == gps.Host.ID() {
			continue
		}

		go func(p peer.ID) {
			if err := sgps.sendToPeer(p, messageBytes); err != nil {
				log.Printf("Failed to gossip message to %s: %v", p, err)
			}
		}(peerID)
	}
}

// sendToPeer sends a message to a specific peer
func (sgps *StructGossipPubSub) sendToPeer(peerID peer.ID, messageBytes []byte) error {
	gps := sgps.GetGossipPubSub()
	stream, err := gps.Host.NewStream(context.Background(), peerID, gps.Protocol)
	if err != nil {
		return err
	}
	defer stream.Close()

	return sgps.writeMessage(stream, messageBytes)
}

// Unsubscribe unsubscribes from a topic
func (sgps *StructGossipPubSub) Unsubscribe(topic string) error {
	gps := sgps.GetGossipPubSub()
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	delete(gps.Topics, topic)
	delete(gps.Handlers, topic)

	log.Printf("Unsubscribed from topic: %s", topic)
	return nil
}

// GetTopics returns a list of subscribed topics
func (sgps *StructGossipPubSub) GetTopics() []string {
	gps := sgps.GetGossipPubSub()
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()

	topics := make([]string, 0, len(gps.Topics))
	for topic := range gps.Topics {
		topics = append(topics, topic)
	}
	return topics
}

// GetPeerCount returns the number of connected peers
func (sgps *StructGossipPubSub) GetPeerCount() int {
	gps := sgps.GetGossipPubSub()
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()
	return len(gps.Peers)
}

// GetPeers returns a list of connected peers
func (sgps *StructGossipPubSub) GetPeers() []peer.ID {
	gps := sgps.GetGossipPubSub()
	gps.Mutex.RLock()
	defer gps.Mutex.RUnlock()

	peers := make([]peer.ID, 0, len(gps.Peers))
	for _, peerID := range gps.Peers {
		peers = append(peers, peerID)
	}
	return peers
}

// Close closes the gossip pub/sub instance
func (sgps *StructGossipPubSub) Close() error {
	gps := sgps.GetGossipPubSub()
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	for topic := range gps.Topics {
		sgps.Unsubscribe(topic)
	}

	return nil
}

// HandlePeerFound is called when a new peer is discovered
func (sgps *StructGossipPubSub) HandlePeerFound(pi peer.AddrInfo) {
	gps := sgps.GetGossipPubSub()
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
