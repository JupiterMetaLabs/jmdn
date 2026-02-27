package kv_repo

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	log "gossipnode/logging"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/cockroachdb/pebble"
	"github.com/ethereum/go-ethereum/common"
	"go.opentelemetry.io/otel/attribute"
)

// ================================================================
// Key Encoding Conventions
// ================================================================
//
// Key prefixes are imported from DB_OPs.DBConstants to ensure PebbleDB
// uses the exact same key format as ImmuDB during migration:
//
//   DB_OPs.Prefix + <0xAddress>            → JSON(Account)      ("address:<0xAddr>")
//   DB_OPs.DIDPrefix + <did_string>        → <0xAddress>        ("did:<did>")
//   DB_OPs.PREFIX_BLOCK + <number>         → JSON(ZKBlock)      ("block:<num>")
//   DB_OPs.PREFIX_BLOCK_HASH + <0xHash>    → uint64 BE          ("block:hash:<hash>")
//   keyLatestBlock                         → uint64 BE          ("latest_block")
//   DB_OPs.DEFAULT_PREFIX_TX + <0xHash>    → JSON(Transaction)  ("tx:<hash>")
//

const (
	keyLatestBlock = "latest_block"
	tracerNameKV   = "KVRepo"
)

// kvLogger returns the *ion.Ion instance for KV repo tracing.
func kvLogger() *ion.Ion {
	l, err := log.NewAsyncLogger().Get().NamedLogger(log.DBCoordinator, "")
	if err != nil {
		return nil
	}
	return l.GetNamedLogger()
}

// ================================================================
// PebbleRepository
// ================================================================

// PebbleRepository implements the CoordinatorRepository interface using PebbleDB.
type PebbleRepository struct {
	db *pebble.DB
}

// NewPebbleRepository creates a new PebbleDB-backed repository.
func NewPebbleRepository(db *pebble.DB) *PebbleRepository {
	return &PebbleRepository{db: db}
}

// ================================================================
// Account Writes
// ================================================================

// StoreAccount atomically writes the account data and its DID index (if present)
// using a PebbleDB Batch. Either both keys are written or neither is.
func (r *PebbleRepository) StoreAccount(ctx context.Context, account *DB_OPs.Account) error {
	logger := kvLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameKV).Start(ctx, "kv.StoreAccount")
		defer span.End()
		span.SetAttributes(attribute.String("address", account.Address.Hex()))
	}

	start := time.Now()

	data, err := json.Marshal(account)
	if err != nil {
		return fmt.Errorf("kv_repo.StoreAccount: marshal: %w", err)
	}

	batch := r.db.NewBatch()
	defer batch.Close()

	// Primary key: account:<address>
	key := DB_OPs.Prefix + account.Address.Hex()
	if err := batch.Set([]byte(key), data, nil); err != nil {
		return fmt.Errorf("kv_repo.StoreAccount: batch set primary: %w", err)
	}

	// Secondary index: did:<did> → address (for DID-based lookups later)
	if account.DIDAddress != "" {
		didKey := DB_OPs.DIDPrefix + account.DIDAddress
		if err := batch.Set([]byte(didKey), []byte(account.Address.Hex()), nil); err != nil {
			return fmt.Errorf("kv_repo.StoreAccount: batch set DID index: %w", err)
		}
	}

	// Atomic commit — both keys written or neither
	if err := batch.Commit(pebble.Sync); err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("kv_repo.StoreAccount: batch commit: %w", err)
	}

	if span != nil {
		span.SetAttributes(
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
			attribute.Int("batch_size", 2),
		)
	}

	_ = ctx
	return nil
}

// UpdateAccountBalance atomically reads the current account, updates the balance,
// and writes it back using a PebbleDB Batch.
//
// Note: PebbleDB does not support row-level locking. In a high-concurrency
// scenario, concurrent UpdateAccountBalance calls on the same address could
// still race. This is acceptable because:
//   - Balance updates in this system are serialized at the block-processing level
//   - PebbleDB is a secondary store; PostgreSQL is the source of truth for balances
func (r *PebbleRepository) UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error {
	logger := kvLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameKV).Start(ctx, "kv.UpdateAccountBalance")
		defer span.End()
		span.SetAttributes(attribute.String("address", address.Hex()))
	}

	start := time.Now()

	key := []byte(DB_OPs.Prefix + address.Hex())

	// Read current account
	val, closer, err := r.db.Get(key)
	if err != nil {
		return fmt.Errorf("kv_repo.UpdateAccountBalance: get: %w", err)
	}

	// Copy the value before closing — PebbleDB's val slice is only valid until closer.Close()
	valCopy := make([]byte, len(val))
	copy(valCopy, val)
	closer.Close()

	var account DB_OPs.Account
	if err := json.Unmarshal(valCopy, &account); err != nil {
		return fmt.Errorf("kv_repo.UpdateAccountBalance: unmarshal: %w", err)
	}

	// Update balance and re-write
	account.Balance = newBalance

	data, err := json.Marshal(account)
	if err != nil {
		return fmt.Errorf("kv_repo.UpdateAccountBalance: marshal: %w", err)
	}

	if err := r.db.Set(key, data, pebble.Sync); err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("kv_repo.UpdateAccountBalance: set: %w", err)
	}

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
	}

	_ = ctx
	return nil
}

