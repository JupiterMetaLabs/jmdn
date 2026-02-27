package sql_repo

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	log "gossipnode/logging"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
)

// ================================================================
// SQL Queries (sourced from sqlMigratoin.md)
// ================================================================

const queryInsertAccount = `
INSERT INTO accounts
  (address, did_address, balance_wei, nonce, account_type, metadata, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (address) DO UPDATE SET
  did_address   = EXCLUDED.did_address,
  balance_wei   = EXCLUDED.balance_wei,
  nonce         = EXCLUDED.nonce,
  account_type  = EXCLUDED.account_type,
  metadata      = EXCLUDED.metadata,
  updated_at    = EXCLUDED.updated_at`

const queryUpdateBalance = `
UPDATE accounts
SET balance_wei = $1, updated_at = $2
WHERE address = $3`

const queryInsertBlock = `
INSERT INTO blocks
  (block_number, block_hash, parent_hash, timestamp, txs_root, state_root,
   logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (block_number) DO NOTHING`

const queryInsertTransaction = `
INSERT INTO transactions
  (tx_hash, block_number, tx_index, from_addr, to_addr, value_wei, nonce, type,
   gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei,
   data, access_list, sig_v, sig_r, sig_s, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (tx_hash) DO NOTHING`

const tracerNameSQL = "SQLRepo"

// sqlLogger returns the *ion.Ion instance for SQL repo tracing.
func sqlLogger() *ion.Ion {
	l, err := log.NewAsyncLogger().Get().NamedLogger(log.DBCoordinator, "")
	if err != nil {
		return nil
	}
	return l.GetNamedLogger()
}

// ================================================================
// SQLRepository
// ================================================================

// SQLRepository implements the CoordinatorRepository interface using raw pgx.
type SQLRepository struct {
	pool *pgxpool.Pool
}

// NewSQLRepository creates a new SQL-backed repository.
func NewSQLRepository(pool *pgxpool.Pool) *SQLRepository {
	return &SQLRepository{pool: pool}
}

// ================================================================
// Account Writes
// ================================================================

func (r *SQLRepository) StoreAccount(ctx context.Context, account *DB_OPs.Account) error {
	logger := sqlLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameSQL).Start(ctx, "sql.StoreAccount")
		defer span.End()
		span.SetAttributes(attribute.String("address", account.Address.Hex()))
	}

	start := time.Now()

	metadataJSON, err := json.Marshal(account.Metadata)
	if err != nil {
		return fmt.Errorf("sql_repo.StoreAccount: failed to marshal metadata: %w", err)
	}

	_, err = r.pool.Exec(ctx, queryInsertAccount,
		account.Address.Hex(), // $1 address
		account.DIDAddress,    // $2 did_address
		account.Balance,       // $3 balance_wei
		account.Nonce,         // $4 nonce
		account.AccountType,   // $5 account_type
		metadataJSON,          // $6 metadata (jsonb)
		account.CreatedAt,     // $7 created_at
		account.UpdatedAt,     // $8 updated_at
	)
	if err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("sql_repo.StoreAccount: %w", err)
	}

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
	}
	return nil
}

func (r *SQLRepository) UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error {
	logger := sqlLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameSQL).Start(ctx, "sql.UpdateAccountBalance")
		defer span.End()
		span.SetAttributes(attribute.String("address", address.Hex()))
	}

	start := time.Now()

	_, err := r.pool.Exec(ctx, queryUpdateBalance,
		newBalance,        // $1 balance_wei
		time.Now().Unix(), // $2 updated_at
		address.Hex(),     // $3 address
	)
	if err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("sql_repo.UpdateAccountBalance: %w", err)
	}

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
	}
	return nil
}

// ================================================================
// Block Writes
// ================================================================

func (r *SQLRepository) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	logger := sqlLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameSQL).Start(ctx, "sql.StoreZKBlock")
		defer span.End()
		span.SetAttributes(
			attribute.Int64("block_number", int64(block.BlockNumber)),
			attribute.String("block_hash", block.BlockHash.Hex()),
			attribute.Int("tx_count", len(block.Transactions)),
		)
	}

	start := time.Now()

	// Use a SQL transaction to ensure block + all transactions are atomic.
	// If any insert fails, the entire batch is rolled back.
	pgTx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sql_repo.StoreZKBlock: begin tx: %w", err)
	}
	defer pgTx.Rollback(ctx) // no-op after commit

	// Convert optional addresses to string or nil
	var coinbaseAddr, zkvmAddr *string
	if block.CoinbaseAddr != nil {
		s := block.CoinbaseAddr.Hex()
		coinbaseAddr = &s
	}
	if block.ZKVMAddr != nil {
		s := block.ZKVMAddr.Hex()
		zkvmAddr = &s
	}

	extraDataJSON, err := json.Marshal(block.ExtraData)
	if err != nil {
		return fmt.Errorf("sql_repo.StoreZKBlock: failed to marshal extra_data: %w", err)
	}

	_, err = pgTx.Exec(ctx, queryInsertBlock,
		block.BlockNumber,                 // $1  block_number
		block.BlockHash.Hex(),             // $2  block_hash
		block.PrevHash.Hex(),              // $3  parent_hash
		block.Timestamp,                   // $4  timestamp
		block.TxnsRoot,                    // $5  txs_root
		block.StateRoot.Hex(),             // $6  state_root
		block.LogsBloom,                   // $7  logs_bloom (bytea)
		coinbaseAddr,                      // $8  coinbase_addr
		zkvmAddr,                          // $9  zkvm_addr
		fmt.Sprintf("%d", block.GasLimit), // $10 gas_limit
		fmt.Sprintf("%d", block.GasUsed),  // $11 gas_used
		block.Status,                      // $12 status
		extraDataJSON,                     // $13 extra_data (jsonb)
		time.Now().Unix(),                 // $14 created_at
	)
	if err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("sql_repo.StoreZKBlock: insert block: %w", err)
	}

	// Store each transaction inside the same SQL transaction
	for i, tx := range block.Transactions {
		if err := r.storeBlockTransactionInTx(ctx, pgTx, block.BlockNumber, i, &tx); err != nil {
			if span != nil {
				span.RecordError(err)
			}
			return fmt.Errorf("sql_repo.StoreZKBlock: tx %s: %w", tx.Hash.Hex(), err)
		}
	}

	// Commit atomically
	if err := pgTx.Commit(ctx); err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("sql_repo.StoreZKBlock: commit: %w", err)
	}

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
	}

	return nil
}

