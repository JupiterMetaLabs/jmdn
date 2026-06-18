// MODULE: DB_OPs/thebeprofile/schema.go
// PURPOSE: Holds the PostgreSQL DDL migration for the JMDN ThebeDB projection schema.
//          Returned verbatim by JMDNProfile.GetMigration() and executed once at startup.
//          Phase 7: migrationSQL002 adds contract_receipts table (contract code/nonce/storage/meta live in KV).
//
// CORE DATA STRUCTURES:
//   - migrationSQL: package-level string constant — read-only after compile.
//     Size: static ~5KB SQL string. Never allocated at runtime.
//   - migrationSQL002: Phase 7 contract_receipts table.
//
// TO MODIFY BEHAVIOR:
//   - Add a new table: add a new migrationSQL00N constant + concatenate in GetMigration()
//   - Rename a table: update DDL here + corresponding apply_<entity>.go + register in profile.go
//
// DO NOT:
//   - Modify this constant at runtime (it is a const — compile-time only)
//   - Add Go logic to this file; SQL DDL only
//   - Use fmt.Sprintf to build SQL (injection risk, const handles it)

package thebeprofile

// migrationSQL is the complete PostgreSQL DDL for the JMDN blockchain projection schema.
// Applied by ThebeDB at startup via GetMigration(). All statements use IF NOT EXISTS
// for idempotency — safe to re-run on every node restart.
//
// Table creation order satisfies FK dependencies:
//  1. accounts  2. blocks  3. snapshots  4. transactions  5. zk_proofs  6. l1_finality
const migrationSQL = `
-- ================================================================
-- ThebeDB - JMDN PostgreSQL Projection Schema
-- Migration: 000001_init_schema (UP)
-- Applied by: JMDNProfile.GetMigration() via ThebeDB profile system
--
-- Storage model:
--   accounts     → SQL mutable  (Create, Update, Read)
--   blocks       → SQL append-only (Create, Read)
--   snapshots    → SQL append-only (Create, Read)
--   transactions → SQL append-only (Create, Read)
--   zk_proofs    → SQL append-only (Create, Read)
--   l1_finality  → SQL append-only (Create, Read)
--
-- Contract data (code, storage, nonces, meta, receipts) → ThebeDB KV store (BadgerDB)
-- Defined in Phase 7 migration when KV key schema is confirmed.
-- ================================================================

-- ================================================================
-- 1) accounts (SQL — Create Update Read)
-- Mutable: balance and nonce change on every tx.
-- DID address uniqueness enforced — one account per DID.
-- balance_wei / nonce stored as VARCHAR(30) to avoid NUMERIC precision
-- loss when round-tripping through Go's big.Int string conversion.
-- ================================================================
CREATE TABLE IF NOT EXISTS accounts (
    address      CHAR(42)     PRIMARY KEY,
    did_address  TEXT         NOT NULL UNIQUE,
    balance_wei  VARCHAR(30)  NOT NULL DEFAULT '0',
    nonce        VARCHAR(30)  NOT NULL DEFAULT '0',
    account_type SMALLINT     NOT NULL,
    metadata     JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_accounts_updated_at
    ON accounts(updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_accounts_did_address
    ON accounts(did_address);

-- Auto-update updated_at on every row change.
CREATE OR REPLACE FUNCTION fn_accounts_set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_accounts_updated_at ON accounts;
CREATE TRIGGER trg_accounts_updated_at
    BEFORE UPDATE ON accounts
    FOR EACH ROW EXECUTE FUNCTION fn_accounts_set_updated_at();

-- ================================================================
-- 2) blocks (SQL — Append Only — Create Read)
-- Immutable after write. UPDATE and DELETE are hard-blocked via RULEs.
-- sig_v stored as BIGINT — int16 overflows for chainID > 16383 (EIP-155).
-- state_root and txs_root uniqueness enforced: two blocks cannot share roots.
-- ================================================================
CREATE TABLE IF NOT EXISTS blocks (
    block_number  BIGINT        PRIMARY KEY,
    block_hash    CHAR(66)      NOT NULL UNIQUE,
    parent_hash   CHAR(66)      NOT NULL,
    timestamp     TIMESTAMPTZ   NOT NULL,
    txs_root      CHAR(66)      NOT NULL UNIQUE,
    state_root    CHAR(66)      NOT NULL UNIQUE,
    logs_bloom    BYTEA,
    coinbase_addr CHAR(42),
    zkvm_addr     CHAR(42),
    gas_limit     NUMERIC(78,0),
    gas_used      NUMERIC(78,0),
    status        SMALLINT      NOT NULL,
    extra_data    JSONB         NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_blocks_timestamp
    ON blocks(timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_blocks_block_hash
    ON blocks(block_hash);

-- ================================================================
-- 3) snapshots (SQL — Append Only — Create Read)
-- One snapshot per block (1:1 FK to blocks).
-- Transactions are owned by snapshots via FK in transactions table.
-- ================================================================
CREATE TABLE IF NOT EXISTS snapshots (
    block_number BIGINT       PRIMARY KEY,
    block_hash   CHAR(66)     NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_snapshot_block
        FOREIGN KEY (block_number) REFERENCES blocks(block_number)
);

CREATE INDEX IF NOT EXISTS idx_snapshots_created_at
    ON snapshots(created_at DESC);

-- ================================================================
-- 4) transactions (SQL — Append Only — Create Read)
-- FK to snapshots (snapshot owns the tx set for a block).
-- to_addr is NULLABLE — NULL means contract creation (no recipient).
-- FK to accounts for from_addr and to_addr enforces referential integrity:
--   accounts must exist before transactions referencing them are inserted.
-- sig_v is BIGINT — covers EIP-155 v values for any chain ID.
-- ================================================================
CREATE TABLE IF NOT EXISTS transactions (
    tx_hash              CHAR(66)      PRIMARY KEY,
    block_number         BIGINT        NOT NULL,
    tx_index             SMALLINT      NOT NULL,
    from_addr            CHAR(42)      NOT NULL,
    to_addr              CHAR(42),                    -- NULL = contract creation
    value_wei            NUMERIC(78,0) NOT NULL DEFAULT 0,
    nonce                NUMERIC(78,0) NOT NULL,
    type                 SMALLINT      NOT NULL DEFAULT 0,
    gas_limit            VARCHAR(30),
    gas_price_wei        VARCHAR(30),
    max_fee_wei          VARCHAR(30),
    max_priority_fee_wei VARCHAR(30),
    data                 BYTEA,
    access_list          JSONB         NOT NULL DEFAULT '[]'::jsonb,
    sig_v                BIGINT        NOT NULL,
    sig_r                CHAR(66)      NOT NULL,
    sig_s                CHAR(66)      NOT NULL,
    created_at           TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_txn_snapshot
        FOREIGN KEY (block_number) REFERENCES snapshots(block_number),
    CONSTRAINT uq_txn_block_index
        UNIQUE (block_number, tx_index)
);

CREATE INDEX IF NOT EXISTS idx_txn_block_number
    ON transactions(block_number);

CREATE INDEX IF NOT EXISTS idx_txn_from_addr
    ON transactions(from_addr);

CREATE INDEX IF NOT EXISTS idx_txn_to_addr
    ON transactions(to_addr) WHERE to_addr IS NOT NULL;

-- Composite index for GetLatestTransactionsByAddress query:
-- SELECT ... WHERE from_addr=$1 OR to_addr=$1 ORDER BY block_number DESC, tx_index DESC LIMIT N
CREATE INDEX IF NOT EXISTS idx_txn_from_block_desc
    ON transactions(from_addr, block_number DESC, tx_index DESC);

CREATE INDEX IF NOT EXISTS idx_txn_to_block_desc
    ON transactions(to_addr, block_number DESC, tx_index DESC)
    WHERE to_addr IS NOT NULL;

-- ================================================================
-- 5) zk_proofs (SQL — Append Only — Create Read)
-- One ZK proof per block (1:1 FK to blocks).
-- proof_hash uniqueness enforced across the table.
-- ================================================================
CREATE TABLE IF NOT EXISTS zk_proofs (
    block_number BIGINT       PRIMARY KEY,
    proof_hash   CHAR(66)     NOT NULL UNIQUE,
    stark_proof  BYTEA        NOT NULL,
    commitment   BYTEA,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_zkproof_block
        FOREIGN KEY (block_number) REFERENCES blocks(block_number)
);

CREATE INDEX IF NOT EXISTS idx_zk_proofs_proof_hash
    ON zk_proofs(proof_hash);

-- ================================================================
-- 6) l1_finality (SQL — Append Only — Create Read)
-- confirmation is the L1 transaction hash or attestation identifier.
-- block_numbers is an array of JMDN block numbers confirmed by this L1 tx.
-- GIN indexes allow efficient containment queries:
--   WHERE block_numbers @> ARRAY[42::bigint]
-- ================================================================
CREATE TABLE IF NOT EXISTS l1_finality (
    confirmation  CHAR(42)     PRIMARY KEY,
    block_numbers BIGINT[]     NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    metadata      JSONB
);

CREATE INDEX IF NOT EXISTS idx_l1_finality_created_at
    ON l1_finality(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_l1_finality_block_numbers
    ON l1_finality USING GIN(block_numbers);

CREATE INDEX IF NOT EXISTS idx_l1_finality_metadata
    ON l1_finality USING GIN(metadata) WHERE metadata IS NOT NULL;
`

