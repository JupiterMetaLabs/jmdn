package repository

import (
	"context"
	"database/sql"
	"fmt"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
)

// StateTracker manages the migration progress securely.
type StateTracker struct {
	db *thebedb.ThebeDB
}

// NewStateTracker creates a new StateTracker.
func NewStateTracker(db *thebedb.ThebeDB) *StateTracker {
	return &StateTracker{
		db: db,
	}
}

// LastSyncedBlock returns the highest block number successfully migrated to ThebeDB.
func (s *StateTracker) LastSyncedBlock(ctx context.Context) (uint64, error) {
	if s.db == nil || s.db.SQL == nil || s.db.SQL.GetDB() == nil {
		return 0, fmt.Errorf("thebedb sql engine not initialized")
	}

	var maxBlock sql.NullInt64
	// Query the core blocks table that our migration populates
	err := s.db.SQL.GetDB().QueryRowContext(ctx, "SELECT MAX(block_number) FROM blocks").Scan(&maxBlock)
	if err != nil {
		// If the table doesn't exist yet, it will return an error. We can safely assume 0 for now.
		return 0, nil
	}

	if !maxBlock.Valid {
		// Table is empty
		return 0, nil
	}

	return uint64(maxBlock.Int64), nil
}

// EnsureSchema formats the fundamental tables if they don't exist.
// This directly maps to the target schema in pkg/sql/.sql/schemaPostgres.go
// but replaces constraints with types that match ThebeDB's builder inputs exactly.
func (s *StateTracker) EnsureSchema(ctx context.Context) error {
	if s.db == nil || s.db.SQL == nil {
		return fmt.Errorf("thebedb sql engine not initialized")
	}

	schema := `
	CREATE TABLE IF NOT EXISTS accounts (
		address      CHAR(42)    PRIMARY KEY,
		did_address  TEXT        NOT NULL UNIQUE,
		balance_wei  VARCHAR(30) NOT NULL DEFAULT '0',
		nonce        VARCHAR(30) NOT NULL DEFAULT '0',
		account_type SMALLINT    NOT NULL,
		metadata     JSONB       NOT NULL DEFAULT '{}'::jsonb,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_accounts_updated_at ON accounts(updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_accounts_did_address ON accounts(did_address);

	CREATE TABLE IF NOT EXISTS blocks (
		block_number  BIGINT         PRIMARY KEY,
		block_hash    CHAR(66)       NOT NULL UNIQUE,
		parent_hash   CHAR(66)       NOT NULL,
		timestamp     TIMESTAMPTZ    NOT NULL,
		txns_root     CHAR(66)       NOT NULL UNIQUE,
		state_root    CHAR(66)       NOT NULL UNIQUE,
		logs_bloom    BYTEA,
		coinbase_addr CHAR(42),
		zkvm_addr     CHAR(42),
		gas_limit     VARCHAR(30),
		gas_used      VARCHAR(30),
		status        VARCHAR(30)    NOT NULL,
		extra_data    JSONB          NOT NULL DEFAULT '{}'::jsonb,
		created_at    TIMESTAMPTZ    NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_blocks_timestamp ON blocks(timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_blocks_block_hash ON blocks(block_hash);

	CREATE TABLE IF NOT EXISTS transactions (
		tx_hash              CHAR(66)       PRIMARY KEY,
		block_number         BIGINT         NOT NULL, 
		tx_index             SMALLINT       NOT NULL,
		from_addr            CHAR(42)       NOT NULL, 
		to_addr              CHAR(42),
		value_wei            VARCHAR(78)    NOT NULL DEFAULT '0',
		nonce                VARCHAR(78)    NOT NULL,
		type                 SMALLINT       NOT NULL DEFAULT 0,
		gas_limit            VARCHAR(30),
		gas_price_wei        VARCHAR(30),
		max_fee_wei          VARCHAR(30),
		max_priority_fee_wei VARCHAR(30),
		data                 BYTEA,
		access_list          JSONB          NOT NULL DEFAULT '[]'::jsonb,
		sig_v                SMALLINT       NOT NULL,
		sig_r                CHAR(66)       NOT NULL,
		sig_s                CHAR(66)       NOT NULL,
		created_at           TIMESTAMPTZ    NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_txn_block_number ON transactions(block_number);
	CREATE INDEX IF NOT EXISTS idx_txn_from_addr    ON transactions(from_addr);

	CREATE TABLE IF NOT EXISTS zk_proofs (
		block_number BIGINT      PRIMARY KEY,
		proof_hash   CHAR(66)    NOT NULL UNIQUE,
		stark_proof  BYTEA       NOT NULL,
		commitment   JSONB,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	`
	_, err := s.db.SQL.GetDB().ExecContext(ctx, schema)
	return err
}
