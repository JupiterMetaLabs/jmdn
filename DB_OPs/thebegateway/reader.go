// MODULE: DB_OPs/thebegateway/reader.go
// PURPOSE: Concrete ThebeReader — read-through cache over PostgreSQL projection.
//          Cache hit → return. Miss → SQL query → cache SET with TTL → return.
//          Phase 7: contract code/nonce/storage/meta read directly from BadgerDB KV via ThebeKVStore.
//          Contract receipts use the standard SQL read-through cache pattern.
//
// CORE DATA STRUCTURES:
//   - thebeReader: holds sqlQuerier (interface) + ThebeKVStore (interface) + cache.Cache (interface, may be nil).
//     Stateless per-call. Safe for concurrent use.
//   - sqlQuerier: local interface over *sql.DB — accepts QueryRowContext + QueryContext.
//     Allows test injection of mock DB without real PostgreSQL.
//
// TO MODIFY BEHAVIOR:
//   - Change SQL for a method: edit the corresponding sqlGet* constant
//   - Add new read method: add to ThebeReader interface + implement read() pattern here
//   - Swap cache backend: inject different cache.Cache — reader unchanged
//
// DO NOT:
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Use fmt.Sprintf for SQL queries (parameterized only)
//   - Ignore rows.Err() after scan loops
//
// EXTENSION POINT: new entity read → new sqlGet* constant + method following read() pattern
//
// CHANGE SCENARIOS:
//   Phase 7 contract reads: DONE — GetContractCode/Nonce/Storage/Meta use KV; GetContractReceipt uses SQL
//   Disable cache: inject nil cache.Cache — reader falls through to SQL always

package thebegateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JupiterMetaLabs/ThebeDB/pkg/cache"
	"github.com/lib/pq"
)

// sqlQuerier is the minimal *sql.DB surface used by thebeReader.
// *sql.DB satisfies this interface.
type sqlQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// scanner abstracts *sql.Row and *sql.Rows so scan helpers work for both single and multi-row queries.
type scanner interface {
	Scan(dest ...any) error
}

type thebeReader struct {
	db    sqlQuerier
	kv    ThebeKVStore
	cache cache.Cache // nil = cache disabled
}

// NewThebeReader constructs a ThebeReader.
// db: *sql.DB pointing at the PostgreSQL projection database
// kv: ThebeKVStore for direct contract KV reads; may be nil (contract KV reads will error)
// c: cache.Cache implementation; nil disables caching (all reads hit SQL directly)
func NewThebeReader(db *sql.DB, kv ThebeKVStore, c cache.Cache) ThebeReader {
	return &thebeReader{db: db, kv: kv, cache: c}
}

// Compile-time interface check.
var _ ThebeReader = (*thebeReader)(nil)

