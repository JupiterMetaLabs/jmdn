package messaging

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/DB_OPs"
	"gossipnode/Vote"
	"gossipnode/config"
	"gossipnode/config/GRO"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/messaging/BlockProcessing"
	GROHelper "gossipnode/messaging/common"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// BroadcastMessage represents a message that is broadcast through the network
type BroadcastMessageStruct struct {
	ID        string `json:"id"`        // Unique message ID
	Sender    string `json:"sender"`    // Original sender's peer ID
	Content   string `json:"content"`   // Message content
	Timestamp int64  `json:"timestamp"` // Unix timestamp when message was created
	Hops      int    `json:"hops"`      // How many hops this message has made
	Type      string `json:"type"`      // Message type: "general", "vote_trigger", etc.
	Data      string `json:"data"`      // Additional data for specific message types
}

// Track seen messages to prevent loops
var (
	seenMessages   = make(map[string]time.Time)
	seenMessagesMu sync.RWMutex
)

// generateMessageID creates a unique ID for a broadcast message
func generateMessageID(sender, content string, timestamp int64) string {
	hasher := sha256.New()
	hasher.Write([]byte(fmt.Sprintf("%s-%s-%d", sender, content, timestamp)))
	hash := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
	return hash[:16] // Return first 16 chars for brevity
}

// cleanupOldMessages periodically removes expired message IDs.
// It stops when ctx is cancelled.
func cleanupOldMessages(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			seenMessagesMu.Lock()
			now := time.Now().UTC()
			for id, timestamp := range seenMessages {
				if now.Sub(timestamp) > config.MessageExpiryTime {
					delete(seenMessages, id)
				}
			}
			seenMessagesMu.Unlock()
		}
	}
}

// Start message cleanup in background
// StartBroadcastCleanup initializes the GRO and starts the cleanup thread.
func StartBroadcastCleanup() {
	if BroadcastLocalGRO == nil {
		var err error
		BroadcastLocalGRO, err = GROHelper.InitializeGRO(GRO.BroadcastLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize BroadcastLocalGRO", err)
			return
		}
	}
	BroadcastLocalGRO.Go(GRO.BroadcastCleanupThread, func(ctx context.Context) error {
		cleanupOldMessages(ctx)
		return nil
	})
}

// isMessageSeen checks if we've seen this message before
func isMessageSeen(msgID string) bool {
	seenMessagesMu.RLock()
	defer seenMessagesMu.RUnlock()
	_, exists := seenMessages[msgID]
	return exists
}

// markMessageSeen records that we've seen this message
func markMessageSeen(msgID string) {
	seenMessagesMu.Lock()
	defer seenMessagesMu.Unlock()
	seenMessages[msgID] = time.Now().UTC()
}

// HandleBroadcastStream processes incoming broadcast messages
func HandleBroadcastStream(stream network.Stream) {
	defer stream.Close()

	// Record metrics
	metrics.MessagesReceivedCounter.WithLabelValues("broadcast", stream.Conn().RemotePeer().String()).Inc()

	// Read the incoming message
	reader := bufio.NewReader(stream)
	messageBytes, err := reader.ReadBytes('\n')
	if err != nil {
		if err != io.EOF {
			broadcastLogger().Error(context.Background(), "Error reading broadcast message", err,
				ion.String("peer", stream.Conn().RemotePeer().String()))
		}
		return
	}

	// Parse the message
	var msg BroadcastMessageStruct
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to unmarshal broadcast message", err)
		return
	}

	// Check if we've already seen this message
	if isMessageSeen(msg.ID) {
		// We've already processed this message, ignore
		return
	}

	// Mark as seen to avoid reprocessing
	markMessageSeen(msg.ID)

	broadcastLogger().Info(context.Background(), "Broadcast received",
		ion.String("sender", msg.Sender),
		ion.String("content", msg.Content))

	// Handle different message types
	if msg.Type == "vote_trigger" {
		handleVoteTriggerBroadcast(msg)
	}

	// Only rebroadcast if we haven't reached max hops
	if msg.Hops < config.MaxHops {
		// Forward to our peers
		msg.Hops++
		localPeer := stream.Conn().LocalPeer().String()
		broadcastLogger().Info(context.Background(), "Rebroadcasting message",
			ion.String("msg_id", msg.ID),
			ion.String("origin", msg.Sender),
			ion.String("via", localPeer),
			ion.Int("hops", msg.Hops))

		// Instead of trying to get the host from the connection,
		// get it from the stored node instance
		// We'll need to access the global node instance
		if hostInstance := getHostInstance(); hostInstance != nil {
			forwardBroadcast(hostInstance, msg)
		} else {
			broadcastLogger().Error(context.Background(), "Cannot access host instance for forwarding broadcast",
				errors.New("getHostInstance returned nil"))
		}
	} else {
		broadcastLogger().Info(context.Background(), "Max hops reached, not rebroadcasting",
			ion.String("msg_id", msg.ID),
			ion.Int("hops", msg.Hops))
	}
}

