package thebeprofile

// migration is the complete PostgreSQL DDL for the JMDN blockchain schema.
// Applied by ThebeDB at startup via GetMigration(). Never run manually.
//
// Table creation order satisfies FK dependencies:
//  1. accounts  2. blocks  3. transactions  4. zk_proofs  5. snapshots
//
// Column type conventions:
//
//	CHAR(42)      = Ethereum address (0x-hex, 20 bytes)
//	CHAR(66)      = Keccak-256 hash  (0x-hex, 32 bytes)
//	NUMERIC(78,0) = uint256 - balance, gas, nonce (never BIGINT)
//	BYTEA         = raw binary (proofs, bloom filter, calldata)
//	JSONB         = binary JSON (metadata, access_list, extra_data)
//	TIMESTAMPTZ   = timezone-aware timestamp
const migration = `
BEGIN;

-- ================================================================
-- 1. accounts
-- Mutable: CREATE, UPDATE, READ (no deletes)
-- PK: address CHAR(42) - the canonical wallet address
-- did_address is optional (only set when DID maps 1:1 to this account)
-- balance_wei and nonce are updated in-place on state changes
-- updated_at is auto-maintained by trigger fn_update_account_timestamp
-- ================================================================
CREATE TABLE IF NOT EXISTS accounts (
    address      CHAR(42)       PRIMARY KEY,
    did_address  TEXT           UNIQUE,
    balance_wei  NUMERIC(78,0)  NOT NULL DEFAULT 0,
    nonce        NUMERIC(78,0)  NOT NULL DEFAULT 0,
    account_type SMALLINT       NOT NULL,
    metadata     JSONB          NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ    NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_account_balance_non_negative
        CHECK (balance_wei >= 0),
    CONSTRAINT chk_account_nonce_non_negative
        CHECK (nonce >= 0),
    CONSTRAINT chk_account_updated_after_created
        CHECK (updated_at >= created_at)
);

CREATE INDEX IF NOT EXISTS idx_accounts_updated_at
    ON accounts(updated_at DESC);

CREATE OR REPLACE FUNCTION fn_update_account_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger WHERE tgname = 'trg_account_updated_at'
    ) THEN
        CREATE TRIGGER trg_account_updated_at
            BEFORE UPDATE ON accounts
            FOR EACH ROW EXECUTE FUNCTION fn_update_account_timestamp();
    END IF;
END $$;

-- Prevent hard deletes. Accounts are never removed.
CREATE OR REPLACE RULE rule_accounts_no_delete AS
    ON DELETE TO accounts DO INSTEAD NOTHING;

-- ================================================================
-- 2. blocks
-- Append Only: CREATE, READ
-- PK: block_number BIGINT - sequential chain identifier
-- block_hash must be globally unique across all blocks
-- status: 0=Pending 1=Confirmed 2=Finalized
-- coinbase_addr / zkvm_addr: optional FK to accounts if referential
--   integrity is desired - uncomment constraints below to enable
-- ================================================================
CREATE TABLE IF NOT EXISTS blocks (
    block_number  BIGINT         PRIMARY KEY,
    block_hash    CHAR(66)       NOT NULL UNIQUE,
    parent_hash   CHAR(66)       NOT NULL,
    timestamp     TIMESTAMPTZ    NOT NULL,
    txs_root      CHAR(66),
    state_root    CHAR(66),
    logs_bloom    BYTEA,
    coinbase_addr CHAR(42),
    zkvm_addr     CHAR(42),
    gas_limit     NUMERIC(78,0),
    gas_used      NUMERIC(78,0),
    status        SMALLINT       NOT NULL,
    extra_data    JSONB          NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ    NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_block_number_non_negative
        CHECK (block_number >= 0),
    CONSTRAINT chk_block_gas_used_within_limit
        CHECK (gas_used IS NULL OR gas_limit IS NULL OR gas_used <= gas_limit),
    CONSTRAINT chk_block_status_valid
        CHECK (status IN (0, 1, 2))

    -- Optional FKs (uncomment if referential integrity on producer addrs needed):
    -- ,CONSTRAINT fk_block_coinbase
    --     FOREIGN KEY (coinbase_addr) REFERENCES accounts(address)
    -- ,CONSTRAINT fk_block_zkvm
    --     FOREIGN KEY (zkvm_addr) REFERENCES accounts(address)
);

CREATE INDEX IF NOT EXISTS idx_blocks_timestamp
    ON blocks(timestamp DESC);

-- Append-only enforcement - silently discard UPDATE and DELETE
CREATE OR REPLACE RULE rule_blocks_no_update AS
    ON UPDATE TO blocks DO INSTEAD NOTHING;
CREATE OR REPLACE RULE rule_blocks_no_delete AS
    ON DELETE TO blocks DO INSTEAD NOTHING;

-- ================================================================
-- 3. transactions
-- Append Only: CREATE, READ
-- PK: tx_hash CHAR(66)
-- FK: block_number -> blocks, from_addr -> accounts, to_addr -> accounts
-- to_addr is NULL for contract creation transactions
-- type: 0=Legacy 1=AccessList(EIP-2930) 2=DynamicFee(EIP-1559)
-- Fee model constraint enforces correct fields per tx type:
--   type 0 or 1: gas_price_wei must be set
--   type 2:      max_fee_wei + max_priority_fee_wei must be set
-- UNIQUE(from_addr, nonce): enforces sender nonce invariant (no replay)
-- UNIQUE(block_number, tx_index): enforces positional integrity in block
-- Partial index on to_addr skips NULLs (contract creations) for efficiency
-- ================================================================
CREATE TABLE IF NOT EXISTS transactions (
    tx_hash              CHAR(66)       PRIMARY KEY,
    block_number         BIGINT         NOT NULL,
    tx_index             INT            NOT NULL,
    from_addr            CHAR(42)       NOT NULL,
    to_addr              CHAR(42),
    value_wei            NUMERIC(78,0)  NOT NULL DEFAULT 0,
    nonce                NUMERIC(78,0)  NOT NULL,
    type                 SMALLINT       NOT NULL,
    gas_limit            NUMERIC(78,0),
    gas_price_wei        NUMERIC(78,0),
    max_fee_wei          NUMERIC(78,0),
    max_priority_fee_wei NUMERIC(78,0),
    data                 BYTEA,
    access_list          JSONB          NOT NULL DEFAULT '[]'::jsonb,
    sig_v                INT,
    sig_r                CHAR(66),
    sig_s                CHAR(66),
    created_at           TIMESTAMPTZ    NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_txn_block
        FOREIGN KEY (block_number) REFERENCES blocks(block_number),
    CONSTRAINT fk_txn_from
        FOREIGN KEY (from_addr) REFERENCES accounts(address),
    CONSTRAINT fk_txn_to
        FOREIGN KEY (to_addr) REFERENCES accounts(address),

    CONSTRAINT uq_txn_block_index
        UNIQUE (block_number, tx_index),
    CONSTRAINT uq_txn_from_nonce
        UNIQUE (from_addr, nonce),

    CONSTRAINT chk_txn_value_non_negative
        CHECK (value_wei >= 0),
    CONSTRAINT chk_txn_nonce_non_negative
        CHECK (nonce >= 0),
    CONSTRAINT chk_txn_type_valid
        CHECK (type IN (0, 1, 2)),
    CONSTRAINT chk_txn_gas_limit_positive
        CHECK (gas_limit IS NULL OR gas_limit > 0),
    CONSTRAINT chk_txn_fee_model
        CHECK (
            (type = 0 AND gas_price_wei IS NOT NULL)
            OR (type = 1 AND gas_price_wei IS NOT NULL)
            OR (type = 2 AND max_fee_wei IS NOT NULL
                         AND max_priority_fee_wei IS NOT NULL)
        )
);

CREATE INDEX IF NOT EXISTS idx_txn_block_number
    ON transactions(block_number);
CREATE INDEX IF NOT EXISTS idx_txn_from_addr
    ON transactions(from_addr);
-- Partial index: skip NULL to_addr (contract creation txns)
CREATE INDEX IF NOT EXISTS idx_txn_to_addr
    ON transactions(to_addr) WHERE to_addr IS NOT NULL;

CREATE OR REPLACE RULE rule_transactions_no_update AS
    ON UPDATE TO transactions DO INSTEAD NOTHING;
CREATE OR REPLACE RULE rule_transactions_no_delete AS
    ON DELETE TO transactions DO INSTEAD NOTHING;

-- ================================================================
-- 4. zk_proofs
-- Append Only: CREATE, READ
-- PK: proof_hash CHAR(66) - unique proof identifier
-- block_number is UNIQUE - exactly one ZK proof per block
-- stark_proof: serialised STARK proof binary (NOT NULL)
-- commitment: cryptographic commitment binding (nullable)
-- ================================================================
CREATE TABLE IF NOT EXISTS zk_proofs (
    proof_hash   CHAR(66)     PRIMARY KEY,
    block_number BIGINT       NOT NULL UNIQUE,
    stark_proof  BYTEA        NOT NULL,
    commitment   BYTEA,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_zkproof_block
        FOREIGN KEY (block_number) REFERENCES blocks(block_number)
);

CREATE OR REPLACE RULE rule_zk_proofs_no_update AS
    ON UPDATE TO zk_proofs DO INSTEAD NOTHING;
CREATE OR REPLACE RULE rule_zk_proofs_no_delete AS
    ON DELETE TO zk_proofs DO INSTEAD NOTHING;

-- ================================================================
-- 5. snapshots
-- Append Only: CREATE, READ
-- PK: snapshot_id BIGINT GENERATED ALWAYS AS IDENTITY (surrogate key)
--     Surrogate key used because self-referencing FK is more natural
--     with an auto-incrementing ID than with block_number
-- block_number is UNIQUE - one snapshot per block
-- block_hash is UNIQUE - exact identity verification
-- prev_snapshot_id: self-referencing FK for snapshot chain. NULL = genesis
-- chk_snapshot_no_self_ref: prevents a snapshot pointing to itself
-- ================================================================
CREATE TABLE IF NOT EXISTS snapshots (
    snapshot_id      BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    block_number     BIGINT       NOT NULL UNIQUE,
    block_hash       CHAR(66)     NOT NULL UNIQUE,
    prev_snapshot_id BIGINT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_snapshot_block
        FOREIGN KEY (block_number) REFERENCES blocks(block_number),
    CONSTRAINT fk_snapshot_prev
        FOREIGN KEY (prev_snapshot_id) REFERENCES snapshots(snapshot_id),

    CONSTRAINT chk_snapshot_block_number_non_negative
        CHECK (block_number >= 0),
    CONSTRAINT chk_snapshot_no_self_ref
        CHECK (prev_snapshot_id IS NULL OR prev_snapshot_id <> snapshot_id)
);

CREATE INDEX IF NOT EXISTS idx_snapshots_created_at
    ON snapshots(created_at DESC);

CREATE OR REPLACE RULE rule_snapshots_no_update AS
    ON UPDATE TO snapshots DO INSTEAD NOTHING;
CREATE OR REPLACE RULE rule_snapshots_no_delete AS
    ON DELETE TO snapshots DO INSTEAD NOTHING;

COMMIT;
`
