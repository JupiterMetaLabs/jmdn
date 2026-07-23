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
| **Go** | 1.25+ | Installed automatically by `setup_dependencies.sh` |
| **GCC** | Any | Required for CGO build (`gcc` package) |
| **ImmuDB** | Latest | Installed automatically |
| **Yggdrasil** | Latest | Installed automatically |
| **Redis** | 5+ | Installed automatically; optional — powers the account sync queue, node falls back to direct ImmuDB writes if unavailable |

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
git checkout v1.2.2  # replace with target version
```

---

## Step 3 — Install Dependencies

Run the unified setup script. This installs Go, ImmuDB, Yggdrasil, and Redis.

```bash
sudo ./Scripts/setup_dependencies.sh
```

> **Note:** After Go is installed, restart your shell or run `source ~/.bashrc` (or `~/.zshrc`) to update your `PATH`.

To install dependencies individually:

```bash
sudo ./Scripts/setup_dependencies.sh --go         # Go runtime only
sudo ./Scripts/setup_dependencies.sh --immudb     # ImmuDB only
sudo ./Scripts/setup_dependencies.sh --yggdrasil  # Yggdrasil only
sudo ./Scripts/setup_dependencies.sh --redis      # Redis only
```

> **Redis password:** the script generates a random password on first run and saves it to `/etc/jmdn/redis.env` (root-only). If you start the node via `start_jmdn_wrapper.sh` (the standard systemd/launchd/rc.d path, Step 6), this is automatic — the wrapper sources `redis.env` and exports `JMDN_DATABASE_REDIS_PASSWORD` for you, no manual step needed. Only set `database.redis.password` in `/etc/jmdn/jmdn.yaml` by hand if you're running the binary directly instead of via the wrapper — the config generator in Step 5 does not do this for you yet. Re-running the script reuses the same password rather than rotating it.

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
sudo cp jmdn_default.yaml /etc/jmdn/jmdn.yaml
sudo nano /etc/jmdn/jmdn.yaml
```

*(Note: The legacy `setup_config.sh` tool is deprecated as it lacks automated secrets injection).*

For all available options, see `config/config.go` or run:

```bash
./jmdn --help
```

---

## Step 6 — Install and Start Services

Install the binary to `/usr/local/bin/` and register systemd services (`jmdn` and `immudb`):

```bash
sudo ./Scripts/install_services.sh
```

> Before opening firewall rules, review **[PORTS.md](./PORTS.md)** for the full security posture of each port and recommended cloud firewall rules.

### Optional (recommended) — Bootstrap from a chain snapshot

Skip this and a fresh node will start from genesis and slowly scan every block
on its own — that's what `fastsync.catch_up_from_block: 0` in `jmdn.yaml`
means. For anything other than a throwaway dev node, load the pre-built
snapshot instead, same as the Docker path does with `jmdn-bootstrap`:

```bash
sudo ./Scripts/bootstrap_sync.sh
```

Requires `curl`, `wget`, `awk`, `md5sum`, `tar`, `python3` on `PATH` — install
any that are missing (`sudo apt install -y wget python3`, most are already
present on a stock Ubuntu/Debian image). Downloads and verifies the chain
snapshot into `/opt/jmdn/data`, same location `install_services.sh` just
created. Takes 10–30 minutes depending on bandwidth; safe to re-run — it
skips immediately if `/opt/jmdn/data/.bootstrapped` already exists.

> **Ownership note:** the script chowns `/opt/jmdn/data` to `IMMUDB_UID`
> (default `3322`, matching the Docker image's `jmdn` user) so it can be
> re-run unmodified against Docker-style snapshots. On bare metal with the
> default `SERVICE_USER=root` (see `install_services.sh`), immudb runs as
> root and ignores file ownership, so this is a no-op in practice. If you set
> `SERVICE_USER` to a non-root user, pass `IMMUDB_UID=<that user's uid>` so
> immudb can actually read its own data:
> ```bash
> sudo IMMUDB_UID=$(id -u jmdn) ./Scripts/bootstrap_sync.sh
> ```

To force a fresh snapshot later (e.g. after a long time offline):

```bash
sudo rm /opt/jmdn/data/.bootstrapped
sudo ./Scripts/bootstrap_sync.sh
```

Start the services:

```bash
sudo systemctl start immudb
sudo systemctl start jmdn
```

Enable them to start automatically on reboot:

```bash
sudo systemctl enable immudb
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
./jmdn -config /etc/jmdn/config.env
```

> **Important:** ImmuDB must be running before starting `jmdn`.
> Either start it via systemd (`sudo systemctl start immudb`) or manually:
> ```bash
> immudb --dir /opt/jmdn/data
> ```

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

Check for: missing config file, ImmuDB not running, or port conflicts.

### ImmuDB connection errors

Ensure ImmuDB is running and accessible:

```bash
sudo systemctl status immudb
```

If you see `server state is older than the client one`, ImmuDB's state is ahead of the local client cache. This typically resolves after a clean restart:

```bash
sudo systemctl restart immudb && sudo systemctl restart jmdn
```

### Minimal logs on Raspberry Pi

The default configuration disables console logging (`LOG_CONSOLE=false`) on low-resource devices. Logs are available via journald:

```bash
sudo journalctl -u jmdn -f
```

To enable console logs, set `LOG_CONSOLE=true` in `/etc/jmdn/config.env` and restart the service.

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