// Store a reference to the host for broadcast handling
var (
	hostInstance host.Host
	hostMutex    sync.RWMutex
)

// SetHostInstance stores the host instance for broadcast use
func SetHostInstance(h host.Host) {
	hostMutex.Lock()
	defer hostMutex.Unlock()
	hostInstance = h
}

// getHostInstance safely retrieves the stored host instance
func getHostInstance() host.Host {
	hostMutex.RLock()
	defer hostMutex.RUnlock()
	return hostInstance
}

// forwardBroadcast sends the message to all connected peers
func forwardBroadcast(h host.Host, msg BroadcastMessageStruct) {
	// Get all connected peers
	peers := h.Network().Peers()

	// Convert message to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to marshal broadcast message", err)
		return
	}
	msgBytes = append(msgBytes, '\n')

	// Track how many peers we successfully broadcasted to
	var successCount int

	// Send to each peer (except original sender)
	for _, peerID := range peers {
		// Don't send back to the original sender
		if peerID.String() == msg.Sender {
			continue
		}

		// Open a stream to the peer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stream, err := h.NewStream(ctx, peerID, config.BroadcastProtocol)
		cancel()

		if err != nil {
			broadcastLogger().Warn(context.Background(), "Failed to open broadcast stream",
				ion.Err(err),
				ion.String("peer", peerID.String()))
			continue
		}

		// Write the message
		_, err = stream.Write(msgBytes)
		if err != nil {
			broadcastLogger().Warn(context.Background(), "Failed to write broadcast message",
				ion.Err(err),
				ion.String("peer", peerID.String()))
			stream.Close()
			continue
		}

		// Close the stream
		stream.Close()
		successCount++

		// Record metrics
		metrics.MessagesSentCounter.WithLabelValues("broadcast", peerID.String()).Inc()
	}

	broadcastLogger().Info(context.Background(), "Broadcast forwarded to peers",
		ion.String("msg_id", msg.ID),
		ion.Int("peers", successCount))
}

// BroadcastMessage sends a message to all connected peers
func BroadcastMessage(h host.Host, content string) error {
	if BroadcastLocalGRO == nil {
		var err error
		BroadcastLocalGRO, err = GROHelper.InitializeGRO(GRO.BroadcastLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize BroadcastLocalGRO", err)
			return err
		}
	}
	// Create a new broadcast message
	now := time.Now().UTC().Unix()
	msg := BroadcastMessageStruct{
		Sender:    h.ID().String(),
		Content:   content,
		Timestamp: now,
		Hops:      0,
	}

	// Generate a unique ID based on content and timestamp
	msg.ID = generateMessageID(msg.Sender, content, now)

	// Remember this message so we don't process it if we receive it back
	markMessageSeen(msg.ID)

	// Convert to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal broadcast message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	// Get all connected peers
	peers := h.Network().Peers()
	if len(peers) == 0 {
		return fmt.Errorf("no connected peers to broadcast to")
	}

	broadcastLogger().Info(context.Background(), "Starting broadcast to peers",
		ion.String("msg_id", msg.ID),
		ion.Int("peers", len(peers)))

	// Send message to all peers
	wg, err := BroadcastLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BroadcastForwardWG)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for broadcast forwarding", err)
		return fmt.Errorf("failed to create waitgroup for broadcast forwarding: %w", err)
	}
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		BroadcastLocalGRO.Go(GRO.BroadcastForwardThread, func(ctx context.Context) error {
			peer := peerID

			// Open stream to peer with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
			if err != nil {
				broadcastLogger().Warn(context.Background(), "Failed to open broadcast stream",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			defer stream.Close()

			// Send the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				broadcastLogger().Warn(context.Background(), "Failed to send broadcast message",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}

			// Record success
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("broadcast", peer.String()).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BroadcastForwardWG))
	}

	// Wait for all sends to complete
	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("failed to broadcast message to any peers")
	}

	broadcastLogger().Info(context.Background(), "Broadcast complete",
		ion.String("msg_id", msg.ID),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)))

	return nil
}

