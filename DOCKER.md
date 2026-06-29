# JMDN — Docker Guide

> **Who this is for:** Node operators running JMDN via Docker or Docker Compose. Exchange integrators (MEXC, etc.), DevOps teams, and anyone who doesn't want to manage a Go toolchain manually.

---

## Table of Contents

1. [How Docker Works — The Basics](#1-how-docker-works--the-basics)
2. [JMDN Architecture Inside Docker](#2-jmdn-architecture-inside-docker)
3. [Services Deep Dive](#3-services-deep-dive)
4. [Quick Start (Docker Compose)](#4-quick-start--new-vm-setup)
5. [Running with `docker run` (Standalone)](#5-running-with-docker-run-standalone)
6. [First Run — What Actually Happens](#6-first-run--what-actually-happens)
7. [Configuration](#7-configuration)
8. [Building the Image from Source](#8-building-the-image-from-source)
9. [Debugging — Live Logs, Exec, Inspect](#9-debugging--live-logs-exec-inspect)
10. [Log Retention](#10-log-retention)
11. [Health Checks](#11-health-checks)
12. [Volumes and Data Management](#12-volumes-and-data-management)
13. [Upgrading](#13-upgrading)
14. [Troubleshooting](#14-troubleshooting)

---

## 1. How Docker Works — The Basics

If you're familiar with VMs and systemd, here's the mental model:

| Bare-metal / VM | Docker equivalent |
|---|---|
| Process (`systemd service`) | Container |
| VM disk image | Docker image |
| Running VM | Running container |
| `systemctl start jmdn` | `docker compose up -d` |
| `journalctl -u jmdn -f` | `docker compose logs -f jmdn` |
| `/etc/jmdn/jmdn.yaml` | Volume mount or env var |
| `/opt/jmdn` (JMDN_DATA) | `jmdn-state` named volume |
| `/opt/jmdn/data` (immudb data) | `immudb-data` named volume |
| `systemctl restart jmdn` | `docker compose restart jmdn` |

**Image vs Container:** An image is the blueprint (read-only). A container is a running instance of that image. You can run 10 containers from the same image. Images are built once; containers are created from them.

**Compose:** Docker Compose is an orchestration file (`docker-compose.yml`) that describes which containers to run, how they connect, and what volumes/ports they use. It replaces writing long `docker run` commands by hand.

**Why we still have shell scripts alongside Compose:** Compose handles *which* containers run and *how* they connect. It cannot handle logic *inside* a container — conditional first-run steps, privilege drops, TLS cert generation, or waiting for custom conditions. Those live in the entrypoint script. Every major Docker image (postgres, redis, mysql) ships with an `entrypoint.sh` for exactly this reason.

---

## 2. JMDN Architecture Inside Docker

```
┌─────────────────────────────── docker-compose ──────────────────────────────┐
│                                                                               │
│  ┌──────────────────────────────────────────────────────────────────────┐    │
│  │                         jmdn container                               │    │
│  │                                                                       │    │
│  │  Startup (as root):                                                   │    │
│  │    docker-entrypoint.sh                                               │    │
│  │      ├─ [IMMUDB_EXTERNAL=true] skip bootstrap (runs separately)      │    │
│  │      ├─ ensure DB/, config/, certs/ exist with correct ownership      │    │
│  │      ├─ generate self-signed TLS certs if missing                    │    │
│  │      ├─ nc -z immudb 3322 (wait for immudb container)                │    │
│  │      └─ gosu jmdn → jmdn binary                                       │    │
│  │                                                                       │    │
│  │  Ports (listening as jmdn user):                                      │    │
│  │    :8545  JSON-RPC   ◄── MEXC / exchange connects here                │    │
│  │    :8546  WebSocket                                                   │    │
│  │    :15052 DID service                                                 │    │
│  │    :8090  Explorer API  (localhost only, health check)                │    │
│  └──────────────────────┬────────────────────────────────────────────────┘   │
│                          │                                                    │
│          immudb:3322      │         redis:6379                                │
│  ┌───────────────────────▼─────────────────────────────────────────────┐     │
│  │                     immudb container                                 │     │
│  │   Tamper-proof append-only ledger (codenotary/immudb:1.10.0)        │     │
│  │   jmdn connects via gRPC — no shared volume with jmdn container     │     │
│  └──────────────────────────────────────────────────────────────────────┘    │
│                                                                               │
│  ┌──────────────────────────────────────────────────────────────────────┐    │
│  │                      redis container                                  │    │
│  │   Account sync worker queue (Redis Streams XADD/XREADGROUP/XACK)     │    │
│  │   Decouples WriteAccounts from ImmuDB's ~15s commit latency           │    │
│  │   Data persisted via AOF (appendonly yes)                             │    │
│  └──────────────────────────────────────────────────────────────────────┘    │
│                                                                               │
└───────────────────────────────────────────────────────────────────────────────┘

Named volumes (on host):
  immudb-data → /opt/jmdn/data   (immudb ledger: systemdb, defaultdb, accountsdb)
                                  also mounted :ro in jmdn container for bootstrap sentinel check
  jmdn-state  → /opt/jmdn        (node state: DB/gossipnode.db, config/peer.json, certs/)
  redis-data  → /data            (Redis AOF log)
```

### ImmuDB — separate container (default) vs embedded

The compose stack runs immudb as its own container (`codenotary/immudb:1.10.0`) and jmdn connects to it over the Docker internal network via `JMDN_DATABASE_ADDRESS=immudb`. This is the default and recommended mode.

| | Separate container (compose default) | Embedded (`IMMUDB_EXTERNAL=false`) |
|---|---|---|
| Restart independence | ✓ immudb crash doesn't kill node | ✗ one failure kills both |
| Upgrade | ✓ bump immudb image tag independently | ✗ must rebuild jmdn image |
| Backup | ✓ snapshot `immudb-data` without stopping node | ✗ must stop everything |
| Resource limits | ✓ separate CPU/mem cgroups | ✗ compete for same limit |
| Network latency | ~0.1ms Docker bridge | 0 (loopback) |
| Failure modes | +1 (network between containers) | simpler |

For a public exchange listing, separate is the right call. The 0.1ms latency is irrelevant at immudb's ~15s commit frequency; the operational independence is not.

**Credentials:** Set `IMMUDB_PASSWORD` in `.env` — compose injects it into both the immudb container and jmdn automatically. The immudb service runs with `--force-admin-password`, which resets the admin password to `IMMUDB_PASSWORD` on every start. This is required because bootstrap snapshots come pre-initialized with a baked-in password; without it, immudb ignores `IMMUDB_ADMIN_PASSWORD` on an existing database and jmdn cannot connect.

### Why Redis is separate

Redis is a pure network service with no shared-memory requirements. Running it separately means:
- Independent restart without affecting the node
- Standard Redis tooling (`redis-cli`, monitoring) works normally
- Persistent AOF log survives container restarts
- Easy to upgrade Redis version without touching the node image

### The account sync worker

When jmdn writes account state, it enqueues to a Redis Stream (`XADD`) and returns immediately. A background worker drains the stream and commits batches to ImmuDB. This exists because ImmuDB's commit latency is ~15 seconds — without the queue, every account write would block for 15s. For exchange integrations (where account balance queries must be consistent), Redis must be running.

---

## 3. Services Deep Dive

### `jmdn` (the node)

| Property | Value |
|---|---|
| Image | `ghcr.io/jupitermetalabs/jmdn:latest` |
| Base OS | `debian:bookworm-slim` |
| Runs as | `jmdn` (non-root) after startup |
| Entrypoint | `/usr/local/bin/docker-entrypoint.sh` (root → gosu drop) |
| Main binary | `/usr/local/bin/jmdn` |
| Config file | `/etc/jmdn/jmdn.yaml` (operator-mounted — no default baked in) |
| Peer identity | `/opt/jmdn/config/peer.json` (auto-generated on first run) |
| TLS certs | `/opt/jmdn/certs/` (auto-generated if missing) |
| Working dir | `/opt/jmdn` (matches bare-metal `WorkingDirectory=${JMDN_DATA}`) |

### `immudb` (ledger)

| Property | Value |
|---|---|
| Image | `codenotary/immudb:1.10.0` |
| Data dir | `/opt/jmdn/data` (volume: `immudb-data`) |
| Port | `3322` (internal only — not exposed to host) |
| Health check | None — distroless image (no shell, no wget, no nc) |

### `redis` (account sync queue)

| Property | Value |
|---|---|
| Image | `redis:7-alpine` |
| Persistence | AOF (`appendonly yes`, `appendfsync everysec`) |
| Auth | Password via `REDIS_PASSWORD` env var |
| Usage | Redis Streams (not simple pub/sub — requires Redis 5+) |

### `jmdn-bootstrap` (one-time, profile-gated)

Runs `bootstrap_sync.sh` inside a temporary container that mounts the `immudb-data` volume directly. Downloads the chain snapshot from GCS, verifies checksums, extracts it, and writes a sentinel so it never runs again. Must run before the stack starts for the first time.

```bash
docker compose run --rm jmdn-bootstrap
```

### What the scripts do

**`docker-entrypoint.sh`** — runs on every container start. Ensures `DB/`, `config/`, `certs/` exist under `/opt/jmdn` with correct ownership, generates TLS certs if missing, waits for immudb, then drops to the `jmdn` user via `gosu`. Think of it as the `ExecStartPre=` and `ExecStart=` of a systemd unit, combined.

**`bootstrap_sync.sh`** — runs only once (guarded by `.bootstrapped` sentinel on the `immudb-data` volume). Downloads the chain snapshot from GCS, verifies checksums, extracts it into `/opt/jmdn/data`. Subsequent starts skip this entirely and are fast.

---

## 4. Quick Start — New VM Setup

This is the complete runbook for a fresh VM with Docker already installed.

### Prerequisites

- Docker 24+ and Docker Compose v2 — verify with `docker compose version`
- 50 GB+ free disk space (chain snapshot on first run)
- Open ports on firewall/security group:

| Port | Protocol | Purpose |
|---|---|---|
| 8545 | TCP | JSON-RPC — exchange endpoint |
| 8546 | TCP | WebSocket RPC |
| 15052 | TCP | DID service |
| 8090 | TCP | Explorer API (optional, localhost-only by default) |

### Step 1 — Clone the repo

```bash
mkdir -p /opt/jmdn
git clone https://github.com/JupiterMetaLabs/jmdn.git /opt/jmdn/jmdn
cd /opt/jmdn/jmdn
git checkout main
```

> Running as root? That's fine — Docker itself runs as root. The `jmdn` user only exists **inside the container** and is managed by the entrypoint script. You do not need a `jmdn` user on the host.

### Step 2 — Create a .env file (container passwords only)

The `.env` file is minimal — it exists only because the immudb and redis containers are separate processes that cannot read `jmdn.yaml`. They need their passwords passed in via environment variables. Everything else lives in `jmdn.yaml` (Step 3).

Generate the two passwords:

```bash
openssl rand -base64 32   # → IMMUDB_PASSWORD
openssl rand -base64 32   # → REDIS_PASSWORD
```

```bash
cat > /opt/jmdn/jmdn/.env << 'EOF'
# ImmuDB password — used by the immudb container.
# Must match database.password in jmdn.yaml.
IMMUDB_PASSWORD=<generated>

# Redis password — used by the redis container.
# Must match database.redis.password in jmdn.yaml.
REDIS_PASSWORD=<generated>

# Explorer API key — forwarded into the jmdn container for the health check curl.
# Must match security.explorer_api_key in jmdn.yaml.
JMDN_SECURITY_EXPLORER_API_KEY=<generated>
EOF
```

> `.env` is in `.gitignore` — it will never be committed.

### Step 3 — Create your node config

All configuration lives in one file. Copy the exchange template:

```bash
cd /opt/jmdn/jmdn
cp jmdn_exchange.yaml jmdn.yaml
nano jmdn.yaml
```

Fill in every field marked REQUIRED:

| Field | What to set |
|---|---|
| `node.alias` | A unique name for this node (e.g. `exchange-prod-1`) |
| `logging.service_name` | Same as `node.alias` |
| `network.seednode` | Provided by JupiterMeta offline |
| `network.mempool` | Provided by JupiterMeta offline |
| `database.password` | Leave blank for Docker Compose — compose injects `IMMUDB_PASSWORD` from `.env` automatically. Set only for bare-metal. |
| `database.redis.password` | Leave blank for Docker Compose — compose injects `REDIS_PASSWORD` from `.env` automatically. Set only for bare-metal. |
| `security.jwt_secret` | Generate: `openssl rand -base64 32` |
| `security.explorer_api_key` | Generate: `openssl rand -base64 32` |
| `fastsync.catch_up_from_block` | Bootstrap snapshot tip + 1. Provided with the bootstrap. Leaving at 0 works but causes a slow full genesis scan on every sync cycle. |

`docker-compose.yml` mounts `./jmdn.yaml` into the container at `/etc/jmdn/jmdn.yaml`. The node reads it automatically on startup — no flags needed.

> **This file must exist before running `docker compose up`.** The mount is always active.

### Step 4 — Get the JMDN image

The `redis` and `immudb` images are pulled from Docker Hub automatically. For the JMDN image, choose one option:

**Option A — Pull the pre-built image (recommended)**

JupiterMeta publishes a signed, multi-arch image to GitHub Container Registry on every release. This is the fastest path — no Go toolchain required.

```bash
# Pull the latest release
docker pull ghcr.io/jupitermetalabs/jmdn:latest

# Or pin to a specific version
docker pull ghcr.io/jupitermetalabs/jmdn:v1.2.0
```

To use a specific version in compose, edit `docker-compose.yml`:
```yaml
jmdn:
  image: ghcr.io/jupitermetalabs/jmdn:v1.2.0   # pin to a release tag
```

**Option B — Build from source**

Use this if you need a custom build or a branch that has not been released yet.

```bash
cd /opt/jmdn/jmdn

docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t ghcr.io/jupitermetalabs/jmdn:latest \
  .
```

This takes a few minutes (downloads Go deps, compiles the binary). The `--build-arg` flags embed version metadata into the binary — without them, `./jmdn --version` shows as `unknown`.

### Step 5 — Bootstrap ImmuDB (first time only)

The chain snapshot must be loaded into the `immudb-data` volume **before** the stack starts. This is a one-time step — subsequent restarts skip it entirely.

```bash
docker compose run --rm jmdn-bootstrap
```

This runs `bootstrap_sync.sh` inside a temporary container that mounts the `immudb-data` volume directly. It downloads the chain snapshot from GCS, verifies checksums, extracts it, and writes a sentinel so it never runs again. Can take 10–30 minutes depending on bandwidth.

Expected output:

```
[bootstrap] First run detected — starting bootstrap sync.
[bootstrap] Listing parts from GCS: gs://jmzk-releases/jmdn_bootstrap_2306/...
[bootstrap] Downloading parts to /opt/jmdn/bootstrap_tmp...
[bootstrap] Checksums OK.
[bootstrap] Extracting parts into sandbox: /opt/jmdn/data_tmp/sandbox
[bootstrap] Moving data to /opt/jmdn/data...
[bootstrap] Setting ownership of /opt/jmdn/data to 3322:3322...
[bootstrap] Bootstrap complete. Sentinel written → /opt/jmdn/data/.bootstrapped
```

### Step 6 — Start the stack

```bash
docker compose up -d
docker compose logs -f jmdn
```

Three containers start: `jmdn-immudb`, `jmdn-redis`, `jmdn`. Expected node output:

```
[entrypoint] External ImmuDB mode — skipping bootstrap.
[entrypoint] Sentinel found — immudb-data volume is populated.
[entrypoint] TLS certs generated.
[entrypoint] Waiting for ImmuDB on immudb:3322...
[entrypoint] ImmuDB ready (2s)
[entrypoint] Starting JMDN as jmdn...
```

### Step 7 — Verify the node is healthy

```bash
# All three containers should show (healthy)
docker compose ps

# JSON-RPC (exchange endpoint) — no auth required
curl -s http://localhost:8545 -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Explorer API — requires the API key set in .env
curl -s http://localhost:8090/api/v1/node/version \
  -H "Authorization: Bearer <JMDN_SECURITY_EXPLORER_API_KEY>"
```

---

## 5. Running with `docker run` (Standalone)

Use these commands when you want to run a single JMDN node without Docker Compose — for quick tests, CI, or a minimal setup with embedded ImmuDB.

### Pull the image

```bash
# Latest release
docker pull ghcr.io/jupitermetalabs/jmdn:latest

# Specific version
docker pull ghcr.io/jupitermetalabs/jmdn:v1.0.0
```

> Images are published for **linux/amd64**, **linux/arm64**, and **linux/arm/v7**. Docker pulls the right variant automatically based on your machine's architecture.

### Minimal run (embedded ImmuDB)

The node starts with ImmuDB running inside the same container. On first start, `bootstrap_sync.sh` downloads the chain snapshot automatically before starting the node — this can take 10–30 minutes.

```bash
docker run -d \
  --name jmdn \
  -v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro \
  -v jmdn-data:/opt/jmdn \
  -p 8545:8545 \
  -p 8546:8546 \
  -p 15052:15052 \
  ghcr.io/jupitermetalabs/jmdn:latest
```

| Flag | Purpose |
|---|---|
| `-v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro` | **Required** — node exits with an error if this is missing |
| `-v jmdn-data:/opt/jmdn` | Persists peer identity, certs, DB, and immudb data across restarts |
| `-p 8545:8545` | JSON-RPC (exchange endpoint) |
| `-p 8546:8546` | WebSocket RPC |
| `-p 15052:15052` | DID service |

### With Explorer API enabled

The Explorer API (`/api/v1/node/version`, etc.) is disabled by default. Enable it with `JMDN_PORTS_API`:

```bash
docker run -d \
  --name jmdn \
  -v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro \
  -v jmdn-data:/opt/jmdn \
  -p 8545:8545 \
  -p 8546:8546 \
  -p 15052:15052 \
  -p 8090:8090 \
  -e JMDN_PORTS_API=8090 \
  ghcr.io/jupitermetalabs/jmdn:latest
```

### With all optional services exposed

```bash
docker run -d \
  --name jmdn \
  -v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro \
  -v jmdn-data:/opt/jmdn \
  -p 8545:8545 \
  -p 8546:8546 \
  -p 15052:15052 \
  -p 8090:8090 \
  -p 15050:15050 \
  -p 15055:15055 \
  -e JMDN_PORTS_API=8090 \
  -e JMDN_DATABASE_PASSWORD=your-strong-password \
  ghcr.io/jupitermetalabs/jmdn:latest
```

### Follow startup logs

```bash
docker logs -f jmdn
```

Expected output on first run:

```
[entrypoint] First run detected — starting bootstrap sync.
[bootstrap] Listing parts from GCS: gs://jmzk-releases/jmdn_bootstrap_2306/...
[bootstrap] Downloading parts to /opt/jmdn/bootstrap_tmp...
[bootstrap] Checksums OK.
[bootstrap] Extracting parts into sandbox: /opt/jmdn/data_tmp/sandbox
[bootstrap] Moving data to /opt/jmdn/data...
[bootstrap] Setting ownership of /opt/jmdn/data to 3322:3322...
[bootstrap] Bootstrap complete. Sentinel written → /opt/jmdn/data/.bootstrapped
[entrypoint] TLS certs generated.
[entrypoint] Starting embedded ImmuDB as jmdn (dir: /opt/jmdn/data)...
[entrypoint] Waiting for ImmuDB on 127.0.0.1:3322...
[entrypoint] ImmuDB ready (4s)
[entrypoint] Starting JMDN as jmdn...
```

Subsequent starts are fast — bootstrap is skipped once the sentinel exists.

### Verify the node is up

```bash
# JSON-RPC
curl -s http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}'

# Explorer API (if JMDN_PORTS_API=8090 was set)
curl -s http://localhost:8090/api/v1/node/version
```

### Force re-bootstrap

Delete the sentinel file and restart. The next start downloads a fresh snapshot:

```bash
docker exec jmdn rm /opt/jmdn/data/.bootstrapped
docker restart jmdn
```

### Stop and remove

```bash
docker stop jmdn && docker rm jmdn
# Volume jmdn-data is preserved — re-run with the same -v flag to resume.

# Full reset (deletes all chain data):
docker stop jmdn && docker rm jmdn
docker volume rm jmdn-data
```

### Environment variable overrides

Pass `-e KEY=value` to `docker run` to override defaults:

| Variable | Default | Purpose |
|---|---|---|
| `JMDN_PORTS_API` | `0` (disabled) | Set `8090` to enable Explorer API |
| `JMDN_DATABASE_PASSWORD` | `immudb` | ImmuDB admin password |
| `GCS_BUCKET` | `jmzk-releases` | Bootstrap snapshot bucket |
| `GCS_PREFIX` | `jmdn_bootstrap_2306` | Snapshot path prefix in bucket |
| `PARTS_PREFIX` | `data_backup_23062026.part` | Part filename prefix |
| `IMMUDB_EXTERNAL` | `false` | Set `true` only with docker-compose (separate immudb container) |
| `JMDN_SECURITY_JWT_SECRET` | `""` | JWT signing secret |

---

## 6. First Run — What Actually Happens

Understanding this prevents confusion when something goes wrong.

```
# Step A: bootstrap (run once before the stack)
docker compose run --rm jmdn-bootstrap
        │
        └─ bootstrap_sync.sh
              ├─ Checks /opt/jmdn/data/.bootstrapped  (on immudb-data volume)
              │   EXISTS? → exits immediately (already done)
              │   MISSING? → first run, continue
              ├─ Lists parts in GCS bucket via public HTTP API
              ├─ Downloads all parts to /opt/jmdn/bootstrap_tmp/
              ├─ Downloads checksums.md5, normalises, verifies
              ├─ Backs up any existing /opt/jmdn/data content → /opt/jmdn/backup/
              ├─ Extracts: cat parts* | tar -xzf - into sandbox
              ├─ Finds systemdb/ → that parent dir is the data root
              ├─ Moves data root → /opt/jmdn/data
              ├─ chown -R 3322:3322 /opt/jmdn/data
              └─ touch /opt/jmdn/data/.bootstrapped

# Step B: stack starts
docker compose up -d
        │
        ├─► immudb starts (reads /opt/jmdn/data from immudb-data volume)
        │
        ├─► redis starts (fast, ~1s)
        │
        └─► jmdn starts
                │
                ├─ [root] docker-entrypoint.sh runs
                │
                ├─ IMMUDB_EXTERNAL=true → skip bootstrap
                │
                ├─ check /opt/jmdn/data/.bootstrapped (on immudb-data:ro mount)
                │   MISSING? → exit 1 "Run jmdn-bootstrap first"
                │   EXISTS?  → continue
                │
                ├─ [root] mkdir -p /opt/jmdn/{config,DB,certs}
                │         chown jmdn:jmdn
                │
                ├─ [root] Generate self-signed TLS certs if /opt/jmdn/certs/ca.crt missing
                │
                ├─ nc -z immudb 3322  (waits up to IMMUDB_READY_TIMEOUT=120s by default)
                │
                └─ gosu jmdn → jmdn
```

**Sentinel file:** `/opt/jmdn/data/.bootstrapped` lives on the `immudb-data` volume. As long as this file exists, bootstrap is skipped. Delete it to force a re-download.

**Force re-bootstrap:**
```bash
# Remove sentinel from the immudb-data volume via a temporary alpine container
docker run --rm -v jmdn_immudb-data:/data alpine rm -f /data/.bootstrapped

# Re-run bootstrap, then bring the stack back up
docker compose run --rm jmdn-bootstrap
docker compose restart immudb jmdn
```

---

## 7. Configuration

### Environment variables (recommended for operators)

Set these in `docker-compose.yml` under the `jmdn:` service's `environment:` block, or in a `.env` file in the same directory.

Most configuration belongs in `jmdn.yaml` — set it there. The environment variables below are the exceptions: Docker-mode routing values that must be injected by compose because they differ from a bare-metal install, and the two container passwords that the immudb/redis containers need directly.

| Variable | Value | Purpose |
|---|---|---|
| `IMMUDB_EXTERNAL` | `true` | Tells jmdn to connect to an external immudb container instead of spawning one |
| `JMDN_DATABASE_ADDRESS` | `immudb` | Docker DNS name of the immudb service (overrides `localhost` default) |
| `JMDN_DATABASE_PORT` | `3322` | ImmuDB gRPC port |
| `IMMUDB_PASSWORD` | *(from .env)* | immudb container startup password — must match `database.password` in jmdn.yaml |
| `REDIS_PASSWORD` | *(from .env)* | Redis container `--requirepass` — must match `database.redis.password` in jmdn.yaml |
| `GCS_BUCKET` | `jmzk-releases` | Bootstrap snapshot GCS bucket (bootstrap container only) |
| `GCS_PREFIX` | `jmdn_bootstrap_2306` | Snapshot path prefix (bootstrap container only) |
| `IMMUDB_USER` | `jmdn` | OS user the immudb files are owned by (entrypoint chown) |

All `JMDN_*` vars map directly to `jmdn.yaml` keys with underscores as separators. For example, `JMDN_NETWORK_CHAIN_ID=7000700` sets `network.chain_id`.

### Custom config file

Exchange operators: copy `jmdn_exchange.yaml` (pre-configured for this use case).
All other operators: copy `jmdn_default.yaml` for a blank-slate starting point.

```bash
cp jmdn_exchange.yaml jmdn.yaml   # exchange operators
# or
cp jmdn_default.yaml jmdn.yaml    # all other operators
```

`docker-compose.yml` always mounts `./jmdn.yaml:/etc/jmdn/jmdn.yaml:ro`. The node reads `/etc/jmdn/jmdn.yaml` automatically via Viper — no flags needed.

### Production TLS certificates

By default, self-signed certs are generated on first run and stored in the `jmdn-state` volume at `/opt/jmdn/certs/`. For production, mount real certs:

```bash
# Your certs directory must contain:
#   ca.crt, ca.key
#   cli_admin.crt, cli_admin.key
#   block_ingest_grpc.crt, block_ingest_grpc.key
#   ... (see Scripts/setup_certs.sh for full list)
```

Uncomment in `docker-compose.yml`:
```yaml
volumes:
  - ./certs:/opt/jmdn/certs:ro
```

---

## 8. Building the Image from Source

### When to build locally

The pre-built image (see Step 4, Option A) is sufficient for most operators. Build locally when:
- You need a branch or commit that hasn't been released yet
- You want to verify the binary from source
- You're developing or testing changes

### How CI publishes images

Every push of a `v*` tag to GitHub (e.g. `v1.2.0`) automatically builds and publishes:
- `ghcr.io/jupitermetalabs/jmdn:v1.2.0` — pinnable version tag
- `ghcr.io/jupitermetalabs/jmdn:latest` — updated on every release

Builds cover linux/amd64, linux/arm64, linux/arm/v7 — Docker pulls the right variant automatically.

### Build commands

```bash
# Build the current branch (version metadata embedded)
docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t ghcr.io/jupitermetalabs/jmdn:latest \
  .

# Build a specific release tag
git checkout v1.2.0
docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=v1.2.0 \
  --build-arg GIT_TAG=v1.2.0 \
  -t ghcr.io/jupitermetalabs/jmdn:v1.2.0 \
  .

# Multi-platform build (push to registry)
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t ghcr.io/jupitermetalabs/jmdn:latest \
  --push .

# Use local build in compose (no pull needed)
# Edit docker-compose.yml: comment out `image:`, uncomment `build: .`
docker compose up -d --build
```

The build has two stages:
1. **Builder** (`golang:1.25.3-bookworm`) — downloads deps, compiles binary with `CGO_ENABLED=1`
2. **Runtime** (`debian:bookworm-slim`) — copies binary + scripts, installs runtime deps only

The final image contains no Go toolchain or source code.

---

## 9. Debugging — Live Logs, Exec, Inspect

### View logs

```bash
# Follow all services
docker compose logs -f

# Follow one service
docker compose logs -f jmdn
docker compose logs -f redis
docker compose logs -f immudb

# Last 100 lines
docker compose logs --tail=100 jmdn

# Since a specific time
docker compose logs --since="2024-01-15T10:00:00" jmdn

# Save to file
docker compose logs jmdn > jmdn.log
```

### Shell into a running container

```bash
# Open a bash shell in the jmdn container
docker compose exec jmdn bash

# Check node state files
docker compose exec jmdn ls /opt/jmdn/config/
docker compose exec jmdn ls /opt/jmdn/certs/
docker compose exec jmdn ls /opt/jmdn/DB/

# Check who the jmdn process is running as
docker compose exec jmdn ps aux

# Inspect immudb volume contents (immudb is distroless — no exec)
docker run --rm -v jmdn_immudb-data:/data alpine ls /data/

# Redis health (use the REDIS_PASSWORD value from your .env)
docker compose exec redis redis-cli -a "$REDIS_PASSWORD" ping
docker compose exec redis redis-cli -a "$REDIS_PASSWORD" xlen account_sync_stream
```

### Inspect containers

```bash
# Container status and health
docker compose ps

# Resource usage (CPU, memory, network, disk I/O)
docker stats

# Detailed container config
docker inspect jmdn

# Port mappings
docker compose port jmdn 8545

# Environment variables (careful — may contain secrets)
docker compose exec jmdn env | grep JMDN_
```

### Network debugging

```bash
# Check JSON-RPC is responding
curl -s http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}'

# Check WebSocket
wscat -c ws://localhost:8546

# Check DID service port
nc -zv localhost 15052

# Check immudb reachability from inside jmdn container
docker compose exec jmdn nc -zv immudb 3322
```

### Force restart / recover

```bash
# Restart a single service
docker compose restart jmdn

# Stop and remove containers (volumes preserved)
docker compose down

# Full reset — removes containers AND volumes (all chain data lost)
docker compose down -v
```

---

## 10. Log Retention

### The difference from journald

On a bare-metal node, `journald` handles log collection and rotation automatically:
- Logs go to `/var/log/journal/`
- Rotation is configured in `/etc/systemd/journald.conf` (`SystemMaxUse`, `SystemKeepFree`)
- You query with `journalctl -u jmdn -f`, filter with `--since`, `--until`, `-p err`

**Docker has no journald.** Container logs go to a JSON file on the host. By default, there is no size limit — a busy node will fill your disk. You must configure this.

### Docker's logging drivers

The default driver is `json-file`. Each container writes to:
```
/var/lib/docker/containers/<container-id>/<container-id>-json.log
```

Our `docker-compose.yml` already configures rotation for all services:

```yaml
# jmdn service
logging:
  driver: json-file
  options:
    max-size: "50m"   # rotate when file reaches 50 MB
    max-file: "5"     # keep 5 rotated files = max 250 MB total

# redis service
logging:
  driver: json-file
  options:
    max-size: "10m"
    max-file: "3"
```

This gives jmdn a maximum of 250 MB of log history. Adjust `max-size` and `max-file` to match your disk capacity.

### Querying logs like journalctl

```bash
# equivalent of: journalctl -u jmdn -f
docker compose logs -f jmdn

# equivalent of: journalctl -u jmdn --since "1 hour ago"
docker compose logs --since="1h" jmdn

# equivalent of: journalctl -u jmdn -p err
docker compose logs jmdn 2>&1 | grep -i "error\|fatal\|panic"

# equivalent of: journalctl -u jmdn -n 200
docker compose logs --tail=200 jmdn
```

### Sending logs to a central system (production)

For production operators who need centralised logging (like a Grafana/Loki stack or ELK), switch the logging driver in `docker-compose.yml`:

**Loki (Grafana stack):**
```yaml
logging:
  driver: loki
  options:
    loki-url: "http://loki:3100/loki/api/v1/push"
    loki-batch-size: "400"
    labels: "service=jmdn,env=production"
```
Requires the Loki Docker driver plugin: `docker plugin install grafana/loki-docker-driver:latest --alias loki --grant-all-permissions`

**journald (send Docker logs to host journald):**
```yaml
logging:
  driver: journald
  options:
    tag: "jmdn"
```
After this, `journalctl -t jmdn -f` works exactly like a systemd service. Rotation is handled by journald config as usual.

**Fluentd / Splunk / AWS CloudWatch:** Docker supports these natively via their respective log drivers. See `docker help logging` for the full list.

### Grafana dashboards

A pre-built Grafana dashboard for JMDN is in `grafana/`. It reads from the node's Prometheus metrics endpoint. Enable it:

```yaml
# docker-compose.yml — jmdn service environment
JMDN_PORTS_METRICS: "8081"
JMDN_BINDS_METRICS: "0.0.0.0"  # or 127.0.0.1 if behind a reverse proxy
```

Then configure Prometheus to scrape `localhost:8081/metrics`.

---

## 11. Health Checks

All three services have health checks configured in `docker-compose.yml`. Docker polls them periodically and marks the container `healthy` or `unhealthy`.

```bash
# Check health status
docker compose ps

# Detailed health history (last 5 checks)
docker inspect --format='{{json .State.Health}}' jmdn | python3 -m json.tool
```

### What each check does

| Service | Health check | Interval | Start period |
|---|---|---|---|
| `jmdn` | `GET /api/v1/node/version` → HTTP 200 | 30s | 300s |
| `redis` | `redis-cli ping` | 10s | — |
| `immudb` | none — distroless image (no shell, no wget, no nc) | — | — |

Readiness for immudb is handled by the jmdn entrypoint: it loops `nc -z immudb 3322` for up to 120s (default) before starting the node process. Override with `IMMUDB_READY_TIMEOUT=300` if your host is slow to load a large snapshot.

### If health checks fail

```bash
# See why health check is failing
docker inspect jmdn | python3 -c "
import json,sys
h=json.load(sys.stdin)[0]['State']['Health']
for c in h['Log'][-3:]:
    print(c['ExitCode'], c['Output'])
"

# Common causes:
# jmdn unhealthy  → JMDN_PORTS_API not set to 8090, or node still starting
# redis unhealthy → wrong REDIS_PASSWORD, or redis OOM
# immudb not reachable → check: docker compose logs immudb
```

---

## 12. Volumes and Data Management

```bash
# List all volumes
docker volume ls | grep jmdn

# Inspect a volume (see mount path on host)
docker volume inspect jmdn_immudb-data
docker volume inspect jmdn_jmdn-state

# Find actual data on disk
docker volume inspect jmdn_immudb-data --format '{{.Mountpoint}}'
# → /var/lib/docker/volumes/jmdn_immudb-data/_data

docker volume inspect jmdn_jmdn-state --format '{{.Mountpoint}}'
# → /var/lib/docker/volumes/jmdn_jmdn-state/_data
```

### Volume layout

```
immudb-data volume (mounted at /opt/jmdn/data in immudb container):
├── .bootstrapped          ← sentinel file (delete to force re-bootstrap)
├── systemdb/              ← immudb system database
├── defaultdb/             ← immudb default database
├── accountsdb/            ← immudb accounts database
└── immudb.identifier      ← immudb node identity

jmdn-state volume (mounted at /opt/jmdn in jmdn container):
├── config/
│   └── peer.json          ← node peer identity (auto-generated on first run)
├── certs/                 ← TLS certs (auto-generated or operator-mounted)
│   ├── ca.crt
│   ├── ca.key
│   └── <service>.{crt,key}
└── DB/
    └── gossipnode.db      ← SQLite node manager state
```

### Backup

```bash
# Stop node cleanly before backup
docker compose stop jmdn immudb

# Backup immudb data (chain state)
docker run --rm \
  -v jmdn_immudb-data:/data \
  -v $(pwd)/backups:/backups \
  alpine tar czf /backups/immudb-data-$(date +%Y%m%d).tar.gz -C /data .

# Backup jmdn node state (peer identity, certs, DB)
docker run --rm \
  -v jmdn_jmdn-state:/data \
  -v $(pwd)/backups:/backups \
  alpine tar czf /backups/jmdn-state-$(date +%Y%m%d).tar.gz -C /data .

# Restart
docker compose start immudb jmdn
```

### Restore

```bash
docker compose down

# Restore immudb data
docker run --rm \
  -v jmdn_immudb-data:/data \
  -v $(pwd)/backups:/backups \
  alpine tar xzf /backups/immudb-data-20240115.tar.gz -C /data

# Restore jmdn state
docker run --rm \
  -v jmdn_jmdn-state:/data \
  -v $(pwd)/backups:/backups \
  alpine tar xzf /backups/jmdn-state-20240115.tar.gz -C /data

docker compose up -d
```

---

## 13. Upgrading

### Option A — Upgrade via pre-built image (recommended)

JupiterMeta publishes a new image for every release. To upgrade:

```bash
# 1. Pin to the new version in docker-compose.yml:
#      image: ghcr.io/jupitermetalabs/jmdn:v1.3.0

# 2. Pull the new image
docker compose pull jmdn

# 3. Restart the node (node process restarts — brief downtime)
docker compose up -d jmdn

# 4. Verify
curl -s http://localhost:8090/api/v1/node/version \
  -H "Authorization: Bearer $JMDN_SECURITY_EXPLORER_API_KEY"
```

> After upgrading, check the release notes for any changes to `fastsync.catch_up_from_block` — if the bootstrap snapshot was refreshed, update this value in `jmdn.yaml` and restart.

### Option B — Build and deploy from source

```bash
git fetch --tags
git checkout v1.2.0   # or the branch/commit you want
docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t ghcr.io/jupitermetalabs/jmdn:latest \
  .
docker compose up -d jmdn
```

### Upgrading Redis or ImmuDB

These are separate images and can be upgraded independently:

```bash
# Update image tag in docker-compose.yml, then:
docker compose pull redis
docker compose up -d redis
# jmdn will reconnect automatically
```

---

## 14. Troubleshooting

### Node stuck on bootstrap

```bash
docker compose logs -f jmdn-bootstrap
# Look for: "Downloading parts..." — normal, can take 10-30 min
# Look for: "Checksum verification failed" — GCS data corrupt, retry
# Look for: "No parts found" — check GCS_BUCKET and GCS_PREFIX env vars
```

### `unhealthy` immediately after start

The health check calls `GET /api/v1/node/version` on port 8090. Two common causes:

**1. Explorer API port not enabled.** Requires `ports.api: 8090` in `jmdn.yaml`.

```bash
# Check that the Explorer API port is configured in jmdn.yaml
grep "api:" /opt/jmdn/jmdn/jmdn.yaml
# Should show: api: 8090

# Check that the node process is actually listening
docker compose exec jmdn ss -tlnp | grep 8090
```

**2. Auth token missing or wrong.** When `security.explorer_api_key` is set, the health check curl must send a matching Bearer token. Set `JMDN_SECURITY_EXPLORER_API_KEY` in `.env` to the same value as `security.explorer_api_key` in `jmdn.yaml`.

```bash
# Confirm the health check is sending a token
docker inspect jmdn | python3 -c "
import json,sys
h=json.load(sys.stdin)[0]['State']['Health']
for c in h['Log'][-1:]:
    print(c['Output'])
"
# If output contains '401' → token mismatch or JMDN_SECURITY_EXPLORER_API_KEY not set in .env
```

### ImmuDB not reachable

```bash
docker compose logs immudb
# Check immudb-data volume is populated (bootstrap must have run first)
docker run --rm -v jmdn_immudb-data:/data alpine ls /data/
# Should show: systemdb/ defaultdb/ accountsdb/ immudb.identifier .bootstrapped
# If empty — bootstrap hasn't run. Run: docker compose run --rm jmdn-bootstrap
```

### Redis connection refused

```bash
docker compose ps redis
# If unhealthy: check password mismatch — REDIS_PASSWORD in .env must match --requirepass used by redis
docker compose logs redis
# Test manually (use REDIS_PASSWORD from your .env)
docker compose exec redis redis-cli -a "$REDIS_PASSWORD" ping
```

### Out of disk space

```bash
# Check volume sizes
du -sh /var/lib/docker/volumes/jmdn*/_data

# Clean Docker system (removes unused images, stopped containers, dangling volumes)
docker system prune

# Check log sizes
du -sh /var/lib/docker/containers/*/  | sort -h | tail -10
```

### Peer.json missing after restart

`peer.json` is generated by the node on first run and lives at `/opt/jmdn/config/peer.json` (on the `jmdn-state` volume). The entrypoint ensures `/opt/jmdn/config/` exists with correct ownership on every start — the node regenerates `peer.json` automatically if missing.

### TLS cert errors

```bash
# Force regeneration by removing the certs dir on the jmdn-state volume
docker run --rm -v jmdn_jmdn-state:/data alpine rm -rf /data/certs
docker compose restart jmdn
# Entrypoint will generate fresh self-signed certs
```

### Manual catchup sync

If the node bootstrapped successfully but is missing recent blocks (e.g. blocks produced after the snapshot was taken), trigger a catchup manually from inside the container:

```bash
docker exec -it jmdn jmdn -cmd catchup \
  /ip4/<peer-ip>/tcp/15000/p2p/<peer-id> \
  <from_block>
```

Replace `<peer-ip>`, `<peer-id>`, and `<from_block>` with the target peer's address and the first block after your bootstrap snapshot (e.g. `bootstrapTip + 1`).

Example — catching up from block 11605:

```bash
docker exec -it jmdn jmdn -cmd catchup \
  /ip4/<peer-ip>/tcp/15000/p2p/12D3KooW... \
  11605
```

Follow progress in the node logs:

```bash
docker logs --tail 100 -f jmdn
```

Look for `[CatchUpSync] done in …` to confirm completion. The SyncMonitor will keep the node in sync automatically after that.

### Full container reset (nuclear option)

```bash
# WARNING: this deletes all chain data. You will re-bootstrap from scratch.
docker compose down -v
docker compose run --rm jmdn-bootstrap
docker compose up -d
```
