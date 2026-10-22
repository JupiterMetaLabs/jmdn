# Getting Started — Running a JMDN Node

> **Prefer Docker?** See **[DOCKER.md](./DOCKER.md)** for the container-based deployment guide — the recommended path for production nodes.
>
> This guide walks you through setting up and running a JMDN node from source on Linux or Raspberry Pi.
> Estimated time: **10–30 minutes** on a clean machine.

---

## Prerequisites

Before you begin, ensure your machine meets the following requirements.

### System Requirements

| Requirement | Minimum |
|---|---|
| **OS** | Ubuntu 20.04+, Debian 11+, Raspberry Pi OS (64-bit) |
| **Architecture** | x86_64 or ARM64 |
| **RAM** | 2 GB |
| **Disk** | 10 GB free |
| **Network** | Internet access |

### Software Requirements

| Tool | Version | Notes |
|---|---|---|
| **Git** | Any | Required to clone the repo |
| **Go** | Version in `go.mod` | Installed by `setup_dependencies.sh` |
| **GCC** | Any | Required for CGO build (`gcc` package) |
| **PostgreSQL** | 15+ | Installed by `setup_dependencies.sh`; holds ThebeDB's SQL projection |
| **Yggdrasil** | Latest | Installed automatically |
| **Redis** | 5+ | Installed by `setup_dependencies.sh`; account sync queue |

---

## Step 1 — Install Git

```bash
# Ubuntu / Debian
sudo apt update && sudo apt install -y git curl

# CentOS / RHEL
sudo yum install -y git curl

# macOS (development only)
brew install git
```

---

## Step 2 — Clone the Repository

```bash
git clone https://github.com/JupiterMetaLabs/jmdn.git
cd jmdn
```

To run a specific release:

```bash
git checkout v2.0.0  # replace with target version
```

---

## Step 3 — Install Dependencies

Run the unified setup script. This installs Go, Yggdrasil, PostgreSQL and Redis. It asks whether PostgreSQL and Redis should run as native services or in Docker.

```bash
sudo ./Scripts/setup_dependencies.sh
```

> **Note:** After Go is installed, restart your shell or run `source ~/.bashrc` (or `~/.zshrc`) to update your `PATH`.

To install dependencies individually:

```bash
sudo ./Scripts/setup_dependencies.sh --go
sudo ./Scripts/setup_dependencies.sh --yggdrasil
sudo ./Scripts/setup_dependencies.sh --redis
sudo ./Scripts/setup_dependencies.sh --solidity
```

Choose how PostgreSQL and Redis are installed with `--storage-local` (native
services), `--storage-docker` (containers via compose) or `--storage-none`
(you provide them).

> **Redis password:** the installer writes it straight into `database.redis.password` in `/etc/jmdn/jmdn.yaml`, and the same value into `REDIS_PASSWORD` in the compose `.env` that the Redis container reads. There is no `redis.env` and nothing to source: the node picks the password up from its own config whether you start it via `start_jmdn_wrapper.sh` or run the binary directly. Re-running the installer reuses the existing value — from `jmdn.yaml` first, then the compose `.env` — and only generates a new one when neither holds a password, so it never rotates silently. `jmdn.yaml` must already exist (copy `jmdn_default.yaml` to `/etc/jmdn/jmdn.yaml` first) or the installer stops and tells you to.

---

## Step 4 — Build the Binary

```bash
./Scripts/build.sh
```

This compiles the `jmdn` binary into your current directory with version metadata embedded (commit, branch, tag, build time).

To verify the build:

```bash
./jmdn --version
```

---

## Step 5 — Configure Your Node

Copy the default template and manually inject your Node Alias and secrets:

```bash
sudo mkdir -p /etc/jmdn
sudo cp jmdn_default.yaml /etc/jmdn/jmdn.yaml
sudo nano /etc/jmdn/jmdn.yaml
```

*(Note: The legacy `setup_config.sh` tool is deprecated as it lacks automated secrets injection).*

The node reads `jmdn.yaml` from `/etc/jmdn/` first, then the working directory. Set the PostgreSQL connection in `thebe.sql_dsn` (or the `THEBE_SQL_DSN` environment variable). Any other key can be overridden with a `JMDN_`-prefixed environment variable (for example `JMDN_PORTS_API=8090`).

For all available options, see `jmdn_default.yaml` and `config/settings/`, or run:

```bash
./jmdn --help
```

---

## Step 6 — Install and Start Services

Install the binary and register the `jmdn` service. `--mode local` uses native
PostgreSQL and Redis services; `--mode docker` runs them with Docker Compose.
Without a flag the script asks.

```bash
sudo ./Scripts/install_services.sh --mode local
```

> Before opening firewall rules, review **[PORTS.md](./PORTS.md)** for the full security posture of each port and recommended cloud firewall rules.

### Optional (recommended) — Bootstrap from a chain snapshot

Skip this and a fresh node will start from genesis and slowly scan every block
on its own — that's what `fastsync.catch_up_from_block: 0` in `jmdn.yaml`
means. For anything other than a throwaway dev node, load the pre-built
snapshot instead, same as the Docker path does with `jmdn-bootstrap`:

```bash
sudo SNAPSHOT_SHA256=<published archive sha256> ./Scripts/bootstrap_sync.sh
```

`SNAPSHOT_SHA256` is the SHA-256 of the full snapshot archive, published by
JupiterMeta separately from the bucket. The script refuses to install a
snapshot that does not match it, and installs only the `storage/` and `DB/`
chain-data directories from the archive.

Requires `curl`, `wget`, `awk`, `md5sum`, `sha256sum`, `tar`, `python3` on `PATH` — install
any that are missing (`sudo apt install -y wget python3`, most are already
present on a stock Ubuntu/Debian image). Downloads and verifies the chain
snapshot into `/opt/jmdn`, the data directory `install_services.sh` uses. Takes 10–30 minutes depending on bandwidth; safe to re-run — it
skips immediately if `/opt/jmdn/.bootstrapped` already exists.

> **Ownership note:** the script chowns the extracted `storage/` and `DB/`
> directories to `JMDN_UID` (default `3322`, the Docker image's `jmdn` user).
> On bare metal with the default `SERVICE_USER=root` this has no effect. If
> the node runs as a non-root user, pass that user's uid:
> ```bash
> sudo SNAPSHOT_SHA256=<sha256> JMDN_UID=$(id -u jmdn) ./Scripts/bootstrap_sync.sh
> ```

To force a fresh snapshot later (e.g. after a long time offline):

```bash
sudo rm /opt/jmdn/.bootstrapped
sudo SNAPSHOT_SHA256=<sha256> ./Scripts/bootstrap_sync.sh
```

On the sequencer, a snapshot older than the node's last checkpoint will not
boot: `checkpoint.floor_file` (default `/opt/jmdn/checkpoint-floor.json`)
remembers the highest checkpoint and the boot check treats an older store as a
rollback. After a deliberate restore, delete that file (and lower
`checkpoint.min_seq` if you set it) before starting the node.

### Sequencer checkpoints

The sequencer signs a chain-head checkpoint once per committee epoch with its
own BLS key, and every boot verifies the latest one against that key. Nothing
needs to be configured. Two optional hardening steps:

- `checkpoint.signer_bls_pub` pins the key in `/etc/jmdn/jmdn.yaml` (the boot
  log prints the value in use as `signer_bls_pub`). With it set, replacing the
  key file on the data volume does not let a forged checkpoint through.
- `checkpoint.min_seq`: after each restart the log prints `checkpoint_seq`.
  Copying it into the config makes the floor survive a whole-volume restore,
  which rolls the floor file back with the store.

Start the services:

```bash
sudo systemctl start jmdn
```

Enable them to start automatically on reboot:

```bash
sudo systemctl enable jmdn
```

---

## Step 7 — Verify the Node is Running

```bash
# Check service status
sudo systemctl status jmdn

# Follow live logs
sudo journalctl -u jmdn -f
```

A healthy node will log peer connections and block synchronisation activity within a few seconds of starting.

---

## Manual Run (Development)

To run the node directly without systemd — useful for local development or debugging:

```bash
./jmdn
```

> **Important:** PostgreSQL must be reachable at `thebe.sql_dsn` before
> starting `jmdn`. ThebeDB's KV store is embedded and needs no separate service.

---

## Updating an Existing Node

To update a running node to the latest code, use the deploy script (Ansible calls this automatically in production):

```bash
sudo ./Scripts/deploy.sh
```

This script builds a new binary, performs an atomic swap, restarts the service, and automatically rolls back to the previous version if the health check fails.

---

## Troubleshooting

### Service fails to start

```bash
sudo journalctl -u jmdn -n 100 --no-pager
```

Check for: missing config file, PostgreSQL not reachable, or port conflicts.

### PostgreSQL connection errors

Check that PostgreSQL is running and that `thebe.sql_dsn` matches it:

```bash
sudo systemctl status postgresql
psql "<your thebe.sql_dsn>" -c 'select 1'
```

The default DSN uses `sslmode=require`. For a local database without TLS,
set `sslmode=disable` in the DSN.

### Minimal logs on Raspberry Pi

If console logging is turned off (`logging.console.enabled: false` in `jmdn.yaml`), logs are still available via journald:

```bash
sudo journalctl -u jmdn -f
```

To enable console logs, set `logging.console.enabled: true` in `/etc/jmdn/jmdn.yaml` (or `JMDN_LOGGING_CONSOLE_ENABLED=true`) and restart the service.

### Go not found after installation

```bash
export PATH="/usr/local/go/bin:${PATH}"
```

Add this line to your `~/.bashrc` or `~/.zshrc` for persistence.

---

## Common Commands

| Command | Description |
|---|---|
| `sudo systemctl restart jmdn` | Restart the node |
| `sudo systemctl stop jmdn` | Stop the node |
| `sudo journalctl -u jmdn -f` | Follow live logs |
| `sudo journalctl -u jmdn -n 50 --no-pager` | View last 50 log lines |
| `./jmdn --version` | Check running binary version |

---

*For architecture and protocol documentation, see [README.md](./README.md).*
