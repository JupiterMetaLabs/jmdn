# Block Module

## Overview

The Block module handles blockchain operations, transaction processing, block generation, and block validation. It provides both HTTP API and gRPC interfaces for block operations and integrates with the mempool service for transaction management.

## Purpose

The Block module is responsible for:
- Processing ZK-verified blocks from the ZKVM
- Submitting raw transactions to the mempool
- Storing validated blocks in the database
- Providing block retrieval APIs
- Managing consensus for block validation
- Executing smart contracts

## Key Components

### 1. HTTP API Server
**File:** `Server.go`

Provides RESTful HTTP API endpoints for block operations:
- `POST /submit-raw-transaction`: Submit raw transactions
- `POST /process-zk-block`: Process ZK-verified blocks
- `POST /process-zk-block-no-consensus`: Process blocks without consensus (testing)
- `GET /block/:number`: Get block by number
- `GET /block/:hash`: Get block by hash

### 2. gRPC Server
**File:** `grpc_server.go`

Provides gRPC service for block processing:
- `ProcessBlock`: Process blocks via gRPC
- Block validation and consensus integration
- Transaction logging

### 3. gRPC Client
**File:** `gRPCclient.go`

Client for communicating with the mempool service:
- Submit transactions to mempool
- Query mempool status
- Transaction validation

### 4. Smart Contract Server
**File:** `SmartcontractServer.go`

Handles smart contract execution and interaction.

### 5. Utilities
**Files:** `Blockhelper.go`, `utils.go`

Helper functions for:
- Transaction logging
- Block conversion
- Block validation
- Merkle tree operations

## Key Functions

### Block Processing

```go
// Process ZK-verified block
func processZKBlock(c *gin.Context) {
    // Validates block
    // Starts consensus process
    // Stores block in database
    // Logs transactions
}
```

### Transaction Submission

```go
// Submit raw transaction to mempool
func SubmitRawTransaction(tx *config.Transaction) (string, error) {
    // Validates transaction
    // Submits to mempool via gRPC
    // Returns transaction hash
}
```

### Block Retrieval

```go
// Get block by number
func GetBlockByNumber(number uint64) (*config.ZKBlock, error)

// Get block by hash
func GetBlockByHash(hash common.Hash) (*config.ZKBlock, error)
```

## Block Processing Flow

1. **Block Reception**: Block received via HTTP API or gRPC
2. **Validation**: Validates block structure and transactions
3. **Consensus**: Initiates consensus process via Sequencer
4. **Storage**: Stores validated block in main database
5. **Propagation**: Propagates block to network peers
6. **Logging**: Logs all transactions in the block

## Integration Points

### Mempool Service
- Submits transactions via gRPC
- Queries transaction status
- Validates transaction format

### Sequencer Module
- Initiates consensus for block validation
- Manages buddy node selection
- Collects consensus results

### Database (DB_OPs)
- Stores validated blocks
- Retrieves block data
- Manages block metadata

### Messaging Module
- Propagates blocks to network peers
- Broadcasts block updates
- Handles block synchronization

## Configuration

Key configuration in `config/`:
- `ZKBlock`: Block data structure
- `Transaction`: Transaction data structure
- Block processing timeouts
- Consensus parameters

## API Endpoints

### HTTP Endpoints

**Submit Transaction:**
```bash
POST /submit-raw-transaction
Content-Type: application/json

{
  "from": "0x...",
  "to": "0x...",
  "value": "1000000000000000000",
  "nonce": 1,
  "gasLimit": 21000,
  "gasPrice": "20000000000",
  "data": "0x"
}
```

**Process Block:**
```bash
POST /process-zk-block
Content-Type: application/json

{
  "blockNumber": 123,
  "blockHash": "0x...",
  "transactions": [...],
  "status": "verified"
}
```

### gRPC Service

**ProcessBlock:**
```protobuf
service BlockService {
    rpc ProcessBlock(ProcessBlockRequest) returns (ProcessBlockResponse);
}
```

## Error Handling

The module includes comprehensive error handling:
- Invalid block format errors
- Transaction validation errors
- Database connection errors
- Consensus failures
- Mempool communication errors

## Logging

Transaction and block operations are logged to:
- `logs/transactions.log`: Transaction logs
- `logs/block-grpc.log`: gRPC block operations
- Loki (if enabled): Centralized logging

## Security

- Transaction signature verification
- Block hash validation
- Consensus validation before storage
- Input sanitization
- Rate limiting (via middleware)

## Performance

- Concurrent block processing
- Batch transaction operations
- Efficient database queries
- Connection pooling for mempool

## Testing

Test files:
- `Block_test.go`: Block processing tests
- Integration tests with mempool
- Consensus integration tests

## Usage Example

```go
import "gossipnode/Block"

// Start HTTP server
Block.Startserver(8080, host, chainID)

// Start gRPC server
Block.StartGRPCServer(9090, host, chainID)

// Submit transaction
hash, err := Block.SubmitRawTransaction(&tx)

// Process block
err := Block.ProcessZKBlock(&block)
```

## Future Enhancements

- Enhanced block validation
- Improved consensus integration
- Advanced smart contract support
- Block pruning and archival
- Performance optimizations

