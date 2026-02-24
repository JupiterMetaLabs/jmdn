# JMDT Decentralized Network - Architecture Documentation

## Overview

The JMDT Decentralized Network is a sophisticated peer-to-peer blockchain system built in Go that combines multiple advanced distributed systems concepts. It provides a complete decentralized infrastructure with blockchain synchronization, distributed consensus, resilient messaging, and decentralized identity management.

## Core Architecture

The system is built on a modular architecture with the following key principles:
- **Libp2p Network Layer**: Foundation for peer discovery, connection management, and direct messaging
- **ImmuDB Integration**: Tamper-proof database providing blockchain-like features including Merkle trees
- **FastSync Protocol**: Efficient blockchain state synchronization between nodes
- **CRDT Engine**: Conflict-free replicated data types ensuring eventual consistency
- **Gossip Protocol**: For reliable information dissemination across the network
- **Bloom Filter Optimization**: Memory-efficient set membership testing to prevent redundant data transfers

## Component Breakdown

### 1. Main Entry Point (`main.go`)

The central orchestrator that initializes and coordinates all system components:

**Key Functions:**
- **Database Pool Management**: Initializes connection pools for main and accounts databases
- **Node Creation**: Creates libp2p host for peer-to-peer networking
- **Service Initialization**: Starts various gRPC servers (DID, CLI, gETH, Block generator)
- **FastSync Setup**: Initializes blockchain synchronization service
- **Yggdrasil Integration**: Optional alternative messaging protocol for improved privacy
- **Metrics & Monitoring**: Starts Prometheus-compatible metrics server

**Command Line Flags:**
- `-seed`: Run as a seed node for network bootstrapping
- `-connect`: Connect to a seed node (multiaddr)
- `-seednode`: Seed node gRPC URL for peer registration (e.g., 34.174.233.203:17002)
- `-heartbeat`: Heartbeat interval in seconds (default: 120)
- `-metrics`: Port for Prometheus metrics (default: 8080)
- `-ygg`: Enable Yggdrasil direct messaging (default: true)
- `-api`: Run ImmuDB API on specified port (0 = disabled)
- `-blockgen`: Run Block creator API on specified port (0 = disabled)
- `-cli`: CLI gRPC server port (default: 15053)
- `-did`: DID gRPC server address (default: localhost:15052)
- `-geth`: gETH gRPC server port (default: 15054)

### 2. Block Component (`Block/`)

Handles blockchain operations, transaction processing, and block generation:

**Key Files:**
- `Server.go`: HTTP API server for block operations and transaction submission
- `Blockhelper.go`: Logging utilities for transaction processing
- `gRPCclient.go`: gRPC client for mempool communication
- `SmartcontractServer.go`: Smart contract execution server
- `utils.go`: Utility functions for block operations

**Core Functions:**
- **Transaction Submission**: `SubmitRawTransaction()` - Validates and submits transactions to mempool
- **Security Checks**: Three-layer security validation for transactions
- **Block Processing**: `processZKBlock()` - Handles ZK-verified blocks
- **Block Retrieval**: Get blocks by number or hash
- **Mempool Integration**: Communicates with external mempool service

**API Endpoints:**
- `POST /submit-raw-transaction`: Submit raw transactions
- `POST /process-zk-block`: Process ZK-verified blocks
- `GET /block/:number`: Get block by number
- `GET /block/:hash`: Get block by hash

### 3. gETH Component (`gETH/`)

Ethereum-compatible interface providing gRPC services for blockchain interaction:

**Key Files:**
- `Server.go`: gRPC server implementation
- `gETH_config.go`: Data structures for Ethereum compatibility
- `gETH_Middleware.go`: Core business logic for blockchain operations
- `utils.go`: Utility functions for data conversion
- `proto/gETH.proto`: Protocol buffer definitions

**Core Services:**
- **Chain Service**: Block and transaction operations
- **Account Operations**: Get account state and balances
- **Contract Interaction**: Call contracts and estimate gas
- **Transaction Submission**: Send raw transactions
- **Event Streaming**: Stream block headers and logs

**Key Functions:**
- `_GetBlockByNumber()`: Retrieve blocks by block number
- `_GetBlockByHash()`: Retrieve blocks by block hash
- `_GetTransactionByHash()`: Get transaction details
- `ConvertZKBlockToETHBlock()`: Convert internal block format to Ethereum format

### 4. CLI Component (`CLI/`)

Command-line interface and gRPC server for node management:

