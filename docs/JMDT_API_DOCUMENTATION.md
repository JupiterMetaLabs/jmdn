# JMDT Decentralized Network - Facade API Documentation

## Overview

The JMDT Facade API provides a JSON-RPC 2.0 compatible interface for interacting with the JMDT decentralized network. It supports both HTTP and WebSocket connections, offering Ethereum-compatible RPC methods for blockchain operations.

## Base URLs

- **HTTP JSON-RPC**: `http://localhost:{facade_port}/`
- **WebSocket**: `ws://localhost:{ws_port}/`


## Content Type

All requests must use `Content-Type: application/json`.

## JSON-RPC 2.0 Format

All requests follow the JSON-RPC 2.0 specification:

```json
{
  "jsonrpc": "2.0",
  "method": "method_name",
  "params": [...],
  "id": 1
}
```

## Supported Methods

### Network Information

#### `web3_clientVersion`
Returns the client version string.

**Parameters:** None

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "JMDT-Node/1.0.0",
  "id": 1
}
```

#### `net_version`
Returns the network ID as a string.

**Parameters:** None

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "700700",
  "id": 1
}
```

#### `eth_chainId`
Returns the chain ID as a hex string.

**Parameters:** None

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0x539",
  "id": 1
}
```

### Block Operations

#### `eth_blockNumber`
Returns the latest block number.

**Parameters:** None

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0x1a2b3c",
  "id": 1
}
```

#### `eth_getBlockByNumber`
Returns block information by block number.

**Parameters:**
- `blockTag` (string): Block identifier ("latest", "pending", or hex number)
- `fullTx` (boolean): If true, returns full transaction objects; if false, returns transaction hashes only

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_getBlockByNumber",
  "params": ["latest", true],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "number": "0x1a2b3c",
    "hash": "0x1234567890abcdef...",
    "parentHash": "0xabcdef1234567890...",
    "timestamp": "0x1234567890",
    "transactions": [
      {
        "hash": "0xtxhash...",
        "from": "0xfromaddress...",
        "to": "0xtoaddress...",
        "input": "0xdata...",
        "value": "0x1000",
        "nonce": "0x1",
        "gas": "0x5208",
        "gasPrice": "0x3b9aca00",
        "type": "0x0"
      }
    ]
  },
  "id": 1
}
```

### Account Operations

#### `eth_getBalance`
Returns the balance of an account.

**Parameters:**
- `address` (string): Account address (hex string)
- `blockTag` (string): Block identifier

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_getBalance",
  "params": ["0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6", "latest"],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0xde0b6b3a7640000",
  "id": 1
}
```

### Transaction Operations

#### `eth_call`
Executes a message call without creating a transaction.

**Parameters:**
- `callObject` (object): Call object containing:
  - `from` (string, optional): Sender address
  - `to` (string): Recipient address
  - `data` (string): Call data (hex string)
  - `value` (string, optional): Value in wei (hex string)
  - `gas` (string, optional): Gas limit (hex string)
  - `gasPrice` (string, optional): Gas price (hex string)
- `blockTag` (string, optional): Block identifier (defaults to "latest")

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_call",
  "params": [
    {
      "to": "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
      "data": "0x70a08231000000000000000000000000742d35cc6634c0532925a3b8d4c9db96c4b4d8b6"
    },
    "latest"
  ],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "id": 1
}
```

#### `eth_estimateGas`
Estimates the gas required for a transaction.

**Parameters:**
- `callObject` (object): Same structure as `eth_call`

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_estimateGas",
  "params": [
    {
      "to": "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
      "value": "0x1000"
    }
  ],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0x5208",
  "id": 1
}
```

#### `eth_gasPrice`
Returns the current gas price.

**Parameters:** None

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0x3b9aca00",
  "id": 1
}
```

#### `eth_sendRawTransaction`
Sends a signed transaction.

**Parameters:**
- `rawTransaction` (string): Signed transaction as hex string

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_sendRawTransaction",
  "params": ["0x0a1234567890abcdef..."],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0x1234567890abcdef...",
  "id": 1
}
```

#### `eth_getTransactionByHash`
Returns transaction information by hash.

