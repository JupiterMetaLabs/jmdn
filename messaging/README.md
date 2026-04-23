# Messaging Module

## Overview

The Messaging module provides peer-to-peer communication capabilities for the JMDT network. It handles message propagation, block broadcasting, DID propagation, and direct messaging via libp2p and Yggdrasil.

## Purpose

The Messaging module enables:
- Peer-to-peer message passing
- Block propagation across the network
- DID document propagation
- Broadcast messaging
- Direct messaging via libp2p and Yggdrasil
- Message deduplication using bloom filters

## Key Components

### 1. Message Handling
**File:** `message.go`

Core messaging functions:
- `HandleMessageStream`: Handle incoming message streams
- `SendMessage`: Send message to peer
- `SetHostInstance`: Set libp2p host instance
- `GetHostInstance`: Get libp2p host instance

### 2. Block Propagation
**File:** `blockPropagation.go`

Block propagation functionality:
- `InitBlockPropagation`: Initialize block propagation
- `PropagateZKBlock`: Propagate ZK-verified block
- `HandleBlockStream`: Handle incoming block streams
- `forwardBlock`: Forward block to peers
- Bloom filter for deduplication

### 3. DID Propagation
**File:** `DIDPropagation.go`

DID document propagation:
- `InitDIDPropagation`: Initialize DID propagation
- `PropagateDID`: Propagate DID document
- `HandleDIDStream`: Handle incoming DID streams
- `forwardDID`: Forward DID to peers
- Bloom filter for deduplication

### 4. Broadcast Messaging
**File:** `broadcast.go`

Broadcast messaging functionality:
- `HandleBroadcastStream`: Handle incoming broadcast streams
- `BroadcastMessage`: Broadcast message to all peers
- Concurrent message sending

### 5. Direct Messaging
**File:** `directMSG/directMSG.go`

Yggdrasil direct messaging:
- `SendYggdrasilMessage`: Send message via Yggdrasil
- `StartYggdrasilListener`: Start Yggdrasil listener
- Yggdrasil address management

### 6. Block Processing
**File:** `BlockProcessing/BlockProcessing.go`

Block processing integration:
- `ProcessBlockTransactions`: Process block transactions
- Transaction validation
- Account updates

## Key Functions

### Send Message

```go
// Send message to peer
func SendMessage(host host.Host, peerMultiaddr string, message string) error {
    // Parse peer address
    // Open stream
    // Send message
    // Close stream
}
```

### Propagate Block

```go
// Propagate block to network
func PropagateZKBlock(h host.Host, block *PubSubMessages.ConsensusMessage) error {
    // Create block message
    // Send to all peers
    // Use bloom filter for deduplication
}
```

### Propagate DID

```go
// Propagate DID document
func PropagateDID(h host.Host, doc *DB_OPs.Account) error {
    // Create DID message
    // Send to all peers
    // Use bloom filter for deduplication
}
```

### Broadcast Message

```go
// Broadcast message to all peers
func BroadcastMessage(host host.Host, message string) error {
    // Send to all connected peers
    // Concurrent sending
}
```

## Usage

### Initialize Messaging

```go
import "gossipnode/messaging"

// Set host instance
messaging.SetHostInstance(host)

// Initialize block propagation
err := messaging.InitBlockPropagation(host)
if err != nil {
    log.Fatal(err)
}

// Initialize DID propagation
err := messaging.InitDIDPropagation(accountsClient)
if err != nil {
    log.Fatal(err)
}
```

### Send Message

```go
// Send message to peer
err := messaging.SendMessage(host, peerMultiaddr, "Hello")
if err != nil {
    log.Error(err)
}
```

### Propagate Block

```go
// Propagate block
err := messaging.PropagateZKBlock(host, block)
if err != nil {
    log.Error(err)
}
```

### Propagate DID

```go
// Propagate DID
err := messaging.PropagateDID(host, account)
if err != nil {
    log.Error(err)
}
```

### Broadcast Message

```go
// Broadcast message
err := messaging.BroadcastMessage(host, "Network announcement")
if err != nil {
    log.Error(err)
}
```

## Integration Points

### Node Module
- Uses libp2p host for messaging
- Sets up stream handlers
- Manages peer connections

### Block Module
- Uses block propagation for block distribution
- Integrates with block processing

### DID Module
- Uses DID propagation for DID distribution
- Integrates with DID management

### PubSub Module
- Uses PubSub for message routing
- Integrates with gossip protocol

### Metrics Module
- Tracks message statistics
- Monitors message throughput

## Configuration

Key configuration in `config/`:
- `MessageProtocol`: Message protocol ID
- `BlockPropagationProtocol`: Block propagation protocol ID
- `DIDPropagationProtocol`: DID propagation protocol ID
- `BroadcastProtocol`: Broadcast protocol ID
- `MaxAccountHops`: Maximum hops for DID propagation

## Error Handling

The module includes comprehensive error handling:
- Connection failures
- Timeout errors
- Message serialization errors
- Peer disconnection errors

## Logging

Messaging operations are logged to:
- Application logs
- Message statistics
- Error logs

## Security

- Message validation
- Peer authentication
- Input sanitization
- Rate limiting (via bloom filters)

## Performance

- **Concurrent Sending**: Parallel message sending
- **Bloom Filters**: Efficient message deduplication
- **Stream Reuse**: Efficient stream management
- **Batch Operations**: Optimized batch sending

## Testing

Test files:
- `messaging_test.go`: Messaging operation tests
- Integration tests
- Network simulation tests

## Future Enhancements

- Enhanced message routing
- Improved deduplication
- Better error recovery
- Performance optimizations
- Additional messaging protocols

