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
| Health check | None — verified reason, not an assumption; see §11 |

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
- **Host sizing: 8 GB RAM / 4 vCPU minimum — then scale the limits to your
  host.** `docker-compose.yml` caps each container's memory and CPU. The
  point of the caps is *blast radius* — a leak in one container OOM-kills
  that container instead of the host — **not** capacity planning, so they
  must grow with the machine. The defaults suit an entry-level 8-16 GB VM;
  on a bigger host, override them in the same `.env` file as your passwords
  (Step 2). Nothing else changes — `docker compose up -d` applies them.

  | Host | `JMDN_MEM_LIMIT` | `JMDN_CPU_LIMIT` | `IMMUDB_MEM_LIMIT` | `IMMUDB_CPU_LIMIT` | `REDIS_MEM_LIMIT` | `REDIS_CPU_LIMIT` | `REDIS_MAXMEMORY` |
  |---|---|---|---|---|---|---|---|
  | 8 GB / 4c *(defaults)* | `4g` | `2.0` | `2g` | `1.0` | `512m` | `0.5` | `384mb` |
  | 16 GB / 8c | `8g` | `6.0` | `4g` | `2.0` | `1g` | `0.5` | `768mb` |
  | 32 GB / 16c | `16g` | `0` (unlimited) | `8g` | `4.0` | `2g` | `1.0` | `1536mb` |
  | 64 GB / 32c | `32g` | `0` (unlimited) | `16g` | `8.0` | `4g` | `1.0` | `3gb` |

  Rules of thumb behind the table: give jmdn ~50% of host RAM and immudb
  ~25%, keep `REDIS_MAXMEMORY` at ~75% of `REDIS_MEM_LIMIT`, and always
  leave ~20% of the host unallocated — that's not waste, it's the OS page
  cache, which immudb read performance depends on. Memory and CPU deserve
  different treatment: memory is incompressible (exceeding the cap = OOM
  kill), so a cap is always warranted; CPU is compressible (contention just
  means kernel scheduling), so on a host dedicated to this node a hard CPU
  cap only throttles you while cores sit idle — set `JMDN_CPU_LIMIT=0`
  (unlimited) there and keep CPU caps for shared hosts. `0` means unlimited
  for any of these. For comparison, bare-metal's stated 2 GB minimum
  (`GETTING_STARTED.md`) is a floor for a single lightweight node process,
  not this full stack with a separate ledger and queue.
- **amd64 host.** The `jmdn` image itself is published for linux/amd64,
  arm64, and arm/v7 (§8), but `codenotary/immudb:1.10.0` — the version this
  repo pins in `docker-compose.yml`, the Dockerfile, and `go.mod`'s immudb
  client SDK — is an **amd64-only** image (immudb only started publishing
  arm64 images at `1.11.1`, newer than what this repo is pinned to). On a
  Raspberry Pi or other arm64 host, use bare-metal (`GETTING_STARTED.md`)
  instead — its `setup_dependencies.sh` installs a native arm64 immudb
  binary directly and doesn't hit this. Bumping the pinned version to pick
  up arm64 support is a client/server upgrade project (every immudb version
  reference in this repo moves in lockstep), not a one-line change.
