package Pubsub

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"gossipnode/internal/saferun"

	Channel "gossipnode/Pubsub/DataProcessing/Channel"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Singleton cleanup goroutine management
// LocalGRO: shared GRO manager for all PubSub instances
// triggerCleanupChan: signals immediate cleanup when new instances are created
// previousGPS: tracks last instance to call Close() before creating new ones
var (
	LocalGRO           interfaces.LocalGoroutineManagerInterface
	localGROMux        sync.Mutex
	localGROInit       bool
	triggerCleanupChan chan struct{}
	previousGPS        *PubSubMessages.GossipPubSub
)

type StructGossipPubSub struct {
	GossipPubSub *PubSubMessages.GossipPubSub
}

func (sgps *StructGossipPubSub) GetGossipPubSub() *PubSubMessages.GossipPubSub {
	return sgps.GossipPubSub
}

// NewGossipPubSub creates a new gossip pub/sub instance
func NewGossipPubSub(host host.Host, Protocol protocol.ID) (*StructGossipPubSub, error) {
	// Validate input parameters
	if host == nil {
		return nil, fmt.Errorf("host cannot be nil")
	}
	if Protocol == "" {
		return nil, fmt.Errorf("protocol cannot be empty")
	}

	GossipPubSubInput := PubSubMessages.NewGossipPubSubBuilder(nil).SetHost(host).SetProtocol(Protocol)
	gps := &StructGossipPubSub{
		GossipPubSub: GossipPubSubInput.Build(),
	}
	if gps.GossipPubSub == nil {
		return nil, fmt.Errorf("failed to create gossip pub/sub")
	}
	// Set up stream handler for gossip messages
	host.SetStreamHandler(Protocol, saferun.Stream(string(Protocol), func(s network.Stream) {
		handleGossipStream(gps.GossipPubSub, s)
	}))

	// Close previous instance to prevent goroutine leaks
	localGROMux.Lock()
	if previousGPS != nil {
		log.Printf("Closing previous GossipPubSub instance")
		Close(previousGPS)
	}
	previousGPS = gps.GossipPubSub
	localGROMux.Unlock()

	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	gps.GossipPubSub.CleanupCtx = cleanupCtx
	gps.GossipPubSub.CleanupCancel = cleanupCancel

	// Initialize singleton cleanup goroutine (once) with thread-safe check
	localGROMux.Lock()
	if !localGROInit {
		var err error
		LocalGRO, err = GRO.GetApp(GRO.PubsubApp).NewLocalManager(GRO.PubsubPublishLocal)
		if err != nil {
			localGROMux.Unlock()
			log.Printf("⚠️ Failed to create local manager for cache cleanup: %v", err)
			return gps, nil
		}
		triggerCleanupChan = make(chan struct{}, 1)
		localGROInit = true
	}
	localGROMux.Unlock()

	if err := LocalGRO.Go(GRO.PubsubCacheCleanupThread, func(ctx context.Context) error {
		cleanupMessageCache(cleanupCtx, gps.GossipPubSub)
		return nil
	}); err != nil {
		log.Printf("⚠️ Failed to start cache cleanup goroutine: %v", err)
	}

	// Trigger immediate cleanup and reset 4-minute timer
	select {
	case triggerCleanupChan <- struct{}{}:
		log.Printf("Triggered immediate cache cleanup for new instance")
	default:
	}

	log.Printf("Gossip pub/sub initialized for host: %s", host.ID())
	return gps, nil
}

// cleanupMessageCache runs as a singleton goroutine
// Clears ALL MessageCache entries every 4 minutes AND on-demand via triggerCleanupChan
// Trigger source: NewGossipPubSub() sends signal → immediate cleanup → resets 4-min timer
func cleanupMessageCache(ctx context.Context, gps *PubSubMessages.GossipPubSub) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("❌ Cache cleanup goroutine panicked and recovered: %v", r)
		}
	}()

	ticker := time.NewTicker(4 * time.Minute)
	defer ticker.Stop()

	performCleanup := func() {
		if gps == nil {
			log.Printf("⚠️ GossipPubSub is nil, skipping cleanup")
			return
		}

		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("⚠️ Panic during cache cleanup (recovered): %v", r)
				}
			}()

			gps.Mutex.Lock()
			defer gps.Mutex.Unlock()

			// Age-based expiry, NOT a wholesale flush. Wholesale-clearing every
			// 4 min made a still-circulating message "unseen" again and it
			// resurrected on the next cycle (audit NET-04). Keep an entry until
			// it is older than the retention window; a hard ceiling bounds
			// memory against a flood of unique IDs (audit NET-03).
			now := time.Now()
			cleared := 0
			for id, seenAt := range gps.MessageCache {
				if now.Sub(seenAt) > gossipCacheEntryTTL {
					delete(gps.MessageCache, id)
					cleared++
				}
			}
			if len(gps.MessageCache) > gossipCacheMaxEntries {
				// Ceiling breached (flood): drop everything as a last resort.
				log.Printf("Cache cleanup: hard ceiling %d exceeded, clearing all", gossipCacheMaxEntries)
				gps.MessageCache = make(map[string]time.Time)
			} else if cleared > 0 {
				log.Printf("Cache cleanup: expired %d aged entries (%d remain)", cleared, len(gps.MessageCache))
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			performCleanup()
		case <-triggerCleanupChan:
			log.Printf("Immediate cleanup triggered")
			performCleanup()
			ticker.Reset(4 * time.Minute)
		}
	}
}

