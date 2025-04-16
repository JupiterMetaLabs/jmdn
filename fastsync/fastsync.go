package fastsync

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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

// Constants for FastSync
const (
    SyncProtocolID      = config.SyncProtocol
    BloomFilterSize     = 100000
    SyncBatchSize       = 200
    MaxRetries          = 3
    SyncTimeout         = 5 * time.Minute
    RequestTimeout      = 30 * time.Second
    ResponseTimeout     = 60 * time.Second
    RetryDelay          = 500 * time.Millisecond
)

// DatabaseType specifies which database to operate on
type DatabaseType int

const (
    MainDB DatabaseType = iota
    AccountsDB
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
    Type         string          `json:"type"`
    SenderID     string          `json:"sender_id"`
    TxID         uint64          `json:"tx_id,omitempty"`
    StartTxID    uint64          `json:"start_tx_id,omitempty"`
    EndTxID      uint64          `json:"end_tx_id,omitempty"`
    BatchNumber  int             `json:"batch_number,omitempty"`
    TotalBatches int             `json:"total_batches,omitempty"`
    BloomFilter  []byte          `json:"bloom_filter,omitempty"`
    MerkleRoot   []byte          `json:"merkle_root,omitempty"`
    KeysCount    int             `json:"keys_count,omitempty"`
    Data         json.RawMessage `json:"data,omitempty"`
    Success      bool            `json:"success,omitempty"`
    ErrorMessage string          `json:"error_message,omitempty"`
    Timestamp    int64           `json:"timestamp"`
    DBType       DatabaseType    `json:"db_type,omitempty"`
}

// DBState contains the state of a database
type DBState struct {
    Type       DatabaseType `json:"type"`
    TxID       uint64       `json:"tx_id"`
    MerkleRoot []byte       `json:"merkle_root"`
}

// BatchData represents a batch of key-value entries
type BatchData struct {
    Entries []KeyValueEntry   `json:"entries"`
    CRDTs   []json.RawMessage `json:"crdts"`
    DBType  DatabaseType      `json:"db_type"`
}

// KeyValueEntry represents a key-value entry in ImmuDB
type KeyValueEntry struct {
    Key       []byte    `json:"key"`
    Value     []byte    `json:"value"`
    Timestamp time.Time `json:"timestamp"`
    TxID      uint64    `json:"tx_id"`
}

// syncState tracks an ongoing synchronization
type syncState struct {
    peer           peer.ID
    startTime      time.Time
    mainStartTxID  uint64
    mainEndTxID    uint64
    acctsStartTxID uint64
    acctsEndTxID   uint64
    batches        int
    completed      int
    mainBloom      *bloom.BloomFilter
    acctsBloom     *bloom.BloomFilter
    currentDB      DatabaseType
    cancel         context.CancelFunc
}

// FastSync manages the synchronization process
type FastSync struct {
    host        host.Host
    mainDB      *config.ImmuClient
    accountsDB  *config.ImmuClient
    mainCRDT    *crdt.Engine
    acctsCRDT   *crdt.Engine
    active      map[peer.ID]*syncState
    mutex       sync.RWMutex
}

// NewFastSync creates a new FastSync instance
func NewFastSync(h host.Host, mainDB, accountsDB *config.ImmuClient) *FastSync {
    fs := &FastSync{
        host:       h,
        mainDB:     mainDB,
        accountsDB: accountsDB,
        mainCRDT:   crdt.NewEngine(mainDB),
        acctsCRDT:  crdt.NewEngine(accountsDB),
        active:     make(map[peer.ID]*syncState),
    }

    // Register protocol handler
    h.SetStreamHandler(SyncProtocolID, fs.handleStream)
    
    log.Info().Msg("FastSync initialized with multi-database support")
    return fs
}

// getDB returns the appropriate database client and CRDT engine
func (fs *FastSync) getDB(dbType DatabaseType) (*config.ImmuClient, *crdt.Engine) {
    if dbType == AccountsDB {
        return fs.accountsDB, fs.acctsCRDT
    }
    return fs.mainDB, fs.mainCRDT
}

