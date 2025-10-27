package Publish

import (
	"context"
	"encoding/json"
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Publish publishes a message to a topic
func Publish(gps *PubSubMessages.GossipPubSub, topic string, message *PubSubMessages.Message, metadata map[string]string) error {
	// Validate input parameters
	if gps == nil {
		return fmt.Errorf("GossipPubSub cannot be nil")
	}
	if message == nil {
		return fmt.Errorf("message cannot be nil")
	}
	if topic == "" {
		return fmt.Errorf("topic cannot be empty")
	}

	// Create message
	messageGossip := &PubSubMessages.GossipMessage{
		ID:        fmt.Sprintf("%s-%d", gps.Host.ID().String(), gps.MessageID),
		Topic:     topic,
		Data:      message,
		Sender:    message.GetSender(),
		Timestamp: time.Now().Unix(),
		TTL:       30, // Default TTL
		Metadata:  metadata,
	}
	gps.MessageID++

	// Need to change the message type to Processmessage type from config.Type_ToBeProcessed if the current type is Publish @config.Type_Publish
	if messageGossip.Data.GetACK() != nil && messageGossip.Data.GetACK().GetStage() == config.Type_Publish {
		messageGossip.Data.GetACK().SetStage(config.Type_ToBeProcessed)
	}

	// Serialize message
	messageBytes, err := json.Marshal(messageGossip)
	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to marshal message: %v", err), err, zap.String("topic", topic), zap.String("message", string(messageBytes)), zap.String("function", "Subscription.Publish"))
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	// Add to message cache to prevent loops
	gps.Mutex.Lock()
	gps.MessageCache[messageGossip.ID] = true
	gps.Mutex.Unlock()

	// Gossip to all connected peers
	GossipMessage(gps, messageBytes)

	log.LogConsensusInfo(fmt.Sprintf("Published message to topic %s: %s", topic, messageGossip.ID), zap.String("topic", topic), zap.String("message", messageGossip.ID), zap.String("function", "Subscription.Publish"))

	// Debugging
	fmt.Printf("=== Publish.Publish CALLED ===\n")
	fmt.Printf("Published message to topic %s: %s\n", topic, messageGossip.ID)
	fmt.Printf("Message: %+v\n", messageGossip)
	fmt.Printf("From Peer: %s\n", gps.Host.ID())
	fmt.Printf("To Peers: %v\n", gps.Peers)
	fmt.Printf("Message Bytes: %s\n", string(messageBytes))

	return nil
}

// gossipMessage forwards a message to connected peers
func GossipMessage(gps *PubSubMessages.GossipPubSub, messageBytes []byte) {
	fmt.Printf("=== Publish.GossipMessage CALLED ===\n")
	fmt.Printf("Message Bytes: %s\n", string(messageBytes))
	fmt.Printf("From Peer: %s\n", gps.Host.ID())
	fmt.Printf("Message ID: %s\n", gps.MessageID)
	fmt.Printf("Protocol: %s\n", gps.Protocol)
	fmt.Printf("Message Cache: %v\n", gps.MessageCache)
	fmt.Printf("Mutex: %v\n", gps.Mutex)
	fmt.Printf("=== Publish.GossipMessage ENDED ===\n")
	gps.Mutex.RLock()

	for _, peerID := range gps.Peers {
		// Don't send to ourselves
		if peerID == gps.Host.ID() {
			continue
		}

		go func(p peer.ID) {
			fmt.Printf("=== Publish.GossipMessage Sending to Peer: %s ===\n", p)

			if err := sendToPeer(gps, p, messageBytes); err != nil {
				fmt.Printf("=== Publish.GossipMessage Failed to send to Peer: %s ===\n", p)
				log.LogConsensusError(fmt.Sprintf("Failed to gossip message to %s: %v", p, err), err, zap.String("peer", p.String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", string(messageBytes)), zap.String("function", "Subscription.gossipMessage"))
			}
			fmt.Printf("=== Publish.GossipMessage Sent to Peer: %s ===\n", p)

		}(peerID)
	}
	fmt.Printf("=== Publish.GossipMessage ENDED ===\n")
}

// sendToPeer sends a message to a specific peer
func sendToPeer(gps *PubSubMessages.GossipPubSub, peerID peer.ID, messageBytes []byte) error {
	stream, err := gps.Host.NewStream(context.Background(), peerID, gps.Protocol)
	if err != nil {
		return err
	}
	defer stream.Close()

	fmt.Printf("=== Publish.sendToPeer Creating stream to Peer: %s ===\n", peerID)
	err = writeMessage(stream, messageBytes)
	if err != nil {
		fmt.Printf("=== Publish.sendToPeer Failed to write message to Peer: %s ===\n", peerID)
		return err
	}
	fmt.Printf("=== Publish.sendToPeer Wrote message to Peer: %s ===\n", peerID)
	return nil
}

// writeMessage writes a message to the stream using delimiter
func writeMessage(stream network.Stream, message []byte) error {
	_, err := stream.Write(append(message, config.Delimiter))
	return err
}
