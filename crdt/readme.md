# CRDT Module

## Overview

The CRDT (Conflict-Free Replicated Data Types) module provides distributed data structures that ensure eventual consistency across network nodes. It supports offline/online synchronization scenarios where nodes can operate independently and sync when reconnected.

## Purpose

The CRDT module enables:
- Conflict-free data replication across network nodes
- Offline/online synchronization capabilities
- Eventual consistency guarantees
- Deterministic conflict resolution
- Efficient data synchronization using HashMaps and IBLT

## Key Features

### ✅ **Offline/Online Sync**
- Nodes can go offline and continue operating independently
- Operations are stored for replay when nodes reconnect
- Automatic synchronization on reconnection
- Eventual consistency across all nodes

### ✅ **CRDT Types**
- **LWW-Set (Last-Writer-Wins Set)**: For concurrent add/remove operations
- **G-Counter**: Growing counter for metrics and statistics
- **Vector Clocks**: For causality tracking between distributed events

### ✅ **Synchronization Mechanisms**
- **HashMap-based Diff**: Efficient identification of missing data
- **IBLT (Invertible Bloom Lookup Table)**: Set reconciliation for large datasets
- **Batch Processing**: Optimized data transfer in batches

## Key Components

### 1. Core CRDT Engine
**File:** `crdt.go`

Main CRDT implementation with:
- LWW-Set operations (Add, Remove, Contains)
- G-Counter operations (Increment, Decrement, Get)
- Vector clock management
- CRDT merging logic

### 2. Database Operations
**File:** `CRDT_DBOps.go`

Database integration for CRDT persistence:
- Store CRDT state in database
- Load CRDT state from database
- Transaction support for atomic operations

### 3. Memory Store
**File:** `MemoryStore.go`

In-memory storage for CRDT operations:
- Fast in-memory operations
- Efficient data structures
- Memory management

### 4. HashMap
**File:** `HashMap/`

HashMap implementation for data reconciliation:
- Efficient diff calculation
- Missing data identification
- Batch synchronization

### 5. IBLT (Invertible Bloom Lookup Table)
**File:** `IBLT/`

IBLT implementation for set reconciliation:
- Efficient set difference calculation
- Large dataset handling
- Network-efficient synchronization

### 6. Helper Functions
**File:** `helper.go`

Utility functions for CRDT operations:
- Serialization/deserialization
- Data conversion
- Validation

## CRDT Types

### LWW-Set (Last-Writer-Wins Set)

Handles concurrent add/remove operations with timestamp-based resolution:

```go
// Add element
crdt.LWWAdd("node1", "users", "alice", vectorClock)

// Remove element
crdt.LWWRemove("node1", "users", "alice", vectorClock)

// Check membership
exists := crdt.LWWContains("users", "alice")

// Get all elements
elements := crdt.LWWGetAll("users")
```

### G-Counter (Growing Counter)

Monotonic counter that only increases:

```go
// Increment counter
crdt.GCounterIncrement("node1", "likes", vectorClock)

// Get counter value
value := crdt.GCounterGet("likes")
```

### Vector Clocks

Track causality between distributed events:

```go
// Create vector clock
vc := NewVectorClock("node1")

// Increment for node
vc.Increment("node1")

// Compare vector clocks
happensBefore := vc1.HappensBefore(vc2)
```

## Usage

### Basic CRDT Operations

```go
import "gossipnode/crdt"

// Create CRDT engine
engine := crdt.NewEngine("node1")

// Add to LWW-Set
engine.LWWAdd("node1", "users", "alice", vectorClock)

// Increment counter
engine.GCounterIncrement("node1", "likes", vectorClock)

// Merge with another engine
engine.Merge(otherEngine)
```

### Offline/Online Sync

```go
// Create persistent engine
node1 := crdt.NewPersistentEngine("node1", 10*1024*1024)

// Normal operations
node1.LWWAdd("node1", "users", "alice", vectorClock)

// Node goes offline
node1.GoOffline()

// Other nodes continue operating
node2.LWWAdd("node2", "users", "bob", vectorClock)

// Node comes back online
node1.ComeOnline()

// Sync with other nodes
syncPersistentNodes(node1, node2, "node1", "node2")

// Both nodes now have: users=[alice, bob]
```

### Database Integration

```go
// Store CRDT state
err := crdt.StoreCRDTState(db, "users", crdtState)

// Load CRDT state
crdtState, err := crdt.LoadCRDTState(db, "users")

// Merge and persist
err := crdt.MergeAndPersist(db, "users", newState)
```

## Synchronization Process

### Phase 1: State Exchange
Nodes exchange current CRDT states and vector clocks.

### Phase 2: Diff Calculation
Use HashMaps or IBLT to identify missing operations.

### Phase 3: Operation Transfer
Transfer missing operations in optimized batches.

### Phase 4: Merge and Apply
Merge operations using CRDT semantics and apply results.

## Integration Points

### FastSync Module
- Uses CRDT for blockchain state synchronization
- Efficient diff calculation for missing blocks

### BuddyNodes Module
- CRDT synchronization for buddy node state
- Consensus state replication

### Database (DB_OPs)
- Persists CRDT state
- Loads CRDT state for recovery

## Configuration

Key configuration:
- Memory limits for in-memory operations
- Batch sizes for synchronization
- Timeout values for sync operations

## Performance

- **Efficient Merging**: O(n) complexity for CRDT merge
- **Network Efficient**: Only missing data is transferred
- **Memory Efficient**: Configurable memory limits
- **Scalable**: Works with any number of nodes

## Testing

Test files:
- `crdt_test.go`: Core CRDT tests
- `HashMap/`: HashMap tests
- `IBLT/`: IBLT tests

**Test Scenarios:**
- `TestOfflineOnlineSync`: Complete offline/online cycle
- `TestLWWSet`: LWW-Set operations
- `TestGCounter`: G-Counter operations
- `TestVectorClocks`: Vector clock causality

## Limitations & Future Enhancements

### Current Limitations
1. **Memory-Only**: Operations stored in memory (not persisted to disk)
2. **Simple Sync**: No sophisticated sync protocols (like Merkle trees)
3. **No Compression**: All operations stored individually
4. **No Garbage Collection**: Operation logs grow indefinitely

### Future Enhancements
1. **Disk Persistence**: Store operations to disk for true offline capability
2. **Incremental Sync**: Only sync changed data since last sync
3. **Compression**: Compress operation logs to save space
4. **Garbage Collection**: Clean up old operations safely
5. **Network Protocols**: Add real network communication
6. **Conflict Resolution UI**: Show users what conflicts were resolved

## Real-World Applications

This offline/online capability enables:
- **Mobile Apps**: Work offline, sync when connected
- **Distributed Systems**: Handle network partitions gracefully
- **Edge Computing**: Sync with central systems when available
- **Collaborative Tools**: Multiple users working independently

## Documentation

See `crdt/readme.md` for detailed documentation on offline/online sync capabilities.

## Conclusion

The CRDT system demonstrates **true distributed system capabilities** with offline/online sync. This provides a production-ready distributed data structure that can handle real-world network conditions and node failures.

The key insight is that **CRDTs make offline/online sync natural** - there's no complex conflict resolution needed because the data structures themselves are designed to handle concurrent modifications gracefully.