**Parameters:**
- `transactionHash` (string): Transaction hash (hex string)

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_getTransactionByHash",
  "params": ["0x1234567890abcdef..."],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "hash": "0x1234567890abcdef...",
    "from": "0xfromaddress...",
    "to": "0xtoaddress...",
    "input": "0xdata...",
    "value": "0x1000",
    "nonce": "0x1",
    "gas": "0x5208",
    "gasPrice": "0x3b9aca00",
    "type": "0x0",
    "r": "0x...",
    "s": "0x...",
    "v": "0x1b"
  },
  "id": 1
}
```

#### `eth_getTransactionReceipt`
Returns transaction receipt by hash.

**Parameters:**
- `transactionHash` (string): Transaction hash (hex string)

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_getTransactionReceipt",
  "params": ["0x1234567890abcdef..."],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "transactionHash": "0x1234567890abcdef...",
    "transactionIndex": "0x0",
    "blockNumber": "0x1a2b3c",
    "blockHash": "0xblockhash...",
    "from": "0xfromaddress...",
    "to": "0xtoaddress...",
    "gasUsed": "0x5208",
    "cumulativeGasUsed": "0x5208",
    "contractAddress": null,
    "logs": [],
    "status": "0x1"
  },
  "id": 1
}
```

### Event Logging

#### `eth_getLogs`
Returns logs matching the filter criteria.

**Parameters:**
- `filterObject` (object): Filter object containing:
  - `fromBlock` (string, optional): Start block (hex string or "latest")
  - `toBlock` (string, optional): End block (hex string or "latest")
  - `address` (array, optional): Contract addresses to filter
  - `topics` (array, optional): Event signature topics

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_getLogs",
  "params": [
    {
      "fromBlock": "0x1",
      "toBlock": "latest",
      "address": "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
      "topics": ["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"]
    }
  ],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": [
    {
      "address": "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
      "topics": ["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"],
      "data": "0x0000000000000000000000000000000000000000000000000000000000000000",
      "blockNumber": "0x1a2b3c",
      "transactionHash": "0xtxhash...",
      "logIndex": "0x0",
      "blockHash": "0xblockhash...",
      "transactionIndex": "0x0",
      "removed": false
    }
  ],
  "id": 1
}
```

## WebSocket Subscriptions

The WebSocket interface supports real-time subscriptions for:

### `eth_subscribe`
Creates a subscription for real-time updates.

**Parameters:**
- `subscriptionType` (string): Type of subscription
- `filter` (object, optional): Filter criteria for logs subscription

**Supported Subscription Types:**

#### `newHeads`
Subscribes to new block headers.

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscribe",
  "params": ["newHeads"],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": "0x1234567890.000000",
  "id": 1
}
```

**Subscription Notifications:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscription",
  "params": {
    "subscription": "0x1234567890.000000",
    "result": {
      "number": "0x1a2b3c",
      "hash": "0x1234567890abcdef...",
      "parentHash": "0xabcdef1234567890...",
      "timestamp": "0x1234567890"
    }
  }
}
```

#### `logs`
Subscribes to event logs matching filter criteria.

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscribe",
  "params": [
    "logs",
    {
      "address": "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
      "topics": ["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"]
    }
  ],
  "id": 1
}
```

#### `newPendingTransactions`
Subscribes to new pending transactions.

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscribe",
  "params": ["newPendingTransactions"],
  "id": 1
}
```

### `eth_unsubscribe`
Cancels a subscription.

**Parameters:**
- `subscriptionId` (string): Subscription ID to cancel

**Example Request:**
```json
{
  "jsonrpc": "2.0",
  "method": "eth_unsubscribe",
  "params": ["0x1234567890.000000"],
  "id": 1
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "result": true,
  "id": 1
}
```

## Error Handling

All errors follow the JSON-RPC 2.0 error format:

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": -32000,
    "message": "Error description"
  },
  "id": 1
}
```

### Common Error Codes

- `-32700`: Parse error
- `-32600`: Invalid Request
- `-32601`: Method not found
- `-32602`: Invalid params
- `-32000`: Server error

### Health Checks

Monitor the following endpoints:
- HTTP: `GET /` (returns 200 when healthy)
- WebSocket: Connection establishment and subscription responses

### Logging

All requests and responses are logged with the following format:
- 📥 Incoming requests
- 📤 Outgoing responses
- 🔌 WebSocket messages
- ❌ Error conditions

## Example Usage

### Using curl

```bash
# Get latest block number
curl -X POST http://localhost:8545/ \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Get account balance
curl -X POST http://localhost:8545/ \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6","latest"],"id":1}'
```

### Using WebSocket (JavaScript)

```javascript
const ws = new WebSocket('ws://localhost:8546/');

ws.onopen = function() {
  // Subscribe to new blocks
  ws.send(JSON.stringify({
    jsonrpc: "2.0",
    method: "eth_subscribe",
    params: ["newHeads"],
    id: 1
  }));
};

ws.onmessage = function(event) {
  const data = JSON.parse(event.data);
  console.log('Received:', data);
};
```

## Support

For technical support and questions:
- GitHub Issues: [Repository Issues](https://github.com/your-org/JMDT-decentralized-network/issues)
---

*This documentation is generated for JMDT Decentralized Network v1.0.0*
