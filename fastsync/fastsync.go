package fastsync

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/bits-and-blooms/bloom/v3"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/crdt"
)

const (
    SyncProtocolID        = config.SyncProtocol
    BloomFilterSize       = 100000  // Keep this the same
    BloomFilterHashFuncs  = 5       // Keep this the same
    BloomFilterCapacity   = 100000  // Add this new constant for capacity estimation
    SyncBatchSize         = 200     // Number of transactions to sync in each batch
    MaxConcurrentBatches  = 8       // Maximum number of batches to process concurrently
    RetriesBeforeAborting = 3       // Maximum number of retries for failed batches
    SyncTimeout           = 5 * time.Minute
    SyncRequestTimeout    = 30 * time.Second
    SyncResponseTimeout   = 2 * time.Minute
)

// Message types
const (
    TypeSyncRequest         = "SYNC_REQ"
    TypeSyncResponse        = "SYNC_RESP"
    TypeBloomFilterExchange = "BLOOM"
    TypeBatchRequest        = "BATCH_REQ"
    TypeBatchData           = "BATCH_DATA"
    TypeSyncComplete        = "SYNC_COMPLETE"
    TypeSyncAbort           = "SYNC_ABORT"
    TypeVerificationRequest = "VERIFY_REQ"
    TypeVerificationResult  = "VERIFY_RESP"
)

// SyncMessage represents a sync protocol message
type SyncMessage struct {
    Type          string          `json:"type"`
    SenderID      string          `json:"sender_id"`
    TxID          uint64          `json:"tx_id,omitempty"`
    StartTxID     uint64          `json:"start_tx_id,omitempty"`
    EndTxID       uint64          `json:"end_tx_id,omitempty"`
    BatchNumber   int             `json:"batch_number,omitempty"`
    TotalBatches  int             `json:"total_batches,omitempty"`
    BloomFilter   []byte          `json:"bloom_filter,omitempty"`
    MerkleRoot    []byte          `json:"merkle_root,omitempty"`
    KeysCount     int             `json:"keys_count,omitempty"`
    Data          json.RawMessage `json:"data,omitempty"`
    Success       bool            `json:"success,omitempty"`
    ErrorMessage  string          `json:"error_message,omitempty"`
    Timestamp     int64           `json:"timestamp"`
}

// BatchData represents a batch of key-value entries
type BatchData struct {
    Entries []KeyValueEntry    `json:"entries"`
    CRDTs   []json.RawMessage  `json:"crdts"` // Change this from []crdt.CRDT to []json.RawMessage
}

// KeyValueEntry represents a key-value entry in ImmuDB
type KeyValueEntry struct {
    Key       []byte    `json:"key"`
    Value     []byte    `json:"value"`
    Timestamp time.Time `json:"timestamp"`
    TxID      uint64    `json:"tx_id"`
}

// FastSync manages the sync process between nodes
type FastSync struct {
    host       host.Host
    db         *DB_OPs.ImmuClient
    crdtEngine *crdt.Engine
    active     map[peer.ID]*syncState
    mutex      sync.RWMutex
}

// syncState tracks the state of an ongoing sync
type syncState struct {
    peer            peer.ID
    startTime       time.Time
    startTxID       uint64
    endTxID         uint64
    currentTxID     uint64
    totalBatches    int
    completedBatches int
    batchResults    map[int]bool // Track success/fail by batch number
    retries         map[int]int  // Track retry counts by batch number
    cancel          context.CancelFunc
    sentKeys        *bloom.BloomFilter
    receivedKeys    *bloom.BloomFilter
}

// NewFastSync creates a new FastSync service
func NewFastSync(h host.Host, db *DB_OPs.ImmuClient) *FastSync {
    fs := &FastSync{
        host:       h,
        db:         db,
        crdtEngine: crdt.NewEngine(db),
        active:     make(map[peer.ID]*syncState),
    }

    // Register protocol handler
    h.SetStreamHandler(SyncProtocolID, fs.handleSyncStream)
    
    log.Info().Msg("FastSync service initialized")
    return fs
}