// CreateChannel creates a new channel with access control and self-subscription
func CreateChannel(gps *PubSubMessages.GossipPubSub, channelName string, isPublic bool, allowedPeers []peer.ID) error {

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
	gps.ChannelAccess[channelName] = &PubSubMessages.ChannelAccess{
		ChannelName:  channelName,
		AllowedPeers: allowedMap,
		IsPublic:     isPublic,
		Creator:      gps.Host.ID(),
		CreatedAt:    time.Now().UTC().Unix(),
	}

	gps.Topics[channelName] = true
	log.Printf("Created channel: %s (public: %v, allowed peers: %d)", channelName, isPublic, len(allowedPeers))
	return nil
}

// Gossip receive-path bounds (audit NET-03/NET-04).
const (
	gossipStreamReadTimeout = 30 * time.Second // slow-drip peers cannot pin a goroutine
	gossipCacheEntryTTL     = 10 * time.Minute // dedup retention; age-expired, not wholesale-flushed
	gossipCacheMaxEntries   = 100_000          // hard ceiling against a unique-ID flood

	// Largest hop budget any local publisher sets (Publish uses 30). A received
	// TTL above it is clamped so a peer cannot extend a message's reach.
	gossipMaxTTL = 30
	// Accepted Timestamp window for relayed messages. The past bound is inside
	// gossipCacheEntryTTL so a message is stale before its dedup entry ages out.
	gossipMaxAge    = gossipCacheEntryTTL - time.Minute
	gossipMaxFuture = time.Minute
)

// gossipContentKey is a dedup key derived from the message content rather
// than the sender-chosen ID, so the same payload re-sent under fresh IDs is
// still recognised as a duplicate.
func gossipContentKey(m *PubSubMessages.GossipMessage) (string, error) {
	data, err := json.Marshal(m.Data)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00", m.Topic, m.Sender, m.Timestamp)
	h.Write(data)
	return "content:" + hex.EncodeToString(h.Sum(nil)), nil
}

// checkGossipEnvelope validates the sender-controlled envelope fields and
// clamps TTL. Returns "" when the message may be processed.
func checkGossipEnvelope(m *PubSubMessages.GossipMessage, now time.Time) string {
	if m.TTL < 0 {
		return "negative_ttl"
	}
	if m.TTL > gossipMaxTTL {
		m.TTL = gossipMaxTTL
	}
	if m.TTL > 0 {
		ts := time.Unix(m.Timestamp, 0)
		if now.Sub(ts) > gossipMaxAge {
			return "stale"
		}
		if ts.Sub(now) > gossipMaxFuture {
			return "future_timestamp"
		}
	}
	return ""
}

