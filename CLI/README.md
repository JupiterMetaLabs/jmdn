# CLI Module

## Overview

The CLI module provides a command-line interface and gRPC server for managing and interacting with the JMZK decentralized network node. It offers both interactive and programmatic access to node operations.

## Purpose

The CLI module enables:
- Interactive command-line interface for node management
- gRPC API for programmatic node control
- Peer management operations
- Messaging and communication commands
- Database state queries
- Network statistics and monitoring
- DID operations
- FastSync operations

## Key Components

### 1. Interactive CLI
**File:** `CLI.go`

Provides an interactive command-line interface with the following commands:

**Peer Management:**
- `addpeer <multiaddr>`: Add a peer to managed nodes
- `removepeer <peer_id>`: Remove a peer from managed nodes
- `listpeers`: List all managed peers
- `cleanpeers`: Clean offline peers
- `addrs`: Show node addresses

**Messaging:**
- `msg <peer_multiaddr> <message>`: Send message via libp2p
- `ygg <peer_multiaddr|ygg_ipv6> <message>`: Send message via Yggdrasil
- `broadcast <message>`: Broadcast message to all peers
- `file <peer_multiaddr> <filepath> <remote_filename>`: Send file to peer

**Network Operations:**
- `stats`: Show messaging statistics
- `dbstate`: Show database state
- `fastsync <peer_multiaddr>`: Fast sync with peer
- `syncinfo`: Show FastSync configuration

**DID Operations:**
- `propagateDID <did> <public_key> [balance]`: Propagate DID to network
- `getDID <did>`: Get DID document

**Discovery:**
- `listaliases`: List all peer aliases from seed node
- `discoverneighbors`: Discover and add neighbors from seed node
- `seednodeStats`: Check seed node connection and statistics
- `mempoolStats`: Show mempool statistics

**System:**
- `gethstatus`: Show gETH server status (chain ID, ports)
- `stopservice`: Stop the service and exit
- `help`: Show help message

### 2. gRPC Server
**File:** `GRPC_Server.go`

Provides gRPC service for programmatic access:

```protobuf
service CLIService {
    // Peer management
    rpc ListPeers(google.protobuf.Empty) returns (PeerList);
    rpc AddPeer(PeerRequest) returns (OperationResponse);
    rpc RemovePeer(PeerRequest) returns (OperationResponse);
    rpc CleanPeers(google.protobuf.Empty) returns (CleanPeersResponse);
    
    // Messaging
    rpc SendMessage(MessageRequest) returns (OperationResponse);
    rpc SendYggdrasilMessage(MessageRequest) returns (OperationResponse);
    rpc SendFile(FileRequest) returns (OperationResponse);
    rpc BroadcastMessage(MessageRequest) returns (OperationResponse);
    rpc GetMessageStats(google.protobuf.Empty) returns (MessageStats);
    
    // DID Operations
    rpc GetDID(DIDRequest) returns (DIDDocument);
    rpc PropagateDID(DIDPropagationRequest) returns (OperationResponse);
    
    // Database Operations
    rpc FastSync(PeerRequest) returns (SyncStats);
    rpc GetDatabaseState(google.protobuf.Empty) returns (DatabaseStates);
    
    // Node Operations
    rpc ReturnAddrs(google.protobuf.Empty) returns (Addrs);
    rpc GetSyncInfo(google.protobuf.Empty) returns (SyncInfo);
    rpc GetGethStatus(google.protobuf.Empty) returns (GethStatus);
    
    // Discovery
    rpc DiscoverNeighbors(google.protobuf.Empty) returns (OperationResponse);
    rpc ListAliases(google.protobuf.Empty) returns (AliasList);
}
```

### 3. gRPC Client
**File:** `client.go`

Client library for connecting to CLI gRPC server from external applications.

### 4. Command Handler
**File:** `CLI.go`

`CommandHandler` struct manages command execution with dependencies:
- Node instance
- NodeManager for peer management
- FastSync service
- Database connections
- Chain configuration

## Usage

### Interactive Mode

Start the node with CLI enabled (default):

```bash
./jmdn
```

Then use commands interactively:

```
>>> listpeers
>>> addpeer /ip4/192.168.1.100/tcp/15000/p2p/Qm...
>>> msg /ip4/192.168.1.100/tcp/15000/p2p/Qm... "Hello"
>>> stats
>>> dbstate
```

### Command-Line Mode

Execute commands directly via command-line flag:

```bash
# List peers
./jmdn -cmd listpeers

# Add peer
./jmdn -cmd addpeer /ip4/192.168.1.100/tcp/15000/p2p/Qm...

# Get DID
./jmdn -cmd getdid did:jmzk:0x1234...

# Fast sync
./jmdn -cmd fastsync /ip4/192.168.1.100/tcp/15000/p2p/Qm...
```

### gRPC API

Connect to gRPC server (default port: 15053):

```go
import "gossipnode/CLI"

client, err := cli.NewClient("localhost:15053")
if err != nil {
    log.Fatal(err)
}
defer client.Close()

// List peers
peers, err := client.ListPeers()

// Send message
resp, err := client.SendMessage(peerAddr, "Hello")

// Get database state
state, err := client.GetDatabaseState()
```

## Configuration

CLI server port can be configured via command-line flag:

```bash
./jmdn -cli 15053  # Default port
```

## Integration Points

### Node Module
- Uses `node.NodeManager` for peer management
- Accesses `node.Node` for network operations

### FastSync Module
- Uses `fastsync.FastSync` for synchronization
- Manages sync operations

### Database (DB_OPs)
- Queries database state
- Accesses account information

### Messaging Module
- Sends messages via libp2p
- Broadcasts to network

### DID Module
- Propagates DID documents
- Retrieves DID information

## Error Handling

The CLI includes comprehensive error handling:
- Invalid command errors
- Connection failures
- Peer management errors
- Database errors
- Network errors

## Logging

CLI operations are logged to:
- Console output (interactive mode)
- gRPC server logs
- Application logs

## Security

- Input validation for all commands
- Peer address verification
- Message sanitization
- Access control via gRPC

## Performance

- Non-blocking command execution
- Concurrent peer operations
- Efficient database queries
- Connection pooling

## Testing

Test files:
- `CLI_test.go`: CLI command tests
- gRPC service tests
- Integration tests

## Example Commands

### Peer Management
```bash
# Add a peer
>>> addpeer /ip4/192.168.1.100/tcp/15000/p2p/QmPeerID

# List all peers
>>> listpeers

# Remove a peer
>>> removepeer QmPeerID

# Clean offline peers
>>> cleanpeers
```

### Messaging
```bash
# Send message via libp2p
>>> msg /ip4/192.168.1.100/tcp/15000/p2p/QmPeerID "Hello World"

# Send message via Yggdrasil
>>> ygg 200:abcd::1234 "Hello via Yggdrasil"

# Broadcast message
>>> broadcast "Network announcement"
```

### Network Information
```bash
# Show statistics
>>> stats

# Show database state
>>> dbstate

# Show node addresses
>>> addrs

# Show gETH status
>>> gethstatus
```

### DID Operations
```bash
# Propagate DID
>>> propagateDID did:jmzk:0x1234... 0x5678... "1000000000000000000"

# Get DID
>>> getdid did:jmzk:0x1234...
```

### Synchronization
```bash
# Fast sync with peer
>>> fastsync /ip4/192.168.1.100/tcp/15000/p2p/QmPeerID

# Show sync info
>>> syncinfo
```

## Future Enhancements

- Enhanced command history
- Command aliases
- Scripting support
- Advanced filtering options
- Performance monitoring commands
- Network diagnostics