**Key Files:**
- `CLI.go`: Main CLI implementation with command handlers
- `CLI_GRPC.go`: gRPC service implementations
- `GRPC_Server.go`: gRPC server setup
- `proto/Connection.proto`: Protocol buffer definitions

**Available Commands:**
- `msg <peer_multiaddr> <message>`: Send message via libp2p
- `ygg <peer_multiaddr|ygg_ipv6> <message>`: Send message via Yggdrasil
- `file <peer_multiaddr> <filepath>`: Send file to peer
- `addpeer <peer_multiaddr>`: Add peer to managed nodes
- `removepeer <peer_id>`: Remove peer from managed nodes
- `listpeers`: Show all managed peers
- `peers`: Request updated peer list from seed
- `stats`: Show messaging statistics
- `broadcast <message>`: Broadcast to all connected peers
- `fastsync <peer_multiaddr>`: Fast sync blockchain data
- `dbstate`: Show current ImmuDB database state
- `propagateDID <did> <public_key>`: Propagate DID to network
- `getDID <did>`: Get DID document from network
- `syncinfo`: Show FastSync configuration

### 5. FastSync Component (`fastsync/`)

High-performance blockchain synchronization system with CRDT support:

**Key Files:**
- `fastsync.go`: Main FastSync implementation
- `fastsyncNew.go`: Enhanced version with HashMap-based synchronization
- `FileTransfer.go`: File transfer utilities for large data sync

**Synchronization Process:**
1. **Phase 1**: Exchange database states and Merkle roots
2. **Phase 2**: Compute and exchange HashMaps to identify missing data
3. **Phase 3**: Transfer missing data in optimized batches
4. **Phase 4**: Verification and consistency checks

**Key Features:**
- **HashMap-based Diff**: Efficient identification of missing data
- **Batch Processing**: Transfer data in configurable batch sizes
- **CRDT Integration**: Conflict-free synchronization of concurrent operations
- **Retry Logic**: Automatic retry with exponential backoff
- **Progress Tracking**: Real-time synchronization progress monitoring

### 6. CRDT Component (`crdt/`)

Conflict-free replicated data types for distributed consistency:

**Key Files:**
- `crdt.go`: Core CRDT implementations (LWW-Set, Counter)
- `CRDT_DBOps.go`: Database operations for CRDTs
- `MemoryStore.go`: In-memory storage for CRDT operations
- `HashMap/HashMap.go`: HashMap implementation for data reconciliation
- `IBLT/IBLT.go`: Invertible Bloom Lookup Table for set reconciliation

**CRDT Types:**
- **LWW-Set (Last-Writer-Wins Set)**: For handling concurrent add/remove operations
- **Counter**: For metrics and statistics requiring eventual consistency
- **Vector Clocks**: For causality tracking between distributed events

**Key Features:**
- **Deterministic Merging**: Consistent results across all nodes
- **Operation-based Sync**: Sync operations rather than state
- **Memory Management**: Configurable memory limits with heap management
- **Export/Import**: Network synchronization of CRDT state

### 7. Messaging Component (`messaging/`)

Peer-to-peer communication system with multiple protocols:

**Key Files:**
- `message.go`: Core messaging functions
- `blockPropagation.go`: Block propagation with bloom filters
- `DIDPropagation.go`: DID document propagation
- `broadcast.go`: Broadcast messaging utilities
- `directMSG/directMSG.go`: Yggdrasil direct messaging

**Protocols:**
- **Libp2p**: Primary P2P protocol for node communication
- **Yggdrasil**: Alternative protocol for improved privacy and connectivity
- **Block Propagation**: Specialized protocol for blockchain data
- **DID Propagation**: Protocol for decentralized identity documents

**Key Features:**
- **Bloom Filter Deduplication**: Prevent processing duplicate messages
- **Concurrent Broadcasting**: Send to multiple peers simultaneously
- **Timeout Management**: Handle peer timeouts gracefully
- **Metrics Integration**: Track messaging statistics

### 8. DID Component (`DID/`)

Decentralized Identity management system:

**Key Files:**
- `DID.go`: Main DID server implementation
- `proto/DID.proto`: Protocol buffer definitions

**Core Services:**
- **DID Registration**: Register new decentralized identities
- **DID Retrieval**: Get DID documents and information
- **Account Management**: Manage account balances and metadata
- **Network Propagation**: Propagate DID updates across the network

**Key Functions:**
- `RegisterDID()`: Register new DID with public key
- `GetDID()`: Retrieve DID information by identifier
- `ListDIDs()`: List all DIDs with pagination
- `GetDIDStats()`: Get statistics about the DID system

### 9. Database Operations (`DB_OPs/`)

