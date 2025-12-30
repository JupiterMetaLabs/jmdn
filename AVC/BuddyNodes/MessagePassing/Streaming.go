package MessagePassing

import (
	"context"
	"fmt"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	ServiceLayer "gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/AVC/BuddyNodes/common"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/logging"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Function to initialize the loggers
func Init_Loggers(loki bool) {
	var lokiURL string
	if loki {
		lokiURL = logging.GetLokiURL()
	} else {
		lokiURL = ""
	}

	_, err := log.NewLoggerBuilder().
		SetURL(lokiURL, loki).
		SetFileName("buddy_nodes.log").
		SetTopic(config.PubSub_ConsensusChannel).
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
		SetURL(lokiURL, loki).
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

func NewListenerNode(h host.Host, responseHandler AVCStruct.ResponseHandler) *StructListener {
	if StreamCacheLocal == nil {
		var err error
		StreamCacheLocal, err = common.InitializeGRO(GRO.StreamCacheParallelCleanUpRoutineLocal)
		if err != nil {
			fmt.Printf("❌ Failed to initialize StreamCache local manager: %v\n", err)
			return nil
		}
	}
	// Reduced TTL from 5 minutes to 2 minutes for faster cleanup of stale streams
	streamCache, err := NewStreamCacheBuilder(nil).SetHost(h).SetMaxStreams(20).SetTTL(2 * time.Minute).SetAccessOrder().Build()
	if err != nil {
		panic(fmt.Sprintf("failed to create stream cache: %v", err))
	}
	streamCache.ParallelCleanUpRoutine()

	// Initialize CRDT Layer
	CRDTLayer := ServiceLayer.GetServiceController()

	Node := &AVCStruct.BuddyNode{
		Host:            h,
		Network:         h.Network(),
		PeerID:          h.ID(),
		ResponseHandler: responseHandler,
		StreamCache:     streamCache.GetStreamCache(), // Max 20 streams, 5min TTL
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
		log.LogMessagesInfo("New submit message connection received",
			zap.String("peer", stream.Conn().RemotePeer().String()),
			zap.String("protocol", string(config.SubmitMessageProtocol)))
		StreamCacheLocal.Go(GRO.StreamCacheMessageListenerThread, func(ctx context.Context) error {
			listener.HandleSubmitMessageStream(stream)
			return nil
		})
	})

	log.LogConsensusInfo(fmt.Sprintf("ListenerNode initialized with ID: %s", h.ID()), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewListenerNode"))
	log.LogConsensusInfo(fmt.Sprintf("Listening for listener messages on protocol: %s", config.SubmitMessageProtocol), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewListenerNode"))

	return listener
}

// NewBuddyNode creates a new BuddyNode instance from an existing host
func NewBuddyNode(h host.Host, buddies *AVCStruct.Buddies, responseHandler AVCStruct.ResponseHandler, pubsub *AVCStruct.GossipPubSub) *AVCStruct.BuddyNode {
	if StreamCacheLocal == nil {
		var err error
		StreamCacheLocal, err = common.InitializeGRO(GRO.StreamCacheParallelCleanUpRoutineLocal)
		if err != nil {
			fmt.Printf("❌ Failed to initialize StreamCache local manager: %v\n", err)
			return nil
		}
	}
	// Debug logging
	fmt.Printf("NewBuddyNode: Creating buddy node for peer %s\n", h.ID())
	if pubsub == nil {
		fmt.Printf("NewBuddyNode: WARNING - pubsub parameter is nil!\n")
	} else {
		fmt.Printf("NewBuddyNode: pubsub parameter is valid, Host: %s\n", pubsub.Host.ID())
	}

	// Reduced TTL from 5 minutes to 2 minutes for faster cleanup of stale streams
	streamCache, err := NewStreamCacheBuilder(nil).SetHost(h).SetMaxStreams(20).SetTTL(2 * time.Minute).SetAccessOrder().Build()
	if err != nil {
		panic(fmt.Sprintf("failed to create stream cache: %v", err))
	}
	streamCache.ParallelCleanUpRoutine()

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
		log.LogConsensusInfo("New buddy nodes connection received",
			zap.String("peer", stream.Conn().RemotePeer().String()),
			zap.String("protocol", string(config.BuddyNodesMessageProtocol)))
		StreamCacheLocal.Go(GRO.BuddyNodesMessageProtocolThread, func(ctx context.Context) error {
			buddyStream.HandleBuddyNodesMessageStream(h, stream)
			return nil
		})
	})

	log.LogConsensusInfo(fmt.Sprintf("BuddyNode initialized with ID: %s", h.ID()), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewBuddyNode"))
	log.LogConsensusInfo(fmt.Sprintf("Listening for buddy messages on protocol: %s", config.BuddyNodesMessageProtocol), zap.String("peer", h.ID().String()), zap.String("topic", log.Consensus_TOPIC), zap.String("function", "NewBuddyNode"))

	return buddy
}

// SendMessageToPeer sends a message to a specific peer using peer.ID (for already connected peers)
// Uses LRU cache with TTL for optimal performance and resource efficiency
func (StructBuddyNode *StructBuddyNode) SendMessageToPeer(peerID peer.ID, message string) error {
	// Get or create a stream from the cache
	StreamCache := NewStreamCacheBuilder(StructBuddyNode.BuddyNode.StreamCache)
	if StreamCache == nil {
		return fmt.Errorf("failed to get the stream cache, nil streamcache occurred")
	}

	stream, err := StreamCache.GetStream(peerID)
	if err != nil {
		return fmt.Errorf("failed to get stream to %s: %v", peerID, err)
	}

	// Send the message
	_, err = stream.Write([]byte(message + string(rune(config.Delimiter))))
	if err != nil {
		// If write fails, the stream might be invalid, close it and try to get a new one
		StreamCache.CloseStream(peerID)
		return fmt.Errorf("failed to send message to %s: %v", peerID, err)
	}

	if message != config.Type_ACK_True && message != config.Type_ACK_False {
		// Update metadata
		StructBuddyNode.BuddyNode.Mutex.Lock()
		StructBuddyNode.BuddyNode.MetaData.Sent++
		StructBuddyNode.BuddyNode.MetaData.Total++
		StructBuddyNode.BuddyNode.MetaData.UpdatedAt = time.Now().UTC()
		StructBuddyNode.BuddyNode.Mutex.Unlock()

		log.LogConsensusInfo(fmt.Sprintf("Sent buddy message to %s: %s", peerID, message), zap.String("peer", peerID.String()), zap.String("message", message), zap.String("function", "SendMessageToPeer"))
	}

	return nil
}

// CloseAllStreams closes all streams in the cache (for cleanup)
func (StructBuddyNode *StructBuddyNode) CloseAllStreams() {
	StreamCache := NewStreamCacheBuilder(StructBuddyNode.BuddyNode.StreamCache)
	if StreamCache == nil {
		return
	}

	StreamCache.CloseAll()
}

// GetStreamCacheStats returns statistics about the stream cache
func (StructBuddyNode *StructBuddyNode) GetStreamCacheStats() map[string]interface{} {
	StreamCache := NewStreamCacheBuilder(StructBuddyNode.BuddyNode.StreamCache)
	if StreamCache == nil {
		return map[string]interface{}{
			"active_streams": 0,
			"max_streams":    0,
			"ttl_seconds":    0,
			"total_accesses": 0,
		}
	}

	return StreamCache.GetStats()
}

// GetVotesFromCRDT retrieves all votes from the CRDT for a given key
func GetVotesFromCRDT(crdtLayer *Types.Controller, key string) ([]string, bool) {
	if crdtLayer == nil {
		return nil, false
	}

	// Get all elements from the CRDT set
	votes, found := DataLayer.GetSet(crdtLayer, key)
	return votes, found
}