// handleVoteTriggerBroadcast processes vote trigger broadcast messages
func handleVoteTriggerBroadcast(msg BroadcastMessageStruct) {
	broadcastLogger().Info(context.Background(), "Processing vote trigger broadcast",
		ion.String("msg_id", msg.ID),
		ion.String("sender", msg.Sender),
		ion.String("type", msg.Type))

	// Parse the consensus message data
	var consensusMessage PubSubMessages.ConsensusMessage
	if err := json.Unmarshal([]byte(msg.Data), &consensusMessage); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to unmarshal consensus message from vote trigger", err,
			ion.String("msg_id", msg.ID))
		return
	}

	// Check if ForListner is initialized
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		broadcastLogger().Error(context.Background(), "ForListner not initialized - cannot submit vote",
			errors.New("ForListner is nil"),
			ion.String("msg_id", msg.ID))
		return
	}

	broadcastLogger().Debug(context.Background(), "ForListner initialized",
		ion.String("node_id", listenerNode.PeerID.String()))

	// IMPORTANT: Populate buddy nodes list from consensus message
	// Extract ONLY the final connected buddy nodes (MaxMainPeers) from consensus message's Buddies map
	// These are the buddy nodes selected by sequencer (main candidates + backup fill-ins if needed)
	buddyPeerIDs := make([]peer.ID, 0, config.MaxMainPeers)
	buddiesMap := consensusMessage.GetBuddies()
	if len(buddiesMap) > 0 {
		// Buddies is a map[int]Buddy_PeerMultiaddr
		// The consensus message contains exactly MaxMainPeers buddies (the final connected ones)
		// Extract them in order (by map key index 0, 1, 2, ... MaxMainPeers-1)
		for i := 0; i < config.MaxMainPeers && i < len(buddiesMap); i++ {
			if buddy, exists := buddiesMap[i]; exists {
				// Check if PeerID is valid (not empty)
				if buddy.PeerID != "" && len(buddy.PeerID.String()) > 0 {
					buddyPeerIDs = append(buddyPeerIDs, buddy.PeerID)
				}
			}
		}
	}

	// Populate listener node's buddy list with the final connected buddy nodes (MaxMainPeers)
	if len(buddyPeerIDs) > 0 {
		listenerNode.BuddyNodes.Buddies_Nodes = buddyPeerIDs
		broadcastLogger().Info(context.Background(), "Populated buddy nodes list from consensus message",
			ion.Int("buddy_count", len(buddyPeerIDs)),
			ion.Int("max_main_peers", config.MaxMainPeers))
	} else {
		broadcastLogger().Warn(context.Background(), "No buddy nodes found in consensus message - CRDT sync may fail",
			ion.String("msg_id", msg.ID))
	}

	// Store consensus message in global cache (needed for CRDT sync multiaddr lookup)
	consensusMessage.SetGloalVarCacheConsensusMessage()
	broadcastLogger().Debug(context.Background(), "Stored consensus message in cache for multiaddr lookup",
		ion.String("msg_id", msg.ID))

	// Create vote trigger and submit vote
	voteTrigger := Vote.NewVoteTrigger()
	voteTrigger.SetConsensusMessage(&consensusMessage)

	broadcastLogger().Debug(context.Background(), "Submitting vote",
		ion.String("msg_id", msg.ID))

	// Submit the vote (this will send Type_SubmitVote message via SubmitMessageProtocol)
	if err := voteTrigger.SubmitVote(); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to submit vote from broadcast trigger", err,
			ion.String("msg_id", msg.ID))
		return
	}

	broadcastLogger().Info(context.Background(), "Vote submitted - waiting for consensus confirmation broadcast",
		ion.String("msg_id", msg.ID),
		ion.String("block_hash", consensusMessage.GetZKBlock().BlockHash.Hex()))
}

