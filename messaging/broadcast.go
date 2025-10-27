package messaging

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"gossipnode/Vote"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/metrics"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
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

// cleanupOldMessages periodically removes expired message IDs
func cleanupOldMessages() {
	for {
		time.Sleep(1 * time.Minute)

		seenMessagesMu.Lock()
		now := time.Now()
		for id, timestamp := range seenMessages {
			if now.Sub(timestamp) > config.MessageExpiryTime {
				delete(seenMessages, id)
			}
		}
		seenMessagesMu.Unlock()
	}
}

// Start message cleanup in background
func init() {
	go cleanupOldMessages()
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
	seenMessages[msgID] = time.Now()
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
			log.Error().Err(err).Str("peer", stream.Conn().RemotePeer().String()).
				Msg("Error reading broadcast message")
		}
		return
	}

	// Parse the message
	var msg BroadcastMessageStruct
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal broadcast message")
		return
	}

	// Check if we've already seen this message
	if isMessageSeen(msg.ID) {
		// We've already processed this message, ignore
		return
	}

	// Mark as seen to avoid reprocessing
	markMessageSeen(msg.ID)

	// Print the received broadcast
	fmt.Printf("\n[BROADCAST from %s] %s\n>>> ", msg.Sender, msg.Content)

	// Handle different message types
	if msg.Type == "vote_trigger" {
		handleVoteTriggerBroadcast(msg)
	}

	// Only rebroadcast if we haven't reached max hops
	if msg.Hops < config.MaxHops {
		// Forward to our peers
		msg.Hops++
		localPeer := stream.Conn().LocalPeer().String()
		log.Info().
			Str("msg_id", msg.ID).
			Str("origin", msg.Sender).
			Str("via", localPeer).
			Int("hops", msg.Hops).
			Msg("Rebroadcasting message")

		// Instead of trying to get the host from the connection,
		// get it from the stored node instance
		// We'll need to access the global node instance
		if hostInstance := getHostInstance(); hostInstance != nil {
			forwardBroadcast(hostInstance, msg)
		} else {
			log.Error().Msg("Cannot access host instance for forwarding broadcast")
		}
	} else {
		log.Info().
			Str("msg_id", msg.ID).
			Int("hops", msg.Hops).
			Msg("Max hops reached, not rebroadcasting")
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
		log.Error().Err(err).Msg("Failed to marshal broadcast message")
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
			log.Error().Err(err).Str("peer", peerID.String()).Msg("Failed to open broadcast stream")
			continue
		}

		// Write the message
		_, err = stream.Write(msgBytes)
		if err != nil {
			log.Error().Err(err).Str("peer", peerID.String()).Msg("Failed to write broadcast message")
			stream.Close()
			continue
		}

		// Close the stream
		stream.Close()
		successCount++

		// Record metrics
		metrics.MessagesSentCounter.WithLabelValues("broadcast", peerID.String()).Inc()
	}

	log.Info().
		Str("msg_id", msg.ID).
		Int("peers", successCount).
		Msg("Broadcast forwarded to peers")
}

// BroadcastMessage sends a message to all connected peers
func BroadcastMessage(h host.Host, content string) error {
	// Create a new broadcast message
	now := time.Now().Unix()
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

	log.Info().
		Str("msg_id", msg.ID).
		Int("peers", len(peers)).
		Msg("Starting broadcast to peers")

	// Send message to all peers
	var wg sync.WaitGroup
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		wg.Add(1)
		go func(peer peer.ID) {
			defer wg.Done()

			// Open stream to peer with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
			if err != nil {
				log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to open broadcast stream")
				return
			}
			defer stream.Close()

			// Send the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to send broadcast message")
				return
			}

			// Record success
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("broadcast", peer.String()).Inc()
		}(peerID)
	}

	// Wait for all sends to complete
	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("failed to broadcast message to any peers")
	}

	log.Info().
		Str("msg_id", msg.ID).
		Int("success", successCount).
		Int("total", len(peers)).
		Msg("Broadcast complete")

	return nil
}

// handleVoteTriggerBroadcast processes vote trigger broadcast messages
func handleVoteTriggerBroadcast(msg BroadcastMessageStruct) {
	log.Info().
		Str("msg_id", msg.ID).
		Str("sender", msg.Sender).
		Str("type", msg.Type).
		Msg("Processing vote trigger broadcast")

	// Parse the consensus message data
	var consensusMessage PubSubMessages.ConsensusMessage
	if err := json.Unmarshal([]byte(msg.Data), &consensusMessage); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal consensus message from vote trigger")
		return
	}

	// Create vote trigger and submit vote
	voteTrigger := Vote.NewVoteTrigger()
	voteTrigger.SetConsensusMessage(&consensusMessage)

	// Submit the vote (this will send Type_SubmitVote message via SubmitMessageProtocol)
	if err := voteTrigger.SubmitVote(); err != nil {
		log.Error().Err(err).Msg("Failed to submit vote from broadcast trigger")
		return
	}

	log.Info().
		Str("msg_id", msg.ID).
		Msg("Successfully processed vote trigger broadcast")
}

// BroadcastVoteTrigger sends a vote trigger message to all connected peers
func BroadcastVoteTrigger(h host.Host, consensusMessage *PubSubMessages.ConsensusMessage) error {
	if consensusMessage == nil {
		return fmt.Errorf("consensus message cannot be nil")
	}

	fmt.Printf("Consensus message: %+v\n", consensusMessage)

	// Set the voting timer when broadcast starts
	now := time.Now()
	consensusMessage.SetStartTime(now)
	consensusMessage.SetEndTimeout(now.Add(config.ConsensusTimeout))

	log.Info().
		Str("start_time", now.Format(time.RFC3339)).
		Str("end_time", now.Add(config.ConsensusTimeout).Format(time.RFC3339)).
		Dur("timeout_duration", config.ConsensusTimeout).
		Msg("Voting timer set - broadcast vote trigger started")

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
	fmt.Printf("Vote trigger broadcast message: %+v\n", msg)
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

	log.Info().
		Str("msg_id", msg.ID).
		Int("peers", len(peers)).
		Msg("Starting vote trigger broadcast to peers")

	// Send message to all peers
	var wg sync.WaitGroup
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		wg.Add(1)
		go func(peer peer.ID) {
			defer wg.Done()

			// Open stream to peer with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
			if err != nil {
				log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to open broadcast stream for vote trigger")
				return
			}
			defer stream.Close()

			// Send the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				log.Error().Err(err).Str("peer", peer.String()).Msg("Failed to send vote trigger broadcast message")
				return
			}

			// Record success
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("broadcast", peer.String()).Inc()
		}(peerID)
	}

	// Wait for all sends to complete
	wg.Wait()

	if successCount == 0 {
		fmt.Printf("Failed to broadcast vote trigger message to any peers\n")
		return fmt.Errorf("failed to broadcast vote trigger message to any peers")
	}

	log.Info().
		Str("msg_id", msg.ID).
		Int("success", successCount).
		Int("total", len(peers)).
		Msg("Vote trigger broadcast complete")
	fmt.Printf("Vote trigger broadcast complete\n")
	return nil
}

// func BroadcastBlockToEveryNode(h host.Host, block *config.ZKBlock, BuddyNodes PubSubMessages.Buddies) error {

// }
