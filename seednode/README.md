# SeedNode Module

## Overview

The SeedNode module provides peer discovery and registration services for the JMZK network. It enables nodes to register with seed nodes, discover neighbors, and manage peer relationships.

## Purpose

The SeedNode module enables:
- Peer registration with seed nodes
- Peer discovery and neighbor management
- Public IP detection
- Peer alias management
- Heartbeat monitoring
- VRS (V, R, S) signature generation for peer records

## Key Components

### 1. SeedNode Client
**File:** `seednode.go`

Client for communicating with seed nodes:
- `NewClient`: Create new seed node client
- `RegisterPeer`: Register peer with seed node
- `RegisterPeerWithAlias`: Register peer with alias
- `SendHeartbeat`: Send heartbeat to seed node
- `DiscoverAndAddNeighbors`: Discover and add neighbors
- `GetNeighbors`: Get neighbor list
- `AddNeighbor`: Add neighbor relationship

### 2. Signature Generation
**File:** `signature.go`

VRS signature generation for peer records:
- `SignPeerRecord`: Sign peer record with VRS signature
- `SignAlias`: Sign alias with VRS signature
- `SignHeartbeat`: Sign heartbeat with VRS signature

### 3. Protocol Buffers
**File:** `proto/seednode.proto`

gRPC service definitions:
- `PeerDirectory`: Main service interface
- `RegisterPeer`: Register peer request/response
- `RegisterPeerWithAlias`: Register peer with alias
- `SendHeartbeat`: Heartbeat request/response
- `GetNeighbors`: Get neighbors request/response
- `AddNeighbor`: Add neighbor request/response

## Key Functions

### Register Peer

```go
// Register peer with seed node
func (c *Client) RegisterPeer(h host.Host) error {
    // Get public IP
    // Create peer record
    // Sign peer record
    // Register with seed node
}
```

### Register Peer with Alias

```go
// Register peer with alias
func (c *Client) RegisterPeerWithAlias(h host.Host, alias string) error {
    // Get public IP
    // Create peer record
    // Create alias
    // Sign both
    // Register with seed node
}
```

### Discover Neighbors

```go
// Discover and add neighbors
func (c *Client) DiscoverAndAddNeighbors(h host.Host, nodeManager interface{}) error {
    // Get neighbors from seed node
    // Add neighbors to node manager
    // Connect to neighbors
}
```

## Usage

### Create SeedNode Client

```go
import "gossipnode/seednode"

// Create seed node client
client, err := seednode.NewClient("localhost:9090")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

### Register Peer

```go
// Register peer
err := client.RegisterPeer(host)
if err != nil {
    log.Error(err)
}
```

### Register Peer with Alias

```go
// Register peer with alias
err := client.RegisterPeerWithAlias(host, "my-node")
if err != nil {
    log.Error(err)
}
```

### Discover Neighbors

```go
// Discover and add neighbors
err := client.DiscoverAndAddNeighbors(host, nodeManager)
if err != nil {
    log.Error(err)
}
```

## Integration Points

### Node Module
- Uses libp2p host for peer information
- Integrates with node manager

### Config Module
- Uses seed node URL configuration
- Accesses protocol IDs

### CLI Module
- Provides CLI commands for seed node operations
- `seednodeStats`: Check seed node connection

## Configuration

Seed node URL can be configured via command-line flag:

```bash
./jmdn -seednode localhost:9090
```

## Error Handling

The module includes comprehensive error handling:
- Connection failures
- Registration errors
- Signature errors
- Neighbor discovery errors

## Logging

SeedNode operations are logged to:
- Application logs
- Seed node connection logs
- Error logs

## Security

- VRS signature generation for peer records
- Public key verification
- Peer authentication
- Secure communication via gRPC

## Performance

- Efficient peer discovery
- Concurrent neighbor operations
- Connection pooling
- Heartbeat optimization

## Testing

Test files:
- `seednode_test.go`: SeedNode operation tests
- Integration tests
- Network simulation tests

## Future Enhancements

- Enhanced peer discovery
- Improved neighbor management
- Better error recovery
- Performance optimizations
- Additional discovery mechanisms

