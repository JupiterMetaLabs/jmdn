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

	"gossipnode/config/GRO"
	GROHelper "gossipnode/messaging/common"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"

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
			broadcastLogger().Error(context.Background(), "Failed to initialize BlockPropagationLocalGRO", err)
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
	var initErr error
	immuClientOnce.Do(func() {
		broadcastLogger().Info(context.Background(), "Block propagation system initialized - connections will be obtained on-demand")
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
	broadcastLogger().Info(context.Background(), "Peer timed out for sending duplicate block", ion.String("peer", peerID), ion.String("duration", duration.String()))
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
		broadcastLogger().Error(context.Background(), "Failed to store message in ImmuDB", err, ion.String("key", key))
		return err
	}

	// Update message set
	if err := updateMessageSet(key); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to update message set", err, ion.String("key", key))
		return err
	}

	broadcastLogger().Debug(context.Background(), "Message stored in ImmuDB", ion.String("key", key), ion.String("type", msg.Type))
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
			broadcastLogger().Error(context.Background(), "Failed to initialize BlockPropagationLocalGRO", err)
			return
		}
	}
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer().String()
	if isPeerTimedOut(remotePeer) {
		broadcastLogger().Debug(context.Background(), "Ignoring message from timed-out peer", ion.String("peer", remotePeer))
		return
	}

	metrics.MessagesReceivedCounter.WithLabelValues("block", remotePeer).Inc()

	// Read the message
	reader := bufio.NewReader(stream)
	messageBytes, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		broadcastLogger().Error(context.Background(), "Failed to read message bytes", err)
		return
	}

	// Parse the message
	var msg config.BlockMessage
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to unmarshal block message", err)
		return
	}

	// Check for duplicates
	messageID := getMessageIDForBloomFilter(msg)
	if isMessageProcessed(messageID) {
		broadcastLogger().Debug(context.Background(), "Duplicate message received", ion.String("message_id", messageID))
		timeoutPeer(remotePeer, 20*time.Second)
		return
	}

	// Mark as processed to prevent duplicate processing
	markMessageProcessed(messageID)

	// For ZK blocks, prioritize forwarding over processing
	if msg.Type == "zkblock" && msg.Block != nil {
		broadcastLogger().Info(context.Background(), "Received ZK block from peer",
			ion.String("block_hash", msg.Block.BlockHash.Hex()),
			ion.Uint64("block_number", msg.Block.BlockNumber),
			ion.Int("txn_count", len(msg.Block.Transactions)))

		// STEP 1: FORWARD BLOCK FIRST - increment hops and forward to other peers
		if msg.Hops < config.MaxHops {
			msg.Hops++
			if globalHost != nil {
				broadcastLogger().Info(context.Background(), "Forwarding ZK block to peers",
					ion.String("block_hash", msg.Block.BlockHash.Hex()),
					ion.Uint64("block_number", msg.Block.BlockNumber),
					ion.Int("hops", msg.Hops))

				// Don't wait for forwarding to complete
				BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
					forwardBlock(globalHost, msg)
					return nil
				})
			} else {
				broadcastLogger().Error(context.Background(), "Cannot forward block: global host not initialized", errors.New("global host not initialized"))
			}
		}

		// STEP 2: PROCESS AND VALIDATE BLOCK AFTERWARD
		BlockPropagationLocalGRO.Go(GRO.BlockPropagationProcessAndValidateThread, func(ctx context.Context) error {
			// Check if block is explicitly rejected
			if status, ok := msg.Data["status"]; ok && status == "rejected" {
				broadcastLogger().Info(ctx, "Received consensus REJECTION for block - discarding",
					ion.String("block_hash", msg.Block.BlockHash.Hex()))
				return nil
			}

			// Verify buddy BLS signatures if provided; require majority to continue
			if blsJSON, ok := msg.Data["bls_results"]; ok && len(blsJSON) > 0 {
				var blsResponses []BLS_Signer.BLSresponse
				if err := json.Unmarshal([]byte(blsJSON), &blsResponses); err != nil {
					broadcastLogger().Error(context.Background(), "Failed to unmarshal bls_results; skipping verification", err)
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
							broadcastLogger().Warn(context.Background(), "BLS verification failed for buddy response", ion.Err(err), ion.String("peer", r.PeerID))
							continue
						}
						validTotal++
						if vote == 1 {
							validYes++
						}
					}
					if validTotal == 0 {
						broadcastLogger().Error(ctx, "No valid BLS signatures - skipping block processing (irrelevant block)", errors.New("no valid BLS signatures"))
						return fmt.Errorf("no valid BLS signatures - skipping block processing (irrelevant block)")
					}
					needed := (validTotal / 2) + 1
					if validYes < needed {
						broadcastLogger().Error(ctx, "BLS majority not in favor (+1) - skipping block processing (irrelevant block)",
							errors.New("BLS majority not in favor"),
							ion.Int("valid_yes", validYes),
							ion.Int("needed", needed),
							ion.Int("valid_total", validTotal))
						return fmt.Errorf("BLS majority not in favor (+1) - skipping block processing (irrelevant block)")
					}
					broadcastLogger().Info(ctx, "BLS majority in favor verified - continuing block processing",
						ion.Int("valid_yes", validYes),
						ion.Int("needed", needed),
						ion.Int("valid_total", validTotal))
				}
			}

			// Create DB clients for processing
			mainDBClient, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
			if err != nil {
				broadcastLogger().Error(ctx, "Failed to create main DB client", err)
				return fmt.Errorf("failed to create main DB client: %w", err)
			}

			accountsClient, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
			if err != nil {
				broadcastLogger().Error(ctx, "Failed to create accounts DB client", err)
				return fmt.Errorf("failed to create accounts DB client: %w", err)
			}
			defer func() {
				DB_OPs.PutMainDBConnection(mainDBClient)
				DB_OPs.PutAccountsConnection(accountsClient)
			}()

			broadcastLogger().Info(ctx, "Processing block transactions",
				ion.String("block_hash", msg.Block.BlockHash.Hex()),
				ion.Uint64("block_number", msg.Block.BlockNumber))

			// Pull-on-demand: ensure contract metadata is present before execution.
			// This handles the case where the ContractMessage gossip was missed
			// (e.g. sequencer went offline before propagation completed).
			if h := getHostInstance(); h != nil {
				PrefetchMissingContracts(ctx, h, msg.Block.Transactions)
			}

			// Process all transactions in the block atomically with rollback capability.
			// Receiver nodes discard the deployments slice — only the sequencer propagates contracts.
			if _, err := BlockProcessing.ProcessBlockTransactions(msg.Block, accountsClient, true); err != nil {
				broadcastLogger().Error(ctx, "Block processing failed - not storing block", err,
					ion.String("block_hash", msg.Block.BlockHash.Hex()))
				return fmt.Errorf("block processing failed - not storing block: %w", err)
			}

			broadcastLogger().Info(ctx, "All transactions processed successfully - storing block",
				ion.String("block_hash", msg.Block.BlockHash.Hex()))

			// Store the validated and processed block in main DB
			if err := DB_OPs.StoreZKBlock(mainDBClient, msg.Block); err != nil {
				broadcastLogger().Error(ctx, "Failed to store block in database", err,
					ion.String("block_hash", msg.Block.BlockHash.Hex()))
				return fmt.Errorf("failed to store block in database: %w", err)
			}

			// Store block message metadata
			if err := storeMessageInImmuDB(msg); err != nil { // msg is a copy, but it's fine
				broadcastLogger().Error(ctx, "Failed to store block message in ImmuDB", err)
			}

			broadcastLogger().Info(ctx, "Block processed and stored successfully",
				ion.String("block_hash", msg.Block.BlockHash.Hex()),
				ion.Uint64("block_number", msg.Block.BlockNumber))
			return nil
		})

		broadcastLogger().Info(context.Background(), "ZKBlock received",
			ion.String("sender", msg.Sender),
			ion.Uint64("block_number", msg.Block.BlockNumber),
			ion.String("block_hash", msg.Block.BlockHash.Hex()),
			ion.Int("txn_count", len(msg.Block.Transactions)))
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
			broadcastLogger().Error(context.Background(), "Failed to initialize BlockPropagationLocalGRO", err)
			return
		}
	}
	peers := h.Network().Peers()

	// Convert message to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to marshal block message", err)
		return
	}
	msgBytes = append(msgBytes, '\n')

	// Track forwarding metrics
	var successCount int
	var successMutex sync.Mutex
	wg, err := BlockPropagationLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BlockPropagationForwardWG)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for block forwarding", err)
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
				broadcastLogger().Debug(ctx, "Failed to open stream", ion.String("peer", peerIDForGoroutine.String()))
				return err
			}
			defer stream.Close()

			if _, err := stream.Write(msgBytes); err != nil {
				broadcastLogger().Debug(ctx, "Failed to write message", ion.String("peer", peerIDForGoroutine.String()))
				return err
			}

			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			metrics.MessagesSentCounter.WithLabelValues(msg.Type, peerIDForGoroutine.String()).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BlockPropagationForwardWG)); err != nil {
			broadcastLogger().Error(context.Background(), "Failed to start goroutine for block forwarding", err, ion.String("peer", peerIDForGoroutine.String()))
		}
	}

	wg.Wait()

	broadcastLogger().Info(context.Background(), "Block forwarded to peers",
		ion.String("type", msg.Type),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)-1))
}