ImmuDB integration with connection pooling and transaction management:

**Key Files:**
- `immuclient.go`: Core ImmuDB client operations
- `MainDB_Connections.go`: Main database connection pool
- `account_immuclient.go`: Account-specific database operations
- `ops.go`: General database operations
- `sqlops/sqlops.go`: SQL-like operations

**Key Features:**
- **Connection Pooling**: Efficient database connection management
- **Transaction Support**: Atomic multi-operation transactions
- **Retry Logic**: Automatic retry with exponential backoff
- **Health Monitoring**: Connection health checks and metrics
- **Dual Database Support**: Separate databases for main blockchain and accounts

**Core Operations:**
- `Create()`: Store key-value pairs
- `Read()`: Retrieve values by key
- `Update()`: Update existing values
- `GetKeys()`: Retrieve keys with prefix matching
- `Transaction()`: Execute multiple operations atomically

### 10. Explorer Component (`explorer/`)

Web-based blockchain explorer and REST API:

**Key Files:**
- `api.go`: Main API server setup
- `BlockOps.go`: Block-related API endpoints
- `DIDOps.go`: DID-related API endpoints
- `StreamTxns.go`: Real-time transaction streaming
- `health.go`: Health check endpoints
- `utils.go`: Utility functions and block polling

**API Endpoints:**
- **Blocks**: `/api/block/id/:id`, `/api/block/number/:number`, `/api/block/all`
- **Transactions**: `/api/block/transactions/:hash`, `/api/block/transactions/all`
- **DIDs**: `/api/did/all/`, `/api/did/details`
- **Stats**: `/api/stats/`
- **WebSocket**: `/api/sockets/blocks` for real-time updates

**Key Features:**
- **Real-time Updates**: WebSocket streaming for new blocks
- **Pagination**: Efficient handling of large datasets
- **Health Monitoring**: System health and status endpoints
- **CORS Support**: Cross-origin resource sharing for web clients

### 11. Node Management (`node/`)

Libp2p node creation and peer management:

**Key Files:**
- `node.go`: Node creation and configuration
- `nodemanager.go`: Peer management and heartbeat monitoring
- `discovery.go`: Peer discovery mechanisms

**Key Features:**
- **Peer Discovery**: Automatic discovery of network peers
- **Connection Management**: Handle peer connections and disconnections
- **Heartbeat Monitoring**: Track peer health and availability
- **Address Management**: Handle multiple network addresses

### 12. Seed Node Integration (`seednode/`)

External seed node registration and peer discovery system:

**Key Files:**
- `seednode.go`: gRPC client for seed node communication
- `proto/seednode.proto`: Protocol buffer definitions for seed node service
- `proto/seednode.pb.go`: Generated Go code for protocol buffers
- `proto/seednode_grpc.pb.go`: Generated gRPC service code

**Core Services:**
- **Peer Registration**: Register this node with external seed node
- **Public IP Detection**: Automatically detect public IP using ifconfig.me
- **Multiaddress Construction**: Build proper libp2p multiaddresses
- **VRS Signing**: Cryptographic signing of peer records and heartbeats

**Key Functions:**
- `RegisterPeer()`: Register this peer with the seed node
- `SendHeartbeat()`: Send periodic heartbeat to seed node
- `getPublicIP()`: Fetch public IP address from ifconfig.me
- `signPeerRecord()`: Sign peer records with VRS signature
- `signHeartbeat()`: Sign heartbeat messages with VRS signature

**Protocol Features:**
- **Public IP Detection**: Uses ifconfig.me service for external IP
- **Port Detection**: Automatically detects TCP port from libp2p host
- **Address Filtering**: Filters out local/private addresses for external registration
- **Fallback Strategy**: Falls back to local addresses if public IP unavailable
- **VRS Signing**: Complete ECDSA signature with R, S, V components
- **Deterministic V Calculation**: Consistent V component calculation

**Integration Points:**
- **Startup Registration**: Automatically registers during node startup
- **Command Line**: Configurable via `-seednode` flag
- **Error Handling**: Graceful handling of connection failures
- **Logging**: Comprehensive logging of registration status

### 13. Configuration (`config/`)

System configuration and constants:

**Key Files:**
- `constants.go`: System-wide constants and protocol IDs
- `ConnectionPool.go`: Database connection pool configuration
- `ImmudbConstants.go`: ImmuDB-specific configuration
- `ZKBlock.go`: Zero-knowledge block data structures
- `UpdateMetrics.go`: Metrics update utilities

### 14. Security (`Security/`)

