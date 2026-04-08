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

	"gossipnode/config/GRO"
	GROHelper "gossipnode/messaging/common"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/rs/zerolog/log"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/helper"
	"gossipnode/messaging/BlockProcessing"
	"gossipnode/metrics"
)

// Global variables for block propagation
var (
	peerTimeouts     = make(map[string]time.Time)
	peerTimeoutMutex sync.RWMutex
	messageFilter    *bloom.BloomFilter
	// immuClient       *config.PooledConnection // unused: declared but never assigned or read
	immuClientOnce sync.Once
	globalHost     host.Host // Add this line
)

// StartBlockPropagationCleanup initializes the GRO and starts the cleanup thread.
func StartBlockPropagationCleanup() {
	if BlockPropagationLocalGRO == nil {
		var err error
		BlockPropagationLocalGRO, err = GROHelper.InitializeGRO(GRO.BlockPropagationLocal)
		if err != nil {
			log.Error().Err(err).Msg("Failed to initialize BlockPropagationLocalGRO")
			return
		}
	}
	if messageFilter == nil {
		messageFilter = bloom.NewWithEstimates(10000, 0.01)
	}
	BlockPropagationLocalGRO.Go(GRO.BlockPropagationPeersCleanupThread, func(ctx context.Context) error {
		cleanupPeerTimeouts(ctx)
		return nil
	})
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

// cleanupPeerTimeouts periodically removes expired peer timeouts.
// It stops when ctx is cancelled.
func cleanupPeerTimeouts(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			peerTimeoutMutex.Lock()
			now := time.Now().UTC()
			for peerID, until := range peerTimeouts {
				if now.After(until) {
					delete(peerTimeouts, peerID)
				}
			}
			peerTimeoutMutex.Unlock()
		}
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

// [UNUSED]
// HandleBlockStream processes incoming block propagation messages
// Priority: FORWARD FIRST, then PROCESS/VALIDATE before STORING
func HandleBlockStream(stream network.Stream) {
	if BlockPropagationLocalGRO == nil {
		var err error
		BlockPropagationLocalGRO, err = GROHelper.InitializeGRO(GRO.BlockPropagationLocal)
		if err != nil {
			log.Error().Err(err).Msg("Failed to initialize BlockPropagationLocalGRO")
			return
		}
	}
	defer func() {
		if err := stream.Close(); err != nil {
			log.Error().Err(err).Msg("Failed to close block stream")
		}
	}()

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
				BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
					forwardBlock(globalHost, msg)
					return nil
				})
			} else {
				log.Error().Msg("Cannot forward block: global host not initialized")
			}
		}

		// STEP 2: PROCESS AND VALIDATE BLOCK AFTERWARD
		BlockPropagationLocalGRO.Go(GRO.BlockPropagationProcessAndValidateThread, func(ctx context.Context) error {
			// Check if block is explicitly rejected
			if status, ok := msg.Data["status"]; ok && status == "rejected" {
				log.Info().
					Str("block_hash", msg.Block.BlockHash.Hex()).
					Msg("Received consensus REJECTION for block - discarding")
				return nil
			}

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
						return fmt.Errorf("no valid BLS signatures - skipping block processing (irrelevant block)")
					}
					needed := (validTotal / 2) + 1
					if validYes < needed {
						log.Error().
							Int("valid_yes", validYes).
							Int("needed", needed).
							Int("valid_total", validTotal).
							Msg("BLS majority not in favor (+1) - skipping block processing (irrelevant block)")
						return fmt.Errorf("BLS majority not in favor (+1) - skipping block processing (irrelevant block)")
					}
					log.Info().
						Int("valid_yes", validYes).
						Int("needed", needed).
						Int("valid_total", validTotal).
						Msg("BLS majority in favor verified - continuing block processing")
				}
			}

			// Create DB clients for processing
			mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
			if err != nil {
				log.Error().Err(err).Msg("Failed to create main DB client")
				return fmt.Errorf("failed to create main DB client: %w", err)
			}

			accountsClient, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
			if err != nil {
				log.Error().Err(err).Msg("Failed to create accounts DB client")
				return fmt.Errorf("failed to create accounts DB client: %w", err)
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
			if err := BlockProcessing.ProcessBlockTransactions(ctx, msg.Block, accountsClient); err != nil {
				log.Error().
					Err(err).
					Str("block_hash", msg.Block.BlockHash.Hex()).
					Msg("Block processing failed - not storing block")
				return fmt.Errorf("block processing failed - not storing block: %w", err)
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
				return fmt.Errorf("failed to store block in database: %w", err)
			}

			// Store block message metadata
			if err := storeMessageInImmuDB(msg); err != nil { // msg is a copy, but it's fine
				log.Error().Err(err).Msg("Failed to store block message in ImmuDB")
			}

			log.Info().
				Str("block_hash", msg.Block.BlockHash.Hex()).
				Uint64("block_number", msg.Block.BlockNumber).
				Msg("Block processed and stored successfully")
			return nil
		})

		// Print to console
		fmt.Printf("\n[ZKBLOCK from %s] Block #%d, Hash: %s, Txns: %d\n>>> ",
			msg.Sender, msg.Block.BlockNumber, msg.Block.BlockHash.Hex(),
			len(msg.Block.Transactions))
	} else {
		// Handle other message types (not our focus)
		if msg.Hops < config.MaxHops {
			msg.Hops++
			BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
				forwardBlock(globalHost, msg)
				return nil
			})
		}
	}

	// Notify explorer or other UI components
	helper.NotifyBroadcast(msg)
}

// forwardBlock sends the block message to all connected peers
func forwardBlock(h host.Host, msg config.BlockMessage) {
	if BlockPropagationLocalGRO == nil {
		var err error
		BlockPropagationLocalGRO, err = GROHelper.InitializeGRO(GRO.BlockPropagationLocal)
		if err != nil {
			log.Error().Err(err).Msg("Failed to initialize BlockPropagationLocalGRO")
			return
		}
	}
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
	wg, err := BlockPropagationLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BlockPropagationForwardWG)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create waitgroup for block forwarding")
		return
	}

	// Send to each peer concurrently
	for _, peerID := range peers {
		// Don't send back to the original sender
		if peerID.String() == msg.Sender {
			continue
		}

		peerIDForGoroutine := peerID // Capture peerID in closure to avoid race condition

		if err := BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
			stream, err := h.NewStream(ctx, peerIDForGoroutine, config.BlockPropagationProtocol)
			if err != nil {
				log.Debug().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to open stream")
				return err
			}
			defer func() {
				if closeErr := stream.Close(); closeErr != nil {
					log.Debug().Err(closeErr).Str("peer", peerIDForGoroutine.String()).Msg("Failed to close stream")
				}
			}()

			if _, err := stream.Write(msgBytes); err != nil {
				log.Debug().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to write message")
				return err
			}

			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			metrics.MessagesSentCounter.WithLabelValues(msg.Type, peerIDForGoroutine.String()).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BlockPropagationForwardWG)); err != nil {
			log.Error().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to start goroutine for block forwarding")
		}
	}

	wg.Wait()

	log.Info().
		Str("type", msg.Type).
		Int("success", successCount).
		Int("total", len(peers)-1).
		Msg("Block forwarded to peers")
}
