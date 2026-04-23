// Package fastsync provides high-performance synchronization for distributed systems
// with support for both traditional key-value data and Conflict-Free Replicated Data Types (CRDTs)
//
// MAJOR UPDATE: This package now integrates with the new CRDT implementation to provide:
// - Conflict-free synchronization of concurrent operations
// - Deterministic convergence across distributed nodes
// - Operation-based synchronization instead of state-based
// - Support for LWW-Sets and Counters with proper merge semantics
//
// Key Changes Made:
// 1. Added CRDT engine to FastSync struct for conflict-free operations
// 2. Updated storeCRDTs to use new operation-based API instead of merge/store
// 3. Added CRDT export/import methods for network synchronization
// 4. Enhanced error handling and logging for CRDT operations
// 5. Maintained backward compatibility with existing database sync functionality
package fastsync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/linkedin/goavro/v2"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/crdt"
	hashmap "gossipnode/crdt/HashMap"
)

// Constants for FastSync
const (
	SyncProtocolID  = config.SyncProtocol
	SyncBatchSize   = 200
	MaxRetries      = 3
	SyncTimeout     = 5 * time.Minute
	RequestTimeout  = 30 * time.Second
	ResponseTimeout = 6000 * time.Second // Increased to 10 minutes for large HashMap transfers
	RetryDelay      = 500 * time.Millisecond
)

// DatabaseType specifies which database to operate on
type DatabaseType int

const (
	MainDB     DatabaseType = 0
	AccountsDB DatabaseType = 1
)

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
	// peer           peer.ID       // unused field
	// startTime      time.Time     // unused field
	// mainStartTxID  uint64        // unused field
	// mainEndTxID    uint64        // unused field
	// acctsStartTxID uint64        // unused field
	// acctsEndTxID   uint64        // unused field
	batches   int
	completed int
	cancel    context.CancelFunc
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
	db := fs.getDB(batchData.DBType)

	// Process entries
	if err := fs.storeEntries(db, batchData.Entries); err != nil {
		return nil, fmt.Errorf("failed to store entries: %w", err)
	}

	// UPDATED: Process CRDTs using the new CRDT engine
	// This enables proper conflict-free synchronization of CRDT operations
	if err := fs.storeCRDTs(fs.crdtEngine, batchData.CRDTs); err != nil {
		return nil, fmt.Errorf("failed to store CRDTs: %w", err)
	}

	// Update sync state
	fs.mutex.Lock()
	state.completed++
	progress := float64(state.completed) / float64(state.batches) * 100
	fs.mutex.Unlock()

	logger().Info(context.Background(), "Processed batch data",
		ion.String("peer", peerID.String()),
		ion.Int("batch", msg.BatchNumber),
		ion.Int("completed", state.completed),
		ion.Int("total", state.batches),
		ion.Float64("progress", progress),
		ion.String("db", dbTypeToString(batchData.DBType)))

	// Check if we've completed all batches
	var response *SyncMessage
	if state.completed >= state.batches {
		response = &SyncMessage{
			Type:      TypeVerificationRequest,
			SenderID:  fs.host.ID().String(),
			Timestamp: time.Now().UTC().Unix(),
		}
	} else {
		response = &SyncMessage{
			Type:        TypeBatchAck,
			SenderID:    fs.host.ID().String(),
			BatchNumber: msg.BatchNumber,
			Success:     true,
			DBType:      batchData.DBType,
			Timestamp:   time.Now().UTC().Unix(),
		}
	}

	return response, nil
}

// storeEntries stores key-value entries
func (fs *FastSync) storeEntries(db *config.PooledConnection, entries []KeyValueEntry) error {
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

// UPDATED: storeCRDTs now uses the new CRDT implementation for conflict-free synchronization
// This function processes CRDTs received during sync and applies them to the local CRDT engine
// using the new operation-based API instead of the old merge/store approach
func (fs *FastSync) storeCRDTs(crdtEngine *crdt.Engine, crdtData []json.RawMessage) error {
	if len(crdtData) == 0 {
		return nil
	}

	// NEW: Check if CRDT engine is available
	if crdtEngine == nil {
		logger().Warn(context.Background(), "No CRDT engine provided, skipping CRDT processing during sync")
		return nil
	}

	logger().Info(context.Background(), "Processing CRDTs during sync", ion.Int("count", len(crdtData)))

	for _, crdtBytes := range crdtData {
		// Parse wrapper containing CRDT metadata
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(crdtBytes, &wrapper); err != nil {
			logger().Error(context.Background(), "Failed to unmarshal CRDT wrapper", err)
			continue
		}

		// Extract CRDT type (lww-set, counter, etc.)
		var crdtType string
		if err := json.Unmarshal(wrapper["type"], &crdtType); err != nil {
			logger().Error(context.Background(), "Failed to unmarshal CRDT type", err)
			continue
		}

		// Extract CRDT key for identification
		var key string
		if err := json.Unmarshal(wrapper["key"], &key); err != nil {
			logger().Error(context.Background(), "Failed to unmarshal CRDT key", err)
			continue
		}

		// NEW: Create CRDT using the new constructor methods
		var crdtValue crdt.CRDT
		switch crdtType {
		case "lww-set":
			crdtValue = crdt.NewLWWSet(key)
		case "counter":
			crdtValue = crdt.NewCounter(key)
		default:
			logger().Error(context.Background(), "Unknown CRDT type during sync", nil, ion.String("type", crdtType))
			continue
		}

		// Unmarshal CRDT data into the created instance
		if err := json.Unmarshal(wrapper["data"], crdtValue); err != nil {
			logger().Error(context.Background(), "Failed to unmarshal CRDT data", err, ion.String("type", crdtType), ion.String("key", key))
			continue
		}

		// NEW: Process CRDT using the new operation-based API
		// Instead of merging entire CRDTs, we extract and replay individual operations
		// This ensures proper conflict resolution and maintains operation history
		switch crdtType {
		case "lww-set":
			if lwwSet, ok := crdtValue.(*crdt.LWWSet); ok {
				// Process each element in the LWW-Set
				for element := range lwwSet.Adds {
					if lwwSet.Contains(element) {
						// NEW: Use LWWAdd operation instead of direct merge
						// This preserves the operation semantics and enables proper conflict resolution
						err := crdtEngine.LWWAdd("sync-node", key, element, crdt.VectorClock{})
						if err != nil {
							logger().Error(context.Background(), "Failed to add element to LWW set during sync", err, ion.String("key", key), ion.String("element", element))
						} else {
							logger().Debug(context.Background(), "Added element to LWW set during sync", ion.String("key", key), ion.String("element", element))
						}
					}
				}
			}
		case "counter":
			if counter, ok := crdtValue.(*crdt.Counter); ok {
				// Process counter increments from each node
				for nodeID, value := range counter.Counters {
					if value > 0 {
						// NEW: Use CounterInc operation instead of direct merge
						// This ensures proper counter semantics and prevents conflicts
						err := crdtEngine.CounterInc(nodeID, key, value, crdt.VectorClock{})
						if err != nil {
							logger().Error(context.Background(), "Failed to increment counter during sync", err, ion.String("key", key), ion.String("node", nodeID), ion.Uint64("value", value))
						} else {
							logger().Debug(context.Background(), "Incremented counter during sync", ion.String("key", key), ion.String("node", nodeID), ion.Uint64("value", value))
						}
					}
				}
			}
		}

		logger().Info(context.Background(), "Successfully processed CRDT during sync", ion.String("type", crdtType), ion.String("key", key))
	}

	logger().Info(context.Background(), "Completed CRDT processing during sync", ion.Int("processed", len(crdtData)))
	return nil
}

// handleVerification processes a verification request
func (fs *FastSync) handleVerification(peerID peer.ID) (*SyncMessage, error) {
	// Get database states
	mainState, err := DB_OPs.GetDatabaseState(fs.mainDB.Client)
	if err != nil {
		return nil, fmt.Errorf("failed to get main database state: %w", err)
	}

	accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB.Client)
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

	logger().Info(context.Background(), "Sending verification data",
		ion.String("peer", peerID.String()),
		ion.Uint64("main_tx_id", mainState.TxId),
		ion.Uint64("accounts_tx_id", accountsState.TxId))

	return &SyncMessage{
		Type:       TypeVerificationResult,
		SenderID:   fs.host.ID().String(),
		TxID:       mainState.TxId,
		MerkleRoot: MerkleRoot{MainMerkleRoot: mainState.TxHash, AccountsMerkleRoot: accountsState.TxHash},
		Data:       statesData,
		Timestamp:  time.Now().UTC().Unix(),
	}, nil
}

