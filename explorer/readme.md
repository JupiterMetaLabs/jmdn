# Explorer Module

## Overview

The Explorer module provides a comprehensive blockchain explorer for the JMZK decentralized network. It offers both a web interface and RESTful API for viewing addresses, balances, transactions, and blocks.

## Purpose

The Explorer module enables:
- Web-based blockchain exploration
- RESTful API for programmatic access
- Real-time transaction streaming via WebSocket
- Network statistics and monitoring
- Address and transaction history
- Block browsing and details

## Key Features

### 🔍 **Address Management**
- List all addresses with pagination
- View address details including balance, nonce, and account type
- Transaction history for specific addresses
- Search functionality to find addresses quickly

### 📊 **Transaction Explorer**
- Browse all transactions with pagination
- Transaction details including hash, from/to addresses, value, and block information
- Filter by address to see transactions for specific accounts
- Real-time transaction streaming via WebSocket

### 🧱 **Block Explorer**
- List all blocks with pagination
- Block details including hash, timestamp, and transaction count
- Latest blocks with real-time updates
- Missing block detection for network synchronization

### 📈 **Network Statistics**
- Total blocks count
- Total transactions count
- Total addresses count
- Latest block number
- Total network balance
- Database state and merkle root

## Key Components

### 1. API Server
**File:** `api.go`

Main HTTP server with Gin framework:
- RESTful API endpoints
- CORS support
- Health check endpoints
- WebSocket support

### 2. Block Operations
**File:** `BlockOps.go`

Block-related API handlers:
- Get block by number
- Get block by hash
- List all blocks
- Get latest blocks
- Get missing blocks

### 3. Address Operations
**File:** `addressOps.go`

Address and balance API handlers:
- List all addresses
- Get address details
- Get address transactions
- Get address balance

### 4. DID Operations
**File:** `DIDOps.go`

DID-related functionality:
- List all DIDs
- Get DID details
- DID statistics

### 5. Streaming
**File:** `StreamTxns.go`

Real-time data streaming:
- WebSocket support for block updates
- Transaction streaming
- Real-time statistics

### 6. Health Check
**File:** `health.go`

Health check endpoints:
- `/health`: Basic health check
- `/ready`: Readiness check

### 7. Utilities
**File:** `utils.go`

Utility functions:
- Block polling
- Data conversion
- Error handling

### 8. Web Interface
**File:** `index.html`

Modern, responsive web interface:
- Dashboard with network statistics
- Address browser
- Transaction explorer
- Block browser
- Search functionality

## API Endpoints

### Address Endpoints
```
GET /api/addresses/                    # List all addresses with pagination
GET /api/addresses/:address            # Get specific address details
GET /api/addresses/:address/transactions # Get transactions for an address
```

### Block Endpoints
```
GET /api/block/all                     # List all blocks with pagination
GET /api/block/id/:id                  # Get block by hash
GET /api/block/number/:number          # Get block by number
GET /api/block/latest/:count           # Get latest N blocks
GET /api/block/missing/:number         # Get missing blocks from a given number
```

### Transaction Endpoints
```
GET /api/block/transactions/all        # List all transactions with pagination
GET /api/block/transactions/:hash     # Get transaction by hash
GET /api/block/transactions/block/:number # Get transactions in a specific block
```

### Statistics Endpoints
```
GET /api/stats/                        # Get network statistics
```

### WebSocket Endpoints
```
WS /api/sockets/blocks                 # Stream real-time block updates
```

## Usage

### Starting the Explorer

```bash
# Start the node with explorer enabled
./jmdn -api 8085 -explorer
```

### Accessing the Web Interface

Open your browser and go to:
```
http://localhost:8085
```

### API Access

All API endpoints are available at:
```
http://localhost:8085/api/
```

### Example API Calls

**Get all addresses:**
```bash
curl "http://localhost:8085/api/addresses/?page=1&limit=20"
```

**Get address details:**
```bash
curl "http://localhost:8085/api/addresses/0x1234567890abcdef1234567890abcdef12345678"
```

**Get network statistics:**
```bash
curl "http://localhost:8085/api/stats/"
```

**Get latest blocks:**
```bash
curl "http://localhost:8085/api/block/latest/10"
```

## Integration Points

### Database (DB_OPs)
- Queries blocks, transactions, and accounts
- Retrieves network statistics
- Accesses database state

### Block Module
- Uses block data structures
- Accesses block operations

### DID Module
- Displays DID information
- Lists all DIDs

### Messaging Module
- Receives block updates
- Handles real-time streaming

## Configuration

Explorer server port can be configured via command-line flag:

```bash
./jmdn -api 8085 -explorer  # Enable explorer on port 8085
```

## Error Handling

The module includes comprehensive error handling:
- Invalid address format errors
- Block not found errors
- Database connection errors
- Pagination errors

## Logging

Explorer operations are logged to:
- `logs/explorer.log`: Explorer operations
- Loki (if enabled): Centralized logging

## Security

- CORS support for cross-origin requests
- Input validation for all parameters
- Address format validation
- Error message sanitization

## Performance

- Pagination for large datasets
- Connection pooling
- Efficient database queries
- Real-time updates via WebSocket

## Testing

Test files:
- `explorer_test.go`: Explorer API tests
- Integration tests
- WebSocket tests

## Future Enhancements

- Enhanced search functionality
- Advanced filtering options
- Export capabilities
- Performance optimizations
- Mobile-responsive improvements
