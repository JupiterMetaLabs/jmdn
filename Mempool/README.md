# Mempool Module

## Overview

The Mempool module provides transaction mempool services for the JMDT network. It handles transaction submission, storage, retrieval, and routing through a dual-database architecture with primary and replica mempools.

## Purpose

The Mempool module enables:
- Transaction submission and storage
- Transaction retrieval by hash
- Pending transaction management
- Transaction routing via Routing Engine (MRE)
- Mempool statistics and monitoring
- Fee statistics and analysis
- Merkle root calculation for transaction verification

## Key Components

### 1. Protocol Buffers
**File:** `proto/mempool.proto`

Defines gRPC services for mempool operations:

**MempoolService:**
- `SubmitTransaction`: Submit single transaction
- `SubmitTransactions`: Submit multiple transactions
- `GetTransactionByHash`: Get transaction by hash
- `PeekPendingTransactions`: Peek at pending transactions (non-destructive)
- `GetPendingTransactions`: Get pending transactions (destructive)
- `GetPrimaryMerkleRoot`: Get primary DB Merkle root
- `GetAllMerkleRoots`: Get all Merkle roots
- `GetMempoolStats`: Get mempool statistics
- `GetFeeStatistics`: Get fee statistics
- `GetFeeSnapshot`: Get fee snapshot

**RoutingService (MRE):**
- `SubmitTransaction`: Submit transaction to routing engine
- `SubmitTransactions`: Submit multiple transactions
- `GetTransaction`: Get transaction by hash
- `GetPendingTransactions`: Get pending transactions from all mempools
- `GetMempoolStats`: Get routing engine stats
- `GetFeeStatistics`: Get fee statistics
- `GetTransactionLookup`: Get transaction lookup information
- `GetAllTransactionLookups`: Get all transaction lookups
- `OnReceiveFromJMDT`: Receive transactions from JMDT

## Key Data Structures

### Transaction
```protobuf
message Transaction {
    string hash = 1;
    string from = 2;
    string to = 3;
    string value = 4;
    uint32 type = 5;
    uint64 timestamp = 6;
    string chain_id = 7;
    uint64 nonce = 8;
    string gas_limit = 9;
    string gas_price = 10;
    string max_fee = 11;
    string max_priority_fee = 12;
    bytes data = 13;
    repeated AccessTuple access_list = 14;
    string v = 15;
    string r = 16;
    string s = 17;
}
```

### MempoolTxn
```protobuf
message MempoolTxn {
    Transaction transaction = 1;
    Metadata metadata = 2;
}
```

### Metadata
```protobuf
message Metadata {
    bool primary_node = 1;
    bool replica_node = 2;
    string created_at = 3;
    int32 total_replicas = 4;
}
```

## Usage

### Submit Transaction

```go
// Via gRPC client
client := mempool.NewClient("localhost:15051")
resp, err := client.SubmitTransaction(&mempool.MempoolTxn{
    Transaction: &mempool.Transaction{...},
    Metadata: &mempool.Metadata{...},
})
```

### Get Transaction

```go
// Get transaction by hash
tx, err := client.GetTransactionByHash(&mempool.GetTransactionByHashRequest{
    Hash: "0x...",
})
```

### Get Pending Transactions

```go
// Get pending transactions
txns, err := client.GetPendingTransactions(&mempool.GetPendingRequest{
    Limit: 100,
})
```

### Get Mempool Stats

```go
// Get mempool statistics
stats, err := client.GetMempoolStats(&emptypb.Empty{})
```

## Integration Points

### Block Module
- Submits transactions to mempool
- Retrieves transactions from mempool
- Queries mempool status

### gETH Module
- Submits transactions via gRPC
- Queries transaction status

### Config Module
- Uses mempool configuration
- Accesses transaction structures

## Configuration

Mempool server address can be configured via command-line flag:

```bash
./jmdn -mempool localhost:15051  # Default address
```

## Error Handling

The module includes comprehensive error handling:
- Transaction validation errors
- Storage errors
- Retrieval errors
- Routing errors

## Logging

Mempool operations are logged to:
- `logs/mempool.log`: Mempool operations
- Loki (if enabled): Centralized logging

## Security

- Transaction validation
- Signature verification
- Input sanitization
- Access control

## Performance

- Dual-database architecture for performance
- Hot cache for fast access
- Efficient transaction routing
- Optimized Merkle root calculation

## Testing

Test files:
- `mempool_test.go`: Mempool operation tests
- Integration tests
- Routing tests

## Future Enhancements

- Enhanced transaction routing
- Improved fee analysis
- Better error recovery
- Performance optimizations
- Additional routing strategies

