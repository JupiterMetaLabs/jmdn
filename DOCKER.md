# JMDN — Docker Guide

> **Who this is for:** Node operators running JMDN via Docker or Docker Compose. Exchange integrators (MEXC, etc.), DevOps teams, and anyone who doesn't want to manage a Go toolchain manually.

---

## Table of Contents

1. [How Docker Works — The Basics](#1-how-docker-works--the-basics)
2. [JMDN Architecture Inside Docker](#2-jmdn-architecture-inside-docker)
3. [Services Deep Dive](#3-services-deep-dive)
4. [Quick Start](#4-quick-start)
5. [First Run — What Actually Happens](#5-first-run--what-actually-happens)
6. [Configuration](#6-configuration)
7. [Building the Image from Source](#7-building-the-image-from-source)
8. [Debugging — Live Logs, Exec, Inspect](#8-debugging--live-logs-exec-inspect)
9. [Log Retention](#9-log-retention)
10. [Health Checks](#10-health-checks)
11. [Volumes and Data Management](#11-volumes-and-data-management)
12. [Upgrading](#12-upgrading)
13. [Troubleshooting](#13-troubleshooting)

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
| `/opt/jmdn/data` | Named volume |
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
│  │      ├─ bootstrap_sync.sh  [first run only]                           │    │
│  │      │    └─ downloads snapshot from GCS, verifies, extracts         │    │
│  │      │       chowns data to jmdn user, writes .bootstrapped sentinel  │    │
│  │      ├─ restore peer.json + generate TLS certs if missing             │    │
│  │      ├─ gosu jmdn → immudb   [embedded, localhost:3322]               │    │
│  │      └─ gosu jmdn → jmdn binary                                       │    │
│  │                                                                       │    │
│  │  Ports (listening as jmdn user):                                      │    │
│  │    :8545  JSON-RPC   ◄── MEXC / exchange connects here                │    │
│  │    :8546  WebSocket                                                   │    │
│  │    :15052 DID service                                                 │    │
│  │    :8090  Explorer API  (localhost only, health check)                │    │
│  │    :3322  ImmuDB        (localhost only, internal)                    │    │
│  └──────────────────────┬────────────────────────────────────────────────┘   │
│                          │                                                    │
│                          │ redis:6379 (Docker internal network)               │
│  ┌───────────────────────▼──────────────────────────────────────────────┐    │
│  │                      redis container                                  │    │
│  │   Account sync worker queue (Redis Streams XADD/XREADGROUP/XACK)     │    │
│  │   Decouples WriteAccounts from ImmuDB's ~15s commit latency           │    │
│  │   Data persisted via AOF (appendonly yes)                             │    │
│  └──────────────────────────────────────────────────────────────────────┘    │
│                                                                               │
└───────────────────────────────────────────────────────────────────────────────┘

Named volumes (on host):
  jmdn-data   → /opt/jmdn/data   (immudb state, peer identity, certs, fastsync)
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

**Credentials:** ImmuDB uses `immudb`/`immudb` as built-in defaults. Both services must use the same password. To harden: set `IMMUDB_PASSWORD=your-strong-password` in a `.env` file — compose picks it up automatically for both services.

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
| Image | `ghcr.io/jupitermeta/jmdn:latest` |
| Base OS | `debian:bookworm-slim` |
| Runs as | `jmdn` (non-root) after bootstrap |
| Entrypoint | `/usr/local/bin/docker-entrypoint.sh` (root → gosu drop) |
| Main binary | `/usr/local/bin/jmdn` |
| Config file | `/etc/jmdn/jmdn.yaml` (baked-in default, override via volume) |
| Peer identity | `/opt/jmdn/data/config/peer.json` |
| TLS certs | `/opt/jmdn/data/certs/` (auto-generated if missing) |

### `redis` (account sync queue)

| Property | Value |
|---|---|
| Image | `redis:7-alpine` |
| Persistence | AOF (`appendonly yes`, `appendfsync everysec`) |
| Auth | Password via `REDIS_PASSWORD` env var |
| Usage | Redis Streams (not simple pub/sub — requires Redis 5+) |

### What the scripts do

**`docker-entrypoint.sh`** — runs on every container start. Orchestrates the startup sequence, handles privilege drop via `gosu`, generates TLS certs if missing. Think of it as the `ExecStartPre=` and `ExecStart=` of a systemd unit, combined.

**`bootstrap_sync.sh`** — runs only on the first container start (guarded by `.bootstrapped` sentinel file on the volume). Downloads the chain snapshot from GCS, verifies checksums, extracts it. Subsequent starts skip this entirely and are fast.

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
# Clone to /opt/jmdn-node (or any directory you prefer)
sudo mkdir -p /opt/jmdn-node
sudo chown $USER /opt/jmdn-node
git clone https://github.com/JupiterMetaLabs/jmdn.git /opt/jmdn-node
cd /opt/jmdn-node
```

### Step 2 — Create a .env file (credentials + secrets)

The `.env` file lives next to `docker-compose.yml`. Docker Compose reads it automatically.

```bash
cat > /opt/jmdn-node/.env << 'EOF'
# ImmuDB password — must match in both immudb and jmdn services.
# Default "immudb" works but change this before exposing to the internet.
IMMUDB_PASSWORD=immudb

# Redis password
REDIS_PASSWORD=jmdnredissync

# Node alias (shows up in explorer)
# JMDN_NODE_ALIAS=my-node

# Secrets — set before mainnet
# JMDN_SECURITY_JWT_SECRET=
# JMDN_SECURITY_EXPLORER_API_KEY=

# Alerting (optional Telegram)
# JMDN_ALERTS_URL=https://tg.jmdt.io/multi-channel
# JMDN_ALERTS_API_KEY=
# JMDN_ALERTS_CHAT_ID=
EOF
```

> `.env` is in `.gitignore` — it will never be committed.

### Step 3 — (Optional) Custom node config

If you need to override any node settings beyond what env vars cover, copy the default config and edit it:

```bash
cp /opt/jmdn-node/jmdn_default.yaml /opt/jmdn-node/jmdn.yaml
# edit /opt/jmdn-node/jmdn.yaml as needed
```

Then uncomment this line in `docker-compose.yml` under the `jmdn:` service volumes:
```yaml
- ./jmdn.yaml:/etc/jmdn/jmdn.yaml:ro
```

If you skip this step, the baked-in default config is used — which is fine for most operators.

### Step 4 — Start the stack

```bash
cd /opt/jmdn-node
docker compose up -d
```

Three containers start: `jmdn-immudb`, `jmdn-redis`, `jmdn`. The jmdn container waits for both to be healthy before starting.

### Step 5 — Watch first-run bootstrap

First run downloads the chain snapshot from GCS (can take 10–30 minutes depending on bandwidth):

```bash
docker compose logs -f jmdn
```

Expected output:

```
[bootstrap] First run detected — starting bootstrap sync.
[bootstrap] Listing parts from GCS: gs://jmzk-releases/jmdn_bootstrap_2306/...
[bootstrap] Downloading parts to /opt/jmdn/bootstrap_tmp...
[bootstrap] Checksums OK.
[bootstrap] Extraction complete. Data root: /opt/jmdn/data
[bootstrap] Fixing permissions...
[bootstrap] Bootstrap complete. Sentinel written → /opt/jmdn/data/.bootstrapped
[entrypoint] peer.json present.
[entrypoint] TLS certs present.
[entrypoint] External ImmuDB mode — waiting for immudb:3322...
[entrypoint] ImmuDB ready (2s)
[entrypoint] Starting JMDN as jmdn...
```

Subsequent restarts skip bootstrap entirely and start in seconds.

### Step 6 — Verify the node is healthy

```bash
# All three containers should show (healthy)
docker compose ps

# JSON-RPC (exchange endpoint)
curl -s http://localhost:8545 -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Node version
curl -s http://localhost:8090/api/v1/node/version
```

---

## 5. First Run — What Actually Happens

Understanding this prevents confusion when something goes wrong.

```
docker compose up -d
        │
        ├─► redis starts (fast, ~1s)
        │
        └─► jmdn starts
                │
                ├─ [root] docker-entrypoint.sh runs
                │
                ├─ [root] bootstrap_sync.sh
                │         ├─ Checks /opt/jmdn/data/.bootstrapped
                │         │   EXISTS? → exits immediately (subsequent runs)
                │         │   MISSING? → first run, continue
                │         ├─ Lists parts in GCS bucket via public HTTP API
                │         ├─ Downloads all parts to /opt/jmdn/bootstrap_tmp/
                │         ├─ Downloads checksums.md5, normalises, verifies
                │         ├─ Backs up existing /opt/jmdn/data → /opt/jmdn/backup/
                │         ├─ Extracts: cat parts* | tar -xzf - into sandbox
                │         ├─ Finds systemdb/ → that parent dir is the data root
                │         ├─ Moves data root → /opt/jmdn/data
                │         ├─ chown -R jmdn:jmdn /opt/jmdn/data
                │         └─ touch /opt/jmdn/data/.bootstrapped
                │
                ├─ [root] Restore peer.json if missing
                ├─ [root] Generate TLS certs if missing
                │
                ├─ gosu jmdn → immudb --dir /opt/jmdn/data
                ├─ Wait for immudb on :3322 (up to 30s)
                │
                └─ gosu jmdn → jmdn -config /etc/jmdn/jmdn.yaml
```

**Sentinel file:** `/opt/jmdn/data/.bootstrapped` lives on the volume. As long as this file exists, bootstrap is skipped. Delete it to force a re-download.

**Force re-bootstrap:**
```bash
docker compose exec jmdn rm /opt/jmdn/data/.bootstrapped
docker compose restart jmdn
```

---

## 6. Configuration

### Environment variables (recommended for operators)

Set these in `docker-compose.yml` under the `jmdn:` service's `environment:` block, or in a `.env` file in the same directory.

| Variable | Default | Purpose |
|---|---|---|
| `JMDN_NODE_ALIAS` | `""` | Human-readable node name |
| `JMDN_DATABASE_ADDRESS` | `localhost` | ImmuDB host (internal, don't change for embedded) |
| `JMDN_DATABASE_PORT` | `3322` | ImmuDB port |
| `JMDN_DATABASE_USERNAME` | `""` | ImmuDB username (not P2P — local DB only) |
| `JMDN_DATABASE_PASSWORD` | `""` | ImmuDB password |
| `JMDN_DATABASE_REDIS_URL` | `127.0.0.1:6379` | Redis address |
| `JMDN_DATABASE_REDIS_PASSWORD` | `jmdnredissync` | Redis password |
| `JMDN_PORTS_API` | `0` | Enable Explorer API (set `8090`) |
| `JMDN_BINDS_API` | `127.0.0.1` | Explorer API bind address |
| `JMDN_SECURITY_JWT_SECRET` | `""` | JWT signing secret |
| `JMDN_SECURITY_EXPLORER_API_KEY` | `""` | Explorer API key |
| `JMDN_ALERTS_URL` | `""` | Telegram alert webhook |
| `GCS_BUCKET` | `jmzk-releases` | Bootstrap snapshot GCS bucket |
| `GCS_PREFIX` | `jmdn_bootstrap_2306` | Snapshot path prefix |
| `REDIS_PASSWORD` | `jmdnredissync` | Redis auth password |
| `IMMUDB_EXTERNAL` | `false` | `true` = skip embedded immudb (future use) |
| `JMDN_USER` | `jmdn` | OS user for privilege drop |

All `JMDN_*` vars map directly to `jmdn_default.yaml` keys with underscores as separators. For example, `JMDN_NETWORK_CHAIN_ID=8000800` sets `network.chain_id`.

### Custom config file (advanced)

Copy and edit the default config, then mount it:

```bash
cp jmdn_default.yaml jmdn.yaml
# edit jmdn.yaml as needed
```

Uncomment in `docker-compose.yml`:
```yaml
volumes:
  - ./jmdn.yaml:/etc/jmdn/jmdn.yaml:ro
```

### Production TLS certificates

By default, self-signed certs are generated on first run and stored in the `jmdn-data` volume. For production, mount real certs:

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
  - ./certs:/opt/jmdn/data/certs:ro
```

---

## 7. Building the Image from Source

```bash
# Standard build
docker build -t jmdn:local .

# With version metadata embedded
docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t jmdn:local .

# Multi-platform build (for ARM64 servers / Apple Silicon CI)
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t ghcr.io/jupitermeta/jmdn:latest \
  --push .

# Use local build in compose instead of pulling
# Edit docker-compose.yml: comment out `image:`, uncomment `build: .`
docker compose up -d --build
```

The build has two stages:
1. **Builder** (`golang:1.25.3-bookworm`) — downloads deps, compiles binary with `CGO_ENABLED=1`
2. **Runtime** (`debian:bookworm-slim`) — copies binary + scripts, installs runtime deps only

The final image does NOT contain the Go toolchain or source code.

---

## 8. Debugging — Live Logs, Exec, Inspect

### View logs

```bash
# Follow all services
docker compose logs -f

# Follow one service
docker compose logs -f jmdn
docker compose logs -f redis

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

# Run a single command
docker compose exec jmdn cat /opt/jmdn/data/.bootstrapped
docker compose exec jmdn ls /opt/jmdn/data/certs/

# Check who the jmdn process is running as
docker compose exec jmdn ps aux

# Check immudb state files
docker compose exec jmdn ls /opt/jmdn/data/.immudb_state/

# Redis health
docker compose exec redis redis-cli -a jmdnredissync ping
docker compose exec redis redis-cli -a jmdnredissync xlen account_sync_stream
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

# Check from inside the container (immudb internal port)
docker compose exec jmdn nc -zv localhost 3322
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

## 9. Log Retention

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

## 10. Health Checks

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
| `jmdn` | `GET /api/v1/node/version` → HTTP 200 | 30s | 120s |
| `redis` | `redis-cli ping` | 10s | — |
| `immudb` | TCP connect to `:3322` | 10s | 15s |

The `start_period` for jmdn is 120s — this covers the worst-case first-run bootstrap (snapshot download + extraction). During this window Docker won't count failed health checks against the container.

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
# jmdn unhealthy  → JMDN_PORTS_API not set, or node still bootstrapping
# redis unhealthy → wrong REDIS_PASSWORD, or redis OOM
# immudb unhealthy → immudb crashed, check: docker compose logs immudb
```

---

## 11. Volumes and Data Management

```bash
# List all volumes
docker volume ls | grep jmdn

# Inspect a volume (see mount path on host)
docker volume inspect jmdn-dev_jmdn-data

# Find actual data on disk
docker volume inspect jmdn-dev_jmdn-data --format '{{.Mountpoint}}'
# → /var/lib/docker/volumes/jmdn-dev_jmdn-data/_data
```

### Volume layout

```
jmdn-data volume (mounted at /opt/jmdn/data):
├── .bootstrapped          ← sentinel file (delete to force re-bootstrap)
├── config/
│   └── peer.json          ← node peer identity (restored by entrypoint)
├── certs/                 ← TLS certs (auto-generated or mounted)
│   ├── ca.crt
│   ├── ca.key
│   └── <service>.{crt,key}
├── .immudb_state/         ← ImmuDB client mTLS state
├── data/                  ← ImmuDB ledger data
│   └── systemdb/
└── DB/                    ← ImmuDB database files
```

### Backup

```bash
# Stop node cleanly before backup
docker compose stop jmdn

# Backup the entire volume
docker run --rm \
  -v jmdn-dev_jmdn-data:/data \
  -v $(pwd)/backups:/backups \
  alpine tar czf /backups/jmdn-data-$(date +%Y%m%d).tar.gz -C /data .

# Restart
docker compose start jmdn
```

### Restore

```bash
docker compose down
# Restore from backup
docker run --rm \
  -v jmdn-dev_jmdn-data:/data \
  -v $(pwd)/backups:/backups \
  alpine tar xzf /backups/jmdn-data-20240115.tar.gz -C /data
docker compose up -d
```

---

## 12. Upgrading

### Pull new image

```bash
# Pull latest
docker compose pull jmdn

# Restart with new image (zero downtime not guaranteed — node process restarts)
docker compose up -d jmdn

# Verify new version
curl -s http://localhost:8090/api/v1/node/version
```

### Build and deploy from source

```bash
git pull origin main
docker compose build jmdn
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

## 13. Troubleshooting

### Node stuck on bootstrap

```bash
docker compose logs -f jmdn
# Look for: "Downloading parts..." — normal, can take 10-30 min
# Look for: "Checksum verification failed" — GCS data corrupt, retry
# Look for: "No parts found" — check GCS_BUCKET and GCS_PREFIX env vars
```

### `unhealthy` immediately after start

```bash
# Check if Explorer API is enabled
docker compose exec jmdn env | grep JMDN_PORTS_API
# Should be 8090. If empty, health check cannot connect.
```

### ImmuDB won't start

```bash
docker compose logs jmdn | grep -i immudb
# "ImmuDB did not start within 30s" — increase IMMUDB_READY_TIMEOUT
# Permission errors — volume may be owned by wrong user
docker compose exec jmdn ls -la /opt/jmdn/data/
# Should be owned by jmdn:jmdn
```

### Redis connection refused

```bash
docker compose ps redis
# If unhealthy: check password mismatch
# REDIS_PASSWORD in jmdn env must match the --requirepass used by redis
docker compose logs redis
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

This is normal — `docker-entrypoint.sh` restores it from `/etc/jmdn/peer.json` (baked into the image) automatically on every start. No action needed.

### TLS cert errors

```bash
# Force regeneration by removing the certs dir on the volume
docker compose exec jmdn rm -rf /opt/jmdn/data/certs
docker compose restart jmdn
# Entrypoint will generate fresh self-signed certs
```

### Full container reset (nuclear option)

```bash
# WARNING: this deletes all chain data. You will re-bootstrap from scratch.
docker compose down -v
docker compose up -d
```
