# JMDT Blockchain Explorer

A comprehensive blockchain explorer for the JMDT decentralized network that allows you to view addresses, balances, transactions, and blocks.

## Features

### 🔍 **Address Management**
- **List all addresses** with pagination support
- **View address details** including balance, nonce, and account type
- **Transaction history** for specific addresses
- **Search functionality** to find addresses quickly

### 📊 **Transaction Explorer**
- **Browse all transactions** with pagination
- **Transaction details** including hash, from/to addresses, value, and block information
- **Filter by address** to see transactions for specific accounts
- **Real-time transaction streaming** via WebSocket

### 🧱 **Block Explorer**
- **List all blocks** with pagination
- **Block details** including hash, timestamp, and transaction count
- **Latest blocks** with real-time updates
- **Missing block detection** for network synchronization

### 📈 **Network Statistics**
- **Total blocks** count
- **Total transactions** count
- **Total addresses** count
- **Latest block number**
- **Total network balance**
- **Database state** and merkle root

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
GET /api/block/transactions/:hash      # Get transaction by hash
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

1. **Start the explorer server:**
   ```bash
   go run main.go
   ```

2. **Access the web interface:**
   - Open your browser and go to `http://localhost:8085`
   - The explorer will automatically load network statistics and addresses

3. **API access:**
   - All API endpoints are available at `http://localhost:8085/api/`
   - Use the web interface or tools like Postman to interact with the API

### Web Interface Features

- **Dashboard**: View network statistics at a glance
- **Addresses Tab**: Browse all addresses with their balances
- **Transactions Tab**: Explore transaction history
- **Blocks Tab**: View blockchain blocks and their details
- **Search**: Use the search boxes to find specific addresses, transactions, or blocks
- **Pagination**: Navigate through large datasets easily

### API Examples

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

## Configuration

The explorer uses the existing database connections from the JMDT network:
- **Main Database**: Stores blocks, transactions, and receipts
- **Accounts Database**: Stores address information and balances

## Architecture

### Backend Components
- **API Server** (`api.go`): Main HTTP server with Gin framework
- **Block Operations** (`BlockOps.go`): Block-related API handlers
- **Address Operations** (`addressOps.go`): Address and balance API handlers
- **DID Operations** (`DIDOps.go`): DID-related functionality
- **Streaming** (`StreamTxns.go`): Real-time data streaming

### Frontend Components
- **HTML Interface** (`index.html`): Modern, responsive web interface
- **JavaScript**: Client-side API interactions and UI management
- **CSS**: Beautiful styling with gradient backgrounds and card layouts

### Database Integration
- **Connection Pooling**: Efficient database connection management
- **Error Handling**: Comprehensive error handling and logging
- **Pagination**: Optimized data retrieval with pagination support

## Security Features

- **CORS Support**: Cross-origin resource sharing enabled
- **Input Validation**: Address format validation and parameter sanitization
- **Error Handling**: Secure error messages without sensitive information
- **Connection Pooling**: Prevents connection exhaustion attacks

## Performance Optimizations

- **Pagination**: Limits data transfer and improves response times
- **Connection Pooling**: Reuses database connections efficiently
- **Concurrent Processing**: Uses goroutines for parallel data fetching
- **Caching**: Implements efficient data caching strategies

## Development

### Adding New Features

1. **New API Endpoints**: Add routes in `api.go` and implement handlers
2. **Database Operations**: Extend `DB_OPs` package with new functions
3. **Frontend Updates**: Modify `index.html` for new UI features
4. **Testing**: Use the existing test patterns for new functionality

### Code Structure

```
explorer/
├── api.go              # Main API server and routing
├── BlockOps.go         # Block-related operations
├── addressOps.go       # Address and balance operations
├── DIDOps.go          # DID operations
├── StreamTxns.go      # Real-time streaming
├── health.go          # Health check endpoints
├── utils.go           # Utility functions
├── index.html         # Web frontend
└── readme.md          # This documentation
```

## Troubleshooting

### Common Issues

1. **Database Connection Errors**: Ensure ImmuDB is running and accessible
2. **Port Conflicts**: Check if port 8085 is available
3. **CORS Issues**: Verify CORS middleware is properly configured
4. **Missing Data**: Check database state and ensure data is properly indexed

### Logs

The explorer logs all operations to:
- **File**: `logs/explorer.log`
- **Loki**: If configured, logs are also sent to Loki for centralized logging

## Contributing

When contributing to the explorer:

1. **Follow the existing code patterns**
2. **Add proper error handling and logging**
3. **Include pagination for list endpoints**
4. **Update documentation for new features**
5. **Test with real blockchain data**

## License

This explorer is part of the JMDT decentralized network project.