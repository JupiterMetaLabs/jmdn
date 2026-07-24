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
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/rs/zerolog/log"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/txindex"
	"gossipnode/Security"
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

	// For ZK blocks: FAIL CLOSED. A remotely received block must be validated
	// BEFORE it is forwarded, processed, or persisted (JMDN-001). Previously the
	// handler forwarded first and treated the committee certificate as optional,
	// so any peer could inject a block that mutated state and propagated
	// network-wide. Nothing about an unvalidated remote block may now cross into
	// forwarding or state mutation.
	if msg.Type == "zkblock" && msg.Block != nil {
		log.Info().
			Str("block_hash", msg.Block.BlockHash.Hex()).
			Uint64("block_number", msg.Block.BlockNumber).
			Int("txn_count", len(msg.Block.Transactions)).
			Msg("Received ZK block from peer")

		// A consensus REJECTION notice carries no block to apply. Discard it
		// without processing (and without forwarding an unauthenticated
		// rejection, which would otherwise be a cheap censorship/DoS vector).
		if status, ok := msg.Data["status"]; ok && status == "rejected" {
			log.Info().
				Str("block_hash", msg.Block.BlockHash.Hex()).
				Msg("Received consensus REJECTION for block - discarding")
			helper.NotifyBroadcast(msg)
			return
		}

		// FAIL-CLOSED GATE — runs synchronously before any side effect.
		if rej := validateRemoteBlock(context.Background(), msg); rej != nil {
			log.Warn().
				Err(rej.err).
				Str("reason", rej.reason).
				Str("peer", remotePeer).
				Str("block_hash", msg.Block.BlockHash.Hex()).
				Uint64("block_number", msg.Block.BlockNumber).
				Msg("Rejecting invalid remote block before forward/process/persist")
			metrics.BlocksRejectedCounter.WithLabelValues(rej.reason, remotePeer).Inc()
			timeoutPeer(remotePeer, 30*time.Second)
			return // no forward, no mutation, no persistence
		}

		// Block validated → forwarding is now safe.
		if msg.Hops < config.MaxHops {
			msg.Hops++
			if globalHost != nil {
				log.Info().
					Str("block_hash", msg.Block.BlockHash.Hex()).
					Uint64("block_number", msg.Block.BlockNumber).
					Int("hops", msg.Hops).
					Msg("Forwarding validated ZK block to peers")

				BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
					forwardBlock(globalHost, msg)
					return nil
				})
			} else {
				log.Error().Msg("Cannot forward block: global host not initialized")
			}
		}

		// PROCESS AND PERSIST — only reachable after the gate has passed.
		BlockPropagationLocalGRO.Go(GRO.BlockPropagationProcessAndValidateThread, func(ctx context.Context) error {
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

			// Full block stored + processed → advance the tip marker.
			// Monotonic: a replayed/out-of-order block can never regress it.
			// StoreZKBlock no longer writes the marker itself (skeleton safety).
			if _, _, err := DB_OPs.UpdateLatestBlockMonotonic(msg.Block.BlockNumber); err != nil {
				log.Warn().Err(err).Uint64("block_number", msg.Block.BlockNumber).
					Msg("latest_block monotonic update failed (non-fatal: ReconcileBlockNumber heals forward)")
			}

			// Index the block's txs into the SQLite address index. Previously only
			// the sequencer path (broadcast.go) indexed live — pubsub-received
			// blocks on non-sequencer nodes were never indexed between catchups,
			// so eth_getTransactionsByAddress drifted stale with IsReady still
			// true. Async + drop-on-overflow; drops heal via the next gap scan.
			txindex.IndexBlockAsync(msg.Block)

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

// blockRejection carries a machine-readable reason label (for the
// BlocksRejectedCounter metric) alongside the human-readable error.
type blockRejection struct {
	reason string
	err    error
}

// reject builds a *blockRejection with a metric reason and a formatted error.
func reject(reason, format string, args ...interface{}) *blockRejection {
	return &blockRejection{reason: reason, err: fmt.Errorf(format, args...)}
}

// validateRemoteBlock is the fail-closed gate for every remotely received
// zkblock (JMDN-001). It MUST pass before the block is forwarded, processed, or
// persisted. It deliberately performs only authenticity / internal-consistency
// checks that do NOT depend on mutable DB state (balances, live nonces), so it
// cannot false-reject an honest block due to the tx-application race that makes
// strict DB-nonce checks unsafe on this path. Deeper checks (state re-execution,
// STARK-proof verification, canonical block-hash recompute) are tracked
// separately — see audits/JMDN-001-remediation-plan.md.
func validateRemoteBlock(ctx context.Context, msg config.BlockMessage) *blockRejection {
	b := msg.Block
	if b == nil {
		return reject("nil_block", "block is nil")
	}
	if len(b.Transactions) == 0 {
		return reject("empty_block", "block %s has no transactions", b.BlockHash.Hex())
	}

	// (Signature/chain-ID authenticity) Every transaction must carry a valid
	// signature for the configured chain. CheckSignature recovers the sender via
	// the chain-bound signer and compares it against tx.From; it reads no
	// balances or live nonces, so it is race-free. This is what prevents an
	// injected block from transferring other users' assets: the attacker cannot
	// forge sender ECDSA signatures.
	for i := range b.Transactions {
		tx := b.Transactions[i]
		if tx.From == nil {
			return reject("bad_signature", "tx %d has nil sender", i)
		}
		ok, err := Security.CheckSignature(&tx, ctx)
		if err != nil || !ok {
			return reject("bad_signature", "tx %d (%s) signature invalid: %v", i, tx.Hash.Hex(), err)
		}
	}

	// (In-block nonce consistency) Each sender's nonces must be strictly
	// ascending with no duplicates within the block. Catches replayed / reordered
	// / duplicated transactions without depending on DB state.
	lastNonce := make(map[common.Address]uint64, len(b.Transactions))
	for i := range b.Transactions {
		tx := b.Transactions[i]
		from := *tx.From
		if prev, seen := lastNonce[from]; seen && tx.Nonce <= prev {
			return reject("bad_nonce",
				"tx %d sender %s nonce %d not strictly ascending (prev %d)",
				i, from.Hex(), tx.Nonce, prev)
		}
		lastNonce[from] = tx.Nonce
	}

	// (Chain linkage) parent-hash + height, catchup-safe (see checkLinkage).
	if EnforceBlockLinkage {
		if rej := checkLinkage(ctx, b); rej != nil {
			return rej
		}
	}

	// (Committee certificate) MANDATORY and must reach quorum. Absent/empty is a
	// rejection, not a pass — this closes the "omit bls_results" bypass (D2).
	if rej := verifyBlockCertificate(msg); rej != nil {
		return rej
	}

	// (Equivocation) Recorded LAST — only after the block is fully validated —
	// so an attacker cannot poison the height->hash map with an unvalidated
	// block and cause the genuine block to be rejected. A second, DIFFERENT
	// validated block at a height already seen is a signed fork → rejected.
	return checkEquivocation(b.BlockNumber, b.BlockHash.Hex())
}

// verifyBlockCertificate enforces a mandatory committee certificate that reaches
// the Byzantine 2f+1 threshold. Verification is delegated to the SINGLE shared
// verifier VerifyCertificate (P2), which:
//   - FAILS CLOSED via the P1 eligibility source (no source / error / empty set
//     => rejection naming the defect);
//   - verifies each vote as BLOCK-BOUND (D3) — a signature over this block's
//     hash, so a vote cannot be replayed onto another block; legacy "vote:<v>"
//     signatures are accepted only while RejectLegacyVotes is false;
//   - counts only eligible signers (peer_id ∈ live buddy set minus block_buddy);
//   - de-duplicates by peer_id AND bls_pub so one signer cannot fake a quorum;
//   - requires 2f+1 over the authenticated committee size (never a simple
//     majority, never the vote count). A single supplied vote cannot finalize.
func verifyBlockCertificate(msg config.BlockMessage) *blockRejection {
	raw, ok := msg.Data["bls_results"]
	if !ok || len(raw) == 0 {
		return reject("no_certificate", "block %s has no committee certificate", msg.Block.BlockHash.Hex())
	}

	var responses []BLS_Signer.BLSresponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		return reject("malformed_certificate", "malformed bls_results: %v", err)
	}
	if len(responses) == 0 {
		return reject("no_certificate", "empty committee certificate")
	}

	res, err := VerifyCertificate(responses, msg.Block.BlockHash.Hex())
	if err != nil {
		// FAIL CLOSED (P1): no authenticated committee => cannot verify.
		return reject("committee_source_invalid",
			"refusing consensus participation (fail closed): %v", err)
	}
	if !res.Reached {
		return reject("quorum_not_met",
			"committee quorum not met: %d eligible verified +1 votes, need %d (2f+1 over committee size %d)",
			res.YesVotes, res.Threshold, res.CommitteeSize)
	}
	return nil
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
			defer stream.Close()

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
