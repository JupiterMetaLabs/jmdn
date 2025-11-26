package messaging

import (
	"bufio"
	AppContext "gossipnode/config/Context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/bits-and-blooms/bloom/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/DB_OPs"
	"gossipnode/Vote"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/helper"
	"gossipnode/messaging/BlockProcessing"
	"gossipnode/metrics"
)

// Global variables for block propagation
var (
	peerTimeouts     = make(map[string]time.Time)
	peerTimeoutMutex sync.RWMutex
	messageFilter    *bloom.BloomFilter
	immuClient       *config.PooledConnection
	immuClientOnce   sync.Once
	globalHost       host.Host // Add this line
)

const (
	BlockPropagationAppContext = "blockpropagation"
)

func init() {
	messageFilter = bloom.NewWithEstimates(10000, 0.01)
	go cleanupPeerTimeouts()
}

// Initialize the host when starting the node
func InitBlockPropagation(h host.Host) error {
	globalHost = h // Save the host reference
	fmt.Println("Block propagation system initialized")
	var initErr error
	immuClientOnce.Do(func() {
		// Block propagation system initialized - will get connections on-demand
		fmt.Println("Block propagation system initialized - connections will be obtained on-demand")
		log.Info().Msg("Block propagation system initialized")
	})
	return initErr
}

// generateBlockMessageID creates a unique ID for a block message
func generateBlockMessageID(sender, nonce string, timestamp int64) string {
	hasher := sha256.New()
	hasher.Write([]byte(fmt.Sprintf("%s-%s-%d", sender, nonce, timestamp)))
	hash := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
	return hash[:16] // Return first 16 chars for brevity
}

// cleanupPeerTimeouts periodically removes expired peer timeouts
func cleanupPeerTimeouts() {
	for {
		time.Sleep(10 * time.Second)
		peerTimeoutMutex.Lock()
		for peerID, until := range peerTimeouts {
			if time.Now().UTC().After(until) {
				delete(peerTimeouts, peerID)
			}
		}
		peerTimeoutMutex.Unlock()
	}
}

// isPeerTimedOut checks if a peer is currently timed out
func isPeerTimedOut(peerID string) bool {
	peerTimeoutMutex.RLock()
	defer peerTimeoutMutex.RUnlock()
	timeout, exists := peerTimeouts[peerID]
	if !exists {
		return false
	}
	return time.Now().UTC().Before(timeout)
}

// timeoutPeer sets a timeout for a specific peer
func timeoutPeer(peerID string, duration time.Duration) {
	peerTimeoutMutex.Lock()
	defer peerTimeoutMutex.Unlock()

	peerTimeouts[peerID] = time.Now().UTC().Add(duration)
	log.Info().
		Str("peer", peerID).
		Dur("duration", duration).
		Msg("Peer timed out for sending duplicate block")
}

// isMessageProcessed checks if this message has already been processed
func isMessageProcessed(messageID string) bool {
	return messageFilter.Test([]byte(messageID))
}

// markMessageProcessed marks a message as processed
func markMessageProcessed(messageID string) {
	messageFilter.Add([]byte(messageID))
}

// storeMessageInImmuDB stores a message in ImmuDB using the appropriate key
func storeMessageInImmuDB(msg config.BlockMessage) error {
	// Determine the key - focus on ZK blocks
	var key string
	if msg.Type == "zkblock" && msg.Block != nil {
		key = fmt.Sprintf("zkblock:%s", msg.Block.BlockHash.Hex())
	} else if msg.Type == "transaction" && msg.Data != nil && msg.Data["transaction_hash"] != "" {
		key = fmt.Sprintf("tx:%s", msg.Data["transaction_hash"])
	} else {
		key = fmt.Sprintf("crdt:nonce:%s", msg.Nonce)
	}

	// Store the message
	if err := DB_OPs.Create(nil, key, msg); err != nil {
		log.Error().Err(err).Str("key", key).Msg("Failed to store message in ImmuDB")
		return err
	}

	// Update message set
	if err := updateMessageSet(key); err != nil {
		log.Error().Err(err).Str("key", key).Msg("Failed to update message set")
		return err
	}

	log.Debug().Str("key", key).Str("type", msg.Type).Msg("Message stored in ImmuDB")
	return nil
}