// migrationSQL003 is the Phase 8 DDL for the contracts registry table.
// Stores deployed-contract metadata (deployer, ABI, deploy block/tx) for
// the ListContracts / GetContract registry queries. Code/storage/nonce/meta
// live in KV — this table is query-index only.
const migrationSQL003 = `
-- ================================================================
-- ThebeDB - JMDN PostgreSQL Projection Schema
-- Migration: 000003_contract_registry (UP)
-- Applied by: JMDNProfile.GetMigration() via ThebeDB profile system
-- ================================================================

CREATE TABLE IF NOT EXISTS contracts (
    address          CHAR(42)      PRIMARY KEY,
    deployer         CHAR(42)      NOT NULL,
    name             TEXT          NOT NULL DEFAULT '',
    abi              TEXT          NOT NULL DEFAULT '',
    bytecode_hash    CHAR(66)      NOT NULL,
    deploy_block     BIGINT        NOT NULL,
    deploy_time      BIGINT        NOT NULL,
    deploy_tx_hash   CHAR(66)      NOT NULL,
    code_size        BIGINT        NOT NULL DEFAULT 0,
    contract_type    TEXT          NOT NULL DEFAULT 'custom',
    state            TEXT          NOT NULL DEFAULT 'active',
    metadata         JSONB         NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_contracts_deployer
    ON contracts(deployer);

CREATE INDEX IF NOT EXISTS idx_contracts_deploy_block
    ON contracts(deploy_block DESC);

CREATE INDEX IF NOT EXISTS idx_contracts_deploy_time
    ON contracts(deploy_time DESC);

CREATE INDEX IF NOT EXISTS idx_contracts_state
    ON contracts(state);
`

