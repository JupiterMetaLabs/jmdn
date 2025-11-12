# gETH Module

## Overview

The gETH module provides Ethereum-compatible interfaces for the JMZK network. It offers gRPC services and HTTP/WebSocket facades that implement Ethereum JSON-RPC API, enabling compatibility with existing Ethereum tooling and applications.

## Purpose

The gETH module enables:
- Ethereum-compatible blockchain interaction
- gRPC services for blockchain operations
- HTTP/WebSocket facades for JSON-RPC compatibility
- Account state queries
- Transaction submission
- Block and transaction retrieval
- Smart contract interaction

## Key Components

### 1. gRPC Server
**File:** `Server.go`

Provides gRPC service for blockchain operations:
- `GetBlockByNumber`: Get block by block number
- `GetBlockByHash`: Get block by block hash
- `GetTransactionByHash`: Get transaction by hash
- `GetReceiptByHash`: Get transaction receipt
- `GetAccountState`: Get account state (balance, nonce, etc.)
- `GetLogs`: Get event logs
- `Call`: Execute contract call
- `EstimateGas`: Estimate gas for transaction
- `SendRawTx`: Submit raw transaction

### 2. gRPC Middleware
**File:** `gETH_Middleware.go`

Core business logic for blockchain operations:
- Block conversion from ZKBlock to Ethereum format
- Transaction conversion
- Receipt generation
- Account state queries
- Gas estimation

### 3. gETH Configuration
**File:** `gETH_config.go`

Data structures for Ethereum compatibility:
- `Block`: Ethereum-compatible block structure
- `BlockHeader`: Block header structure
- `Transaction`: Transaction structure
- `Receipt`: Transaction receipt structure

### 4. HTTP Facade
**File:** `Facade/rpc/http_server.go`

HTTP server implementing Ethereum JSON-RPC API:
- JSON-RPC 2.0 protocol
- Standard Ethereum endpoints
- Error handling
- Request validation

### 5. WebSocket Facade
**File:** `Facade/rpc/ws_server.go`

WebSocket server for real-time subscriptions:
- New block subscriptions
- Event log subscriptions
- Pending transaction subscriptions

### 6. Service Layer
**File:** `Facade/Service/Service.go`

Service interface and implementation:
- Chain operations
- Account operations
- Transaction operations
- Event operations

## Key Functions

### Get Block by Number

```go
// Get block by block number
func _GetBlockByNumber(req *proto.GetBlockByNumberReq) (*proto.Block, error) {
    // Get ZKBlock from database
    // Convert to Ethereum format
    // Return block
}
```

### Get Account State

```go
// Get account state
func _GetAccountState(req *proto.GetAccountStateReq) (*proto.AccountState, error) {
    // Get account from database
    // Get transactions
    // Calculate nonce and balance
    // Return account state
}
```

### Submit Transaction

```go
// Submit raw transaction
func _SubmitRawTransaction(req *proto.SendRawTxReq) (*proto.SendRawTxResp, error) {
    // Parse transaction
    // Submit to mempool
    // Return transaction hash
}
```

## Usage

### Starting gETH gRPC Server

```bash
# Start node with gETH gRPC server
./jmdn -geth 15054
```

### Starting gETH Facade Server

```bash
# Start node with gETH facade (HTTP/WebSocket)
./jmdn -facade 8545 -ws 8546
```

### Using gRPC Client

```go
import "gossipnode/gETH/proto"

// Connect to gRPC server
conn, _ := grpc.Dial("localhost:15054", grpc.WithInsecure())
client := proto.NewGETHServiceClient(conn)

// Get block by number
block, _ := client.GetBlockByNumber(ctx, &proto.GetBlockByNumberReq{Number: 123})

// Get account state
state, _ := client.GetAccountState(ctx, &proto.GetAccountStateReq{Address: addr})
```

### Using HTTP Facade (Ethereum JSON-RPC)

```bash
# Get block by number
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "eth_getBlockByNumber",
    "params": ["0x7b", true],
    "id": 1
  }'

# Get account balance
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "eth_getBalance",
    "params": ["0x1234...", "latest"],
    "id": 1
  }'

# Send raw transaction
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "eth_sendRawTransaction",
    "params": ["0x..."],
    "id": 1
  }'
```

### Using WebSocket Facade

```javascript
// Connect to WebSocket
const ws = new WebSocket('ws://localhost:8546');

// Subscribe to new blocks
ws.send(JSON.stringify({
  jsonrpc: "2.0",
  method: "eth_subscribe",
  params: ["newHeads"],
  id: 1
}));

// Receive block updates
ws.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log('New block:', data);
};
```

## Integration Points

### Block Module
- Uses block data structures
- Accesses block operations
- Submits transactions to mempool

### Database (DB_OPs)
- Queries blocks and transactions
- Retrieves account information
- Accesses receipt data

### Mempool Service
- Submits transactions
- Queries transaction status

### Config Module
- Uses chain ID configuration
- Accesses network parameters

## Configuration

gETH server ports can be configured via command-line flags:

```bash
./jmdn -geth 15054      # gRPC server port
./jmdn -facade 8545     # HTTP facade port
./jmdn -ws 8546         # WebSocket facade port
./jmdn -chainID 7000700 # Chain ID
```

## Error Handling

The module includes comprehensive error handling:
- Invalid block number errors
- Transaction validation errors
- Account not found errors
- Network errors

## Logging

gETH operations are logged to:
- `logs/gETH.log`: gETH operations
- Loki (if enabled): Centralized logging

## Security

- Transaction signature verification
- Input validation
- Rate limiting (via middleware)
- Error message sanitization

## Performance

- Efficient block conversion
- Connection pooling
- Batch operations
- Caching for frequently accessed data

## Testing

Test files:
- `gETH_test.go`: gETH operation tests
- gRPC service tests
- HTTP facade tests
- WebSocket tests

## Future Enhancements

- Enhanced Ethereum compatibility
- Advanced smart contract support
- Improved gas estimation
- Performance optimizations
- Additional JSON-RPC methods