// updateMessageSet adds a message key to the grow-only set in ImmuDB
func updateMessageSet(key string) error {

	const setKey = "crdt:message_set"

	var messageSet map[string]bool
	err := DB_OPs.ReadJSON(setKey, &messageSet)
	if err != nil {
		messageSet = make(map[string]bool)
	}

	messageSet[key] = true
	return DB_OPs.Create(nil, setKey, messageSet)
}

// getMessageIDForBloomFilter gets the appropriate ID to use for duplication checking
func getMessageIDForBloomFilter(msg config.BlockMessage) string {
	// Special handling for ZK blocks to use hash for deduplication
	if msg.Type == "zkblock" && msg.Block != nil {
		return fmt.Sprintf("zkblock:%s", msg.Block.BlockHash.Hex())
	}

	if msg.Type == "transaction" && msg.Data != nil && msg.Data["transaction_hash"] != "" {
		return msg.Data["transaction_hash"]
	}

	return msg.Nonce
}

// HandleBlockStream processes incoming block propagation messages
// Priority: FORWARD FIRST, then PROCESS/VALIDATE before STORING
func HandleBlockStream(stream network.Stream) {
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer().String()
	if isPeerTimedOut(remotePeer) {
		log.Debug().Str("peer", remotePeer).Msg("Ignoring message from timed-out peer")
		return
	}

	metrics.MessagesReceivedCounter.WithLabelValues("block", remotePeer).Inc()

	// Read the message
	reader := bufio.NewReader(stream)
	messageBytes, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		log.Error().Err(err).Msg("Failed to read message bytes")
		return
	}

	// Parse the message
	var msg config.BlockMessage
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal block message")
		return
	}

	// Check for duplicates
	messageID := getMessageIDForBloomFilter(msg)
	if isMessageProcessed(messageID) {
		log.Debug().Str("message_id", messageID).Msg("Duplicate message received")
		timeoutPeer(remotePeer, 20*time.Second)
		return
	}

	// Mark as processed to prevent duplicate processing
	markMessageProcessed(messageID)

	// For ZK blocks, prioritize forwarding over processing
	if msg.Type == "zkblock" && msg.Block != nil {
		log.Info().
			Str("block_hash", msg.Block.BlockHash.Hex()).
			Uint64("block_number", msg.Block.BlockNumber).
			Int("txn_count", len(msg.Block.Transactions)).
			Msg("Received ZK block from peer")

		// STEP 1: FORWARD BLOCK FIRST - increment hops and forward to other peers
		if msg.Hops < config.MaxHops {
			msg.Hops++
			if globalHost != nil {
				log.Info().
					Str("block_hash", msg.Block.BlockHash.Hex()).
					Uint64("block_number", msg.Block.BlockNumber).
					Int("hops", msg.Hops).
					Msg("Forwarding ZK block to peers")

				// Don't wait for forwarding to complete
				go forwardBlock(globalHost, msg)
			} else {
				log.Error().Msg("Cannot forward block: global host not initialized")
			}
		}

		// STEP 2: PROCESS AND VALIDATE BLOCK AFTERWARD
		go func() {
			// Verify buddy BLS signatures if provided; require majority to continue
			if blsJSON, ok := msg.Data["bls_results"]; ok && len(blsJSON) > 0 {
				var blsResponses []BLS_Signer.BLSresponse
				if err := json.Unmarshal([]byte(blsJSON), &blsResponses); err != nil {
					log.Error().Err(err).Msg("Failed to unmarshal bls_results; skipping verification")
				} else if len(blsResponses) > 0 {
					// Count how many verified signatures explicitly favor (+1)
					validYes := 0
					validTotal := 0
					for _, r := range blsResponses {
						// verify signature for stated vote (+1 if Agree else -1)
						vote := int8(-1)
						if r.Agree {
							vote = 1
						}
						if err := BLS_Verifier.Verify(r, vote); err != nil {
							log.Warn().Err(err).Str("peer", r.PeerID).Msg("BLS verification failed for buddy response")
							continue
						}
						validTotal++
						if vote == 1 {
							validYes++
						}
					}
					if validTotal == 0 {
						log.Error().Msg("No valid BLS signatures - skipping block processing (irrelevant block)")
						return
					}
					needed := (validTotal / 2) + 1
					if validYes < needed {
						log.Error().
							Int("valid_yes", validYes).
							Int("needed", needed).
							Int("valid_total", validTotal).
							Msg("BLS majority not in favor (+1) - skipping block processing (irrelevant block)")
						return
					}
					log.Info().
						Int("valid_yes", validYes).
						Int("needed", needed).
						Int("valid_total", validTotal).
						Msg("BLS majority in favor verified - continuing block processing")
				}
			}

			ctx, cancel := AppContext.GetAppContext(BlockPropagationAppContext).NewChildContext()
			defer cancel()
			// Create DB clients for processing
			mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
			if err != nil {
				log.Error().Err(err).Msg("Failed to create main DB client")
				return
			}

			accountsClient, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
			if err != nil {
				log.Error().Err(err).Msg("Failed to create accounts DB client")
				return
			}
			defer func() {
				DB_OPs.PutMainDBConnection(mainDBClient)
				DB_OPs.PutAccountsConnection(accountsClient)
			}()

			log.Info().
				Str("block_hash", msg.Block.BlockHash.Hex()).
				Uint64("block_number", msg.Block.BlockNumber).
				Msg("Processing block transactions")

			// Process all transactions in the block atomically with rollback capability
			if err := BlockProcessing.ProcessBlockTransactions(msg.Block, accountsClient); err != nil {
				log.Error().
					Err(err).
					Str("block_hash", msg.Block.BlockHash.Hex()).
					Msg("Block processing failed - not storing block")
				return
			}

			log.Info().
				Str("block_hash", msg.Block.BlockHash.Hex()).
				Msg("All transactions processed successfully - storing block")

			// Store the validated and processed block in main DB
			if err := DB_OPs.StoreZKBlock(mainDBClient, msg.Block); err != nil {
				log.Error().
					Err(err).
					Str("block_hash", msg.Block.BlockHash.Hex()).
					Msg("Failed to store block in database")
				return
			}

			// Store block message metadata
			if err := storeMessageInImmuDB(msg); err != nil { // msg is a copy, but it's fine
				log.Error().Err(err).Msg("Failed to store block message in ImmuDB")
			}

			log.Info().
				Str("block_hash", msg.Block.BlockHash.Hex()).
				Uint64("block_number", msg.Block.BlockNumber).
				Msg("Block processed and stored successfully")
		}()

		// Print to console
		fmt.Printf("\n[ZKBLOCK from %s] Block #%d, Hash: %s, Txns: %d\n>>> ",
			msg.Sender, msg.Block.BlockNumber, msg.Block.BlockHash.Hex(),
			len(msg.Block.Transactions))
	} else {
		// Handle other message types (not our focus)
		if msg.Hops < config.MaxHops {
			msg.Hops++
			go forwardBlock(globalHost, msg)
		}
	}

	// Notify explorer or other UI components
	helper.NotifyBroadcast(msg)
}

