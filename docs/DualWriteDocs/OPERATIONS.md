# Operations Guide — Dual-Write & ThebeDB

> **Audience:** DevOps, SRE, or any engineer running or troubleshooting a JMDN node with ThebeDB enabled.

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Configuration Reference](#configuration-reference)
3. [Running a Node with ThebeDB](#running-a-node-with-thebedb)
4. [Backfill Operations](#backfill-operations)
5. [Admin HTTP API](#admin-http-api)
6. [Port Reference](#port-reference)
7. [Postgres Setup](#postgres-setup)
8. [Troubleshooting](#troubleshooting)

---

## Prerequisites

- ImmuDB running (existing setup unchanged)
- PostgreSQL ≥ 14 installed and running on port **5433** (not 5432 — see [Port Reference](#port-reference))
- A Postgres database named `jmdn_thebe` created
- Go ≥ 1.21

---

## Configuration Reference

The Postgres DSN can be set three ways. Priority order (highest wins):

### 1. CLI flag
```bash
./jmdn --thebe-dsn "postgres://postgres:postgres@127.0.0.1:5433/jmdn_thebe?sslmode=disable"
```

### 2. Environment variable
```bash
export JMDN_DATABASE_POSTGRES_DSN="postgres://postgres:postgres@127.0.0.1:5433/jmdn_thebe?sslmode=disable"
./jmdn
```

### 3. YAML config (`jmdn_default.yaml` or any config file)
```yaml
database:
  postgres_dsn: "postgres://postgres:postgres@127.0.0.1:5433/jmdn_thebe?sslmode=disable"
```

### All environment variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `JMDN_DATABASE_POSTGRES_DSN` | `postgres://postgres:postgres@127.0.0.1:5433/jmdn_thebe?sslmode=disable` | Postgres connection string for ThebeDB |
| `BACKFILL_ENABLED` | `false` | Set to `true` or `1` to auto-start backfill at node startup |
| `ADMIN_PORT` | *(unset)* | If set, binds admin HTTP server on `127.0.0.1:{ADMIN_PORT}` |
| `ADMIN_TOKEN` | *(unset)* | If set, `X-Admin-Token` header required for admin API calls |

---

## Running a Node with ThebeDB

ThebeDB requires two paths:
- **KV path** — local filesystem directory for the embedded key-value store (PebbleDB)
- **SQL path** — PostgreSQL DSN

These are wired in `main.go` as `RepositoryConfig`:
```go
RepositoryConfig{
    ThebeDB_KVPath:  "/var/jmdn/thebe_kv",
    ThebeDB_SQLPath: cfg.Database.PostgresDSN,
}
```

If either path is empty, ThebeDB is **skipped** and the node runs ImmuDB-only (legacy mode). This is the safe default for nodes that have not yet set up Postgres.

### Startup log indicators

When ThebeDB initializes successfully, look for:
```
[Migration] ThebeDB blocks fully synced up to <N>.
```
or
```
[Migration] Auto-Backfilling blocks 0 to <N>
```
(if `BACKFILL_ENABLED=true`)

If ThebeDB fails to init, the node logs an error and **continues in ImmuDB-only mode** — it does not crash.

---

## Backfill Operations

Backfill copies historical blocks and accounts from ImmuDB → ThebeDB.

### Option A: Auto-start at startup
```bash
BACKFILL_ENABLED=true ./jmdn
```
The backfill worker runs in a background goroutine. Progress is logged to stdout:
```
[Migration] Auto-Backfilling blocks 0 to 14823
[Migration] Block backfill complete up to 14823
[Migration] Found 342 accounts in ImmuDB
[Migration] Account backfill complete.
```

### Option B: On-demand via admin API

Start the node without `BACKFILL_ENABLED`, then trigger manually:
```bash
# Start
curl -X POST http://127.0.0.1:9090/admin/backfill/start \
  -H "X-Admin-Token: your-secret-token"

# Check status
curl http://127.0.0.1:9090/admin/backfill/status \
  -H "X-Admin-Token: your-secret-token"

# Stop
curl -X POST http://127.0.0.1:9090/admin/backfill/stop \
  -H "X-Admin-Token: your-secret-token"
```

### Status response format
```json
{
  "status": "running",
  "started_at": "2026-04-02T10:00:00Z",
  "finished_at": "0001-01-01T00:00:00Z",
  "current_block": 7412,
  "blocks_done": 7412,
  "error_count": 0,
  "last_error": ""
}
```

Possible status values: `idle`, `running`, `done`, `failed`, `stopped`

### Backfill is resumable

Backfill resumes from where it left off. The state is tracked by:
```sql
SELECT MAX(block_number) FROM blocks;
```
If the node restarts mid-backfill, it picks up from `MAX(block_number) + 1`.

### Backfill is idempotent

Both blocks and accounts use `ON CONFLICT DO NOTHING`. Running backfill multiple times is safe — duplicate rows are silently skipped.

### Batch sizes and throttling

Defaults (in `migration_config.go`):
- **50 blocks** per batch → 200ms sleep
- **100 accounts** per batch → 200ms sleep

These prevent the backfill from flooding ImmuDB with reads during normal node operation.

---

## Admin HTTP API

Enable by setting `ADMIN_PORT`:
```bash
ADMIN_PORT=9090 ADMIN_TOKEN=mysecret ./jmdn
```

The server binds on `127.0.0.1:9090` — **localhost only**, never exposed externally.

| Method | Endpoint | Description | Success | Error |
|--------|----------|-------------|---------|-------|
| `POST` | `/admin/backfill/start` | Start backfill | `202 {"status":"started"}` | `409 {"error":"backfill already running"}` |
| `POST` | `/admin/backfill/stop` | Stop backfill | `200 {"status":"stopped"}` | — |
| `GET` | `/admin/backfill/status` | Progress JSON | `200 Progress{}` | — |

If `ADMIN_TOKEN` is unset, all requests are accepted without auth (dev mode).

---

## Port Reference

| Service | Port | Protocol | Notes |
|---------|------|----------|-------|
| ImmuDB gRPC | 3322 | gRPC | Main ImmuDB client port |
| ImmuDB PG wire | 5432 | Postgres | ImmuDB's Postgres-compatible interface |
| **ThebeDB / Real Postgres** | **5433** | Postgres | Must not be 5432 (conflicts with ImmuDB above) |
| Admin HTTP | `$ADMIN_PORT` | HTTP | Localhost only; disabled if unset |

---

## Postgres Setup

### Using the setup script
```bash
./Scripts/setup_dependencies.sh --postgres
```

This installs PostgreSQL, patches `postgresql.conf` to use port **5433**, creates the `jmdn_thebe` database, and starts the service.

### Manual setup
```bash
# Install PostgreSQL (Ubuntu/Debian)
sudo apt install postgresql

# Change port to 5433 in /etc/postgresql/<version>/main/postgresql.conf
sudo sed -i "s/^#*port = .*/port = 5433/" /etc/postgresql/*/main/postgresql.conf

# Restart
sudo systemctl restart postgresql

# Create database
sudo -u postgres psql -p 5433 -c "CREATE DATABASE jmdn_thebe;"
```

### Schema

Tables are created automatically by `StateTracker.EnsureSchema()` on first backfill start (or node startup if `BACKFILL_ENABLED=true`). You do not need to run migrations manually.

Tables created:
- `accounts` — on-chain accounts with balance, nonce, DID, metadata
- `blocks` — ZK blocks with hash, roots, coinbase, gas
- `transactions` — individual transactions with full EIP-2718 fields
- `zk_proofs` — STARK proofs and KZG commitments per block

### Useful queries for operations

```sql
-- How many blocks are in ThebeDB?
SELECT MAX(block_number) FROM blocks;

-- How many accounts?
SELECT COUNT(*) FROM accounts;

-- Recent blocks
SELECT block_number, block_hash, status, timestamp FROM blocks ORDER BY block_number DESC LIMIT 10;

-- Backfill lag (difference between ImmuDB head and ThebeDB head)
-- Run this and compare to what ImmuDB reports as latest block
SELECT MAX(block_number) AS thebe_head FROM blocks;
```

---

## Troubleshooting

### Node crashes on startup: "failed to start ThebeDB"
- Check Postgres is running on port 5433: `pg_isready -p 5433`
- Check the DSN is correct and the database `jmdn_thebe` exists
- Check user has CREATE TABLE permissions

### "failed to ensure schema"
- ThebeDB connected but Postgres user lacks DDL permissions
- Grant: `GRANT ALL PRIVILEGES ON DATABASE jmdn_thebe TO postgres;`

### Backfill shows "failed to fetch block N from ImmuDB"
- ImmuDB is temporarily unresponsive — backfill skips the block and continues (soft error)
- These are counted in `error_count` in the status response
- If `error_count` grows rapidly, check ImmuDB health

### Backfill returns `409 Conflict`
- A backfill is already running
- Check status: `GET /admin/backfill/status`
- If status is `running` but you believe it's stuck, call stop then start again

### ImmuDB connection fails with "connection refused" on macOS
- Cause: `localhost` resolving to IPv6 `::1`, ImmuDB bound to IPv4
- Fixed in this branch: `DBAddress = "127.0.0.1"` in `config/ImmudbConstants.go`
- If still failing, check ImmuDB's bind address in its config

### Writes succeed on ImmuDB but ThebeDB shows nothing
- ThebeDB writes are async — small delay is normal
- If delay is large, check GRO goroutine queue depth
- If ThebeDB is down, writes fail silently (by design — ImmuDB is primary)
- Restart backfill to catch up

### Tracing shows `thebe_fallback` for reads on recent blocks
- Expected during migration — ThebeDB has not yet received those blocks via async write
- Fallback to ImmuDB is correct behavior; no action needed
- If this persists after migration is complete, check the async write goroutine logs for errors

---

*Document date: 2026-04-02. Branch: `fix/DB_DualWrite`.*
