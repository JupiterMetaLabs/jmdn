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

// Publish publishes a message to a topic (now uses enhanced implementation)
func Publish(gps *PubSubMessages.GossipPubSub, topic string, message *PubSubMessages.Message, metadata map[string]string) error {
	// Use enhanced publishing if available, fall back to original implementation
	if gps.GossipSubPS != nil {
		return PublishEnhanced(gps, topic, message, metadata)
	}

	// Fall back to original implementation for custom gossip
	return publishOriginal(gps, topic, message, metadata)
}

// publishOriginal is the original publish implementation (renamed for clarity)
func publishOriginal(gps *PubSubMessages.GossipPubSub, topic string, message *PubSubMessages.Message, metadata map[string]string) error {
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

	// Validate that message has ACK
	if message.GetACK() == nil {
		return fmt.Errorf("message cannot be published without ACK - message type: %T", message)
	}

	// Create message
	messageGossip := &PubSubMessages.GossipMessage{
		ID:        fmt.Sprintf("%s-%d", gps.Host.ID().String(), gps.MessageID),
		Topic:     topic,
		Data:      message,
		Sender:    message.GetSender(),
		Timestamp: time.Now().UTC().Unix(),
		TTL:       30, // Default TTL
		Metadata:  metadata,
	}
	gps.MessageID++

	// Need to change the message type to Processmessage type from config.Type_ToBeProcessed if the current type is Publish @config.Type_Publish
	if messageGossip.Data.GetACK() != nil && messageGossip.Data.GetACK().GetStage() == config.Type_Publish {
		messageGossip.Data.GetACK().SetStage(config.Type_ToBeProcessed)
	}

	// Serialize message - for GossipSub, publish only the Data (Message) part
	var messageBytes []byte
	var err error

	if gps.GossipSubPS != nil {
		// For GossipSub, marshal only the Data part
		messageBytes, err = json.Marshal(messageGossip.Data)
	} else {
		// For custom gossip, marshal the entire GossipMessage
		messageBytes, err = json.Marshal(messageGossip)
	}

	if err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to marshal message: %v", err), err, zap.String("topic", topic), zap.String("message", string(messageBytes)), zap.String("function", "Subscription.Publish"))
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	// Add to message cache to prevent loops
	gps.Mutex.Lock()
	gps.MessageCache[messageGossip.ID] = true
	gps.Mutex.Unlock()

	// Publish using GossipSub if available, otherwise use custom gossip
	if gps.GossipSubPS != nil {
		if err := publishViaGossipSub(gps, topic, messageBytes); err != nil {
			log.LogConsensusError(fmt.Sprintf("Failed to publish via GossipSub, falling back to custom gossip: %v", err), err, zap.String("topic", topic), zap.String("function", "Publish.Publish"))
			// Fall back to custom gossip on error
			GossipMessage(gps, messageBytes)
		}
	} else {
		// Use custom gossip when GossipSub is not available
		GossipMessage(gps, messageBytes)
	}

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
	fmt.Printf("Message ID: %d\n", gps.MessageID)
	fmt.Printf("Protocol: %s\n", gps.Protocol)
	fmt.Printf("Message Cache: %v\n", gps.MessageCache)

	// Parse the message to get the topic
	var gossipMsg PubSubMessages.GossipMessage
	if err := json.Unmarshal(messageBytes, &gossipMsg); err != nil {
		log.LogConsensusError(fmt.Sprintf("Failed to parse message for topic routing: %v", err), err, zap.String("function", "Publish.GossipMessage"))
		return
	}

	topic := gossipMsg.Topic
	fmt.Printf("Message topic: %s\n", topic)

	gps.Mutex.RLock()
	// Get topic subscribers for this topic
	topicSubscribers := gps.TopicSubscribers[topic]
	fmt.Printf("Topic subscribers for %s: %v\n", topic, topicSubscribers)
	gps.Mutex.RUnlock()

	// Use topic subscribers if available, otherwise fall back to all connected peers
	connectedPeers := gps.Host.Network().Peers()
	fmt.Printf("Connected Peers: %v\n", connectedPeers)

	// Determine which peers to send to
	peersToSend := make([]peer.ID, 0)
	if len(topicSubscribers) > 0 {
		// Send to topic subscribers
		for peerID := range topicSubscribers {
			if peerID != gps.Host.ID() {
				// Check if peer is actually connected
				connectedPeerMap := make(map[peer.ID]bool)
				for _, cp := range connectedPeers {
					connectedPeerMap[cp] = true
				}
				if connectedPeerMap[peerID] {
					peersToSend = append(peersToSend, peerID)
				}
			}
		}
		fmt.Printf("Using topic-based routing: %d subscribers\n", len(peersToSend))
	} else {
		// Fall back to all connected peers
		for _, peerID := range connectedPeers {
			if peerID != gps.Host.ID() {
				peersToSend = append(peersToSend, peerID)
			}
		}
		fmt.Printf("Using broadcast fallback: %d peers\n", len(peersToSend))
	}

	fmt.Printf("=== Publish.GossipMessage ENDED ===\n")

	// Send to filtered peers
	for _, peerID := range peersToSend {
		go func(p peer.ID) {
			fmt.Printf("=== Publish.GossipMessage Sending to Peer: %s ===\n", p)

			if err := sendToPeer(gps, p, messageBytes); err != nil {
				fmt.Printf("=== Publish.GossipMessage Failed to send to Peer: %s ===\n", p)
				log.LogConsensusError(fmt.Sprintf("Failed to gossip message to %s: %v", p, err), err, zap.String("peer", p.String()), zap.String("topic", topic), zap.String("message", string(messageBytes)), zap.String("function", "Subscription.gossipMessage"))
			} else {
				fmt.Printf("=== Publish.GossipMessage Sent to Peer: %s ===\n", p)
			}
		}(peerID)
	}
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

// publishViaGossipSub publishes a message using libp2p GossipSub
func publishViaGossipSub(gps *PubSubMessages.GossipPubSub, topicName string, messageBytes []byte) error {
	// Get or join the topic
	topic, err := gps.GetOrJoinTopic(topicName)
	if err != nil {
		return fmt.Errorf("failed to get or join topic %s: %w", topicName, err)
	}

	// Publish the message
	err = topic.Publish(context.Background(), messageBytes)
	if err != nil {
		return fmt.Errorf("failed to publish message to topic %s: %w", topicName, err)
	}

	fmt.Printf("=== Published via GossipSub to topic: %s ===\n", topicName)
	return nil
}