// forwardBlock sends the block message to all connected peers
func forwardBlock(h host.Host, msg config.BlockMessage) {
	peers := h.Network().Peers()

	// Convert message to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal block message")
		return
	}
	msgBytes = append(msgBytes, '\n')

	// Track forwarding metrics
	var successCount int
	var successMutex sync.Mutex
	var wg sync.WaitGroup

	// Send to each peer concurrently
	for _, peerID := range peers {
		// Don't send back to the original sender
		if peerID.String() == msg.Sender {
			continue
		}

		wg.Add(1)
		go func(peer peer.ID) {
			defer wg.Done()

			ctx, cancel := AppContext.GetAppContext(BlockPropagationAppContext).NewChildContextWithTimeout(5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BlockPropagationProtocol)
			if err != nil {
				log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to open stream")
				return
			}
			defer stream.Close()

			if _, err := stream.Write(msgBytes); err != nil {
				log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to write message")
				return
			}

			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			metrics.MessagesSentCounter.WithLabelValues(msg.Type, peer.String()).Inc()
		}(peerID)
	}

	wg.Wait()

	log.Info().
		Str("type", msg.Type).
		Int("success", successCount).
		Int("total", len(peers)-1).
		Msg("Block forwarded to peers")
}

// This function is called by Server.go when receiving a new block via API
func PropagateZKBlock(h host.Host, block *PubSubMessages.ConsensusMessage) error {
	log.Info().
		Str("block_hash", block.GetZKBlock().BlockHash.Hex()).
		Uint64("block_number", block.GetZKBlock().BlockNumber).
		Int("txn_count", len(block.GetZKBlock().Transactions)).
		Msg("Starting ZK block propagation and voting process")

	// Step 1: Create consensus message and store in cache (non-blocking)
	consensusMessage := createConsensusMessageForVoting(block)
	if consensusMessage == nil {
		return fmt.Errorf("failed to create consensus message for voting")
	}

	// Step 2: Store in global cache using consensus builder
	consensusMessage.SetGloalVarCacheConsensusMessage()

	// Step 3: Submit to voting process asynchronously (don't wait for response)
	go func() {
		if notTimeout, err := submitZKBlockToVoting(consensusMessage); notTimeout {
			log.Info().
				Str("block_hash", block.GetZKBlock().BlockHash.Hex()).
				Msg("ZK block submitted to voting process successfully")
			return
		} else {
			log.Error().
				Str("error", err.Error()).
				Str("block_hash", block.GetZKBlock().BlockHash.Hex()).
				Msg("ZK block timed out, not submitting to voting process")
			return
		}
	}()

	// Step 3.5: Start consensus monitoring asynchronously (only if not timed out)
	go func() {
		// Check if consensus message is still valid before monitoring
		if consensusMessage.CheckTimeOut() {
			log.Info().
				Str("block_hash", block.GetZKBlock().BlockHash.Hex()).
				Msg("Consensus already timed out - skipping monitoring")
			return
		}

		// Wait for consensus result with extended timeout
		consensusTimeout := config.ConsensusTimeout + 10*time.Second // Add buffer
		if err := WaitForConsensusResult(block.GetZKBlock().BlockHash.Hex(), consensusTimeout); err != nil {
			log.Info().
				Err(err).
				Str("block_hash", block.GetZKBlock().BlockHash.Hex()).
				Msg("Consensus monitoring completed (timeout expected)")
		}
	}()

	// Step 4: Continue with block propagation (don't wait for consensus)
	log.Info().
		Str("block_hash", block.GetZKBlock().BlockHash.Hex()).
		Msg("ZK block submitted to voting, continuing with propagation")

	// Step 4: Generate a unique nonce for the block message
	nonceBytes := make([]byte, 16)
	for i := range nonceBytes {
		nonceBytes[i] = byte(time.Now().UTC().UnixNano() & 0xff)
		time.Sleep(1 * time.Nanosecond)
	}
	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	// Step 5: Create summary metadata for logs and Bloom filter
	metadata := map[string]string{
		"block_hash":   block.GetZKBlock().BlockHash.Hex(),
		"block_number": strconv.FormatUint(block.GetZKBlock().BlockNumber, 10),
		"txn_count":    strconv.Itoa(len(block.GetZKBlock().Transactions)),
		"proof_hash":   block.GetZKBlock().ProofHash,
		"status":       block.GetZKBlock().Status,
		"timestamp":    strconv.FormatInt(block.GetZKBlock().Timestamp, 10),
	}

	// Add sample transaction hashes
	txLimit := min(5, len(block.GetZKBlock().Transactions))
	for i := 0; i < txLimit; i++ {
		metadata[fmt.Sprintf("tx_%d", i)] = block.GetZKBlock().Transactions[i].Hash.Hex()
	}

	// Step 6: Create block message with full ZK block data
	now := time.Now().UTC().Unix()
	msg := config.BlockMessage{
		Sender:    h.ID().String(),
		Timestamp: now,
		Nonce:     nonce,
		Data:      metadata,
		Block:     block.GetZKBlock(), // Include full block
		Type:      "zkblock",
		Hops:      0,
	}

	// Generate message ID
	msg.ID = generateBlockMessageID(msg.Sender, nonce, now)

	// Mark as processed by us to avoid processing our own message
	markMessageProcessed(getMessageIDForBloomFilter(msg))

	// Store block message metadata
	if err := storeMessageInImmuDB(msg); err != nil {
		log.Error().Err(err).Msg("Failed to store block message in ImmuDB")
		// Continue even if metadata storage fails
	}

	// Step 7: Propagate to peers
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal block message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	// Get connected peers
	peers := h.Network().Peers()
	if len(peers) == 0 {
		log.Warn().Msg("No connected peers to propagate ZK block to")
		return nil // Not an error, just no peers to propagate to
	}

	// Send to all peers concurrently
	var wg sync.WaitGroup
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		wg.Add(1)
		go func(peer peer.ID) {
			defer wg.Done()

			ctx, cancel := AppContext.GetAppContext(BlockPropagationAppContext).NewChildContextWithTimeout(5*time.Second)
			defer cancel()

			// Use the correct protocol ID from constants
			stream, err := h.NewStream(ctx, peer, config.BlockPropagationProtocol)
			if err != nil {
				log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to open stream")
				return
			}
			defer stream.Close()

			if _, err := stream.Write(msgBytes); err != nil {
				log.Debug().Err(err).Str("peer", peer.String()).Msg("Failed to write message")
				return
			}

			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			metrics.MessagesSentCounter.WithLabelValues("zkblock", peer.String()).Inc()
		}(peerID)
	}

	wg.Wait()

	log.Info().
		Str("block_hash", block.GetZKBlock().BlockHash.Hex()).
		Int("success", successCount).
		Int("total", len(peers)).
		Msg("ZK block propagated successfully")

	return nil
}

