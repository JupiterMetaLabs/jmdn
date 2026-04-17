// Package cassata is the sole middleware between JMDN and ThebeDB.
// It owns all SQL reads and all KV write encoding.
// No other JMDN package imports ThebeDB directly.
package cassata

import (
	"context"
	"encoding/json"
	"fmt"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"go.uber.org/zap"
)

type Cassata struct {
	db     *thebedb.ThebeDB
	logger *zap.Logger
}

func New(db *thebedb.ThebeDB, logger *zap.Logger) *Cassata {
	return &Cassata{db: db, logger: logger}
}

// Write path.
// Each Ingest* marshals the result type to JSON and calls db.Append().
// ThebeDB projects it into Postgres synchronously via Apply().

func (c *Cassata) IngestAccount(ctx context.Context, a AccountResult) error {
	v, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("cassata.IngestAccount marshal: %w", err)
	}
	return c.db.Append(ctx, "account", "account:"+a.Address, v)
}

func (c *Cassata) IngestBlock(ctx context.Context, b BlockResult) error {
	v, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("cassata.IngestBlock marshal: %w", err)
	}
	return c.db.Append(ctx, "block", fmt.Sprintf("block:%d", b.BlockNumber), v)
}

func (c *Cassata) IngestTx(ctx context.Context, t TxResult) error {
	v, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("cassata.IngestTx marshal: %w", err)
	}
	return c.db.Append(ctx, "tx", "tx:"+t.TxHash, v)
}

func (c *Cassata) IngestZKProof(ctx context.Context, z ZKProofResult) error {
	v, err := json.Marshal(z)
	if err != nil {
		return fmt.Errorf("cassata.IngestZKProof marshal: %w", err)
	}
	return c.db.Append(ctx, "zk", "zk:"+z.ProofHash, v)
}

func (c *Cassata) IngestSnapshot(ctx context.Context, s SnapshotResult) error {
	v, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("cassata.IngestSnapshot marshal: %w", err)
	}
	return c.db.Append(ctx, "snapshot", fmt.Sprintf("snapshot:%d", s.BlockNumber), v)
}

// Read path.
// All reads query the Postgres projection directly.
// Column order in SELECT must match Scan() argument order exactly.

func (c *Cassata) GetAccount(ctx context.Context, address string) (*AccountResult, error) {
	row := c.db.SQL.QueryRowContext(ctx, `
		SELECT address, did_address, balance_wei, nonce,
		       account_type, metadata, created_at, updated_at
		FROM accounts WHERE address = $1`, address)
	var a AccountResult
	if err := row.Scan(
		&a.Address, &a.DIDAddress, &a.BalanceWei, &a.Nonce,
		&a.AccountType, &a.Metadata, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("cassata.GetAccount: %w", err)
	}
	return &a, nil
}