// readMessage reads a message from a stream with timeout
func readMessage(reader *bufio.Reader, stream network.Stream) (*SyncMessage, error) {
    if err := stream.SetReadDeadline(time.Now().Add(ResponseTimeout)); err != nil {
        return nil, fmt.Errorf("failed to set read deadline: %w", err)
    }
    
    // Use a larger buffer for reading message data
    var msgData []byte
    
    // Read until newline to get complete message
    for {
        chunk, isPrefix, err := reader.ReadLine()
        if err != nil {
            return nil, fmt.Errorf("failed to read message: %w", err)
        }
        
        msgData = append(msgData, chunk...)
        
        if !isPrefix {
            break
        }
    }
    
    // Add debugging to show message size
    log.Debug().Int("bytes", len(msgData)).Msg("Read message data")
    
    var msg SyncMessage
    if err := json.Unmarshal(msgData, &msg); err != nil {
        return nil, fmt.Errorf("failed to unmarshal message (%d bytes): %w", len(msgData), err)
    }
    
    return &msg, nil
}

// writeMessage writes a message to a stream
func writeMessage(writer *bufio.Writer, stream network.Stream, msg *SyncMessage) error {
    msgBytes, err := json.Marshal(msg)
    if err != nil {
        return fmt.Errorf("failed to marshal message: %w", err)
    }
    msgBytes = append(msgBytes, '\n')
    
    if err := stream.SetWriteDeadline(time.Now().Add(ResponseTimeout)); err != nil {
        return fmt.Errorf("failed to set write deadline: %w", err)
    }
    
    if _, err := writer.Write(msgBytes); err != nil {
        return fmt.Errorf("failed to write message: %w", err)
    }
    
    if err := writer.Flush(); err != nil {
        return fmt.Errorf("failed to flush message: %w", err)
    }
    
    return nil
}

// retry attempts an operation with retries
func retry(operation func() error) error {
    var lastErr error
    backoff := RetryDelay
    
    for attempt := 0; attempt < MaxRetries; attempt++ {
        if attempt > 0 {
            log.Debug().Int("attempt", attempt+1).Dur("delay", backoff).Msg("Retrying operation")
            time.Sleep(backoff)
            backoff *= 2 // Exponential backoff
        }
        
        if err := operation(); err == nil {
            return nil // Success
        } else {
            lastErr = err
        }
    }
    
    return fmt.Errorf("operation failed after %d attempts: %w", MaxRetries, lastErr)
}

// handleStream processes incoming sync protocol messages
func (fs *FastSync) handleStream(stream network.Stream) {
    defer stream.Close()
    
    peerID := stream.Conn().RemotePeer()
    remote := stream.Conn().RemoteMultiaddr().String()
    
    log.Info().
        Str("peer", peerID.String()).
        Str("remote", remote).
        Msg("Received sync stream")
    
    reader := bufio.NewReader(stream)
    writer := bufio.NewWriter(stream)
    
    for {
        msg, err := readMessage(reader, stream)
        if err != nil {
            if err != io.EOF {
                log.Debug().Err(err).Msg("Error reading from stream")
            }
            break
        }
        
        var response *SyncMessage
        var handleErr error
        
        switch msg.Type {
        case TypeSyncRequest:
            response, handleErr = fs.handleSyncRequest(peerID, msg)
        case TypeBloomFilterExchange:
            response, handleErr = fs.handleBloomFilter(peerID, msg)
        case TypeBatchRequest:
            response, handleErr = fs.handleBatchRequest(peerID, msg)
        case TypeBatchData:
            response, handleErr = fs.handleBatchData(peerID, msg)
        case TypeVerificationRequest:
            response, handleErr = fs.handleVerification(peerID)
        case TypeSyncComplete:
            response, handleErr = fs.handleSyncComplete(peerID, msg)
        default:
            log.Warn().Str("type", msg.Type).Msg("Unknown message type")
            continue
        }
        
        if handleErr != nil {
            log.Error().Err(handleErr).
                Str("msg_type", msg.Type).
                Str("peer", peerID.String()).
                Msg("Error handling message")
            
            // Send abort message
            abortMsg := &SyncMessage{
                Type:         TypeSyncAbort,
                SenderID:     fs.host.ID().String(),
                ErrorMessage: handleErr.Error(),
                Timestamp:    time.Now().Unix(),
            }
            
            writeMessage(writer, stream, abortMsg)
            break
        }
        
        if response != nil {
            if err := writeMessage(writer, stream, response); err != nil {
                log.Error().Err(err).Msg("Failed to send response")
                break
            }
        }
    }
}