// BroadcastVoteTrigger sends a vote trigger message to all connected peers
func BroadcastVoteTrigger(h host.Host, consensusMessage *PubSubMessages.ConsensusMessage) error {
	if consensusMessage == nil {
		return fmt.Errorf("consensus message cannot be nil")
	}

	if consensusMessage.GetZKBlock().BlockHash.String() == "" {
		return fmt.Errorf("consensus message ZKBlock block hash is empty")
	}

	broadcastLogger().Debug(context.Background(), "BroadcastVoteTrigger called",
		ion.String("block_hash", consensusMessage.GetZKBlock().BlockHash.Hex()))

	// Set the voting timer when broadcast starts
	now := time.Now().UTC()
	consensusMessage.SetStartTime(now)
	consensusMessage.SetEndTimeout(now.Add(config.ConsensusTimeout))

	broadcastLogger().Info(context.Background(), "Voting timer set - broadcast vote trigger started",
		ion.String("start_time", now.Format(time.RFC3339)),
		ion.String("end_time", now.Add(config.ConsensusTimeout).Format(time.RFC3339)),
		ion.Duration("timeout_duration", config.ConsensusTimeout))

	// Marshal the consensus message to JSON
	consensusData, err := json.Marshal(consensusMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal consensus message: %w", err)
	}

	// Create a vote trigger broadcast message
	msg := BroadcastMessageStruct{
		Sender:    h.ID().String(),
		Content:   "Vote trigger broadcast - initiate voting process",
		Timestamp: now.Unix(),
		Hops:      0,
		Type:      "vote_trigger",
		Data:      string(consensusData),
	}

	// Generate a unique ID based on content and timestamp
	msg.ID = generateMessageID(msg.Sender, msg.Content, now.Unix())
	broadcastLogger().Debug(context.Background(), "Vote trigger broadcast message prepared",
		ion.String("msg_id", msg.ID),
		ion.String("sender", msg.Sender))

	// Remember this message so we don't process it if we receive it back
	markMessageSeen(msg.ID)

	// Convert to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal vote trigger broadcast message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	// Get all connected peers
	peers := h.Network().Peers()
	if len(peers) == 0 {
		return fmt.Errorf("no connected peers to broadcast vote trigger to")
	}

	broadcastLogger().Info(context.Background(), "Starting vote trigger broadcast to peers",
		ion.String("msg_id", msg.ID),
		ion.Int("peers", len(peers)))

	// Send message to all peers
	wg, err := BroadcastLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BroadcastVoteTriggerWG)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for broadcast vote trigger", err)
		return fmt.Errorf("failed to create waitgroup for broadcast vote trigger: %w", err)
	}
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		BroadcastLocalGRO.Go(GRO.BroadcastVoteTriggerThread, func(ctx context.Context) error {
			peer := peerID
			// Open stream to peer with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
			if err != nil {
				broadcastLogger().Warn(context.Background(), "Failed to open broadcast stream for vote trigger",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			defer stream.Close()

			// Send the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				broadcastLogger().Warn(context.Background(), "Failed to send vote trigger broadcast message",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}

			// Record success
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("broadcast", peer.String()).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BroadcastVoteTriggerWG))
	}

	// Wait for all sends to complete
	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("failed to broadcast vote trigger message to any peers")
	}

	broadcastLogger().Info(context.Background(), "Vote trigger broadcast complete",
		ion.String("msg_id", msg.ID),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)))

	return nil
}

// BroadcastVoteTriggerToCommittee sends a vote trigger message only to the specified committee peers
// instead of broadcasting to all connected peers. This prevents non-committee nodes from receiving
// the trigger and submitting votes that go nowhere.
func BroadcastVoteTriggerToCommittee(h host.Host, consensusMessage *PubSubMessages.ConsensusMessage, committeePeers []peer.ID) error {
	if BroadcastLocalGRO == nil {
		var err error
		BroadcastLocalGRO, err = GROHelper.InitializeGRO(GRO.BroadcastLocal)
		if err != nil {
			log.Error().Err(err).Msg("Failed to initialize BroadcastLocalGRO")
			return err
		}
	}

	if consensusMessage == nil {
		return fmt.Errorf("consensus message cannot be nil")
	}

	if consensusMessage.GetZKBlock().BlockHash.String() == "" {
		return fmt.Errorf("consensus message ZKBlock block hash is empty")
	}

	if len(committeePeers) == 0 {
		return fmt.Errorf("no committee peers to broadcast vote trigger to")
	}

	// Set the voting timer when broadcast starts
	now := time.Now().UTC()
	consensusMessage.SetStartTime(now)
	consensusMessage.SetEndTimeout(now.Add(config.ConsensusTimeout))

	// Marshal the consensus message to JSON
	consensusData, err := json.Marshal(consensusMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal consensus message: %w", err)
	}

	// Create a vote trigger broadcast message
	msg := BroadcastMessageStruct{
		Sender:    h.ID().String(),
		Content:   "Vote trigger broadcast - initiate voting process",
		Timestamp: now.Unix(),
		Hops:      0,
		Type:      "vote_trigger",
		Data:      string(consensusData),
	}

	msg.ID = generateMessageID(msg.Sender, msg.Content, now.Unix())
	markMessageSeen(msg.ID)

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal vote trigger broadcast message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	log.Info().
		Str("msg_id", msg.ID).
		Int("committee_peers", len(committeePeers)).
		Msg("Starting targeted vote trigger broadcast to committee peers")

	wg, err := BroadcastLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BroadcastVoteTriggerWG)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create waitgroup for committee broadcast vote trigger")
		return fmt.Errorf("failed to create waitgroup for committee broadcast vote trigger: %w", err)
	}
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range committeePeers {
		BroadcastLocalGRO.Go(GRO.BroadcastVoteTriggerThread, func(ctx context.Context) error {
			peer := peerID
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
			if err != nil {
				log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to open broadcast stream for committee vote trigger")
				return err
			}
			defer stream.Close()

			_, err = stream.Write(msgBytes)
			if err != nil {
				log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to send committee vote trigger message")
				return err
			}

			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			metrics.MessagesSentCounter.WithLabelValues("broadcast", peer.String()).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BroadcastVoteTriggerWG))
	}

	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("failed to broadcast vote trigger to any committee peers")
	}

	log.Info().
		Str("msg_id", msg.ID).
		Int("success", successCount).
		Int("total", len(committeePeers)).
		Msg("Committee vote trigger broadcast complete")
	return nil
}