// GetMessage retrieves a message by key
func GetMessage(key string) (*config.BlockMessage, error) {
	var message config.BlockMessage
	if err := DB_OPs.ReadJSON(key, &message); err != nil {
		return nil, fmt.Errorf("failed to read message data: %w", err)
	}
	return &message, nil
}

// GetZKBlockByHash retrieves a ZK block by its hash
func GetZKBlockByHash(blockHash string) (*config.ZKBlock, error) {
	key := fmt.Sprintf("zkblock:%s", blockHash)

	msg, err := GetMessage(key)
	if err != nil {
		return nil, fmt.Errorf("block not found: %w", err)
	}

	if msg.Block == nil {
		return nil, fmt.Errorf("message found but does not contain a ZK block")
	}

	return msg.Block, nil
}

// Helper function for min value
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// createConsensusMessageForVoting creates a consensus message for voting process
func createConsensusMessageForVoting(MSG *PubSubMessages.ConsensusMessage) *PubSubMessages.ConsensusMessage {
	// Create a new consensus message
	consensusMessage := PubSubMessages.NewConsensusMessageBuilder(MSG)

	// Set the ZK block
	consensusMessage.SetZKBlock(MSG.GetZKBlock())

	// Set timing information
	now := time.Now().UTC()
	consensusMessage.SetStartTime(now)
	consensusMessage.SetEndTimeout(now.Add(config.ConsensusTimeout))

	fmt.Printf("Consensus message: %+v\n", consensusMessage)
	// Note: Buddies will be added by the consensus process
	// This is just the initial setup for voting

	return consensusMessage
}