// handleSyncRequest processes an initial sync request
func (fs *FastSync) handleSyncRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    // Get database states
    mainState, err := DB_OPs.GetDatabaseState(fs.mainDB)
    if err != nil {
        return nil, fmt.Errorf("failed to get main database state: %w", err)
    }
    
    accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB)
    if err != nil {
        return nil, fmt.Errorf("failed to get accounts database state: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Uint64("peer_tx_id", msg.TxID).
        Uint64("main_tx_id", mainState.TxId).
        Uint64("accounts_tx_id", accountsState.TxId).
        Msg("Received sync request")
    
    // Create sync context
    ctx, cancel := context.WithTimeout(context.Background(), SyncTimeout)
    
    // Create sync state
    state := &syncState{
        peer:           peerID,
        startTime:      time.Now(),
        mainStartTxID:  msg.TxID,
        mainEndTxID:    mainState.TxId,
        acctsStartTxID: msg.TxID,
        acctsEndTxID:   accountsState.TxId,
        mainBloom:      bloom.NewWithEstimates(BloomFilterSize, 0.01),
        acctsBloom:     bloom.NewWithEstimates(BloomFilterSize, 0.01),
        currentDB:      MainDB,
        cancel:         cancel,
    }
    
    // Store sync state
    fs.mutex.Lock()
    fs.active[peerID] = state
    fs.mutex.Unlock()
    
    // Calculate total batches
    mainBatchCount := calculateBatchCount(msg.TxID, mainState.TxId)
    acctsBatchCount := calculateBatchCount(msg.TxID, accountsState.TxId)
    totalBatches := mainBatchCount + acctsBatchCount
    
    state.batches = totalBatches
    
    // Create database states array for response
    dbStates := []DBState{
        {
            Type:       MainDB,
            TxID:       mainState.TxId,
            MerkleRoot: mainState.TxHash,
        },
        {
            Type:       AccountsDB,
            TxID:       accountsState.TxId,
            MerkleRoot: accountsState.TxHash,
        },
    }
    
    statesData, err := json.Marshal(dbStates)
    if err != nil {
        cancel()
        return nil, fmt.Errorf("failed to marshal DB states: %w", err)
    }
    
    // Monitor timeout
    go func() {
        defer cancel()
        <-ctx.Done()
        if ctx.Err() == context.DeadlineExceeded {
            log.Warn().
                Str("peer", peerID.String()).
                Dur("elapsed", time.Since(state.startTime)).
                Msg("Sync timed out")
            
            fs.cleanupSync(peerID)
        }
    }()
    
    // Return sync response
    return &SyncMessage{
        Type:         TypeSyncResponse,
        SenderID:     fs.host.ID().String(),
        TxID:         mainState.TxId,
        MerkleRoot:   mainState.TxHash,
        TotalBatches: totalBatches,
        Data:         statesData,
        Timestamp:    time.Now().Unix(),
    }, nil
}

// Calculate number of batches needed
func calculateBatchCount(startTxID, endTxID uint64) int {
    if startTxID >= endTxID {
        return 1 // At least one batch even if no diff
    }
    
    txDiff := endTxID - startTxID
    return int((txDiff + SyncBatchSize - 1) / SyncBatchSize)
}

// handleBloomFilter processes bloom filter exchange
func (fs *FastSync) handleBloomFilter(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    fs.mutex.RLock()
    state, exists := fs.active[peerID]
    fs.mutex.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("no active sync for peer %s", peerID)
    }
    
    // Deserialize peer's bloom filter
    peerBloom := &bloom.BloomFilter{}
    if err := peerBloom.UnmarshalBinary(msg.BloomFilter); err != nil {
        return nil, fmt.Errorf("failed to deserialize bloom filter: %w", err)
    }
    
    // Store in the appropriate bloom filter
    if msg.DBType == AccountsDB {
        state.acctsBloom = peerBloom
    } else {
        state.mainBloom = peerBloom
    }
    
    // Get database for current type
    db, _ := fs.getDB(msg.DBType)
    
    // Build our bloom filter
    ourBloom := bloom.NewWithEstimates(BloomFilterSize, 0.01)
    
    // Get all keys from database
    keys, err := fs.getAllKeys(db)
    if err != nil {
        return nil, fmt.Errorf("failed to get keys: %w", err)
    }
    
    // Add keys to filter
    for _, key := range keys {
        addToBloomFilter(ourBloom, key)
    }
    
    // Serialize filter
    filterBytes, err := ourBloom.MarshalBinary()
    if err != nil {
        return nil, fmt.Errorf("failed to serialize bloom filter: %w", err)
    }
    
    // Return our filter
    return &SyncMessage{
        Type:        TypeBloomFilterExchange,
        SenderID:    fs.host.ID().String(),
        BloomFilter: filterBytes,
        KeysCount:   len(keys),
        DBType:      msg.DBType,
        Timestamp:   time.Now().Unix(),
    }, nil
}

