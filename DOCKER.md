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
| `/opt/jmdn` (JMDN_DATA, incl. ThebeDB KV at `storage/thebe-kv`) | `jmdn-state` named volume |
| PostgreSQL data directory (ThebeDB SQL projection) | `postgres-data` named volume |

Compose (`docker-compose.yml`) declares which containers run, how they connect, and what volumes/ports they use — replacing long `docker run` commands. What Compose *can't* do is logic inside a container (conditional first-run steps, privilege drops, TLS generation); that lives in the entrypoint script, the same as postgres/redis/mysql images.

---

## 2. JMDN Architecture Inside Docker

```
docker-compose
│
├── jmdn container  (ghcr.io/jupitermetalabs/jmdn:${JMDN_VERSION})
│     Startup (as root): docker-entrypoint.sh
│       ├─ /etc/jmdn/jmdn.yaml missing?     → exit 1
│       ├─ /opt/jmdn/.bootstrapped missing? → run bootstrap_sync.sh
│       ├─ ensure DB/, storage/, config/, certs/ exist with correct ownership
│       ├─ generate self-signed TLS certs if missing
│       └─ gosu jmdn → jmdn binary
│
│     ThebeDB KV (BadgerDB, embedded in the jmdn process)
│       /opt/jmdn/storage/thebe-kv   ← on the jmdn-state volume
│
│     Ports (listening as jmdn user):
│       :8545  JSON-RPC   ◄── MEXC / exchange connects here
│       :8546  WebSocket
│       :15000 P2P gossip — LibP2P (TCP+UDP) ◄── peers dial in here
│       :8090  Explorer API  (not published, health check)
│
│        │ THEBE_SQL_DSN → postgres:5432        │ database.redis.url → redis:6379
│        ▼                                       ▼
├── postgres container  (postgres:16-alpine)
│     ThebeDB SQL projection (wal_level=logical for ThebeDB CDC)
│     Host port 127.0.0.1:5430 → 5432 (loopback only)
│
└── redis container  (redis:7-alpine)
      Account sync worker queue (Redis Streams XADD/XREADGROUP/XACK)
      Decouples WriteAccounts from synchronous DB commit latency

Named volumes (on host):
  postgres-data → /var/lib/postgresql/data  (ThebeDB SQL projection)
  jmdn-state    → /opt/jmdn                 (node state: storage/thebe-kv, DB/gossipnode.db,
                                             config/peer.json, certs/, .bootstrapped sentinel)
  redis-data    → /data                     (Redis AOF log)
```

### ThebeDB — embedded KV log + PostgreSQL projection

The node's storage backend is ThebeDB (`thebe.enabled: true` in `jmdn.yaml`). It has two parts, and they live in different places:

| Part | Where it runs | Where its data lives | Configured by |
|---|---|---|---|
| KV log (BadgerDB) | Inside the `jmdn` process — no separate container | `jmdn-state` volume, `/opt/jmdn/storage/thebe-kv` | `thebe.kv_path` (`./storage/thebe-kv`, relative to `/opt/jmdn`) |
| SQL projection | `postgres` container | `postgres-data` volume | `THEBE_SQL_DSN` (set by compose; overrides `thebe.sql_dsn`) |

Compose builds the DSN for you: `postgres://jmdn:${POSTGRES_PASSWORD}@postgres:5432/jmdn?sslmode=disable`. It uses `sslmode=disable` only because it travels over the internal compose network to a Postgres shipped without TLS; the shipped yaml configs use `sslmode=require`.

**Credentials:** Set `POSTGRES_PASSWORD` in `.env`. Compose fails closed — `docker compose up` errors out if it is unset — and injects the same value into both the `postgres` container and the node's `THEBE_SQL_DSN`.

### Why Redis is separate, and what it does