// BroadcastBlockToEveryNodeWithExtraData sends a block to all connected peers and attaches extra metadata.
// The extra map will be merged into msg.Data. Keys in extra override existing keys.
func BroadcastBlockToEveryNodeWithExtraData(h host.Host, block *config.ZKBlock, result bool, extra map[string]string, bls []BLS_Signer.BLSresponse) error {
	if BlockPropagationLocalGRO == nil {
		var err error
		BlockPropagationLocalGRO, err = GROHelper.InitializeGRO(GRO.BlockPropagationLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize BlockPropagationLocalGRO", err)
			return err
		}
	}
	broadcastLogger().Info(context.Background(), "Broadcasting block to all nodes (with extra data)",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("block_number", block.BlockNumber),
		ion.Bool("process_result", result))

	peers := h.Network().Peers()
	if len(peers) == 0 {
		broadcastLogger().Warn(context.Background(), "No connected peers to broadcast block to — skipping broadcast, caller handles local processing")
		return nil
	}

	nonceBytes := make([]byte, 16)
	for i := range nonceBytes {
		nonceBytes[i] = byte(time.Now().UTC().UnixNano() & 0xff)
		time.Sleep(1 * time.Nanosecond)
	}
	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	now := time.Now().UTC().Unix()
	data := map[string]string{
		"block_hash":   block.BlockHash.Hex(),
		"block_number": fmt.Sprintf("%d", block.BlockNumber),
		"txn_count":    fmt.Sprintf("%d", len(block.Transactions)),
		"proof_hash":   block.ProofHash,
		"status":       block.Status,
		"timestamp":    fmt.Sprintf("%d", block.Timestamp),
	}
	// Merge extras
	for k, v := range extra {
		data[k] = v
	}

	// Attach BLS results (typed) as JSON string under data["bls_results"]
	if len(bls) > 0 {
		if enc, err := json.Marshal(bls); err == nil {
			data["bls_results"] = string(enc)
		}
	}

	msg := config.BlockMessage{
		Sender:    h.ID().String(),
		Timestamp: now,
		Nonce:     nonce,
		Block:     block,
		Type:      "zkblock",
		Hops:      0,
		Data:      data,
	}

	msg.ID = generateBlockMessageID(msg.Sender, nonce, now)
	markMessageProcessed(getMessageIDForBloomFilter(msg))

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal block message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	wg, err := BlockPropagationLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BlockPropagationForwardWG)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for block propagation forwarding", err)
		return fmt.Errorf("failed to create waitgroup for block propagation forwarding: %w", err)
	}

	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		peer := peerID
		BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
			ctxWithTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			stream, err := h.NewStream(ctxWithTimeout, peer, config.BlockPropagationProtocol)
			if err != nil {
				broadcastLogger().Debug(context.Background(), "Failed to open stream",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			defer stream.Close()
			if _, err := stream.Write(msgBytes); err != nil {
				broadcastLogger().Debug(context.Background(), "Failed to write message",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			successMutex.Lock()
			successCount++
			successMutex.Unlock()
			metrics.MessagesSentCounter.WithLabelValues("zkblock", peer.String()).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BlockPropagationForwardWG))
	}

	wg.Wait()

	broadcastLogger().Info(context.Background(), "Block broadcast complete (with extra data)",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)))

	return nil
}

