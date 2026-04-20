# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

JMDN is a Layer 2 blockchain node implementation in Go. It uses **ImmuDB** (append-only, tamper-proof database) as its backing store and supports cryptographic verification via `VerifiedSet`/`VerifiedGet`. The module name is `gossipnode`.

## Commands

```bash
# Build
make build                  # Produces ./jmdn binary (CGO_ENABLED=1 required)

# Test
make test                   # go test ./... (requires live ImmuDB + seed node)

# Lint & Format
make lint                   # Full codebase lint (golangci-lint)
make lint-new               # Lint only changed files
make fmt                    # Auto-fix formatting
make fmt-check              # Check formatting without modifying

# Run a single test
go test ./DB_OPs/Tests/... -run TestName -v
```

Tests require live infrastructure (ImmuDB + seed node) and are disabled in CI. The CI pipeline runs: `go mod verify` → `go build ./...` → format check → lint.

## Recent Thebe / DualDB Updates (Apr 2026)

The following behavior is now part of this repo and should be preserved when editing related code:

1. **Facade debug APIs for Thebe reads**
   - Added in `gETH/Facade/rpc/thebe_read_routes.go`.
   - Routes under `/debug/thebe/*` read from Cassata/Postgres projection.
   - Includes:
     - status, accounts list/get, blocks list/get, tx-by-hash,
     - block txs/zkproof/snapshot,
     - account nonce, account transactions.

2. **Nonce API semantics**
   - `GET /debug/thebe/accounts/:address/nonce` returns:
   - `latest sent tx nonce + 1` from `transactions.from_addr = address`.
   - If no sent tx exists, returns `"0"`.
   - Implemented via `cassata.GetNextNonceByAddress`.

3. **Address canonicalization for read APIs**
   - Incoming address params are canonicalized using `common.HexToAddress(...).Hex()`
     before SQL lookups to match ingest format.

4. **Migration hardening in `DB_OPs/dualdb/shadow_adapter.go`**
   - Enforce block gas constraint compatibility: `gas_limit >= gas_used`.
   - Normalize tx fees for Thebe schema checks (`chk_txn_fee_model`).
   - Use zero address when tx `from` is nil to satisfy NOT NULL/FK.
   - Ensure `stark_proof` is non-nil (`[]byte{}` fallback).
   - Snapshot `PrevSnapshotID` intentionally left `nil` (not block_number-1).
   - Tx `created_at` currently uses ingest time (`time.Now().UTC()`), not chain block time.

5. **DID/metadata normalization on account ingest**
   - `DIDAddress` normalization supports legacy forms and emits canonical:
   - `did:jmdt:<chain_id>:<lowercase-address>`.
   - Missing account metadata is stored as `{}` (not `null`).

6. **Thebe init in `main.go`**
   - ThebeDB is initialized with a profile registry that registers `thebeprofile.New()`
     when `thebe.enabled` is true.
   - Facade server wires Cassata via `WithCassata(...)` so `/debug/thebe/*` works.

## High-Level Architecture

```
main.go (orchestrator)
├── DB_OPs/          — ImmuDB abstraction (two-DB design: mainDB + accountsDB)
├── gETH/            — Ethereum-compatible gRPC interface
├── Block/           — Block/transaction processing, HTTP API
├── CLI/             — CLI gRPC server for node management
├── FastsyncV2/      — Blockchain synchronization engine
├── messaging/       — P2P (libp2p + Yggdrasil + gossip protocol)
├── AVC/             — Asynchronous Validation Consensus (BFT/BLS/VRF)
├── DID/             — Decentralized Identity (W3C-compliant)
├── crdt/            — CRDTs (LWW-Set, Counter, HashMap, IBLT)
├── Mempool/         — Transaction mempool
└── metrics/         — Prometheus metrics + GRO tracking
```

**Startup sequence in `main.go`:**
1. GRO (Goroutine Orchestrator) initialization
2. DB connection pools init (main + accounts)
3. libp2p host creation
4. gRPC servers: DID (`:15052`), CLI (`:15053`), gETH (`:15054`), Block generator
5. FastSync/FastsyncV2 setup
6. Messaging layer (Yggdrasil + libp2p)
7. Prometheus metrics server (default `:8080`)
8. CLI command loop

