# DID Module

## Overview

The DID (Decentralized Identity) module provides decentralized identity management for the JMZK network. It enables users to register, manage, and query decentralized identities with associated public keys, balances, and metadata.

## Purpose

The DID module handles:
- Decentralized identity registration
- DID document storage and retrieval
- Account management with balances
- DID propagation across the network
- gRPC service for DID operations

## Key Components

### 1. gRPC Server
**File:** `DID.go`

Provides gRPC service for DID operations:

**Service Methods:**
- `RegisterDID`: Register a new DID with public key
- `GetDID`: Retrieve DID information by identifier
- `ListDIDs`: List all DIDs with pagination
- `GetDIDStats`: Get statistics about the DID system

### 2. Protocol Buffers
**File:** `proto/DID.proto`

Defines gRPC service and message types:
- `DIDService`: Main service interface
- `DIDInfo`: DID information structure
- `RegisterDIDRequest/Response`: Registration messages
- `GetDIDRequest/Response`: Query messages
- `ListDIDsRequest/Response`: List messages
- `DIDStats`: Statistics message

## Key Functions

### Register DID

```go
// Register a new DID
func (s *AccountServer) RegisterDID(ctx context.Context, req *pb.RegisterDIDRequest) (*pb.RegisterDIDResponse, error) {
    // Validates DID and public key
    // Creates account in database
    // Propagates DID to network
    // Returns DID information
}
```

### Get DID

```go
// Retrieve DID information
func (s *AccountServer) GetDID(ctx context.Context, req *pb.GetDIDRequest) (*pb.DIDResponse, error) {
    // Queries database for DID
    // Returns DID information
}
```

### List DIDs

```go
// List all DIDs with pagination
func (s *AccountServer) ListDIDs(ctx context.Context, req *pb.ListDIDsRequest) (*pb.ListDIDsResponse, error) {
    // Queries database with pagination
    // Returns list of DIDs
}
```

## DID Data Structure

```go
type Account struct {
    DIDAddress  string
    Address     common.Address
    PublicKey   string
    Balance     string
    Nonce       uint64
    CreatedAt   int64
    UpdatedAt   int64
    AccountType string
    Metadata    map[string]interface{}
}
```

## Usage

### Starting DID Server

```go
import "gossipnode/DID"

// Start DID gRPC server
err := DID.StartDIDServer(host, "localhost:15052", accountsClient)
```

### Registering a DID

```bash
# Via gRPC client
grpcurl -plaintext localhost:15052 proto.DIDService/RegisterDID <<EOF
{
  "did": "did:jmzk:0x1234...",
  "public_key": "0x5678..."
}
EOF
```

### Getting DID Information

```bash
# Via gRPC client
grpcurl -plaintext localhost:15052 proto.DIDService/GetDID <<EOF
{
  "did": "did:jmzk:0x1234..."
}
EOF
```

### Listing DIDs

```bash
# Via gRPC client
grpcurl -plaintext localhost:15052 proto.DIDService/ListDIDs <<EOF
{
  "limit": 10,
  "offset": 0
}
EOF
```

## Integration Points

### Database (DB_OPs)
- Stores DID documents in accounts database
- Retrieves DID information
- Manages account balances

### Messaging Module
- Propagates DID updates to network peers
- Handles DID message forwarding
- Manages DID synchronization

### CLI Module
- Provides CLI commands for DID operations
- `propagateDID`: Propagate DID to network
- `getDID`: Get DID document

### Explorer Module
- Displays DID information in explorer
- Lists all DIDs with pagination
- Shows DID statistics

## Configuration

DID server port can be configured via command-line flag:

```bash
./jmdn -did localhost:15052  # Default port
```

## Error Handling

The module includes comprehensive error handling:
- Invalid DID format errors
- Missing public key errors
- Database connection errors
- Network propagation errors

## Logging

DID operations are logged to:
- `logs/DID.log`: DID operations
- Loki (if enabled): Centralized logging

## Security

- DID format validation
- Public key verification
- Input sanitization
- Network propagation security

## Performance

- Efficient database queries
- Connection pooling
- Pagination for large datasets
- Concurrent DID operations

## Testing

Test files:
- `DID_test.go`: DID operation tests
- gRPC service tests
- Integration tests

## Future Enhancements

- Enhanced DID format support
- DID document versioning
- Advanced metadata support
- DID revocation
- Performance optimizations