// getAllKeys retrieves all keys from a database
func (fs *FastSync) getAllKeys(db *config.ImmuClient) ([]string, error) {
    const maxKeysPerBatch = 2000
    var allKeys []string
    var lastKey string
    
    for {
        keys, err := DB_OPs.GetKeys(db, lastKey, maxKeysPerBatch)
        if err != nil {
            return nil, fmt.Errorf("failed to get keys: %w", err)
        }
        
        allKeys = append(allKeys, keys...)
        
        // Check if we've reached the end
        if len(keys) < maxKeysPerBatch {
            break
        }
        
        // Set last key for next batch
        lastKey = keys[len(keys)-1]
    }
    
    return allKeys, nil
}

// handleBatchRequest processes a batch request
func (fs *FastSync) handleBatchRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    fs.mutex.RLock()
    state, exists := fs.active[peerID]
    fs.mutex.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("no active sync for peer %s", peerID)
    }
    
    // Get database and bloom filter
    db, crdtEngine := fs.getDB(msg.DBType)
    filter := state.mainBloom
    if msg.DBType == AccountsDB {
        filter = state.acctsBloom
    }
    
    // Get batch data
    entries, crdts, err := fs.getBatchData(db, crdtEngine, filter)
    if err != nil {
        return nil, fmt.Errorf("failed to get batch data: %w", err)
    }
    
    // Limit entries to avoid overly large responses
    const maxEntriesPerBatch = 100
    if len(entries) > maxEntriesPerBatch {
        log.Warn().
            Int("total_entries", len(entries)).
            Int("limited_to", maxEntriesPerBatch).
            Msg("Limiting batch size to avoid data truncation")
        entries = entries[:maxEntriesPerBatch]
    }
    
    // Limit CRDTs too
    const maxCRDTsPerBatch = 50
    if len(crdts) > maxCRDTsPerBatch {
        log.Warn().
            Int("total_crdts", len(crdts)).
            Int("limited_to", maxCRDTsPerBatch).
            Msg("Limiting CRDT batch size to avoid data truncation")
        crdts = crdts[:maxCRDTsPerBatch]
    }
    
    // Create batch data
    batchData := BatchData{
        Entries: entries,
        CRDTs:   crdts,
        DBType:  msg.DBType,
    }
    
    // Serialize data
    dataBytes, err := json.Marshal(batchData)
    if err != nil {
        return nil, fmt.Errorf("failed to serialize batch data: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Int("batch", msg.BatchNumber).
        Int("entries", len(entries)).
        Int("crdts", len(crdts)).
        Str("db", dbTypeToString(msg.DBType)).
        Msg("Sending batch data")
    
    // Send batch data
    return &SyncMessage{
        Type:        TypeBatchData,
        SenderID:    fs.host.ID().String(),
        BatchNumber: msg.BatchNumber,
        Data:        dataBytes,
        DBType:      msg.DBType,
        Timestamp:   time.Now().Unix(),
    }, nil
}

// getBatchData retrieves data for a batch
func (fs *FastSync) getBatchData(db *config.ImmuClient, crdtEngine *crdt.Engine, peerBloom *bloom.BloomFilter) ([]KeyValueEntry, []json.RawMessage, error) {
    var entries []KeyValueEntry
    var crdts []json.RawMessage
    
    // Get all keys
    keys, err := fs.getAllKeys(db)
    if err != nil {
        return nil, nil, err
    }
    
    // Process keys
    for _, key := range keys {
        // Skip if peer already has this key
        if peerBloom != nil && peerBloom.Test([]byte(key)) {
            continue
        }
        
        // Get key data
        data, err := DB_OPs.Read(db, key)
        if err != nil {
            // Skip if not found
            continue
        }
        
        // Check if CRDT
        if crdtEngine.IsCRDT(key) {
            crdtWrapper, err := fs.serializeCRDT(crdtEngine, key, data)
            if err != nil {
                log.Error().Err(err).Str("key", key).Msg("Failed to serialize CRDT")
                continue
            }
            crdts = append(crdts, crdtWrapper)
        } else {
            // Regular key-value
            entries = append(entries, KeyValueEntry{
                Key:       []byte(key),
                Value:     data,
                Timestamp: time.Now(),
                TxID:      0,
            })
        }
    }
    
    return entries, crdts, nil
}

// serializeCRDT prepares a CRDT for transmission
func (fs *FastSync) serializeCRDT(crdtEngine *crdt.Engine, key string, data []byte) (json.RawMessage, error) {
    // Deserialize the CRDT
    crdtValue, err := crdtEngine.DeserializeCRDT(key, data, nil)
    if err != nil {
        return nil, err
    }
    
    // Determine CRDT type
    crdtType := "unknown"
    switch crdtValue.(type) {
    case *crdt.LWWSet:
        crdtType = "lww-set"
    case *crdt.Counter:
        crdtType = "counter"
    }
    
    // Serialize CRDT
    crdtJSON, err := json.Marshal(crdtValue)
    if err != nil {
        return nil, err
    }
    
    // Create wrapper with type
    wrapper := map[string]json.RawMessage{
        "type": json.RawMessage(fmt.Sprintf(`"%s"`, crdtType)),
        "data": crdtJSON,
    }
    
    // Serialize wrapper
    return json.Marshal(wrapper)
}

// handleBatchData processes received batch data
func (fs *FastSync) handleBatchData(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    fs.mutex.RLock()
    state, exists := fs.active[peerID]
    fs.mutex.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("no active sync for peer %s", peerID)
    }
    
    // Parse batch data
    var batchData BatchData
    if err := json.Unmarshal(msg.Data, &batchData); err != nil {
        return nil, fmt.Errorf("failed to parse batch data: %w", err)
    }
    
    // Get database and CRDT engine
    db, crdtEngine := fs.getDB(batchData.DBType)
    
    // Process entries
    if err := fs.storeEntries(db, batchData.Entries); err != nil {
        return nil, fmt.Errorf("failed to store entries: %w", err)
    }
    
    // Process CRDTs
    if err := fs.storeCRDTs(crdtEngine, batchData.CRDTs); err != nil {
        return nil, fmt.Errorf("failed to store CRDTs: %w", err)
    }
    
    // Update sync state
    fs.mutex.Lock()
    state.completed++
    progress := float64(state.completed) / float64(state.batches) * 100
    fs.mutex.Unlock()
    
    log.Info().
        Str("peer", peerID.String()).
        Int("batch", msg.BatchNumber).
        Int("completed", state.completed).
        Int("total", state.batches).
        Float64("progress", progress).
        Str("db", dbTypeToString(batchData.DBType)).
        Msg("Processed batch data")
    
    // Check if we've completed all batches
    var response *SyncMessage
    if state.completed >= state.batches {
        response = &SyncMessage{
            Type:      TypeVerificationRequest,
            SenderID:  fs.host.ID().String(),
            Timestamp: time.Now().Unix(),
        }
    } else {
        response = &SyncMessage{
            Type:        TypeBatchData,
            SenderID:    fs.host.ID().String(),
            BatchNumber: msg.BatchNumber,
            Success:     true,
            DBType:      batchData.DBType,
            Timestamp:   time.Now().Unix(),
        }
    }
    
    return response, nil
}

