# JMDT - Decentralized Node

**JMDT Layer 2 Blockchain - The Truth Layer for all information.**

Restoring authenticity in digital infrastructure by privately verifying humans, and decentralising their data.

**Whitepaper**: [JMDT White Paper (PDF)](./docs/JMDT%20White%20Paper%20-%20latest.pdf)

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
- **Docker & Docker Compose**: Containerization platform
- **Yggdrasil**: Decentralized mesh networking protocol
- **ImmuDB**: Tamper-proof database (installed automatically)

### Quick Setup

The easiest way to set up the environment is using the provided setup script:

```bash
# Make the setup script executable and run it
sudo ./Scripts/setup_dependencies.sh
```

This script will automatically install all prerequisites:
1. **Go Programming Language**
2. **ImmuDB**
3. **Yggdrasil Network**

### Manual Installation

If you prefer to install dependencies manually or need to install them individually, use the `setup_dependencies.sh` script with specific flags:

#### 1. Install Go
```bash
sudo ./Scripts/setup_dependencies.sh --go
```

#### 2. Install ImmuDB
```bash
sudo ./Scripts/setup_dependencies.sh --immudb
```

#### 3. Install Yggdrasil
```bash
sudo ./Scripts/setup_dependencies.sh --yggdrasil
```

### Build the Application

After installing prerequisites:

1. Build the application using the build script:
   ```bash
   ./Scripts/build.sh
   ```

For full node setup, configuration, and service installation see **[GETTING_STARTED.md](./GETTING_STARTED.md)**.

## Running a Node

Basic node startup (requires configuration file):

```bash
./jmdn -config /etc/jmdn/config.env
```

### Configuration

We recommend using the configuration generation script:

```bash
./Scripts/setup_config.sh
```

Alternatively, you can configure via flags (see `jmdn --help` or `docs/CONFIG.md`):

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
| `fastsync <peer_multiaddr>`               | Fast sync blockchain data with a peer |
| `dbstate`                                 | Show current ImmuDB database state    |
| `exit`                                    | Exit the program                      |

---
**Document Version**: Based on JMDT White Paper 1.3 | Nov 2025
**Copyright**: © 2025 JMDT | Jupiter Meta Labs Foundation | Seychelles
