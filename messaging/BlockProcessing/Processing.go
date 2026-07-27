package BlockProcessing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"go.opentelemetry.io/otel/attribute"
)

const (
	LOG_FILE = ""
	TOPIC    = "BlockProcessing"
)

// ErrStaleNonce is returned when a transaction's nonce is lower than the
// account's current DB nonce. This is a skippable condition — it means the
// tx was valid at security-check time but the account moved on before
// ProcessBlockLocally ran (race between vote validation and execution).
var ErrStaleNonce = errors.New("stale nonce")

// AccountSnapshot captures the mutable state of an account before block processing begins.
// All three fields must be restored atomically on rollback to prevent nonce/count corruption.
type AccountSnapshot struct {
	Balance     string
	TxNonce     uint64
	TxCountSent uint64
	UpdatedAt   int64
}

// txStage accumulates one transaction's account mutations in memory so they can
// commit in a SINGLE accountsdb ExecAll together with the tx_processed marker.
// Keeping balances and the marker in one commit means either the whole tx
// lands or none of it does, so a crash mid-transaction cannot leave
// partially-applied balances without a marker.
//
// get is READ-THROUGH: an account already staged by an earlier step of the SAME
// tx (self-transfer, sender==coinbase, recipient==zkvm, ...) returns the staged
// document, so later steps observe earlier mutations exactly as they did under
// sequential commits.
type txStage struct {
	conn  *config.PooledConnection
	docs  map[common.Address]*DB_OPs.Account
	order []common.Address // ExecAll op ordering = first-touch order (deterministic)
}

func newTxStage(conn *config.PooledConnection) *txStage {
	return &txStage{conn: conn, docs: make(map[common.Address]*DB_OPs.Account)}
}

// get returns the staged document for addr, falling back to the committed DB
// state for accounts this tx has not touched yet.
func (s *txStage) get(addr common.Address) (*DB_OPs.Account, error) {
	if doc, ok := s.docs[addr]; ok {
		return doc, nil
	}
	return DB_OPs.GetAccount(s.conn, addr)
}

// put stages the (mutated) document. No DB write happens here.
func (s *txStage) put(doc *DB_OPs.Account) {
	if _, ok := s.docs[doc.Address]; !ok {
		s.order = append(s.order, doc.Address)
	}
	s.docs[doc.Address] = doc
}

// staged returns the documents in first-touch order for the atomic commit.
func (s *txStage) staged() []*DB_OPs.Account {
	out := make([]*DB_OPs.Account, 0, len(s.order))
	for _, addr := range s.order {
		out = append(out, s.docs[addr])
	}
	return out
}

// Global map to track processed transactions during block processing
var (
	processedTxs      = make(map[string]bool)
	processedTxsMutex sync.Mutex
	txProcessingLocks = make(map[string]*sync.Mutex)
	txLocksGuard      = sync.Mutex{}

	// Configurable defaults that can be adjusted as needed
	DefaultGasLimit       = int64(21000)
	DefaultGasPrice       = int64(1000000000) // 1 Gwei
	CreateMissingAccounts = true              // Set to false to disable automatic DID creation
)

// ClearProcessedTransactions clears the processed transactions map
// This should be called at the start of processing a new block
func ClearProcessedTransactions() {
	processedTxsMutex.Lock()
	defer processedTxsMutex.Unlock()
	processedTxs = make(map[string]bool)
}

// getTransactionLock gets or creates a mutex for a specific transaction
func getTransactionLock(txHash string) *sync.Mutex {
	txLocksGuard.Lock()
	defer txLocksGuard.Unlock()

	if _, exists := txProcessingLocks[txHash]; !exists {
		txProcessingLocks[txHash] = &sync.Mutex{}
	}
	return txProcessingLocks[txHash]
}

// cleanupTransactionLock removes a transaction lock when no longer needed
func cleanupTransactionLock(txHash string) {
	txLocksGuard.Lock()
	defer txLocksGuard.Unlock()

	delete(txProcessingLocks, txHash)
}

// ─── Per-block-hash apply lock ────────────────────────────────────────────────
//
// blockApplyLock serializes ProcessBlockTransactions per block hash so a block
// delivered more than once — direct stream + gossip near-simultaneously, a
// re-flood, or live delivery racing catch-up — is applied EXACTLY ONCE. Without
// it, two goroutines run the full apply for the same block and each credits the
// balances; the double-credit is timing-dependent, so nodes diverge. The lock is
// held across the whole apply (the already-processed check + balance writes +
// marker commit), making that sequence atomic per block hash. Different block
// hashes take different locks and still process in parallel.
//
// Reference-counted so an entry is removed once no goroutine holds or waits on
// it (no unbounded growth). refs is guarded by blockApplyLocksMu, so a concurrent
// acquirer always shares the same *blockApplyLock and a release cannot delete a
// lock another goroutine is about to use.
type blockApplyLock struct {
	mu   sync.Mutex
	refs int
}

var (
	blockApplyLocks   = make(map[string]*blockApplyLock)
	blockApplyLocksMu sync.Mutex
)

// acquireBlockApplyLock locks the per-hash apply lock and returns its release func.
func acquireBlockApplyLock(blockHash string) func() {
	blockApplyLocksMu.Lock()
	l, ok := blockApplyLocks[blockHash]
	if !ok {
		l = &blockApplyLock{}
		blockApplyLocks[blockHash] = l
	}
	l.refs++
	blockApplyLocksMu.Unlock()

	l.mu.Lock()

	return func() {
		l.mu.Unlock()
		blockApplyLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(blockApplyLocks, blockHash)
		}
		blockApplyLocksMu.Unlock()
	}
}

