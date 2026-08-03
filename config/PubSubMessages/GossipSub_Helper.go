package PubSubMessages

import (
	"context"
	"fmt"
	"sync"

	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	log "gossipnode/logging"
)

// A libp2p host must have exactly ONE GossipSub router: creating a second one
// silently overwrites the first router's stream handlers, so subscriptions made
// on the first router stop receiving network messages. We therefore cache one
// router (and one joined-topic map) per host and share them across every
// GossipPubSub wrapper built for that host.
var (
	sharedRoutersMu sync.Mutex
	sharedRouters   = make(map[peer.ID]*pubsub.PubSub)
	sharedTopicMaps = make(map[peer.ID]map[string]*pubsub.Topic)
)

// InitGossipSub initializes libp2p GossipSub for the GossipPubSub instance.
// The underlying router and joined-topic map are per-host singletons.
func (gps *GossipPubSub) InitGossipSub() error {
	if gps.Host == nil {
		return fmt.Errorf("host must be set before initializing GossipSub")
	}

	sharedRoutersMu.Lock()
	defer sharedRoutersMu.Unlock()

	hostID := gps.Host.ID()
	gossipSub, exists := sharedRouters[hostID]
	if !exists {
		// Initialize GossipSub instance with FloodPublish for reliable small-network propagation
		// and PeerExchange to help nodes find each other
		var err error
		gossipSub, err = pubsub.NewGossipSub(context.Background(), gps.Host,
			pubsub.WithFloodPublish(true),
			pubsub.WithPeerExchange(true),
			// match the direct-stream cap or blocks >1 MiB (libp2p gossip
			// default) silently skip gossip → fan-out degrades to direct+catch-up.
			// Fleet-wide identical value required (peers reject mismatched sizes).
			pubsub.WithMaxMessageSize(config.MaxBlockMessageBytes),
		)
		if err != nil {
			return fmt.Errorf("failed to create GossipSub: %w", err)
		}
		sharedRouters[hostID] = gossipSub
		sharedTopicMaps[hostID] = make(map[string]*pubsub.Topic)
	}

	gps.GossipSubPS = gossipSub
	gps.Mutex.Lock()
	// Share the joined-topic map: a topic can only be joined once per router,
	// so every wrapper on this host must see the same *pubsub.Topic handles.
	gps.TopicsMap = sharedTopicMaps[hostID]
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

	// TopicsMap is shared across all wrappers on this host, so guard the whole
	// check-join-store sequence with the package-level mutex: a topic can only
	// be joined once per router.
	sharedRoutersMu.Lock()
	defer sharedRoutersMu.Unlock()

	ps := gps.GossipSubPS
	if ps == nil {
		return nil, fmt.Errorf("GossipSub not initialized")
	}

	if gps.TopicsMap == nil {
		gps.TopicsMap = make(map[string]*pubsub.Topic)
	}
	if topic, exists := gps.TopicsMap[topicName]; exists && topic != nil {
		return topic, nil
	}

	joinedTopic, err := ps.Join(topicName)
	if err != nil {
		return nil, fmt.Errorf("failed to join topic %s: %w", topicName, err)
	}
	gps.TopicsMap[topicName] = joinedTopic
	return joinedTopic, nil
}

// CloseTopic closes a topic
func (gps *GossipPubSub) CloseTopic(topicName string) error {
	sharedRoutersMu.Lock()
	topic, exists := gps.TopicsMap[topicName]
	if exists {
		delete(gps.TopicsMap, topicName)
	}
	sharedRoutersMu.Unlock()
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
				ctx := context.Background()
				logInstance, logErr := log.NewAsyncLogger().Get().NamedLogger(log.Config, "")
				if logErr == nil && logInstance != nil {
					logInstance.GetNamedLogger().Warn(ctx, "Failed to close topic", ion.Err(err), ion.String("topic", topicName))
				}
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
