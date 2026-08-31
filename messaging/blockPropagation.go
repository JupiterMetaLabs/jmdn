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
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

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

// maxBlockStreamBytes caps a single direct block-propagation stream read so a
// peer that opens the stream and streams an endless body (no newline) cannot
// force unbounded allocation → remote OOM. Sized just above the 7 MB gossip
// cap (Pubsub.MaxMessageSize) so any block that fits the gossip path also fits
// the direct stream. blockStreamReadTimeout bounds how long a slow/idle peer may
// hold the read open.
const (
	// maxBlockStreamBytes shares config.MaxBlockMessageBytes with the gossip topic
	// cap so the two transports can never silently diverge on block size.
	maxBlockStreamBytes    = config.MaxBlockMessageBytes
	blockStreamReadTimeout = 30 * time.Second
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

	// Wire the durable equivocation store on EVERY node — equivocation
	// detection runs in validateRemoteBlock on every block-receiving node, not
	// just the sequencer. The DB-backed store acquires its accountsdb connection
	// on demand (first checkEquivocation), so this is safe at init. If unset the
	// detector is in-memory only and does not survive restart.
	if equivocationStore == nil {
		SetEquivocationStore(DBEquivocationStore{})
	}
	return initErr
}

// generateBlockMessageID creates a unique ID for a block message
func generateBlockMessageID(sender, nonce string, timestamp int64) string {
	hasher := sha256.New()
	hasher.Write(fmt.Appendf(nil, "%s-%s-%d", sender, nonce, timestamp))
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

// storeMessageInDB stores a message in ImmuDB using the appropriate key
func storeMessageInDB(msg config.BlockMessage) error {
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
		broadcastLogger().Error(context.Background(), "Failed to store message in ThebeDB", err, ion.String("key", key))
		return err
	}

	// Update message set
	if err := updateMessageSet(key); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to update message set", err, ion.String("key", key))
		return err
	}

	broadcastLogger().Debug(context.Background(), "Message stored in ThebeDB", ion.String("key", key), ion.String("type", msg.Type))
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

// HandleBlockStream is the registered direct block-propagation stream handler; it
// feeds HandleReceivedBlockMessage (the shared receive path).
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

	// Bound the read: cap the size (remote-OOM guard, mirrors the 7 MB gossip cap)
	// and set a deadline so a slow/idle peer cannot hold the stream open forever.
	_ = stream.SetReadDeadline(time.Now().Add(blockStreamReadTimeout))
	reader := bufio.NewReader(io.LimitReader(stream, maxBlockStreamBytes))
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

	HandleReceivedBlockMessage(msg, remotePeer, true)
}