// ================================================================
// Block Writes
// ================================================================

// StoreZKBlock atomically writes the block data, hash index, and latest_block
// tracker using a PebbleDB Batch. All 3 keys are committed together.
//
// The latest_block tracker is only updated if the new block number is strictly
// greater than the current value, preventing regressions from out-of-order blocks.
func (r *PebbleRepository) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	logger := kvLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameKV).Start(ctx, "kv.StoreZKBlock")
		defer span.End()
		span.SetAttributes(
			attribute.Int64("block_number", int64(block.BlockNumber)),
			attribute.String("block_hash", block.BlockHash.Hex()),
			attribute.Int("tx_count", len(block.Transactions)),
		)
	}

	start := time.Now()

	data, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("kv_repo.StoreZKBlock: marshal: %w", err)
	}

	batch := r.db.NewBatch()
	defer batch.Close()

	// Primary key: block:<number>
	blockKey := fmt.Sprintf("%s%d", DB_OPs.PREFIX_BLOCK, block.BlockNumber)
	if err := batch.Set([]byte(blockKey), data, nil); err != nil {
		return fmt.Errorf("kv_repo.StoreZKBlock: batch set primary: %w", err)
	}

	// Hash index: block:hash:<hash> → block number (8 bytes big-endian)
	hashKey := DB_OPs.PREFIX_BLOCK_HASH + block.BlockHash.Hex()
	numBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(numBytes, block.BlockNumber)
	if err := batch.Set([]byte(hashKey), numBytes, nil); err != nil {
		return fmt.Errorf("kv_repo.StoreZKBlock: batch set hash index: %w", err)
	}

	// Tracker: latest_block — only update if this block is newer
	batchSize := 2
	shouldUpdateLatest := true
	existing, closer, err := r.db.Get([]byte(keyLatestBlock))
	if err == nil && len(existing) == 8 {
		currentMax := binary.BigEndian.Uint64(existing)
		if block.BlockNumber <= currentMax {
			shouldUpdateLatest = false
		}
		closer.Close()
	} else if err != nil && err != pebble.ErrNotFound {
		return fmt.Errorf("kv_repo.StoreZKBlock: read latest_block: %w", err)
	} else if err == nil {
		closer.Close()
	}

	if shouldUpdateLatest {
		batchSize = 3
		if err := batch.Set([]byte(keyLatestBlock), numBytes, nil); err != nil {
			return fmt.Errorf("kv_repo.StoreZKBlock: batch set latest_block: %w", err)
		}
	}

	// Atomic commit — all keys written together or none
	if err := batch.Commit(pebble.Sync); err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("kv_repo.StoreZKBlock: batch commit: %w", err)
	}

	if span != nil {
		span.SetAttributes(
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
			attribute.Int("batch_size", batchSize),
		)
	}

	_ = ctx
	return nil
}

// ================================================================
// Transaction Writes
// ================================================================

func (r *PebbleRepository) StoreTransaction(ctx context.Context, tx interface{}) error {
	logger := kvLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameKV).Start(ctx, "kv.StoreTransaction")
		defer span.End()
	}

	start := time.Now()

	t, ok := tx.(*config.Transaction)
	if !ok {
		return fmt.Errorf("kv_repo.StoreTransaction: unsupported transaction type")
	}

	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("kv_repo.StoreTransaction: marshal: %w", err)
	}

	key := DB_OPs.DEFAULT_PREFIX_TX + t.Hash.Hex()
	if err := r.db.Set([]byte(key), data, pebble.Sync); err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("kv_repo.StoreTransaction: set: %w", err)
	}

	if span != nil {
		span.SetAttributes(
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
			attribute.String("tx_hash", t.Hash.Hex()),
		)
	}

	_ = ctx
	return nil
}

// ================================================================
// Read Stubs (writes-only phase — all reads still go through ImmuDB)
// ================================================================

func (r *PebbleRepository) GetAccount(_ context.Context, _ common.Address) (*DB_OPs.Account, error) {
	return nil, nil
}

func (r *PebbleRepository) GetAccountByDID(_ context.Context, _ string) (*DB_OPs.Account, error) {
	return nil, nil
}

func (r *PebbleRepository) GetZKBlockByNumber(_ context.Context, _ uint64) (*config.ZKBlock, error) {
	return nil, nil
}

func (r *PebbleRepository) GetZKBlockByHash(_ context.Context, _ string) (*config.ZKBlock, error) {
	return nil, nil
}

func (r *PebbleRepository) GetLatestBlockNumber(_ context.Context) (uint64, error) {
	return 0, nil
}

func (r *PebbleRepository) GetLogs(_ context.Context, _ Types.FilterQuery) ([]Types.Log, error) {
	return nil, nil
}

func (r *PebbleRepository) GetTransactionByHash(_ context.Context, _ string) (*config.Transaction, error) {
	return nil, nil
}
