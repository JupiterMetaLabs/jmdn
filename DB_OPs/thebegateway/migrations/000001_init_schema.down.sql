-- ================================================================
-- ThebeDB - JMDN PostgreSQL Projection Schema
-- Migration: 000001_init_schema (DOWN)
-- WARNING: Destroys all projection data. KV log (BadgerDB) is unaffected
-- and can be used to rebuild via JMDNProfile replay from sequence 0.
-- ================================================================

-- Drop in reverse FK dependency order.
DROP TABLE IF EXISTS l1_finality;
DROP TABLE IF EXISTS zk_proofs;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS blocks;
DROP TABLE IF EXISTS accounts;

DROP FUNCTION IF EXISTS fn_accounts_set_updated_at();
