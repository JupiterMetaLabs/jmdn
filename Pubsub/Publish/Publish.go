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
	// Create message
	messageGossip := &PubSubMessages.GossipMessage{
		ID:        fmt.Sprintf("%s-%d", gps.Host.ID().String(), gps.MessageID),
		Topic:     topic,
		Data:      message,
		Sender:    message.GetSender(),
		Timestamp: time.Now().Unix(),
		TTL:       10, // Default TTL
		Metadata:  metadata,
	}
	gps.MessageID++

	// Need to change the message type to Processmessage type from config.Type_ToBeProcessed if the current type is Publish @config.Type_Publish
	if messageGossip.Data.GetACK().GetStage() == config.Type_Publish {
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
	return nil
}

// gossipMessage forwards a message to connected peers
func GossipMessage(gps *PubSubMessages.GossipPubSub, messageBytes []byte) {
	gps.Mutex.RLock()

	for _, peerID := range gps.Peers {
		// Don't send to ourselves
		if peerID == gps.Host.ID() {
			continue
		}

		go func(p peer.ID) {
			if err := sendToPeer(gps, p, messageBytes); err != nil {
				log.LogConsensusError(fmt.Sprintf("Failed to gossip message to %s: %v", p, err), err, zap.String("peer", p.String()), zap.String("topic", log.Consensus_TOPIC), zap.String("message", string(messageBytes)), zap.String("function", "Subscription.gossipMessage"))
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

	return writeMessage(stream, messageBytes)
}

// writeMessage writes a message to the stream using delimiter
func writeMessage(stream network.Stream, message []byte) error {
	_, err := stream.Write(append(message, config.Delimiter))
	return err
}
