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

	// Initialize GossipSub instance with FloodPublish for reliable small-network propagation
	// and PeerExchange to help nodes find each other
	gossipSub, err := pubsub.NewGossipSub(context.Background(), gps.Host,
		pubsub.WithFloodPublish(true),
		pubsub.WithPeerExchange(true),
	)
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
	if topicName == "" {
		return nil, fmt.Errorf("topic name must not be empty")
	}

	// Fast path: read lock to check for existing topic + capture GossipSub pointer.
	gps.Mutex.RLock()
	ps := gps.GossipSubPS
	if gps.TopicsMap != nil {
		if topic, exists := gps.TopicsMap[topicName]; exists && topic != nil {
			gps.Mutex.RUnlock()
			return topic, nil
		}
	}
	gps.Mutex.RUnlock()

	if ps == nil {
		return nil, fmt.Errorf("GossipSub not initialized")
	}

	// Join can do non-trivial work; do it outside locks to avoid deadlocks.
	joinedTopic, err := ps.Join(topicName)
	if err != nil {
		return nil, fmt.Errorf("failed to join topic %s: %w", topicName, err)
	}

	// Store under write lock (double-check to handle concurrent Join).
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	if gps.TopicsMap == nil {
		gps.TopicsMap = make(map[string]*pubsub.Topic)
	}
	if existing, exists := gps.TopicsMap[topicName]; exists && existing != nil {
		_ = joinedTopic.Close()
		return existing, nil
	}
	gps.TopicsMap[topicName] = joinedTopic
	return joinedTopic, nil
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

// Shutdown gracefully shuts down the GossipPubSub instance
// This closes all topics and cleans up resources to prevent goroutine leaks
func (gps *GossipPubSub) Shutdown(ctx context.Context) error {
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	// Close all active topics
	for topicName, topic := range gps.TopicsMap {
		if topic != nil {
			if err := topic.Close(); err != nil {
				// Log but continue closing other topics
				fmt.Printf("Warning: failed to close topic %s: %v\n", topicName, err)
			}
		}
		delete(gps.TopicsMap, topicName)
	}

	// Cancel all subscription contexts
	for topicName, cancelFunc := range gps.SubscriptionCancels {
		if cancelFunc != nil {
			cancelFunc()
		}
		delete(gps.SubscriptionCancels, topicName)
	}

	// Cancel all subscriptions
	for topicName, sub := range gps.Subscriptions {
		if sub != nil {
			sub.Cancel()
		}
		delete(gps.Subscriptions, topicName)
	}

	// Note: We don't close the GossipSubPS itself as it may be shared
	// The caller should manage the lifecycle of the PubSub instance

	return nil
}