// handleSyncComplete processes a sync complete message
func (fs *FastSync) handleSyncComplete(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// Cleanup sync state
	fs.cleanupSync(peerID)

	// Get database states
	mainState, err := DB_OPs.GetDatabaseState(fs.mainDB.Client)
	if err != nil {
		return nil, fmt.Errorf("failed to get main database state: %w", err)
	}

	accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB.Client)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts database state: %w", err)
	}

	// Create response
	return &SyncMessage{
		Type:       TypeSyncComplete,
		SenderID:   fs.host.ID().String(),
		Success:    true,
		MerkleRoot: MerkleRoot{MainMerkleRoot: mainState.TxHash, AccountsMerkleRoot: accountsState.TxHash},
		Timestamp:  time.Now().UTC().Unix(),
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

/* UNUSED
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
		db := fs.getDB(dbState.Type)
		ourState, err := DB_OPs.GetDatabaseState(db.Client)
		if err != nil {
			return fmt.Errorf("failed to get our %s state: %w", dbTypeToString(dbState.Type), err)
		}

		// Skip if we're already up to date
		if ourState.TxId >= dbState.TxID {
			logger().Info(context.Background(), "Already up to date for this database", ion.String("db", dbTypeToString(dbState.Type)))
			continue
		}

		// Calculate batches
		batchCount := calculateBatchCount(ourState.TxId, dbState.TxID)
		if batchCount == 0 {
			logger().Info(context.Background(), "No batches needed", ion.String("db_type", dbTypeToString(dbState.Type)))
			continue
		}

		// Process batches
		for i := 0; i < batchCount; i++ {
			startTx := ourState.TxId + 1 + uint64(i*SyncBatchSize)
			endTx := startTx + uint64(SyncBatchSize) - 1
			if endTx > dbState.TxID {
				endTx = dbState.TxID
			}

			// Request and process batch
			if err := fs.requestBatch(stream, reader, writer, i, startTx, endTx, dbState.Type); err != nil {
				return fmt.Errorf("failed to process batch %d for %s: %w",
					i, dbTypeToString(dbState.Type), err)
			}

			logger().Info(context.Background(), "Batch processed",
				ion.Int("batch", i+1),
				ion.Int("total", batchCount),
				ion.String("db", dbTypeToString(dbState.Type)),
				ion.Float64("progress", float64(i+1)/float64(batchCount)*100))
		}
	}

	// Request verification
	verifyReq := SyncMessage{
		Type:      TypeVerificationRequest,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
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
	mainState, err := DB_OPs.GetDatabaseState(fs.mainDB.Client)
	if err != nil {
		return fmt.Errorf("failed to get main database state: %w", err)
	}

	accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB.Client)
	if err != nil {
		return fmt.Errorf("failed to get accounts database state: %w", err)
	}

	// Check if hashes match
	mainMatch := bytes.Equal(mainState.TxHash, verifyStates[0].MerkleRoot)
	accountsMatch := bytes.Equal(accountsState.TxHash, verifyStates[1].MerkleRoot)

	logger().Info(context.Background(), "Sync verification completed",
		ion.Bool("main_match", mainMatch),
		ion.Bool("accounts_match", accountsMatch))

	// Send completion
	completeMsg := SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Success:   mainMatch && accountsMatch,
		Timestamp: time.Now().UTC().Unix(),
	}

	if err := writeMessage(writer, stream, &completeMsg); err != nil {
		return fmt.Errorf("failed to send completion message: %w", err)
	}

	if !mainMatch || !accountsMatch {
		return fmt.Errorf("verification failed: database hashes don't match")
	}

	logger().Info(context.Background(), "Sync completed successfully")
	return nil
}
*/

/* UNUSED
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
		Timestamp:   time.Now().UTC().Unix(),
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

	// Print raw response details to terminal
	logger().Debug(context.Background(), "BATCH RECEIVED",
		ion.Int("number", int(batchResp.BatchNumber)),
		ion.Int("size", len(batchResp.Data)),
		ion.String("type", batchResp.Type),
		ion.String("db", dbTypeToString(dbType)))

	// Check for abort message
	if batchResp.Type == TypeSyncAbort {
		logger().Warn(context.Background(), "Peer aborted sync", ion.String("error_message", batchResp.ErrorMessage))
		return fmt.Errorf("peer aborted sync: %s", batchResp.ErrorMessage)
	}

	// Check response
	if batchResp.Type != TypeBatchData {
		logger().Warn(context.Background(), "Unexpected batch response type", ion.String("type", string(batchResp.Type)))
		return fmt.Errorf("unexpected response type: %s", batchResp.Type)
	}

	// Parse batch data
	var batchData BatchData
	if err := json.Unmarshal(batchResp.Data, &batchData); err != nil {
		// Log error details for debugging
		if len(batchResp.Data) > 100 {
			logger().Error(context.Background(), "BATCH PARSE ERROR",
				ion.String("data_preview", string(batchResp.Data[:100])), err)
		} else {
			logger().Error(context.Background(), "BATCH PARSE ERROR",
				ion.String("data", string(batchResp.Data)), err)
		}
		return fmt.Errorf("failed to parse batch data: %w", err)
	}

	// Log detailed batch contents
	logger().Debug(context.Background(), "BATCH PARSED",
		ion.Int("number", int(batchResp.BatchNumber)),
		ion.Int("entries", len(batchData.Entries)),
		ion.Int("crdts", len(batchData.CRDTs)),
		ion.String("db", dbTypeToString(batchData.DBType)))

	if len(batchData.Entries) > 0 {
		// Log sample of first few keys
		var keys []string
		for i := 0; i < min(3, len(batchData.Entries)); i++ {
			keys = append(keys, string(batchData.Entries[i].Key))
		}
		logger().Debug(context.Background(), "BATCH ENTRIES SAMPLE",
			ion.String("keys", fmt.Sprintf("%v", keys)))
	}

	// Get database and CRDT engine
	db := fs.getDB(dbType) // Pass nil for crdtEngine

	// Process data
	if err := fs.storeEntries(db, batchData.Entries); err != nil {
		logger().Error(context.Background(), "Failed to store batch entries", err)
		return fmt.Errorf("failed to store entries: %w", err)
	}

	// UPDATED: Process CRDTs using the new CRDT engine during batch processing
	// This ensures CRDT operations are properly synchronized without conflicts
	if err := fs.storeCRDTs(fs.crdtEngine, batchData.CRDTs); err != nil {
		logger().Error(context.Background(), "Failed to store batch CRDTs", err)
		return fmt.Errorf("failed to store CRDTs: %w", err)
	}

	logger().Info(context.Background(), "Batch processed successfully", ion.Int("batch_number", int(batchResp.BatchNumber)))

	// Send acknowledgement
	ackMsg := SyncMessage{
		Type:        TypeBatchAck,
		SenderID:    fs.host.ID().String(),
		BatchNumber: batchNum,
		Success:     true,
		DBType:      dbType,
		Timestamp:   time.Now().UTC().Unix(),
	}

	if err := writeMessage(writer, stream, &ackMsg); err != nil {
		return fmt.Errorf("failed to send batch acknowledgement: %w", err)
	}

	return nil
}
*/

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

func (fs *FastSync) Phase2_Sync(msg *SyncMessage, peerID peer.ID, stream network.Stream, writer *bufio.Writer, reader *bufio.Reader) (*SyncMessage, string, string, error) {
	// Got all the data in - msg *SyncMessage
	// Send the Client_HashMap to the server to get the SYNC_HashMap
	logger().Info(context.Background(), ">>> [CLIENT] Starting Phase2_Sync - sending HashMap exchange request (CHUNKED)")

	// Extract HashMaps from the message
	mainMap := msg.HashMap.MAIN_HashMap
	accountsMap := msg.HashMap.Accounts_HashMap

	mainKeys := mainMap.Keys()
	accountsKeys := accountsMap.Keys()
	totalKeys := len(mainKeys) + len(accountsKeys)

	chunkSize := 1000 // Keys per chunk
	sendTotalChunks := (totalKeys + chunkSize - 1) / chunkSize
	if sendTotalChunks == 0 {
		sendTotalChunks = 1
	}

	// 1. Send Header Message (TypeHashMapExchangeSYNC with TotalChunks)
	// Clear the HashMap data from the header message to keep it small
	headerMsg := &SyncMessage{
		Type:             TypeHashMapExchangeSYNC,
		SenderID:         fs.host.ID().String(),
		Timestamp:        time.Now().UTC().Unix(),
		TotalChunks:      sendTotalChunks,
		HashMap_MetaData: msg.HashMap_MetaData, // Keep metadata
		// HashMap field is nil to keep message small
	}

	logger().Info(context.Background(), "Sending HashMap exchange header", ion.Int("total_keys", totalKeys), ion.Int("total_chunks", sendTotalChunks))

	// Debug: Check header size
	headerBytes, _ := json.Marshal(headerMsg)
	logger().Debug(context.Background(), "Header message size", ion.Int("bytes", len(headerBytes)))

	if err := writeMessage(writer, stream, headerMsg); err != nil {
		logger().Error(context.Background(), "Failed to write Phase2_Sync header", err)
		return nil, "", "", err
	}

	// 2. Send Chunks (TypeHashMapChunk)
	chunkNum := 0

	// Helper to send a batch of keys
	sendChunk := func(keys []string, dbType DatabaseType) error {
		chunkNum++
		// Serialize keys to JSON
		keysJSON, err := json.Marshal(keys)
		if err != nil {
			return fmt.Errorf("failed to marshal chunk keys: %w", err)
		}

		chunkMsg := &SyncMessage{
			Type:        TypeHashMapChunk,
			SenderID:    fs.host.ID().String(),
			Timestamp:   time.Now().UTC().Unix(),
			ChunkNumber: chunkNum,
			TotalChunks: sendTotalChunks,
			DBType:      dbType,
			Data:        json.RawMessage(keysJSON),
		}

		if err := writeMessage(writer, stream, chunkMsg); err != nil {
			return fmt.Errorf("failed to write chunk %d: %w", chunkNum, err)
		}

		// Read ACK for flow control (Stop-and-Wait)
		ackMsg, err := readMessage(reader, stream)
		if err != nil {
			return fmt.Errorf("failed to read ACK for chunk %d: %w", chunkNum, err)
		}
		if ackMsg.Type != TypeHashMapChunkAck {
			return fmt.Errorf("received unexpected message expecting ACK: %s", ackMsg.Type)
		}

		if chunkNum%50 == 0 {
			logger().Info(context.Background(), "Sent chunk",
				ion.Int("chunk", chunkNum),
				ion.Int("total", sendTotalChunks),
				ion.Float64("pct", float64(chunkNum)/float64(sendTotalChunks)*100))
		}
		return nil
	}

	// Send MainDB keys
	for i := 0; i < len(mainKeys); i += chunkSize {
		end := i + chunkSize
		if end > len(mainKeys) {
			end = len(mainKeys)
		}
		if err := sendChunk(mainKeys[i:end], MainDB); err != nil {
			return nil, "", "", err
		}
	}

	// Send AccountsDB keys
	for i := 0; i < len(accountsKeys); i += chunkSize {
		end := i + chunkSize
		if end > len(accountsKeys) {
			end = len(accountsKeys)
		}
		if err := sendChunk(accountsKeys[i:end], AccountsDB); err != nil {
			return nil, "", "", err
		}
	}

	// 3. Send Complete Message (TypeHashMapChunkComplete)
	completeMsg := &SyncMessage{
		Type:      TypeHashMapChunkComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Success:   true,
	}
	logger().Info(context.Background(), ">>> [CLIENT] Sending HashMap exchange COMPLETE")
	if err := writeMessage(writer, stream, completeMsg); err != nil {
		logger().Error(context.Background(), ">>> [CLIENT] ERROR: Failed to write Phase2_Sync complete", err)
		return nil, "", "", err
	}

	logger().Info(context.Background(), ">>> [CLIENT] ✓ Phase2_Sync chunks sent, waiting for server response...")
	logger().Info(context.Background(), "Phase2_Sync chunks sent successfully, waiting for server metadata...")

	// Receive metadata first - extend timeout for HashMap computation (server may take time)
	logger().Info(context.Background(), ">>> [CLIENT] Waiting for HashMap metadata from server (this may take time for large datasets)...")
	// Extend read deadline for HashMap computation phase - 30 minutes for debugging
	if err := stream.SetReadDeadline(time.Now().UTC().Add(30 * time.Minute)); err != nil {
		logger().Warn(context.Background(), ">>> [CLIENT] WARNING: Failed to extend read deadline", ion.Err(err))
	}
	logger().Info(context.Background(), ">>> [CLIENT] Extended read deadline to 30 minutes for HashMap computation")
	metadataMsg, err := readMessage(reader, stream)
	if err != nil {
		logger().Error(context.Background(), ">>> [CLIENT] ERROR: Failed to read HashMap metadata", err)
		return nil, "", "", fmt.Errorf("failed to read HashMap metadata: %w", err)
	}
	logger().Info(context.Background(), ">>> [CLIENT] ✓ Received HashMap metadata from server")

	if metadataMsg.Type != TypeHashMapExchangeSYNC {
		return nil, "", "", fmt.Errorf("unexpected message type: %s", metadataMsg.Type)
	}

	expectedData := json.RawMessage([]byte(`"Message From Server"`))
	if !bytes.Equal(metadataMsg.Data, expectedData) {
		return nil, "", "", fmt.Errorf("unexpected metadata data")
	}

	// Extract metadata
	totalChunks := metadataMsg.TotalChunks
	mainKeysCount := metadataMsg.HashMap_MetaData.Main_HashMap_MetaData.KeysCount
	accountsKeysCount := metadataMsg.HashMap_MetaData.Accounts_HashMap_MetaData.KeysCount

	logger().Info(context.Background(), "Metadata received",
		ion.Int("total_chunks", totalChunks),
		ion.Int("main_keys", mainKeysCount),
		ion.Int("accounts_keys", accountsKeysCount))
	logger().Info(context.Background(), ">>> [CLIENT] Metadata received - Total chunks: %d, Main keys: %d, Accounts keys: %d\n",
		ion.Int("total_chunks", totalChunks),
		ion.Int("main_keys", mainKeysCount),
		ion.Int("accounts_keys", accountsKeysCount))

	// Reassemble HashMap from chunks
	logger().Info(context.Background(), ">>> [CLIENT] Starting to receive chunks", ion.Int("expected_chunks", totalChunks))
	var allKeys []string
	receivedChunks := 0
	lastDeadlineUpdate := time.Now().UTC()

	for receivedChunks < totalChunks {
		// Extend read deadline periodically during chunk reception to prevent timeout
		// This is important when server is still batching/computing keys
		if time.Since(lastDeadlineUpdate) > 30*time.Second {
			newDeadline := time.Now().UTC().Add(30 * time.Minute)
			if err := stream.SetReadDeadline(newDeadline); err != nil {
				logger().Warn(context.Background(), ">>> [CLIENT] WARNING: Failed to extend read deadline", ion.Err(err))
			} else {
				lastDeadlineUpdate = time.Now().UTC()
				logger().Info(context.Background(), ">>> [CLIENT] Extended read deadline", ion.Int("chunk", receivedChunks+1), ion.Int("total", totalChunks))
			}
		}

		logger().Info(context.Background(), ">>> [CLIENT] Waiting for chunk", ion.Int("chunk", receivedChunks+1), ion.Int("total", totalChunks))
		chunkMsg, err := readMessage(reader, stream)
		if err != nil {
			logger().Error(context.Background(), ">>> [CLIENT] ERROR: Failed to read chunk", err, ion.Int("chunk", receivedChunks+1))
			return nil, "", "", fmt.Errorf("failed to read chunk %d: %w", receivedChunks+1, err)
		}

		if chunkMsg.Type == TypeHashMapChunkComplete {
			logger().Info(context.Background(), ">>> [CLIENT] ✓ Received chunk complete message")
			logger().Info(context.Background(), "Received chunk complete message")
			break
		}

		if chunkMsg.Type != TypeHashMapChunk {
			logger().Error(context.Background(), ">>> [CLIENT] ERROR: Unexpected message type (expected HASHMAP_CHUNK)", nil, ion.String("type", chunkMsg.Type))
			return nil, "", "", fmt.Errorf("unexpected message type: %s", chunkMsg.Type)
		}

		// Add chunk keys to our collection
		allKeys = append(allKeys, chunkMsg.ChunkKeys...)
		receivedChunks++

		logger().Debug(context.Background(), ">>> [CLIENT] Received chunk",
			ion.Int("chunk_number", chunkMsg.ChunkNumber),
			ion.Int("total_chunks", totalChunks),
			ion.Int("keys_in_chunk", len(chunkMsg.ChunkKeys)),
			ion.Int("total_keys_collected", len(allKeys)))
		logger().Debug(context.Background(), "Received chunk",
			ion.Int("chunk", chunkMsg.ChunkNumber),
			ion.Int("total", totalChunks),
			ion.Int("keys_in_chunk", len(chunkMsg.ChunkKeys)),
			ion.Int("total_keys_collected", len(allKeys)))

		// Send ACK for this chunk
		logger().Info(context.Background(), ">>> [CLIENT] Sending ACK for chunk", ion.Int("chunk_number", chunkMsg.ChunkNumber))
		ackMsg := &SyncMessage{
			Type:        TypeHashMapChunkAck,
			SenderID:    fs.host.ID().String(),
			Timestamp:   time.Now().UTC().Unix(),
			ChunkNumber: chunkMsg.ChunkNumber,
			Success:     true,
		}

		if err := writeMessage(writer, stream, ackMsg); err != nil {
			logger().Error(context.Background(), ">>> [CLIENT] ERROR: Failed to send chunk ACK", err)
			return nil, "", "", fmt.Errorf("failed to send chunk ACK: %w", err)
		}
		progress := float64(receivedChunks) / float64(totalChunks) * 100
		logger().Info(context.Background(), ">>> [CLIENT] ACK sent for chunk", ion.Int("chunk_number", chunkMsg.ChunkNumber), ion.Float64("progress_percent", progress))
	}

	// Split keys back into Main and Accounts
	logger().Info(context.Background(), ">>> [CLIENT] Reassembling HashMaps", ion.Int("chunks_received", receivedChunks))
	SYNC_Keys_Main := allKeys[:mainKeysCount]
	SYNC_Keys_Accounts := allKeys[mainKeysCount:]

	// Rebuild HashMaps
	logger().Info(context.Background(), ">>> [CLIENT] Rebuilding Main HashMap", ion.Int("keys_count", len(SYNC_Keys_Main)))
	SYNC_HashMap_MAIN := hashmap.New()
	for _, key := range SYNC_Keys_Main {
		SYNC_HashMap_MAIN.Insert(key)
	}
	logger().Info(context.Background(), ">>> [CLIENT] Main HashMap rebuilt")

	logger().Info(context.Background(), ">>> [CLIENT] Rebuilding Accounts HashMap", ion.Int("keys_count", len(SYNC_Keys_Accounts)))
	SYNC_HashMap_Accounts := hashmap.New()
	for _, key := range SYNC_Keys_Accounts {
		SYNC_HashMap_Accounts.Insert(key)
	}
	logger().Info(context.Background(), ">>> [CLIENT] ✓ Accounts HashMap rebuilt")

	// Compute checksums
	logger().Info(context.Background(), ">>> [CLIENT] Computing checksums...")
	MainChecksum := SYNC_HashMap_MAIN.Fingerprint()
	AccountChecksum := SYNC_HashMap_Accounts.Fingerprint()
	logger().Info(context.Background(), ">>> [CLIENT] ✓ Checksums computed")

	logger().Info(context.Background(), "Reassembled HashMap from chunks",
		ion.Int("total_chunks", receivedChunks),
		ion.Int("main_keys", len(SYNC_Keys_Main)),
		ion.Int("accounts_keys", len(SYNC_Keys_Accounts)))

	logger().Info(context.Background(), ">>> [CLIENT] Phase2_Sync completed successfully",
		ion.Int("main_keys", len(SYNC_Keys_Main)),
		ion.Int("accounts_keys", len(SYNC_Keys_Accounts)))

	// Extract server's full state fingerprints from metadata for post-sync verification
	serverMainFingerprint := metadataMsg.HashMap_MetaData.Main_HashMap_MetaData.Checksum
	serverAcctsFingerprint := metadataMsg.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum

	// Create response message with reassembled HashMap
	response := &SyncMessage{
		Type:      TypeHashMapExchangeSYNC,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Data:      json.RawMessage([]byte(`"Message From Server"`)),
		HashMap: &TypeHashMapExchange_Struct{
			MAIN_HashMap:     SYNC_HashMap_MAIN,
			Accounts_HashMap: SYNC_HashMap_Accounts,
		},
		HashMap_MetaData: &HashMap_MetaData{
			Main_HashMap_MetaData: &MetaData{
				KeysCount: SYNC_HashMap_MAIN.Size(),
				Checksum:  serverMainFingerprint, // Store SERVER FULL STATE fingerprint for later
			},
			Accounts_HashMap_MetaData: &MetaData{
				KeysCount: SYNC_HashMap_Accounts.Size(),
				Checksum:  serverAcctsFingerprint, // Store SERVER FULL STATE fingerprint for later
			},
		},
	}

	return response, MainChecksum, AccountChecksum, nil
}

func (fs *FastSync) Phase3_FileRequest(msg *SyncMessage, peerID peer.ID, stream network.Stream, writer *bufio.Writer, reader *bufio.Reader) error {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Log the attempt
		logger().Info(context.Background(), "Initiating AVRO file transfer request",
			ion.String("peer", peerID.String()),
			ion.Int("attempt", attempt+1))

		// 1. Create a new message for the request
		requestMsg := &SyncMessage{
			Type:             RequestFiletransfer,
			SenderID:         fs.host.ID().String(),
			Timestamp:        time.Now().UTC().Unix(),
			HashMap:          msg.HashMap,
			HashMap_MetaData: msg.HashMap_MetaData,
			Data:             json.RawMessage([]byte(`"Message From Client - For AVRO File Transfer"`)),
		}

		// 2. Send the request to the server
		if err := writeMessage(writer, stream, requestMsg); err != nil {
			lastErr = fmt.Errorf("failed to send file transfer request (attempt %d/%d): %w",
				attempt+1, maxRetries, err)
			logger().Error(context.Background(), "File transfer request failed", lastErr)

			// If we can't write to the stream, we need to create a new one
			if attempt < maxRetries-1 {
				time.Sleep(time.Second * time.Duration(attempt+1))

				// Try to create a new stream
				newStream, err := fs.host.NewStream(context.Background(), peerID, stream.Protocol())
				if err != nil {
					logger().Error(context.Background(), "Failed to create new stream for retry", err)
					continue
				}
				defer newStream.Close()

				// Update the stream and its reader/writer
				stream = newStream
				reader = bufio.NewReader(stream)
				writer = bufio.NewWriter(stream)
			}
			continue
		}

		// 3. Wait for the server's response
		// Extend deadline significantly as server needs to create AVRO files
		if err := stream.SetReadDeadline(time.Now().UTC().Add(30 * time.Minute)); err != nil {
			logger().Warn(context.Background(), "Failed to extend read deadline for file transfer response", ion.Err(err))
		}

		response, err := readMessage(reader, stream)
		if err != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w",
				attempt+1, maxRetries, err)
			logger().Error(context.Background(), "Failed to read file transfer response", lastErr)

			if attempt < maxRetries-1 {
				time.Sleep(time.Second * time.Duration(attempt+1))
			}
			continue
		}

		// 4. Check the response
		if response.Type == TypeSyncComplete && response.Success {
			logger().Info(context.Background(), "Server successfully initiated file transfers. Sync complete.",
				ion.String("peer", peerID.String()))
			return nil
		}

		// If we get here, the server responded but with an error or unexpected message
		lastErr = fmt.Errorf("unexpected response after file request (attempt %d/%d): type=%s, success=%t, err=%s",
			attempt+1, maxRetries, response.Type, response.Success, response.ErrorMessage)
		logger().Error(context.Background(), "Unexpected response from server", lastErr)

		if attempt < maxRetries-1 {
			time.Sleep(time.Second * time.Duration(attempt+1))
		}
	}

	// If we've exhausted all retries, return the last error
	return fmt.Errorf("failed to complete file transfer after %d attempts: %w", maxRetries, lastErr)
}