## Architecture: DB_OPs Layer

### Two-Database Design

| Database | Pool Variable | Purpose |
|----------|--------------|---------|
| **Main DB** (`jmdn_main_db`) | `mainDBPool` | Blocks, transactions, receipts, latest block tracking |
| **Accounts DB** (`jmdn_accounts_db`) | `accountsPool` | Accounts (by address & DID), balances, nonces |

Both pools are initialized exactly once via `sync.Once`.

### Connection Pool Lifecycle

```
InitPool() → sync.Once ensures single init
GetConnection(ctx) → borrows from pool
PutConnection(conn) → returns to pool
GetConnectionandPutBack(ctx) → auto-return via GRO goroutine (watches ctx.Done())
```

- **GRO (Goroutine Orchestrator)**: Manages goroutine lifecycle; auto-returns connections when context cancels.
- Pool package: `config.ConnectionPool` with Get/Put semantics.
- **mTLS**: Connections use mutual TLS with certs from `.immudb-state/` directory.

### Key Files

| File | Responsibility |
|------|---------------|
| `MainDB_Connections.go` | Main DB pool init, get/put, DB creation, health check |
| `Account_Connections.go` | Accounts DB pool init, get/put, DB creation, health check |
| `immuclient.go` | Core CRUD (Create/Read/SafeCreate/SafeRead/BatchCreate), block & tx operations, retry logic |
| `account_immuclient.go` | Account CRUD, balance updates, nonce management, batch create/restore |
| `Accounts_helper.go` | Convenience wrappers, CountBuilder pattern |
| `DBConstants.go` | Key prefixes, error sentinels, logging constants |
| `Facade_Receipts.go` | On-the-fly receipt generation (not stored), bloom filters |
| `BlockLogs.go` | Log filtering by block range, address, and topics |
| `BulkGetAccounts.go` | Bulk account retrieval via ImmuDB `GetAll` |
| `BulkGetBlock.go` | Bulk block retrieval, `BlockIterator` with configurable batch size |
| `HashMapValidator.go` | CRDT HashMap key validation against DB state |
| `Immudb_AVROfile.go` | Avro OCF export with Snappy compression |
| `immuclient_helper.go` | `GetTransactionsOfBlock()` helper |
| `logger.go` | Zero-allocation Ion async logger factory |

### Key Schema (Prefixes)

| Prefix | Database | Description |
|--------|----------|-------------|
| `address:<hex>` | Accounts | Account data keyed by address |
| `did:<string>` | Accounts | ImmuDB Reference pointing to `address:` key |
| `block:<number>` | Main | Block data by number |
| `block:hash:<hash>` | Main | Block data by hash |
| `tx:<hash>` | Main | Transaction data by hash |
| `receipt:<hash>` | Main | Receipt data by hash |
| `latest_block` | Main | Latest block number (single key) |
| `tx_processing:<hash>` | Main | Transaction processing status (-1 = failed) |

### Core CRUD Primitives (immuclient.go)

| Method | ImmuDB API | Verified? |
|--------|-----------|-----------|
| `Create` | `Set` | No |
| `Read` | `Get` | No |
| `SafeCreate` | `VerifiedSet` | Yes (tamper-proof) |
| `SafeRead` | `VerifiedGet` | Yes (tamper-proof) |
| `BatchCreate` | `ExecAll` | Atomic multi-op |

### Retry & Reconnection

- `withRetry()`: Exponential backoff on gRPC connection errors.
- `isConnectionError()`: Checks gRPC status codes 14 (Unavailable), 1 (Cancelled), 4 (DeadlineExceeded).
- `reconnect()`: Disconnects old client, creates new one, re-selects database.
- `EnsureDBConnection()`: Health check with 3 retries (used on startup).

### Account Operations

- **Create**: Atomic write of `address:` KV + `did:` reference via `ExecAll`. Checks existence first to prevent "Fake Balance Attack".
- **Read**: `GetAccount()` / `GetAccountByDID()` both delegate to `loadAccountByKey()`. DID references auto-resolve to address entries.
- **Update Balance**: Read → update balance+timestamp → write via `SafeCreate` (verified).
- **Batch Restore (Sync)**: Uses **LWW (Last-Writer-Wins)** conflict resolution comparing `UpdatedAt` timestamps. Only writes newer data.
- **Nonce Management**: `CheckNonceDuplicate()`, `GetLatestNonce()`, `CheckNonceAndGetLatest()`.

