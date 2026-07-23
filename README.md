# JMDT - Decentralized Node

**JMDT Layer 2 Blockchain - The Truth Layer for all information.**

Restoring authenticity in digital infrastructure by privately verifying humans, and decentralising their data.

**Whitepaper**: [JMDT White Paper (PDF)](./docs/JMDT_Layer_2_Blockchain.pdf)

[![CERT-IN Security Audit](https://img.shields.io/badge/CERT--IN_Audit-Passed-brightgreen?logo=shield&logoColor=white)](./audits/2026-03-terasoft-certin-vapt/VERIFICATION.md)
[![Auditor](https://img.shields.io/badge/Auditor-Terasoft_Technologies-blue)](./audits/2026-03-terasoft-certin-vapt/TERA_CERT-IN_03_2026_CR_16_Certificate.pdf)

Jupiter Meta Data Token Chain (JMDT) is a modular, Ethereum-based Layer 2 (L2) blockchain protocol designed to address the scalability, privacy, and compliance limitations of traditional blockchain systems. Built with Zero-Knowledge Proofs (ZKPs), Decentralized Identity (DID), and our own proprietary Asynchronous Validation Consensus (AVC), JMDT delivers a high-performance, privacy-preserving infrastructure tailored for both decentralized applications and enterprise-grade solutions.

## Vision and Mission

> “Building the truth layer for human intelligence—where verified humans own their data, enterprises access verified insights, and privacy is absolute.”

We are enabling a single source of truth for all human information. A next-gen blockchain infrastructure where decentralized identities, zero-knowledge proofs, and verifiable computation allow us to redefine how data is owned, controlled and monetized without compromising compliance across digital platforms.

**Privacy-first. Truth-verified. Human-owned. Decentralized by design.**

## Architecture

The system is built on a modular architecture combining several advanced distributed systems concepts:

### Layer 3: Enterprise DAG & Consent Infrastructure
- **Enterprise DAG Nodes**: Private DAG nodes for high-throughput, localized operations.
- **Smart Contracts for Consent**: Govern user onboarding and access rights.
- **InterDAG Bridge**: Facilitates cross-enterprise collaboration.

### Layer 2: ZK-Enabled State and Rollup Layer
- **zk Engine (SNARK/STARK)**: Verifies DAG transactions using zero-knowledge proofs.
- **DID Engine**: W3C-compliant Decentralized Identity for private authentication.
- **AVC Consensus**: Quorum-based buddy voting and asynchronous global tally.
- **RISC Zero zkVM**: Executes rollup verification logic as a deterministic guest program.

### Layer 1: Ethereum Settlement Layer
- **Finality**: Anchors L2 state changes to Ethereum L1 for global settlement and security.

## Technology Stack

- **Zero-Knowledge Proofs (ZKPs)**: Uses zk-SNARKs and zk-STARKs for privacy-preserving identity verifications and transactions.
- **Decentralized Identity (DID)**: W3C-standardized self-sovereign identities without PII exposure.
- **AVC Consensus**:
    - VRF-based Buddy Node Selection
    - Zero-Knowledge Proof Integration
    - Gossip Protocol & Bloom Filters
    - CRDT-based Conflict Resolution
    - ImmuDB Append-Only Ledger

## JMDN - Decentralized Node Operation

Run a node to participate in the JMDT network.

### Setup & Requirements

#### System Requirements
- **Operating System**: Ubuntu 18.04+, Debian 10+, elementaryOS, or Linux Mint
- **Architecture**: x86_64, ARM64, or ARMv7
- **Memory**: Minimum 2GB RAM
- **Storage**: At least 10GB free space
- **Network**: Internet connection for initial setup

#### Dependencies
- **Go 1.25+**: Programming language runtime
- **Yggdrasil**: Decentralized mesh networking protocol
- **ImmuDB**: Tamper-proof database (installed automatically via setup script)
- **Redis**: Account sync worker queue (installed automatically via setup script; optional — the node falls back to direct ImmuDB writes if unavailable)

### Quick Setup — From Source

```bash
# Install Go, ImmuDB, Yggdrasil, and Redis
sudo ./Scripts/setup_dependencies.sh

# Build the binary
./Scripts/build.sh

# Run the node
./jmdn -config /etc/jmdn/config.env
```

For full setup including configuration, firewall rules, and systemd service installation, see **[GETTING_STARTED.md](./GETTING_STARTED.md)**.

> **Docker** (v1.2.0+): Container-based deployment is available for operators who prefer it. See [DOCKER.md](./DOCKER.md) for the full setup guide.

## Running a Node

Basic node startup (requires configuration file):

```bash
./jmdn -config /etc/jmdn/config.env
```

### Configuration

**Manual Configuration (Recommended):**
Copy the default template and manually inject your Node Alias and secrets:

```bash
sudo cp jmdn_default.yaml /etc/jmdn/jmdn.yaml
sudo nano /etc/jmdn/jmdn.yaml
```

*(Note: The legacy `setup_config.sh` tool is deprecated as it lacks automated secrets injection).*

Alternatively, you can configure via flags (see `jmdn --help`):

| Flag                     | Description                          | Default   |
| ------------------------ | ------------------------------------ | --------- |
| `-config`              | Path to config file                  | ""        |
| `-seed`                | Run as a seed node                   | `false` |
| `-connect <multiaddr>` | Connect to a seed node               | ""        |
| `-metrics <port>`      | Prometheus metrics port              | "8080"    |
| `-logdir <path>`       | Log directory                        | "./logs"  |
| `-console`             | Log to console                       | `false` |
| `-ygg`                 | Enable Yggdrasil messaging           | `true`  |
| `-explorer <port>`     | Run blockchain explorer (0=disabled) | 0         |
| `-api <port>`          | Run ImmuDB API (0=disabled)          | 0         |

### Available Commands

| Command                                     | Description                           |
| ------------------------------------------- | ------------------------------------- |
| `msg <peer_multiaddr> <message>`          | Send a message via libp2p             |
| `ygg <peer_multiaddr\|ygg_ipv6> <message>` | Send a message via Yggdrasil          |
| `file <peer_multiaddr> <filepath>`        | Send a file to a peer                 |
| `addpeer <peer_multiaddr>`                | Add a peer to managed nodes           |
| `removepeer <peer_id>`                    | Remove a peer from managed list       |
| `listpeers`                               | Show all managed peers                |
| `peers`                                   | Request updated peer list from seed   |
| `stats`                                   | Show messaging statistics             |
| `broadcast <message>`                     | Broadcast to all connected peers      |
| `fastsync <peer_multiaddr>`               | Fast sync blockchain data with a peer        |
| `catchup <peer_multiaddr> <from_block>`   | CatchUpSync from a specific block with a peer |
| `dbstate`                                 | Show current ImmuDB database state           |
| `exit`                                    | Exit the program                             |

## Security

JMDN has been independently audited by [Terasoft Technologies](https://www.terasoft.in), a **STQC & CERT-IN empaneled** test laboratory. The source code review covered 69,000 lines of Go, following OWASP Secure Coding Guidelines and CERT Secure Coding Standards.

**Certificate**: [TERA/CERT-IN/03/2026/CR/16](./audits/2026-03-terasoft-certin-vapt/TERA_CERT-IN_03_2026_CR_16_Certificate.pdf) — issued 12 March 2026, covering release [v1.1.0](https://github.com/JupiterMetaLabs/jmdn/releases/tag/v1.1.0).

All identified findings were remediated and verified closed. See [`audits/2026-03-terasoft-certin-vapt/VERIFICATION.md`](./audits/2026-03-terasoft-certin-vapt/VERIFICATION.md) for independent verification instructions and checksum matching.

The full VAPT report is available on request — contact security@jupitermeta.io.

To report a vulnerability, see [SECURITY.md](./SECURITY.md).

---
**Document Version**: Based on JMDT White Paper 1.3 | Updated Jun 2026
**Copyright**: © 2026 JMDT | Jupiter Meta Labs Foundation | Seychelles