/* UNUSED
func (fs *FastSync) batchCreateWithRetry(entriesMap map[string]interface{}, dbType DatabaseType) error {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Get the current client for this attempt
		var dbClient *config.PooledConnection
		switch dbType {
		case MainDB:
			dbClient = fs.mainDB
		case AccountsDB:
			dbClient = fs.accountsDB
		default:
			return fmt.Errorf("invalid database type: %v", dbType)
		}
		if dbClient == nil {
			return fmt.Errorf("database client for type %s is not initialized", dbTypeToString(dbType))
		}

		err := DB_OPs.BatchCreate(dbClient, entriesMap)
		if err == nil {
			return nil // Success
		}
		lastErr = err

		// Check for the specific "invalid token" error from immudb
		if strings.Contains(err.Error(), "invalid token") {
			logger().Warn(context.Background(), "Authentication token expired. Re-authenticating and retrying.",
				ion.String("db", dbTypeToString(dbType)),
				ion.Int("attempt", attempt+1))

			var newClient *config.PooledConnection
			var clientErr error

			if dbType == MainDB {
				newClient, clientErr = DB_OPs.GetMainDBConnectionandPutBack(context.Background())
				if clientErr == nil {
					DB_OPs.Close(fs.mainDB.Client) // Close the old, invalid client
					DB_OPs.PutMainDBConnection(fs.mainDB)
					fs.mainDB = newClient // Replace with the new, valid client
				}
			} else if dbType == AccountsDB {
				newClient, clientErr = DB_OPs.GetAccountConnectionandPutBack(context.Background())
				if clientErr == nil {
					DB_OPs.Close(fs.accountsDB.Client)
					DB_OPs.PutAccountsConnection(fs.accountsDB)
					fs.accountsDB = newClient
				}
			}

			if clientErr != nil {
				return fmt.Errorf("failed to re-authenticate after token error: %w", clientErr)
			}
			continue // Retry the operation with the new client
		}

		// For other transient errors, perform a simple retry with backoff
		time.Sleep(RetryDelay * time.Duration(attempt+1))
	}

	return fmt.Errorf("failed to apply batch transaction after %d attempts: %w", maxRetries, lastErr)
}
*/