// ProcessBlockTransactions processes all transactions in a block atomically
// If any transaction fails, all are rolled back
func ProcessBlockTransactions(logger_ctx context.Context, block *config.ZKBlock, accountsClient *config.PooledConnection) error {
	// Serialize concurrent applies of the SAME block so it is applied exactly
	// once no matter how many copies arrive (multi-transport delivery, re-flood,
	// or live delivery racing catch-up). Held across the already-processed check
	// and the full apply below, so a second caller blocks here and then observes
	// the committed block marker and returns without re-crediting balances.
	releaseBlockLock := acquireBlockApplyLock(block.BlockHash.Hex())
	defer releaseBlockLock()

	// Record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("BlockProcessing").Start(logger_ctx, "BlockProcessing.ProcessBlockTransactions")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(
		attribute.Int64("block_number", int64(block.BlockNumber)),
		attribute.String("block_hash", block.BlockHash.Hex()),
		attribute.Int("transaction_count", len(block.Transactions)),
	)

	// Check if block was already processed.
	// Dual-read value-aware guard (accountsdb authoritative, defaultdb
	// legacy) — the old Exists→Read path only ever saw defaultdb.
	blockKey := DB_OPs.BlockProcessedKey(block.BlockHash.Hex())
	processed, err := DB_OPs.IsMarkerApplied(accountsClient, blockKey)
	if err == nil && processed {
		span.SetAttributes(attribute.String("status", "already_processed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Info(span_ctx, "Block already processed, skipping",
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int64("block_number", int64(block.BlockNumber)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
		)
		// The block's effects ARE applied (marker proves it) — give the applied
		// anchor a chance to catch up if this duplicate is the contiguous next.
		advanceAppliedAnchor(span_ctx, accountsClient, block.BlockNumber)
		return nil
	}

	ClearProcessedTransactions()

	// Store original state to enable rollback - captures balance + nonce + txcount atomically
	originalState := make(map[common.Address]AccountSnapshot)
	affectedAccounts := make(map[common.Address]bool)

	// First, collect all affected DIDs from the block
	for _, tx := range block.Transactions {
		affectedAccounts[*tx.From] = true
		affectedAccounts[*tx.To] = true
	}
	affectedAccounts[*block.CoinbaseAddr] = true
	affectedAccounts[*block.ZKVMAddr] = true

	span.SetAttributes(attribute.Int("affected_accounts", len(affectedAccounts)))

	// Fetch and store original state BEFORE any processing
	for addr := range affectedAccounts {
		doc, err := DB_OPs.GetAccount(accountsClient, addr)
		if err == nil {
			originalState[addr] = AccountSnapshot{
				Balance:     doc.Balance,
				TxNonce:     doc.TxNonce,
				TxCountSent: doc.TxCountSent,
				UpdatedAt:   doc.UpdatedAt,
			}
		} else {
			// Account doesn't exist yet — zero-value snapshot, rollback will restore to 0
			originalState[addr] = AccountSnapshot{Balance: "0"}
		}
	}

	span.SetAttributes(attribute.Int("sorted_transactions", len(block.Transactions)))

	logger().NamedLogger.Info(span_ctx, "Starting block processing",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.Int("transaction_count", len(block.Transactions)),
		ion.Int("affected_accounts", len(affectedAccounts)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
	)

	// Resolve the whole block's tx_processed markers in ONE dual-DB batch
	// lookup (value-aware: -1 = revoked = not processed). Replaces the per-tx
	// Exists() calls, which read only defaultdb and flipped the session DB
	// twice per transaction. FAIL CLOSED: processing without the guard set
	// risks re-applying already-applied txs — the exact corruption this guard
	// exists to remove.
	blockTxHashes := make([]string, 0, len(block.Transactions))
	for i := range block.Transactions {
		blockTxHashes = append(blockTxHashes, block.Transactions[i].Hash.String())
	}
	liveApplied, err := DB_OPs.FilterProcessedTxMarkers(blockTxHashes)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "marker_prefilter_failed"))
		return fmt.Errorf("block %d: tx_processed marker prefilter failed (fail closed): %w", block.BlockNumber, err)
	}

	// Track successfully processed transactions for rollback bookkeeping (their
	// markers must be revoked if a later tx hard-fails).
	successfullyProcessedTxs := make([]string, 0, len(block.Transactions))

	// Process all transactions exactly as ordered by the Sequencer
	// If ANY fails, rollback ALL affected accounts
	for i, tx := range block.Transactions {
		// Check if this transaction was already processed within this block
		processedTxsMutex.Lock()
		if processedTxs[tx.Hash.Hex()] {
			logger().NamedLogger.Warn(span_ctx, "Duplicate transaction in block, skipping",
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("tx_index", i),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)
			processedTxsMutex.Unlock()
			continue
		}
		processedTxs[tx.Hash.Hex()] = true
		processedTxsMutex.Unlock()

		// Check if this transaction was already processed in a previous block
		// (or an earlier attempt at this one) — in-memory set from the
		// block-level dual-DB prefilter above.
		if liveApplied[tx.Hash.String()] {
			logger().NamedLogger.Warn(span_ctx, "Transaction already processed in previous block, skipping",
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("tx_index", i),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)
			continue
		}

		// Process the transaction with span context
		Process_err := processTransaction(span_ctx, tx, *block.CoinbaseAddr, *block.ZKVMAddr, block.FeeRecipients, accountsClient, block.Timestamp)
		if Process_err != nil {
			// DETERMINISM ON FINALIZED BLOCKS: a 2f+1 block is a canonical, agreed
			// transaction sequence — every node MUST apply every tx or state
			// diverges SILENTLY. The node's sync fingerprint is a Merkle root over
			// BLOCK HASHES, not account balances (internal/merkle), so a tx skipped
			// here is invisible to the sync monitor and never self-heals. Therefore
			// NO per-tx skip is permitted: ANY failure — INCLUDING a stale nonce
			// (a race with PoTS / concurrent block processing) — rolls back and
			// fails the WHOLE block. Nothing partial is stored, the head does not
			// advance, the node lags, and catch-up (FastsyncV2, which applies all
			// txs unconditionally) re-applies the block deterministically. Failing
			// loud and self-healing is correct; the previous "skip the stale-nonce
			// tx and keep the block" behaviour is exactly what produced fleet-wide
			// balance divergence at matching block heights.
			if errors.Is(Process_err, ErrStaleNonce) {
				logger().NamedLogger.Warn(span_ctx, "Stale nonce on finalized-block tx — failing the WHOLE block for determinism (catch-up re-applies)",
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.Int("tx_index", i),
					ion.String("error", Process_err.Error()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
				)
			}

			// ATOMICITY: If any transaction fails, roll back ALL affected accounts
			span.RecordError(Process_err)
			span.SetAttributes(attribute.String("status", "failed"), attribute.String("failed_tx_hash", tx.Hash.Hex()), attribute.Int("failed_tx_index", i))

			logger().NamedLogger.Error(span_ctx, "Transaction failed, rolling back entire block",
				Process_err,
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.Int("tx_index", i),
				ion.Int("total_transactions", len(block.Transactions)),
				ion.Int("successful_before_failure", len(successfullyProcessedTxs)),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)

			// The prefix's txs committed atomically WITH their markers, so
			// the markers are durable before this rollback runs. Revoke them
			// (overwrite with -1) BEFORE restoring balances — otherwise a replay
			// would skip txs 1..k against rolled-back balances (permanent
			// silent skip, worse than a bounded double-apply).
			//
			// ORDER IS LOAD-BEARING: revoke-then-restore. A crash between the two
			// leaves revoked markers over still-applied balances → replay
			// re-applies → bounded double-apply (the repairable direction). The
			// reverse order's crash leaves applied markers over restored
			// balances → permanent skip.
			//
			// If revocation itself fails, ABORT the rollback: applied+marked is a
			// CONSISTENT state (replay skips the prefix, retries only the failed
			// tx). Restoring balances under live markers would not be.
			//
			// The revoke+restore pair runs under the state-apply lock: the
			// reconciliation applier reads markers to decide what to apply, so
			// it must never observe revoked markers over not-yet-restored
			// balances (it would re-apply effects that are still present).
			DB_OPs.LockStateApply()
			if revokeErr := DB_OPs.RevokeTxProcessedMarkers(accountsClient, successfullyProcessedTxs); revokeErr != nil {
				DB_OPs.UnlockStateApply()
				span.RecordError(revokeErr)
				span.SetAttributes(attribute.String("status", "marker_revocation_failed"))
				logger().NamedLogger.Error(span_ctx, "Marker revocation failed — SKIPPING balance rollback (applied+marked prefix stays consistent)",
					revokeErr,
					ion.Int("prefix_txs", len(successfullyProcessedTxs)),
					ion.String("failed_tx_hash", tx.Hash.Hex()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
				)
				cleanupProcessingMarkers(span_ctx, accountsClient, tx.Hash.Hex())
				duration := time.Since(startTime).Seconds()
				span.SetAttributes(attribute.Float64("duration", duration))
				return fmt.Errorf("block processing failed at transaction %d/%d (hash: %s): %w (marker revocation also failed: %v — prefix left applied+marked)",
					i+1, len(block.Transactions), tx.Hash.Hex(), Process_err, revokeErr)
			}

			// Rollback all account state to original snapshot
			rollbackError := rollbackState(span_ctx, originalState, accountsClient)
			DB_OPs.UnlockStateApply()
			if rollbackError != nil {
				span.RecordError(rollbackError)
				logger().NamedLogger.Error(span_ctx, "Failed to rollback balances after transaction failure",
					rollbackError,
					ion.String("tx_hash", tx.Hash.Hex()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("topic", TOPIC),
					ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
				)
				// Still return the original error as it's more critical. Markers
				// are already revoked, so a partial restore fails toward
				// re-apply-on-replay (bounded double-apply), never toward skip.
			}

			// Clean up processing markers for all transactions processed so far
			for _, txHash := range successfullyProcessedTxs {
				cleanupProcessingMarkers(span_ctx, accountsClient, txHash)
			}
			cleanupProcessingMarkers(span_ctx, accountsClient, tx.Hash.Hex())

			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return fmt.Errorf("block processing failed at transaction %d/%d (hash: %s): %w", i+1, len(block.Transactions), tx.Hash.Hex(), Process_err)
		}

		// Track successfully processed transaction
		successfullyProcessedTxs = append(successfullyProcessedTxs, tx.Hash.Hex())
	}

	// tx_processed markers are committed atomically with each tx's
	// balances inside the loop rather than in a single block-end batch. A
	// block-end batch would write 2×txs+1 entries in ONE ExecAll, exceeding
	// immudb's 1024-entry transaction cap on any block with >511 transactions.
	// Per-tx commits are ≤5 entries each — the cap is
	// unreachable by construction.
	//
	// The block marker is a fast-path replay hint only (the per-tx markers
	// carry the exactly-once guarantee), so its failure must NOT roll back the
	// block's already-committed, already-marked transactions.
	if len(successfullyProcessedTxs) > 0 {
		if err := DB_OPs.WriteBlockProcessedMarker(accountsClient, block.BlockHash.Hex()); err != nil {
			span.RecordError(err)
			logger().NamedLogger.Warn(span_ctx, "Block marker write failed (non-fatal: per-tx markers carry the replay guarantee)",
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.String("error", err.Error()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
			)
		}
		// tx_processing advisory cleanups (defaultdb, transient — unchanged home).
		for _, txHash := range successfullyProcessedTxs {
			cleanupProcessingMarkers(span_ctx, accountsClient, txHash)
		}
		span.SetAttributes(attribute.Int("atomically_committed_transactions", len(successfullyProcessedTxs)))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("processed_transactions", len(successfullyProcessedTxs)),
	)
	logger().NamedLogger.Info(span_ctx, "Block processed successfully",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int64("block_number", int64(block.BlockNumber)),
		ion.Int("processed_transactions", len(successfullyProcessedTxs)),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.ProcessBlockTransactions"),
	)

	// Advance the accounts-applied anchor (accountsdb). Runs AFTER the atomic
	// marker commit — the block's effects are proven applied at this point. This
	// applies to zero-tx blocks too (nothing to apply still counts as applied).
	advanceAppliedAnchor(span_ctx, accountsClient, block.BlockNumber)

	return nil
}

// advanceAppliedAnchor advances the accounts-applied anchor via the contiguity
// rule (DB_OPs.NextLiveAnchor): only block == anchor+1 moves it. Gaps are left
// for reconciliation to fill and advance past.
//
// Errors are logged and swallowed BY DESIGN: a lagging anchor is safe
// (reconciliation re-covers the range; tx_processed markers prevent
// double-apply), but failing block processing over an anchor write would not be.
func advanceAppliedAnchor(span_ctx context.Context, accountsClient *config.PooledConnection, blockNumber uint64) {
	anchor, advanced, err := DB_OPs.AdvanceAppliedAnchorContiguous(accountsClient, blockNumber)
	if err != nil {
		logger().NamedLogger.Warn(span_ctx, "Applied-anchor advance failed (safe: anchor lags, recon will catch up)",
			ion.Uint64("block_number", blockNumber),
			ion.String("error", err.Error()),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.advanceAppliedAnchor"),
		)
		return
	}
	if advanced {
		logger().NamedLogger.Debug(span_ctx, "Applied anchor advanced (live, contiguous)",
			ion.Uint64("anchor", anchor),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.advanceAppliedAnchor"),
		)
	}
}

// cleanupProcessingMarkers removes temporary processing markers
func cleanupProcessingMarkers(span_ctx context.Context, accountsClient *config.PooledConnection, txHash string) {
	processingKey := fmt.Sprintf("tx_processing:%s", txHash)
	if exists, _ := DB_OPs.Exists(accountsClient, processingKey); exists {
		if err := DB_OPs.Create(accountsClient, processingKey, int64(-1)); err != nil {
			logger().NamedLogger.Warn(span_ctx, "Failed to clean up processing marker",
				ion.String("tx_hash", txHash),
				ion.String("error", err.Error()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.cleanupProcessingMarkers"),
			)
		}
	}

	// Also clean up the transaction lock
	cleanupTransactionLock(txHash)
}

// rollbackState restores all affected accounts to their pre-block snapshot atomically.
// It restores balance, TxNonce, and TxCountSent in a single write per account.
func rollbackState(span_ctx context.Context, snapshots map[common.Address]AccountSnapshot, accountsClient *config.PooledConnection) error {
	rollbackSpanCtx, rollbackSpan := logger().NamedLogger.Tracer("BlockProcessing").Start(span_ctx, "BlockProcessing.rollbackState")
	defer rollbackSpan.End()

	rollbackStartTime := time.Now().UTC()
	rollbackSpan.SetAttributes(attribute.Int("accounts_to_rollback", len(snapshots)))

	rollbackCount := 0
	for addr, snap := range snapshots {
		doc, err := DB_OPs.GetAccount(accountsClient, addr)
		if err != nil {
			// If it doesn't exist yet, we create an empty placeholder to zero it out
			doc = &DB_OPs.Account{Address: addr}
		}

		doc.Balance = snap.Balance
		doc.TxNonce = snap.TxNonce
		doc.TxCountSent = snap.TxCountSent
		doc.UpdatedAt = snap.UpdatedAt

		if err := DB_OPs.UpdateAccount(accountsClient, doc); err != nil {
			rollbackSpan.RecordError(err)
			rollbackSpan.SetAttributes(attribute.String("status", "partial_failure"), attribute.String("failed_account", addr.Hex()))
			logger().NamedLogger.Error(rollbackSpanCtx, "Failed to restore account state during rollback",
				err,
				ion.String("account", addr.Hex()),
				ion.String("original_balance", snap.Balance),
				ion.Uint64("original_tx_nonce", snap.TxNonce),
				ion.Uint64("original_tx_count_sent", snap.TxCountSent),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.rollbackState"),
			)
			return fmt.Errorf("failed to restore state for %s: %w", addr, err)
		}
		rollbackCount++
		logger().NamedLogger.Debug(rollbackSpanCtx, "Rolled back account state to original snapshot",
			ion.String("account", addr.Hex()),
			ion.String("balance", snap.Balance),
			ion.Uint64("tx_nonce", snap.TxNonce),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.rollbackState"),
		)
	}

	duration := time.Since(rollbackStartTime).Seconds()
	rollbackSpan.SetAttributes(
		attribute.Float64("duration", duration),
		attribute.String("status", "success"),
		attribute.Int("rolled_back_accounts", rollbackCount),
	)
	logger().NamedLogger.Info(rollbackSpanCtx, "Rollback completed successfully",
		ion.Int("rolled_back_accounts", rollbackCount),
		ion.Float64("duration", duration),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.rollbackState"),
	)
	return nil
}

// ProcessTransaction handles a single transaction's balance updates
func processTransaction(span_ctx context.Context, tx config.Transaction, coinbaseAddr common.Address, zkvmAddr common.Address, feeRecipients []config.FeeRecipient, accountsClient *config.PooledConnection, blockTimestamp int64) error {
	// Record trace span and close it
	txSpanCtx, txSpan := logger().NamedLogger.Tracer("BlockProcessing").Start(span_ctx, "BlockProcessing.processTransaction")
	defer txSpan.End()

	txStartTime := time.Now().UTC()
	txSpan.SetAttributes(
		attribute.String("tx_hash", tx.Hash.Hex()),
		attribute.String("from", tx.From.Hex()),
		attribute.String("to", tx.To.Hex()),
		attribute.String("coinbase", coinbaseAddr.Hex()),
		attribute.String("zkvm", zkvmAddr.Hex()),
	)

	// First check the connection
	if accountsClient == nil {
		txSpan.RecordError(errors.New("accountsClient is nil"))
		txSpan.SetAttributes(attribute.String("status", "error"))
		return errors.New("accountsClient is nil")
	}

	// Confirm the DB connection
	err := DB_OPs.EnsureDBConnection(accountsClient)
	if err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "db_connection_failed"))
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Failed to establish database connection",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("failed to establish database connection: %w", err)
	}

	logger().NamedLogger.Debug(txSpanCtx, "Database connection check successful",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.processTransaction"),
	)

	// Check if transaction was already processed (from previous blocks)
	txLock := getTransactionLock(tx.Hash.String())
	txLock.Lock()
	defer func() {
		txLock.Unlock()
		cleanupTransactionLock(tx.Hash.String()) // Always clean up the lock
	}()

	// First check with a preliminary key that shows we've started processing
	txProcessingKey := fmt.Sprintf("tx_processing:%s", tx.Hash)

	// Serialize this transaction's marker check → stage reads → atomic commit
	// with the reconciliation applier (DB_OPs.ApplyBlockRecon). Both writers
	// run read→modify→commit cycles on account documents; the shared lock
	// makes each cycle atomic with respect to the other, so an effect can
	// never be applied by both paths and neither can build on a base the
	// other is mid-way through changing.
	DB_OPs.LockStateApply()
	defer DB_OPs.UnlockStateApply()

	// Check if already completed. Dual-read value-aware guard (accountsdb
	// authoritative incl. -1 revocations, defaultdb legacy). Defense-in-depth
	// re-check under the tx lock — the block-level prefilter can be stale if a
	// concurrent path (PoTS replay) applied this tx after the prefilter ran.
	processed, err := DB_OPs.IsMarkerApplied(accountsClient, DB_OPs.TxProcessedKey(tx.Hash.String()))
	if err == nil && processed {
		txSpan.SetAttributes(attribute.String("status", "already_processed"))
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Info(txSpanCtx, "Transaction already processed in previous block, skipping",
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return nil
	}

	// Check if we're currently processing this transaction
	processing, err := DB_OPs.Exists(accountsClient, txProcessingKey)
	if err == nil && processing {
		// Get the timestamp to check if this is a stale marker
		valueBytes, getErr := DB_OPs.Read(accountsClient, txProcessingKey)
		if getErr == nil {
			// If processing marker is older than 5 minutes, consider it stale
			var timestamp int64
			if err := json.Unmarshal(valueBytes, &timestamp); err == nil {
				if time.Now().UTC().Unix()-timestamp > 300 {
					txSpan.SetAttributes(attribute.String("processing_marker_status", "stale"), attribute.Int64("stale_timestamp", timestamp))
					logger().NamedLogger.Warn(txSpanCtx, "Found stale processing marker, continuing with transaction",
						ion.String("tx_hash", tx.Hash.Hex()),
						ion.Int64("stale_timestamp", timestamp),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("topic", TOPIC),
						ion.String("function", "BlockProcessing.processTransaction"),
					)
				} else {
					txSpan.SetAttributes(attribute.String("processing_marker_status", "active"))
					logger().NamedLogger.Warn(txSpanCtx, "Transaction is already being processed, possible duplicate",
						ion.String("tx_hash", tx.Hash.Hex()),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("topic", TOPIC),
						ion.String("function", "BlockProcessing.processTransaction"),
					)
					// We have the lock, so continue processing anyway as previous attempt might have failed
				}
			}
		}
	}

	// Mark transaction as being processed
	if err := DB_OPs.Create(accountsClient, txProcessingKey, time.Now().UTC().Unix()); err != nil {
		logger().NamedLogger.Warn(txSpanCtx, "Failed to mark transaction as processing",
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("error", err.Error()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		// Continue processing since this is just a precaution
	}

	// Store original state for rollback if needed
	originalState := make(map[common.Address]AccountSnapshot)
	affectedDIDs := []common.Address{*tx.From, *tx.To, coinbaseAddr, zkvmAddr}

	for _, did := range affectedDIDs {
		doc, err := DB_OPs.GetAccount(accountsClient, did)
		if err == nil {
			originalState[did] = AccountSnapshot{
				Balance:     doc.Balance,
				TxNonce:     doc.TxNonce,
				TxCountSent: doc.TxCountSent,
				UpdatedAt:   doc.UpdatedAt,
			}
		} else if err == DB_OPs.ErrNotFound || strings.Contains(err.Error(), "key not found") {
			originalState[did] = AccountSnapshot{Balance: "0"}
		} else {
			txSpan.RecordError(err)
			txSpan.SetAttributes(attribute.String("status", "balance_retrieval_failed"), attribute.String("failed_account", did.Hex()))
			cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
			duration := time.Since(txStartTime).Seconds()
			txSpan.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(txSpanCtx, "Failed to retrieve original balance",
				err,
				ion.String("tx_hash", tx.Hash.Hex()),
				ion.String("account", did.Hex()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("topic", TOPIC),
				ion.String("function", "BlockProcessing.processTransaction"),
			)
			return fmt.Errorf("failed to retrieve original balance for %s: %w", did.Hex(), err)
		}
	}

	// Parse the transaction values
	var parsedTx *config.ParsedZKTransaction
	parsedTx, err = parseTransaction(tx)
	if err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "parse_failed"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Failed to parse transaction",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("failed to parse transaction: %w", err)
	}

	// Gas Limit is already a bigInt
	var gasLimit *big.Int
	if tx.GasLimit != 0 {
		gasLimit = big.NewInt(int64(tx.GasLimit))
	} else {
		gasLimit = big.NewInt(DefaultGasLimit)
	}

	// Calculate gas fee (gasLimit * gasPrice / 1,000,000,000)
	gasFeeToDeduct := new(big.Int).Mul(gasLimit, parsedTx.EffectiveGasFee)

	// Transaction value should remain in Wei for balance calculations
	// parsedTx.ValueBig is already in Wei, no conversion needed

	// Calculate total amount to deduct from sender (amount + gas fee)
	totalDeduction := new(big.Int).Add(parsedTx.ValueBig, gasFeeToDeduct)
	// Split the gas fee: floor(half) to the ZKVM, the coinbase-side share
	// distributed by config.SplitFee — a single credit to the coinbase address
	// when feeRecipients is empty (unchanged behavior), or weighted across the
	// recipients when set. coinbaseGasFee is the summed coinbase-side total, kept
	// for the trace attribute below.
	zkvmGasFee, coinbaseCredits := config.SplitFee(gasFeeToDeduct, coinbaseAddr, feeRecipients)
	coinbaseGasFee := new(big.Int)
	for _, c := range coinbaseCredits {
		coinbaseGasFee.Add(coinbaseGasFee, c.Amount)
	}

	txSpan.SetAttributes(
		attribute.String("value", parsedTx.ValueBig.String()),
		attribute.String("gas_limit", gasLimit.String()),
		attribute.String("gas_fee", gasFeeToDeduct.String()),
		attribute.String("total_deduction", totalDeduction.String()),
		attribute.String("coinbase_gas_fee", coinbaseGasFee.String()),
		attribute.String("zkvm_gas_fee", zkvmGasFee.String()),
	)

	logger().NamedLogger.Info(txSpanCtx, "Transaction Amount Calculated",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.String("from", tx.From.Hex()),
		ion.String("to", tx.To.Hex()),
		ion.String("value", parsedTx.ValueBig.String()),
		ion.String("gas_limit", gasLimit.String()),
		ion.String("gas_fee", gasFeeToDeduct.String()),
		ion.String("total_deduction", totalDeduction.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.processTransaction"),
	)

	// Check if sender exists before attempting deduction
	senderExists, _ := accountExists(tx.From, accountsClient)
	txSpan.SetAttributes(attribute.Bool("sender_exists", senderExists))
	if !senderExists {
		txSpan.RecordError(errors.New("sender DID does not exist"))
		txSpan.SetAttributes(attribute.String("status", "sender_not_found"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Sender DID does not exist",
			errors.New("sender DID does not exist"),
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("from", tx.From.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("sender DID %s does not exist", tx.From)
	}

	// Check if recipient exists (for better error reporting)
	recipientExists, _ := accountExists(tx.To, accountsClient)
	txSpan.SetAttributes(attribute.Bool("recipient_exists", recipientExists))
	if !recipientExists && !CreateMissingAccounts {
		txSpan.RecordError(errors.New("recipient DID does not exist"))
		txSpan.SetAttributes(attribute.String("status", "recipient_not_found"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Recipient DID does not exist",
			errors.New("recipient DID does not exist"),
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("to", tx.To.Hex()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("recipient DID %s does not exist and automatic creation is disabled", tx.To)
	}

	// All account mutations for this tx are STAGED in memory and then
	// committed in ONE accountsdb ExecAll together with the tx_processed marker
	// (ApplyTxAtomic below). Either the whole tx lands — balances AND marker —
	// or none of it does, so a crash cannot leave partially-applied
	// balances that a replay would double-apply.
	stage := newTxStage(accountsClient)

	// 1. Deduct from sender
	if err := deductFromSender(txSpanCtx, &tx, totalDeduction.String(), stage, blockTimestamp); err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "deduction_failed"), attribute.String("failed_step", "deduct_from_sender"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(txSpanCtx, "Failed to deduct from sender",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("from", tx.From.Hex()),
			ion.String("amount", totalDeduction.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return categorizeDeductionError(err)
	}

	txSpan.SetAttributes(attribute.String("deduction_step", "completed"))

	// 2. Add amount to recipient
	if err := addToRecipient(txSpanCtx, *tx.To, parsedTx.ValueBig.String(), stage, blockTimestamp); err != nil {
		// Remove nested rollback logic: parent loop will handle full block rollback via rollbackState
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "recipient_add_failed"), attribute.String("failed_step", "add_to_recipient"))

		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to add to recipient: %w", err)
	}

	txSpan.SetAttributes(attribute.String("recipient_add_step", "completed"))

	// 3. Credit the coinbase-side gas fee (one recipient by default, or the
	// weighted set when the block carries FeeRecipients), then the ZKVM.
	for _, c := range coinbaseCredits {
		if err := addToRecipient(txSpanCtx, c.Addr, c.Amount.String(), stage, blockTimestamp); err != nil {
			// Parent loop handles full block rollback via rollbackState.
			txSpan.RecordError(err)
			txSpan.SetAttributes(attribute.String("status", "coinbase_gas_fee_failed"), attribute.String("failed_step", "add_to_coinbase"))
			cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
			duration := time.Since(txStartTime).Seconds()
			txSpan.SetAttributes(attribute.Float64("duration", duration))
			return fmt.Errorf("failed to add gas fee to coinbase recipient %s: %w", c.Addr.Hex(), err)
		}
	}

	txSpan.SetAttributes(attribute.String("coinbase_gas_fee_step", "completed"))

	if err := addToRecipient(txSpanCtx, zkvmAddr, zkvmGasFee.String(), stage, blockTimestamp); err != nil {
		// Remove nested rollback logic: parent loop will handle full block rollback via rollbackState
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "zkvm_gas_fee_failed"), attribute.String("failed_step", "add_to_zkvm"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		duration := time.Since(txStartTime).Seconds()
		txSpan.SetAttributes(attribute.Float64("duration", duration))
		return fmt.Errorf("failed to add gas fee to ZKVM: %w", err)
	}

	txSpan.SetAttributes(attribute.String("zkvm_gas_fee_step", "completed"))

	// Commit the whole tx atomically — every staged account document
	// plus the tx_processed marker in ONE accountsdb ExecAll. Balances and
	// marker land together, so there are no applied-but-unmarked balances that
	// a replay could double-apply.
	//
	// Failure here means NOTHING was applied for this tx, so returning an error
	// is mandatory (the balances did not land either).
	if err := DB_OPs.ApplyTxAtomic(accountsClient, stage.staged(), tx.Hash.String(), time.Now().UTC().Unix()); err != nil {
		txSpan.RecordError(err)
		txSpan.SetAttributes(attribute.String("status", "atomic_commit_failed"))
		cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())
		logger().NamedLogger.Error(txSpanCtx, "Failed to atomically commit transaction (no effects applied)",
			err,
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.Int("staged_accounts", len(stage.staged())),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("topic", TOPIC),
			ion.String("function", "BlockProcessing.processTransaction"),
		)
		return fmt.Errorf("atomic tx commit failed for %s: %w", tx.Hash.Hex(), err)
	}

	// Clean up the processing marker
	cleanupProcessingMarkers(txSpanCtx, accountsClient, tx.Hash.String())

	duration := time.Since(txStartTime).Seconds()
	txSpan.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(txSpanCtx, "Transaction processed successfully",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.processTransaction"),
	)

	return nil
}

// accountExists checks if an account exists in the database
func accountExists(account *common.Address, accountsClient *config.PooledConnection) (bool, error) {
	fmt.Println("Checking if account exists: ", account.Hex()) // Debugging
	_, err := DB_OPs.GetAccount(accountsClient, *account)
	if err != nil {
		if err == DB_OPs.ErrNotFound || strings.Contains(err.Error(), "key not found") {
			fmt.Println("Account does not exist: ", account.Hex()) // Debugging
			return false, nil
		}
		fmt.Println("Error checking account existence: ", account.Hex(), "Error: ", err.Error()) // Debugging
		return false, err
	}
	fmt.Println("Account exists: ", account.Hex()) // Debugging
	return true, nil
}

// categorizeDeductionError provides more specific error types for deduction failures
func categorizeDeductionError(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	if contains(errStr, "insufficient balance") {
		return fmt.Errorf("insufficient funds: %w", err)
	} else if contains(errStr, "failed to retrieve sender DID") {
		return fmt.Errorf("account not found: %w", err)
	} else if contains(errStr, "invalid balance format") {
		return fmt.Errorf("account data corrupted: %w", err)
	}

	return fmt.Errorf("transaction failed: %w", err)
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr
}

// parseTransaction parses the numeric values in a transaction
func parseTransaction(tx config.Transaction) (*config.ParsedZKTransaction, error) {
	parsed := &config.ParsedZKTransaction{
		Original: &tx,
	}

	// Set the value directly since it's already a *big.Int
	if tx.Value != nil {
		parsed.ValueBig = new(big.Int).Set(tx.Value)
	} else {
		parsed.ValueBig = big.NewInt(0)
	}

	// Determine gas fee based on transaction type.
	// Type 0x0 = Legacy, 0x1 = AccessList, 0x2 = DynamicFee (EIP-1559).
	// The formula lives in config.EffectiveGasPrice — the single source of truth
	// shared with FastsyncV2 delta reconciliation. Do NOT inline fee logic here.
	parsed.EffectiveGasFee = config.EffectiveGasPrice(tx.Type, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee)

	// Execution-level assertion (defense in depth). The ingress and
	// remote-admission gates (Security.CheckTransactionValues) already reject
	// negative fields, but execution must never apply a negative amount: a
	// negative ValueBig or EffectiveGasFee would invert the balance arithmetic
	// (sender credited, receiver debited). Fail closed here so a tx that reaches
	// execution with a negative field cannot mutate balances.
	if parsed.ValueBig != nil && parsed.ValueBig.Sign() < 0 {
		return nil, fmt.Errorf("negative transaction value in execution: %s", parsed.ValueBig.String())
	}
	if parsed.EffectiveGasFee != nil && parsed.EffectiveGasFee.Sign() < 0 {
		return nil, fmt.Errorf("negative effective gas fee in execution: %s", parsed.EffectiveGasFee.String())
	}

	return parsed, nil
}

// deductFromSender validates and STAGES the sender-side deduction (no DB
// write here — the mutation commits atomically with the rest of the tx via
// ApplyTxAtomic in processTransaction).
func deductFromSender(span_ctx context.Context, tx *config.Transaction, amount string, stage *txStage, blockTimestamp int64) error {
	fromDID := *tx.From
	// Read-through the stage: sees earlier mutations of this same tx.
	didDoc, err := stage.get(fromDID)
	if err != nil {
		return fmt.Errorf("failed to retrieve sender DID %s: %w", fromDID, err)
	}

	// Parse current balance
	currentBalance, ok := new(big.Int).SetString(didDoc.Balance, 10)
	if !ok {
		return fmt.Errorf("invalid balance format for DID %s: %s", fromDID, didDoc.Balance)
	}

	// Foolproof execution-time nonce check (prevents same-block replay).
	// Returns ErrStaleNonce so the caller can skip this tx rather than rolling
	// back the entire block — the tx was valid at security-check time but the
	// account nonce advanced before ProcessBlockLocally ran.
	if tx.Nonce < didDoc.TxNonce {
		return fmt.Errorf("%w: submitted nonce %d is lower than account's current DB nonce %d", ErrStaleNonce, tx.Nonce, didDoc.TxNonce)
	}

	// Parse amount to deduct
	deductAmount, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return fmt.Errorf("invalid deduction amount: %s", amount)
	}
	// A negative deduction would ADD to the sender (balance - (-x)). Reject.
	if deductAmount.Sign() < 0 {
		return fmt.Errorf("negative deduction amount for DID %s: %s", fromDID, amount)
	}

	// Check if sufficient balance
	if currentBalance.Cmp(deductAmount) < 0 {
		return fmt.Errorf("insufficient balance for DID %s: has %s, needs %s",
			fromDID, currentBalance.String(), deductAmount.String())
	}

	// Calculate new balance
	newBalance := new(big.Int).Sub(currentBalance, deductAmount)

	// Update balance, TxNonce, and TxCountSent sequentially using the fetched doc
	didDoc.Balance = newBalance.String()
	didDoc.TxNonce = tx.Nonce + 1
	didDoc.TxCountSent = didDoc.TxCountSent + 1
	// LWW timestamp in NANOSECONDS at the source: blockTimestamp is Unix
	// seconds — storing it raw made every nano-stamped sync write beat later
	// live writes by 9 orders of magnitude. Block-timestamp-derived
	// (not wall-clock) so all nodes stamp identical values for the same block.
	// normalizeUpdatedAtNanos remains the compare-time safety net for legacy rows.
	didDoc.UpdatedAt = blockTimestamp * int64(time.Second)

	// Stage only — committed atomically with the tx marker in ApplyTxAtomic.
	stage.put(didDoc)

	logger().NamedLogger.Debug(span_ctx, "Deducted amount from sender and updated state",
		ion.String("account", fromDID.String()),
		ion.String("amount", amount),
		ion.String("old_balance", currentBalance.String()),
		ion.String("new_balance", newBalance.String()),
		ion.Uint64("new_nonce", tx.Nonce+1),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.deductFromSender"),
	)

	return nil
}

// addToRecipient validates and STAGES a credit (no DB write here — commits
// atomically with the rest of the tx via ApplyTxAtomic in processTransaction).
// blockTimestamp is used as updatedAt to keep account state deterministic across nodes.
func addToRecipient(span_ctx context.Context, ToAddress common.Address, amount string, stage *txStage, blockTimestamp int64) error {
	// Read-through the stage: credits to an account already touched by this tx
	// (self-send, sender==coinbase, ...) accumulate on the staged document.
	didDoc, err := stage.get(ToAddress)
	if err != nil {
		return fmt.Errorf("failed to retrieve recipient DID %s (account must exist before transfer): %w", ToAddress, err)
	}

	// Parse current balance
	currentBalance, ok := new(big.Int).SetString(didDoc.Balance, 10)
	if !ok {
		return fmt.Errorf("invalid balance format for DID %s: %s", ToAddress, didDoc.Balance)
	}

	// Parse amount to add
	addAmount, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return fmt.Errorf("invalid addition amount: %s", amount)
	}
	// A negative credit would DEBIT the receiver (balance + (-x)). Reject.
	if addAmount.Sign() < 0 {
		return fmt.Errorf("negative credit amount for DID %s: %s", ToAddress, amount)
	}

	// Calculate new balance
	newBalance := new(big.Int).Add(currentBalance, addAmount)

	// Update the balance and timestamp sequentially using the fetched doc
	didDoc.Balance = newBalance.String()
	// LWW timestamp in NANOSECONDS at the source: blockTimestamp is Unix
	// seconds — storing it raw made every nano-stamped sync write beat later
	// live writes by 9 orders of magnitude. Block-timestamp-derived
	// (not wall-clock) so all nodes stamp identical values for the same block.
	// normalizeUpdatedAtNanos remains the compare-time safety net for legacy rows.
	didDoc.UpdatedAt = blockTimestamp * int64(time.Second)

	// Stage only — committed atomically with the tx marker in ApplyTxAtomic.
	stage.put(didDoc)

	logger().NamedLogger.Debug(span_ctx, "Added amount to recipient",
		ion.String("account", ToAddress.String()),
		ion.String("amount", amount),
		ion.String("old_balance", currentBalance.String()),
		ion.String("new_balance", newBalance.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.addToRecipient"),
	)

	return nil
}
