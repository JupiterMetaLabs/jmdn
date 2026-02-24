# Getting Started — Running a JMDN Node

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
git checkout v2.5.0  # replace with target version
```

---

## Step 3 — Install Dependencies

Run the unified setup script. This installs Go, ImmuDB, and Yggdrasil.

```bash
sudo ./Scripts/setup_dependencies.sh
```

> **Note:** After Go is installed, restart your shell or run `source ~/.bashrc` (or `~/.zshrc`) to update your `PATH`.

To install dependencies individually:

```bash
sudo ./Scripts/setup_dependencies.sh --go         # Go runtime only
sudo ./Scripts/setup_dependencies.sh --immudb     # ImmuDB only
sudo ./Scripts/setup_dependencies.sh --yggdrasil  # Yggdrasil only
```

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

Generate your node configuration interactively. This creates `/etc/jmdn/config.env`:

```bash
sudo ./Scripts/setup_config.sh
```

Follow the prompts to set your **Node Alias** and configure ports. For all available options, see `config/config.go` or run:

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