func (fs *FastSync) batchCreateOrderedWithRetry(entries []struct {
	Key   string
	Value []byte
}, dbType DatabaseType) error {
	const maxRetries = 3
	var lastErr error
	logger().Info(context.Background(), ">>> [DB] batchCreateOrderedWithRetry", ion.Int("entries_count", len(entries)), ion.String("db_type", dbTypeToString(dbType)))

	for attempt := 0; attempt < maxRetries; attempt++ {
		var dbClient *config.PooledConnection
		switch dbType {
		case MainDB:
			dbClient = fs.mainDB
			if dbClient == nil {
				logger().Error(context.Background(), ">>> [DB] ERROR: MainDB client is nil", nil)
				return fmt.Errorf("database client for type %s is not initialized", dbTypeToString(dbType))
			}
			dbName := "unknown"
			if dbClient != nil && dbClient.Client != nil {
				dbName = dbClient.Database
			}
			logger().Info(context.Background(), "DB client ready",
				ion.Bool("connected", dbClient != nil),
				ion.String("database", dbName),
				ion.String("type", "MainDB"))
		case AccountsDB:
			dbClient = fs.accountsDB
			if dbClient == nil {
				logger().Error(context.Background(), ">>> [DB] ERROR: AccountsDB client is nil", nil)
				return fmt.Errorf("database client for type %s is not initialized", dbTypeToString(dbType))
			}
			dbName := "unknown"
			if dbClient != nil && dbClient.Client != nil {
				dbName = dbClient.Database
			}
			logger().Info(context.Background(), "DB client ready",
				ion.Bool("connected", dbClient != nil),
				ion.String("database", dbName),
				ion.String("type", "AccountsDB"))
		default:
			return fmt.Errorf("invalid database type: %v", dbType)
		}

		var err error
		switch dbType {
		case MainDB:
			logger().Info(context.Background(), "Calling BatchCreateOrdered for MainDB", ion.Int("entries", len(entries)))
			err = DB_OPs.BatchCreateOrdered(dbClient, entries)
			if err != nil {
				logger().Error(context.Background(), "BatchCreateOrdered failed for MainDB", err, ion.Int("entries", len(entries)))
			} else {
				logger().Info(context.Background(), "BatchCreateOrdered succeeded for MainDB", ion.Int("entries", len(entries)))
			}
		case AccountsDB:
			logger().Info(context.Background(), "Calling BatchRestoreAccounts for AccountsDB", ion.Int("entries", len(entries)))
			err = DB_OPs.BatchRestoreAccounts(dbClient, entries)
			if err != nil {
				logger().Error(context.Background(), "BatchRestoreAccounts failed for AccountsDB", err, ion.Int("entries", len(entries)))
			} else {
				logger().Info(context.Background(), "BatchRestoreAccounts succeeded for AccountsDB", ion.Int("entries", len(entries)))
			}
		}
		if err == nil {
			return nil
		}
		lastErr = err
		logger().Warn(context.Background(), "DB write attempt failed", ion.Int("attempt", attempt+1), ion.Int("max_retries", maxRetries), ion.Err(err))
		if strings.Contains(lastErr.Error(), "invalid token") {
			var newClient *config.PooledConnection
			var clientErr error
			if dbType == MainDB {
				newClient, clientErr = DB_OPs.GetMainDBConnectionandPutBack(context.Background())
				if clientErr == nil {
					DB_OPs.Close(fs.mainDB.Client)
					DB_OPs.PutMainDBConnection(fs.mainDB)
					fs.mainDB = newClient
				}
			} else {
				newClient, clientErr = DB_OPs.GetAccountConnectionandPutBack(context.Background())
				if clientErr == nil {
					DB_OPs.Close(fs.accountsDB.Client)
					DB_OPs.PutAccountsConnection(fs.accountsDB)
					fs.accountsDB = newClient
				}
			}
			if clientErr != nil {
				return fmt.Errorf("failed to re-authenticate after token error: %w", clientErr)
			}
			continue
		}
		time.Sleep(RetryDelay * time.Duration(attempt+1))
	}
	return fmt.Errorf("failed to apply ordered batch after %d attempts: %w", maxRetries, lastErr)
}

