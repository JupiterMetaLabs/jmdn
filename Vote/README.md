# Vote Module

## Overview

The Vote module provides voting mechanisms for consensus in the JMDT network. It handles vote submission, validation, and routing to listener nodes using consistent hashing.

## Purpose

The Vote module enables:
- Vote submission for block validation
- Vote validation using security checks
- Consistent hashing for listener node selection
- Vote routing to buddy nodes
- Retry logic for vote submission

## Key Components

### 1. Vote Trigger
**File:** `Trigger.go`

Main vote submission logic:
- `VoteTrigger`: Vote trigger structure
- `NewVoteTrigger`: Create new vote trigger
- `SetConsensusMessage`: Set consensus message
- `SubmitVote`: Submit vote for block validation
- `PickListner`: Pick listener node using consistent hashing
- `PickListnerWithOffset`: Pick listener node with offset

## Key Functions

### Submit Vote

```go
// Submit vote for block validation
func (vt *VoteTrigger) SubmitVote() error {
    // Validate block using security checks
    // Create vote (1 = accept, -1 = reject)
    // Pick listener node using consistent hashing
    // Send vote to listener node
    // Retry on failure
}
```

### Pick Listener Node

```go
// Pick listener node using consistent hashing
func (vt *VoteTrigger) PickListner(PeerID peer.ID) PubSubMessages.Buddy_PeerMultiaddr {
    // Hash peer ID
    // Select buddy node from map
    // Return buddy node
}
```

### Consistent Hashing

```go
// Consistent hashing for node selection
func consistentHashing(PeerID peer.ID, num int) int {
    // Hash peer ID
    // Return index in buddy nodes map
}
```

## Usage

### Create Vote Trigger

```go
import "gossipnode/Vote"

// Create vote trigger
vt := Vote.NewVoteTrigger()

// Set consensus message
vt.SetConsensusMessage(consensusMessage)
```

### Submit Vote

```go
// Submit vote
err := vt.SubmitVote()
if err != nil {
    log.Error(err)
}

// Get vote result
vote := vt.GetVote()
fmt.Printf("Vote: %d\n", vote.GetVote())
```

## Integration Points

### Security Module
- Uses security validation for block validation
- Validates blocks before voting

### AVC Module
- Integrates with buddy nodes
- Uses message passing for vote submission

### PubSub Module
- Uses PubSub for vote messaging
- Integrates with consensus messaging

### Config Module
- Uses protocol IDs for vote submission
- Accesses consensus message structures

## Configuration

Vote configuration:
- Vote values: 1 (accept), -1 (reject)
- Max retry attempts: 3
- Consistent hashing algorithm: SHA256

## Error Handling

The module includes comprehensive error handling:
- Invalid vote errors
- Listener node errors
- Message sending errors
- Retry logic for failures

## Logging

Vote operations are logged to:
- Application logs
- Vote submission logs
- Error logs

## Security

- Vote validation
- Block security checks
- Listener node authentication
- Message integrity

## Performance

- Efficient consistent hashing
- Fast vote submission
- Retry logic for reliability
- Concurrent vote processing

## Testing

Test files:
- `Vote_test.go`: Vote operation tests
- Integration tests
- Consensus tests

## Future Enhancements

- Enhanced vote validation
- Improved listener selection
- Better error recovery
- Performance optimizations
- Additional voting mechanisms

