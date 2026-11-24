# Task-wise DB connection pools + account query indexes (v3base)

Follow-up to *RCA: sequencer accountsdb pool exhaustion* (prod `a7c1630`, ImmuDB).
On v3base the 30-slot ImmuDB pool is gone, but every reader still shared one
10-connection Postgres pool, and the account queries scanned the whole table.

## What was wrong on v3base (measured, 200k accounts, Postgres 16)

| Query | Before | After | Used by |
|---|---|---|---|
| `GetAccount` — `WHERE LOWER(address) = LOWER($1)` | Parallel Seq Scan, 112 ms | Index Only Scan, 0.05 ms | JSON-RPC, block apply |
| Listing page — `ORDER BY LOWER(address) LIMIT 3000 OFFSET 150000` | full sort + skip, 343 ms | keyset index range, 7 ms | state fingerprint (every block), FastSync AccountSync |
| `GetAccountsByNonces` — `nonce = ANY($1)` | Parallel Seq Scan, 28 ms | Index Scan, 0.1 ms | FastSync AccountSync pages |

The address primary key could not serve `LOWER(address)`, and `nonce` had no
index. Cost grows linearly with the table (the listing quadratically).

## Changes

1. **Indexes** (startup DDL, `thebeprofile/schema.go`):
   `idx_accounts_address_lower ON accounts(LOWER(address), address)` and
   `idx_accounts_nonce ON accounts(nonce)`.
2. **Keyset pagination.** `ListAccountsAfter` (reader → backend → store) pages
   by `LOWER(address) > LOWER($cursor)`. Used by the FastSync account iterator and
   by `ListAccountsPaginatedFrom` (the P2.5 fingerprint scan). Same order as the
   old OFFSET listing (test `TestAccounts_KeysetEqualsOffsetOrder`), so the
   fingerprint value is unchanged; it cannot repeat a row under concurrent inserts.
3. **Task-wise pools** (`DB_OPs/task_pools.go`). Each task has its own `*sql.DB`:

   | Task | Config / env | Default | Carries |
   |---|---|---|---|
   | write | `thebe.pools.write` / `JMDN_THEBE_POOLS_WRITE` | 10 | ThebeDB engine: 2PC writes, outbox, contract (cassata) reads |
   | read | `thebe.pools.read` / `JMDN_THEBE_POOLS_READ` | 10 | process handle: JSON-RPC, block apply, explorer, CLI |
   | sync | `thebe.pools.sync` / `JMDN_THEBE_POOLS_SYNC` | 4 | serving peers: FastSync account/block/header iterators, ThebeSync provider |

   Paths opt in with `DB_OPs.TaskConn(DB_OPs.TaskSync)`; an unregistered task
   falls back to the process handle. `PG_MAX_OPEN_CONNS` no longer applies.
   Boot warns when the total exceeds `max_connections - superuser_reserved`.
4. **Metrics:** `jmdn_db_task_pool_{max_open,open,in_use,idle,wait_total,wait_seconds_total}{task}`.
   Alert on `in_use / max_open > 0.8` for `read` and on `wait_seconds_total` growth.

Isolation test (`TestTaskPools_SyncLoadCannotStarveReads`, real Postgres):
12 concurrent 200 ms sync scans on a sync budget of 2 → sync never exceeds 2;
RPC reads on the read pool: 0 waits, p99 ≈ 8 ms. Same load on one shared pool
of 5 (today's shape): RPC p99 ≈ 1.0 s.

## Rollout

Not a consensus change; nodes can upgrade one at a time.

- **First boot builds the two indexes** and blocks the node's startup while it
  does: about 19 s for 4.46M accounts on a 2-core sandbox. To avoid that on a large
  node, create them beforehand without blocking writes:
  ```sql
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_address_lower ON accounts(LOWER(address), address);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_nonce ON accounts(nonce);
  ```
  (Startup then skips them via `IF NOT EXISTS`. If a CONCURRENTLY build fails it
  leaves an INVALID index that `IF NOT EXISTS` will not rebuild — drop it first.)
- Default total is 24 connections per node (was 10). Check `max_connections`
  on any Postgres shared by several nodes.

## Not covered here

- **Consensus apply is not split from JSON-RPC** (both on `read`). Most DB_OPs
  helpers call `getHandle(nil)` and ignore their connection argument, so routing
  apply separately means making those honor `conn` first.
- Contract-state reads (cassata) stay on the write pool's `*sql.DB`.
- FastSync items from the RCA: session admission control, context in
  `AccountNonceIterator`, retry backoff/jitter (JMDN-FastSync repo).
