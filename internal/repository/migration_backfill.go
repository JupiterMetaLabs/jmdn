package repository

import (
	"context"
	"fmt"
	"gossipnode/DB_OPs"
	"strings"
	"time"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/ethereum/go-ethereum/common"
)

// BackfillWorker orchestrates migrating historical data from ImmuDB to ThebeDB.
type BackfillWorker struct {
	source CoordinatorRepository // ImmuDB reader
	target CoordinatorRepository // ThebeDB writer
	config Config
	state  *StateTracker

	// onProgress is called after each successfully migrated block.
	// current is the block number just written; errCount is the running soft-error total.
	// Nil-safe — leave unset for standalone use.
	onProgress func(current uint64, errCount int)

	// onError is called when a non-fatal error is recorded (fetch failure, store skip, etc.).
	// Nil-safe.
	onError func(msg string)
}

// NewBackfillWorker instantiates the background auto-backfill engine.
func NewBackfillWorker(
	source CoordinatorRepository,
	target CoordinatorRepository,
	thebeInstance *thebedb.ThebeDB,
	cfg Config,
) *BackfillWorker {
	return &BackfillWorker{
		source: source,
		target: target,
		config: cfg,
		state:  NewStateTracker(thebeInstance),
	}
}

// Run executes the backfill process and returns the first fatal error encountered,
// or nil on clean completion. Context cancellation is not an error — callers should
// check ctx.Err() separately if they need to distinguish stop vs. failure.
func (w *BackfillWorker) Run(ctx context.Context) error {
	if !w.config.Enabled {
		return nil
	}

	// 1. Ensure SQL Schema is present
	if err := w.state.EnsureSchema(ctx); err != nil {
		fmt.Printf("[Migration Warning] Failed to ensure schema: %v\n", err)
	}

	// 2. Migrate Blocks and nested transactions
	if w.config.MigrateBlocks {
		if err := w.migrateBlocks(ctx); err != nil {
			fmt.Printf("[Migration Error] Block migration aborted: %v\n", err)
			return err
		}
	}

	// 3. Migrate Accounts
	if w.config.MigrateAccounts {
		if err := w.migrateAccounts(ctx); err != nil {
			fmt.Printf("[Migration Error] Account migration aborted: %v\n", err)
			return err
		}
	}

	return nil
}

func (w *BackfillWorker) migrateBlocks(ctx context.Context) error {
	// Find head of ImmuDB
	targetHead, err := w.source.GetLatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("failed to get immu head: %w", err)
	}

	// Find where we left off via postgres state
	lastSynced, err := w.state.LastSyncedBlock(ctx)
	if err != nil {
		return fmt.Errorf("failed to get last synced block: %w", err)
	}

	// Note: since ImmuDB genesis is 0, we must handle block 0 specially if lastSynced returned 0 on empty table.
	// LastSyncedBlock naturally returns 0 string for empty table. But 0 is valid.
	startBlock := lastSynced + 1
	if lastSynced == 0 {
		startBlock = 0 // start from genesis if nothing was found
	}

	if startBlock > targetHead && targetHead > 0 {
		fmt.Printf("[Migration] ThebeDB blocks fully synced up to %d.\n", targetHead)
		return nil
	}

	fmt.Printf("[Migration] Auto-Backfilling blocks %d to %d\n", startBlock, targetHead)

	var batchCount, errCount int
	for current := startBlock; current <= targetHead; current++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		block, err := w.source.GetZKBlockByNumber(ctx, current)
		if err != nil {
			errCount++
			msg := fmt.Sprintf("failed to fetch block %d from ImmuDB: %v", current, err)
			fmt.Printf("[Migration Warning] %s\n", msg)
			if w.onError != nil {
				w.onError(msg)
			}
			continue
		}

		if block == nil {
			continue
		}

		// StoreZKBlock cascades and atomic-inserts the ZKBlock, all Transactions, and ZK_Proofs directly to SQL.
		// Idempotent with ON CONFLICT DO NOTHING
		if err := w.target.StoreZKBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to store block %d to thebedb: %w", current, err)
		}

		if w.onProgress != nil {
			w.onProgress(current, errCount)
		}

		batchCount++
		if batchCount >= w.config.MaxBlocksPerBatch {
			time.Sleep(w.config.ThrottleDuration)
			batchCount = 0
		}
	}

	fmt.Printf("[Migration] Block backfill complete up to %d\n", targetHead)
	return nil
}

func (w *BackfillWorker) migrateAccounts(ctx context.Context) error {
	fmt.Printf("[Migration] Starting Account backfill\n")

	// Get all keys with 'account:' prefix. Using high limit to ensure we hit them all.
	// Since CoordinatorRepository doesn't expose key scans, we invoke DB_OPs.
	keys, err := DB_OPs.GetKeys(nil, "account:", 1000000)
	if err != nil {
		return fmt.Errorf("failed to fetch account keys from immuDB: %w", err)
	}

	fmt.Printf("[Migration] Found %d accounts in ImmuDB\n", len(keys))

	var batchCount int
	for _, key := range keys {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		addrHex := strings.TrimPrefix(key, "account:")
		addr := common.HexToAddress(addrHex)

		account, err := w.source.GetAccount(ctx, addr)
		if err != nil {
			fmt.Printf("[Migration Warning] Missed account DB read %s: %v\n", addrHex, err)
			continue
		}

		if account == nil {
			continue
		}

		if err := w.target.StoreAccount(ctx, account); err != nil {
			fmt.Printf("[Migration Error] Failed to store account %s in thebedb: %v\n", addrHex, err)
			continue
		}

		batchCount++
		if batchCount >= w.config.MaxAccountsPerBatch {
			time.Sleep(w.config.ThrottleDuration)
			batchCount = 0
		}
	}

	fmt.Printf("[Migration] Account backfill complete.\n")
	return nil
}