// Package-level SQL constants — never fmt.Sprintf for SQL.
const (
	sqlGetLatestBlock = `
        SELECT block_number, block_hash, parent_hash, timestamp, txs_root, state_root,
               logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data
        FROM blocks
        ORDER BY block_number DESC
        LIMIT 1`

	sqlGetBlock = `
        SELECT block_number, block_hash, parent_hash, timestamp, txs_root, state_root,
               logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data
        FROM blocks WHERE block_number = $1`

	sqlGetAccount = `
        SELECT address, did_address, balance_wei, nonce, account_type, metadata,
               created_at, updated_at
        FROM accounts WHERE address = $1`

	sqlGetTransaction = `
        SELECT tx_hash, block_number, tx_index, from_addr, to_addr, value_wei, nonce,
               type, gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei,
               data, access_list, sig_v, sig_r, sig_s
        FROM transactions WHERE tx_hash = $1`

	sqlGetLatestTxsByAddr = `
        SELECT tx_hash, block_number, tx_index, from_addr, to_addr, value_wei, nonce,
               type, gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei,
               data, access_list, sig_v, sig_r, sig_s
        FROM transactions
        WHERE from_addr = $1 OR to_addr = $1
        ORDER BY block_number DESC, tx_index DESC
        LIMIT $2`

	sqlGetZKProof = `
        SELECT block_number, proof_hash, stark_proof, commitment
        FROM zk_proofs WHERE block_number = $1`

	sqlGetSnapshot = `
        SELECT block_number, block_hash, created_at
        FROM snapshots WHERE block_number = $1`

	// Phase 2.0 — bulk and alternate-key reads
	sqlGetBlockByHash = `
        SELECT block_number, block_hash, parent_hash, timestamp, txs_root, state_root,
               logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data
        FROM blocks WHERE block_hash = $1`

	sqlBulkGetBlocks = `
        SELECT block_number, block_hash, parent_hash, timestamp, txs_root, state_root,
               logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data
        FROM blocks WHERE block_number >= $1 AND block_number <= $2
        ORDER BY block_number ASC`

	sqlGetAccountByDID = `
        SELECT address, did_address, balance_wei, nonce, account_type, metadata,
               created_at, updated_at
        FROM accounts WHERE did_address = $1`

	sqlBulkGetAccounts = `
        SELECT address, did_address, balance_wei, nonce, account_type, metadata,
               created_at, updated_at
        FROM accounts WHERE address = ANY($1)`

	sqlListAccounts = `
        SELECT address, did_address, balance_wei, nonce, account_type, metadata,
               created_at, updated_at
        FROM accounts ORDER BY created_at ASC`

	sqlGetTxsByBlock = `
        SELECT tx_hash, block_number, tx_index, from_addr, to_addr, value_wei, nonce,
               type, gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei,
               data, access_list, sig_v, sig_r, sig_s
        FROM transactions WHERE block_number = $1
        ORDER BY tx_index ASC`
)

// read is the shared read-through pattern for single-record methods.
// 1. Try cache (if enabled). On hit: unmarshal into dest and return nil.
// 2. On miss or disabled: run sqlFn to populate dest from SQL.
// 3. On SQL success: marshal dest → cache.Set (best-effort, errors ignored).
// Time: O(1) — one cache GET + one SQL query on miss
func (r *thebeReader) read(
	ctx context.Context,
	key string,
	ttl time.Duration,
	dest any,
	sqlFn func() error,
) error {
	if r.cache != nil {
		data, err := r.cache.Get(ctx, key)
		if err == nil {
			return json.Unmarshal(data, dest)
		}
		// ErrMiss or any other error → fall through to SQL
	}
	if err := sqlFn(); err != nil {
		return err
	}
	if r.cache != nil {
		if data, err := json.Marshal(dest); err == nil {
			_ = r.cache.Set(ctx, key, data, ttl)
		}
	}
	return nil
}

// scanBlock scans a single block row into rec.
// Shared by GetLatestBlock and GetBlock.
func (r *thebeReader) scanBlock(row *sql.Row, rec *BlockRecord) error {
	var (
		coinbaseNull sql.NullString
		zkvmNull     sql.NullString
		gasLimitStr  string
		gasUsedStr   string
		extraJSON    []byte
	)
	err := row.Scan(
		&rec.BlockNumber,
		&rec.BlockHash,
		&rec.ParentHash,
		&rec.Timestamp,
		&rec.TxsRoot,
		&rec.StateRoot,
		&rec.LogsBloom,
		&coinbaseNull,
		&zkvmNull,
		&gasLimitStr,
		&gasUsedStr,
		&rec.Status,
		&extraJSON,
	)
	if err != nil {
		return err
	}
	rec.CoinbaseAddr = coinbaseNull.String
	rec.ZKVMAddr = zkvmNull.String
	// GasLimit and GasUsed stored as NUMERIC(78,0) → scan as string, parse to uint64.
	// If blank (NULL) leave as zero.
	if gasLimitStr != "" {
		_, _ = fmt.Sscanf(gasLimitStr, "%d", &rec.GasLimit)
	}
	if gasUsedStr != "" {
		_, _ = fmt.Sscanf(gasUsedStr, "%d", &rec.GasUsed)
	}
	_ = json.Unmarshal(extraJSON, &rec.ExtraData)
	return nil
}