// txQuerier is satisfied by both pgxpool.Pool and pgx.Tx, allowing
// storeBlockTransactionInTx to work inside or outside a SQL transaction.
type txQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// storeBlockTransactionInTx inserts a single transaction using the provided querier
// (either a pgx.Tx for transactional writes, or pool for standalone writes).
func (r *SQLRepository) storeBlockTransactionInTx(ctx context.Context, q txQuerier, blockNumber uint64, txIndex int, tx *config.Transaction) error {
	var fromAddr, toAddr *string
	if tx.From != nil {
		s := tx.From.Hex()
		fromAddr = &s
	}
	if tx.To != nil {
		s := tx.To.Hex()
		toAddr = &s
	}

	// Convert big.Int fields to string safely
	valueWei := "0"
	if tx.Value != nil {
		valueWei = tx.Value.String()
	}
	gasPriceWei := ""
	if tx.GasPrice != nil {
		gasPriceWei = tx.GasPrice.String()
	}
	maxFeeWei := ""
	if tx.MaxFee != nil {
		maxFeeWei = tx.MaxFee.String()
	}
	maxPriorityFeeWei := ""
	if tx.MaxPriorityFee != nil {
		maxPriorityFeeWei = tx.MaxPriorityFee.String()
	}

	accessListJSON, err := json.Marshal(tx.AccessList)
	if err != nil {
		return fmt.Errorf("failed to marshal access_list: %w", err)
	}

	// Signature components
	sigV := int16(0)
	if tx.V != nil {
		sigV = int16(tx.V.Int64())
	}
	sigR := ""
	if tx.R != nil {
		sigR = fmt.Sprintf("0x%064x", tx.R)
	}
	sigS := ""
	if tx.S != nil {
		sigS = fmt.Sprintf("0x%064x", tx.S)
	}

	_, err = q.Exec(ctx, queryInsertTransaction,
		tx.Hash.Hex(),                  // $1  tx_hash
		blockNumber,                    // $2  block_number
		txIndex,                        // $3  tx_index
		fromAddr,                       // $4  from_addr
		toAddr,                         // $5  to_addr
		valueWei,                       // $6  value_wei
		fmt.Sprintf("%d", tx.Nonce),    // $7  nonce
		int16(tx.Type),                 // $8  type
		fmt.Sprintf("%d", tx.GasLimit), // $9  gas_limit
		gasPriceWei,                    // $10 gas_price_wei
		maxFeeWei,                      // $11 max_fee_wei
		maxPriorityFeeWei,              // $12 max_priority_fee_wei
		tx.Data,                        // $13 data (bytea)
		accessListJSON,                 // $14 access_list (jsonb)
		sigV,                           // $15 sig_v
		sigR,                           // $16 sig_r
		sigS,                           // $17 sig_s
		time.Now().Unix(),              // $18 created_at
	)
	if err != nil {
		return fmt.Errorf("sql_repo: insert tx: %w", err)
	}

	return nil
}

// ================================================================
// Transaction Writes
// ================================================================

func (r *SQLRepository) StoreTransaction(ctx context.Context, tx interface{}) error {
	logger := sqlLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameSQL).Start(ctx, "sql.StoreTransaction")
		defer span.End()
	}

	t, ok := tx.(*config.Transaction)
	if !ok {
		return fmt.Errorf("sql_repo.StoreTransaction: unsupported transaction type")
	}
	// Store as a standalone transaction (block_number = 0, tx_index = 0 for unconfirmed)
	err := r.storeBlockTransactionInTx(ctx, r.pool, 0, 0, t)
	if err != nil && span != nil {
		span.RecordError(err)
	}
	return err
}

// ================================================================
// Read Stubs (writes-only phase — all reads still go through ImmuDB)
// ================================================================

func (r *SQLRepository) GetAccount(_ context.Context, _ common.Address) (*DB_OPs.Account, error) {
	return nil, nil
}

func (r *SQLRepository) GetAccountByDID(_ context.Context, _ string) (*DB_OPs.Account, error) {
	return nil, nil
}

func (r *SQLRepository) GetZKBlockByNumber(_ context.Context, _ uint64) (*config.ZKBlock, error) {
	return nil, nil
}

func (r *SQLRepository) GetZKBlockByHash(_ context.Context, _ string) (*config.ZKBlock, error) {
	return nil, nil
}

func (r *SQLRepository) GetLatestBlockNumber(_ context.Context) (uint64, error) {
	return 0, nil
}

func (r *SQLRepository) GetLogs(_ context.Context, _ Types.FilterQuery) ([]Types.Log, error) {
	return nil, nil
}

func (r *SQLRepository) GetTransactionByHash(_ context.Context, _ string) (*config.Transaction, error) {
	return nil, nil
}
