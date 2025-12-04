package PubSubMessages

import (
	AppContext "gossipnode/config/Context"
	"fmt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

const (
	PubSubMessagesAppContext = "pubsub.messages"
)

// InitGossipSub initializes libp2p GossipSub for the GossipPubSub instance
func (gps *GossipPubSub) InitGossipSub() error {
	if gps.Host == nil {
		return fmt.Errorf("host must be set before initializing GossipSub")
	}

	// Initialize GossipSub instance

	longCTX, _ := AppContext.GetAppContext(PubSubMessagesAppContext).NewChildContext()
	gossipSub, err := pubsub.NewGossipSub(longCTX, gps.Host)
	if err != nil {
		return fmt.Errorf("failed to create GossipSub: %w", err)
	}

	gps.GossipSubPS = gossipSub
	gps.TopicsMap = make(map[string]*pubsub.Topic)

	return nil
}

// GetOrJoinTopic gets an existing topic or joins a new one (thread-safe)
func (gps *GossipPubSub) GetOrJoinTopic(topicName string) (*pubsub.Topic, error) {
	if gps.GossipSubPS == nil {
		return nil, fmt.Errorf("GossipSub not initialized")
	}

	// Check if topic already exists
	if topic, exists := gps.TopicsMap[topicName]; exists {
		return topic, nil
	}

	// Join the topic
	topic, err := gps.GossipSubPS.Join(topicName)
	if err != nil {
		return nil, fmt.Errorf("failed to join topic %s: %w", topicName, err)
	}

	gps.TopicsMap[topicName] = topic
	return topic, nil
}

// CloseTopic closes a topic
func (gps *GossipPubSub) CloseTopic(topicName string) error {
	if topic, exists := gps.TopicsMap[topicName]; exists {
		topic.Close()
		delete(gps.TopicsMap, topicName)
	}
	return nil
}