func (c *Cassata) ListAccounts(ctx context.Context, limit, offset int) ([]AccountResult, error) {
	rows, err := c.db.SQL.QueryContext(ctx, `
		SELECT address, did_address, balance_wei, nonce,
		       account_type, metadata, created_at, updated_at
		FROM accounts ORDER BY updated_at DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("cassata.ListAccounts: %w", err)
	}
	defer rows.Close()
	var out []AccountResult
	for rows.Next() {
		var a AccountResult
		if err := rows.Scan(
			&a.Address, &a.DIDAddress, &a.BalanceWei, &a.Nonce,
			&a.AccountType, &a.Metadata, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (c *Cassata) GetBlock(ctx context.Context, blockNumber uint64) (*BlockResult, error) {
	row := c.db.SQL.QueryRowContext(ctx, `
		SELECT block_number, block_hash, parent_hash, timestamp,
		       txs_root, state_root, logs_bloom,
		       coinbase_addr, zkvm_addr,
		       gas_limit, gas_used, status, extra_data, created_at
		FROM blocks WHERE block_number = $1`, blockNumber)
	var b BlockResult
	if err := row.Scan(
		&b.BlockNumber, &b.BlockHash, &b.ParentHash, &b.Timestamp,
		&b.TxsRoot, &b.StateRoot, &b.LogsBloom,
		&b.CoinbaseAddr, &b.ZKVMAddr,
		&b.GasLimit, &b.GasUsed, &b.Status, &b.ExtraData, &b.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("cassata.GetBlock: %w", err)
	}
	return &b, nil
}

func (c *Cassata) ListBlocks(ctx context.Context, limit, offset int) ([]BlockResult, error) {
	rows, err := c.db.SQL.QueryContext(ctx, `
		SELECT block_number, block_hash, parent_hash, timestamp,
		       txs_root, state_root, logs_bloom,
		       coinbase_addr, zkvm_addr,
		       gas_limit, gas_used, status, extra_data, created_at
		FROM blocks ORDER BY block_number DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("cassata.ListBlocks: %w", err)
	}
	defer rows.Close()
	var out []BlockResult
	for rows.Next() {
		var b BlockResult
		if err := rows.Scan(
			&b.BlockNumber, &b.BlockHash, &b.ParentHash, &b.Timestamp,
			&b.TxsRoot, &b.StateRoot, &b.LogsBloom,
			&b.CoinbaseAddr, &b.ZKVMAddr,
			&b.GasLimit, &b.GasUsed, &b.Status, &b.ExtraData, &b.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (c *Cassata) GetTransaction(ctx context.Context, txHash string) (*TxResult, error) {
	row := c.db.SQL.QueryRowContext(ctx, `
		SELECT tx_hash, block_number, tx_index, from_addr, to_addr,
		       value_wei, nonce, type,
		       gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei,
		       data, access_list, sig_v, sig_r, sig_s, created_at
		FROM transactions WHERE tx_hash = $1`, txHash)
	var t TxResult
	if err := row.Scan(
		&t.TxHash, &t.BlockNumber, &t.TxIndex, &t.FromAddr, &t.ToAddr,
		&t.ValueWei, &t.Nonce, &t.Type,
		&t.GasLimit, &t.GasPriceWei, &t.MaxFeeWei, &t.MaxPriorityFeeWei,
		&t.Data, &t.AccessList, &t.SigV, &t.SigR, &t.SigS, &t.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("cassata.GetTransaction: %w", err)
	}
	return &t, nil
}

func (c *Cassata) ListTransactionsByBlock(ctx context.Context, blockNumber uint64) ([]TxResult, error) {
	rows, err := c.db.SQL.QueryContext(ctx, `
		SELECT tx_hash, block_number, tx_index, from_addr, to_addr,
		       value_wei, nonce, type,
		       gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei,
		       data, access_list, sig_v, sig_r, sig_s, created_at
		FROM transactions WHERE block_number = $1
		ORDER BY tx_index ASC`, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("cassata.ListTransactionsByBlock: %w", err)
	}
	defer rows.Close()
	var out []TxResult
	for rows.Next() {
		var t TxResult
		if err := rows.Scan(
			&t.TxHash, &t.BlockNumber, &t.TxIndex, &t.FromAddr, &t.ToAddr,
			&t.ValueWei, &t.Nonce, &t.Type,
			&t.GasLimit, &t.GasPriceWei, &t.MaxFeeWei, &t.MaxPriorityFeeWei,
			&t.Data, &t.AccessList, &t.SigV, &t.SigR, &t.SigS, &t.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (c *Cassata) GetZKProof(ctx context.Context, proofHash string) (*ZKProofResult, error) {
	row := c.db.SQL.QueryRowContext(ctx, `
		SELECT proof_hash, block_number, stark_proof, commitment, created_at
		FROM zk_proofs WHERE proof_hash = $1`, proofHash)
	var z ZKProofResult
	if err := row.Scan(&z.ProofHash, &z.BlockNumber, &z.StarkProof, &z.Commitment, &z.CreatedAt); err != nil {
		return nil, fmt.Errorf("cassata.GetZKProof: %w", err)
	}
	return &z, nil
}

func (c *Cassata) GetZKProofByBlock(ctx context.Context, blockNumber uint64) (*ZKProofResult, error) {
	row := c.db.SQL.QueryRowContext(ctx, `
		SELECT proof_hash, block_number, stark_proof, commitment, created_at
		FROM zk_proofs WHERE block_number = $1`, blockNumber)
	var z ZKProofResult
	if err := row.Scan(&z.ProofHash, &z.BlockNumber, &z.StarkProof, &z.Commitment, &z.CreatedAt); err != nil {
		return nil, fmt.Errorf("cassata.GetZKProofByBlock: %w", err)
	}
	return &z, nil
}

func (c *Cassata) GetSnapshot(ctx context.Context, blockNumber uint64) (*SnapshotResult, error) {
	row := c.db.SQL.QueryRowContext(ctx, `
		SELECT snapshot_id, block_number, block_hash, prev_snapshot_id, created_at
		FROM snapshots WHERE block_number = $1`, blockNumber)
	var s SnapshotResult
	if err := row.Scan(&s.SnapshotID, &s.BlockNumber, &s.BlockHash, &s.PrevSnapshotID, &s.CreatedAt); err != nil {
		return nil, fmt.Errorf("cassata.GetSnapshot: %w", err)
	}
	return &s, nil
}

func (c *Cassata) ListSnapshots(ctx context.Context, limit, offset int) ([]SnapshotResult, error) {
	rows, err := c.db.SQL.QueryContext(ctx, `
		SELECT snapshot_id, block_number, block_hash, prev_snapshot_id, created_at
		FROM snapshots ORDER BY created_at DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("cassata.ListSnapshots: %w", err)
	}
	defer rows.Close()
	var out []SnapshotResult
	for rows.Next() {
		var s SnapshotResult
		if err := rows.Scan(
			&s.SnapshotID, &s.BlockNumber, &s.BlockHash, &s.PrevSnapshotID, &s.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
