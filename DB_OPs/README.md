# DB_OPs Module

## Overview

The DB_OPs module provides database operations for the JMZK decentralized network. It manages connections to ImmuDB (immutable database) for both main blockchain data and accounts/DID data, with connection pooling, transaction support, and comprehensive CRUD operations.

## Purpose

The DB_OPs module handles:
- Database connection pool management
- Block storage and retrieval
- Transaction operations
- Account/DID management
- Receipt storage and querying
- HashMap validation
- Block logs and metadata

## Key Components

### 1. Connection Pool Management
**Files:** `MainDB_Connections.go`, `Account_Connections.go`

Manages connection pools for:
- **Main Database**: Stores blocks, transactions, receipts
- **Accounts Database**: Stores accounts, DIDs, balances

**Key Functions:**
- `GetMainDBConnection()`: Get connection from main DB pool
- `PutMainDBConnection()`: Return connection to main DB pool
- `GetAccountsConnection()`: Get connection from accounts DB pool
- `PutAccountsConnection()`: Return connection to accounts DB pool

### 2. ImmuDB Client
**File:** `immuclient.go`

Core ImmuDB client operations:
- `Create()`: Store key-value pairs
- `Read()`: Retrieve values by key
- `Update()`: Update existing values
- `Delete()`: Delete keys
- `GetKeys()`: Retrieve keys with prefix matching
- `Transaction()`: Execute multiple operations atomically

### 3. Account Operations
**File:** `account_immuclient.go`

Account and DID-specific operations:
- `CreateAccount()`: Create new account/DID
- `GetAccount()`: Retrieve account by address
- `UpdateAccount()`: Update account balance/metadata
- `GetAccounts()`: List all accounts with pagination
- `GetAccountTransactions()`: Get transactions for an account
- `GetAccountBalance()`: Get account balance

### 4. Block Operations
**File:** `BlockLogs.go`

Block-related operations:
- `StoreZKBlock()`: Store ZK-verified block
- `GetZKBlockByNumber()`: Get block by block number
- `GetZKBlockByHash()`: Get block by block hash
- `GetLatestBlock()`: Get latest block
- `GetBlockCount()`: Get total block count

### 5. Transaction Operations
**File:** `immuclient.go`

Transaction-related operations:
- `StoreTransaction()`: Store transaction
- `GetTransactionByHash()`: Get transaction by hash
- `GetTransactionsByAccount()`: Get transactions for account
- `GetTransactionsByBlock()`: Get transactions in block
- `GetTransactionsByAddressIndexed()`: Fast address scans via `address:<addr>:<blockNumber>:<txHash>` keys

### 6. Receipt Operations
**File:** `Facade_Receipts.go`

Transaction receipt operations:
- `StoreReceipt()`: Store transaction receipt
- `GetReceiptByHash()`: Get receipt by transaction hash
- `GetReceiptsByBlock()`: Get receipts for block

### 7. HashMap Validation
**File:** `HashMapValidator.go`

HashMap validation for synchronization:
- `ValidateHashMap()`: Validate HashMap structure
- `CompareHashMaps()`: Compare two HashMaps
- `GetHashMapDiff()`: Get difference between HashMaps

### 8. SQL Operations
**File:** `sqlops/sqlops.go`

SQL-like operations for SQLite:
- `AddPeer()`: Add peer to database
- `GetPeer()`: Get peer information
- `UpdatePeer()`: Update peer information
- `GetAllPeers()`: List all peers

## Key Data Structures

### Account
```go
type Account struct {
    DIDAddress string
    Address    common.Address
    PublicKey  string
    Balance    string
    Nonce      uint64
    CreatedAt  int64
    UpdatedAt  int64
}
```

### ZKBlock
```go
type ZKBlock struct {
    BlockNumber  uint64
    BlockHash    common.Hash
    Transactions []Transaction
    Status       string
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

## Usage

### Connection Pool Management

```go
import "gossipnode/DB_OPs"

// Get main database connection
mainClient, err := DB_OPs.GetMainDBConnection()
if err != nil {
    log.Fatal(err)
}
defer DB_OPs.PutMainDBConnection(mainClient)

