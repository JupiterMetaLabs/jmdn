# JMZK Blockchain Explorer API Documentation

This document provides comprehensive API documentation for the JMZK Blockchain Explorer, including all available endpoints with curl examples and detailed descriptions.

## Base URL
```
http://localhost:8090
```

## Table of Contents
- [Statistics Endpoints](#statistics-endpoints)
- [Address Endpoints](#address-endpoints)
- [Block Endpoints](#block-endpoints)
- [Transaction Endpoints](#transaction-endpoints)
- [DID Endpoints](#did-endpoints)
- [WebSocket Endpoints](#websocket-endpoints)
- [Error Handling](#error-handling)
- [Response Formats](#response-formats)

---

## Statistics Endpoints

### Get Network Statistics
Retrieves comprehensive network statistics including total blocks, transactions, addresses, and balances.

**Endpoint:** `GET /api/stats/`

**Description:** Returns overall network statistics including database state, merkle root, latest block number, total counts, and network balance.

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/stats/" \
  -H "Accept: application/json"
```

**Response Example:**
```json
{
  "DBState": {
    "db": "defaultdb",
    "txId": 15,
    "txHash": "OIgZ6ld/mzMa/XK6PQeBBqjUnmndj9tnBRc7Hvn/Y30=",
    "precommittedTxId": 15,
    "precommittedTxHash": "OIgZ6ld/mzMa/XK6PQeBBqjUnmndj9tnBRc7Hvn/Y30="
  },
  "MerkleRoot": "388819ea577f9b331afd72ba3d078106a8d49e69dd8fdb6705173b1ef9ff637d",
  "LatestBlockNumber": 1,
  "TotalBlocks": 2,
  "TotalDIDs": 2,
  "TotalTransactions": 1,
  "TotalAddresses": 2,
  "TotalBalance": "10000000000000000000 + 0"
}
```

---

## Address Endpoints

### List All Addresses
Retrieves a paginated list of all addresses with their balances and account information.

**Endpoint:** `GET /api/addresses/`

**Description:** Returns all addresses in the network with pagination support. Includes balance, nonce, account type, and DID information.

**Query Parameters:**
- `page` (optional): Page number (default: 1)
- `limit` (optional): Items per page (default: 20, max: 100)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/addresses/?page=1&limit=20" \
  -H "Accept: application/json"
```

**Response Example:**
```json
{
  "addresses": [
    {
      "address": "0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45",
      "balance": "10000000000000000000",
      "nonce": 13093881962719346690,
      "account_type": "user",
      "did_address": "did:jmdt:metamask:0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45",
      "created_at": 1761388726,
      "updated_at": 1761388726121679000
    }
  ],
  "pagination": {
    "current_page": 1,
    "has_next": false,
    "has_prev": false,
    "per_page": 20,
    "total_items": 2,
    "total_pages": 1
  }
}
```

### Get Address Details
Retrieves detailed information about a specific address including transaction count and metadata.

**Endpoint:** `GET /api/addresses/{address}`

**Description:** Returns comprehensive details for a specific address including balance, nonce, account type, transaction count, and metadata.

**Path Parameters:**
- `address`: Ethereum address (hex format, e.g., 0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/addresses/0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45" \
  -H "Accept: application/json"
```

**Response Example:**
```json
{
  "address": "0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45",
  "balance": "10000000000000000000",
  "nonce": 13093881962719346690,
  "account_type": "user",
  "did_address": "did:jmdt:metamask:0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45",
  "created_at": 1761388726,
  "updated_at": 1761388726121679000,
  "transaction_count": 1,
  "metadata": {}
}
```

### Get Address Transactions
Retrieves all transactions associated with a specific address.

**Endpoint:** `GET /api/addresses/{address}/transactions`

**Description:** Returns paginated list of transactions for a specific address, including both incoming and outgoing transactions.

**Path Parameters:**
- `address`: Ethereum address (hex format)

**Query Parameters:**
- `page` (optional): Page number (default: 1)
- `limit` (optional): Items per page (default: 20, max: 100)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/addresses/0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45/transactions?page=1&limit=20" \
  -H "Accept: application/json"
```

**Response Example:**
```json
{
  "transactions": [
    {
      "hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
      "from": "0x69ee9a32109ee1cc8c95b49ad1d4ddaebb46db45",
      "to": "0xcdf1effd70cecb41ba0b4c41eb13d263578a4cc2",
      "value": 0,
      "type": 0,
      "timestamp": 1761045000,
      "chain_id": 7000700,
      "nonce": 0,
      "gas_limit": 21000,
      "gas_price": 20000000000
    }
  ],
  "pagination": {
    "current_page": 1,
    "per_page": 20,
    "total_pages": 1,
    "total_items": 1,
    "has_next": false,
    "has_prev": false
  }
}
```

---

## Block Endpoints

### List All Blocks
Retrieves a paginated list of all blocks in the blockchain.

**Endpoint:** `GET /api/block/all`

**Description:** Returns all blocks with pagination support, ordered by block number (newest first).

**Query Parameters:**
- `page` (optional): Page number (default: 1)
- `limit` (optional): Items per page (default: 10, max: 100)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/all?page=1&limit=10" \
  -H "Accept: application/json"
```

**Response Example:**
```json
{
  "data": [
    {
      "blocknumber": 1,
      "blockhash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
      "transactions": [
        {
          "hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
          "from": "0x69ee9a32109ee1cc8c95b49ad1d4ddaebb46db45",
          "to": "0xcdf1effd70cecb41ba0b4c41eb13d263578a4cc2",
          "value": 0,
          "type": 0,
          "timestamp": 1761045000,
          "chain_id": 7000700,
          "nonce": 0,
          "gas_limit": 21000,
          "gas_price": 20000000000
        }
      ],
      "timestamp": 1761045000,
      "starkproof": "",
      "commitment": [1234567890, 2345678901, 3456789012],
      "proof_hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
      "status": "verified",
      "txnsroot": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
      "stateroot": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
      "logsbloom": "",
      "coinbaseaddr": "0x69ee9a32109ee1cc8c95b49ad1d4ddaebb46db45",
      "zkvmaddr": "0x69ee9a32109ee1cc8c95b49ad1d4ddaebb46db45",
      "prevhash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
      "gaslimit": 30000000,
      "gasused": 21000
    }
  ],
  "pagination": {
    "current_page": 1,
    "per_page": 10,
    "total_pages": 1,
    "total_items": 1,
    "has_next": false,
    "has_prev": false
  }
}
```

### Get Block by Hash
Retrieves a specific block by its hash.

**Endpoint:** `GET /api/block/id/{hash}`

**Description:** Returns detailed information about a specific block identified by its hash.

**Path Parameters:**
- `hash`: Block hash (hex format)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/id/0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef" \
  -H "Accept: application/json"
```

### Get Block by Number
Retrieves a specific block by its block number.

**Endpoint:** `GET /api/block/number/{number}`

**Description:** Returns detailed information about a specific block identified by its block number.

**Path Parameters:**
- `number`: Block number (integer)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/number/1" \
  -H "Accept: application/json"
```

### Get Latest Blocks
Retrieves the most recent blocks up to a specified count.

**Endpoint:** `GET /api/block/latest/{count}`

**Description:** Returns the latest N blocks (maximum 100 blocks at a time).

**Path Parameters:**
- `count`: Number of latest blocks to return (max: 100)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/latest/10" \
  -H "Accept: application/json"
```

### Get Missing Blocks
Retrieves blocks that are missing from a given block number to the latest block.

**Endpoint:** `GET /api/block/missing/{number}`

**Description:** Returns all blocks from the specified block number to the latest block, useful for synchronization.

**Path Parameters:**
- `number`: Starting block number

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/missing/1" \
  -H "Accept: application/json"
```

### Get Block Health Check
Checks the health status of the block service.

**Endpoint:** `GET /api/block/health`

**Description:** Returns the health status of the block-related services.

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/health" \
  -H "Accept: application/json"
```

---

## Transaction Endpoints

### List All Transactions
Retrieves a paginated list of all transactions in the blockchain.

**Endpoint:** `GET /api/block/transactions/all`

**Description:** Returns all transactions with pagination support, ordered by timestamp (newest first).

**Query Parameters:**
- `page` (optional): Page number (default: 1)
- `limit` (optional): Items per page (default: 10, max: 100)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/transactions/all?page=1&limit=10" \
  -H "Accept: application/json"
```

**Response Example:**
```json
{
  "pagination": {
    "limit": 10,
    "page": 1,
    "total": 1,
    "totalPages": 1
  },
  "transactions": [
    {
      "hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
      "from": "0x69ee9a32109ee1cc8c95b49ad1d4ddaebb46db45",
      "to": "0xcdf1effd70cecb41ba0b4c41eb13d263578a4cc2",
      "value": 0,
      "type": 0,
      "timestamp": 1761045000,
      "chain_id": 7000700,
      "nonce": 0,
      "gas_limit": 21000,
      "gas_price": 20000000000,
      "v": 27,
      "r": "1234567890123456789012345678901234567890123456789012345678901234567890",
      "s": "9876543210987654321098765432109876543210987654321098765432109876543210"
    }
  ]
}
```

### Get Transaction by Hash
Retrieves detailed information about a specific transaction.

**Endpoint:** `GET /api/block/transactions/{hash}`

**Description:** Returns comprehensive details about a specific transaction including all transaction data and receipt information.

**Path Parameters:**
- `hash`: Transaction hash (hex format)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/transactions/0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef" \
  -H "Accept: application/json"
```

### Get Transactions in Block
Retrieves all transactions within a specific block.

**Endpoint:** `GET /api/block/transactions/block/{number}`

**Description:** Returns all transactions contained within a specific block.

**Path Parameters:**
- `number`: Block number (integer)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/block/transactions/block/1" \
  -H "Accept: application/json"
```

---

## DID Endpoints

### List All DIDs
Retrieves a paginated list of all Decentralized Identifiers (DIDs) in the network.

**Endpoint:** `GET /api/did/all/`

**Description:** Returns all DIDs with pagination support, including DID addresses and associated metadata.

**Query Parameters:**
- `page` (optional): Page number (default: 1)
- `limit` (optional): Items per page (default: 20, max: 100)

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/did/all/?page=1&limit=20" \
  -H "Accept: application/json"
```

### Get DID Details
Retrieves detailed information about specific DIDs.

**Endpoint:** `GET /api/did/details`

**Description:** Returns detailed information about one or more DIDs specified in query parameters.

**Query Parameters:**
- `did` (required): DID string(s) to retrieve details for

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/did/details?did=did:jmdt:metamask:0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45" \
  -H "Accept: application/json"
```

### Get DID Details by Address
Retrieves DID information associated with a specific address.

**Endpoint:** `GET /api/did/details/pubaddr`

**Description:** Returns DID document information for a specific public address.

**Query Parameters:**
- `addr` (required): Public address to get DID details for

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/did/details/pubaddr?addr=0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45" \
  -H "Accept: application/json"
```

### DID Health Check
Checks the health status of the DID service.

**Endpoint:** `GET /api/did/health`

**Description:** Returns the health status of the DID-related services.

**Curl Example:**
```bash
curl -X GET "http://localhost:8090/api/did/health" \
  -H "Accept: application/json"
```

---

## WebSocket Endpoints

### Stream Real-time Blocks
Establishes a WebSocket connection to receive real-time block updates.

**Endpoint:** `WS /api/sockets/blocks`

**Description:** Provides a WebSocket connection that streams new blocks as they are added to the blockchain in real-time.

**WebSocket Example:**
```javascript
const ws = new WebSocket('ws://localhost:8090/api/sockets/blocks');

ws.onopen = function(event) {
    console.log('Connected to block stream');
};

ws.onmessage = function(event) {
    const block = JSON.parse(event.data);
    console.log('New block received:', block);
};

ws.onerror = function(error) {
    console.error('WebSocket error:', error);
};

ws.onclose = function(event) {
    console.log('WebSocket connection closed');
};
```

---

## Error Handling

### Common Error Responses

**400 Bad Request:**
```json
{
  "error": "Invalid address format"
}
```

**404 Not Found:**
```json
{
  "error": "Address not found"
}
```

**500 Internal Server Error:**
```json
{
  "error": "Failed to fetch addresses"
}
```

### Error Codes
- `400`: Bad Request - Invalid parameters or malformed requests
- `404`: Not Found - Resource not found
- `500`: Internal Server Error - Server-side errors

---

## Response Formats

### Pagination Format
All list endpoints return pagination metadata in the following format:
```json
{
  "pagination": {
    "current_page": 1,
    "per_page": 20,
    "total_pages": 5,
    "total_items": 100,
    "has_next": true,
    "has_prev": false
  }
}
```

### Address Format
Address objects contain the following fields:
```json
{
  "address": "0x...",
  "balance": "10000000000000000000",
  "nonce": 1234567890,
  "account_type": "user",
  "did_address": "did:jmdt:metamask:0x...",
  "created_at": 1761388726,
  "updated_at": 1761388726121679000
}
```

### Transaction Format
Transaction objects contain the following fields:
```json
{
  "hash": "0x...",
  "from": "0x...",
  "to": "0x...",
  "value": 0,
  "type": 0,
  "timestamp": 1761045000,
  "chain_id": 7000700,
  "nonce": 0,
  "gas_limit": 21000,
  "gas_price": 20000000000,
  "v": 27,
  "r": "...",
  "s": "..."
}
```

### Block Format
Block objects contain the following fields:
```json
{
  "blocknumber": 1,
  "blockhash": "0x...",
  "transactions": [...],
  "timestamp": 1761045000,
  "starkproof": "",
  "commitment": [...],
  "proof_hash": "0x...",
  "status": "verified",
  "txnsroot": "0x...",
  "stateroot": "0x...",
  "logsbloom": "",
  "coinbaseaddr": "0x...",
  "zkvmaddr": "0x...",
  "prevhash": "0x...",
  "gaslimit": 30000000,
  "gasused": 21000
}
```

---

## Usage Examples

### Complete API Testing Script
```bash
#!/bin/bash

BASE_URL="http://localhost:8090"

echo "Testing JMZK Blockchain Explorer API"
echo "=================================="

# Test statistics
echo "1. Testing statistics endpoint..."
curl -s "$BASE_URL/api/stats/" | jq '.'

# Test addresses
echo -e "\n2. Testing addresses endpoint..."
curl -s "$BASE_URL/api/addresses/" | jq '.'

# Test blocks
echo -e "\n3. Testing blocks endpoint..."
curl -s "$BASE_URL/api/block/all" | jq '.'

# Test transactions
echo -e "\n4. Testing transactions endpoint..."
curl -s "$BASE_URL/api/block/transactions/all" | jq '.'

# Test specific address details
echo -e "\n5. Testing address details..."
ADDRESS=$(curl -s "$BASE_URL/api/addresses/" | jq -r '.addresses[0].address')
curl -s "$BASE_URL/api/addresses/$ADDRESS" | jq '.'

echo -e "\nAPI testing completed!"
```

### Web Interface Access
The explorer also provides a web interface accessible at:
```
http://localhost:8090
```

This interface provides a user-friendly way to browse addresses, transactions, and blocks without needing to use curl commands.

---

## Notes

- All timestamps are in Unix epoch format (seconds since January 1, 1970)
- Addresses are in Ethereum format (0x followed by 40 hex characters)
- All hash values are in hex format
- Pagination is available on all list endpoints
- The API supports CORS for web applications
- WebSocket connections are available for real-time updates
- All endpoints return JSON responses
- Error responses include descriptive error messages