// storeEntries stores key-value entries
func (fs *FastSync) storeEntries(db *config.ImmuClient, entries []KeyValueEntry) error {
    if len(entries) == 0 {
        return nil
    }
    
    // Convert to map
    entriesMap := make(map[string]interface{})
    for _, entry := range entries {
        entriesMap[string(entry.Key)] = entry.Value
    }
    
    // Store with retry
    return retry(func() error {
        return DB_OPs.BatchCreate(db, entriesMap)
    })
}

// storeCRDTs processes and stores CRDTs
func (fs *FastSync) storeCRDTs(crdtEngine *crdt.Engine, crdtData []json.RawMessage) error {
    if len(crdtData) == 0 {
        return nil
    }
    
    for _, crdtBytes := range crdtData {
        // Parse wrapper
        var wrapper map[string]json.RawMessage
        if err := json.Unmarshal(crdtBytes, &wrapper); err != nil {
            log.Error().Err(err).Msg("Failed to unmarshal CRDT wrapper")
            continue
        }
        
        // Get type
        var crdtType string
        if err := json.Unmarshal(wrapper["type"], &crdtType); err != nil {
            log.Error().Err(err).Msg("Failed to unmarshal CRDT type")
            continue
        }
        
        // Create CRDT
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
        
        // Unmarshal data
        if err := json.Unmarshal(wrapper["data"], crdtValue); err != nil {
            log.Error().Err(err).Str("type", crdtType).Msg("Failed to unmarshal CRDT data")
            continue
        }
        
        // Merge CRDT
        mergedCRDT, err := crdtEngine.MergeCRDT(crdtValue)
        if err != nil {
            log.Error().Err(err).Str("key", crdtValue.GetKey()).Msg("Failed to merge CRDT")
            continue
        }
        
        // Store with retry
        err = retry(func() error {
            return crdtEngine.StoreCRDT(mergedCRDT)
        })
        
        if err != nil {
            log.Error().Err(err).Str("key", crdtValue.GetKey()).Msg("Failed to store CRDT")
        }
    }
    
    return nil
}

