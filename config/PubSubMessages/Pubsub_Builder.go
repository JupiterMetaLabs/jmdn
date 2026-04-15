package PubSubMessages

import (
	"context"
	"time"

	log "gossipnode/logging"
	"github.com/JupiterMetaLabs/ion"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// < -- Builder Pattern for GossipPubSub -- >
func NewGossipPubSubBuilder(GossipPubSubInput *GossipPubSub) *GossipPubSub {
	if GossipPubSubInput != nil {
		return &GossipPubSub{
			Host:          GossipPubSubInput.Host,
			Topics:        GossipPubSubInput.Topics,
			Handlers:      GossipPubSubInput.Handlers,
			MessageCache:  GossipPubSubInput.MessageCache,
			ChannelAccess: GossipPubSubInput.ChannelAccess,
			Peers:         GossipPubSubInput.Peers,
			Protocol:      GossipPubSubInput.Protocol,
			TopicsMap:     GossipPubSubInput.TopicsMap,

			Subscriptions:       GossipPubSubInput.Subscriptions,
			SubscriptionCancels: GossipPubSubInput.SubscriptionCancels,
		}
	}
	return &GossipPubSub{
		Host:                nil,
		Topics:              make(map[string]bool),
		Handlers:            make(map[string]func(*GossipMessage)),
		MessageCache:        make(map[string]time.Time),
		ChannelAccess:       make(map[string]*ChannelAccess),
		Peers:               make([]peer.ID, 0),
		TopicSubscribers:    make(map[string]map[peer.ID]bool),
		TopicsMap:           make(map[string]*pubsub.Topic),
		Subscriptions:       make(map[string]*pubsub.Subscription),
		SubscriptionCancels: make(map[string]context.CancelFunc),
		Protocol:            "",
	}
}

func (gps *GossipPubSub) SetHost(host host.Host) *GossipPubSub {
	gps.Host = host
	return gps
}

func (gps *GossipPubSub) GetHost() host.Host {
	return gps.Host
}

func (gps *GossipPubSub) SetTopics(topics map[string]bool) *GossipPubSub {
	gps.Topics = topics
	return gps
}

func (gps *GossipPubSub) GetTopics() map[string]bool {
	return gps.Topics
}

func (gps *GossipPubSub) SetHandlers(handlers map[string]func(*GossipMessage)) *GossipPubSub {
	gps.Handlers = handlers
	return gps
}

func (gps *GossipPubSub) GetHandlers() map[string]func(*GossipMessage) {
	return gps.Handlers
}

func (gps *GossipPubSub) SetMessageCache(messageCache map[string]time.Time) *GossipPubSub {
	gps.MessageCache = messageCache
	return gps
}

func (gps *GossipPubSub) GetMessageCache() map[string]time.Time {
	return gps.MessageCache
}

func (gps *GossipPubSub) SetChannelAccess(channelAccess map[string]*ChannelAccess) *GossipPubSub {
	gps.ChannelAccess = channelAccess
	return gps
}

func (gps *GossipPubSub) GetChannelAccess() map[string]*ChannelAccess {
	return gps.ChannelAccess
}

func (gps *GossipPubSub) SetPeers(peers []peer.ID) *GossipPubSub {
	gps.Peers = peers
	return gps
}

func (gps *GossipPubSub) GetPeers() []peer.ID {
	return gps.Peers
}

func (gps *GossipPubSub) SetProtocol(protocol protocol.ID) *GossipPubSub {
	gps.Protocol = protocol
	return gps
}

func (gps *GossipPubSub) GetProtocol() protocol.ID {
	return gps.Protocol
}

func (gps *GossipPubSub) Build() *GossipPubSub {
	// Initialize GossipSub if host is set
	if gps.Host != nil {
		if err := gps.InitGossipSub(); err != nil {
			// Log error but don't fail build
			ctx := context.Background()
			logInstance, logErr := log.NewAsyncLogger().Get().NamedLogger(log.Config, "")
			if logErr == nil && logInstance != nil {
				logInstance.GetNamedLogger().Warn(ctx, "Failed to initialize GossipSub", ion.Err(err))
			}
		}
	}
	return gps
}