// handleGossipStream handles incoming gossip messages
func handleGossipStream(gps *PubSubMessages.GossipPubSub, s network.Stream) {
	defer s.Close()

	// Bound how long an unauthenticated peer can hold this goroutine open
	// (audit NET-03): a peer trickling one byte per minute must not pin a
	// goroutine + growing buffer indefinitely.
	_ = s.SetReadDeadline(time.Now().Add(gossipStreamReadTimeout))

	// Read message using delimiter
	messageBytes, err := readMessage(s)
	if err != nil {
		log.Printf("Error reading gossip message from %s: %v", s.Conn().RemotePeer(), err)
		return
	}

	// Deserialize message
	var gossipMsg PubSubMessages.GossipMessage
	if err := json.Unmarshal(messageBytes, &gossipMsg); err != nil {
		log.Printf("Failed to unmarshal gossip message: %v", err)
		return
	}

	// Attach ACK if missing or if Data is nil
	if gossipMsg.Data == nil {
		logger().Debug(context.Background(), "Received message with nil Data - initializing new Message")
		gossipMsg.Data = PubSubMessages.NewMessageBuilder(nil).SetSender(gossipMsg.Sender)
	}

	if gossipMsg.Data.GetACK() == nil {
		logger().Debug(context.Background(), "Received message with nil ACK - attaching default ACK")

		// Create a default ACK with Type_Publish stage
		ack := PubSubMessages.NewACKBuilder().
			True_ACK_Message(gossipMsg.Sender, config.Type_Publish)

		gossipMsg.Data.SetACK(ack)
	}
	logger().Debug(context.Background(), "Received message with ACK",
		ion.String("ack", fmt.Sprintf("%+v", gossipMsg.Data.GetACK())))
	if reason := checkGossipEnvelope(&gossipMsg, time.Now().UTC()); reason != "" {
		log.Printf("Dropping gossip message %s from %s: %s", gossipMsg.ID, s.Conn().RemotePeer(), reason)
		return
	}
	// Relayed messages are also deduplicated by content, since the ID is
	// sender-chosen. Direct (TTL 0) messages are never relayed and keep
	// ID-only dedup.
	var contentKey string
	if gossipMsg.TTL > 0 {
		k, err := gossipContentKey(&gossipMsg)
		if err != nil {
			log.Printf("Dropping gossip message %s: %v", gossipMsg.ID, err)
			return
		}
		contentKey = k
	}

	// Check if we've already seen this message.
	gps.Mutex.Lock()
	_, seen := gps.MessageCache[gossipMsg.ID]
	if !seen && contentKey != "" {
		_, seen = gps.MessageCache[contentKey]
	}
	if seen {
		gps.Mutex.Unlock()
		return // Already processed
	}
	now := time.Now()
	gps.MessageCache[gossipMsg.ID] = now
	if contentKey != "" {
		gps.MessageCache[contentKey] = now
	}
	gps.Mutex.Unlock()

	// Skip TTL checks for direct messages (TTL = 0)
	// TTL is only needed for multi-hop gossip propagation
	if gossipMsg.TTL == 0 {
		log.Printf("Processing direct message (no TTL needed): %s", gossipMsg.ID)
	} else {
		// Only decrement TTL for actual gossip messages
		gossipMsg.TTL--
		log.Printf("Forwarding gossip message (TTL=%d): %s", gossipMsg.TTL, gossipMsg.ID)
	}

	log.Printf("Received gossip message from %s on topic %s: %s", gossipMsg.Sender, gossipMsg.Topic, gossipMsg.ID)
	// <-- Write the logic to check the processing of the message based on the message type --> TODO

	Channel.AppendMessage(&gossipMsg)
	// Call handler if we're subscribed to this topic
	gps.Mutex.RLock()
	handler, subscribed := gps.Handlers[gossipMsg.Topic]
	gps.Mutex.RUnlock()

	if subscribed && handler != nil {
		handler(&gossipMsg)
	}

	// Forward message to other peers (gossip). Re-marshal so the DECREMENTED
	// TTL travels on the wire — the old code forwarded the original bytes, so
	// TTL never dropped hop-to-hop and a message resurrected forever (audit
	// NET-04). Same JSON schema, so nodes on any branch parse it identically.
	if gossipMsg.TTL > 0 {
		forwardBytes, mErr := json.Marshal(gossipMsg)
		if mErr != nil {
			log.Printf("Failed to re-marshal gossip message for forward: %v", mErr)
			return
		}
		Publisher.GossipMessage(gps, forwardBytes)
	}
}

// readMessage reads a message from the stream using delimiter
func readMessage(s network.Stream) ([]byte, error) {
	const MaxMessageSize = 7 * 1024 * 1024 // 7 MB
	// Buffered read to the delimiter, hard-capped by a LimitReader so an
	// oversized or unterminated message cannot grow the buffer without bound
	// (audit NET-03: the old loop did one syscall PER BYTE — ~7M reads/msg).
	r := bufio.NewReader(io.LimitReader(s, MaxMessageSize+1))
	msg, err := r.ReadBytes(config.Delimiter)
	if err != nil {
		// EOF without a delimiter (peer closed early or hit the cap), a read
		// deadline, or a transport error — all fail closed.
		return nil, err
	}
	msg = msg[:len(msg)-1] // strip the trailing delimiter
	if len(msg) > MaxMessageSize {
		return nil, fmt.Errorf("message size exceeds limit of %d bytes", MaxMessageSize)
	}
	return msg, nil
}

// Close closes the gossip pub/sub instance
func Close(gps *PubSubMessages.GossipPubSub) error {
	gps.Mutex.Lock()
	defer gps.Mutex.Unlock()

	// Cancel cleanup goroutine if it exists
	if gps.CleanupCancel != nil {
		log.Printf("Canceling cache cleanup goroutine")
		gps.CleanupCancel()
	}

	for topic := range gps.Topics {
		Connector.Unsubscribe(gps, topic)
	}

	return nil
}