// handleVerification processes a verification request
func (fs *FastSync) handleVerification(peerID peer.ID) (*SyncMessage, error) {
    // Get database states
    mainState, err := DB_OPs.GetDatabaseState(fs.mainDB)
    if err != nil {
        return nil, fmt.Errorf("failed to get main database state: %w", err)
    }
    
    accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB)
    if err != nil {
        return nil, fmt.Errorf("failed to get accounts database state: %w", err)
    }
    
    // Create database states array
    dbStates := []DBState{
        {
            Type:       MainDB,
            TxID:       mainState.TxId,
            MerkleRoot: mainState.TxHash,
        },
        {
            Type:       AccountsDB,
            TxID:       accountsState.TxId,
            MerkleRoot: accountsState.TxHash,
        },
    }
    
    statesData, err := json.Marshal(dbStates)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal DB states: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Uint64("main_tx_id", mainState.TxId).
        Uint64("accounts_tx_id", accountsState.TxId).
        Msg("Sending verification data")
    
    return &SyncMessage{
        Type:       TypeVerificationResult,
        SenderID:   fs.host.ID().String(),
        TxID:       mainState.TxId,
        MerkleRoot: mainState.TxHash,
        Data:       statesData,
        Timestamp:  time.Now().Unix(),
    }, nil
}