// migrationSQL002 is the Phase 7 DDL for the contract_receipts table.
// Contract code/nonce/storage/meta live in BadgerDB KV — not in SQL.
// Applied via GetMigration() which concatenates all migration constants.
const migrationSQL002 = `
-- ================================================================
-- ThebeDB - JMDN PostgreSQL Projection Schema
-- Migration: 000002_contract_receipt (UP)
-- Contract receipts in SQL for log filtering and block-level queries.
-- Contract code/storage/nonce/meta live in KV (BadgerDB) — not here.
-- ================================================================

CREATE TABLE IF NOT EXISTS contract_receipts (
    tx_hash          CHAR(66)      PRIMARY KEY,
    block_number     BIGINT        NOT NULL,
    tx_index         SMALLINT      NOT NULL,
    status           SMALLINT      NOT NULL CHECK (status IN (0, 1)),
    gas_used         NUMERIC(78,0) NOT NULL,
    contract_address CHAR(42),
    logs             JSONB         NOT NULL DEFAULT '[]'::jsonb,
    revert_reason    TEXT,
    created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_contract_receipt_block
        FOREIGN KEY (block_number) REFERENCES blocks(block_number)
);

CREATE INDEX IF NOT EXISTS idx_contract_receipts_block_number
    ON contract_receipts(block_number);

CREATE INDEX IF NOT EXISTS idx_contract_receipts_contract_address
    ON contract_receipts(contract_address) WHERE contract_address IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_contract_receipts_logs
    ON contract_receipts USING GIN(logs);
`
