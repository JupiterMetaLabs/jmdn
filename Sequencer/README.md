# Sequencer Module

## Overview

The Sequencer module provides consensus orchestration for the JMZK network. It manages buddy node selection, consensus initiation, vote collection, and BFT consensus execution.

## Purpose

The Sequencer module enables:
- Consensus orchestration for block validation
- Buddy node selection and management
- Vote collection from buddy nodes
- BFT consensus execution
- PubSub channel management for consensus
- CRDT synchronization for buddy nodes

## Key Components

### 1. Consensus
**File:** `Consensus.go`

Main consensus orchestration:
- `NewConsensus`: Create new consensus instance
- `Start`: Start consensus process
- `QueryBuddyNodes`: Query and select buddy nodes
- `RequestSubscriptionPermission`: Request subscription from buddy nodes
- `CreatePubSubChannel`: Create PubSub channel for consensus

### 2. Communication
**File:** `Communication.go`

Communication with buddy nodes:
- `AskForSubscription`: Ask buddy nodes to subscribe to consensus channel
- `RequestVoteResultsFromBuddies`: Request vote results from buddy nodes
- `StartBFTConsensus`: Start BFT consensus process

### 3. Router
**File:** `Router/Router.go`

Routing for consensus operations:
- Message routing
- Vote aggregation
- Response handling

### 4. Metadata
**File:** `Metadata/Metadata.go`

Consensus metadata management:
- Block metadata
- Vote metadata
- Consensus state

### 5. Triggers
**File:** `Triggers/`

Consensus triggers:
- `Triggers.go`: Trigger management
- `Maps.go`: Vote result maps

## Key Functions

### Start Consensus

```go
// Start consensus process
func (c *Consensus) Start(block *config.ZKBlock) error {
    // Query buddy nodes
    // Create PubSub channel
    // Request subscriptions
    // Collect votes
    // Execute BFT consensus
}
```

### Query Buddy Nodes

```go
// Query and select buddy nodes
func (c *Consensus) QueryBuddyNodes() error {
    // Use VRF to select buddy nodes
    // Connect to buddy nodes
    // Populate peer list
}
```

### Request Vote Results

```go
// Request vote results from buddy nodes
func RequestVoteResultsFromBuddies() error {
    // Request votes from all buddy nodes
    // Collect vote results
    // Aggregate votes
}
```

## Usage

### Create Consensus Instance

```go
import "gossipnode/Sequencer"

// Create consensus instance
peerList := Sequencer.PeerList{
    MainPeers:   []peer.ID{},
    BackupPeers: []peer.ID{},
}
consensus := Sequencer.NewConsensus(peerList, host)
```

### Start Consensus

```go
// Start consensus for block
err := consensus.Start(block)
if err != nil {
    log.Error(err)
}
```

## Integration Points

### AVC Module
- Uses BFT for consensus execution
- Integrates with buddy nodes
- Uses node selection for buddy selection

### PubSub Module
- Uses PubSub for consensus messaging
- Creates private channels for consensus
- Manages subscriptions

### Messaging Module
- Uses messaging for vote collection
- Integrates with direct messaging

### Block Module
- Initiates consensus for block validation
- Receives consensus results

## Configuration

Key configuration in `config/`:
- `MaxMainPeers`: Maximum main buddy nodes (default: 13)
- `MaxBackupPeers`: Maximum backup buddy nodes (default: 10)
- `ConsensusTimeout`: Consensus timeout (default: 20 seconds)
- `PubSub_ConsensusChannel`: Consensus channel name
- `Pubsub_CRDTSync`: CRDT sync channel name

## Error Handling

The module includes comprehensive error handling:
- Buddy node selection errors
- Subscription errors
- Vote collection errors
- BFT consensus errors

## Logging

Sequencer operations are logged to:
- Application logs
- Consensus logs
- Error logs

## Security

- Buddy node authentication
- Vote signature verification
- BFT consensus security
- Private channel access control

## Performance

- Efficient buddy node selection
- Concurrent vote collection
- Optimized BFT execution
- PubSub channel management

## Testing

Test files:
- `Sequencer_test.go`: Sequencer operation tests
- Integration tests
- Consensus simulation tests

## Future Enhancements

- Enhanced consensus algorithms
- Improved buddy node selection
- Better error recovery
- Performance optimizations
- Additional consensus mechanisms

