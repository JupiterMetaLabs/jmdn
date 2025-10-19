package MessagePassing

import (
	"fmt"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/Pubsub"
	"gossipnode/config"
	"gossipnode/logging"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Function to initialize the loggers
func Init_Loggers(loki bool) {
	_, err := log.NewLoggerBuilder().
		SetURL(logging.GetLokiURL(), loki).
		SetFileName("buddy_nodes.log").
		SetTopic(log.Consensus_TOPIC).
		SetDirectory("logs").
		SetBatchSize(100).
		SetBatchWait(2 * time.Second).
		SetTimeout(6 * time.Second).
		SetKeepLogs(true).
		Build()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize Consensus loggers: %v", err))
	}

	_, err = log.NewLoggerBuilder().
		SetURL(logging.GetLokiURL(), loki).
		SetFileName("buddy_nodes.log").
		SetTopic(log.Messages_TOPIC).
		SetDirectory("logs").
		SetBatchSize(100).
		SetBatchWait(2 * time.Second).
		SetTimeout(6 * time.Second).
		SetKeepLogs(true).
		Build()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize Messages loggers: %v", err))
	}
}

func StartStreamHandlers(h host.Host, buddies *Buddies, responseHandler ResponseHandler, pubsub *Pubsub.GossipPubSub) {
	buddy := NewBuddyNode(h, buddies, responseHandler, pubsub)
	listener := NewListenerNode(h, responseHandler)

	// Set stream handlers (these will run continuously)
	h.SetStreamHandler(config.BuddyNodesMessageProtocol, func(stream network.Stream) {
		log.LogConsensusInfo("New buddy nodes connection received",
			zap.String("peer", stream.Conn().RemotePeer().String()),
			zap.String("protocol", string(config.BuddyNodesMessageProtocol)))
		go buddy.HandleBuddyNodesMessageStream(stream)
	})

	h.SetStreamHandler(config.SubmitMessageProtocol, func(stream network.Stream) {
		log.LogMessagesInfo("New submit message connection received",
			zap.String("peer", stream.Conn().RemotePeer().String()),
			zap.String("protocol", string(config.SubmitMessageProtocol)))
		go listener.HandleSubmitMessageStream(stream)
	})

	log.LogConsensusInfo("Stream handlers started and listening for connections")
}

func NewListenerNode(h host.Host, responseHandler ResponseHandler) *BuddyNode {
	streamCache, err := NewStreamCacheBuilder().SetHost(h).SetMaxStreams(20).SetTTL(5 * time.Minute).SetAccessOrder().Build()
	if err != nil {
		panic(fmt.Sprintf("failed to create stream cache: %v", err))
	}
	streamCache.ParallelCleanUpRoutine()

	listener := &BuddyNode{
		Host:            h,
		Network:         h.Network(),
		PeerID:          h.ID(),
		ResponseHandler: responseHandler,
		StreamCache:     streamCache, // Max 20 streams, 5min TTL
		MetaData: MetaData{
			Received:  0,
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now(),
		},
	}

	// Set up the stream handler for the listener nodes message protocol
	h.SetStreamHandler(config.SubmitMessageProtocol, listener.HandleSubmitMessageStream)

	log.LogConsensusInfo(fmt.Sprintf("ListenerNode initialized with ID: %s", h.ID()), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewListenerNode"))
	log.LogConsensusInfo(fmt.Sprintf("Listening for listener messages on protocol: %s", config.SubmitMessageProtocol), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewListenerNode"))

	return listener
}

// NewBuddyNode creates a new BuddyNode instance from an existing host
func NewBuddyNode(h host.Host, buddies *Buddies, responseHandler ResponseHandler, pubsub *Pubsub.GossipPubSub) *BuddyNode {
	streamCache, err := NewStreamCacheBuilder().SetHost(h).SetMaxStreams(20).SetTTL(5 * time.Minute).SetAccessOrder().Build()
	if err != nil {
		panic(fmt.Sprintf("failed to create stream cache: %v", err))
	}
	streamCache.ParallelCleanUpRoutine()

	buddy := &BuddyNode{
		Host:            h,
		Network:         h.Network(),
		PeerID:          h.ID(),
		BuddyNodes:      *buddies,
		ResponseHandler: responseHandler,
		PubSub:          pubsub,
		StreamCache:     streamCache,
		MetaData: MetaData{
			Received:  0,
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now(),
		},
	}

	// Set up the stream handler for the buddy nodes message protocol
	h.SetStreamHandler(config.BuddyNodesMessageProtocol, buddy.HandleBuddyNodesMessageStream)

	log.LogConsensusInfo(fmt.Sprintf("BuddyNode initialized with ID: %s", h.ID()), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewBuddyNode"))
	log.LogConsensusInfo(fmt.Sprintf("Listening for buddy messages on protocol: %s", config.BuddyNodesMessageProtocol), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewBuddyNode"))

	return buddy
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
// Uses LRU cache with TTL for optimal performance and resource efficiency
func (buddy *BuddyNode) SendMessageToPeer(peerID peer.ID, message string) error {
	// Get or create a stream from the cache
	stream, err := buddy.StreamCache.GetStream(peerID)
	if err != nil {
		return fmt.Errorf("failed to get stream to %s: %v", peerID, err)
	}

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		// If write fails, the stream might be invalid, close it and try to get a new one
		buddy.StreamCache.CloseStream(peerID)
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	if message != config.Type_ACK_True && message != config.Type_ACK_False {
		// Update metadata
		buddy.Mutex.Lock()
		buddy.MetaData.Sent++
		buddy.MetaData.Total++
		buddy.MetaData.UpdatedAt = time.Now()
		buddy.Mutex.Unlock()

		log.LogConsensusInfo(fmt.Sprintf("Sent buddy message to %s: %s", peerID, message), zap.String("peer", peerID.String()), zap.String("message", message), zap.String("function", "SendMessageToPeer"))
	}

	return nil
}

// CloseAllStreams closes all streams in the cache (for cleanup)
func (buddy *BuddyNode) CloseAllStreams() {
	if buddy.StreamCache != nil {
		buddy.StreamCache.CloseAll()
	}
}

// GetStreamCacheStats returns statistics about the stream cache
func (buddy *BuddyNode) GetStreamCacheStats() map[string]interface{} {
	if buddy.StreamCache != nil {
		return buddy.StreamCache.GetStats()
	}
	return map[string]interface{}{
		"active_streams": 0,
		"max_streams":    0,
		"ttl_seconds":    0,
		"total_accesses": 0,
	}
}