// handleSyncComplete processes a sync complete message
func (fs *FastSync) handleSyncComplete(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
    // Cleanup sync state
    fs.cleanupSync(peerID)
    
    // Get database states
    mainState, err := DB_OPs.GetDatabaseState(fs.mainDB)
    if err != nil {
        return nil, fmt.Errorf("failed to get main database state: %w", err)
    }
    
    // Create response
    return &SyncMessage{
        Type:       TypeSyncComplete,
        SenderID:   fs.host.ID().String(),
        Success:    true,
        MerkleRoot: mainState.TxHash,
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

// StartSync initiates synchronization with a peer
func (fs *FastSync) StartSync(peerID peer.ID) error {
    // Get database states
    mainState, err := DB_OPs.GetDatabaseState(fs.mainDB)
    if err != nil {
        return fmt.Errorf("failed to get main database state: %w", err)
    }
    
    accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB)
    if err != nil {
        return fmt.Errorf("failed to get accounts database state: %w", err)
    }
    
    log.Info().
        Str("peer", peerID.String()).
        Uint64("main_tx_id", mainState.TxId).
        Uint64("accounts_tx_id", accountsState.TxId).
        Msg("Starting sync with peer")
    
    // Open stream
    ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
    defer cancel()
    
    stream, err := fs.host.NewStream(ctx, peerID, SyncProtocolID)
    if err != nil {
        return fmt.Errorf("failed to open stream: %w", err)
    }
    defer stream.Close()
    
    // Setup reader/writer
    reader := bufio.NewReader(stream)
    writer := bufio.NewWriter(stream)
    
    // Send sync request
    syncReq := SyncMessage{
        Type:      TypeSyncRequest,
        SenderID:  fs.host.ID().String(),
        TxID:      mainState.TxId,
        Timestamp: time.Now().Unix(),
    }
    
    if err := writeMessage(writer, stream, &syncReq); err != nil {
        return fmt.Errorf("failed to send sync request: %w", err)
    }
    
    // Read response
    response, err := readMessage(reader, stream)
    if err != nil {
        return fmt.Errorf("failed to read sync response: %w", err)
    }
    
    // Process response
    switch response.Type {
    case TypeSyncResponse:
        return fs.processSync(peerID, stream, reader, writer, response)
    case TypeSyncComplete:
        log.Info().Msg("Peer reports we're already in sync")
        return nil
    default:
        return fmt.Errorf("unexpected response type: %s", response.Type)
    }
}

// processSync handles the sync process
func (fs *FastSync) processSync(peerID peer.ID, stream network.Stream, reader *bufio.Reader, writer *bufio.Writer, response *SyncMessage) error {
    // Parse database states
    var dbStates []DBState
    if err := json.Unmarshal(response.Data, &dbStates); err != nil {
        return fmt.Errorf("failed to parse database states: %w", err)
    }
    
    if len(dbStates) != 2 {
        return fmt.Errorf("expected 2 database states, got %d", len(dbStates))
    }
    
    // Process each database
    for _, dbState := range dbStates {
        // Get our current state
        db, _ := fs.getDB(dbState.Type)
        ourState, err := DB_OPs.GetDatabaseState(db)
        if err != nil {
            return fmt.Errorf("failed to get our %s state: %w", dbTypeToString(dbState.Type), err)
        }
        
        // Skip if we're already up to date
        if ourState.TxId >= dbState.TxID {
            log.Info().
                Str("db", dbTypeToString(dbState.Type)).
                Msg("Already up to date for this database")
            continue
        }
        
        // Exchange bloom filters
        if err := fs.exchangeBloomFilters(stream, reader, writer, dbState.Type); err != nil {
            return fmt.Errorf("failed to exchange bloom filters for %s: %w", dbTypeToString(dbState.Type), err)
        }
        
        // Calculate batches
        batchCount := calculateBatchCount(ourState.TxId, dbState.TxID)
        
        // Process batches
        for i := 0; i < batchCount; i++ {
            startTx := ourState.TxId + uint64(i*SyncBatchSize)
            endTx := startTx + uint64(SyncBatchSize-1)
            if endTx > dbState.TxID {
                endTx = dbState.TxID
            }
            
            // Request and process batch
            if err := fs.requestBatch(stream, reader, writer, i, startTx, endTx, dbState.Type); err != nil {
                return fmt.Errorf("failed to process batch %d for %s: %w", 
                    i, dbTypeToString(dbState.Type), err)
            }
            
            log.Info().
                Int("batch", i+1).
                Int("total", batchCount).
                Str("db", dbTypeToString(dbState.Type)).
                Float64("progress", float64(i+1)/float64(batchCount)*100).
                Msg("Batch processed")
        }
    }
    
    // Request verification
    verifyReq := SyncMessage{
        Type:      TypeVerificationRequest,
        SenderID:  fs.host.ID().String(),
        Timestamp: time.Now().Unix(),
    }
    
    if err := writeMessage(writer, stream, &verifyReq); err != nil {
        return fmt.Errorf("failed to send verification request: %w", err)
    }
    
    // Read verification result
    verifyResp, err := readMessage(reader, stream)
    if err != nil {
        return fmt.Errorf("failed to read verification result: %w", err)
    }
    
    if verifyResp.Type != TypeVerificationResult {
        return fmt.Errorf("unexpected message type: %s", verifyResp.Type)
    }
    
    // Parse verification data
    var verifyStates []DBState
    if err := json.Unmarshal(verifyResp.Data, &verifyStates); err != nil {
        return fmt.Errorf("failed to parse verification states: %w", err)
    }
    
    // Verify states match
    mainState, err := DB_OPs.GetDatabaseState(fs.mainDB)
    if err != nil {
        return fmt.Errorf("failed to get main database state: %w", err)
    }
    
    accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB)
    if err != nil {
        return fmt.Errorf("failed to get accounts database state: %w", err)
    }
    
    // Check if hashes match
    mainMatch := bytes.Equal(mainState.TxHash, verifyStates[0].MerkleRoot)
    accountsMatch := bytes.Equal(accountsState.TxHash, verifyStates[1].MerkleRoot)
    
    log.Info().
        Bool("main_match", mainMatch).
        Bool("accounts_match", accountsMatch).
        Msg("Sync verification completed")
    
    // Send completion
    completeMsg := SyncMessage{
        Type:      TypeSyncComplete,
        SenderID:  fs.host.ID().String(),
        Success:   mainMatch && accountsMatch,
        Timestamp: time.Now().Unix(),
    }
    
    if err := writeMessage(writer, stream, &completeMsg); err != nil {
        return fmt.Errorf("failed to send completion message: %w", err)
    }
    
    if !mainMatch || !accountsMatch {
        return fmt.Errorf("verification failed: database hashes don't match")
    }
    
    log.Info().Msg("Sync completed successfully")
    return nil
}

