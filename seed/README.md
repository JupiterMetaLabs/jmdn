# Seed Module

## Overview

The Seed module provides seed node functionality for the JMZK network. It enables nodes to operate as seed nodes for peer discovery and registration, managing peer registries and providing peer information to network participants.

## Purpose

The Seed module enables:
- Seed node registration and configuration
- Peer discovery and registration
- Peer registry management
- Peer information distribution
- Stale peer cleanup

## Key Components

### 1. Seed Node Registration
**File:** `seed.go`

Main seed node functionality:
- `RegisterAsSeed`: Register node as seed node
- `NewPeerRegistry`: Create new peer registry
- `handleSeedRequest`: Handle general seed requests
- `handlePeerDiscoveryRequest`: Handle peer discovery requests
- `handleRegisterRequest`: Handle peer registration requests
- `runPeerCleanup`: Clean up stale peers

### 2. Seed Helper
**File:** `seedhelper.go`

Seed node helper functions:
- `SeedNode`: Seed node structure
- `GetAllPeers`: Get all peers from database
- `GetPeers`: Get peers with connection limit
- `ValidateMultiaddr`: Validate multiaddress
- `RegisterPeer`: Register peer with seed node
- `SetupStreamHandler`: Setup stream handler for registration

### 3. Peer Request/Response

Peer discovery structures:
- `PeerRequest`: Request for peer information
- `PeerResponse`: Response with peer information
- `PeerStatus`: Peer status information

## Key Functions

### Register as Seed

```go
// Register node as seed node
func RegisterAsSeed(node *config.Node) error {
    // Initialize peer store
    // Set up database connection
    // Set up stream handlers
    // Start peer cleanup routine
}
```

### Handle Peer Discovery

```go
// Handle peer discovery request
func handlePeerDiscoveryRequest(stream network.Stream, node *config.Node) {
    // Track peer in registry
    // Read peer request
    // Get peers from database
    // Send peer response
}
```

### Handle Peer Registration

```go
// Handle peer registration
func handleRegisterRequest(stream network.Stream, node *config.Node) {
    // Get peer information
    // Add peer to database
    // Send acknowledgment with peers
}
```

### Request Peers

```go
// Request peers from seed node
func RequestPeers(h host.Host, seedAddr string, maxPeers int, peerType string) ([]config.PeerInfo, error) {
    // Connect to seed node
    // Send peer request
    // Receive peer response
}
```

## Usage

### Register as Seed Node

```go
import "gossipnode/seed"

// Register node as seed node
err := seed.RegisterAsSeed(node)
if err != nil {
    log.Fatal(err)
}
```

### Request Peers

```go
// Request peers from seed node
peers, err := seed.RequestPeers(host, seedAddr, 20, "")
if err != nil {
    log.Error(err)
}

// Use peers
for _, peer := range peers {
    fmt.Printf("Peer: %s\n", peer.ID)
}
```

## Integration Points

### Node Module
- Uses libp2p host for seed operations
- Sets up stream handlers

### Database (DB_OPs)
- Stores peer information
- Retrieves peer lists

### Config Module
- Uses protocol IDs for seed operations
- Accesses peer configuration

## Configuration

Seed node configuration:
- `SeedProtocol`: Seed protocol ID
- `PeerDiscoveryProtocol`: Peer discovery protocol ID
- `RegisterProtocol`: Registration protocol ID
- `MaxTrackedPeers`: Maximum tracked peers (default: 1000)
- `PeerTTL`: Peer time-to-live (default: 3600 seconds)

## Error Handling

The module includes comprehensive error handling:
- Registration errors
- Peer discovery errors
- Database errors
- Connection errors

## Logging

Seed node operations are logged to:
- Application logs
- Peer registration logs
- Error logs

## Security

- Peer authentication
- Address validation
- Input sanitization
- Access control

## Performance

- Efficient peer registry management
- Fast peer discovery
- Optimized database queries
- Periodic cleanup

## Testing

Test files:
- `seed_test.go`: Seed node tests
- Integration tests
- Peer discovery tests

## Future Enhancements

- Enhanced peer discovery
- Improved peer management
- Better error recovery
- Performance optimizations
- Additional discovery mechanisms

