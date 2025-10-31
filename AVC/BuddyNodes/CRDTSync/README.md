# CRDT Sync Integration Guide

This guide explains how to integrate the CRDT synchronization functionality into your AVC BuddyNodes system.

## Files Created

The following files have been created in `AVC/BuddyNodes/CRDTSync/`:

1. **`types.go`** - Message types and data structures for CRDT sync
2. **`service.go`** - Core CRDT synchronization service
3. **`consensus_integration.go`** - Integration manager for consensus operations

## Integration Steps

### 1. Import the CRDTSync Package

Add this import to your consensus files:

```go
import "gossipnode/AVC/BuddyNodes/CRDTSync"
```

### 2. Initialize CRDT Sync in Consensus

In your `Sequencer/Consensus.go`, add CRDT sync initialization:

```go
// Add this field to your Consensus struct
type Consensus struct {
    // ... existing fields ...
    SyncManager *CRDTSync.ConsensusSyncManager
}

// In your NewConsensus function, initialize the sync manager
func NewConsensus(peerList PeerList, host host.Host) *Consensus {
    responseHandler := NewResponseHandler()
    syncManager := CRDTSync.NewConsensusSyncManager(host)
    
    return &Consensus{
        PeerList:        peerList,
        Host:            host,
        Channel:         config.PubSub_ConsensusChannel,
        ResponseHandler: responseHandler,
        SyncManager:     syncManager, // Add this line
    }
}
```

### 3. Initialize Sync Before Vote Aggregation

In your `Start` method, add sync initialization:

```go
func (consensus *Consensus) Start(zkblock *config.ZKBlock) error {
    // ... existing code ...
    
    // Initialize CRDT sync for all buddy nodes
    buddyPeerIDs := make([]string, len(consensus.PeerList.MainPeers)+len(consensus.PeerList.BackupPeers))
    for i, peer := range consensus.PeerList.MainPeers {
        buddyPeerIDs[i] = peer.String()
    }
    for i, peer := range consensus.PeerList.BackupPeers {
        buddyPeerIDs[i+len(consensus.PeerList.MainPeers)] = peer.String()
    }
    
    if err := consensus.SyncManager.InitializeSyncForConsensus(buddyPeerIDs, consensus.gossipnode); err != nil {
        log.Printf("⚠️ Failed to initialize CRDT sync: %v", err)
        // Don't fail the consensus, just log the warning
    }
    
    // ... rest of existing code ...
}
```

### 4. Trigger Sync Before Vote Aggregation

In your `PrintCRDTState` method, add sync trigger before processing votes:

```go
func (consensus *Consensus) PrintCRDTState() error {
    // ... existing code ...
    
    // Trigger CRDT sync before vote aggregation
    if consensus.SyncManager != nil {
        if err := consensus.SyncManager.TriggerSyncBeforeVoteAggregation(); err != nil {
            log.Printf("⚠️ Failed to trigger CRDT sync: %v", err)
        }
        
        // Wait for sync completion
        if err := consensus.SyncManager.WaitForSyncCompletion(5 * time.Second); err != nil {
            log.Printf("⚠️ CRDT sync timeout: %v", err)
        }
    }
    
    // ... rest of existing code ...
}
```

### 5. Use Synced CRDT for Vote Processing

Update your vote processing to use the synced CRDT:

```go
// In your vote processing logic, get the synced CRDT engine
if consensus.SyncManager != nil {
    syncedEngine, err := consensus.SyncManager.GetSyncedCRDTEngine()
    if err == nil {
        // Use syncedEngine instead of the original CRDT
        // This ensures all nodes have the latest data before vote aggregation
        log.Printf("✅ Using synced CRDT engine for vote processing")
    }
}
```

### 6. Cleanup on Exit

Add cleanup in your consensus cleanup:

```go
func (consensus *Consensus) Stop() error {
    if consensus.SyncManager != nil {
        consensus.SyncManager.StopAllSyncServices()
    }
    // ... rest of cleanup ...
}
```

## Configuration

You can customize the sync behavior by modifying the config:

```go
// Create custom config
config := &CRDTSync.SyncConfig{
    Mode:              CRDTSync.ModeBoth,
    SyncInterval:      2 * time.Second, // Faster sync
    TopicName:         "crdt-sync-topic",
    PrintStore:        false,
    HeartbeatInterval: 5 * time.Second,
}

// Use custom config when creating sync manager
syncManager := CRDTSync.NewConsensusSyncManagerWithConfig(host, config)
```

## Benefits

1. **Data Consistency**: All buddy nodes will have synchronized CRDT data before vote aggregation
2. **Conflict Resolution**: CRDTs automatically handle conflicts using vector clocks
3. **Fault Tolerance**: Sync continues even if some nodes are temporarily unavailable
4. **Performance**: Optimized for fast sync before critical consensus operations

## Monitoring

You can monitor sync status:

```go
stats := consensus.SyncManager.GetSyncStats()
log.Printf("Sync stats: %+v", stats)
```

This integration ensures that all buddy nodes have the most up-to-date CRDT data before performing vote aggregation, leading to more accurate and consistent consensus results.
