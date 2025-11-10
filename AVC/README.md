# AVC (Asyncronous Validation Consensus) Module

## Overview

The AVC module provides advanced verification and consensus mechanisms for the JMZK decentralized network. It implements Byzantine Fault Tolerance (BFT), buddy node selection, voting mechanisms, and cryptographic signing using BLS (Boneh-Lynn-Shacham) signatures.

## Purpose

The AVC module ensures secure, distributed consensus in the network by:
- Implementing BFT consensus protocols for block validation
- Selecting and managing buddy nodes for consensus participation
- Providing cryptographic signing and verification capabilities
- Managing voting mechanisms for network decisions

## Submodules

### 1. BFT (Byzantine Fault Tolerance)
Location: `AVC/BFT/`

Implements a Byzantine Fault Tolerant consensus protocol for validating blocks and reaching agreement among network participants.

**Key Features:**
- PREPARE and COMMIT phases for consensus
- Byzantine fault detection and handling
- GossipSub-based message passing
- gRPC communication with sequencer

**Key Files:**
- `bft/bft.go`: Main BFT consensus logic
- `bft/engine.go`: PREPARE and COMMIT phase execution
- `bft/byzantine.go`: Byzantine fault detection
- `bft/buddy_service.go`: gRPC server for buddy nodes
- `bft/sequencer_client.go`: Client for sequencer communication
- `network/libp2p_setup.go`: Network setup for BFT

**Documentation:** See `AVC/BFT/readme-bft.md` for detailed BFT implementation documentation.

### 2. BLS (Boneh-Lynn-Shacham) Signatures
Location: `AVC/BLS/`

Provides BLS signature generation and verification for cryptographic operations.

**Key Features:**
- BLS signature generation
- Signature aggregation
- Signature verification
- Router for signature routing

**Key Files:**
- `bls-sign/bls-sgin.go`: BLS signature implementation
- `Router/Router.go`: Signature routing logic

### 3. BuddyNodes
Location: `AVC/BuddyNodes/`

Manages buddy node selection, communication, and consensus participation.

**Key Features:**
- Buddy node selection and management
- Message passing between buddy nodes
- CRDT synchronization for buddy nodes
- PubSub integration for consensus messaging
- BLS signing and verification for buddy messages

**Key Components:**
- `MessagePassing/`: Handles communication between buddy nodes
- `CRDTSync/`: CRDT synchronization for buddy node state
- `ServiceLayer/`: Service layer for buddy node operations
- `DataLayer/`: Data layer for buddy node storage

**Key Files:**
- `MessagePassing/BuddyNodeStream.go`: Stream handling for buddy messages
- `MessagePassing/MessageListener.go`: Listens for buddy node messages
- `MessagePassing/CRDTSyncHandler.go`: Handles CRDT synchronization
- `ServiceLayer/Service.go`: Service layer implementation

### 4. NodeSelection
Location: `AVC/NodeSelection/`

Implements Verifiable Random Function (VRF) based node selection for choosing buddy nodes.

**Key Features:**
- VRF-based random node selection
- Node filtering and validation
- Selection service for buddy node committees
- Router for selection operations

**Key Files:**
- `pkg/selection/vrf.go`: VRF implementation
- `pkg/selection/service.go`: Selection service
- `pkg/selection/filter.go`: Node filtering logic
- `Router/Router.go`: Selection routing

**Documentation:** See `AVC/NodeSelection/README.md` for detailed documentation.

### 5. VoteModule
Location: `AVC/VoteModule/`

Handles voting mechanisms for network decisions and consensus.

**Key Features:**
- Vote validation
- Vote aggregation
- Consensus voting logic

**Key Files:**
- `vote_validation.go`: Vote validation logic

## Usage

### Initializing BFT Consensus

```go
import "gossipnode/AVC/BFT/bft"

// Create BFT instance
bftInstance := bft.NewBFT(config)

// Start consensus round
err := bftInstance.StartConsensus(blockData)
```

### Using Buddy Nodes

```go
import "gossipnode/AVC/BuddyNodes/MessagePassing"

// Create buddy node
buddyNode := MessagePassing.NewBuddyNode(host, buddies, responseHandler, pubsub)

// Handle buddy messages
buddyNode.HandleBuddyNodesMessageStream(host, stream)
```

### Node Selection

```go
import "gossipnode/AVC/NodeSelection/pkg/selection"

// Create selection service
service := selection.NewSelectionService()

// Select buddy nodes
buddies, err := service.SelectBuddies(count)
```

## Integration Points

- **Sequencer**: Uses AVC for consensus on block validation
- **PubSub**: AVC uses PubSub for message propagation
- **Messaging**: AVC integrates with messaging module for peer communication
- **Config**: AVC uses configuration constants for consensus parameters

## Configuration

Key configuration constants in `config/constants.go`:
- `MaxMainPeers`: Maximum number of main buddy nodes (default: 13)
- `MaxBackupPeers`: Maximum number of backup buddy nodes (default: 10)
- `ConsensusTimeout`: Timeout for consensus operations (default: 20 seconds)

## Security Considerations

- BFT protocol ensures consensus even with Byzantine faults
- BLS signatures provide cryptographic security for messages
- VRF ensures fair and verifiable node selection
- Byzantine fault detection prevents malicious behavior

## Performance

- BFT consensus requires 2/3 + 1 honest nodes
- Buddy node selection uses efficient VRF algorithms
- Message passing optimized for low latency
- CRDT synchronization ensures eventual consistency

## Testing

Each submodule includes test files:
- `AVC/BFT/bft/bft_test.go`: BFT consensus tests
- `AVC/BLS/Router/Router_test.go`: BLS router tests
- `AVC/BuddyNodes/MessagePassing/MessageListener_test.go`: Message listener tests
- `AVC/NodeSelection/pkg/selection/vrf_test.go`: VRF tests

## Future Enhancements

- Enhanced Byzantine fault tolerance mechanisms
- Improved BLS signature aggregation
- Advanced node selection algorithms
- Performance optimizations for large networks