// GetLatestBlock returns the most recently appended block.
// Time: O(1) — single row, indexed ORDER BY block_number DESC LIMIT 1
func (r *thebeReader) GetLatestBlock(ctx context.Context) (*BlockRecord, error) {
	var rec BlockRecord
	err := r.read(ctx, LatestBlockKey(), TTLLatestBlock, &rec, func() error {
		return r.scanBlock(r.db.QueryRowContext(ctx, sqlGetLatestBlock), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetBlock returns the block with the given block number.
// Time: O(1) — PK lookup
func (r *thebeReader) GetBlock(ctx context.Context, blockNumber uint64) (*BlockRecord, error) {
	var rec BlockRecord
	err := r.read(ctx, BlockKey(blockNumber), TTLBlock, &rec, func() error {
		return r.scanBlock(r.db.QueryRowContext(ctx, sqlGetBlock, blockNumber), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// scanAccount scans a single account row into rec.
func (r *thebeReader) scanAccount(s scanner, rec *AccountRecord) error {
	var metaJSON []byte
	err := s.Scan(
		&rec.Address,
		&rec.DIDAddress,
		&rec.BalanceWei,
		&rec.Nonce,
		&rec.AccountType,
		&metaJSON,
		&rec.CreatedAt,
		&rec.UpdatedAt,
	)
	if err != nil {
		return err
	}
	_ = json.Unmarshal(metaJSON, &rec.Metadata)
	return nil
}

// GetAccount returns the account with the given address.
// Time: O(1) — PK lookup
func (r *thebeReader) GetAccount(ctx context.Context, address string) (*AccountRecord, error) {
	var rec AccountRecord
	err := r.read(ctx, AccountKey(address), TTLAccount, &rec, func() error {
		return r.scanAccount(r.db.QueryRowContext(ctx, sqlGetAccount, address), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// scanTx scans a single transaction row into rec.
// Accepts the scanner interface so it works with both *sql.Row and *sql.Rows.
func (r *thebeReader) scanTx(s scanner, rec *TransactionRecord) error {
	var (
		toAddrNull     sql.NullString
		accessListJSON []byte
	)
	err := s.Scan(
		&rec.TxHash,
		&rec.BlockNumber,
		&rec.TxIndex,
		&rec.FromAddr,
		&toAddrNull,
		&rec.ValueWei,
		&rec.Nonce,
		&rec.Type,
		&rec.GasLimit,
		&rec.GasPriceWei,
		&rec.MaxFeeWei,
		&rec.MaxPriorityFeeWei,
		&rec.Data,
		&accessListJSON,
		&rec.SigV,
		&rec.SigR,
		&rec.SigS,
	)
	if err != nil {
		return err
	}
	if toAddrNull.Valid {
		rec.ToAddr = &toAddrNull.String
	}
	_ = json.Unmarshal(accessListJSON, &rec.AccessList)
	return nil
}

// GetTransaction returns the transaction with the given hash.
// Time: O(1) — PK lookup
func (r *thebeReader) GetTransaction(ctx context.Context, txHash string) (*TransactionRecord, error) {
	var rec TransactionRecord
	err := r.read(ctx, TransactionKey(txHash), TTLTransaction, &rec, func() error {
		return r.scanTx(r.db.QueryRowContext(ctx, sqlGetTransaction, txHash), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetLatestTransactionsByAddress returns up to limit transactions involving address,
// ordered by block_number DESC, tx_index DESC.
// Does NOT use read() — returns a slice, not a single record.
// Time: O(limit) — uses composite index idx_txn_from_block_desc / idx_txn_to_block_desc
func (r *thebeReader) GetLatestTransactionsByAddress(ctx context.Context, address string, limit int) ([]*TransactionRecord, error) {
	rows, err := r.db.QueryContext(ctx, sqlGetLatestTxsByAddr, address, limit)
	if err != nil {
		return nil, fmt.Errorf("GetLatestTransactionsByAddress: query: %w", err)
	}
	defer rows.Close()

	var results []*TransactionRecord
	for rows.Next() {
		var rec TransactionRecord
		if err := r.scanTx(rows, &rec); err != nil {
			return nil, fmt.Errorf("GetLatestTransactionsByAddress: scan: %w", err)
		}
		results = append(results, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetLatestTransactionsByAddress: rows: %w", err)
	}
	return results, nil
}

// scanZKProof scans a single zk_proofs row into rec.
func (r *thebeReader) scanZKProof(s scanner, rec *ZKProofRecord) error {
	return s.Scan(
		&rec.BlockNumber,
		&rec.ProofHash,
		&rec.StarkProof,
		&rec.Commitment,
	)
}

// GetZKProof returns the ZK proof for the given block number.
// Time: O(1) — PK lookup
func (r *thebeReader) GetZKProof(ctx context.Context, blockNumber uint64) (*ZKProofRecord, error) {
	var rec ZKProofRecord
	err := r.read(ctx, ZKProofKey(blockNumber), TTLZKProof, &rec, func() error {
		return r.scanZKProof(r.db.QueryRowContext(ctx, sqlGetZKProof, blockNumber), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// scanSnapshot scans a single snapshots row into rec.
func (r *thebeReader) scanSnapshot(s scanner, rec *SnapshotRecord) error {
	return s.Scan(
		&rec.BlockNumber,
		&rec.BlockHash,
		&rec.CreatedAt,
	)
}

// GetSnapshot returns the snapshot for the given block number.
// Time: O(1) — PK lookup
func (r *thebeReader) GetSnapshot(ctx context.Context, blockNumber uint64) (*SnapshotRecord, error) {
	var rec SnapshotRecord
	err := r.read(ctx, SnapshotKey(blockNumber), TTLSnapshot, &rec, func() error {
		return r.scanSnapshot(r.db.QueryRowContext(ctx, sqlGetSnapshot, blockNumber), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetBlockByHash returns the block with the given block hash.
// Time: O(1) — hash-indexed lookup (requires index on block_hash column)
func (r *thebeReader) GetBlockByHash(ctx context.Context, hash string) (*BlockRecord, error) {
	var rec BlockRecord
	err := r.read(ctx, "jmdn:block:hash:"+hash, TTLBlock, &rec, func() error {
		return r.scanBlock(r.db.QueryRowContext(ctx, sqlGetBlockByHash, hash), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// BulkGetBlocks returns all blocks in [from, to] inclusive ordered by block_number ASC.
// Time: O(n) where n = to-from+1 — single SQL range scan.
func (r *thebeReader) BulkGetBlocks(ctx context.Context, from, to uint64) ([]*BlockRecord, error) {
	rows, err := r.db.QueryContext(ctx, sqlBulkGetBlocks, from, to)
	if err != nil {
		return nil, fmt.Errorf("BulkGetBlocks: query: %w", err)
	}
	defer rows.Close()

	var results []*BlockRecord
	for rows.Next() {
		var rec BlockRecord
		var (
			coinbaseNull sql.NullString
			zkvmNull     sql.NullString
			gasLimitStr  string
			gasUsedStr   string
			extraJSON    []byte
		)
		if err := rows.Scan(
			&rec.BlockNumber, &rec.BlockHash, &rec.ParentHash, &rec.Timestamp,
			&rec.TxsRoot, &rec.StateRoot, &rec.LogsBloom,
			&coinbaseNull, &zkvmNull,
			&gasLimitStr, &gasUsedStr,
			&rec.Status, &extraJSON,
		); err != nil {
			return nil, fmt.Errorf("BulkGetBlocks: scan: %w", err)
		}
		rec.CoinbaseAddr = coinbaseNull.String
		rec.ZKVMAddr = zkvmNull.String
		if gasLimitStr != "" {
			_, _ = fmt.Sscanf(gasLimitStr, "%d", &rec.GasLimit)
		}
		if gasUsedStr != "" {
			_, _ = fmt.Sscanf(gasUsedStr, "%d", &rec.GasUsed)
		}
		_ = json.Unmarshal(extraJSON, &rec.ExtraData)
		results = append(results, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("BulkGetBlocks: rows: %w", err)
	}
	return results, nil
}

// GetAccountByDID returns the account with the given DID address.
// Time: O(1) — DID-indexed lookup (requires index on did_address column)
func (r *thebeReader) GetAccountByDID(ctx context.Context, did string) (*AccountRecord, error) {
	var rec AccountRecord
	err := r.read(ctx, "jmdn:account:did:"+did, TTLAccount, &rec, func() error {
		return r.scanAccount(r.db.QueryRowContext(ctx, sqlGetAccountByDID, did), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// BulkGetAccounts returns accounts for all given addresses in a single SQL ANY() query.
// Time: O(n) where n = len(addresses) — single SQL ANY() scan.
func (r *thebeReader) BulkGetAccounts(ctx context.Context, addresses []string) ([]*AccountRecord, error) {
	rows, err := r.db.QueryContext(ctx, sqlBulkGetAccounts, pq.Array(addresses))
	if err != nil {
		return nil, fmt.Errorf("BulkGetAccounts: query: %w", err)
	}
	defer rows.Close()

	var results []*AccountRecord
	for rows.Next() {
		var rec AccountRecord
		if err := r.scanAccount(rows, &rec); err != nil {
			return nil, fmt.Errorf("BulkGetAccounts: scan: %w", err)
		}
		results = append(results, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("BulkGetAccounts: rows: %w", err)
	}
	return results, nil
}

// ListAccounts returns accounts ordered by creation time. limit <= 0 means no cap.
// Time: O(n) — sequential scan ordered by created_at; n = rows returned.
func (r *thebeReader) ListAccounts(ctx context.Context, limit int) ([]*AccountRecord, error) {
	query := sqlListAccounts
	args := []any{}
	if limit > 0 {
		query += " LIMIT $1"
		args = append(args, limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListAccounts: query: %w", err)
	}
	defer rows.Close()

	var results []*AccountRecord
	for rows.Next() {
		var rec AccountRecord
		if err := r.scanAccount(rows, &rec); err != nil {
			return nil, fmt.Errorf("ListAccounts: scan: %w", err)
		}
		results = append(results, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListAccounts: rows: %w", err)
	}
	return results, nil
}

// GetTransactionsByBlock returns all transactions in the given block ordered by tx_index ASC.
// Time: O(n) where n = number of transactions in the block.
func (r *thebeReader) GetTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]*TransactionRecord, error) {
	rows, err := r.db.QueryContext(ctx, sqlGetTxsByBlock, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByBlock: query: %w", err)
	}
	defer rows.Close()

	var results []*TransactionRecord
	for rows.Next() {
		var rec TransactionRecord
		if err := r.scanTx(rows, &rec); err != nil {
			return nil, fmt.Errorf("GetTransactionsByBlock: scan: %w", err)
		}
		results = append(results, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetTransactionsByBlock: rows: %w", err)
	}
	return results, nil
}

// sqlGetContractReceipt fetches a contract receipt by tx_hash (PK).
const sqlGetContractReceipt = `
    SELECT tx_hash, block_number, tx_index, status, gas_used, contract_address,
           logs, revert_reason, created_at
    FROM contract_receipts WHERE tx_hash = $1`

// readKV reads from KV store, populates dest via JSON unmarshal.
// No cache for KV reads — BadgerDB is already in-process and fast.
// Time: O(1) — single BadgerDB read
func (r *thebeReader) readKV(key []byte, dest any) error {
	data, err := r.kv.Get(key)
	if err != nil {
		return fmt.Errorf("KV get: %w", err)
	}
	return json.Unmarshal(data, dest)
}

// GetContractCode returns contract bytecode from KV.
// Time: O(1) — KV direct read
func (r *thebeReader) GetContractCode(_ context.Context, address string) (*ContractCodeRecord, error) {
	var rec ContractCodeRecord
	if err := r.readKV(kvCodeKey(address), &rec); err != nil {
		return nil, fmt.Errorf("GetContractCode %s: %w", address, err)
	}
	return &rec, nil
}

// GetContractNonce returns contract nonce from KV (stored as raw big-endian uint64).
// Time: O(1) — KV direct read, nonce stored as raw uint64 big-endian
func (r *thebeReader) GetContractNonce(_ context.Context, address string) (*ContractNonceRecord, error) {
	data, err := r.kv.Get(kvNonceKey(address))
	if err != nil {
		return nil, fmt.Errorf("GetContractNonce %s: %w", address, err)
	}
	return &ContractNonceRecord{Address: address, Nonce: decodeUint64(data)}, nil
}

// GetContractStorage returns a contract storage slot from KV (binary key).
// slot must be exactly 32 bytes (raw, not hex-encoded).
// Time: O(1) — KV direct read (binary key)
func (r *thebeReader) GetContractStorage(_ context.Context, address string, slot []byte) (*ContractStorageRecord, error) {
	addrBytes, err := hexToBytes20(address)
	if err != nil {
		return nil, fmt.Errorf("GetContractStorage: invalid address: %w", err)
	}
	if len(slot) != 32 {
		return nil, fmt.Errorf("GetContractStorage: slot must be 32 bytes, got %d", len(slot))
	}
	var rec ContractStorageRecord
	if err := r.readKV(kvStorageKey(addrBytes, slot), &rec); err != nil {
		return nil, fmt.Errorf("GetContractStorage %s: %w", address, err)
	}
	return &rec, nil
}

// GetContractMeta returns contract deployment metadata from KV.
// Time: O(1) — KV direct read
func (r *thebeReader) GetContractMeta(_ context.Context, address string) (*ContractMetaRecord, error) {
	var rec ContractMetaRecord
	if err := r.readKV(kvMetaKey(address), &rec); err != nil {
		return nil, fmt.Errorf("GetContractMeta %s: %w", address, err)
	}
	return &rec, nil
}

// GetContractReceipt returns the contract receipt for the given tx hash.
// Time: O(1) — SQL PK lookup with read-through cache
func (r *thebeReader) GetContractReceipt(ctx context.Context, txHash string) (*ContractReceiptRecord, error) {
	var rec ContractReceiptRecord
	err := r.read(ctx, ContractReceiptKey(txHash), TTLTransaction, &rec, func() error {
		return r.scanContractReceipt(r.db.QueryRowContext(ctx, sqlGetContractReceipt, txHash), &rec)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// IsTxProcessing returns true if txHash has a "-1" in-flight flag in KV.
// A missing key or empty tombstone both return false.
// Time: O(1) — KV direct read (no SQL, no cache)
func (r *thebeReader) IsTxProcessing(_ context.Context, txHash string) (bool, error) {
	val, err := r.kv.Get(kvTxProcessingKey(txHash))
	if err != nil {
		// key not found → not processing
		return false, nil
	}
	// empty tombstone = cleared
	return len(val) > 0, nil
}

// scanContractReceipt scans a single contract_receipts row into rec.
func (r *thebeReader) scanContractReceipt(row *sql.Row, rec *ContractReceiptRecord) error {
	var contractAddrNull sql.NullString
	var logsJSON []byte
	err := row.Scan(
		&rec.TxHash, &rec.BlockNumber, &rec.TxIndex, &rec.Status,
		&rec.GasUsed, &contractAddrNull, &logsJSON, &rec.RevertReason, &rec.CreatedAt,
	)
	if err != nil {
		return err
	}
	if contractAddrNull.Valid {
		rec.ContractAddress = &contractAddrNull.String
	}
	rec.Logs = logsJSON
	return nil
}