// ProcessBlockLocally processes a block locally after consensus is verified.
// Returns the list of contracts deployed in the block so the sequencer can propagate them.
// blsResults must be non-empty; consensus is verified before any state changes are made.
func ProcessBlockLocally(block *config.ZKBlock, blsResults []BLS_Signer.BLSresponse) ([]BlockProcessing.ContractDeploymentInfo, error) {
	broadcastLogger().Info(context.Background(), "Processing block locally",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("block_number", block.BlockNumber),
		ion.Int("bls_results_count", len(blsResults)))

	// Validate BLS/consensus if results are provided
	// This ensures we only process blocks that have reached consensus
	if len(blsResults) > 0 {
		validYes := 0
		validTotal := 0
		for _, r := range blsResults {
			// Verify signature for stated vote (+1 if Agree else -1)
			vote := int8(-1)
			if r.Agree {
				vote = 1
			}
			if err := BLS_Verifier.Verify(r, vote); err != nil {
				broadcastLogger().Warn(context.Background(), "BLS verification failed for buddy response",
					ion.Err(err),
					ion.String("peer", r.PeerID))
				continue
			}
			validTotal++
			if vote == 1 {
				validYes++
			}
		}

		if validTotal == 0 {
			broadcastLogger().Error(context.Background(), "No valid BLS signatures - skipping block processing (invalid consensus)",
				errors.New("no valid BLS signatures"),
				ion.String("block_hash", block.BlockHash.Hex()))
			return nil, fmt.Errorf("no valid BLS signatures for block %s", block.BlockHash.Hex())
		}

		needed := (validTotal / 2) + 1
		if validYes < needed {
			broadcastLogger().Error(context.Background(), "BLS majority not in favor (+1) - skipping block processing (consensus not reached)",
				errors.New("consensus not reached"),
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.Int("valid_yes", validYes),
				ion.Int("needed", needed),
				ion.Int("valid_total", validTotal))
			return nil, fmt.Errorf("consensus not reached for block %s: %d/%d votes in favor (needed: %d)",
				block.BlockHash.Hex(), validYes, validTotal, needed)
		}

		broadcastLogger().Info(context.Background(), "BLS majority in favor verified - consensus reached",
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int("valid_yes", validYes),
			ion.Int("needed", needed),
			ion.Int("valid_total", validTotal))
	} else {
		// BLS results are required to ensure consensus was reached
		// If no BLS results are provided, we cannot verify consensus and should not process
		broadcastLogger().Error(context.Background(), "No BLS results provided - cannot verify consensus, refusing to process block",
			errors.New("empty BLS results"),
			ion.String("block_hash", block.BlockHash.Hex()))
		return nil, fmt.Errorf("cannot process block %s without BLS results to verify consensus", block.BlockHash.Hex())
	}

	// Create DB clients for processing
	mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(context.Background())
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to get main DB connection", err)
		return nil, fmt.Errorf("failed to get main DB connection: %w", err)
	}

	accountsClient, err := DB_OPs.GetAccountConnectionandPutBack(context.Background())
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to get accounts DB connection", err)
		return nil, fmt.Errorf("failed to get accounts DB connection: %w", err)
	}
	defer func() {
		DB_OPs.PutMainDBConnection(mainDBClient)
		DB_OPs.PutAccountsConnection(accountsClient)
	}()

	// Store the block in main DB FIRST to ensure it's valid before processing transactions
	// This prevents balance updates for invalid blocks that fail to store
	if err := DB_OPs.StoreZKBlock(mainDBClient, block); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to store block in database - skipping transaction processing", err,
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Uint64("block_number", block.BlockNumber))
		return nil, fmt.Errorf("failed to store block in database: %w", err)
	}

	// Only process transactions if block storage succeeded
	// This ensures balance updates only happen for valid, stored blocks
	deployments, err := BlockProcessing.ProcessBlockTransactions(block, accountsClient, true)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Block transaction processing failed after block storage", err,
			ion.String("block_hash", block.BlockHash.Hex()))
		// Note: Block is already stored, but transactions failed
		// This is a separate issue that may need rollback handling in the future
		return nil, fmt.Errorf("failed to process block transactions: %w", err)
	}

	broadcastLogger().Info(context.Background(), "Block processed and stored successfully",
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int("tx_count", len(block.Transactions)),
		ion.Int("contract_deployments", len(deployments)))

	return deployments, nil
}
