# NodeSelection Package

Production-ready VRF-based buddy node selection for distributed networks.

## Features

- ✅ **VRF Buddy Selection** - Cryptographically secure, verifiable buddy selection
- ✅ **gRPC Integration** - Connect to peer directory for node discovery
- ✅ **Mnemonic Support** - Generate keys from BIP39 mnemonic phrases
- ✅ **Production Ready** - Comprehensive error handling and validation

## Quick Start

### 1. Import the Package

```go
import "gossipnode/AVC/NodeSelection/pkg/selection"
```

### 2. Generate Keys from Mnemonic

```go
import (
    "crypto/ed25519"
    "gossipnode/AVC/NodeSelection/pkg/selection"
)

// Generate keys from mnemonic
publicKey, privateKey, err := selection.GenerateKeysFromMnemonic("your twelve word mnemonic phrase here")
if err != nil {
    log.Fatal(err)
}
```

### 3. Select Buddy Nodes

```go
import "context"

ctx := context.Background()

// Select buddies from peer directory
buddies, err := selection.GetBuddyNodes(
    ctx,
    "your-node-id",
    privateKey,
    []byte("network-salt"),
    "peer-directory:50051",
    3, // number of buddies
)
if err != nil {
    log.Fatal(err)
}

// Use the selected buddies
for _, buddy := range buddies {
    fmt.Printf("Buddy: %s at %s\n", buddy.Node.PeerId, buddy.Node.Address)
}
```

## Complete Example

```go
package main

import (
    "context"
    "fmt"
    "log"

    "gossipnode/AVC/NodeSelection/pkg/selection"
)

func main() {
    fmt.Println("🚀 NodeSelection Package Example")
    fmt.Println("=================================")

    // Generate keys from mnemonic
    publicKey, privateKey, err := selection.GenerateKeysFromMnemonic("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
    if err != nil {
        log.Fatalf("❌ Failed to generate keys from mnemonic: %v", err)
    }

    fmt.Printf("✅ Generated keys from mnemonic\n")
    fmt.Printf("   Public Key: %x...\n", publicKey[:8])
    fmt.Println()

    // Configuration
    nodeID := "my-node-001"
    networkSalt := []byte("my-network-salt-2024")
    peerDirAddress := "localhost:50051"
    numBuddies := 3

    fmt.Printf("🔑 Node ID: %s\n", nodeID)
    fmt.Printf("📡 Peer Directory: %s\n", peerDirAddress)
    fmt.Printf("🎲 Selecting: %d buddies\n", numBuddies)
    fmt.Println()

    // Select buddy nodes
    ctx := context.Background()
    buddies, err := selection.GetBuddyNodes(
        ctx,
        nodeID,
        privateKey,
        networkSalt,
        peerDirAddress,
        numBuddies,
    )
    if err != nil {
        log.Fatalf("❌ Failed to select buddies: %v", err)
    }

    // Display results
    fmt.Printf("✅ Successfully selected %d buddies!\n\n", len(buddies))
    fmt.Println("Selected Buddies:")
    fmt.Println("=================")
    for i, buddy := range buddies {
        fmt.Printf("%2d. %-20s | %s | Score: %.2f | ASN: %d\n",
            i+1,
            buddy.Node.PeerId,
            buddy.Node.Address,
            buddy.Node.SelectionScore,
            buddy.Node.ASN,
        )
    }

    fmt.Println("\n✨ Example completed successfully!")
}
```

## API Reference

### Core Functions

#### `GetBuddyNodes(ctx, nodeID, privateKey, networkSalt, peerDirAddress, numBuddies)`
Selects buddy nodes from a peer directory service.

**Parameters:**
- `ctx` - Context for cancellation/timeout
- `nodeID` - Your node's unique identifier
- `privateKey` - Ed25519 private key for VRF operations
- `networkSalt` - Network-specific salt for VRF calculations
- `peerDirAddress` - Address of peer directory service ("host:port")
- `numBuddies` - Number of buddy nodes to select

**Returns:** `([]*BuddyNode, error)`

#### `GetBuddyNodesWithNodes(ctx, nodeID, privateKey, networkSalt, nodes, numBuddies)`
Selects buddy nodes from a provided list of nodes (useful for testing).

**Parameters:**
- `ctx` - Context for cancellation/timeout
- `nodeID` - Your node's unique identifier
- `privateKey` - Ed25519 private key for VRF operations
- `networkSalt` - Network-specific salt for VRF calculations
- `nodes` - Slice of available nodes
- `numBuddies` - Number of buddy nodes to select

**Returns:** `([]*BuddyNode, error)`

#### `GenerateKeysFromMnemonic(mnemonic)`
Generates Ed25519 keys from a BIP39 mnemonic phrase.

**Parameters:**
- `mnemonic` - BIP39 mnemonic phrase (12 or 24 words)

**Returns:** `(ed25519.PublicKey, ed25519.PrivateKey, error)`

### Data Types

#### `BuddyNode`
```go
type BuddyNode struct {
    Node  *Node
    Proof []byte
}
```

#### `Node`
```go
type Node struct {
    PeerId       string
    Alias        string
    Region       string
    ASN          int
    IPPrefix     string
    Reachability string
    RTTBucket    string
    RTTMs        int
    LastSeen     time.Time
    Multiaddrs   []string
    // Legacy fields for backward compatibility
    ID              string
    PublicKey       ed25519.PublicKey
    Address         string
    ReputationScore float64
    SelectionScore  float64
    LastSelectedRound uint64
    IsActive        bool
    Capacity        int
}
```

### Required Dependencies

The following dependencies are automatically included:

- `github.com/tyler-smith/go-bip39` - BIP39 mnemonic support
- `github.com/yahoo/coname/vrf` - VRF operations

## Usage Patterns

### With Mock Data (Testing)

```go
// Create mock nodes for testing
mockNodes := []selection.Node{
    {
        Node: types.Node{
            PeerId: "node-1",
            Address: "/ip4/127.0.0.1/tcp/8000",
            ASN: 1001,
            ReputationScore: 0.8,
            IsActive: true,
        },
        SelectionScore: 0.7,
    },
    // ... more nodes
}

buddies, err := selection.GetBuddyNodesWithNodes(
    ctx, nodeID, privateKey, networkSalt, mockNodes, 3,
)
```

### With Real Peer Directory

```go
buddies, err := selection.GetBuddyNodes(
    ctx, nodeID, privateKey, networkSalt, "peer-dir:50051", 3,
)
```

## Error Handling

```go
buddies, err := selection.GetBuddyNodes(ctx, nodeID, privateKey, networkSalt, peerDir, 3)
if err != nil {
    switch err {
    case selection.ErrNoPeersAvailable:
        log.Println("No peers available for selection")
    case selection.ErrVRFGenerationFailed:
        log.Println("VRF generation failed")
    default:
        log.Printf("Selection error: %v", err)
    }
    return
}
```

## Troubleshooting

### Common Issues

1. **"No peers available"** - Ensure peer directory has registered nodes
2. **"Connection refused"** - Check peer directory service is running
3. **"Invalid mnemonic"** - Verify mnemonic phrase is valid BIP39
4. **"VRF generation failed"** - Check private key and network salt

### Build Issues

```bash
# Install dependencies
go mod tidy

# Verify build
go build ./AVC/NodeSelection/...
```

## Examples

See the `examples/` directory for complete working examples:

- `examples/simple_usage.go` - Basic usage
- `examples/use_nodeselection.go` - Multiple usage patterns
- `examples/real_world_usage.go` - Real application integration