// handleSyncStream handles incoming sync requests
func (fs *FastSync) handleSyncStream(stream network.Stream) {
    peerID := stream.Conn().RemotePeer()
    remote := stream.Conn().RemoteMultiaddr().String()
    
    log.Info().
        Str("peer", peerID.String()).
        Str("remote", remote).
        Msg("Received sync stream")
    
    // Create buffered reader/writer
    reader := bufio.NewReader(stream)
    writer := bufio.NewWriter(stream)
    
    // Process messages until stream is closed
    for {
        // Set read deadline
        if err := stream.SetReadDeadline(time.Now().Add(SyncResponseTimeout)); err != nil {
            log.Error().Err(err).Msg("Failed to set read deadline")
            break
        }
        
        // Read message
        msgBytes, err := reader.ReadBytes('\n')
        if err != nil {
            if err != io.EOF {
                log.Debug().Err(err).Msg("Error reading from stream")
            }
            break
        }
        
        // Parse message
        var msg SyncMessage
        if err := json.Unmarshal(msgBytes, &msg); err != nil {
            log.Error().Err(err).Msg("Failed to parse sync message")
            break
        }
        
        // Handle message based on type
        var response *SyncMessage
        var handleErr error
        
        switch msg.Type {
        case TypeSyncRequest:
            response, handleErr = fs.handleSyncRequest(peerID, &msg)
        case TypeBloomFilterExchange:
            response, handleErr = fs.handleBloomFilterExchange(peerID, &msg)
        case TypeBatchRequest:
            response, handleErr = fs.handleBatchRequest(peerID, &msg)
        case TypeBatchData:
            response, handleErr = fs.handleBatchData(peerID, &msg)
        case TypeSyncComplete:
            response, handleErr = fs.handleSyncComplete(peerID, &msg)
        case TypeVerificationRequest:
            response, handleErr = fs.handleVerificationRequest(peerID, &msg)
        case TypeSyncAbort:
            log.Warn().
                Str("peer", peerID.String()).
                Str("reason", msg.ErrorMessage).
                Msg("Sync aborted by peer")
            fs.cleanupSync(peerID)
            break
        default:
            log.Warn().
                Str("type", msg.Type).
                Msg("Unknown message type received")
            continue
        }
        
        // Handle errors
        if handleErr != nil {
            log.Error().Err(handleErr).
                Str("msg_type", msg.Type).
                Str("peer", peerID.String()).
                Msg("Error handling sync message")
            
            // Send abort message
            abortMsg := &SyncMessage{
                Type:         TypeSyncAbort,
                SenderID:     fs.host.ID().String(),
                ErrorMessage: handleErr.Error(),
                Timestamp:    time.Now().Unix(),
            }
            
            abortBytes, _ := json.Marshal(abortMsg)
            abortBytes = append(abortBytes, '\n')
            
            if err := stream.SetWriteDeadline(time.Now().Add(5 * time.Second)); err == nil {
                writer.Write(abortBytes)
                writer.Flush()
            }
            
            break
        }
        
        // Send response if needed
        if response != nil {
            respBytes, err := json.Marshal(response)
            if err != nil {
                log.Error().Err(err).Msg("Failed to marshal response")
                break
            }
            respBytes = append(respBytes, '\n')
            
            if err := stream.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
                log.Error().Err(err).Msg("Failed to set write deadline")
                break
            }
            
            if _, err := writer.Write(respBytes); err != nil {
                log.Error().Err(err).Msg("Failed to write response")
                break
            }
            
            if err := writer.Flush(); err != nil {
                log.Error().Err(err).Msg("Failed to flush response")
                break
            }
        }
    }
}

