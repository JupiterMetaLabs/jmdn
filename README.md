# P2P-Communication

A robust peer-to-peer communication framework featuring blockchain synchronization, distributed consensus, and resilient messaging.

## Architecture

The system is built on a modular architecture combining several advanced distributed systems concepts:

### Core Components

- **Libp2p Network Layer**: Foundation for peer discovery, connection management, and direct messaging
- **ImmuDB Integration**: Tamper-proof database providing blockchain-like features including Merkle trees
- **FastSync Protocol**: Efficient blockchain state synchronization between nodes
- **Gossip Protocol**: For reliable information dissemination across the network
- **CRDT Engine**: Conflict-free replicated data types ensuring eventual consistency
- **Bloom Filter Optimization**: Memory-efficient set membership testing to prevent redundant data transfers.

## Features

- **Decentralized Communication**: Direct peer-to-peer messaging without central servers
- **Blockchain Synchronization**: Efficiently sync blockchain state between nodes using FastSync
- **Bloom Filter Optimization**: Reduces network traffic by identifying missing data
- **Web Explorer & API**: Browser-based blockchain explorer and REST API
- **Heartbeat Monitoring**: Automatic health checks and peer status tracking
- **Yggdrasil Integration**: Alternative messaging protocol for improved privacy and connectivity
- **Metrics & Monitoring**: Prometheus-compatible metrics for system performance
- **Seed Node Discovery**: Bootstrap mechanism for network joining

## Setup & Requirements

### Dependencies

- Go 1.18+
- ImmuDB
- SQLite (via mattn/go-sqlite3)

### Installation

1. Clone the repository:

   ```
   cd P2P-Communication
   ```
2. Install Go dependencies:

   ```
   go mod download
   ```
3. Build the application:

   ```
   go build -o p2p-node
   ```

## Running a Node

Basic node startup:

```
./p2p-node
```

### Configuration Options

| Flag                     | Description                          | Default   |
| ------------------------ | ------------------------------------ | --------- |
| `-seed`                | Run as a seed node                   | `false` |
| `-connect <multiaddr>` | Connect to a seed node               | ""        |
| `-heartbeat <seconds>` | Heartbeat interval                   | 120       |
| `-metrics <port>`      | Prometheus metrics port              | "8080"    |
| `-logdir <path>`       | Log directory                        | "./logs"  |
| `-console`             | Log to console                       | `false` |
| `-ygg`                 | Enable Yggdrasil messaging           | `true`  |
| `-explorer <port>`     | Run blockchain explorer (0=disabled) | 0         |
| `-api <port>`          | Run ImmuDB API (0=disabled)          | 0         |

## Node Operation

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

## Architecture Deep Dive

### FastSync Protocol

The FastSync protocol efficiently synchronizes blockchain data between nodes using:

1. **Initial State Exchange**: Nodes exchange current blockchain height and Merkle roots
2. **Bloom Filter Optimization**: Identifying which blocks are missing using probabilistic data structures
3. **Batch Processing**: Transferring missing blocks in optimized batches
4. **Verification**: Final Merkle root comparison to ensure consistency

### CRDT Implementation

The system uses Conflict-free Replicated Data Types (CRDTs) to ensure data consistency across nodes:

- **Last-Writer-Wins Sets**: For handling user data with timestamps
- **Counters**: For metrics and statistics that require eventual consistency
- **Vector Clocks**: For causality tracking between distributed events

### P2P Network Features

- **Automatic Node Discovery**: Find and connect to other nodes in the network
- **NAT Traversal**: Connect to peers behind NATs and firewalls
- **Transport Layer Security**: Secure communication between peers
- **Multi-Protocol Support**: Both libp2p and Yggdrasil messaging options

## Web Explorer & API

When enabled, the system provides:

- **Block Explorer**: Web interface for viewing blockchain data
- **RESTful API**: HTTP endpoints for programmatic access to the database
- **Metrics Dashboard**: Performance and health monitoring