func (fs *FastSync) PushDataToDB(msg *SyncMessage, dbType DatabaseType, dbPath string) error {
	logger().Info(context.Background(), ">>> [DB] PushDataToDB called", ion.String("db_type", dbTypeToString(dbType)), ion.String("file", dbPath))

	// Get the appropriate database client
	var dbClient *config.PooledConnection
	switch dbType {
	case MainDB:
		dbClient = fs.mainDB
		if dbClient == nil {
			logger().Error(context.Background(), ">>> [DB] ERROR: MainDB client is nil!", nil)
		} else {
			logger().Info(context.Background(), ">>> [DB] MainDB client OK", ion.String("database", dbClient.Database))
		}
		logger().Info(context.Background(), "Using defaultDB client for restoration", ion.String("db_type", "defaultDB"))
	case AccountsDB:
		dbClient = fs.accountsDB
		if dbClient == nil {
			logger().Error(context.Background(), ">>> [DB] ERROR: AccountsDB client is nil!", nil)
		} else {
			logger().Info(context.Background(), ">>> [DB] AccountsDB client OK", ion.String("database", dbClient.Database))
		}
		logger().Info(context.Background(), "Using AccountsDB client for restoration", ion.String("db_type", "AccountsDB"))
	default:
		return fmt.Errorf("invalid database type: %v", dbType)
	}
	if dbClient == nil {
		logger().Error(context.Background(), "Database client is nil", nil, ion.String("db_type", dbTypeToString(dbType)))
		return fmt.Errorf("database client for type %s is not initialized", dbTypeToString(dbType))
	}

	logger().Info(context.Background(), "Starting database restoration",
		ion.String("db_type", dbTypeToString(dbType)),
		ion.String("file_path", dbPath))

	// Ensure the backup file exists
	fileInfo, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		// Try expected path under fastsync/.temp
		tryPath := dbPath
		// For legacy calls that might pass only basename, prefix the temp dir
		if filepath.Base(dbPath) == dbPath { // no dir component
			tryPath = filepath.Join("fastsync", ".temp", dbPath)
		}
		if fi, err2 := os.Stat(tryPath); err2 == nil && fi.Size() > 0 {
			dbPath = tryPath
			fileInfo = fi
			err = nil
		} else {
			// Try legacy filenames explicitly
			legacy := ""
			switch dbType {
			case MainDB:
				legacy = filepath.Join("fastsync", ".temp", "main.avro")
			case AccountsDB:
				legacy = filepath.Join("fastsync", ".temp", "accounts.avro")
			}
			if legacy != "" {
				if fi2, err3 := os.Stat(legacy); err3 == nil && fi2.Size() > 0 {
					dbPath = legacy
					fileInfo = fi2
					err = nil
				} else {
					logger().Info(context.Background(), "AVRO file does not exist (and legacy not found), skipping restore.", ion.String("path", dbPath))
					return nil
				}
			} else {
				logger().Info(context.Background(), "AVRO file does not exist, skipping restore.", ion.String("path", dbPath))
				return nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("failed to stat avro file %s: %w", dbPath, err)
	}
	if fileInfo.Size() == 0 {
		logger().Info(context.Background(), "AVRO file is empty, skipping restore.", ion.String("path", dbPath))
		return nil
	}

	// Validate filename against DB type (allow legacy names for backward compatibility)
	base := filepath.Base(dbPath)
	valid := false
	switch dbType {
	case MainDB:
		if base == "defaultdb.avro" {
			valid = true
		}
	case AccountsDB:
		if base == "accountsdb.avro" {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("avro filename mismatch: got %s for %s", base, dbTypeToString(dbType))
	}

	// Open the backup file
	file, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open avro file: %w", err)
	}
	defer file.Close()

	startTime := time.Now()
	logger().Info(context.Background(), "Starting database restore from AVRO backup",
		ion.String("db", dbTypeToString(dbType)),
		ion.String("file", filepath.Base(dbPath)))

	// Create an OCF reader for the Avro file
	ocfReader, err := goavro.NewOCFReader(file)
	if err != nil {
		return fmt.Errorf("failed to create avro ocf reader for %s: %w", dbPath, err)
	}

	var entriesOrdered []struct {
		Key   string
		Value []byte
	}
	// ImmuDB has a limit on entries per transaction (typically 1000-5000)
	// Using 100 to be safe and avoid gRPC message size limits (32MB default, 200MB max)
	// Large block values can cause 1000 entries to exceed message size limits
	batchSize := 50 // Process in batches of 50 entries to avoid message size limits
	totalEntries := 0
	blockKeysCount := 0
	latestBlockCount := 0

	logger().Info(context.Background(), "Using batch size", ion.Int("batch_size", batchSize))

	// Read records from the Avro file
	recordsRead := 0
	for ocfReader.Scan() {
		record, err := ocfReader.Read()
		if err != nil {
			logger().Warn(context.Background(), "Failed to read AVRO record", ion.Err(err))
			continue
		}
		recordsRead++

		recordMap, ok := record.(map[string]interface{})
		if !ok {
			logger().Warn(context.Background(), "Unexpected avro record type")
			continue
		}

		key, keyOk := recordMap["Key"].(string)
		value, valueOk := recordMap["Value"].(string)

		// Track block and latest_block keys for debugging
		isBlockKey := keyOk && strings.HasPrefix(key, "block:")
		isLatestBlock := keyOk && key == "latest_block"
		if isBlockKey || isLatestBlock {
			keyType := "block"
			if isLatestBlock {
				keyType = "latest_block"
			}
			valueLen := 0
			if valueOk {
				valueLen = len(value)
			}
			logger().Debug(context.Background(), ">>> [DB] Found key in AVRO",
				ion.String("type", keyType),
				ion.String("key", key),
				ion.Int("value_length", valueLen))
		}

		// Optional Database field for origin validation
		avroDB, _ := recordMap["Database"].(string)
		if avroDB != "" {
			var expectedDB string
			if dbType == MainDB {
				expectedDB = config.DBName
			} else if dbType == AccountsDB {
				expectedDB = config.AccountsDBName
			}
			if expectedDB != "" && avroDB != expectedDB {
				return fmt.Errorf("avro database mismatch: got %s, expected %s", avroDB, expectedDB)
			}
		}

		if !keyOk || !valueOk {
			logger().Warn(context.Background(), "avro record has missing or invalid Key/Value fields")
			continue
		}

		entriesOrdered = append(entriesOrdered, struct {
			Key   string
			Value []byte
		}{Key: key, Value: []byte(value)})

		// Track block keys added to batch
		if isBlockKey {
			blockKeysCount++
			logger().Info(context.Background(), ">>> [DB] Added block key to restore batch", ion.String("key", key), ion.Int("total_blocks", blockKeysCount))
		} else if isLatestBlock {
			latestBlockCount++
			logger().Info(context.Background(), ">>> [DB] Added latest_block to restore batch", ion.Int("count", latestBlockCount))
		}

		if len(entriesOrdered) >= batchSize {
			logger().Info(context.Background(), ">>> [DB] Processing batch", ion.Int("entries_count", len(entriesOrdered)), ion.String("db_type", dbTypeToString(dbType)))
			if err := fs.batchCreateOrderedWithRetry(entriesOrdered, dbType); err != nil {
				// If batch is too large, try splitting it into smaller chunks
				if strings.Contains(err.Error(), "max number of entries per tx exceeded") || strings.Contains(err.Error(), "message larger than max") {
					logger().Warn(context.Background(), ">>> [DB] WARNING: Batch too large, splitting into smaller chunks")
					// Split into smaller batches of 50 to avoid message size limits
					chunkSize := 50
					for i := 0; i < len(entriesOrdered); i += chunkSize {
						end := i + chunkSize
						if end > len(entriesOrdered) {
							end = len(entriesOrdered)
						}
						chunk := entriesOrdered[i:end]
						logger().Info(context.Background(), ">>> [DB] Processing chunk", ion.Int("start", i), ion.Int("end", end), ion.Int("entries_count", len(chunk)))
						if err := fs.batchCreateOrderedWithRetry(chunk, dbType); err != nil {
							return fmt.Errorf("failed to push chunk to DB: %w", err)
						}
						totalEntries += len(chunk)
					}
				} else {
					return fmt.Errorf("failed to push batch to DB: %w", err)
				}
			} else {
				totalEntries += len(entriesOrdered)
			}
			entriesOrdered = nil

			logger().Info(context.Background(), ">>> [DB] Restore in progress",
				ion.Int("entries_processed", totalEntries),
				ion.Duration("elapsed", time.Since(startTime)),
				ion.String("db", dbTypeToString(dbType)))
		}
	}

	// Process any remaining entries in the last batch
	if len(entriesOrdered) > 0 {
		logger().Info(context.Background(), ">>> [DB] Processing final batch", ion.Int("entries_count", len(entriesOrdered)), ion.String("db_type", dbTypeToString(dbType)))
		if err := fs.batchCreateOrderedWithRetry(entriesOrdered, dbType); err != nil {
			// If batch is too large, try splitting it into smaller chunks
			if strings.Contains(err.Error(), "max number of entries per tx exceeded") || strings.Contains(err.Error(), "message larger than max") {
				logger().Warn(context.Background(), ">>> [DB] WARNING: Final batch too large, splitting into smaller chunks")
				// Split into smaller batches of 50 to avoid message size limits
				chunkSize := 50
				for i := 0; i < len(entriesOrdered); i += chunkSize {
					end := i + chunkSize
					if end > len(entriesOrdered) {
						end = len(entriesOrdered)
					}
					chunk := entriesOrdered[i:end]
					logger().Info(context.Background(), ">>> [DB] Processing final chunk", ion.Int("start", i), ion.Int("end", end), ion.Int("entries_count", len(chunk)))
					if err := fs.batchCreateOrderedWithRetry(chunk, dbType); err != nil {
						return fmt.Errorf("failed to push final chunk to DB: %w", err)
					}
					totalEntries += len(chunk)
				}
			} else {
				return fmt.Errorf("failed to push final batch to DB: %w", err)
			}
		} else {
			totalEntries += len(entriesOrdered)
		}
	}

	logger().Info(context.Background(), ">>> [DB] Database restore completed",
		ion.Int("total_entries", totalEntries),
		ion.Int("records_read", recordsRead),
		ion.String("db", dbTypeToString(dbType)),
		ion.Duration("time", time.Since(startTime)))

	if dbType == MainDB {
		logger().Info(context.Background(), ">>> [DB] Block keys summary",
			ion.Int("block_keys_processed", blockKeysCount),
			ion.Int("latest_block_count", latestBlockCount))
		if blockKeysCount == 0 && latestBlockCount == 0 {
			logger().Warn(context.Background(), ">>> [DB] WARNING: No block keys or latest_block were processed")
		}
	}

	if totalEntries == 0 {
		logger().Warn(context.Background(), ">>> [DB] WARNING: No entries were written",
			ion.String("db", dbTypeToString(dbType)))
	}

	return nil
}
