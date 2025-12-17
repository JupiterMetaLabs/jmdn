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
	gps.Mutex.Lock()
	if gps.TopicsMap == nil {
		gps.TopicsMap = make(map[string]*pubsub.Topic)
	}
	if gps.Subscriptions == nil {
		gps.Subscriptions = make(map[string]*pubsub.Subscription)
	}
	if gps.SubscriptionCancels == nil {
		gps.SubscriptionCancels = make(map[string]context.CancelFunc)
	}
	gps.Mutex.Unlock()

	return nil
}

// GetOrJoinTopic gets an existing topic or joins a new one (thread-safe)
func (gps *GossipPubSub) GetOrJoinTopic(topicName string) (*pubsub.Topic, error) {
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	if gps.GossipSubPS == nil {
		return nil, fmt.Errorf("GossipSub not initialized")
	}

	// Check if topic already exists
	gps.Mutex.RLock()
	if topic, exists := gps.TopicsMap[topicName]; exists {
		gps.Mutex.RUnlock()
		return topic, nil
	}
	gps.Mutex.RUnlock()

	// Join the topic
	topic, err := gps.GossipSubPS.Join(topicName)
	if err != nil {
		return nil, fmt.Errorf("failed to join topic %s: %w", topicName, err)
	}

	gps.Mutex.Lock()
	if gps.TopicsMap == nil {
		gps.TopicsMap = make(map[string]*pubsub.Topic)
	}
	// Another goroutine may have joined concurrently.
	if existing, exists := gps.TopicsMap[topicName]; exists {
		gps.Mutex.Unlock()
		_ = topic.Close()
		return existing, nil
	}
	gps.TopicsMap[topicName] = topic
	gps.Mutex.Unlock()
	return topic, nil
}

// CloseTopic closes a topic
func (gps *GossipPubSub) CloseTopic(topicName string) error {
	gps.Mutex.Lock()
	topic, exists := gps.TopicsMap[topicName]
	if exists {
		delete(gps.TopicsMap, topicName)
	}
	gps.Mutex.Unlock()
	if exists {
		_ = topic.Close()
	}
	return nil
}