### Receipt Generation (Facade Pattern)

Receipts are generated **on-the-fly**, not stored:
1. Find transaction by hash
2. Get the containing block
3. Generate receipt with cumulative gas, logs, bloom filter
4. Check `tx_processing:<hash>` for -1 status (failed tx)

### Error Sentinels (DBConstants.go)

`ErrEmptyKey`, `ErrEmptyBatch`, `ErrNilValue`, `ErrNotFound`, `ErrConnectionLost`, `ErrPoolClosed`, `ErrTokenExpired`, `ErrNoAvailableConn`

### Design Decisions

1. **Separate databases** for accounts vs blocks — isolation and independent scaling.
2. **DID as ImmuDB Reference** — avoids data duplication; auto-resolved on Get.
3. **Verified operations for balance updates** — tamper-proof financial data.
4. **On-the-fly receipt generation** — reduces storage, receipts derived from blocks.
5. **Connection auto-return via GRO** — prevents connection leaks using context-aware goroutines.
6. **LWW for sync** — simple, deterministic conflict resolution for distributed account sync.
7. **Existence check before account creation** — prevents overwriting existing accounts with fake balances.

## FastSync V2

`FastsyncV2/` implements the blockchain sync engine used when a new node joins or falls behind:
1. Exchange Merkle roots to identify divergence
2. Compute CRDT HashMaps to find missing keys
3. Batch-transfer missing blocks/accounts via gRPC (`FastSyncV2` endpoint)
4. Verify consistency after transfer

Data is serialized in **Avro OCF format with Snappy compression** (`DB_OPs/Immudb_AVROfile.go`).

## Proto / gRPC

Proto definitions live in `proto/`. The gRPC services are:
- **DID service** — identity registration and propagation
- **CLI service** — remote node management commands
- **gETH service** — Ethereum-compatible RPC (blocks, txs, accounts, events)
- **Block generator service** — block creation API
- **FastSync V2 service** — sync protocol

## Linter Notes

Active linters: `govet`, `ineffassign`, `unused`, `nolintlint`. `staticcheck`, `errcheck`, and `gosec` are disabled pending backlog cleanup — do not re-enable them in a PR without addressing existing violations first.

<!-- code-review-graph MCP tools -->
## MCP Tools: code-review-graph

**IMPORTANT: This project has a knowledge graph. ALWAYS use the
code-review-graph MCP tools BEFORE using Grep/Glob/Read to explore
the codebase.** The graph is faster, cheaper (fewer tokens), and gives
you structural context (callers, dependents, test coverage) that file
scanning cannot.

### When to use graph tools FIRST

- **Exploring code**: `semantic_search_nodes` or `query_graph` instead of Grep
- **Understanding impact**: `get_impact_radius` instead of manually tracing imports
- **Code review**: `detect_changes` + `get_review_context` instead of reading entire files
- **Finding relationships**: `query_graph` with callers_of/callees_of/imports_of/tests_for
- **Architecture questions**: `get_architecture_overview` + `list_communities`

Fall back to Grep/Glob/Read **only** when the graph doesn't cover what you need.

### Key Tools

| Tool | Use when |
|------|----------|
| `detect_changes` | Reviewing code changes — gives risk-scored analysis |
| `get_review_context` | Need source snippets for review — token-efficient |
| `get_impact_radius` | Understanding blast radius of a change |
| `get_affected_flows` | Finding which execution paths are impacted |
| `query_graph` | Tracing callers, callees, imports, tests, dependencies |
| `semantic_search_nodes` | Finding functions/classes by name or keyword |
| `get_architecture_overview` | Understanding high-level codebase structure |
| `refactor_tool` | Planning renames, finding dead code |

### Workflow

1. The graph auto-updates on file changes (via hooks).
2. Use `detect_changes` for code review.
3. Use `get_affected_flows` to understand impact.
4. Use `query_graph` pattern="tests_for" to check coverage.
