# JMDT - Decentralized Node

**JMDT Layer 2 Blockchain - The Truth Layer for all information.**

Restoring authenticity in digital infrastructure by privately verifying humans, and decentralising their data.

**Whitepaper**: [JMDT White Paper (PDF)](./docs/JMDT-WP.pdf)

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

- **Zero-Knowledge Proofs (ZKPs)**: zk-SNARKs and zk-STARKs for privacy-preserving identity verification and transactions.
- **Decentralized Identity (DID)**: W3C-standardized self-sovereign identities without PII exposure.
- **AVC Consensus**:
    - VRF-based buddy node selection from an authenticated committee
    - BLS-signed votes bound to block hash, chain id and height; `2f+1` quorum
    - libp2p gossip for block and vote propagation
- **ThebeDB storage**: append-only KV log (BadgerDB) with a PostgreSQL projection for queries.
- **Ethereum compatibility**: JSON-RPC and WebSocket facade, EVM execution for smart contracts.

## Running a Node

### Requirements

- **OS**: Linux (Ubuntu/Debian, RHEL/CentOS, Arch, Alpine) or macOS for development
- **Architecture**: x86_64, ARM64 or ARMv7
- **Memory / disk**: 2 GB RAM, 10 GB free disk minimum
- **Go**: the version in `go.mod` (currently 1.26); CGO and GCC are required
- **PostgreSQL**: ThebeDB's SQL projection
- **Redis**: account sync queue
- **Yggdrasil**: optional mesh messaging

### Quick start (from source)

```bash
sudo ./Scripts/setup_dependencies.sh
./Scripts/build.sh
sudo mkdir -p /etc/jmdn
sudo cp jmdn_default.yaml /etc/jmdn/jmdn.yaml
sudo nano /etc/jmdn/jmdn.yaml
./jmdn
```

The node reads `jmdn.yaml` from `/etc/jmdn/` or the working directory. Most
settings can also be overridden with `JMDN_`-prefixed environment variables.
Set the PostgreSQL connection in `thebe.sql_dsn` (or `THEBE_SQL_DSN`).

For systemd services, snapshot bootstrap and troubleshooting, see
**[GETTING_STARTED.md](./GETTING_STARTED.md)**. For the container stack
(node + PostgreSQL + Redis), see **[DOCKER.md](./DOCKER.md)**.

### Common flags

Run `./jmdn --help` for the full list. Ports set here override `ports:` in `jmdn.yaml`.

| Flag | Description | Default |
| --- | --- | --- |
| `-seednode <host:port>` | Seed node gRPC URL for peer registration | `""` |
| `-alias <name>` | Peer alias registered with the seed node | `""` |
| `-api <port>` | Explorer API (0 = disabled) | `0` |
| `-facade <port>` | Ethereum JSON-RPC | `8545` |
| `-ws <port>` | Ethereum WebSocket RPC | `8546` |
| `-metrics <port>` | Prometheus metrics (empty = disabled) | `""` |
| `-chainID <id>` | Chain ID | `7000700` |
| `-cmd <command>` | Run one CLI command against a running node and exit | `""` |
| `-version` | Print version information and exit | `false` |

Operator commands (`listpeers`, `stats`, `dbstate`, `fastsync`, `catchup`, ...)
are documented in **[CLI_COMMANDS.md](./CLI_COMMANDS.md)**.

## Documentation

| Document | Contents |
| --- | --- |
| [GETTING_STARTED.md](./GETTING_STARTED.md) | Install, configure and run a node from source |
| [DOCKER.md](./DOCKER.md) | Docker / Docker Compose deployment |
| [PORTS.md](./PORTS.md) | Every port, its exposure and firewall guidance |
| [CLI_COMMANDS.md](./CLI_COMMANDS.md) | Operator CLI commands |
| [ARCHITECTURE.md](./ARCHITECTURE.md) | Package layout and data flow |
| [explorer/API_DOCUMENTATION.md](./explorer/API_DOCUMENTATION.md) | Explorer REST API |
| [Block/README_GRPC.md](./Block/README_GRPC.md) | Block gRPC server |
| [SmartContract/README.md](./SmartContract/README.md) | EVM and smart contract module |
| [docs/quickstart-foundry.md](./docs/quickstart-foundry.md) | Using Foundry against a JMDT node |
| [CONTRIBUTING.md](./CONTRIBUTING.md), [RELEASING.md](./RELEASING.md), [CHANGELOG.md](./CHANGELOG.md) | Development workflow, releases, history |
| [docs/audit/](./docs/audit/) | Consensus and storage design reviews and incident write-ups |

## Security

JMDN has been independently audited by [Terasoft Technologies](https://www.terasoft.in), a **STQC & CERT-IN empaneled** test laboratory. The source code review covered 69,000 lines of Go, following OWASP Secure Coding Guidelines and CERT Secure Coding Standards.

**Certificate**: [TERA/CERT-IN/03/2026/CR/16](./audits/2026-03-terasoft-certin-vapt/TERA_CERT-IN_03_2026_CR_16_Certificate.pdf) — issued 12 March 2026, covering release [v1.1.0](https://github.com/JupiterMetaLabs/jmdn/releases/tag/v1.1.0).

All identified findings were remediated and verified closed. See [`audits/2026-03-terasoft-certin-vapt/VERIFICATION.md`](./audits/2026-03-terasoft-certin-vapt/VERIFICATION.md) for independent verification instructions and checksum matching.

The full VAPT report is available on request — contact security@jupitermeta.io.

To report a vulnerability, see [SECURITY.md](./SECURITY.md).

---
**Document Version**: Based on JMDT White Paper 1.3 | Updated Oct 2026
**Copyright**: © 2026 JMDT | Jupiter Meta Labs Foundation | Seychelles