// Get accounts database connection
accountsClient, err := DB_OPs.GetAccountsConnection()
if err != nil {
    log.Fatal(err)
}
defer DB_OPs.PutAccountsConnection(accountsClient)
```

### Block Operations

```go
// Store block
err := DB_OPs.StoreZKBlock(mainClient, &block)

// Get block by number
block, err := DB_OPs.GetZKBlockByNumber(mainClient, blockNumber)

// Get block by hash
block, err := DB_OPs.GetZKBlockByHash(mainClient, blockHash)

// Get latest block
block, err := DB_OPs.GetLatestBlock(mainClient)
```

### Account Operations

```go
// Create account
account := &DB_OPs.Account{
    DIDAddress: "did:jmzk:0x1234...",
    Address:    common.HexToAddress("0x1234..."),
    PublicKey:  "0x5678...",
    Balance:    "1000000000000000000",
}
err := DB_OPs.CreateAccount(accountsClient, account)

// Get account
account, err := DB_OPs.GetAccount(accountsClient, address)

// Update account balance
err := DB_OPs.UpdateAccount(accountsClient, address, newBalance)

// Get account transactions
txns, err := DB_OPs.GetAccountTransactions(accountsClient, address)
```

### Transaction Operations

```go
// Store transaction
err := DB_OPs.StoreTransaction(mainClient, &tx)

// Get transaction by hash
tx, err := DB_OPs.GetTransactionByHash(mainClient, txHash)

// Get transactions by account (legacy scan)
txns, err := DB_OPs.GetTransactionsByAccount(mainClient, &address)

// Get the latest 50 transactions involving an address using the index
indexedTxs, err := DB_OPs.GetTransactionsByAddressIndexed(mainClient, address, 50)
```

### Basic CRUD Operations

```go
// Create key-value pair
err := mainClient.Create("key", "value")

// Read value
value, err := mainClient.Read("key")

// Update value
err := mainClient.Update("key", "new_value")

// Delete key
err := mainClient.Delete("key")

// Get keys with prefix
keys, err := mainClient.GetKeys("prefix")
```

### Transaction Support

```go
// Execute multiple operations atomically
ops := []*schema.Op{
    {Key: []byte("key1"), Value: []byte("value1")},
    {Key: []byte("key2"), Value: []byte("value2")},
}
err := mainClient.Transaction(ops)
```

## Integration Points

### Config Module
- Uses connection pool configuration
- Accesses database constants

### Block Module
- Stores and retrieves blocks
- Manages block metadata

### DID Module
- Stores and retrieves DID documents
- Manages account information

### Explorer Module
- Provides data for blockchain explorer
- Queries blocks, transactions, accounts

### FastSync Module
- Provides data for synchronization
- Validates HashMap structures

## Configuration

Database configuration in `config/ImmudbConstants.go`:
- `DBAddress`: Database address (default: "localhost")
- `DBPort`: Database port (default: 3322)
- `DBUsername`: Database username (default: "immudb")
- `DBPassword`: Database password (default: "immudb")
- `DBName`: Main database name (default: "defaultdb")
- `AccountsDBName`: Accounts database name (default: "accountsdb")

## Error Handling

The module includes comprehensive error handling:
- Connection pool errors
- Database connection errors
- Transaction errors
- Validation errors
- Retry logic with exponential backoff

## Logging

Database operations are logged to:
- `logs/ImmuDB.log`: Database operations
- Loki (if enabled): Centralized logging

## Security

- Connection pooling prevents connection exhaustion
- Token-based authentication for ImmuDB
- Automatic token refresh
- Input validation
- SQL injection prevention (via ORM)

## Performance

- **Connection Pooling**: Efficient connection reuse
- **Batch Operations**: Optimized batch writes
- **Indexing**: Efficient key lookups
- **Caching**: Connection and query caching

## Testing

Test files:
- `Tests/`: Database operation tests
- Integration tests with ImmuDB
- Connection pool tests

## Best Practices

1. **Always use connection pools**: Don't create direct connections
2. **Return connections**: Always return connections to pool
3. **Use transactions**: For multiple related operations
4. **Handle errors**: Check all error returns
5. **Validate input**: Validate data before storing

## Future Enhancements

- Enhanced query capabilities
- Advanced indexing
- Data archival and pruning
- Performance optimizations
- Backup and recovery tools

