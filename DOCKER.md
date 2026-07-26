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

If you're coming from a bare-metal/systemd install, the mental model maps directly:

| Bare-metal / VM | Docker equivalent |
|---|---|
| Process (`systemd service`) | Container |
| VM disk image | Docker image |
| `systemctl start jmdn` | `docker compose up -d` |
| `systemctl restart jmdn` | `docker compose restart jmdn` |
| `journalctl -u jmdn -f` | `docker compose logs -f jmdn` |
| `/etc/jmdn/jmdn.yaml` | Volume mount or env var |
| `/opt/jmdn` (JMDN_DATA) | `jmdn-state` named volume |
| `/opt/jmdn/data` (immudb data) | `immudb-data` named volume |

Compose (`docker-compose.yml`) declares which containers run, how they connect, and what volumes/ports they use — replacing long `docker run` commands. What Compose *can't* do is logic inside a container (conditional first-run steps, privilege drops, TLS generation); that lives in the entrypoint script, the same as postgres/redis/mysql images.

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
│  │    :15000 P2P gossip — LibP2P (TCP+UDP) ◄── peers dial in here        │    │
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

The compose stack runs immudb as its own container and jmdn connects over the Docker network via `JMDN_DATABASE_ADDRESS=immudb`. This is the default and recommended mode.

| | Separate container (compose default) | Embedded (`IMMUDB_EXTERNAL=false`) |
|---|---|---|
| Restart independence | ✓ immudb crash doesn't kill node | ✗ one failure kills both |
| Upgrade | ✓ bump immudb image tag independently | ✗ must rebuild jmdn image |
| Backup | ✓ snapshot `immudb-data` without stopping node | ✗ must stop everything |
| Resource limits | ✓ separate CPU/mem cgroups | ✗ compete for same limit |
| Network latency | ~0.1ms Docker bridge | 0 (loopback) |

For a public exchange listing, separate is the right call — 0.1ms is irrelevant at immudb's ~15s commit frequency; the operational independence is not.

**Credentials:** Set `IMMUDB_PASSWORD` in `.env` — compose injects it into both containers. The immudb service runs with `--force-admin-password`, required because bootstrap snapshots ship pre-initialized with a baked-in password; without it immudb ignores `IMMUDB_ADMIN_PASSWORD` on an existing database and jmdn cannot connect.

### Why Redis is separate, and what it does

When jmdn writes account state it enqueues to a Redis Stream (`XADD`) and returns immediately; a background worker drains the stream into ImmuDB. This exists because ImmuDB's commit latency is ~15s — without the queue every account write would block that long. Running Redis as its own container gives independent restarts, standard tooling, and version upgrades without touching the node image.

---

## 3. Services Deep Dive

### `jmdn` (the node)

| Property | Value |
|---|---|
| Image | `ghcr.io/jupitermetalabs/jmdn:latest` |
| Base OS | `debian:bookworm-slim` |
| Runs as | `jmdn` (non-root) after startup |
| Entrypoint | `/usr/local/bin/docker-entrypoint.sh` (root → gosu drop) |
| Config file | `/etc/jmdn/jmdn.yaml` (operator-mounted — no default baked in) |
| Peer identity | `/opt/jmdn/config/peer.json` (auto-generated on first run) |
| TLS certs | `/opt/jmdn/certs/` (auto-generated if missing) |
| Working dir | `/opt/jmdn` |

### `immudb` (ledger)