// submitZKBlockToVoting submits the ZK block to voting process
// If we have crossed the end timeout, we will not submit the vote
func submitZKBlockToVoting(consensusMessage *PubSubMessages.ConsensusMessage) (bool, error) {
	if consensusMessage.CheckTimeOut() {
		// Timeout occurred - this is normal and expected
		return false, fmt.Errorf("consensus message timed out - skipping vote")
	}

	// Create vote trigger
	voteTrigger := Vote.NewVoteTrigger()

	// Set the consensus message
	voteTrigger.SetConsensusMessage(consensusMessage)

	// Submit the vote (this will trigger the voting process)
	if err := voteTrigger.SubmitVote(); err != nil {
		return false, fmt.Errorf("failed to submit vote: %w", err)
	}

	return true, nil
}

// WaitForConsensusResult waits for consensus result and handles final storage
func WaitForConsensusResult(blockHash string, timeout time.Duration) error {
	// Create a channel to receive consensus result
	resultChan := make(chan error, 1)

	// Start goroutine to monitor consensus
	go func() {
		startTime := time.Now().UTC()
		for time.Since(startTime) < timeout {
			// Check if consensus is complete
			consensusMessage := PubSubMessages.NewConsensusMessageBuilder(nil)
			consensusMessage.SetZKBlock(&config.ZKBlock{BlockHash: common.HexToHash(blockHash)})
			cachedMessage := consensusMessage.GetGloalVarCacheConsensusMessage()

			if cachedMessage != nil {
				// Check if consensus timeout has passed
				if time.Now().UTC().After(cachedMessage.GetEndTimeout()) {
					// Consensus window closed, check results
					resultChan <- handleConsensusResult(cachedMessage)
					return
				}
			}

			// Wait a bit before checking again
			time.Sleep(1 * time.Second)
		}

		// Timeout reached - this is expected behavior
		resultChan <- fmt.Errorf("consensus timeout reached for block %s (expected)", blockHash)
	}()

	// Wait for result or timeout
	select {
	case result := <-resultChan:
		return result
	case <-time.After(timeout):
		return fmt.Errorf("consensus monitoring timeout for block %s", blockHash)
	}
}

// handleConsensusResult processes the final consensus result
func handleConsensusResult(consensusMessage *PubSubMessages.ConsensusMessage) error {
	// Check if consensus timed out
	if consensusMessage.CheckTimeOut() {
		log.Info().
			Str("block_hash", consensusMessage.GetZKBlock().BlockHash.Hex()).
			Time("end_timeout", consensusMessage.GetEndTimeout()).
			Msg("Consensus timed out - cleaning up cache")

		// Remove from cache
		consensusMessage.RemoveGloalVarCacheConsensusMessage()
		return nil
	}

	// TODO: Implement consensus result processing for successful consensus
	// This is where you would implement the consensus result processing
	// For now, we'll just log the result and clean up the cache

	log.Info().
		Str("block_hash", consensusMessage.GetZKBlock().BlockHash.Hex()).
		Time("end_timeout", consensusMessage.GetEndTimeout()).
		Msg("Processing consensus result")

	// Remove from cache
	consensusMessage.RemoveGloalVarCacheConsensusMessage()

	// Here you would typically:
	// 1. Check vote results
	// 2. Store in database if consensus reached
	// 3. Handle rejection if consensus failed

	return nil
}