When jmdn writes account state it enqueues to a Redis Stream (`XADD` on `accountsync:accounts`) and returns immediately; a background worker drains the stream and commits the writes to ThebeDB. The queue decouples account writes from synchronous DB commit latency. Running Redis as its own container gives independent restarts, standard tooling, and version upgrades without touching the node image.

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
| ThebeDB KV | `/opt/jmdn/storage/thebe-kv` (BadgerDB, embedded; volume: `jmdn-state`) |
| Starts after | `postgres` and `redis` report healthy (`depends_on: condition: service_healthy`) |

### `postgres` (ThebeDB SQL projection)

| Property | Value |
|---|---|
| Image | `postgres:16-alpine` |
| Container name | `jmdn-postgres` |
| Data dir | `/var/lib/postgresql/data` (volume: `postgres-data`) |
| User / database | `jmdn` / `jmdn` |
| Password | `POSTGRES_PASSWORD` from `.env` (required — compose fails closed if unset) |
| Server flags | `wal_level=logical`, `max_replication_slots=4`, `max_wal_senders=4` (required for the ThebeDB CDC / WAL consumer) |
| Port | `5432` on the compose network; published to the host on `127.0.0.1:5430` only |
| Health check | `pg_isready -U jmdn -d jmdn` — see [§11](#11-health-checks) |
| Stop grace | `120s`, so Postgres is not SIGKILLed while flushing WAL |

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
> data is verified and ENQUEUED — before the drain commits it to ThebeDB. If Redis
> loses the queue (crash without AOF, eviction, `FLUSHALL`), the anchor claims
> ranges whose effects never landed and reconciliation permanently skips them.
> `appendonly yes` must be set on EVERY deployment, including bare-metal nodes
> that don't use this compose file. Never point the node at a cache-mode Redis.

### `jmdn-bootstrap` (one-time, profile-gated)

Runs `bootstrap_sync.sh` in a temporary container that mounts `jmdn-state` at `/opt/jmdn`: downloads the chain snapshot from GCS, verifies checksums, extracts the snapshot's `storage/` (ThebeDB store) and `DB/` directories into `/opt/jmdn`, and writes a `/opt/jmdn/.bootstrapped` sentinel so it never runs again. `config/` and `certs/` are left in place. It does not touch `postgres-data`. Run it before the stack starts for the first time.

### The two scripts

- **`docker-entrypoint.sh`** — every start. Refuses to start without `/etc/jmdn/jmdn.yaml`, runs `bootstrap_sync.sh` itself if the sentinel is missing, ensures `DB/`, `storage/`, `config/`, `certs/` exist with correct ownership, generates TLS certs if missing, and drops to the `jmdn` user via `gosu`. It does not wait for any database — the KV store is embedded, and compose's `depends_on` holds the node until `postgres` and `redis` are healthy. The `ExecStartPre=` + `ExecStart=` of a systemd unit, combined.
- **`bootstrap_sync.sh`** — once only, guarded by the sentinel on the `jmdn-state` volume.

---

## 4. Quick Start — New VM Setup

Complete runbook for a fresh VM with Docker already installed.

### Prerequisites

- Docker 24+ and Docker Compose v2 — verify with `docker compose version`
- 50 GB+ free disk — **on the disk Docker actually uses.** Named volumes and container logs live under `/var/lib/docker`, not `/opt/jmdn`. Run `df -h /var/lib/docker` and see [§12](#12-volumes-and-data-management) → *Repointing Docker's storage* **before** your first `docker compose up`.
- **Host sizing: 8 GB RAM / 4 vCPU minimum.** `docker-compose.yml` caps the memory and CPU of the `jmdn`, `redis` and `jmdn-bootstrap` containers. The caps exist for *blast radius* — a leak OOM-kills one container instead of the host — **not** capacity planning, so scale them with the machine via `.env` (Step 2):

  | Host | `JMDN_MEM_LIMIT` | `JMDN_CPU_LIMIT` | `REDIS_MEM_LIMIT` | `REDIS_CPU_LIMIT` | `REDIS_MAXMEMORY` |
  |---|---|---|---|---|---|
  | 8 GB / 4c *(defaults)* | `4g` | `2.0` | `512m` | `0.5` | `384mb` |
  | 16 GB / 8c | `8g` | `6.0` | `1g` | `0.5` | `768mb` |
  | 32 GB / 16c | `16g` | `0` (unlimited) | `2g` | `1.0` | `1536mb` |
  | 64 GB / 32c | `32g` | `0` (unlimited) | `4g` | `1.0` | `3gb` |

  The `postgres` service has no memory or CPU cap in `docker-compose.yml`, and there is no `.env` variable for one — the RAM not given to jmdn and redis is what Postgres and the OS share.

  Rules of thumb: jmdn ~50% of host RAM (the embedded BadgerDB KV store counts against this limit), `REDIS_MAXMEMORY` ~75% of `REDIS_MEM_LIMIT`, and leave the remainder unallocated for the uncapped `postgres` container and the OS page cache. Memory caps are always warranted (exceeding = OOM kill); CPU is compressible, so on a dedicated host set `JMDN_CPU_LIMIT=0` and keep CPU caps for shared hosts. `0` = unlimited.

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

`.env` is minimal: it exists because the postgres and redis containers are separate processes that can't read `jmdn.yaml`. Everything else lives in `jmdn.yaml` (Step 3).

Template: `cp .env.docker.example .env && chmod 600 .env` and edit — or build it by hand (run from your install directory, `/opt/jmdn/jmdn` from Step 1):

```bash
openssl rand -base64 32   # → POSTGRES_PASSWORD
openssl rand -base64 32   # → REDIS_PASSWORD
openssl rand -base64 32   # → JMDN_SECURITY_EXPLORER_API_KEY

cat > .env << 'EOF'
# Prefixes volume/network names (jmdn_postgres-data ...). Keeps names stable
# regardless of checkout directory, so this guide's commands work verbatim.
# NEW INSTALLS ONLY — migrating an existing node? See §13 "One-time migration
# (v1.2.0 installs only)" first, or you'll repoint compose at empty volumes.
COMPOSE_PROJECT_NAME=jmdn

# JMDN release to run. Upgrade by changing this line — never by editing
# docker-compose.yml.
JMDN_VERSION=v2.0.0

# Used by the postgres container AND the node's THEBE_SQL_DSN (compose builds
# the DSN from it). Required — `docker compose up` fails if unset.
POSTGRES_PASSWORD=<generated>

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
| `thebe.sql_dsn` | Leave as shipped for Compose — overridden by `THEBE_SQL_DSN`, which compose builds from `POSTGRES_PASSWORD` in `.env`. Set only for bare-metal. |
| `database.redis.url` | `redis:6379` for Compose (the Redis service's Docker DNS name — already set in `jmdn_exchange.yaml`); `127.0.0.1:6379` for bare-metal. |
| `database.redis.password` | Leave blank for Compose — injected from `.env`. Set only for bare-metal. |
| `security.jwt_secret` | Generate: `openssl rand -base64 32` |
| `security.explorer_api_key` | Generate: `openssl rand -base64 32` |
| `fastsync.catch_up_from_block` | Bootstrap snapshot tip + 1 — **`13450`** for the current snapshot (tip 13449). `0` works but forces a slow full genesis scan every sync cycle. |

Compose mounts `./jmdn.yaml` at `/etc/jmdn/jmdn.yaml:ro`; the node reads it automatically. **This file must exist before `docker compose up`** — the mount is always active.

### Step 4 — Pre-pull the images (recommended)

Compose resolves the JMDN image as `ghcr.io/jupitermetalabs/jmdn:${JMDN_VERSION}` from your `.env` (Step 2), and pulls it — along with `postgres` and `redis` — automatically on first use. So this step is optional; run it to surface registry or network problems now rather than partway through the 10–30 minute bootstrap:

```bash
docker compose pull
```

Always pull through Compose rather than `docker pull <tag>` by hand — Compose uses the tag from `.env`, so the two can't drift apart. For the same reason, never edit the `image:` line in `docker-compose.yml`: leaving that file untouched is what lets `git pull` update it cleanly. To build from source instead, see [§8](#8-building-the-image-from-source).

### Step 5 — Bootstrap node state (first time only)

The chain snapshot (the ThebeDB `storage/` directory plus `DB/`) is loaded into the `jmdn-state` volume. Run this **before** the stack starts. One-time; subsequent restarts skip it. Takes 10–30 minutes depending on bandwidth. (If you skip it, the `jmdn` entrypoint runs the same bootstrap itself on first start — running it separately keeps the long download out of the node's startup and health-check window.)

```bash
docker compose run --rm jmdn-bootstrap
```

```
[bootstrap] First run detected — starting bootstrap sync.
[bootstrap] Listing parts from GCS: gs://jmdn-bootstrap/bootstrap-26072026/data-patched.part*
[bootstrap] Downloading parts to /opt/jmdn/bootstrap_tmp...
[bootstrap] Checksums OK.
[bootstrap] Extracting parts into sandbox: /opt/jmdn/data_tmp/sandbox
[bootstrap] Moving data to /opt/jmdn...
[bootstrap] Bootstrap complete. Sentinel written → /opt/jmdn/.bootstrapped
```

### Step 6 — Start the stack

```bash
docker compose up -d
docker compose logs -f jmdn
```

`jmdn` is created only after `postgres` and `redis` report healthy. Expected entrypoint output:

```
[entrypoint] Bootstrap sentinel found — node state already populated.
[entrypoint] Generating self-signed TLS certs...
[entrypoint] TLS certs generated.
[entrypoint] Starting JMDN as jmdn...
```

Followed by the node's ThebeDB initialisation (`thebedb: kv store OK`, `thebedb: sql engine OK`, `thebedb: db init OK`).

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

For quick tests, CI, or a minimal single-node setup without Compose. Production exchange deployments should use Compose ([§4](#4-quick-start--new-vm-setup)).

In this mode the entrypoint runs bootstrap itself on first start (10–30 min), then starts the node. The ThebeDB KV store is embedded in the node, but the SQL projection is not: you must provide a reachable PostgreSQL yourself and point the node at it with `THEBE_SQL_DSN` (the shipped default, `127.0.0.1:5430`, refers to the container itself). The node also uses Redis — set `database.redis.url` / `database.redis.password` in `jmdn.yaml` to a Redis it can reach.

```bash
docker run -d \
  --name jmdn \
  -v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro \
  -v jmdn-data:/opt/jmdn \
  -e THEBE_SQL_DSN='postgres://jmdn:<password>@<postgres-host>:5432/jmdn?sslmode=require' \
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
| `-v jmdn-data:/opt/jmdn` | Persists peer identity, certs, `DB/`, and the ThebeDB KV store (`storage/thebe-kv`) |
| `-e THEBE_SQL_DSN=...` | PostgreSQL for the ThebeDB SQL projection — required unless `thebe.sql_dsn` in your `jmdn.yaml` already points at a Postgres the container can reach. The node exits at startup (`FATAL thebedb: sql engine init failed`) if the SQL engine cannot be initialised. Use `sslmode=require` unless the connection stays on a private network to a Postgres without TLS |
| `-p 15000:15000` + `/udp` | P2P gossip — **required**; without inbound 15000 peers can't dial you |
| `-p 8545` / `-p 8546` | JSON-RPC / WebSocket |

Same port caveats as [§4](#4-quick-start--new-vm-setup) — don't publish 15050/15052/15055.

```bash
docker logs -f jmdn                    # follow startup
docker stop jmdn && docker rm jmdn     # volume jmdn-data is preserved
docker volume rm jmdn-data             # full reset (deletes all chain data)

# Force re-bootstrap
docker exec jmdn rm /opt/jmdn/.bootstrapped && docker restart jmdn
```

Environment overrides: pass `-e KEY=value`; see the reference table in [§7](#7-configuration).

---

## 6. First Run — What Actually Happens

Understanding this prevents confusion when something goes wrong.

```
# Step A: bootstrap (run once before the stack)
docker compose run --rm jmdn-bootstrap
        │
        └─ bootstrap_sync.sh  (jmdn-state volume mounted at /opt/jmdn)
              ├─ Checks /opt/jmdn/.bootstrapped
              │   EXISTS? → exits immediately (already done)
              ├─ Lists + downloads parts from GCS over public HTTP
              ├─ Downloads checksums.md5, verifies
              ├─ Moves existing storage/ and DB/ aside (temporary copy, discarded with the container)
              ├─ Extracts into a sandbox, finds storage/ → its parent is the data root
              ├─ Moves data root contents → /opt/jmdn
              ├─ chown 3322:3322 + restore owner write on storage/ and DB/
              └─ touch /opt/jmdn/.bootstrapped

# Step B: stack starts
docker compose up -d
        │
        ├─► postgres starts → healthy when pg_isready passes
        ├─► redis starts    → healthy when redis-cli ping passes
        └─► jmdn starts (only after postgres + redis are healthy)
                ├─ [root] docker-entrypoint.sh
                ├─ /etc/jmdn/jmdn.yaml missing? → exit 1
                ├─ check /opt/jmdn/.bootstrapped
                │   MISSING? → runs bootstrap_sync.sh now (exit 1 if it fails)
                ├─ [root] mkdir -p /opt/jmdn/{config,DB,storage,certs}; chown jmdn
                ├─ [root] generate self-signed TLS certs if ca.crt missing
                └─ gosu jmdn → jmdn
                        ├─ opens ThebeDB KV at ./storage/thebe-kv
                        └─ connects to postgres:5432 via THEBE_SQL_DSN
```

**Sentinel:** `/opt/jmdn/.bootstrapped` lives on the `jmdn-state` volume. While it exists, bootstrap is skipped. The bootstrap snapshot only populates `jmdn-state`; it does not write to `postgres-data`.

```bash
# Force re-bootstrap (compose)
docker compose stop jmdn
docker run --rm -v jmdn_jmdn-state:/data alpine rm -f /data/.bootstrapped
docker compose run --rm jmdn-bootstrap
docker compose start jmdn
```

This replaces the KV store and `DB/` but leaves `postgres-data` as it was. To rebuild everything from a snapshot, including the SQL projection, use the [one-time chain reset](#one-time-chain-reset-only-when-explicitly-instructed) in §13.

---

## 7. Configuration

Most configuration belongs in `jmdn.yaml`. The environment variables below are the exceptions: Docker-mode routing values that differ from bare-metal, and the container passwords postgres/redis need directly. Set them in `.env`.

| Variable | Value | Purpose |
|---|---|---|
| `POSTGRES_PASSWORD` | *(from .env, required)* | postgres container password; compose also uses it to build `THEBE_SQL_DSN` |
| `THEBE_SQL_DSN` | *(set by compose)* | `postgres://jmdn:${POSTGRES_PASSWORD}@postgres:5432/jmdn?sslmode=disable` — overrides `thebe.sql_dsn`. Not set in `.env` |
| `REDIS_PASSWORD` | *(from .env, required)* | Redis `--requirepass`; compose passes it to the node as `JMDN_DATABASE_REDIS_PASSWORD` |
| `JMDN_PORTS_API` | `0` (disabled) | Set `8090` to enable the Explorer API |
| `JMDN_SECURITY_JWT_SECRET` | `""` | JWT signing secret |
| `COMPOSE_PROJECT_NAME` | *(directory name)* | Prefixes volume/network names — set to `jmdn` in `.env` so this guide's volume commands work verbatim. Changing it on a live node repoints compose at different volumes |
| `JMDN_VERSION` | `latest` | Image tag for `jmdn` and `jmdn-bootstrap`. Pin a release in `.env`; upgrades change this line only ([§13](#13-upgrading)) |
| `JMDN_MEM_LIMIT` / `JMDN_CPU_LIMIT` | `4g` / `2.0` | jmdn container caps — scale to host, `0` = unlimited ([§4](#4-quick-start--new-vm-setup)) |
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

Two stages: builder (`golang:1.26.9-bookworm`, pinned by digest, `CGO_ENABLED=1`) then runtime (`debian:bookworm-slim`, pinned by digest). The final image contains no Go toolchain or source.

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
docker compose exec jmdn ls /opt/jmdn/storage/thebe-kv/   # ThebeDB KV (BadgerDB)
docker compose exec jmdn ps aux

# PostgreSQL (ThebeDB SQL projection)
docker compose exec postgres pg_isready -U jmdn -d jmdn
docker compose exec postgres psql -U jmdn -d jmdn -c '\dt'

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
docker compose exec jmdn nc -zv postgres 5432
docker compose exec jmdn nc -zv redis 6379

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
| `postgres` | `pg_isready -U jmdn -d jmdn` | 10s | — |

The fallback matters because `:8545` is what exchanges actually consume, and operators running `jmdn_default.yaml` (Explorer API disabled) would otherwise show permanently `unhealthy` while serving RPC fine.

**Startup ordering:** `jmdn` declares `depends_on` with `condition: service_healthy` for both `postgres` and `redis`, so compose does not create it until both health checks pass. This orders container *creation* only — if `postgres` or `redis` restarts later, Docker does not restart `jmdn`.

```bash
# Why is a health check failing?
docker inspect jmdn | python3 -c "
import json,sys
h=json.load(sys.stdin)[0]['State']['Health']
for c in h['Log'][-3:]: print(c['ExitCode'], c['Output'])
"
# jmdn unhealthy   → node still starting, or neither endpoint reachable (§14)
# redis unhealthy  → wrong REDIS_PASSWORD, or OOM
# postgres unhealthy → docker compose logs postgres
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
docker volume inspect jmdn_jmdn-state --format '{{.Mountpoint}}'
# → /var/lib/docker/volumes/jmdn_jmdn-state/_data
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
  - /mnt/data-disk/jmdn/postgres-data:/var/lib/postgresql/data   # postgres service, instead of postgres-data:
  - /mnt/data-disk/jmdn/jmdn-state:/opt/jmdn                     # jmdn service, instead of jmdn-state:
```

### Volume layout

```
jmdn-state  (/opt/jmdn in the jmdn container):
├── .bootstrapped       ← sentinel (delete to force re-bootstrap)
├── storage/
│   └── thebe-kv/       ← ThebeDB KV log (BadgerDB, embedded in jmdn)
├── config/peer.json    ← node peer identity (auto-generated first run)
├── certs/              ← TLS (auto-generated or operator-mounted)
└── DB/
    └── gossipnode.db   ← SQLite node manager state

postgres-data  (/var/lib/postgresql/data in the postgres container):
└── PostgreSQL data directory — ThebeDB SQL projection (database `jmdn`)

redis-data  (/data in the redis container):
└── Redis AOF log (account sync queue)
```

With `thebe.cdc.enabled: true`, the CDC event log and dead-letter file are written to the paths set by `thebe.cdc.log_path` / `thebe.cdc.dlq_path` (relative paths resolve under `/opt/jmdn`, i.e. on `jmdn-state`).

Address→transaction lookups and the explorer totals are served by the ThebeDB SQL projection (the `transactions` / `accounts` tables), which the node writes synchronously at block apply. There is no separate index file to back up or rebuild. (Older releases kept a `txindex.db` here; a leftover file is unused and can be deleted.)

### Backup and restore

```bash
# Backup — stop cleanly first. The KV log (jmdn-state) and the SQL projection
# (postgres-data) are backed up together so they stay consistent.
docker compose stop jmdn postgres
for v in postgres-data jmdn-state; do
  docker run --rm -v jmdn_$v:/data -v $(pwd)/backups:/backups \
    alpine tar czf /backups/$v-$(date +%Y%m%d).tar.gz -C /data .
done
docker compose start postgres jmdn
```

```bash
# Restore
docker compose down
for v in postgres-data jmdn-state; do
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
docker compose down -v                     # removes postgres-data, redis-data, jmdn-state
docker compose run --rm jmdn-bootstrap     # downloads + verifies + extracts
docker compose up -d

# 5. Verify it catches up from the new baseline
docker compose logs -f jmdn
curl -s http://localhost:8545 -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Why all three volumes go: **`jmdn-state`** holds the chain itself (the ThebeDB KV store under `storage/`, replaced by the snapshot) plus node identity and certs — wiping it also gives a new identity (it re-peers automatically); **`postgres-data`** is the ThebeDB SQL projection of the old chain; **`redis-data`** is the account-sync queue, whose stale entries reference the old chain and must not replay onto fresh state.

**To keep node identity and custom certs**, replace step 4's `down -v` with a selective wipe. `bootstrap_sync.sh` replaces only `storage/` and `DB/` in `jmdn-state` and leaves `config/` and `certs/` in place, so remove the other two volumes and the sentinel instead:

```bash
docker compose down
docker volume ls --filter name=postgres-data --filter name=redis-data   # confirm exact names
docker volume rm jmdn_postgres-data jmdn_redis-data                     # use the names listed above
docker run --rm -v jmdn_jmdn-state:/data alpine rm -f /data/.bootstrapped
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

### Upgrading Redis or PostgreSQL

Their tags (`redis:7-alpine`, `postgres:16-alpine`) are pinned in `docker-compose.yml` with no `.env` variable. Editing that file dirties your checkout, so override it instead (Compose merges automatically; the file is untracked):

```yaml
# docker-compose.override.yml
services:
  redis:
    image: redis:7.4-alpine
```

```bash
docker compose pull redis && docker compose up -d redis   # jmdn reconnects automatically
```

The same override pattern works for `postgres`. Stay within the same PostgreSQL major version (16): a major-version change cannot start on the existing `postgres-data` directory as-is and needs a data migration (dump/restore or `pg_upgrade`). Back up `postgres-data` and `jmdn-state` first ([§12](#12-volumes-and-data-management)).

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

### PostgreSQL not reachable

```bash
docker compose ps postgres && docker compose logs postgres
docker compose exec postgres pg_isready -U jmdn -d jmdn
docker compose exec jmdn nc -zv postgres 5432
docker compose logs jmdn | grep -i "thebedb"
# "FATAL thebedb: sql engine init failed" → node could not initialise the SQL projection:
#   check POSTGRES_PASSWORD is set in .env (THEBE_SQL_DSN is built from it)
#   and that the postgres container is healthy
```

`docker compose up` refusing to start with `set POSTGRES_PASSWORD in .env` means the variable is missing from `.env` — compose fails closed rather than using a default password.

### Chain data missing (bootstrap did not populate the KV store)

```bash
docker run --rm -v jmdn_jmdn-state:/data alpine ls -a /data/ /data/storage/
# Expect: .bootstrapped  storage/  DB/  config/  certs/ — storage/ holds the ThebeDB
#   store (thebe-kv/ at the default thebe.kv_path)
# No .bootstrapped / empty storage/ → bootstrap hasn't run: docker compose run --rm jmdn-bootstrap
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

### Transaction-address lookups returning 503

`eth_getTransactionsByAddress` and `GET /explorer/address/:address/transactions` read the ThebeDB SQL projection directly. A `503` from either means the projection database is unreachable (check `thebe.sql_dsn` / Postgres health), not that an index is syncing — there is no index to rebuild. The former `rebuildindex`, `rebuildrange` and `txindexstatus` commands are retired and only print a notice.

### Full container reset

Deletes all chain data and re-bootstraps — see [One-time chain reset](#one-time-chain-reset-only-when-explicitly-instructed) in §13 for the complete procedure, including how to preserve node identity.
