# Config Module

## Overview

The config module provides system-wide configuration, constants, and utility functions for the JMZK decentralized network. It manages database connections, protocol definitions, connection pooling, and shared data structures.

## Purpose

The config module centralizes:
- System-wide constants and configuration values
- Database connection pool management
- Protocol ID definitions
- Network configuration
- Data structures for blocks, transactions, and accounts
- Utility functions for key management

## Key Components

### 1. Constants
**File:** `constants.go`

Defines system-wide constants:
- **Protocol IDs**: Message, file, broadcast, block propagation, DID propagation protocols
- **Peer Configuration**: Max main peers (13), max backup peers (10)
- **Timeouts**: Consensus timeout (20 seconds), message handling windows
- **Seed Node Configuration**: Max tracked peers, TTL, heartbeat intervals
- **Color Codes**: ANSI color codes for terminal output

### 2. Connection Pool
**File:** `ConnectionPool.go`

Manages database connection pooling for ImmuDB:
- Connection pool configuration
- Pooled connection management
- Token refresh and management
- Health monitoring
- Metrics integration

**Key Features:**
- Minimum and maximum connection limits
- Connection timeout handling
- Idle connection management
- Token lifetime management
- Automatic token refresh

### 3. ImmuDB Constants
**File:** `ImmudbConstants.go`

ImmuDB-specific configuration:
- Database connection settings (address, port, username, password)
- Database names (defaultdb, accountsdb)
- Operation settings (scan limits, timeouts)
- ImmuClient interface
- Block hasher implementation

### 4. ZKBlock
**File:** `ZKBlock.go`

Defines zero-knowledge block data structures:
- ZKBlock structure with ZK proof data
- Transaction structures
- Block header structures
- Receipt structures

### 5. Utilities
**File:** `utils/`

Utility functions for:
- Private key loading from `peer.json`
- Public key derivation
- Key caching and management

### 6. PubSub Messages
**File:** `PubSubMessages/`

Data structures and utilities for PubSub messaging:
- Message builders
- Global variable management
- PubSub node structures
- Consensus message structures

## Key Data Structures

### Node
```go
type Node struct {
    Host       host.Host
    EnableQUIC bool
    IsSeed     bool
    PeerStore  *PeerRegistry
    DB         *UnifiedDB
}
```

### ConnectionPool
```go
type ConnectionPool struct {
    Config     *ConnectionPoolConfig
    Connections chan *PooledConnection
    Mutex      sync.RWMutex
    // ... pool management fields
}
```

### ZKBlock
```go
type ZKBlock struct {
    BlockNumber   uint64
    BlockHash     common.Hash
    Transactions  []Transaction
    Status        string
    // ... ZK proof fields
}
```

### Transaction
```go
type Transaction struct {
    Hash    common.Hash
    From    common.Address
    To      common.Address
    Value   *big.Int
    Nonce   uint64
    // ... transaction fields
}
```

## Configuration Constants

### Protocol IDs
```go
MessageProtocol           = "/custom/message/1.0.0"
FileProtocol             = "/custom/file/1.0.0"
BroadcastProtocol        = "/custom/broadcast/1.0.0"
BlockPropagationProtocol = "/custom/block/1.0.0"
DIDPropagationProtocol    = "/custom/did/1.0.0"
BuddyNodesMessageProtocol = "/custom/buddy-nodes/1.0.0"
```

### Peer Configuration
```go
MaxMainPeers     = 13  // Production size for buddy node committees
MaxBackupPeers   = 10  // Backup peers to handle failures
ConsensusTimeout = 20 * time.Second
```

### Database Configuration
```go
DBAddress  = "localhost"
DBPort     = 3322
DBUsername = "immudb"
DBPassword = "immudb"
DBName     = "defaultdb"
AccountsDBName = "accountsdb"
```

## Usage

### Accessing Constants

```go
import "gossipnode/config"

// Protocol IDs
protocol := config.MessageProtocol

// Peer limits
maxPeers := config.MaxMainPeers

// Database settings
dbName := config.DBName
```

### Connection Pool

```go
import "gossipnode/config"

// Get default pool configuration
poolConfig := config.DefaultConnectionPoolConfig()

// Initialize global pool
config.InitGlobalPoolWithLoki(poolConfig, enableLoki)

// Get connection from pool
conn, err := config.GetGlobalPool().Get()

// Return connection to pool
config.GetGlobalPool().Put(conn)
```

### Loading Private Key

```go
import "gossipnode/config/utils"

// Load private key from peer.json
privKey, err := utils.ReturnPrivateKey()

// Get public key
pubKey, err := utils.ReturnPublicKey()
```

## Integration Points

### Database Operations (DB_OPs)
- Uses connection pool for database access
- Accesses ImmuDB constants for configuration

### Node Module
- Uses Node structure for node management
- Accesses protocol IDs for stream handlers

### Messaging Module
- Uses protocol IDs for message routing
- Accesses message structures

### Block Module
- Uses ZKBlock structure for block operations
- Accesses transaction structures

## Configuration Files

### peer.json
Location: `./config/peer.json`

Stores peer identity:
```json
{
  "peer_id": "Qm...",
  "priv_key": "base64_encoded_private_key"
}
```

## Environment Variables

The module supports environment variables for configuration:
- `LOKI_URL`: Loki logging URL (optional)
- Database credentials (can be overridden)

## Security Considerations

- Private keys stored in `peer.json` (should be secured)
- Database credentials should be protected
- Connection pooling prevents connection exhaustion
- Token refresh ensures secure database access

## Performance

- Connection pooling improves database performance
- Key caching reduces key loading overhead
- Efficient data structures for block operations
- Optimized protocol ID lookups

## Testing

Configuration can be tested by:
- Verifying constant values
- Testing connection pool operations
- Validating key loading
- Testing data structure serialization

## Future Enhancements

- Configuration file support (YAML/JSON)
- Environment variable expansion
- Dynamic configuration updates
- Configuration validation
- Configuration documentation generation

