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

### System Requirements

- **Operating System**: Ubuntu 18.04+, Debian 10+, elementaryOS, or Linux Mint
- **Architecture**: x86_64, ARM64, or ARMv7
- **Memory**: Minimum 2GB RAM
- **Storage**: At least 10GB free space
- **Network**: Internet connection for initial setup

### Dependencies

- **Go 1.18+**: Programming language runtime
- **Docker & Docker Compose**: Containerization platform
- **Yggdrasil**: Decentralized mesh networking protocol
- **ImmuDB**: Tamper-proof database (installed automatically)
- **SQLite**: Database engine (via mattn/go-sqlite3)

### Quick Setup

The easiest way to set up the environment is using the provided setup script:

```bash
# Make the setup script executable and run it
chmod +x Setup.sh
./Setup.sh
```

This script will automatically install all prerequisites in the correct order:
1. **Go Programming Language** - Latest version with proper PATH configuration
2. **ImmuDB** - Tamper-proof database with OS detection
3. **Yggdrasil Network** - Official Debian package installation
4. **Docker** (optional) - Uncomment in Setup.sh if needed

#### Setup.sh Script Details

The `Setup.sh` script provides a streamlined installation process:

```bash
#!/bin/bash
# Setup script for JMZK Decentralized Network
# Installs all prerequisites in the correct order

echo "=== JMZK Decentralized Network Setup ==="
echo "Installing prerequisites..."

# Make prerequisite scripts executable
chmod +x ./Scripts/Go_Prerequisite.sh
chmod +x ./Scripts/ImmuDB_Prerequisite.sh
chmod +x ./Scripts/YGG_Prerequisite.sh
# chmod +x ./Scripts/Docker_Prerequisite.sh

# Install prerequisites in order
echo "1. Installing Go..."
./Scripts/Go_Prerequisite.sh

echo "2. Installing ImmuDB..."
./Scripts/ImmuDB_Prerequisite.sh

echo "3. Installing Yggdrasil..."
./Scripts/YGG_Prerequisite.sh

# Uncomment the line below if you need Docker
# echo "4. Installing Docker..."
# ./Scripts/Docker_Prerequisite.sh

echo "=== Setup Complete ==="
echo "All prerequisites have been installed successfully!"
echo "You can now build the application with: go build -o gossipnode"
```

**Features:**
- Automatically makes prerequisite scripts executable
- Installs Go with cross-platform support (Ubuntu/macOS)
- Installs ImmuDB with OS and architecture detection
- Installs Yggdrasil using official Debian packages
- Docker installation is commented out by default (uncomment if needed)
- Provides colored output and error handling
- Clear progress indicators and completion messages

### Manual Installation

If you prefer to install dependencies manually or need to install them individually:

#### 1. Install Go
```bash
chmod +x ./Scripts/Go_Prerequisite.sh
./Scripts/Go_Prerequisite.sh
```

#### 2. Install ImmuDB
```bash
chmod +x ./Scripts/ImmuDB_Prerequisite.sh
./Scripts/ImmuDB_Prerequisite.sh
```

#### 3. Install Yggdrasil
```bash
chmod +x ./Scripts/YGG_Prerequisite.sh
./Scripts/YGG_Prerequisite.sh
```

#### 4. Install Docker (Optional)
```bash
chmod +x ./Scripts/Docker_Prerequisite.sh
./Scripts/Docker_Prerequisite.sh
```

### Build the Application

After installing prerequisites:

1. Install Go dependencies:
   ```bash
   go mod download
   ```

2. Build the application:
   ```bash
   go build -o gossipnode
   ```

## Running a Node

Basic node startup:

```bash
./gossipnode
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
