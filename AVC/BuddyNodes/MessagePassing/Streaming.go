package MessagePassing

import (
	"context"
	"fmt"
	"time"

	"jmdn/AVC/BuddyNodes/DataLayer"
	ServiceLayer "jmdn/AVC/BuddyNodes/ServiceLayer"
	"jmdn/AVC/BuddyNodes/Types"
	"jmdn/AVC/BuddyNodes/common"
	"jmdn/config"
	GRO "jmdn/config/GRO"
	AVCStruct "jmdn/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
)

func NewListenerNode(logger_ctx context.Context, h host.Host, responseHandler AVCStruct.ResponseHandler) *StructListener {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.NewListenerNode")
	defer span.End()

	startTime := time.Now().UTC()
	peerID := h.ID()
	span.SetAttributes(attribute.String("peer_id", peerID.String()))

	logger().NamedLogger.Info(spanCtx, "Initializing ListenerNode",
		ion.String("peer_id", peerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.NewListenerNode"))

	if StreamCacheLocal == nil {
		var err error
		StreamCacheLocal, err = common.InitializeGRO(GRO.StreamCacheParallelCleanUpRoutineLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(spanCtx, "Failed to initialize StreamCache local manager",
				err,
				ion.String("peer_id", peerID.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.NewListenerNode"))
			return nil
		}
	}

	// Reduced TTL from 5 minutes to 2 minutes for faster cleanup of stale streams
	streamCache, err := NewStreamCacheBuilder(nil).SetHost(h).SetMaxStreams(20).SetTTL(2 * time.Minute).SetAccessOrder().Build()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "stream_cache_build_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to create stream cache",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.NewListenerNode"))
		panic(fmt.Sprintf("failed to create stream cache: %v", err))
	}
	streamCache.ParallelCleanUpRoutine()

	span.SetAttributes(
		attribute.Int("max_streams", 20),
		attribute.Float64("ttl_minutes", 2.0),
	)

	// Initialize CRDT Layer
	CRDTLayer := ServiceLayer.GetServiceController()

	Node := &AVCStruct.BuddyNode{
		Host:            h,
		Network:         h.Network(),
		PeerID:          h.ID(),
		ResponseHandler: responseHandler,
		StreamCache:     streamCache.GetStreamCache(), // Max 20 streams, 2min TTL
		CRDTLayer:       CRDTLayer,                    // Initialize CRDT Layer
		MetaData: AVCStruct.MetaData{
			Received:  0,
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}
	listener := NewListenerStruct(Node)
	// Set the ResponseHandler for handling subscription responses
	listener.ResponseHandler = responseHandler

	// Add the buddy Node to the Listener node for singleton instance
	AVCStruct.NewGlobalVariables().Set_ForListner(Node)

	// Set up the stream handler for the listener nodes message protocol
	h.SetStreamHandler(config.SubmitMessageProtocol, func(stream network.Stream) {
		logger().NamedLogger.Info(spanCtx, "New submit message connection received",
			ion.String("remote_peer_id", stream.Conn().RemotePeer().String()),
			ion.String("protocol", string(config.SubmitMessageProtocol)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.NewListenerNode"))
		StreamCacheLocal.Go(GRO.StreamCacheMessageListenerThread, func(ctx context.Context) error {
			listener.HandleSubmitMessageStream(logger_ctx, stream)
			return nil
		})
	})

	span.SetAttributes(attribute.String("protocol", string(config.SubmitMessageProtocol)))

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "ListenerNode initialized successfully",
		ion.String("peer_id", peerID.String()),
		ion.String("protocol", string(config.SubmitMessageProtocol)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.NewListenerNode"))

	return listener
}

// NewBuddyNode creates a new BuddyNode instance from an existing host
func NewBuddyNode(logger_ctx context.Context, h host.Host, buddies *AVCStruct.Buddies, responseHandler AVCStruct.ResponseHandler, pubsub *AVCStruct.GossipPubSub) *AVCStruct.BuddyNode {
	// Record trace span and close it
	spanCtx, span := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.NewBuddyNode")
	defer span.End()

	startTime := time.Now().UTC()
	peerID := h.ID()
	span.SetAttributes(attribute.String("peer_id", peerID.String()))

	logger().NamedLogger.Info(spanCtx, "Creating buddy node",
		ion.String("peer_id", peerID.String()),
		ion.Bool("pubsub_provided", pubsub != nil),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.NewBuddyNode"))

	if StreamCacheLocal == nil {
		var err error
		StreamCacheLocal, err = common.InitializeGRO(GRO.StreamCacheParallelCleanUpRoutineLocal)
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "gro_init_failed"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(spanCtx, "Failed to initialize StreamCache local manager",
				err,
				ion.String("peer_id", peerID.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "MessagePassing.NewBuddyNode"))
			return nil
		}
	}

	if pubsub == nil {
		span.SetAttributes(attribute.String("status", "pubsub_nil_warning"))
		logger().NamedLogger.Warn(spanCtx, "Pubsub parameter is nil",
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.NewBuddyNode"))
	} else {
		span.SetAttributes(attribute.String("pubsub_host_id", pubsub.Host.ID().String()))
		logger().NamedLogger.Info(spanCtx, "Pubsub parameter is valid",
			ion.String("peer_id", peerID.String()),
			ion.String("pubsub_host_id", pubsub.Host.ID().String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.NewBuddyNode"))
	}

	// Reduced TTL from 5 minutes to 2 minutes for faster cleanup of stale streams
	streamCache, err := NewStreamCacheBuilder(nil).SetHost(h).SetMaxStreams(20).SetTTL(2 * time.Minute).SetAccessOrder().Build()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "stream_cache_build_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(spanCtx, "Failed to create stream cache",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.NewBuddyNode"))
		panic(fmt.Sprintf("failed to create stream cache: %v", err))
	}
	streamCache.ParallelCleanUpRoutine()

	span.SetAttributes(
		attribute.Int("max_streams", 20),
		attribute.Float64("ttl_minutes", 2.0),
		attribute.Int("buddies_count", len(buddies.Buddies_Nodes)),
	)

	buddy := &AVCStruct.BuddyNode{
		Host:            h,
		Network:         h.Network(),
		PeerID:          h.ID(),
		BuddyNodes:      *buddies,
		ResponseHandler: responseHandler,
		PubSub:          pubsub,
		StreamCache:     streamCache.GetStreamCache(),
		MetaData: AVCStruct.MetaData{
			Received:  0,
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}
	buddyStream := NewStructBuddyNode(buddy)

	// Add the buddy Node to the Pubsub node for singleton instance
	AVCStruct.NewGlobalVariables().Set_PubSubNode(buddy)

	// Set up the stream handler for the buddy nodes message protocol
	h.SetStreamHandler(config.BuddyNodesMessageProtocol, func(stream network.Stream) {
		logger().NamedLogger.Info(spanCtx, "New buddy nodes connection received",
			ion.String("remote_peer_id", stream.Conn().RemotePeer().String()),
			ion.String("protocol", string(config.BuddyNodesMessageProtocol)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.NewBuddyNode"))
		StreamCacheLocal.Go(GRO.BuddyNodesMessageProtocolThread, func(ctx context.Context) error {
			buddyStream.HandleBuddyNodesMessageStream(h, stream)
			return nil
		})
	})

	span.SetAttributes(attribute.String("protocol", string(config.BuddyNodesMessageProtocol)))

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(spanCtx, "BuddyNode initialized successfully",
		ion.String("peer_id", peerID.String()),
		ion.String("protocol", string(config.BuddyNodesMessageProtocol)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.NewBuddyNode"))

	return buddy
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
// Uses LRU cache with TTL for optimal performance and resource efficiency
func (StructBuddyNode *StructBuddyNode) SendMessageToPeer(logger_ctx context.Context, peerID peer.ID, message string) error {
	// Record trace span and close it
	sendSpanCtx, sendSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.SendMessageToPeer")
	defer sendSpan.End()

	startTime := time.Now().UTC()
	sendSpan.SetAttributes(
		attribute.String("peer_id", peerID.String()),
		attribute.Int("message_length", len(message)),
	)

	logger().NamedLogger.Info(sendSpanCtx, "Sending buddy message to peer",
		ion.String("peer_id", peerID.String()),
		ion.String("message", message),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.SendMessageToPeer"))

	// Get or create a stream from the cache
	StreamCache := NewStreamCacheBuilder(StructBuddyNode.BuddyNode.StreamCache)
	if StreamCache == nil {
		sendSpan.RecordError(fmt.Errorf("failed to get StreamCache"))
		sendSpan.SetAttributes(attribute.String("status", "stream_cache_nil"))
		duration := time.Since(startTime).Seconds()
		sendSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to get the stream cache, nil streamcache occurred")
	}

	stream, err := StreamCache.GetStream(peerID)
	if err != nil {
		sendSpan.RecordError(err)
		sendSpan.SetAttributes(attribute.String("status", "get_stream_failed"))
		duration := time.Since(startTime).Seconds()
		sendSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to get stream to %s: %v", peerID, err)
	}

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		// If write fails, the stream might be invalid, close it and try to get a new one
		StreamCache.CloseStream(peerID)
		sendSpan.RecordError(err)
		sendSpan.SetAttributes(attribute.String("status", "write_failed"))
		duration := time.Since(startTime).Seconds()
		sendSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	sendSpan.SetAttributes(attribute.String("message_sent", "true"))

	if message != config.Type_ACK_True && message != config.Type_ACK_False {
		// Update metadata
		StructBuddyNode.BuddyNode.Mutex.Lock()
		StructBuddyNode.BuddyNode.MetaData.Sent++
		StructBuddyNode.BuddyNode.MetaData.Total++
		StructBuddyNode.BuddyNode.MetaData.UpdatedAt = time.Now().UTC()
		StructBuddyNode.BuddyNode.Mutex.Unlock()

		sendSpan.SetAttributes(attribute.Bool("metadata_updated", true))
	}

	duration := time.Since(startTime).Seconds()
	sendSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(sendSpanCtx, "Successfully sent buddy message to peer",
		ion.String("peer_id", peerID.String()),
		ion.String("message", message),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.SendMessageToPeer"))

	return nil
}

// CloseAllStreams closes all streams in the cache (for cleanup)
func (StructBuddyNode *StructBuddyNode) CloseAllStreams(logger_ctx context.Context) {
	// Record trace span and close it
	closeSpanCtx, closeSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.CloseAllStreams")
	defer closeSpan.End()

	startTime := time.Now().UTC()

	logger().NamedLogger.Info(closeSpanCtx, "Closing all streams",
		ion.String("peer_id", StructBuddyNode.BuddyNode.PeerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.CloseAllStreams"))

	StreamCache := NewStreamCacheBuilder(StructBuddyNode.BuddyNode.StreamCache)
	if StreamCache == nil {
		closeSpan.SetAttributes(attribute.String("status", "stream_cache_nil"))
		logger().NamedLogger.Warn(closeSpanCtx, "StreamCache is nil, cannot close streams",
			ion.String("peer_id", StructBuddyNode.BuddyNode.PeerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "MessagePassing.CloseAllStreams"))
		return
	}

	StreamCache.CloseAll()

	duration := time.Since(startTime).Seconds()
	closeSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(closeSpanCtx, "Successfully closed all streams",
		ion.String("peer_id", StructBuddyNode.BuddyNode.PeerID.String()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.CloseAllStreams"))
}

// GetStreamCacheStats returns statistics about the stream cache
func (StructBuddyNode *StructBuddyNode) GetStreamCacheStats(logger_ctx context.Context) map[string]interface{} {
	// Record trace span and close it
	statsSpanCtx, statsSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.GetStreamCacheStats")
	defer statsSpan.End()

	startTime := time.Now().UTC()

	logger().NamedLogger.Info(statsSpanCtx, "Getting stream cache stats",
		ion.String("peer_id", StructBuddyNode.BuddyNode.PeerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.GetStreamCacheStats"))

	StreamCache := NewStreamCacheBuilder(StructBuddyNode.BuddyNode.StreamCache)
	if StreamCache == nil {
		statsSpan.SetAttributes(attribute.String("status", "stream_cache_nil"))
		duration := time.Since(startTime).Seconds()
		statsSpan.SetAttributes(attribute.Float64("duration", duration))
		return map[string]interface{}{
			"active_streams": 0,
			"max_streams":    0,
			"ttl_seconds":    0,
			"total_accesses": 0,
		}
	}

	stats := StreamCache.GetStats()

	// Set span attributes from stats if available
	if activeStreams, ok := stats["active_streams"].(int); ok {
		statsSpan.SetAttributes(attribute.Int("active_streams", activeStreams))
	}
	if maxStreams, ok := stats["max_streams"].(int); ok {
		statsSpan.SetAttributes(attribute.Int("max_streams", maxStreams))
	}

	duration := time.Since(startTime).Seconds()
	statsSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(statsSpanCtx, "Successfully retrieved stream cache stats",
		ion.String("peer_id", StructBuddyNode.BuddyNode.PeerID.String()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.GetStreamCacheStats"))

	return stats
}

// GetVotesFromCRDT retrieves all votes from the CRDT for a given key
func GetVotesFromCRDT(logger_ctx context.Context, crdtLayer *Types.Controller, key string) ([]string, bool) {
	// Record trace span and close it
	votesSpanCtx, votesSpan := logger().NamedLogger.Tracer("MessagePassing").Start(logger_ctx, "MessagePassing.GetVotesFromCRDT")
	defer votesSpan.End()

	startTime := time.Now().UTC()
	votesSpan.SetAttributes(attribute.String("key", key))

	logger().NamedLogger.Info(votesSpanCtx, "Getting votes from CRDT",
		ion.String("key", key),
		ion.Bool("crdt_layer_provided", crdtLayer != nil),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.GetVotesFromCRDT"))

	if crdtLayer == nil {
		votesSpan.SetAttributes(attribute.String("status", "crdt_layer_nil"))
		duration := time.Since(startTime).Seconds()
		votesSpan.SetAttributes(attribute.Float64("duration", duration))
		return nil, false
	}

	// Get all elements from the CRDT set
	votes, found := DataLayer.GetSet(crdtLayer, key)

	votesSpan.SetAttributes(
		attribute.Bool("found", found),
		attribute.Int("votes_count", len(votes)),
	)

	duration := time.Since(startTime).Seconds()
	votesSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(votesSpanCtx, "Retrieved votes from CRDT",
		ion.String("key", key),
		ion.Bool("found", found),
		ion.Int("votes_count", len(votes)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "MessagePassing.GetVotesFromCRDT"))

	return votes, found
}
