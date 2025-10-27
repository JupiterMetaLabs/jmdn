package PubSubMessages

import (
	"context"
	"fmt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// InitGossipSub initializes libp2p GossipSub for the GossipPubSub instance
func (gps *GossipPubSub) InitGossipSub() error {
	if gps.Host == nil {
		return fmt.Errorf("host must be set before initializing GossipSub")
	}

	// Initialize GossipSub instance
	gossipSub, err := pubsub.NewGossipSub(context.Background(), gps.Host)
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

	// Check if topic already exists (with read lock)
	gps.Mutex.RLock()
	if topic, exists := gps.TopicsMap[topicName]; exists {
		gps.Mutex.RUnlock()
		return topic, nil
	}
	gps.Mutex.RUnlock()

	// Acquire write lock before creating new topic
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	// Double-check after acquiring lock (another goroutine might have created it)
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