// HandleReceivedBlockMessage is the single validate-and-apply path for a received
// block message, shared by both transports: the direct block stream
// (HandleBlockStream, forward=true) and the gossip mesh (forward=false, since the
// pubsub layer re-propagates). It runs dedup, the fail-closed admitZKBlock
// certificate gate, then processes and stores. `forward` gates ONLY the
// direct-stream re-flood; the fail-closed security gate is identical regardless
// of transport.
func HandleReceivedBlockMessage(msg config.BlockMessage, remotePeer string, forward bool) {
	// Check for duplicates
	messageID := getMessageIDForBloomFilter(msg)
	if isMessageProcessed(messageID) {
		// remotePeer is the transport tag: "gossip:<peer>" for a gossip copy,
		// a bare peer id for a direct-stream copy. This is the dropped (second)
		// copy of a block delivered over both transports.
		broadcastLogger().Debug(context.Background(), "Duplicate message received",
			ion.String("message_id", messageID), ion.String("from", remotePeer))
		timeoutPeer(remotePeer, 20*time.Second)
		return
	}

	// NOTE: do NOT mark processed here. The dedup store is a
	// Bloom filter whose entries can never be removed, so a hash added before
	// validation can never be cleared — an invalid block claiming a genuine
	// hash would permanently mark that hash as seen, so the genuine block is
	// later dropped as a "duplicate". Caching happens ONLY after a block
	// validates: zkblocks via admitZKBlock (post-gate, below); other message
	// types in the else branch.

	// For ZK blocks: fail-closed. A remotely received block must be validated
	// BEFORE it is forwarded, processed, or persisted, and the committee
	// certificate is mandatory. Nothing about an unvalidated remote block may
	// cross into forwarding or state mutation.
	if msg.Type == "zkblock" && msg.Block != nil {
		broadcastLogger().Info(context.Background(), "Received ZK block from peer",
			ion.String("block_hash", msg.Block.BlockHash.Hex()),
			ion.Uint64("block_number", msg.Block.BlockNumber),
			ion.Int("txn_count", len(msg.Block.Transactions)),
			ion.String("from", remotePeer)) // transport tag: "gossip:<peer>" = gossip, bare id = direct

		// A consensus REJECTION notice carries no block to apply. Discard it
		// without processing, and without forwarding an unauthenticated
		// rejection.
		if status, ok := msg.Data["status"]; ok && status == "rejected" {
			broadcastLogger().Info(context.Background(), "Received consensus REJECTION for block - discarding",
				ion.String("block_hash", msg.Block.BlockHash.Hex()))
			helper.NotifyBroadcast(msg)
			return
		}

		// Fail-closed gate — runs synchronously before any side effect. On
		// success admitZKBlock marks the block processed (validate-before-cache);
		// on failure the hash never enters the dedup cache.
		if rej := admitZKBlock(context.Background(), msg, messageID); rej != nil {
			broadcastLogger().Warn(context.Background(), "Rejecting invalid remote block before forward/process/persist",
				ion.Err(rej.err),
				ion.String("reason", rej.reason),
				ion.String("peer", remotePeer),
				ion.String("block_hash", msg.Block.BlockHash.Hex()),
				ion.Uint64("block_number", msg.Block.BlockNumber))
			metrics.BlocksRejectedCounter.WithLabelValues(rej.reason, remotePeer).Inc()
			timeoutPeer(remotePeer, 30*time.Second)
			return // no forward, no mutation, no persistence, NOT cached
		}

		// Block validated and marked processed → forwarding is now safe. The direct
		// re-flood is OFF by default (gossip-only): the pubsub mesh re-propagates,
		// and gossip-delivered blocks already pass forward=false. It only runs when
		// direct propagation is explicitly re-enabled (consensus.p2p >= 1), so a
		// gossip-only node never re-floods a block it received over a direct stream.
		if forward && directBlockPropagationEnabled() && msg.Hops < config.MaxHops {
			msg.Hops++
			if globalHost != nil {
				broadcastLogger().Info(context.Background(), "Forwarding validated ZK block to peers",
					ion.String("block_hash", msg.Block.BlockHash.Hex()),
					ion.Uint64("block_number", msg.Block.BlockNumber),
					ion.Int("hops", msg.Hops))

				BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
					forwardBlock(globalHost, msg)
					return nil
				})
			} else {
				broadcastLogger().Error(context.Background(), "Cannot forward block: global host not initialized", errors.New("global host not initialized"))
			}
		}

		// PROCESS AND PERSIST — only reachable after the gate has passed.
		BlockPropagationLocalGRO.Go(GRO.BlockPropagationProcessAndValidateThread, func(ctx context.Context) error {
			// Rejected notices and certificate verification already ran in the
			// fail-closed admitZKBlock gate BEFORE forward/process (v2 receive path).
			// Storage resolves through the process-wide ThebeHandle (nil conn).

			// Pull-on-demand: ensure contract bytecode/metadata is present BEFORE
			// execution, so a missed ContractMessage gossip (e.g. sequencer went
			// offline before propagation completed) cannot make a Type-2 call
			// fall through to the regular transfer path.
			if h := getHostInstance(); h != nil {
				PrefetchMissingContracts(ctx, h, msg.Block.Transactions)
			}

			// Process all transactions in the block atomically with rollback capability.
			if err := BlockProcessing.ProcessBlockTransactions(context.Background(), msg.Block, nil); err != nil {
				broadcastLogger().Error(ctx, "Block processing failed - not storing block", err,
					ion.String("block_hash", msg.Block.BlockHash.Hex()))
				return fmt.Errorf("block processing failed - not storing block: %w", err)
			}

			broadcastLogger().Info(ctx, "All transactions processed successfully - storing block",
				ion.String("block_hash", msg.Block.BlockHash.Hex()))

			// Persist the committee certificate that already passed
			// verifyBlockCertificate (fail-closed 2f+1) so it survives past this
			// ephemeral gossip envelope and is re-verifiable on sync (P-cert /
			// ThebeSync). Advisory field; does not affect BlockHash.
			if cert := msg.Data["bls_results"]; cert != "" {
				msg.Block.CommitteeCertificate = cert
			}

			// Store the validated and processed block in main DB
			if err := DB_OPs.StoreZKBlock(nil, msg.Block); err != nil {
				broadcastLogger().Error(ctx, "Failed to store block in database", err,
					ion.String("block_hash", msg.Block.BlockHash.Hex()))
				return fmt.Errorf("failed to store block in database: %w", err)
			}

			// Full block stored + processed → advance the tip marker.
			// Monotonic: a replayed/out-of-order block can never regress it.
			// StoreZKBlock no longer writes the marker itself (skeleton safety).
			if _, _, err := DB_OPs.UpdateLatestBlockMonotonic(msg.Block.BlockNumber); err != nil {
				broadcastLogger().Warn(context.Background(), "latest_block monotonic update failed (non-fatal: ReconcileBlockNumber heals forward)",
					ion.String("error", err.Error()),
					ion.Uint64("block_number", msg.Block.BlockNumber))
			}

			// M0.1 (Architecture §7.1) — receiver-side twin of the hook in
			// broadcast.go's ProcessBlockLocally. Every node folds a
			// committed height into its OWN slot counter independently
			// (§7.1b) — this is not receiving a peer's slot value, it is
			// deriving the same one locally from the same certified event.
			DefaultSlotStore.AdvanceOnCommit(msg.Block.BlockNumber, msg.Block.Period)

			// M4 §C (Architecture §4.2 Rule 2, §4.5) — receiver-side twin of the
			// hook in broadcast.go's ProcessBlockLocally. See entropy_reveal.go's
			// header comment for scope (currently a no-op in the live system).
			foldBlockDeclaredReveals(msg.Block)

			// M4 §D, receiver-side twin — see entropy_finalise.go's header
			// comment and broadcast.go's ProcessBlockLocally twin.
			// B1 — verify the parent's commit certificate and DERIVE its aggregate
			// locally, then record it for the fallback fold. Runs BEFORE
			// maybeFinaliseCompletedEpochs so a window slot recorded by this block
			// is available to any epoch this same block finalises.
			VerifyAndRecordPrevCert(msg.Block)

			maybeFinaliseCompletedEpochs(msg.Block)

			// M4 §4.4 RevealPush — added 2026-08-20. If this node is on the
			// current epoch's entropy committee and its reveal has not landed
			// in a committed block yet, push it to whoever proposed this one.
			//
			// msg.Sender is the peer that broadcast this block, i.e. the
			// proposer — a real, already-present value, not a resolver seam.
			// §4.4 requires pushing once per SLOT across the whole reveal
			// window rather than once per epoch, and a committed block is
			// exactly a slot boundary, so this hook is the natural trigger.
			//
			// In a goroutine: a push opens a network stream, and the commit
			// path must never block on a peer. Every no-op case (not seated,
			// no identity, already landed, window closed, we ARE the proposer)
			// is checked inside PushOwnRevealForSlot, so this stays cheap for
			// the P-m nodes that are not on the committee.
			if proposer, decErr := peer.Decode(msg.Sender); decErr == nil {
				pushSlot := DefaultSlotStore.Current()
				go func() {
					if err := PushOwnRevealForSlot(pushSlot, proposer); err != nil {
						broadcastLogger().Warn(context.Background(), "entropy: RevealPush to the current proposer failed (will retry next slot)",
							ion.String("error", err.Error()),
							ion.Uint64("slot", pushSlot))
					}
				}()
			}

			// Index the block's txs into the SQLite address index. Non-sequencer
			// nodes receive blocks via pubsub; indexing them here keeps
			// eth_getTransactionsByAddress current between catchups instead of
			// drifting stale while IsReady stays true. Async + drop-on-overflow;
			// drops heal via the next gap scan.
			txindex.IndexBlockAsync(msg.Block)

			// Store block message metadata
			if err := storeMessageInDB(msg); err != nil { // msg is a copy, but it's fine
				broadcastLogger().Error(ctx, "Failed to store block message in ThebeDB", err)
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
		// Non-consensus message types have no validation gate here, so mark
		// processed on receipt to preserve duplicate/loop suppression. Only
		// zkblock caching is deferred behind validation.
		markMessageProcessed(messageID)

		// Handle other message types (not our focus)
		if forward && msg.Hops < config.MaxHops {
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

// admitZKBlock is the validate-before-cache gate: a zkblock hash only enters the
// dedup cache after the block passes fail-closed validation (validateRemoteBlock).
// It marks the block's messageID processed ONLY if validation succeeds. Because
// the dedup cache is a Bloom filter whose entries cannot be deleted, a block hash
// must never enter it before the block is proven valid: otherwise an invalid
// block carrying a genuine block's hash would permanently mark that hash "seen",
// so the genuine block is dropped when it later arrives. Returns the rejection
// (nil == admitted and cached).
func admitZKBlock(ctx context.Context, msg config.BlockMessage, messageID string) *blockRejection {
	if rej := validateRemoteBlock(ctx, msg); rej != nil {
		return rej // rejected block does NOT occupy the dedup cache
	}
	markMessageProcessed(messageID)
	return nil
}

// validateRemoteBlock is the fail-closed gate for every remotely received
// zkblock. It MUST pass before the block is forwarded, processed, or
// persisted. It deliberately performs only authenticity / internal-consistency
// checks that do NOT depend on mutable DB state (balances, live nonces), so it
// cannot false-reject an honest block due to the tx-application race that makes
// strict DB-nonce checks unsafe on this path. It recomputes the canonical
// block hash from transaction CONTENTS and binds tx.Hash to contents. The
// remaining deferred check is STARK-proof verification (verifyBlockProof is a
// placeholder while the prover is placeholder-grade).
func validateRemoteBlock(ctx context.Context, msg config.BlockMessage) *blockRejection {
	b := msg.Block
	if b == nil {
		return reject("nil_block", "block is nil")
	}
	if len(b.Transactions) == 0 {
		return reject("empty_block", "block %s has no transactions", b.BlockHash.Hex())
	}

	// FeeRecipients is NOT bound into the canonical block hash and
	// the catch-up (FastsyncV2) apply path does not credit it (passes nil), so a
	// block carrying FeeRecipients would apply differently on live-vs-catch-up
	// nodes — a silent, non-healing balance divergence (the merkle fingerprint
	// omits it too). Until it is hash-bound AND threaded through catch-up, refuse
	// to admit such a block so an accidental enable fails LOUD (rejected) rather
	// than silently diverging balances.
	if len(b.FeeRecipients) > 0 {
		return reject("feerecipients_unsupported",
			"block %s carries FeeRecipients, which is not yet hash-bound or catch-up-threaded; refusing to admit", b.BlockHash.Hex())
	}

	// (Signature/chain-ID authenticity) Every transaction must carry a valid
	// signature for the configured chain. CheckSignature recovers the sender via
	// the chain-bound signer and compares it against tx.From; it reads no
	// balances or live nonces, so it is race-free. Because sender ECDSA
	// signatures cannot be produced without the sender's key, a block cannot
	// move another account's assets.
	for i := range b.Transactions {
		tx := b.Transactions[i]
		if tx.From == nil {
			return reject("bad_signature", "tx %d has nil sender", i)
		}
		ok, err := Security.CheckSignature(&tx, ctx)
		if err != nil || !ok {
			return reject("bad_signature", "tx %d (%s) signature invalid: %v", i, tx.Hash.Hex(), err)
		}
		// tx.Hash is a remote-supplied wire field, and canonical body binding
		// (checkBodyBinding) hashes OVER tx.Hash. If it is not verified against the
		// transaction contents, a crafted transaction could carry its own body
		// while copying a certified block's tx.Hash values to reproduce that
		// block's BlockHash and re-present its committee certificate. Require
		// tx.Hash == hash(contents); reject a mismatch.
		if hok, herr := Security.CheckTransactionHash(&tx, ctx); herr != nil || !hok {
			return reject("tx_hash_mismatch", "tx %d hash does not match its contents: %v", i, herr)
		}

		// Reject negative numeric fields on the remote path too. A negative
		// Value/gas field would invert the sender/receiver balance arithmetic in
		// execution, allowing a block to debit an account it should not. The
		// ingress gate (Security.AllChecks) does not cover blocks arriving from
		// peers, so the value gate must be enforced here independently.
		if vok, verr := Security.CheckTransactionValues(&tx); !vok {
			return reject("negative_tx_value", "tx %d has a negative numeric field: %v", i, verr)
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

	// (Canonical body binding) Recompute BlockHash + TxnsRoot from the
	// received transactions and reject a mismatch. The certificate's votes are
	// signed over BlockHash, so this binds the certificate to THIS body: a
	// certified hash cannot be reused over a substituted (even validly-signed)
	// transaction set. Runs BEFORE certificate verification.
	if EnforceBodyBinding {
		if rej := checkBodyBinding(b); rej != nil {
			return rej
		}
		// Bind BlockHash to transaction CONTENTS, not the wire tx.Hash: recompute
		// the block hash from ethTx.Hash() of each tx and reject a mismatch.
		// checkBodyBinding hashes over tx.Hash (now verified per tx above); this
		// is the authoritative contents-based gate and holds even if the per-tx
		// check is ever bypassed.
		if ok, err := Security.CheckBlockHash(b); err != nil || !ok {
			return reject("block_hash_mismatch", "block %s hash does not match tx contents: %v", b.BlockHash.Hex(), err)
		}
	}

	// (Proof seam) Hook for ZK/STARK verification, ordered before the
	// certificate check. See verifyBlockProof.
	if err := verifyBlockProof(b); err != nil {
		return reject("invalid_proof", "block %s proof verification failed: %v", b.BlockHash.Hex(), err)
	}

	// (Chain linkage) parent-hash + height + state-root chain, catchup-safe
	// (see checkLinkage).
	if EnforceBlockLinkage {
		if rej := checkLinkage(ctx, b); rej != nil {
			return rej
		}
	}

	// (Committee certificate) MANDATORY and must reach quorum. Absent or empty
	// bls_results is a rejection, not a pass.
	if rej := verifyBlockCertificate(msg); rej != nil {
		return rej
	}

	// (Equivocation) Recorded LAST — only after the block is fully validated —
	// so an unvalidated block cannot enter the height->hash map and cause the
	// genuine block to be rejected. A second, DIFFERENT validated block at a
	// height already seen is a signed fork → rejected.
	return checkEquivocation(b.BlockNumber, b.BlockHash.Hex())
}

// verifyBlockCertificate enforces a mandatory committee certificate that reaches
// the Byzantine 2f+1 threshold. Verification is delegated to the SINGLE shared
// verifier VerifyCertificate, which:
//   - fails closed via the eligibility source (no source / error / empty set
//     => rejection naming the defect);
//   - verifies each vote as BLOCK-BOUND — a signature over this block's
//     hash, so a vote cannot be reused on another block; legacy "vote:<v>"
//     signatures are accepted only while RejectLegacyVotes is false;
//   - counts only eligible signers (peer_id ∈ live buddy set minus block_buddy);
//   - de-duplicates by peer_id AND bls_pub so one signer cannot be counted more
//     than once toward quorum;
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

	// Routed through VerifyCertificateForRound so the tally can run against the
	// SEATED committee once JMDN_COMMITTEE_V2 is on. With the flag off this is
	// byte-identical to the previous VerifyCertificate call. The round context
	// comes from the block, never the clock - see RoundContextForBlock.
	rc, rcErr := RoundContextForBlock(msg.Block)
	if rcErr != nil {
		// Fail closed - most likely this node hasn't processed the
		// TimeoutCertificate that advanced Period for this height yet. See
		// ErrPeriodNotSynced's doc comment.
		return reject("period_not_synced",
			"round context unavailable (fail closed): %v", rcErr)
	}
	res, err := VerifyCertificateForRound(responses, msg.Block.BlockHash.Hex(), msg.Block.BlockNumber, rc)
	if err != nil {
		// Fail closed: no authenticated committee => cannot verify.
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