| Property | Value |
|---|---|
| Image | `codenotary/immudb:1.10.0` |
| Data dir | `/opt/jmdn/data` (volume: `immudb-data`) |
| Port | `3322` (internal only — not exposed to host) |
| Health check | None — see [§11](#11-health-checks) |

### `redis` (account sync queue)

| Property | Value |
|---|---|
| Image | `redis:7-alpine` |
| Persistence | AOF (`appendonly yes`, `appendfsync everysec`) — **correctness requirement, not tuning** |
| Auth | Password via `REDIS_PASSWORD` |
| Usage | Redis Streams (requires Redis 5+) |

> **⚠ Redis persistence is load-bearing for account-state correctness.**
> Reconciliation's balance effects and tx_processed markers travel through this
> queue, and the recon anchor (`sync:accounts_last_applied_block`) advances once
> data is verified and ENQUEUED — before the drain commits it to ImmuDB. If Redis
> loses the queue (crash without AOF, eviction, `FLUSHALL`), the anchor claims
> ranges whose effects never landed and reconciliation permanently skips them.
> `appendonly yes` must be set on EVERY deployment, including bare-metal nodes
> that don't use this compose file. Never point the node at a cache-mode Redis.

### `jmdn-bootstrap` (one-time, profile-gated)

Runs `bootstrap_sync.sh` in a temporary container that mounts `immudb-data` directly: downloads the chain snapshot from GCS, verifies checksums, extracts it, and writes a `.bootstrapped` sentinel so it never runs again. Must run before the stack starts for the first time.

### The two scripts

- **`docker-entrypoint.sh`** — every start. Ensures `DB/`, `config/`, `certs/` exist with correct ownership, generates TLS certs if missing, waits for immudb, drops to the `jmdn` user via `gosu`. The `ExecStartPre=` + `ExecStart=` of a systemd unit, combined.
- **`bootstrap_sync.sh`** — once only, guarded by the sentinel on the `immudb-data` volume.

---

## 4. Quick Start — New VM Setup

Complete runbook for a fresh VM with Docker already installed.

### Prerequisites

- Docker 24+ and Docker Compose v2 — verify with `docker compose version`
- **amd64 host.** The `jmdn` image is multi-arch, but the pinned `codenotary/immudb:1.10.0` is **amd64-only** (immudb published arm64 from `1.11.1`). On arm64, use bare-metal (`GETTING_STARTED.md`), whose `setup_dependencies.sh` installs a native arm64 immudb binary.
- 50 GB+ free disk — **on the disk Docker actually uses.** Named volumes and container logs live under `/var/lib/docker`, not `/opt/jmdn`. Run `df -h /var/lib/docker` and see [§12](#12-volumes-and-data-management) → *Repointing Docker's storage* **before** your first `docker compose up`.
- **Host sizing: 8 GB RAM / 4 vCPU minimum.** `docker-compose.yml` caps each container's memory and CPU. The caps exist for *blast radius* — a leak OOM-kills one container instead of the host — **not** capacity planning, so scale them with the machine via `.env` (Step 2):

  | Host | `JMDN_MEM_LIMIT` | `JMDN_CPU_LIMIT` | `IMMUDB_MEM_LIMIT` | `IMMUDB_CPU_LIMIT` | `REDIS_MEM_LIMIT` | `REDIS_CPU_LIMIT` | `REDIS_MAXMEMORY` |
  |---|---|---|---|---|---|---|---|
  | 8 GB / 4c *(defaults)* | `4g` | `2.0` | `2g` | `1.0` | `512m` | `0.5` | `384mb` |
  | 16 GB / 8c | `8g` | `6.0` | `4g` | `2.0` | `1g` | `0.5` | `768mb` |
  | 32 GB / 16c | `16g` | `0` (unlimited) | `8g` | `4.0` | `2g` | `1.0` | `1536mb` |
  | 64 GB / 32c | `32g` | `0` (unlimited) | `16g` | `8.0` | `4g` | `1.0` | `3gb` |

  Rules of thumb: jmdn ~50% of host RAM, immudb ~25%, `REDIS_MAXMEMORY` ~75% of `REDIS_MEM_LIMIT`, and leave ~20% of the host unallocated for the OS page cache (immudb read performance depends on it). Memory caps are always warranted (exceeding = OOM kill); CPU is compressible, so on a dedicated host set `JMDN_CPU_LIMIT=0` and keep CPU caps for shared hosts. `0` = unlimited.

- Open ports on firewall/security group:

| Port | Protocol | Purpose |
|---|---|---|
| 15000 | TCP **+ UDP** | P2P gossip (LibP2P, TCP + QUIC) — **must be public**; without inbound 15000 the node can dial out but can't be dialed, degrading to outbound-only participation |
| 8545 | TCP | JSON-RPC — exchange endpoint |
| 8546 | TCP | WebSocket RPC |

Port **8090** (Explorer API) is deliberately *not* published — the mapping is commented out in `docker-compose.yml`. The health check reaches it inside the container, so it needs no firewall rule. Uncomment `- "8090:8090"` only if you need it from outside the host, and set `JMDN_SECURITY_EXPLORER_API_KEY` first.

> **Ports 15050, 15052, 15055 are deliberately not exposed.** `15052` (DID service) runs `RegisterDID` with **no authentication** — publishing it lets anyone who reaches it register arbitrary DIDs, not just resolve them. Port 15001 (Yggdrasil) is also unpublished: the daemon isn't wired up in this image, so the feature can't work regardless. See **[PORTS.md](./PORTS.md)** before opening any of these.

### Step 1 — Clone the repo

```bash
mkdir -p /opt/jmdn
git clone https://github.com/JupiterMetaLabs/jmdn.git /opt/jmdn/jmdn
cd /opt/jmdn/jmdn
git checkout main
```

Clone anywhere you like — volume and network names come from `COMPOSE_PROJECT_NAME` in `.env` (Step 2), not the directory.

> Running as root is fine — Docker runs as root. The `jmdn` user exists only *inside* the container.

### Step 2 — Create a .env file (container passwords only)

`.env` is minimal: it exists because the immudb and redis containers are separate processes that can't read `jmdn.yaml`. Everything else lives in `jmdn.yaml` (Step 3).

Template: `cp .env.docker.example .env && chmod 600 .env` and edit — or build it by hand (run from your install directory, `/opt/jmdn/jmdn` from Step 1):

```bash
openssl rand -base64 32   # → IMMUDB_PASSWORD
openssl rand -base64 32   # → REDIS_PASSWORD
openssl rand -base64 32   # → JMDN_SECURITY_EXPLORER_API_KEY

cat > .env << 'EOF'
# Prefixes volume/network names (jmdn_immudb-data ...). Keeps names stable
# regardless of checkout directory, so this guide's commands work verbatim.
# NEW INSTALLS ONLY — migrating an existing node? See §13 "One-time migration
# (v1.2.0 installs only)" first, or you'll repoint compose at empty volumes.
COMPOSE_PROJECT_NAME=jmdn

# JMDN release to run. Upgrade by changing this line — never by editing
# docker-compose.yml.
JMDN_VERSION=v2.0.0

# Must match database.password in jmdn.yaml.
IMMUDB_PASSWORD=<generated>

# Must match database.redis.password in jmdn.yaml.
REDIS_PASSWORD=<generated>

# Must match security.explorer_api_key in jmdn.yaml (health check uses it).
JMDN_SECURITY_EXPLORER_API_KEY=<generated>
EOF

# Restrict it — .env holds passwords and an API key
chmod 600 .env
```

> `.env` is gitignored — it will never be committed.

> **Know where these secrets end up.** Compose injects `.env` values into
> container environments, and anything in a container's environment is readable
> by anyone who can talk to the Docker socket (`docker inspect jmdn | grep -A5
> Env`). So: (1) Docker socket access == secret access — don't add users to the
> `docker` group casually; (2) never paste `docker inspect` output into
> tickets/chat unscrubbed; (3) if your org needs stronger handling, front these
> with a secrets manager that writes `.env` at deploy time.

### Step 3 — Create your node config

```bash
cp jmdn_exchange.yaml jmdn.yaml   # exchange operators
# or: cp jmdn_default.yaml jmdn.yaml   # everyone else
nano jmdn.yaml
```

Fill in every REQUIRED field:

| Field | What to set |
|---|---|
| `node.alias` | Unique name for this node (e.g. `exchange-prod-1`) |
| `logging.service_name` | Same as `node.alias` |
| `network.seednode` | Provided by JupiterMeta offline |
| `network.mempool` | Provided by JupiterMeta offline |
| `database.password` | Leave blank for Compose — injected from `.env`. Set only for bare-metal. |
| `database.redis.password` | Leave blank for Compose — injected from `.env`. Set only for bare-metal. |
| `security.jwt_secret` | Generate: `openssl rand -base64 32` |
| `security.explorer_api_key` | Generate: `openssl rand -base64 32` |
| `fastsync.catch_up_from_block` | Bootstrap snapshot tip + 1 — **`13450`** for the current snapshot (tip 13449). `0` works but forces a slow full genesis scan every sync cycle. |

Compose mounts `./jmdn.yaml` at `/etc/jmdn/jmdn.yaml:ro`; the node reads it automatically. **This file must exist before `docker compose up`** — the mount is always active.

### Step 4 — Pre-pull the images (recommended)

Compose resolves the JMDN image as `ghcr.io/jupitermetalabs/jmdn:${JMDN_VERSION}` from your `.env` (Step 2), and pulls it — along with `redis` and `immudb` — automatically on first use. So this step is optional; run it to surface registry or network problems now rather than partway through the 10–30 minute bootstrap:

```bash
docker compose pull
```

Always pull through Compose rather than `docker pull <tag>` by hand — Compose uses the tag from `.env`, so the two can't drift apart. For the same reason, never edit the `image:` line in `docker-compose.yml`: leaving that file untouched is what lets `git pull` update it cleanly. To build from source instead, see [§8](#8-building-the-image-from-source).

### Step 5 — Bootstrap ImmuDB (first time only)

The chain snapshot must be loaded into `immudb-data` **before** the stack starts. One-time; subsequent restarts skip it. Takes 10–30 minutes depending on bandwidth.

```bash
docker compose run --rm jmdn-bootstrap
```

```
[bootstrap] First run detected — starting bootstrap sync.
[bootstrap] Listing parts from GCS: gs://jmdn-bootstrap/bootstrap-26072026/...
[bootstrap] Downloading parts to /opt/jmdn/bootstrap_tmp...
[bootstrap] Checksums OK.
[bootstrap] Extracting parts into sandbox: /opt/jmdn/data_tmp/sandbox
[bootstrap] Moving data to /opt/jmdn/data...
[bootstrap] Bootstrap complete. Sentinel written → /opt/jmdn/data/.bootstrapped
```

### Step 6 — Start the stack

```bash
docker compose up -d
docker compose logs -f jmdn
```

```
[entrypoint] External ImmuDB mode — skipping bootstrap.
[entrypoint] Sentinel found — immudb-data volume is populated.
[entrypoint] TLS certs generated.
[entrypoint] Waiting for ImmuDB on immudb:3322...
[entrypoint] ImmuDB ready (2s)
[entrypoint] Starting JMDN as jmdn...
```

### Step 7 — Verify

```bash
# All three containers should show (healthy)
docker compose ps

# JSON-RPC (exchange endpoint) — no auth required
curl -s http://localhost:8545 -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Explorer API — port 8090 isn't published by default, so query it from
# inside the container (add -p 8090:8090 in compose to reach it from the host)
docker compose exec jmdn curl -s http://localhost:8090/api/v1/node/version \
  -H "Authorization: Bearer $JMDN_SECURITY_EXPLORER_API_KEY"
```

---

## 5. Running with `docker run` (Standalone)

For quick tests, CI, or a minimal single-node setup with **embedded** ImmuDB — no Compose. Production exchange deployments should use Compose ([§4](#4-quick-start--new-vm-setup)).

In this mode the entrypoint runs bootstrap itself on first start (10–30 min), then starts an embedded immudb before the node.

```bash
docker run -d \
  --name jmdn \
  -v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro \
  -v jmdn-data:/opt/jmdn \
  -p 15000:15000 -p 15000:15000/udp \
  -p 8545:8545 \
  -p 8546:8546 \
  ghcr.io/jupitermetalabs/jmdn:latest

# Add the Explorer API (disabled by default):
#   -p 8090:8090 -e JMDN_PORTS_API=8090
```

| Flag | Purpose |
|---|---|
| `-v .../jmdn.yaml:/etc/jmdn/jmdn.yaml:ro` | **Required** — node exits with an error without it |
| `-v jmdn-data:/opt/jmdn` | Persists peer identity, certs, DB, and immudb data |
| `-p 15000:15000` + `/udp` | P2P gossip — **required**; without inbound 15000 peers can't dial you |
| `-p 8545` / `-p 8546` | JSON-RPC / WebSocket |

Same port caveats as [§4](#4-quick-start--new-vm-setup) — don't publish 15050/15052/15055.

```bash
docker logs -f jmdn                    # follow startup
docker stop jmdn && docker rm jmdn     # volume jmdn-data is preserved
docker volume rm jmdn-data             # full reset (deletes all chain data)

# Force re-bootstrap
docker exec jmdn rm /opt/jmdn/data/.bootstrapped && docker restart jmdn
```

Environment overrides: pass `-e KEY=value`; see the reference table in [§7](#7-configuration).

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
              ├─ Lists + downloads parts from GCS over public HTTP
              ├─ Downloads checksums.md5, verifies
              ├─ Backs up existing /opt/jmdn/data → /opt/jmdn/backup/
              ├─ Extracts into a sandbox, finds systemdb/ → that's the data root
              ├─ Moves data root → /opt/jmdn/data, chown 3322:3322
              └─ touch /opt/jmdn/data/.bootstrapped

# Step B: stack starts
docker compose up -d
        │
        ├─► immudb starts (reads /opt/jmdn/data)
        ├─► redis starts (~1s)
        └─► jmdn starts
                ├─ [root] docker-entrypoint.sh
                ├─ IMMUDB_EXTERNAL=true → skip bootstrap
                ├─ check .bootstrapped (immudb-data :ro mount)
                │   MISSING? → exit 1 "Run jmdn-bootstrap first"
                ├─ [root] mkdir -p /opt/jmdn/{config,DB,certs}; chown jmdn
                ├─ [root] generate self-signed TLS certs if ca.crt missing
                ├─ nc -z immudb 3322  (waits up to IMMUDB_READY_TIMEOUT=120s)
                └─ gosu jmdn → jmdn
```

**Sentinel:** `/opt/jmdn/data/.bootstrapped` lives on the `immudb-data` volume. While it exists, bootstrap is skipped.

```bash
# Force re-bootstrap (compose)
docker run --rm -v jmdn_immudb-data:/data alpine rm -f /data/.bootstrapped
docker compose run --rm jmdn-bootstrap
docker compose restart immudb jmdn
```

---

## 7. Configuration

Most configuration belongs in `jmdn.yaml`. The environment variables below are the exceptions: Docker-mode routing values that differ from bare-metal, and the container passwords immudb/redis need directly. Set them in `.env`.

| Variable | Value | Purpose |
|---|---|---|
| `IMMUDB_EXTERNAL` | `true` | Connect to an external immudb container instead of spawning one |
| `JMDN_DATABASE_ADDRESS` | `immudb` | Docker DNS name of the immudb service |
| `JMDN_DATABASE_PORT` | `3322` | ImmuDB gRPC port |
| `IMMUDB_PASSWORD` | *(from .env)* | immudb container password — must match `database.password` |
| `REDIS_PASSWORD` | *(from .env)* | Redis `--requirepass` — must match `database.redis.password` |
| `JMDN_PORTS_API` | `0` (disabled) | Set `8090` to enable the Explorer API |
| `JMDN_SECURITY_JWT_SECRET` | `""` | JWT signing secret |
| `COMPOSE_PROJECT_NAME` | *(directory name)* | Prefixes volume/network names — set to `jmdn` in `.env` so this guide's volume commands work verbatim. Changing it on a live node repoints compose at different volumes |
| `JMDN_VERSION` | `latest` | Image tag for `jmdn` and `jmdn-bootstrap`. Pin a release in `.env`; upgrades change this line only ([§13](#13-upgrading)) |
| `JMDN_MEM_LIMIT` / `JMDN_CPU_LIMIT` | `4g` / `2.0` | jmdn container caps — scale to host, `0` = unlimited ([§4](#4-quick-start--new-vm-setup)) |
| `IMMUDB_MEM_LIMIT` / `IMMUDB_CPU_LIMIT` | `2g` / `1.0` | immudb container caps |
| `REDIS_MEM_LIMIT` / `REDIS_CPU_LIMIT` | `512m` / `0.5` | redis container caps |
| `REDIS_MAXMEMORY` | `384mb` | Redis self-enforced ceiling — ~75% of `REDIS_MEM_LIMIT` |
| `BOOTSTRAP_MEM_LIMIT` / `BOOTSTRAP_CPU_LIMIT` | `2g` / `1.0` | Bootstrap container caps; raise if a very large snapshot fails on decompress |

<a id="environment-variable-overrides"></a>

**Bootstrap snapshot source** (read by the bootstrap container only):

| Variable | Default | Purpose |
|---|---|---|
| `GCS_BUCKET` | `jmdn-bootstrap` | Snapshot bucket |
| `GCS_PREFIX` | `bootstrap-26072026` | Snapshot path prefix — must be world-readable (fetched over public HTTP) |
| `PARTS_PREFIX` | `data-patched.part` | Part filename prefix |
| `CHECKSUM_FILE` | `checksums.md5` | Checksum manifest filename |

All `JMDN_*` vars map to `jmdn.yaml` keys with underscores as separators — e.g. `JMDN_NETWORK_CHAIN_ID=7000700` sets `network.chain_id`.

### File-based logging (optional)

`logging.file.enabled` is `false` by default — console output goes to stdout, which Docker's `json-file` driver captures and rotates ([§10](#10-log-retention)). That's the safe default.

If you enable it, **`logging.file.path` must resolve under the `jmdn-state` mount (`/opt/jmdn/...`)**. Anything else writes to the container's writable layer, which is deleted whenever the container is recreated (image pull, `down` + `up`) — the file looks fine until the next deploy silently wipes it.

```yaml
logging:
  file:
    enabled: true
    path: "/opt/jmdn/logs/app.log"   # under jmdn-state — survives recreation
```

Rotation settings beside it (`max_size_mb`, `max_age_days`, `max_backups`, `compress`) are sane defaults.

### Production TLS certificates

Self-signed certs are generated on first run into `jmdn-state` at `/opt/jmdn/certs/`. For production, mount real ones (`ca.crt`, `ca.key`, `cli_admin.*`, `block_ingest_grpc.*`, … — see `Scripts/setup_certs.sh`) by uncommenting in `docker-compose.yml`:

```yaml
volumes:
  - ./certs:/opt/jmdn/certs:ro
```

---

## 8. Building the Image from Source

Use the pre-built image unless you need an unreleased branch/commit, want to verify the binary from source, or are developing changes.

CI publishes on every `v*` tag: `ghcr.io/jupitermetalabs/jmdn:v2.0.0` (pinnable) and `:latest`, for linux/amd64, arm64, and arm/v7.

```bash
# Current branch (version metadata embedded — without these flags
# `jmdn --version` reports "unknown")
docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t ghcr.io/jupitermetalabs/jmdn:latest \
  .

# A specific release tag
git checkout v2.0.0
docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=v2.0.0 --build-arg GIT_TAG=v2.0.0 \
  -t ghcr.io/jupitermetalabs/jmdn:v2.0.0 .

# Multi-platform (push to registry)
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  -t ghcr.io/jupitermetalabs/jmdn:latest --push .

# Use a local build in compose: comment out `image:`, uncomment `build: .`
docker compose up -d --build
```

Two stages: builder (`golang:1.25.3-bookworm`, `CGO_ENABLED=1`) then runtime (`debian:bookworm-slim`). The final image contains no Go toolchain or source.

---

## 9. Debugging — Live Logs, Exec, Inspect

```bash
# Logs — all services, one service, tail, since, to file
docker compose logs -f
docker compose logs -f jmdn
docker compose logs --tail=100 jmdn
docker compose logs --since="1h" jmdn
docker compose logs jmdn > jmdn.log

# Errors only (equivalent of journalctl -p err)
docker compose logs jmdn 2>&1 | grep -i "error\|fatal\|panic"
```

```bash
# Shell in, inspect node state
docker compose exec jmdn bash
docker compose exec jmdn ls /opt/jmdn/config/ /opt/jmdn/certs/ /opt/jmdn/DB/
docker compose exec jmdn ps aux

# immudb is distroless (no shell) — inspect its volume from a temp container
docker run --rm -v jmdn_immudb-data:/data alpine ls /data/

# Redis (use REDIS_PASSWORD from your .env)
docker compose exec redis redis-cli -a "$REDIS_PASSWORD" ping
docker compose exec redis redis-cli -a "$REDIS_PASSWORD" xlen accountsync:accounts
```

```bash
# Status, resources, config, ports
docker compose ps
docker stats
docker inspect jmdn
docker compose port jmdn 8545
docker compose exec jmdn env | grep JMDN_   # careful — contains secrets

# Connectivity
curl -s http://localhost:8545 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}'
docker compose exec jmdn nc -zv immudb 3322

# Restart / stop (volumes preserved)
docker compose restart jmdn
docker compose down
```

> `docker compose down -v` also deletes volumes — all chain data. See [§13](#13-upgrading) → *One-time chain reset* before using it.

---

## 10. Log Retention

Docker has no journald. Container logs go to a JSON file on the host (`/var/lib/docker/containers/<id>/<id>-json.log`) with **no size limit by default** — a busy node fills the disk.

`docker-compose.yml` already configures rotation:

```yaml
# jmdn: max-size 50m × max-file 5  = 250 MB ceiling
# redis: max-size 10m × max-file 3 =  30 MB ceiling
logging:
  driver: json-file
  options:
    max-size: "50m"
    max-file: "5"
```

Adjust to your disk. For centralised logging, swap the driver:

```yaml
# Loki — needs: docker plugin install grafana/loki-docker-driver:latest \
#                 --alias loki --grant-all-permissions
logging:
  driver: loki
  options:
    loki-url: "http://loki:3100/loki/api/v1/push"
    labels: "service=jmdn,env=production"

# journald — after this, `journalctl -t jmdn -f` works like a systemd unit
logging:
  driver: journald
  options:
    tag: "jmdn"
```

Fluentd, Splunk, and CloudWatch are supported natively too (`docker help logging`).

**Grafana:** a pre-built dashboard is in `grafana/`, reading the node's Prometheus metrics. Enable with `JMDN_PORTS_METRICS: "8081"` (and `JMDN_BINDS_METRICS`), then scrape `localhost:8081/metrics`.

---

## 11. Health Checks

```bash
docker compose ps                                                    # status
docker inspect --format='{{json .State.Health}}' jmdn | python3 -m json.tool
```

| Service | Health check | Interval | Start period |
|---|---|---|---|
| `jmdn` | Two-tier: `GET /api/v1/node/version` → HTTP 200, falling back to JSON-RPC `eth_blockNumber` on :8545 | 30s | 300s |
| `redis` | `redis-cli ping` | 10s | — |
| `immudb` | none — see below | — | — |

The fallback matters because `:8545` is what exchanges actually consume, and operators running `jmdn_default.yaml` (Explorer API disabled) would otherwise show permanently `unhealthy` while serving RPC fine.

**Why immudb has no healthcheck:** the image is built on `scratch` — no shell, no `nc`/`curl`, so any `healthcheck.test` that execs inside it cannot run. Its own baked-in `immuadmin status` fails cold (it needs a login handshake first). A gRPC sidecar probe would work but isn't worth an always-on container: the jmdn entrypoint's `nc -z immudb 3322` loop (up to `IMMUDB_READY_TIMEOUT`, default 120s) already gates startup ordering correctly.

```bash
# Why is a health check failing?
docker inspect jmdn | python3 -c "
import json,sys
h=json.load(sys.stdin)[0]['State']['Health']
for c in h['Log'][-3:]: print(c['ExitCode'], c['Output'])
"
# jmdn unhealthy   → node still starting, or neither endpoint reachable (§14)
# redis unhealthy  → wrong REDIS_PASSWORD, or OOM
# immudb           → docker compose logs immudb
```

### Crash-loop detection

Unlike systemd (which gives up after `StartLimitBurst` and lands in a visible `failed` state), `restart: unless-stopped` retries **forever** — `docker compose ps` shows `Restarting` but never a terminal failure. A crash loop can run for days unnoticed. Watch `RestartCount`:

```bash
docker inspect --format='{{.RestartCount}}' jmdn

# Alert-friendly (run from monitoring, not by hand)
COUNT=$(docker inspect --format='{{.RestartCount}}' jmdn)
[ "$COUNT" -gt 5 ] && echo "jmdn restarted ${COUNT}× — likely crash-looping" >&2 && exit 1
```

To make Docker give up like systemd does, swap the restart policy for `jmdn` in `docker-compose.yml`:

```yaml
# restart: unless-stopped   # retries forever — self-heals, can mask a crash loop
restart: on-failure:5       # stops after 5 — visible, but won't self-heal past that
```

Pick based on whether anything is watching `RestartCount`. If nothing is, `on-failure:5` is safer — a stopped container is much harder to miss than a quietly restarting one.

---

## 12. Volumes and Data Management

> Volume names assume `COMPOSE_PROJECT_NAME=jmdn`. On an older install without it, the prefix is your checkout directory's name — check `docker volume ls`.

```bash
docker volume ls | grep jmdn
docker volume inspect jmdn_immudb-data --format '{{.Mountpoint}}'
# → /var/lib/docker/volumes/jmdn_immudb-data/_data
```

### Repointing Docker's storage to a different disk

On bare metal, `JMDN_DATA=/opt/jmdn` is just a path — mount any disk there. In Docker that isolation disappears: every named volume *and* every container log file lives under Docker's `data-root`, default `/var/lib/docker`, which on most cloud images is the OS root disk. The snapshot alone is tens of GB.

Check **before** your first `docker compose up`:

```bash
df -h /var/lib/docker
```

If that's the wrong disk, repoint `data-root` before any containers/volumes exist (moving it later means copying `/var/lib/docker` by hand):

```bash
sudo systemctl stop docker
sudo mkdir -p /mnt/data-disk/docker
sudo rsync -aP /var/lib/docker/ /mnt/data-disk/docker/   # skip on a fresh install
sudo mkdir -p /etc/docker
printf '{\n  "data-root": "/mnt/data-disk/docker"\n}\n' | sudo tee /etc/docker/daemon.json
sudo systemctl start docker
docker info | grep "Docker Root Dir"
```

Alternatively, bind-mount host paths for the two data-heavy services instead of named volumes:

```yaml
volumes:
  - /mnt/data-disk/jmdn/immudb-data:/opt/jmdn/data   # instead of immudb-data:
  - /mnt/data-disk/jmdn/jmdn-state:/opt/jmdn         # instead of jmdn-state:
```

### Volume layout

```
immudb-data  (/opt/jmdn/data in the immudb container):
├── .bootstrapped   ← sentinel (delete to force re-bootstrap)
├── systemdb/  defaultdb/  accountsdb/
└── immudb.identifier

jmdn-state  (/opt/jmdn in the jmdn container):
├── config/peer.json    ← node peer identity (auto-generated first run)
├── certs/              ← TLS (auto-generated or operator-mounted)
└── DB/
    ├── gossipnode.db   ← SQLite node manager state
    └── txindex.db      ← SQLite address→transaction index (+ -wal, -shm)
```

`txindex.db` is fully rebuildable from ImmuDB — a derived index, not a source of truth. Losing it isn't catastrophic: the node re-catches-up automatically in the background (address-history endpoints return "still syncing" until done — see [§14](#14-troubleshooting)). It's included in the backup below only because that's simpler than special-casing it.

### Backup and restore

```bash
# Backup — stop cleanly first
docker compose stop jmdn immudb
for v in immudb-data jmdn-state; do
  docker run --rm -v jmdn_$v:/data -v $(pwd)/backups:/backups \
    alpine tar czf /backups/$v-$(date +%Y%m%d).tar.gz -C /data .
done
docker compose start immudb jmdn
```

```bash
# Restore
docker compose down
for v in immudb-data jmdn-state; do
  docker run --rm -v jmdn_$v:/data -v $(pwd)/backups:/backups \
    alpine tar xzf /backups/$v-20240115.tar.gz -C /data
done
docker compose up -d
```

---

## 13. Upgrading

Two channels update independently:

| Channel | Command | What it delivers |
|---|---|---|
| **Image** | `docker compose pull` | New `jmdn` binary + in-container scripts — the node software |
| **Repo** | `git pull` | `docker-compose.yml` and this documentation |

A version bump only *requires* the image channel. Pulling the repo too is recommended and always safe — nothing you configure lives in tracked files (`.env` and `jmdn.yaml` are both gitignored).

Most upgrades are the [normal upgrade](#normal-upgrade). Two exceptions:

- **Coming from any v1.x release?** v2.0.0 is a breaking protocol change — use [Upgrading to v2.0.0](#upgrading-to-v200-from-v1x).
- **First upgrade from the original v1.2.0 install** (hand-edited `image:` tag, no `JMDN_VERSION` in `.env`)? Do the [one-time migration](#one-time-migration-v120-installs-only) once first.

<a id="normal-upgrade"></a>
### Normal upgrade

Never touches chain data or config.

```bash
# 1. Set the new version in .env — portable (no sed -i), preserves the file's
#    permissions and every other line, comments included
{ grep -v '^JMDN_VERSION=' .env || true; echo 'JMDN_VERSION=v2.0.0'; } > .env.tmp && cat .env.tmp > .env && rm .env.tmp

# 2. (Recommended) refresh compose + docs
git pull

# 3. Pull + restart, with automatic rollback on failure
./Scripts/docker-deploy.sh
```

`docker-deploy.sh` snapshots the running image, pulls the new one, restarts, and **rolls back to the previous image** if the new one fails to start or fails its health check. It also refuses to run twice concurrently. Note it restores the *image* only — it doesn't touch volumes, so snapshot those ([§12](#12-volumes-and-data-management)) for anything past a routine point release.

By hand instead (no automatic rollback):

```bash
docker compose pull jmdn && docker compose up -d jmdn
curl -s http://localhost:8545 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

### Upgrading to v2.0.0 (from v1.x)

v2.0.0 changes the peer-to-peer protocol version, so **a v2.0.0 node does not talk to a v1.x node**. The network upgrades together in a maintenance window — this is not a rolling upgrade. JupiterMeta will confirm the window with you.

It also ships a **refreshed chain snapshot**, so this upgrade is a rebuild, not a routine version bump. Follow the [one-time chain reset](#one-time-chain-reset-only-when-explicitly-instructed) below, using these values:

| Setting | Value |
|---|---|
| `JMDN_VERSION` in `.env` | `v2.0.0` |
| `catch_up_from_block` in `jmdn.yaml` (under `fastsync:`) | `13450` — snapshot tip 13449 + 1 |

**Rollback** is not a one-command revert — this path rebuilds chain data. Restore your backups and re-bootstrap from the previous snapshot, and coordinate with JupiterMeta: a single node rolled back to v1.x cannot talk to an upgraded network.

### One-time migration (v1.2.0 installs only)

The v1.2.0 guide had you pin releases by editing the `image:` line in `docker-compose.yml`. That edit dirties your checkout, so `git pull` will refuse or conflict. Migrate once:

```bash
git stash          # parks the hand-edited `image:` tag
git pull           # brings the compose file that reads JMDN_VERSION from .env
git stash drop     # the old pinned edit is obsolete
echo "JMDN_VERSION=v2.0.0" >> .env
docker compose pull jmdn && docker compose up -d jmdn
```

> **Do NOT add `COMPOSE_PROJECT_NAME=jmdn` to an existing node's `.env`.** Your
> volumes are named after the project name the stack was created with (usually
> your checkout directory). Changing it repoints compose at fresh empty volumes
> and the node refuses to start (missing sentinel). Leave it unset.

Afterwards you're on the same footing as any new install.

### One-time chain reset (only when explicitly instructed)

> **Not a routine operation.** Outside a release that explicitly ships a new
> snapshot, a refreshed bootstrap only makes *new* installs faster — it does not
> invalidate a running node's data. Ordinary upgrades never touch chain data.
> Run this when JupiterMeta asks you to rebuild from a specific snapshot —
> including the [v2.0.0 upgrade](#upgrading-to-v200-from-v1x).

This **discards local chain data** and re-seeds from the provided snapshot. A node is a replica, so nothing unique is lost — but it isn't reversible, so back up first.

**Run from your install directory** (where `docker-compose.yml` lives) — that's what makes `down -v` target *your* volumes.

```bash
# 1. Back up config + secrets (custom certs too, if you mount your own)
cp .env ~/jmdn.env.backup && cp jmdn.yaml ~/jmdn.yaml.backup

# 2. Pin the version JupiterMeta provides, and refresh the repo
{ grep -v '^JMDN_VERSION=' .env || true; echo 'JMDN_VERSION=v2.0.0'; } > .env.tmp && cat .env.tmp > .env && rm .env.tmp
git pull

# 3. Set catch_up_from_block in jmdn.yaml = snapshot tip + 1 (e.g. 13450)

# 4. Pull, wipe, re-bootstrap, start
docker compose pull
docker compose down -v                     # removes immudb-data, redis-data, jmdn-state
docker compose run --rm jmdn-bootstrap     # downloads + verifies + extracts
docker compose up -d

# 5. Verify it catches up from the new baseline
docker compose logs -f jmdn
curl -s http://localhost:8545 -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Why all three volumes go: **`immudb-data`** is the chain itself (replaced by the snapshot); **`redis-data`** is the account-sync queue, whose stale entries reference the old chain and must not replay onto fresh state; **`jmdn-state`** holds node identity, certs, and the derived tx-index — wiping gives a new identity (it re-peers automatically) and a clean index rebuild.

**To keep node identity and custom certs**, replace step 4's `down -v` with a selective wipe that leaves `jmdn-state` intact:

```bash
docker compose down
docker volume ls --filter name=immudb-data --filter name=redis-data   # confirm exact names
docker volume rm jmdn_immudb-data jmdn_redis-data                     # use the names listed above
# then continue: docker compose run --rm jmdn-bootstrap && docker compose up -d
```

> Two things must be right: `catch_up_from_block` = **snapshot tip + 1** (wrong value = full genesis re-scan or silently skipped blocks), and you must run from the install directory. The snapshot prefix must be reachable over public HTTP — see `GCS_*` in [§7](#environment-variable-overrides).

### Building from source (advanced)

```bash
git fetch --tags && git checkout v2.0.0

# The built tag must match what compose requests (JMDN_VERSION, else "latest").
# Get this wrong and `docker compose up` silently keeps the old image.
TAG=$(grep '^JMDN_VERSION=' .env | cut -d= -f2); TAG=${TAG:-latest}

docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t ghcr.io/jupitermetalabs/jmdn:${TAG} .
docker compose up -d jmdn
```

> Don't follow a local build with `./Scripts/docker-deploy.sh` — it runs `docker compose pull`, overwriting your build with whatever's on the registry under that tag.

### Upgrading Redis or ImmuDB

Their tags are pinned in `docker-compose.yml` with no `.env` variable. Editing that file dirties your checkout, so override it instead (Compose merges automatically; the file is untracked):

```yaml
# docker-compose.override.yml
services:
  redis:
    image: redis:7.4-alpine
```

```bash
docker compose pull redis && docker compose up -d redis   # jmdn reconnects automatically
```

---

## 14. Troubleshooting

### Node stuck on bootstrap

```bash
docker compose logs -f jmdn-bootstrap
# "Downloading parts..."          → normal, 10-30 min
# "Checksum verification failed"  → corrupt download, retry
# "No parts found"                → check GCS_BUCKET / GCS_PREFIX
```

### `unhealthy` immediately after start

The check tries the Explorer API then falls back to JSON-RPC, so `unhealthy` means **both** failed — usually the node process isn't up yet (`docker compose logs jmdn`). If the node is up but you expected the Explorer tier to pass:

```bash
# 1. Explorer API port enabled? Needs `ports.api: 8090` in jmdn.yaml
grep "api:" jmdn.yaml
docker compose exec jmdn ss -tlnp | grep 8090

# 2. Token mismatch? JMDN_SECURITY_EXPLORER_API_KEY in .env must equal
#    security.explorer_api_key in jmdn.yaml. A 401 in the health log means it doesn't:
docker inspect jmdn | python3 -c "
import json,sys
print(json.load(sys.stdin)[0]['State']['Health']['Log'][-1]['Output'])"
```

### ImmuDB not reachable

```bash
docker compose logs immudb
docker run --rm -v jmdn_immudb-data:/data alpine ls /data/
# Expect: systemdb/ defaultdb/ accountsdb/ immudb.identifier .bootstrapped
# Empty → bootstrap hasn't run: docker compose run --rm jmdn-bootstrap
```

### Redis connection refused

```bash
docker compose ps redis && docker compose logs redis
docker compose exec redis redis-cli -a "$REDIS_PASSWORD" ping
# Unhealthy is usually a password mismatch between .env and jmdn.yaml
```

### Out of disk space

```bash
du -sh /var/lib/docker/volumes/jmdn*/_data
du -sh /var/lib/docker/containers/*/ | sort -h | tail -10
docker system prune          # unused images, stopped containers, dangling volumes
```

**Prevent rather than react.** Every `docker build` leaves the previous image's layers dangling and grows the builder cache; neither is cleaned automatically. On a host that rebuilds regularly, schedule a prune:

```bash
# /etc/cron.weekly/docker-prune (chmod +x)
#!/usr/bin/env bash
# Dangling/unused images older than 7 days only — never touches running
# containers, in-use images, or named volumes.
docker image prune -af --filter "until=168h"
docker builder prune -af --filter "until=168h"
```

### peer.json missing after restart

Expected and harmless — the entrypoint recreates `/opt/jmdn/config/` on every start and the node regenerates `peer.json` automatically if absent.

### TLS cert errors

```bash
docker run --rm -v jmdn_jmdn-state:/data alpine rm -rf /data/certs
docker compose restart jmdn      # entrypoint generates fresh self-signed certs
```

### Manual catchup sync

If the node bootstrapped but is missing recent blocks (produced after the snapshot was taken):

```bash
docker exec -it jmdn jmdn -cmd catchup \
  /ip4/<peer-ip>/tcp/15000/p2p/<peer-id> \
  <from_block>            # e.g. bootstrapTip + 1

docker logs --tail 100 -f jmdn    # look for "[CatchUpSync] done in …"
```

The SyncMonitor keeps the node in sync automatically after that.

### Transaction-address index stuck or erroring

`eth_getTransactionsByAddress` and `GET /explorer/address/:address/transactions` are backed by a SQLite index (`DB/txindex.db`) the node rebuilds from ImmuDB in the background — never restored from backup. While catching up, both endpoints return "still syncing" / `503` rather than wrong data.

```bash
docker exec -it jmdn jmdn -cmd txindexstatus     # READY / SYNCING + last indexed block
docker exec -it jmdn jmdn -cmd rebuildindex      # full rebuild from genesis (background, node stays up)
docker exec -it jmdn jmdn -cmd rebuildrange <from_block> <to_block>   # narrower repair
```

### Full container reset

Deletes all chain data and re-bootstraps — see [One-time chain reset](#one-time-chain-reset-only-when-explicitly-instructed) in §13 for the complete procedure, including how to preserve node identity.