// exchangeBloomFilters exchanges bloom filters for a database
func (fs *FastSync) exchangeBloomFilters(stream network.Stream, reader *bufio.Reader, writer *bufio.Writer, dbType DatabaseType) error {
    // Create bloom filter
    bloomFilter := bloom.NewWithEstimates(BloomFilterSize, 0.01)
    
    // Get database
    db, _ := fs.getDB(dbType)
    
    // Get all our keys
    keys, err := fs.getAllKeys(db)
    if err != nil {
        return fmt.Errorf("failed to get keys: %w", err)
    }
    
    // Add to filter
    for _, key := range keys {
        addToBloomFilter(bloomFilter, key)
    }
    
    // Serialize filter
    filterBytes, err := bloomFilter.MarshalBinary()
    if err != nil {
        return fmt.Errorf("failed to serialize bloom filter: %w", err)
    }
    
    // Send filter
    bloomMsg := SyncMessage{
        Type:        TypeBloomFilterExchange,
        SenderID:    fs.host.ID().String(),
        BloomFilter: filterBytes,
        KeysCount:   len(keys),
        DBType:      dbType,
        Timestamp:   time.Now().Unix(),
    }
    
    if err := writeMessage(writer, stream, &bloomMsg); err != nil {
        return fmt.Errorf("failed to send bloom filter: %w", err)
    }
    
    // Read peer's filter
    peerBloomMsg, err := readMessage(reader, stream)
    if err != nil {
        return fmt.Errorf("failed to read peer's bloom filter: %w", err)
    }
    
    // Check for abort message
    if peerBloomMsg.Type == TypeSyncAbort {
        return fmt.Errorf("peer aborted during bloom filter exchange: %s", peerBloomMsg.ErrorMessage)
    }
    
    if peerBloomMsg.Type != TypeBloomFilterExchange || peerBloomMsg.DBType != dbType {
        return fmt.Errorf("unexpected message type or DB type for bloom filter")
    }
    
    
    log.Info().
        Str("db", dbTypeToString(dbType)).
        Int("our_keys", len(keys)).
        Int("peer_keys", peerBloomMsg.KeysCount).
        Msg("Bloom filters exchanged")
    
    return nil
}

// requestBatch requests and processes a batch
func (fs *FastSync) requestBatch(stream network.Stream, reader *bufio.Reader, writer *bufio.Writer, batchNum int, startTx, endTx uint64, dbType DatabaseType) error {
    // Create request
    batchReq := SyncMessage{
        Type:        TypeBatchRequest,
        SenderID:    fs.host.ID().String(),
        BatchNumber: batchNum,
        StartTxID:   startTx,
        EndTxID:     endTx,
        DBType:      dbType,
        Timestamp:   time.Now().Unix(),
    }
    
    // Send request
    if err := writeMessage(writer, stream, &batchReq); err != nil {
        return fmt.Errorf("failed to send batch request: %w", err)
    }
    
    // Read response
    batchResp, err := readMessage(reader, stream)
    if err != nil {
        if strings.Contains(err.Error(), "unexpected end of JSON input") {
            return fmt.Errorf("received truncated data - try reducing batch size: %w", err)
        }
        return fmt.Errorf("failed to read batch data: %w", err)
    }
    // Check for abort message
    if batchResp.Type == TypeSyncAbort {
        return fmt.Errorf("peer aborted sync: %s", batchResp.ErrorMessage)
    }
    
    // Check response
    if batchResp.Type != TypeBatchData {
        return fmt.Errorf("unexpected response type: %s", batchResp.Type)
    }
    
    // Parse batch data
    var batchData BatchData
    if err := json.Unmarshal(batchResp.Data, &batchData); err != nil {
        return fmt.Errorf("failed to parse batch data: %w", err)
    }
    
    // Get database and CRDT engine
    db, crdtEngine := fs.getDB(dbType)
    
    // Process data
    if err := fs.storeEntries(db, batchData.Entries); err != nil {
        return fmt.Errorf("failed to store entries: %w", err)
    }
    
    if err := fs.storeCRDTs(crdtEngine, batchData.CRDTs); err != nil {
        return fmt.Errorf("failed to store CRDTs: %w", err)
    }
    
    // Send acknowledgement
    ackMsg := SyncMessage{
        Type:        TypeBatchData,
        SenderID:    fs.host.ID().String(),
        BatchNumber: batchNum,
        Success:     true,
        DBType:      dbType,
        Timestamp:   time.Now().Unix(),
    }
    
    if err := writeMessage(writer, stream, &ackMsg); err != nil {
        return fmt.Errorf("failed to send batch acknowledgement: %w", err)
    }
    
    return nil
}

// addToBloomFilter adds a key to a bloom filter
func addToBloomFilter(filter *bloom.BloomFilter, key string) {
    if len(key) == 0 {
        return
    }
    
    // Hash the key for better distribution
    hasher := sha256.New()
    hasher.Write([]byte(key))
    hash := hasher.Sum(nil)
    
    filter.Add(hash)
}

// dbTypeToString converts a database type to a string
func dbTypeToString(dbType DatabaseType) string {
    switch dbType {
    case MainDB:
        return "Main"
    case AccountsDB:
        return "Accounts"
    default:
        return "Unknown"
    }
}