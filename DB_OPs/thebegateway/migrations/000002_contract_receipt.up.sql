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

CREATE OR REPLACE RULE rule_contract_receipts_no_update AS
    ON UPDATE TO contract_receipts DO INSTEAD NOTHING;

CREATE OR REPLACE RULE rule_contract_receipts_no_delete AS
    ON DELETE TO contract_receipts DO INSTEAD NOTHING;