Security validation and checks:

**Key Files:**
- `Security.go`: Three-layer security validation system

**Security Layers:**
1. **Input Validation**: Validate transaction format and parameters
2. **Signature Verification**: Verify cryptographic signatures
3. **Business Logic**: Apply business rules and constraints

### 15. Monitoring & Logging

**Metrics (`metrics/`):**
- Prometheus-compatible metrics for system monitoring
- Connection pool metrics
- Message statistics
- Performance indicators

**Logging (`logging/`):**
- Structured logging with zerolog
- Loki integration for log aggregation
- Configurable log levels and outputs

## Data Flow

### 1. Node Startup and Seed Node Registration Flow
1. **Node Initialization**: Create libp2p host and initialize services
2. **Public IP Detection**: Query ifconfig.me for external IP address
3. **Port Detection**: Extract TCP port from libp2p host addresses
4. **Multiaddress Construction**: Build proper libp2p multiaddresses with public IP
5. **VRS Signing**: Generate cryptographic signature for peer record
6. **Seed Node Registration**: Register with external seed node via gRPC
7. **Confirmation**: Receive registration confirmation from seed node

### 2. Transaction Processing Flow
1. **Transaction Submission**: Via Block API or gETH interface
2. **Security Validation**: Three-layer security checks
3. **Mempool Submission**: Send to external mempool service
4. **Block Creation**: ZK-verified blocks created externally
5. **Block Processing**: Process and store verified blocks
6. **Network Propagation**: Broadcast blocks to all peers

### 3. Synchronization Flow
1. **State Exchange**: Nodes exchange current database states
2. **Diff Calculation**: Use HashMaps to identify missing data
3. **Batch Transfer**: Transfer missing data in optimized batches
4. **Verification**: Verify synchronization consistency
5. **CRDT Merge**: Merge any concurrent operations

### 4. DID Management Flow
1. **DID Registration**: Register new identity with public key
2. **Database Storage**: Store in accounts database
3. **Network Propagation**: Broadcast to all connected peers
4. **Retrieval**: Query DID information from network

## Dependencies

**Core Dependencies:**
- **Libp2p**: Peer-to-peer networking
- **ImmuDB**: Immutable database with Merkle trees
- **Ethereum Go**: Ethereum compatibility layer
- **gRPC**: High-performance RPC framework
- **Gin**: HTTP web framework
- **Zerolog**: Structured logging
- **Prometheus**: Metrics collection
- **Protocol Buffers**: Data serialization for gRPC services

**Key External Services:**
- **Mempool Service**: External transaction mempool (port 15051)
- **ZKVM**: Zero-knowledge virtual machine for block verification
- **Yggdrasil**: Alternative P2P networking protocol
- **Seed Node Service**: External peer discovery and registration service
- **ifconfig.me**: Public IP address detection service

## Deployment Architecture

The system supports multiple deployment modes:

1. **Single Node**: Standalone node for development/testing
2. **Seed Node**: Bootstrap node for network discovery
3. **Full Node**: Complete node with all services enabled
4. **API Node**: Node focused on providing API services
5. **Sync Node**: Node optimized for blockchain synchronization

## Monitoring & Observability

**Metrics:**
- Prometheus metrics on port 8080
- Grafana dashboards for visualization
- Loki for log aggregation

**Health Checks:**
- Database connection health
- Peer connectivity status
- Service availability endpoints

**Logging:**
- Structured JSON logs
- Multiple log levels (DEBUG, INFO, WARN, ERROR)
- Centralized log aggregation with Loki

## Security Considerations

1. **Cryptographic Security**: All transactions cryptographically signed
2. **Network Security**: Libp2p provides secure peer-to-peer communication
3. **Database Security**: ImmuDB provides tamper-proof storage
4. **Input Validation**: Comprehensive validation of all inputs
5. **Access Control**: gRPC-based access control for services
6. **VRS Signing**: ECDSA signatures with R, S, V components for peer records
7. **Public Key Verification**: Cryptographic verification of peer identities
8. **External Service Security**: Secure communication with seed nodes and external services

## Performance Optimizations

1. **Connection Pooling**: Efficient database connection management
2. **Batch Processing**: Optimized data transfer in batches
3. **Bloom Filters**: Efficient duplicate message detection
4. **Concurrent Operations**: Parallel processing where possible
5. **Memory Management**: Configurable memory limits and garbage collection

This architecture provides a robust, scalable, and secure foundation for a decentralized blockchain network with advanced features like CRDT synchronization, decentralized identity management, and comprehensive monitoring capabilities.
