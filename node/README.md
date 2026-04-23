# Node Module

## Overview

The Node module provides libp2p node creation and peer management for the JMDT network. It handles node initialization, peer discovery, connection management, and heartbeat monitoring.

## Purpose

The Node module enables:
- Libp2p node creation and configuration
- Peer discovery and connection management
- Heartbeat monitoring for peer health
- Address management and multiaddress handling
- Stream handler setup for various protocols

## Key Components

### 1. Node Creation
**File:** `node.go`

Main node creation and configuration:
- `NewNode`: Create new libp2p node
- `loadOrCreatePrivateKey`: Load or create private key
- Node configuration with TCP and QUIC transports
- NAT traversal and relay support

### 2. Node Manager
**File:** `nodemanager.go`

Peer management and heartbeat monitoring:
- `NodeManager`: Manages peer connections
- `NewNodeManager`: Create new node manager
- `AddPeer`: Add peer to managed nodes
- `RemovePeer`: Remove peer from managed nodes
- `ListPeers`: List all managed peers
- `StartHeartbeat`: Start heartbeat monitoring
- `Shutdown`: Shutdown node manager

### 3. Peer Discovery
**File:** `discovery.go`

Peer discovery mechanisms:
- Peer discovery via seed nodes
- Address resolution
- Multiaddress handling

## Key Functions

### Create Node

```go
// Create new libp2p node
func NewNode() (*config.Node, error) {
    // Load or create private key
    // Create libp2p host
    // Set up transports (TCP, QUIC)
    // Configure NAT traversal
    // Set up stream handlers
    // Return node
}
```

### Add Peer

```go
// Add peer to managed nodes
func (nm *NodeManager) AddPeer(multiAddr string) error {
    // Parse multiaddress
    // Extract peer info
    // Connect to peer
    // Add to managed peers
}
```

### Start Heartbeat

```go
// Start heartbeat monitoring
func (nm *NodeManager) StartHeartbeat(interval time.Duration) {
    // Start ticker
    // Ping peers periodically
    // Update peer status
}
```

## Usage

### Create Node

```go
import "gossipnode/node"

// Create new node
n, err := node.NewNode()
if err != nil {
    log.Fatal(err)
}
defer n.Host.Close()

// Display node information
fmt.Printf("Node ID: %s\n", n.Host.ID())
for _, addr := range n.Host.Addrs() {
    fmt.Printf("Address: %s/p2p/%s\n", addr, n.Host.ID())
}
```

### Create Node Manager

```go
import "gossipnode/node"

// Create node manager
nodeManager, err := node.NewNodeManager(n)
if err != nil {
    log.Fatal(err)
}
defer nodeManager.Shutdown()

// Start heartbeat
nodeManager.StartHeartbeat(120 * time.Second)
```

### Add Peer

```go
// Add peer
err := nodeManager.AddPeer("/ip4/192.168.1.100/tcp/15000/p2p/QmPeerID")
if err != nil {
    log.Error(err)
}
```

### List Peers

```go
// List peers
peers := nodeManager.ListPeers()
for _, peer := range peers {
    fmt.Printf("Peer: %s - %s\n", peer.ID, peer.Multiaddr)
}
```

## Integration Points

### Config Module
- Uses node configuration
- Accesses protocol IDs
- Uses peer configuration

### Messaging Module
- Uses libp2p host for messaging
- Sets up stream handlers
- Manages peer connections

### CLI Module
- Uses node manager for peer management
- Accesses node information

### SeedNode Module
- Uses seed node for peer discovery
- Registers with seed node

## Configuration

Key configuration in `config/`:
- `PeerFile`: Path to peer.json file
- `MessageProtocol`: Message protocol ID
- `FileProtocol`: File transfer protocol ID
- `BroadcastProtocol`: Broadcast protocol ID
- `BlockPropagationProtocol`: Block propagation protocol ID
- `DIDPropagationProtocol`: DID propagation protocol ID

## Error Handling

The module includes comprehensive error handling:
- Node creation errors
- Peer connection errors
- Heartbeat failures
- Address resolution errors

## Logging

Node operations are logged to:
- `logs/node.log`: Node operations
- `logs/p2p-node.log`: P2P node operations
- Loki (if enabled): Centralized logging

## Security

- Private key management
- Peer authentication
- Connection encryption
- NAT traversal security

## Performance

- **Connection Pooling**: Efficient peer connections
- **Heartbeat Optimization**: Efficient heartbeat monitoring
- **Concurrent Operations**: Parallel peer operations
- **Stream Reuse**: Efficient stream management

## Testing

Test files:
- `node_test.go`: Node operation tests
- Integration tests
- Network simulation tests

## Future Enhancements

- Enhanced peer discovery
- Improved connection management
- Better heartbeat monitoring
- Performance optimizations
- Additional transport protocols