// handleSyncRequest processes a sync request from a peer
func (fs *FastSync) handleSyncRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    // Get current state from ImmuDB
    state, err := fs.db.GetDatabaseState()
    if err != nil {
        return nil, fmt.Errorf("failed to get database state: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Uint64("peer_tx_id", msg.TxID).
        Uint64("our_tx_id", state.TxId).
        Msg("Received sync request")
    
    // Create new sync state
    ctx, cancel := context.WithTimeout(context.Background(), SyncTimeout)
    
    bloomFilter := bloom.NewWithEstimates(BloomFilterCapacity, 0.01) // 1% false positive rate    

    // Create sync state
    syncState := &syncState{
        peer:            peerID,
        startTime:       time.Now(),
        startTxID:       msg.TxID, // The remote node's current TxID
        endTxID:         state.TxId,
        currentTxID:     msg.TxID,
        batchResults:    make(map[int]bool),
        retries:         make(map[int]int),
        cancel:          cancel,
        sentKeys:        bloomFilter,
        receivedKeys:    bloom.NewWithEstimates(BloomFilterSize, 0.01),
    }
    
    // Store sync state
    fs.mutex.Lock()
    fs.active[peerID] = syncState
    fs.mutex.Unlock()
    
    // Calculate number of batches
    txDiff := state.TxId - msg.TxID
    totalBatches := int((txDiff + SyncBatchSize - 1) / SyncBatchSize)
    if totalBatches == 0 {
        totalBatches = 1 // At least one batch even if no actual diff
    }
    
    syncState.totalBatches = totalBatches
    
    // Prepare response with our state
    response := &SyncMessage{
        Type:         TypeSyncResponse,
        SenderID:     fs.host.ID().String(),
        TxID:         state.TxId,
        MerkleRoot:   state.TxHash,
        TotalBatches: totalBatches,
        Timestamp:    time.Now().Unix(),
    }
    
    // Start a goroutine to monitor sync progress and timeout
    go func() {
        defer cancel()
        
        select {
        case <-ctx.Done():
            if ctx.Err() == context.DeadlineExceeded {
                log.Warn().
                    Str("peer", peerID.String()).
                    Dur("elapsed", time.Since(syncState.startTime)).
                    Msg("Sync timed out")
                
                fs.cleanupSync(peerID)
            }
        }
    }()
    
    // If we're synced already, no need to exchange data
    if msg.TxID >= state.TxId {
        log.Info().
            Str("peer", peerID.String()).
            Msg("Peer is already in sync")
            
        // Send sync complete message instead
        return &SyncMessage{
            Type:       TypeSyncComplete,
            SenderID:   fs.host.ID().String(),
            Success:    true,
            MerkleRoot: state.TxHash,
            Timestamp:  time.Now().Unix(),
        }, nil
    }
    
    return response, nil
}

// handleBloomFilterExchange processes a bloom filter from a peer
func (fs *FastSync) handleBloomFilterExchange(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    fs.mutex.RLock()
    syncState, exists := fs.active[peerID]
    fs.mutex.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("no active sync for peer %s", peerID)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Int("filter_size_bytes", len(msg.BloomFilter)).
        Msg("Received Bloom filter")
        
    // Deserialize the bloom filter
    remoteFilter := &bloom.BloomFilter{}
    if err := remoteFilter.UnmarshalBinary(msg.BloomFilter); err != nil {
        return nil, fmt.Errorf("failed to deserialize bloom filter: %w", err)
    }
    
    // Store the received filter
    syncState.receivedKeys = remoteFilter
    
    // Now we need to build our bloom filter of keys
    ourFilter := bloom.NewWithEstimates(BloomFilterCapacity, 0.01) // 1% false positive rate    

    // Get all keys from our database (or just the ones in the sync range)
    keys, err := fs.getKeysInRange(syncState.startTxID, syncState.endTxID)
    if err != nil {
        return nil, fmt.Errorf("failed to get keys: %w", err)
    }
    
    // Add keys to our filter
    for _, key := range keys {
        addSafelyToBloomFilter(ourFilter, key)
    }
    
    // Serialize our filter
    filterBytes, err := ourFilter.MarshalBinary()
    if err != nil {
        return nil, fmt.Errorf("failed to serialize bloom filter: %w", err)
    }
    
    // Store our filter in the sync state
    syncState.sentKeys = ourFilter
    
    // Send our filter back
    return &SyncMessage{
        Type:        TypeBloomFilterExchange,
        SenderID:    fs.host.ID().String(),
        BloomFilter: filterBytes,
        KeysCount:   len(keys),
        Timestamp:   time.Now().Unix(),
    }, nil
}

// getKeysInRange retrieves keys between specified transaction IDs
func (fs *FastSync) getKeysInRange(startTxID, endTxID uint64) ([]string, error) {
    // The maximum allowed limit is 2500, so we use this as our batch size
    const maxKeysPerBatch = 2000 // Using slightly less than max to be safe
    var allKeys []string
    var lastKey string
    var count int
    
    // Get keys in batches to respect ImmuDB's limits
    for {
        keys, err := fs.db.GetKeys(lastKey, maxKeysPerBatch)
        if err != nil {
            return nil, fmt.Errorf("GetKeys failed: %w", err)
        }
        
        // Add keys to our collection
        allKeys = append(allKeys, keys...)
        count = len(keys)
        
        // If we got fewer keys than our limit, we've reached the end
        if count < maxKeysPerBatch {
            break
        }
        
        // Otherwise, set the last key for the next batch
        lastKey = keys[count-1]
    }
    
    log.Info().
        Int("total_keys", len(allKeys)).
        Uint64("start_tx", startTxID).
        Uint64("end_tx", endTxID).
        Msg("Retrieved keys from database")
    
    return allKeys, nil
}

// handleBatchRequest processes a request for a specific batch of data
func (fs *FastSync) handleBatchRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    fs.mutex.RLock()
    syncState, exists := fs.active[peerID]
    fs.mutex.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("no active sync for peer %s", peerID)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Uint64("start_tx", msg.StartTxID).
        Uint64("end_tx", msg.EndTxID).
        Int("batch", msg.BatchNumber).
        Msg("Received batch request")
    
    // Gather keys/values to send based on bloom filter comparison
    entries, crdts, err := fs.getBatchData(msg.StartTxID, msg.EndTxID, syncState.receivedKeys)
    if err != nil {
        return nil, fmt.Errorf("failed to get batch data: %w", err)
    }
    
    // Create batch data
    batchData := BatchData{
        Entries: entries,
        CRDTs:   crdts,
    }
    
    // Serialize batch data
    dataBytes, err := json.Marshal(batchData)
    if err != nil {
        return nil, fmt.Errorf("failed to serialize batch data: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Int("entries", len(entries)).
        Int("crdts", len(crdts)).
        Int("bytes", len(dataBytes)).
        Int("batch", msg.BatchNumber).
        Msg("Sending batch data")
    
    // Send batch data
    return &SyncMessage{
        Type:        TypeBatchData,
        SenderID:    fs.host.ID().String(),
        BatchNumber: msg.BatchNumber,
        StartTxID:   msg.StartTxID,
        EndTxID:     msg.EndTxID,
        Data:        dataBytes,
        Timestamp:   time.Now().Unix(),
    }, nil
}

// getBatchData retrieves data for a batch based on Bloom filter comparison
// Update the getBatchData method to properly serialize CRDTs
func (fs *FastSync) getBatchData(startTxID, endTxID uint64, remoteFilter *bloom.BloomFilter) ([]KeyValueEntry, []json.RawMessage, error) {
    var entries []KeyValueEntry
    var crdtEntries []json.RawMessage
    
    // Get all keys in this TxID range with pagination
    const maxKeysPerBatch = 2000
    var lastKey string
    var continueLoop = true
    
    for continueLoop {
        keys, err := fs.db.GetKeys(lastKey, maxKeysPerBatch)
        if err != nil {
            return nil, nil, err
        }
        
        count := len(keys)
        
        // Process this batch of keys
        for _, key := range keys {
            // Only send keys that the remote node doesn't have
            if !remoteFilter.Test([]byte(key)) {
                // Get the key data
                data, err := fs.db.Read(key)
                if err != nil {
                    // Skip if key not found - might have been deleted
                    continue
                }
                
                // Check if this is a CRDT
                if fs.crdtEngine.IsCRDT(key) {
                    crdtValue, err := fs.crdtEngine.DeserializeCRDT(key, data, nil)
                    if err == nil {
                        // Wrap the CRDT in a type envelope for proper serialization/deserialization
                        crdtType := ""
                        switch crdtValue.(type) {
                        case *crdt.LWWSet:
                            crdtType = "lww-set"
                        case *crdt.Counter:
                            crdtType = "counter"
                        default:
                            crdtType = "unknown"
                        }
                        
                        // Create a wrapper with type information
                        wrapper := map[string]interface{}{
                            "type": crdtType,
                            "data": crdtValue,
                        }
                        
                        // Serialize to JSON
                        crdtBytes, err := json.Marshal(wrapper)
                        if err == nil {
                            crdtEntries = append(crdtEntries, crdtBytes)
                        } else {
                            log.Error().
                                Err(err).
                                Str("key", key).
                                Msg("Failed to serialize CRDT")
                        }
                    } else {
                        log.Error().
                            Err(err).
                            Str("key", key).
                            Msg("Failed to deserialize CRDT")
                    }
                } else {
                    // Regular key-value entry
                    entries = append(entries, KeyValueEntry{
                        Key:   []byte(key),
                        Value: data,
                        // Note: In a real implementation, we'd extract the actual timestamp and TxID
                        Timestamp: time.Now(),
                        TxID:      0,
                    })
                }
            }
        }
        
        // If we got fewer keys than our limit, we've reached the end
        if count < maxKeysPerBatch {
            continueLoop = false
        } else {
            // Set the last key for the next batch
            lastKey = keys[count-1]
        }
    }
    
    return entries, crdtEntries, nil
}

// handleBatchData processes a batch of data from a peer
func (fs *FastSync) handleBatchData(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    fs.mutex.RLock()
    syncState, exists := fs.active[peerID]
    fs.mutex.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("no active sync for peer %s", peerID)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Int("batch", msg.BatchNumber).
        Int("data_bytes", len(msg.Data)).
        Msg("Received batch data")
    
    // Parse batch data
    var batchData BatchData
    if err := json.Unmarshal(msg.Data, &batchData); err != nil {
        return nil, fmt.Errorf("failed to parse batch data: %w", err)
    }
    
    // Process entries first (no change)
    entries := make(map[string]interface{})
    for _, entry := range batchData.Entries {
        key := string(entry.Key)
        entries[key] = entry.Value
    }
    
    // Batch create the entries if we have any (no change)
    if len(entries) > 0 {
        if err := fs.db.BatchCreate(entries); err != nil {
            return nil, fmt.Errorf("failed to store batch entries: %w", err)
        }
    }
    
    // Process CRDTs with proper type handling
    if len(batchData.CRDTs) > 0 {
        for _, crdtBytes := range batchData.CRDTs {
            // Parse the wrapper first
            var wrapper map[string]json.RawMessage
            if err := json.Unmarshal(crdtBytes, &wrapper); err != nil {
                log.Error().Err(err).Msg("Failed to unmarshal CRDT wrapper")
                continue
            }
            
            // Get the type
            var crdtType string
            if err := json.Unmarshal(wrapper["type"], &crdtType); err != nil {
                log.Error().Err(err).Msg("Failed to unmarshal CRDT type")
                continue
            }
            
            // Create the correct CRDT type based on the type string
            var crdtValue crdt.CRDT
            switch crdtType {
            case "lww-set":
                crdtValue = &crdt.LWWSet{}
            case "counter":
                crdtValue = &crdt.Counter{}
            default:
                log.Error().Str("type", crdtType).Msg("Unknown CRDT type")
                continue
            }
            
            // Unmarshal the data into the specific CRDT type
            if err := json.Unmarshal(wrapper["data"], crdtValue); err != nil {
                log.Error().
                    Err(err).
                    Str("type", crdtType).
                    Msg("Failed to unmarshal CRDT data")
                continue
            }
            
            // Now you have a properly deserialized CRDT
            mergedCRDT, err := fs.crdtEngine.MergeCRDT(crdtValue)
            if err != nil {
                log.Error().Err(err).
                    Str("key", crdtValue.GetKey()).
                    Msg("Failed to merge CRDT")
                continue
            }
            
            // Store the merged CRDT
            if err := fs.crdtEngine.StoreCRDT(mergedCRDT); err != nil {
                log.Error().Err(err).
                    Str("key", crdtValue.GetKey()).
                    Msg("Failed to store merged CRDT")
                continue
            }
        }
    }
    
    // Update sync state
    fs.mutex.Lock()
    syncState.batchResults[msg.BatchNumber] = true
    syncState.completedBatches++
    
    // Track progress
    progress := float64(syncState.completedBatches) / float64(syncState.totalBatches) * 100
    fs.mutex.Unlock()
    
    log.Info().
        Str("peer", peerID.String()).
        Int("batch", msg.BatchNumber).
        Int("completed", syncState.completedBatches).
        Int("total", syncState.totalBatches).
        Float64("progress", progress).
        Msg("Batch processed")
    
    // If we've processed all batches, request verification
    var response *SyncMessage
    if syncState.completedBatches >= syncState.totalBatches {
        log.Info().
            Str("peer", peerID.String()).
            Int("total_batches", syncState.totalBatches).
            Msg("All batches received, requesting verification")
        
        // Request verification
        response = &SyncMessage{
            Type:       TypeVerificationRequest,
            SenderID:   fs.host.ID().String(),
            Timestamp:  time.Now().Unix(),
        }
    } else {
        // Just acknowledge receipt
        response = &SyncMessage{
            Type:        TypeBatchData,
            SenderID:    fs.host.ID().String(),
            BatchNumber: msg.BatchNumber,
            Success:     true,
            Timestamp:   time.Now().Unix(),
        }
    }
    
    return response, nil
}

// handleSyncComplete processes a sync complete message from a peer
func (fs *FastSync) handleSyncComplete(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    fs.mutex.RLock()
    syncState, exists := fs.active[peerID]
    fs.mutex.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("no active sync for peer %s", peerID)
    }
    
    // Get our current state
    state, err := fs.db.GetDatabaseState()
    if err != nil {
        return nil, fmt.Errorf("failed to get database state: %w", err)
    }
    
    // Check if Merkle roots match
    rootsMatch := true
    if len(msg.MerkleRoot) > 0 {
        rootsMatch = bytes.Equal(state.TxHash, msg.MerkleRoot)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Bool("success", msg.Success).
        Bool("roots_match", rootsMatch).
        Dur("duration", time.Since(syncState.startTime)).
        Msg("Sync complete")
    
    // Cleanup sync state
    fs.cleanupSync(peerID)
    
    // Send acknowledgement
    return &SyncMessage{
        Type:       TypeSyncComplete,
        SenderID:   fs.host.ID().String(),
        Success:    rootsMatch,
        MerkleRoot: state.TxHash,
        Timestamp:  time.Now().Unix(),
    }, nil
}

// handleVerificationRequest processes a verification request from a peer
func (fs *FastSync) handleVerificationRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    // Get current state from ImmuDB
    state, err := fs.db.GetDatabaseState()
    if err != nil {
        return nil, fmt.Errorf("failed to get database state: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Str("merkle_root", fmt.Sprintf("%x", state.TxHash)).
        Uint64("tx_id", state.TxId).
        Msg("Received verification request")
    
    // Send verification result
    return &SyncMessage{
        Type:       TypeVerificationResult,
        SenderID:   fs.host.ID().String(),
        MerkleRoot: state.TxHash,
        TxID:       state.TxId,
        Timestamp:  time.Now().Unix(),
    }, nil
}

// cleanupSync removes a sync state
func (fs *FastSync) cleanupSync(peerID peer.ID) {
    fs.mutex.Lock()
    defer fs.mutex.Unlock()
    
    if state, exists := fs.active[peerID]; exists {
        if state.cancel != nil {
            state.cancel()
        }
        delete(fs.active, peerID)
    }
}

// StartSync initiates a sync with a peer
func (fs *FastSync) StartSync(peerID peer.ID) error {
    // Get current state from ImmuDB
    state, err := fs.db.GetDatabaseState()
    if err != nil {
        return fmt.Errorf("failed to get current database state: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Uint64("our_tx_id", state.TxId).
        Msg("Starting fast sync with peer")
    
    // Open stream to peer
    ctx, cancel := context.WithTimeout(context.Background(), SyncRequestTimeout)
    defer cancel()
    
    stream, err := fs.host.NewStream(ctx, peerID, SyncProtocolID)
    if err != nil {
        return fmt.Errorf("failed to open stream: %w", err)
    }
    
    // Set up buffered reader/writer
    reader := bufio.NewReader(stream)
    writer := bufio.NewWriter(stream)
    
    // Send sync request
    syncReq := SyncMessage{
        Type:      TypeSyncRequest,
        SenderID:  fs.host.ID().String(),
        TxID:      state.TxId,
        Timestamp: time.Now().Unix(),
    }
    
    reqBytes, err := json.Marshal(syncReq)
    if err != nil {
        stream.Close()
        return fmt.Errorf("failed to serialize sync request: %w", err)
    }
    reqBytes = append(reqBytes, '\n')
    
    if _, err := writer.Write(reqBytes); err != nil {
        stream.Close()
        return fmt.Errorf("failed to send sync request: %w", err)
    }
    
    if err := writer.Flush(); err != nil {
        stream.Close()
        return fmt.Errorf("failed to flush sync request: %w", err)
    }
    
    // Wait for response
    if err := stream.SetReadDeadline(time.Now().Add(SyncResponseTimeout)); err != nil {
        stream.Close()
        return fmt.Errorf("failed to set read deadline: %w", err)
    }
    
    respBytes, err := reader.ReadBytes('\n')
    if err != nil {
        stream.Close()
        return fmt.Errorf("failed to read sync response: %w", err)
    }
    
    var response SyncMessage
    if err := json.Unmarshal(respBytes, &response); err != nil {
        stream.Close()
        return fmt.Errorf("failed to parse sync response: %w", err)
    }
    
    // Handle sync response
    switch response.Type {
    case TypeSyncResponse:
        // If we received a sync response, continue with sync process
        err := fs.processSyncResponse(peerID, stream, reader, writer, &response)
        if err != nil {
            stream.Close()
            return err
        }
    case TypeSyncComplete:
        // If peer says we're already in sync, verify Merkle roots
        if response.Success {
            rootsMatch := bytes.Equal(state.TxHash, response.MerkleRoot)
            
            log.Info().
                Str("peer", peerID.String()).
                Bool("roots_match", rootsMatch).
                Msg("Peer reports we're already in sync")
            
            if rootsMatch {
                log.Info().Msg("Fast sync completed successfully (no data transfer needed)")
                stream.Close()
                return nil
            } else {
                // Roots don't match - force full sync
                log.Warn().
                    Str("our_root", fmt.Sprintf("%x", state.TxHash)).
                    Str("peer_root", fmt.Sprintf("%x", response.MerkleRoot)).
                    Msg("Merkle roots don't match, forcing full sync")
                
                stream.Close()
                return fmt.Errorf("merkle roots don't match")
            }
        }
        stream.Close()
        return fmt.Errorf("peer reported sync failure: %s", response.ErrorMessage)
    default:
        stream.Close()
        return fmt.Errorf("unexpected response type: %s", response.Type)
    }
    
    // We'll keep the stream open for the processSyncResponse method
    return nil
}

// processSyncResponse continues the sync process after receiving the initial response
func (fs *FastSync) processSyncResponse(peerID peer.ID, stream network.Stream, reader *bufio.Reader, writer *bufio.Writer, response *SyncMessage) error {
    defer stream.Close() // Make sure we close the stream when we're done
    
    // Extract peer's state
    peerTxID := response.TxID
    peerMerkleRoot := response.MerkleRoot
    totalBatches := response.TotalBatches
    
    log.Info().
        Str("peer", peerID.String()).
        Uint64("peer_tx_id", peerTxID).
        Int("total_batches", totalBatches).
		Str("merkle_root", fmt.Sprintf("%x", peerMerkleRoot)).
        Msg("Processing sync response")

    // Get our current state from ImmuDB
    ourState, err := fs.db.GetDatabaseState()
    if err != nil {
        return fmt.Errorf("failed to get our database state: %w", err)
    }

    // First exchange bloom filters to optimize what we need to sync
    bloomFilter := bloom.NewWithEstimates(BloomFilterCapacity, 0.01) 
    
    // Get all our keys and add them to the filter
    ourKeys, err := fs.getKeysInRange(0, ourState.TxId)
    if err != nil {
        return fmt.Errorf("failed to get our keys: %w", err)
    }
    
    for _, key := range ourKeys {
        addSafelyToBloomFilter(bloomFilter, key)
    }

    // Serialize our filter
    filterBytes, err := bloomFilter.MarshalBinary()
    if err != nil {
        return fmt.Errorf("failed to serialize bloom filter: %w", err)
    }
    
    // Send our bloom filter
    bloomMsg := SyncMessage{
        Type:        TypeBloomFilterExchange,
        SenderID:    fs.host.ID().String(),
        BloomFilter: filterBytes,
        KeysCount:   len(ourKeys),
        Timestamp:   time.Now().Unix(),
    }
    
    bloomBytes, err := json.Marshal(bloomMsg)
    if err != nil {
        return fmt.Errorf("failed to marshal bloom filter message: %w", err)
    }
    bloomBytes = append(bloomBytes, '\n')
    
    if _, err := writer.Write(bloomBytes); err != nil {
        return fmt.Errorf("failed to send bloom filter: %w", err)
    }
    
    if err := writer.Flush(); err != nil {
        return fmt.Errorf("failed to flush bloom filter: %w", err)
    }
    
    // Wait for peer's bloom filter
    if err := stream.SetReadDeadline(time.Now().Add(SyncResponseTimeout)); err != nil {
        return fmt.Errorf("failed to set read deadline: %w", err)
    }
    
    peerBloomBytes, err := reader.ReadBytes('\n')
    if err != nil {
        return fmt.Errorf("failed to read peer's bloom filter: %w", err)
    }
    
    var peerBloomMsg SyncMessage
    if err := json.Unmarshal(peerBloomBytes, &peerBloomMsg); err != nil {
        return fmt.Errorf("failed to parse peer's bloom filter: %w", err)
    }
    
    if peerBloomMsg.Type != TypeBloomFilterExchange {
        return fmt.Errorf("unexpected message type: %s", peerBloomMsg.Type)
    }
    
    // Deserialize peer's bloom filter
    peerBloom := &bloom.BloomFilter{}
    if err := peerBloom.UnmarshalBinary(peerBloomMsg.BloomFilter); err != nil {
        return fmt.Errorf("failed to deserialize peer's bloom filter: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Int("peer_keys", peerBloomMsg.KeysCount).
        Int("our_keys", len(ourKeys)).
        Msg("Bloom filters exchanged")

    // Request batches concurrently
    var wg sync.WaitGroup
    batchErrors := make(chan error, totalBatches)
    batchResults := make(map[int]bool)
    batchSemaphore := make(chan struct{}, MaxConcurrentBatches)

    // Calculate batch ranges
    ourTxID := ourState.TxId
    txDiff := peerTxID - ourTxID
    
    // If we're ahead, no need to pull data
    if txDiff <= 0 {
        log.Info().
            Str("peer", peerID.String()).
            Msg("We're already up to date with peer")
            
        // Request final verification to confirm merkle roots match
        verifyMsg := SyncMessage{
            Type:      TypeVerificationRequest,
            SenderID:  fs.host.ID().String(),
            Timestamp: time.Now().Unix(),
        }
        
        verifyBytes, _ := json.Marshal(verifyMsg)
        verifyBytes = append(verifyBytes, '\n')
        
        if _, err := writer.Write(verifyBytes); err != nil {
            return fmt.Errorf("failed to send verification request: %w", err)
        }
        
        if err := writer.Flush(); err != nil {
            return fmt.Errorf("failed to flush verification request: %w", err)
        }
        
        return fs.handleFinalVerification(reader, ourState.TxHash)
    }
    
    // Process batches concurrently
    for batchNum := 0; batchNum < totalBatches; batchNum++ {
        wg.Add(1)
        go func(batch int) {
            defer wg.Done()
            
            // Acquire semaphore
            batchSemaphore <- struct{}{}
            defer func() { <-batchSemaphore }()
            
            // Calculate batch range
            batchStart := ourTxID + uint64(batch*SyncBatchSize)
            batchEnd := batchStart + uint64(SyncBatchSize) - 1
            if batchEnd > peerTxID {
                batchEnd = peerTxID
            }
            
            // Request this batch
            err := fs.requestAndProcessBatch(writer, reader, batch, batchStart, batchEnd, peerBloom)
            if err != nil {
                batchErrors <- fmt.Errorf("batch %d failed: %w", batch, err)
                return
            }
            
            // Mark batch as successful
            batchResults[batch] = true
            
            log.Info().
                Str("peer", peerID.String()).
                Int("batch", batch).
                Int("total", totalBatches).
                Float64("progress", float64(batch+1)/float64(totalBatches)*100).
                Msg("Batch processed successfully")
        }(batchNum)
    }
    
    // Wait for all batches to complete
    wg.Wait()
    close(batchErrors)
    
    // Check for errors
    var errs []error
    for err := range batchErrors {
        errs = append(errs, err)
    }
    
    if len(errs) > 0 {
        return fmt.Errorf("failed to process %d batches: %v", len(errs), errs[0])
    }
    
    // All batches succeeded, verify final state
    log.Info().
        Str("peer", peerID.String()).
        Int("batches_processed", len(batchResults)).
        Msg("All batches processed, verifying final state")
        
    // Send verification request
    verifyMsg := SyncMessage{
        Type:      TypeVerificationRequest,
        SenderID:  fs.host.ID().String(),
        Timestamp: time.Now().Unix(),
    }
    
    verifyBytes, _ := json.Marshal(verifyMsg)
    verifyBytes = append(verifyBytes, '\n')
    
    if _, err := writer.Write(verifyBytes); err != nil {
        return fmt.Errorf("failed to send verification request: %w", err)
    }
    
    if err := writer.Flush(); err != nil {
        return fmt.Errorf("failed to flush verification request: %w", err)
    }
    
    // Handle verification response
    return fs.handleFinalVerification(reader, peerMerkleRoot)
}

// requestAndProcessBatch requests and processes a single batch of data
func (fs *FastSync) requestAndProcessBatch(writer *bufio.Writer, reader *bufio.Reader, 
    batchNum int, startTxID, endTxID uint64, peerBloom *bloom.BloomFilter) error {
    
    // Create batch request
    batchReq := SyncMessage{
        Type:        TypeBatchRequest,
        SenderID:    fs.host.ID().String(),
        BatchNumber: batchNum,
        StartTxID:   startTxID,
        EndTxID:     endTxID,
        Timestamp:   time.Now().Unix(),
    }
    
    // Send batch request
    reqBytes, err := json.Marshal(batchReq)
    if err != nil {
        return fmt.Errorf("failed to marshal batch request: %w", err)
    }
    reqBytes = append(reqBytes, '\n')
    
    if _, err := writer.Write(reqBytes); err != nil {
        return fmt.Errorf("failed to send batch request: %w", err)
    }
    
    if err := writer.Flush(); err != nil {
        return fmt.Errorf("failed to flush batch request: %w", err)
    }
    
    // Wait for batch data
    respBytes, err := reader.ReadBytes('\n')
    if err != nil {
        return fmt.Errorf("failed to read batch data: %w", err)
    }
    
    var batchResp SyncMessage
    if err := json.Unmarshal(respBytes, &batchResp); err != nil {
        return fmt.Errorf("failed to parse batch response: %w", err)
    }
    
    // Check if this is the correct batch
    if batchResp.Type != TypeBatchData || batchResp.BatchNumber != batchNum {
        return fmt.Errorf("unexpected batch response: type=%s, batch=%d", 
            batchResp.Type, batchResp.BatchNumber)
    }
    
    // Parse and process batch data
    var batchData BatchData
    if err := json.Unmarshal(batchResp.Data, &batchData); err != nil {
        return fmt.Errorf("failed to parse batch data: %w", err)
    }
    
    // Process entries
    entries := make(map[string]interface{})
    for _, entry := range batchData.Entries {
        key := string(entry.Key)
        entries[key] = entry.Value
    }
    
    // Batch create the entries if we have any
    if len(entries) > 0 {
        if err := fs.db.BatchCreate(entries); err != nil {
            return fmt.Errorf("failed to store batch entries: %w", err)
        }
    }
    
    // Process CRDTs
    if len(batchData.CRDTs) > 0 {
        for _, crdtBytes := range batchData.CRDTs {
            // Parse the wrapper first
            var wrapper map[string]json.RawMessage
            if err := json.Unmarshal(crdtBytes, &wrapper); err != nil {
                log.Error().Err(err).Msg("Failed to unmarshal CRDT wrapper")
                continue
            }
            
            // Get the type
            var crdtType string
            if err := json.Unmarshal(wrapper["type"], &crdtType); err != nil {
                log.Error().Err(err).Msg("Failed to unmarshal CRDT type")
                continue
            }
            
            // Create the correct CRDT type based on the type string
            var crdtValue crdt.CRDT
            switch crdtType {
            case "lww-set":
                crdtValue = &crdt.LWWSet{}
            case "counter":
                crdtValue = &crdt.Counter{}
            default:
                log.Error().Str("type", crdtType).Msg("Unknown CRDT type")
                continue
            }
            
            // Unmarshal the data into the specific CRDT type
            if err := json.Unmarshal(wrapper["data"], crdtValue); err != nil {
                log.Error().
                    Err(err).
                    Str("type", crdtType).
                    Msg("Failed to unmarshal CRDT data")
                continue
            }
            
            // Now you have a properly deserialized CRDT
            mergedCRDT, err := fs.crdtEngine.MergeCRDT(crdtValue)
            if err != nil {
                log.Error().Err(err).
                    Str("key", crdtValue.GetKey()).
                    Msg("Failed to merge CRDT")
                continue
            }
            
            // Store the merged CRDT
            if err := fs.crdtEngine.StoreCRDT(mergedCRDT); err != nil {
                log.Error().Err(err).
                    Str("key", crdtValue.GetKey()).
                    Msg("Failed to store merged CRDT")
                continue
            }
        }
    }
    
    // Send acknowledgement
    ackMsg := SyncMessage{
        Type:        TypeBatchData,
        SenderID:    fs.host.ID().String(),
        BatchNumber: batchNum,
        Success:     true,
        Timestamp:   time.Now().Unix(),
    }
    
    ackBytes, _ := json.Marshal(ackMsg)
    ackBytes = append(ackBytes, '\n')
    
    if _, err := writer.Write(ackBytes); err != nil {
        return fmt.Errorf("failed to send batch acknowledgement: %w", err)
    }
    
    if err := writer.Flush(); err != nil {
        return fmt.Errorf("failed to flush batch acknowledgement: %w", err)
    }
    
    return nil
}

// handleFinalVerification processes the verification phase to ensure consistency
func (fs *FastSync) handleFinalVerification(reader *bufio.Reader, peerMerkleRoot []byte) error {
    // Wait for verification result
    respBytes, err := reader.ReadBytes('\n')
    if err != nil {
        return fmt.Errorf("failed to read verification result: %w", err)
    }
    
    var verifyResp SyncMessage
    if err := json.Unmarshal(respBytes, &verifyResp); err != nil {
        return fmt.Errorf("failed to parse verification result: %w", err)
    }
    
    if verifyResp.Type != TypeVerificationResult {
        return fmt.Errorf("unexpected message type: %s", verifyResp.Type)
    }
    
    // Get our final state
    ourState, err := fs.db.GetDatabaseState()
    if err != nil {
        return fmt.Errorf("failed to get final database state: %w", err)
    }
    
    // Compare merkle roots
    peerRoot := verifyResp.MerkleRoot
    ourRoot := ourState.TxHash
    rootsMatch := bytes.Equal(ourRoot, peerRoot)
    
    log.Info().
        Str("peer_root", fmt.Sprintf("%x", peerRoot)).
        Str("our_root", fmt.Sprintf("%x", ourRoot)).
        Bool("match", rootsMatch).
        Uint64("peer_tx_id", verifyResp.TxID).
        Uint64("our_tx_id", ourState.TxId).
        Msg("Sync verification completed")
    
    if !rootsMatch {
        return fmt.Errorf("sync failed: merkle roots don't match after sync")
    }
    
    // Send sync complete message
    completeMsg := SyncMessage{
        Type:       TypeSyncComplete,
        SenderID:   fs.host.ID().String(),
        Success:    true,
        MerkleRoot: ourState.TxHash,
        Timestamp:  time.Now().Unix(),
    }
    
    completeBytes, _ := json.Marshal(completeMsg)
    completeBytes = append(completeBytes, '\n')
    
    return nil
}

// Helper function to add keys safely to bloom filter
func addSafelyToBloomFilter(filter *bloom.BloomFilter, key string) {
    // Skip empty keys
    if len(key) == 0 {
        return
    }
    
    // Use a SHA-256 hash of the key for more uniform distribution
    // This helps prevent certain problematic bit patterns that could cause issues
    hasher := sha256.New()
    hasher.Write([]byte(key))
    hashBytes := hasher.Sum(nil)
    
    filter.Add(hashBytes)
}