- 50 GB+ free disk space (chain snapshot on first run)
  > **Where does that 50GB need to live?** Named volumes and container logs
  > default to `/var/lib/docker`, not `/opt/jmdn`. On a VM with a small root
  > disk and a separate large data disk, check the free space that matters —
  > see [§12 Volumes and Data Management](#12-volumes-and-data-management) →
  > "Repointing Docker's storage to a different disk" before your first
  > `docker compose up`.
- Open ports on firewall/security group:

| Port | Protocol | Purpose |
|---|---|---|
| 15000 | TCP **+ UDP** | P2P gossip (LibP2P, TCP + QUIC) — **must be public**; without inbound 15000 the node can dial out but can't be dialed by peers, degrading to outbound-only participation |
| 8545 | TCP | JSON-RPC — exchange endpoint |
| 8546 | TCP | WebSocket RPC |
| 8090 | TCP | Explorer API (optional, localhost-only by default) |

> **Port 15001 (Yggdrasil direct-messaging) is deliberately not published here.**
> The Yggdrasil daemon isn't wired up in this image — no `tun0`/`ygg0`
> interface, confirmed live on a running container — so the feature can't
> work yet regardless. Publishing it wouldn't fix that either way: mesh
> traffic would arrive over a TUN device inside the container's own network
> namespace, not over the Docker bridge, so Docker's port-forwarding has
> nothing to do with reaching it over the mesh. See `PORTS.md` §4.

> **Ports 15050, 15052, 15055 are also deliberately not in this table** — not
> exposed by default. `15052` (DID service) runs `RegisterDID` with **no
> authentication**, so publishing it lets anyone who reaches it register
> arbitrary DIDs, not just resolve existing ones. See `PORTS.md` §5, §7, §8
> before opening any of the three.

See **[PORTS.md](./PORTS.md)** for the full security posture of every port.

### Step 1 — Clone the repo

Clone into any directory you like — `/opt/jmdn/jmdn` is our suggested
default. The directory name has no effect on the stack: volume and network
names are controlled by `COMPOSE_PROJECT_NAME` in `.env` (Step 2), not by
where you cloned.

```bash
mkdir -p /opt/jmdn
git clone https://github.com/JupiterMetaLabs/jmdn.git /opt/jmdn/jmdn
cd /opt/jmdn/jmdn
git checkout main
```

> Running as root? That's fine — Docker itself runs as root. The `jmdn` user only exists **inside the container** and is managed by the entrypoint script. You do not need a `jmdn` user on the host.

### Step 2 — Create a .env file (container passwords only)

The `.env` file is minimal — it exists only because the immudb and redis containers are separate processes that cannot read `jmdn.yaml`. They need their passwords passed in via environment variables. Everything else lives in `jmdn.yaml` (Step 3).

A filled-in-able template is at [`.env.docker.example`](./.env.docker.example) in the repo root — `cp .env.docker.example .env` and edit, or build it by hand with the heredoc below.

Generate the two passwords:

```bash
openssl rand -base64 32   # → IMMUDB_PASSWORD
openssl rand -base64 32   # → REDIS_PASSWORD
```

```bash
cat > /opt/jmdn/jmdn/.env << 'EOF'
# Compose project name — prefixes volume/network names (jmdn_immudb-data ...).
# Pinning it here keeps those names stable regardless of the checkout
# directory, so every volume/backup command in this guide works verbatim.
# NEW INSTALLS ONLY — if you're migrating an existing node, do NOT copy this
# line as-is; see §13 "Upgrading a node installed with the v1.2.0 guide"
# first, or you'll repoint compose at empty volumes.
COMPOSE_PROJECT_NAME=jmdn

# JMDN release to run. Set to a release tag (recommended for production);
# upgrade by changing this line — never by editing docker-compose.yml.
JMDN_VERSION=v1.2.1

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

> **Know where these secrets end up.** Compose injects `.env` values into
> container environments, and anything in a container's environment is
> readable in plaintext by anyone who can talk to the Docker socket:
> `docker inspect jmdn | grep -A5 Env` shows every password and API key.
> This is the standard trade-off for container-native config (bare-metal is
> no better — `/etc/jmdn/jmdn.yaml` is plaintext on disk too), but it means:
> (1) access to the Docker socket is equivalent to access to the secrets —
> don't add users to the `docker` group casually; (2) never paste
> `docker inspect` output into tickets/chat without scrubbing the `Env`
> block; (3) if your org requires stronger handling, front these with a
> secrets manager that writes `.env` at deploy time and rotates after.

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

Compose reads the version from `JMDN_VERSION` in `.env` (Step 2) — you
already pinned it there. Don't edit the image tag in `docker-compose.yml`;
keeping that file untouched is what lets `git pull` update it cleanly later.

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
  -p 15000:15000 -p 15000:15000/udp \
  -p 8545:8545 \
  -p 8546:8546 \
  ghcr.io/jupitermetalabs/jmdn:latest
```

| Flag | Purpose |
|---|---|
| `-v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro` | **Required** — node exits with an error if this is missing |
| `-v jmdn-data:/opt/jmdn` | Persists peer identity, certs, DB, and immudb data across restarts |
| `-p 15000:15000` + `/udp` | P2P gossip (LibP2P, TCP + QUIC) — **required**; without inbound 15000 the node can't be dialed by peers |
| `-p 8545:8545` | JSON-RPC (exchange endpoint) |
| `-p 8546:8546` | WebSocket RPC |

### With Explorer API enabled

The Explorer API (`/api/v1/node/version`, etc.) is disabled by default. Enable it with `JMDN_PORTS_API`:

```bash
docker run -d \
  --name jmdn \
  -v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro \
  -v jmdn-data:/opt/jmdn \
  -p 15000:15000 -p 15000:15000/udp \
  -p 8545:8545 \
  -p 8546:8546 \
  -p 8090:8090 \
  -e JMDN_PORTS_API=8090 \
  ghcr.io/jupitermetalabs/jmdn:latest
```

### Ports intentionally left out of these examples

`15052` (DID service), `15050` (BlockGen API), and `15055` (BlockGRPC) are not
exposed by default. `15052`'s `RegisterDID` has no authentication, so
publishing it lets anyone reach it register arbitrary DIDs, not just resolve
existing ones. See `PORTS.md` §5, §7, §8 before enabling any of the three.

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
| `COMPOSE_PROJECT_NAME` | *(directory name)* | Prefixes volume/network names — set to `jmdn` in `.env` (Step 2) so this guide's volume commands work verbatim. Existing deployments without it keep their current prefix; changing it on a live node repoints compose at different volumes |
| `JMDN_VERSION` | `latest` | JMDN image tag for the `jmdn` and `jmdn-bootstrap` services. Pin a release in `.env`; upgrades change this line only (§13) |
| `JMDN_MEM_LIMIT` / `JMDN_CPU_LIMIT` | `4g` / `2.0` | jmdn container resource caps — scale to host, `0` = unlimited (see §4 sizing table) |
| `IMMUDB_MEM_LIMIT` / `IMMUDB_CPU_LIMIT` | `2g` / `1.0` | immudb container resource caps (see §4 sizing table) |
| `REDIS_MEM_LIMIT` / `REDIS_CPU_LIMIT` | `512m` / `0.5` | redis container resource caps (see §4 sizing table) |
| `REDIS_MAXMEMORY` | `384mb` | Redis self-enforced memory ceiling — keep at ~75% of `REDIS_MEM_LIMIT` |
| `BOOTSTRAP_MEM_LIMIT` / `BOOTSTRAP_CPU_LIMIT` | `2g` / `1.0` | `jmdn-bootstrap` container resource caps — one-time snapshot download/extract, not in the §4 sizing table since it doesn't run alongside the other three; raise `BOOTSTRAP_MEM_LIMIT` if bootstrapping a very large snapshot fails on decompress |

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

### Enabling file-based logging (optional)

`logging.file.enabled` is `false` by default — console output goes to
stdout, which Docker's `json-file` driver captures and rotates for you (see
[§10 Log Retention](#10-log-retention)). That's the safe default; most
operators don't need to change it.

If you do enable it (`logging.file.enabled: true` in `jmdn.yaml`) — for
example to feed a log shipper that tails files instead of `docker logs` —
**`logging.file.path` must resolve to somewhere under the `jmdn-state`
volume mount (`/opt/jmdn/...`)**. Anything else writes to the container's
writable layer, which is not backed by a volume and is deleted the moment
the container is recreated (`docker compose up -d` after an image pull,
`docker compose down` + `up`, etc.) — the file will look fine until the
next deploy silently wipes it.

```yaml
logging:
  file:
    enabled: true
    path: "/opt/jmdn/logs/app.log"   # under the jmdn-state volume — survives recreation
```

The rotation settings next to it (`max_size_mb`, `max_age_days`,
`max_backups`, `compress`) are already sane defaults and self-contained —
no extra Docker config needed once the path is right.

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
| `jmdn` | Two-tier: `GET /api/v1/node/version` → HTTP 200, falling back to JSON-RPC `eth_blockNumber` on :8545 | 30s | 300s |
| `redis` | `redis-cli ping` | 10s | — |
| `immudb` | none — see below | — | — |

The fallback matters for two reasons: `:8545` is the endpoint exchanges
actually consume, so it's the more honest liveness signal — and operators
running `jmdn_default.yaml` (where `ports.api` is disabled) would otherwise
show permanently `unhealthy` for a node that's serving RPC fine.

Readiness for immudb is handled by the jmdn entrypoint: it loops `nc -z immudb 3322` for up to 120s (default) before starting the node process. Override with `IMMUDB_READY_TIMEOUT=300` if your host is slow to load a large snapshot.

### Why immudb has no Compose healthcheck

- **No shell exists in the image.** `codenotary/immudb:1.10.0` is built on
  `scratch` with exactly `/usr/sbin/immudb`, `/usr/local/bin/immuadmin`, and
  CA certs — no coreutils, no `nc`, `wget`, or `curl`. Any
  `healthcheck.test` that execs a command inside this container cannot run.
- **The image's own baked-in `HEALTHCHECK CMD immuadmin status` doesn't work
  cold.** `immuadmin status` performs a login handshake before it may query
  the server, and a freshly started container has no cached token — it
  fails on auth, not on reachability, every time.
- **A network-based probe is technically possible** — immudb's `Health` RPC
  is unauthenticated and reachable via gRPC reflection (on by default), so a
  sidecar running `grpcurl` against `immudb.schema.ImmuService/Health` would
  work (the generic `grpc_health_probe` tool would not — immudb uses its own
  proto, not the standard `grpc.health.v1.Health` service). Not implemented:
  an extra always-on container isn't worth it when the jmdn entrypoint's
  `nc -z immudb 3322` wait loop already gates startup ordering correctly.

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

### Crash-loop detection (Docker behaves differently from systemd here)

On bare metal, systemd's defaults (`StartLimitIntervalSec=10`, `StartLimitBurst=5`)
mean a unit that crashes 5 times in 10 seconds gives up and lands in a
visible `failed` state — `systemctl status jmdn` shouts about it, and
`Restart=always` stops retrying until someone runs `systemctl reset-failed`.

`restart: unless-stopped` in `docker-compose.yml` has no such limit. A
container that crashes on startup will restart forever on Docker's backoff
schedule, silently, with `docker compose ps` showing `Restarting` — it never
reaches a terminal "give up" state the way a systemd unit does. If nobody's
watching, a crash loop can run for days without anything paging anyone.

Watch `RestartCount` instead of waiting for a `failed` state that will
never come:

```bash
# One-off check
docker inspect --format='{{.RestartCount}}' jmdn

# Alert-friendly: exits non-zero if restarts are climbing
#   (run this from your monitoring cron/agent, not by hand)
COUNT=$(docker inspect --format='{{.RestartCount}}' jmdn)
if [ "$COUNT" -gt 5 ]; then
    echo "jmdn has restarted ${COUNT} times — likely crash-looping" >&2
    exit 1
fi
```

If you'd rather have Docker itself give up (closer to the systemd
behavior) instead of retrying forever, swap the restart policy for the
`jmdn` service in `docker-compose.yml`:

```yaml
# restart: unless-stopped        # retries forever
restart: on-failure:5             # gives up after 5 restarts, container
                                   # then sits Exited — same visibility
                                   # tradeoff systemd makes by default
```

Trade-off either way: `unless-stopped` self-heals from transient issues
(brief network blip, host reboot) without paging anyone, but can mask a
real crash loop. `on-failure:5` surfaces crash loops immediately as a
stopped container, but also won't self-heal from a transient issue past
attempt 5 — someone has to notice and restart it manually. Pick based on
whether you have monitoring watching `RestartCount`/container state at all;
if you don't yet, `on-failure:5` is the safer default since a stopped
container is much harder to miss than a quietly-restarting one.

---

## 12. Volumes and Data Management

> Volume names below assume `COMPOSE_PROJECT_NAME=jmdn` (Step 2). On an
> older install without that `.env` line, the prefix is your checkout
> directory's name instead — check with `docker volume ls`.

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

### Repointing Docker's storage to a different disk

**On bare metal** (`GETTING_STARTED.md`), `JMDN_DATA=/opt/jmdn` is just a path —
mount any disk there and the chain data lives on it. There's no ambiguity
about which disk fills up.

**In Docker**, that isolation disappears by default. Every named volume
(`immudb-data`, `jmdn-state`, `redis-data`) *and* every container's JSON log
file live under Docker's `data-root`, which defaults to `/var/lib/docker`.
On most cloud VM images that's the OS root disk — the same disk as the OS,
`apt` cache, and everything else. The chain snapshot alone can be tens of
GB; add pulled images (~1-2 GB) and log budgets (jmdn 250MB + immudb 60MB +
redis 30MB, see [§10 Log Retention](#10-log-retention)) and a small root
disk fills up fast, well before the node itself reports any problem.

Check what disk `/var/lib/docker` actually sits on **before** your first
`docker compose up`:

```bash
df -h /var/lib/docker
```

If that's not the disk you sized for 50GB+, repoint Docker's `data-root` at
the correct disk before starting the stack (this must happen before any
containers/volumes exist — moving it afterward means manually copying
`/var/lib/docker`):

```bash
sudo systemctl stop docker
sudo mkdir -p /mnt/data-disk/docker

# Move any existing data (skip on a fresh install)
sudo rsync -aP /var/lib/docker/ /mnt/data-disk/docker/

sudo mkdir -p /etc/docker
cat <<'EOF' | sudo tee /etc/docker/daemon.json
{
  "data-root": "/mnt/data-disk/docker"
}
EOF

sudo systemctl start docker
docker info | grep "Docker Root Dir"
# → Docker Root Dir: /mnt/data-disk/docker
```

Alternative if you'd rather not touch the daemon config: bind-mount host
paths on the correct disk instead of named volumes for the two data-heavy
services in `docker-compose.yml`:

```yaml
volumes:
  - /mnt/data-disk/jmdn/immudb-data:/opt/jmdn/data   # instead of immudb-data:
  - /mnt/data-disk/jmdn/jmdn-state:/opt/jmdn          # instead of jmdn-state:
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
    ├── gossipnode.db      ← SQLite node manager state
    ├── txindex.db         ← SQLite address→transaction index (eth_getTransactionsByAddress, /explorer)
    ├── txindex.db-wal     ← WAL journal (present while the node is running)
    └── txindex.db-shm     ← WAL shared-memory index
```

`txindex.db` is fully rebuildable from ImmuDB — it's a derived index, not a source of truth. Losing it (missing volume, disk issue, corruption) is not catastrophic: the node detects it's empty/behind and re-catchups it automatically in the background on next start (`eth_getTransactionsByAddress` / `/explorer/.../transactions` return "still syncing" until that completes — see §14 for manual rebuild). It's still included in the backup below since that's simpler than special-casing it, but restoring it is optional.

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

Two things update on different channels, and it helps to keep them apart:

| Channel | Command | What it delivers |
|---|---|---|
| **Image** | `docker compose pull` | New `jmdn` binary + in-container scripts (entrypoint, wrapper, bootstrap) — the actual node software |
| **Repo** | `git pull` | `docker-compose.yml` (limits, healthcheck, service wiring) and this documentation |

A version upgrade only *requires* the image channel. Pulling the repo too is
recommended — releases sometimes ship compose improvements — and is always
safe because nothing you configure lives in tracked files: your settings are
in `.env` and `jmdn.yaml`, both gitignored.

> `docker-deploy.sh`'s rollback only restores the previous **image** — it
> doesn't touch the `immudb-data` / `jmdn-state` volumes. For anything past
> a routine point release, snapshot them first with the steps in §12
> [Backup](#12-volumes-and-data-management).

### Which path applies to you?

- **Installed with the v1.2.0 guide** (the first Docker release — `docker-compose.yml` has a hand-edited `image:` tag, and `.env` has no `JMDN_VERSION`) → run the **one-time migration** below first. After that, every future upgrade is Option A.
- **Installed with this guide** (`.env` already has `JMDN_VERSION`) → skip the migration, go straight to **Option A**.

### One-time migration (v1.2.0 installs only)

The v1.2.0 guide had you pin releases by editing the `image:` line inside
`docker-compose.yml`. That edit makes your checkout dirty, so `git pull`
will refuse or merge-conflict on the compose file. Migrate once — afterwards
you're on the same footing as any new install and use Option A below:

```bash
# 1. Park your local compose edit (the image tag is its only local change)
git stash

# 2. Refresh the repo — brings the compose file that reads JMDN_VERSION from .env
git pull

# 3. Your stashed tag edit is now obsolete — the tag lives in .env instead
git stash drop

# 4. Pin your version in .env (REQUIRED — without it the tag defaults to :latest)
echo "JMDN_VERSION=v1.2.1" >> .env

# 5. Pull + restart as usual
docker compose pull jmdn && docker compose up -d jmdn
```

> **Do NOT add `COMPOSE_PROJECT_NAME=jmdn` to an existing node's `.env`.**
> Your volumes are named after the project name your stack was created with
> (usually your checkout directory). Changing it repoints compose at fresh
> empty volumes and your node will refuse to start (missing bootstrap
> sentinel). Leave it unset — everything keeps working under your existing
> names. If you ever *want* to adopt the standard names, follow the volume
> copy steps in the `docker-compose.yml` header comment during a planned
> maintenance window.

Once migrated, you will not need this section again — new releases only ever touch `.env`.

### Option A — Upgrade via pre-built image (recommended, all installs after migration)

```bash
# 1. Set the new version in .env — adds the line if missing, portable (no sed -i,
#    so it works the same on Linux and macOS)
grep -v '^JMDN_VERSION=' .env > .env.tmp && echo 'JMDN_VERSION=v1.2.1' >> .env.tmp && mv .env.tmp .env

# 2. (Recommended) refresh compose + docs — clean, nothing local is tracked
git pull

# 3. Pull + restart with automatic rollback on failure
./Scripts/docker-deploy.sh
```

`docker-deploy.sh` snapshots the currently running image, pulls the new one,
and restarts the node — **automatically rolling back to the previous image**
if the new one either fails to come up at all or comes up but fails its
health check. It also guards against two overlapping runs (e.g. a cron job
firing mid-upgrade): a second invocation exits immediately with an
"already in progress" message instead of racing the first.

To do the same by hand instead (no automatic rollback):

```bash
docker compose pull jmdn
docker compose up -d jmdn

# JSON-RPC is published by default; Explorer API (port 8090) is opt-in
# (Step 2) so it isn't a reliable check unless you enabled it yourself.
curl -s http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

> After upgrading, check the release notes for any changes to `fastsync.catch_up_from_block` — if the bootstrap snapshot was refreshed, update this value in `jmdn.yaml` and restart.

### Upgrading a node installed with the v1.2.0 guide (one-time migration)

The v1.2.0 guide had you pin releases by editing the `image:` line inside
`docker-compose.yml`. That edit makes your checkout dirty, so `git pull`
will refuse or merge-conflict on the compose file. Migrate once — afterwards
every upgrade is the 3 steps above:

```bash
# 1. Park your local compose edit (the image tag is its only local change)
git stash

# 2. Refresh the repo — brings the compose file that reads JMDN_VERSION from .env
git pull

# 3. Your stashed tag edit is now obsolete — the tag lives in .env instead
git stash drop

# 4. Pin your version in .env (REQUIRED — without it the tag defaults to :latest)
echo "JMDN_VERSION=v1.2.1" >> .env

# 5. Pull + restart as usual
docker compose pull jmdn && docker compose up -d jmdn
```

> **Do NOT add `COMPOSE_PROJECT_NAME=jmdn` to an existing node's `.env`.**
> Your volumes are named after the project name your stack was created with
> (usually your checkout directory). Changing it repoints compose at fresh
> empty volumes and your node will refuse to start (missing bootstrap
> sentinel). Leave it unset — everything keeps working under your existing
> names. If you ever *want* to adopt the standard names, follow the volume
> copy steps in the `docker-compose.yml` header comment during a planned
> maintenance window.

### Option B — Build and deploy from source

```bash
git fetch --tags
git checkout v1.2.0   # or the branch/commit you want

# The built tag must match what docker-compose.yml will request — that's
# JMDN_VERSION from .env, or "latest" if it's unset. Get this wrong and
# `docker compose up` silently keeps running the old image.
TAG=$(grep '^JMDN_VERSION=' .env | cut -d= -f2)
TAG=${TAG:-latest}

docker build \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  --build-arg GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD) \
  --build-arg GIT_TAG=$(git describe --tags --always --dirty) \
  -t ghcr.io/jupitermetalabs/jmdn:${TAG} \
  .
docker compose up -d jmdn
```

> Don't follow this build with `./Scripts/docker-deploy.sh` — it runs
> `docker compose pull`, which would overwrite your local build with
> whatever's on the registry under the same tag.

### Upgrading Redis or ImmuDB

Unlike `jmdn`, these image tags are pinned directly in `docker-compose.yml` —
there's no `.env` variable for them. Editing that file in place would dirty
your checkout and fight the next `git pull`, so pin the new version in a
`docker-compose.override.yml` instead (Compose merges it in automatically,
and it's untracked):

```yaml
# docker-compose.override.yml
services:
  redis:
    image: redis:7.4-alpine
```

```bash
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

The health check tries `GET /api/v1/node/version` on port 8090 first, then
falls back to JSON-RPC `eth_blockNumber` on 8545 — so `unhealthy` means
**both** endpoints are failing, which almost always means the node process
itself isn't up yet (check `docker compose logs jmdn`). If the node is up
but you expected the Explorer API tier specifically to pass, two common
causes:

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

**Prevent this instead of reacting to it.** `bare-metal build.sh` overwrites
a single binary in place — nothing accumulates. Rebuilding a Docker image
from source (§8) is different: every `docker build` leaves the previous
image's layers behind as dangling, and the builder cache grows on every
build. Neither is cleaned up automatically. On a host that rebuilds
regularly (CI, frequent version bumps), schedule a weekly prune instead of
waiting for `docker system prune` to become a fire drill:

```bash
# /etc/cron.weekly/docker-prune (chmod +x)
#!/usr/bin/env bash
# Only removes dangling/unused images older than 7 days — never touches
# running containers, in-use images, or named volumes (immudb-data, jmdn-state, redis-data).
docker image prune -af --filter "until=168h"
docker builder prune -af --filter "until=168h"
```

Or via this tool's scheduler if you'd rather not manage a cron file by hand.

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

### Transaction-address index stuck, missing, or returning errors

`eth_getTransactionsByAddress` (RPC) and `GET /explorer/address/:address/transactions` (Explorer API) are backed by a small SQLite index (`DB/txindex.db`) that the node rebuilds from ImmuDB automatically in the background — it's never restored from a backup, only ever caught up live. While it's catching up (first boot, after a fresh volume, or after `rebuildindex`), both endpoints return a "still syncing" / `503` response instead of wrong or empty-looking data.

Check status:

```bash
docker exec -it jmdn jmdn -cmd txindexstatus
# READY — last indexed block: 184213
# or: SYNCING (catchup in progress) — last indexed block: 91004
```

If it's stuck in `SYNCING` for far longer than the chain height would justify, or the RPC/Explorer address-history endpoints keep erroring, force a full rebuild from genesis:

```bash
docker exec -it jmdn jmdn -cmd rebuildindex
```

This wipes and re-scans the entire chain from ImmuDB — safe to run any time, but can take a while on a long chain (it runs in the background; the node stays up and keeps serving everything else while it does). Watch progress with `txindexstatus` or in the logs (`[txindex] Indexed up to block …`).

For a narrower gap (e.g. you know blocks in a specific range were missed), repair just that range instead of a full rebuild:

```bash
docker exec -it jmdn jmdn -cmd rebuildrange <from_block> <to_block>
```

### Full container reset (nuclear option)

```bash
# WARNING: this deletes all chain data. You will re-bootstrap from scratch.
docker compose down -v
docker compose run --rm jmdn-bootstrap
docker compose up -d
```
