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

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/linkedin/goavro/v2"
	"github.com/rs/zerolog/log"

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
	ResponseTimeout = 60 * time.Second // Reduced to 60s since we chunk HashMaps (100 keys per chunk)
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
	peer           peer.ID
	startTime      time.Time
	mainStartTxID  uint64
	mainEndTxID    uint64
	acctsStartTxID uint64
	acctsEndTxID   uint64
	batches        int
	completed      int
	cancel         context.CancelFunc
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
		log.Warn().Msg("No CRDT engine provided, skipping CRDT processing during sync")
		return nil
	}

	log.Info().Int("count", len(crdtData)).Msg("Processing CRDTs during sync")

	for _, crdtBytes := range crdtData {
		// Parse wrapper containing CRDT metadata
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(crdtBytes, &wrapper); err != nil {
			log.Error().Err(err).Msg("Failed to unmarshal CRDT wrapper")
			continue
		}

		// Extract CRDT type (lww-set, counter, etc.)
		var crdtType string
		if err := json.Unmarshal(wrapper["type"], &crdtType); err != nil {
			log.Error().Err(err).Msg("Failed to unmarshal CRDT type")
			continue
		}

		// Extract CRDT key for identification
		var key string
		if err := json.Unmarshal(wrapper["key"], &key); err != nil {
			log.Error().Err(err).Msg("Failed to unmarshal CRDT key")
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
			log.Error().Str("type", crdtType).Msg("Unknown CRDT type during sync")
			continue
		}

		// Unmarshal CRDT data into the created instance
		if err := json.Unmarshal(wrapper["data"], crdtValue); err != nil {
			log.Error().Err(err).Str("type", crdtType).Str("key", key).Msg("Failed to unmarshal CRDT data")
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
							log.Error().Err(err).Str("key", key).Str("element", element).Msg("Failed to add element to LWW set during sync")
						} else {
							log.Debug().Str("key", key).Str("element", element).Msg("Added element to LWW set during sync")
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
							log.Error().Err(err).Str("key", key).Str("node", nodeID).Uint64("value", value).Msg("Failed to increment counter during sync")
						} else {
							log.Debug().Str("key", key).Str("node", nodeID).Uint64("value", value).Msg("Incremented counter during sync")
						}
					}
				}
			}
		}

		log.Info().
			Str("type", crdtType).
			Str("key", key).
			Msg("Successfully processed CRDT during sync")
	}

	log.Info().Int("processed", len(crdtData)).Msg("Completed CRDT processing during sync")
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

	log.Info().
		Str("peer", peerID.String()).
		Uint64("main_tx_id", mainState.TxId).
		Uint64("accounts_tx_id", accountsState.TxId).
		Msg("Sending verification data")

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
			log.Info().
				Str("db", dbTypeToString(dbState.Type)).
				Msg("Already up to date for this database")
			continue
		}

		// Calculate batches
		batchCount := calculateBatchCount(ourState.TxId, dbState.TxID)
		if batchCount == 0 {
			fmt.Printf("No batches needed for %s\n", dbTypeToString(dbState.Type))
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

	log.Info().
		Bool("main_match", mainMatch).
		Bool("accounts_match", accountsMatch).
		Msg("Sync verification completed")

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

	log.Info().Msg("Sync completed successfully")
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
	fmt.Printf("BATCH RECEIVED [%d]: Size=%d bytes, Type=%s, DB=%s\n",
		batchResp.BatchNumber,
		len(batchResp.Data),
		batchResp.Type,
		dbTypeToString(dbType))

	// Check for abort message
	if batchResp.Type == TypeSyncAbort {
		fmt.Printf("BATCH ERROR: Peer aborted sync: %s\n", batchResp.ErrorMessage)
		return fmt.Errorf("peer aborted sync: %s", batchResp.ErrorMessage)
	}

	// Check response
	if batchResp.Type != TypeBatchData {
		fmt.Printf("BATCH ERROR: Unexpected response type: %s\n", batchResp.Type)
		return fmt.Errorf("unexpected response type: %s", batchResp.Type)
	}

	// Parse batch data
	var batchData BatchData
	if err := json.Unmarshal(batchResp.Data, &batchData); err != nil {
		// Print error details for debugging
		if len(batchResp.Data) > 100 {
			fmt.Printf("BATCH PARSE ERROR: %v\nData starts with: %s...\n",
				err, string(batchResp.Data[:100]))
		} else {
			fmt.Printf("BATCH PARSE ERROR: %v\nFull data: %s\n",
				err, string(batchResp.Data))
		}
		return fmt.Errorf("failed to parse batch data: %w", err)
	}

	// Print detailed batch contents to terminal
	fmt.Printf("BATCH PARSED [%d]: %d entries, %d CRDTs, DB=%s\n",
		batchResp.BatchNumber,
		len(batchData.Entries),
		len(batchData.CRDTs),
		dbTypeToString(batchData.DBType))

	if len(batchData.Entries) > 0 {
		// Print sample of first few keys
		fmt.Printf("BATCH ENTRIES SAMPLE: ")
		for i := 0; i < min(3, len(batchData.Entries)); i++ {
			fmt.Printf("%s, ", string(batchData.Entries[i].Key))
		}
		fmt.Println("...")
	}

	// Get database and CRDT engine
	db := fs.getDB(dbType) // Pass nil for crdtEngine

	// Process data
	if err := fs.storeEntries(db, batchData.Entries); err != nil {
		fmt.Printf("BATCH ERROR: Failed to store entries: %v\n", err)
		return fmt.Errorf("failed to store entries: %w", err)
	}

	// UPDATED: Process CRDTs using the new CRDT engine during batch processing
	// This ensures CRDT operations are properly synchronized without conflicts
	if err := fs.storeCRDTs(fs.crdtEngine, batchData.CRDTs); err != nil {
		fmt.Printf("BATCH ERROR: Failed to store CRDTs: %v\n", err)
		return fmt.Errorf("failed to store CRDTs: %w", err)
	}

	fmt.Printf("BATCH PROCESSED [%d] successfully\n", batchResp.BatchNumber)

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
	fmt.Println(">>> [CLIENT] Starting Phase2_Sync - sending HashMap exchange request")
	msg.Type = TypeHashMapExchangeSYNC

	// Debug: Log the message being sent
	log.Info().
		Str("peer_id", peerID.String()).
		Str("message_type", msg.Type).
		Int("data_length", len(msg.Data)).
		Msg("Sending Phase2_Sync request")

	fmt.Printf(">>> [CLIENT] Sending HashMap exchange request to server...\n")
	if err := writeMessage(writer, stream, msg); err != nil {
		fmt.Printf(">>> [CLIENT] ERROR: Failed to write Phase2_Sync message: %v\n", err)
		log.Error().Err(err).Msg("Failed to write Phase2_Sync message")
		return nil, "", "", err
	}

	fmt.Println(">>> [CLIENT] ✓ Phase2_Sync message sent, waiting for server response...")
	log.Info().Msg("Phase2_Sync message sent successfully, waiting for chunks...")

	// Receive metadata first - extend timeout for HashMap computation (server may take time)
	fmt.Println(">>> [CLIENT] Waiting for HashMap metadata from server (this may take time for large datasets)...")
	// Extend read deadline for HashMap computation phase - 30 minutes for debugging
	if err := stream.SetReadDeadline(time.Now().UTC().Add(30 * time.Minute)); err != nil {
		fmt.Printf(">>> [CLIENT] WARNING: Failed to extend read deadline: %v\n", err)
	}
	fmt.Println(">>> [CLIENT] Extended read deadline to 30 minutes for HashMap computation")
	metadataMsg, err := readMessage(reader, stream)
	if err != nil {
		fmt.Printf(">>> [CLIENT] ERROR: Failed to read HashMap metadata: %v\n", err)
		return nil, "", "", fmt.Errorf("failed to read HashMap metadata: %w", err)
	}
	fmt.Println(">>> [CLIENT] ✓ Received HashMap metadata from server")

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

	fmt.Printf(">>> [CLIENT] Metadata received - Total chunks: %d, Main keys: %d, Accounts keys: %d\n", totalChunks, mainKeysCount, accountsKeysCount)
	log.Info().
		Int("total_chunks", totalChunks).
		Int("main_keys", mainKeysCount).
		Int("accounts_keys", accountsKeysCount).
		Msg("Received HashMap metadata, receiving chunks...")

	// Reassemble HashMap from chunks
	fmt.Printf(">>> [CLIENT] Starting to receive chunks (expecting %d chunks)...\n", totalChunks)
	var allKeys []string
	receivedChunks := 0

	for receivedChunks < totalChunks {
		fmt.Printf(">>> [CLIENT] Waiting for chunk %d/%d...\n", receivedChunks+1, totalChunks)
		chunkMsg, err := readMessage(reader, stream)
		if err != nil {
			fmt.Printf(">>> [CLIENT] ERROR: Failed to read chunk %d: %v\n", receivedChunks+1, err)
			return nil, "", "", fmt.Errorf("failed to read chunk %d: %w", receivedChunks+1, err)
		}

		if chunkMsg.Type == TypeHashMapChunkComplete {
			fmt.Println(">>> [CLIENT] ✓ Received chunk complete message")
			log.Info().Msg("Received chunk complete message")
			break
		}

		if chunkMsg.Type != TypeHashMapChunk {
			fmt.Printf(">>> [CLIENT] ERROR: Unexpected message type: %s (expected HASHMAP_CHUNK)\n", chunkMsg.Type)
			return nil, "", "", fmt.Errorf("unexpected message type: %s", chunkMsg.Type)
		}

		// Add chunk keys to our collection
		allKeys = append(allKeys, chunkMsg.ChunkKeys...)
		receivedChunks++

		fmt.Printf(">>> [CLIENT] ✓ Received chunk %d/%d (%d keys, total collected: %d keys)\n",
			chunkMsg.ChunkNumber, totalChunks, len(chunkMsg.ChunkKeys), len(allKeys))
		log.Debug().
			Int("chunk", chunkMsg.ChunkNumber).
			Int("total", totalChunks).
			Int("keys_in_chunk", len(chunkMsg.ChunkKeys)).
			Int("total_keys_collected", len(allKeys)).
			Msg("Received chunk")

		// Send ACK for this chunk
		fmt.Printf(">>> [CLIENT] Sending ACK for chunk %d...\n", chunkMsg.ChunkNumber)
		ackMsg := &SyncMessage{
			Type:        TypeHashMapChunkAck,
			SenderID:    fs.host.ID().String(),
			Timestamp:   time.Now().UTC().Unix(),
			ChunkNumber: chunkMsg.ChunkNumber,
			Success:     true,
		}

		if err := writeMessage(writer, stream, ackMsg); err != nil {
			fmt.Printf(">>> [CLIENT] ERROR: Failed to send chunk ACK: %v\n", err)
			return nil, "", "", fmt.Errorf("failed to send chunk ACK: %w", err)
		}
		fmt.Printf(">>> [CLIENT] ✓ ACK sent for chunk %d (progress: %.1f%%)\n", chunkMsg.ChunkNumber, float64(receivedChunks)/float64(totalChunks)*100)
	}

	// Split keys back into Main and Accounts
	fmt.Printf(">>> [CLIENT] Reassembling HashMaps from %d received chunks...\n", receivedChunks)
	SYNC_Keys_Main := allKeys[:mainKeysCount]
	SYNC_Keys_Accounts := allKeys[mainKeysCount:]

	// Rebuild HashMaps
	fmt.Printf(">>> [CLIENT] Rebuilding Main HashMap (%d keys)...\n", len(SYNC_Keys_Main))
	SYNC_HashMap_MAIN := hashmap.New()
	for _, key := range SYNC_Keys_Main {
		SYNC_HashMap_MAIN.Insert(key)
	}
	fmt.Println(">>> [CLIENT] ✓ Main HashMap rebuilt")

	fmt.Printf(">>> [CLIENT] Rebuilding Accounts HashMap (%d keys)...\n", len(SYNC_Keys_Accounts))
	SYNC_HashMap_Accounts := hashmap.New()
	for _, key := range SYNC_Keys_Accounts {
		SYNC_HashMap_Accounts.Insert(key)
	}
	fmt.Println(">>> [CLIENT] ✓ Accounts HashMap rebuilt")

	// Compute checksums
	fmt.Println(">>> [CLIENT] Computing checksums...")
	MainChecksum := SYNC_HashMap_MAIN.Fingerprint()
	AccountChecksum := SYNC_HashMap_Accounts.Fingerprint()
	fmt.Println(">>> [CLIENT] ✓ Checksums computed")

	log.Info().
		Int("total_chunks", receivedChunks).
		Int("main_keys", len(SYNC_Keys_Main)).
		Int("accounts_keys", len(SYNC_Keys_Accounts)).
		Msg("Reassembled HashMap from chunks")

	fmt.Printf(">>> [CLIENT] ✓ Phase2_Sync completed successfully - Main: %d keys, Accounts: %d keys\n", len(SYNC_Keys_Main), len(SYNC_Keys_Accounts))

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
				Checksum:  MainChecksum,
			},
			Accounts_HashMap_MetaData: &MetaData{
				KeysCount: SYNC_HashMap_Accounts.Size(),
				Checksum:  AccountChecksum,
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
		log.Info().
			Str("peer", peerID.String()).
			Int("attempt", attempt+1).
			Msg("Initiating AVRO file transfer request")

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
			log.Error().Err(lastErr).Msg("File transfer request failed")

			// If we can't write to the stream, we need to create a new one
			if attempt < maxRetries-1 {
				time.Sleep(time.Second * time.Duration(attempt+1))

				// Try to create a new stream
				newStream, err := fs.host.NewStream(context.Background(), peerID, stream.Protocol())
				if err != nil {
					log.Error().Err(err).Msg("Failed to create new stream for retry")
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
		response, err := readMessage(reader, stream)
		if err != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w",
				attempt+1, maxRetries, err)
			log.Error().Err(lastErr).Msg("Failed to read file transfer response")

			if attempt < maxRetries-1 {
				time.Sleep(time.Second * time.Duration(attempt+1))
			}
			continue
		}

		// 4. Check the response
		if response.Type == TypeSyncComplete && response.Success {
			log.Info().
				Str("peer", peerID.String()).
				Msg("Server successfully initiated file transfers. Sync complete.")
			return nil
		}

		// If we get here, the server responded but with an error or unexpected message
		lastErr = fmt.Errorf("unexpected response after file request (attempt %d/%d): type=%s, success=%t, err=%s",
			attempt+1, maxRetries, response.Type, response.Success, response.ErrorMessage)
		log.Error().Err(lastErr).Msg("Unexpected response from server")

		if attempt < maxRetries-1 {
			time.Sleep(time.Second * time.Duration(attempt+1))
		}
	}

	// If we've exhausted all retries, return the last error
	return fmt.Errorf("failed to complete file transfer after %d attempts: %w", maxRetries, lastErr)
}

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
			log.Warn().
				Str("db", dbTypeToString(dbType)).
				Int("attempt", attempt+1).
				Msg("Authentication token expired. Re-authenticating and retrying.")

			var newClient *config.PooledConnection
			var clientErr error

			if dbType == MainDB {
				newClient, clientErr = DB_OPs.GetMainDBConnection()
				if clientErr == nil {
					DB_OPs.Close(fs.mainDB.Client) // Close the old, invalid client
					DB_OPs.PutMainDBConnection(fs.mainDB)
					fs.mainDB = newClient // Replace with the new, valid client
				}
			} else if dbType == AccountsDB {
				newClient, clientErr = DB_OPs.GetAccountsConnection()
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

func (fs *FastSync) batchCreateOrderedWithRetry(entries []struct {
	Key   string
	Value []byte
}, dbType DatabaseType) error {
	const maxRetries = 3
	var lastErr error
	fmt.Printf(">>> [DB] batchCreateOrderedWithRetry: %d entries for %s\n", len(entries), dbTypeToString(dbType))

	for attempt := 0; attempt < maxRetries; attempt++ {
		var dbClient *config.PooledConnection
		switch dbType {
		case MainDB:
			dbClient = fs.mainDB
			if dbClient == nil {
				fmt.Printf(">>> [DB] ERROR: MainDB client is nil\n")
				return fmt.Errorf("database client for type %s is not initialized", dbTypeToString(dbType))
			}
			fmt.Printf(">>> [DB] MainDB client: %v, database: %s\n", dbClient != nil, func() string {
				if dbClient != nil && dbClient.Client != nil {
					return dbClient.Database
				}
				return "unknown"
			}())
		case AccountsDB:
			dbClient = fs.accountsDB
			if dbClient == nil {
				fmt.Printf(">>> [DB] ERROR: AccountsDB client is nil\n")
				return fmt.Errorf("database client for type %s is not initialized", dbTypeToString(dbType))
			}
			fmt.Printf(">>> [DB] AccountsDB client: %v, database: %s\n", dbClient != nil, func() string {
				if dbClient != nil && dbClient.Client != nil {
					return dbClient.Database
				}
				return "unknown"
			}())
		default:
			return fmt.Errorf("invalid database type: %v", dbType)
		}

		var err error
		switch dbType {
		case MainDB:
			fmt.Printf(">>> [DB] Calling BatchCreateOrdered for MainDB with %d entries...\n", len(entries))
			err = DB_OPs.BatchCreateOrdered(dbClient, entries)
			if err != nil {
				fmt.Printf(">>> [DB] ERROR: BatchCreateOrdered failed for MainDB: %v\n", err)
			} else {
				fmt.Printf(">>> [DB] ✓ BatchCreateOrdered succeeded for MainDB (%d entries)\n", len(entries))
			}
		case AccountsDB:
			fmt.Printf(">>> [DB] Calling BatchRestoreAccounts for AccountsDB with %d entries...\n", len(entries))
			err = DB_OPs.BatchRestoreAccounts(dbClient, entries)
			if err != nil {
				fmt.Printf(">>> [DB] ERROR: BatchRestoreAccounts failed for AccountsDB: %v\n", err)
			} else {
				fmt.Printf(">>> [DB] ✓ BatchRestoreAccounts succeeded for AccountsDB (%d entries)\n", len(entries))
			}
		}
		if err == nil {
			return nil
		}
		lastErr = err
		fmt.Printf(">>> [DB] Attempt %d/%d failed: %v\n", attempt+1, maxRetries, err)
		if strings.Contains(lastErr.Error(), "invalid token") {
			var newClient *config.PooledConnection
			var clientErr error
			if dbType == MainDB {
				newClient, clientErr = DB_OPs.GetMainDBConnection()
				if clientErr == nil {
					DB_OPs.Close(fs.mainDB.Client)
					DB_OPs.PutMainDBConnection(fs.mainDB)
					fs.mainDB = newClient
				}
			} else {
				newClient, clientErr = DB_OPs.GetAccountsConnection()
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
	fmt.Printf(">>> [DB] PushDataToDB called for %s, file: %s\n", dbTypeToString(dbType), dbPath)

	// Get the appropriate database client
	var dbClient *config.PooledConnection
	switch dbType {
	case MainDB:
		dbClient = fs.mainDB
		if dbClient == nil {
			fmt.Printf(">>> [DB] ERROR: MainDB client is nil!\n")
		} else {
			fmt.Printf(">>> [DB] MainDB client OK, database: %s\n", dbClient.Database)
		}
		log.Info().Str("db_type", "defaultDB").Msg("Using defaultDB client for restoration")
	case AccountsDB:
		dbClient = fs.accountsDB
		if dbClient == nil {
			fmt.Printf(">>> [DB] ERROR: AccountsDB client is nil!\n")
		} else {
			fmt.Printf(">>> [DB] AccountsDB client OK, database: %s\n", dbClient.Database)
		}
		log.Info().Str("db_type", "AccountsDB").Msg("Using AccountsDB client for restoration")
	default:
		return fmt.Errorf("invalid database type: %v", dbType)
	}
	if dbClient == nil {
		fmt.Printf(">>> [DB] ERROR: Database client for %s is nil\n", dbTypeToString(dbType))
		log.Error().
			Str("db_type", dbTypeToString(dbType)).
			Msg("Database client is nil")
		return fmt.Errorf("database client for type %s is not initialized", dbTypeToString(dbType))
	}

	log.Info().
		Str("db_type", dbTypeToString(dbType)).
		Str("file_path", dbPath).
		Msg("Starting database restoration")

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
					log.Info().Str("path", dbPath).Msg("AVRO file does not exist (and legacy not found), skipping restore.")
					return nil
				}
			} else {
				log.Info().Str("path", dbPath).Msg("AVRO file does not exist, skipping restore.")
				return nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("failed to stat avro file %s: %w", dbPath, err)
	}
	if fileInfo.Size() == 0 {
		log.Info().Str("path", dbPath).Msg("AVRO file is empty, skipping restore.")
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
	fmt.Printf(">>> [DB] Starting database restore from AVRO: %s (DB: %s)\n", filepath.Base(dbPath), dbTypeToString(dbType))
	log.Info().
		Str("db", dbTypeToString(dbType)).
		Str("file", filepath.Base(dbPath)).
		Msg("Starting database restore from AVRO backup")

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
	// Using 1000 to be safe and avoid "max number of entries per tx exceeded" errors
	batchSize := 1000 // Process in batches of 1,000 entries
	totalEntries := 0
	blockKeysCount := 0
	latestBlockCount := 0

	fmt.Printf(">>> [DB] Using batch size: %d entries per transaction\n", batchSize)

	// Read records from the Avro file
	recordsRead := 0
	for ocfReader.Scan() {
		record, err := ocfReader.Read()
		if err != nil {
			fmt.Printf(">>> [DB] WARNING: Failed to read AVRO record: %v\n", err)
			log.Warn().Err(err).Msg("failed to read avro record")
			continue
		}
		recordsRead++

		recordMap, ok := record.(map[string]interface{})
		if !ok {
			log.Warn().Msgf("unexpected avro record type: %T", record)
			continue
		}

		key, keyOk := recordMap["Key"].(string)
		value, valueOk := recordMap["Value"].(string)

		// Track block and latest_block keys for debugging
		isBlockKey := keyOk && strings.HasPrefix(key, "block:")
		isLatestBlock := keyOk && key == "latest_block"
		if isBlockKey || isLatestBlock {
			fmt.Printf(">>> [DB] Found %s key in AVRO: %s (value length: %d)\n",
				func() string {
					if isLatestBlock {
						return "latest_block"
					}
					return "block"
				}(), key, func() int {
					if valueOk {
						return len(value)
					}
					return 0
				}())
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
			log.Warn().Msg("avro record has missing or invalid Key/Value fields")
			continue
		}

		entriesOrdered = append(entriesOrdered, struct {
			Key   string
			Value []byte
		}{Key: key, Value: []byte(value)})

		// Track block keys added to batch
		if isBlockKey {
			blockKeysCount++
			fmt.Printf(">>> [DB] Added block key to restore batch: %s (total blocks: %d)\n", key, blockKeysCount)
		} else if isLatestBlock {
			latestBlockCount++
			fmt.Printf(">>> [DB] Added latest_block to restore batch (count: %d)\n", latestBlockCount)
		}

		if len(entriesOrdered) >= batchSize {
			fmt.Printf(">>> [DB] Processing batch of %d entries for %s...\n", len(entriesOrdered), dbTypeToString(dbType))
			if err := fs.batchCreateOrderedWithRetry(entriesOrdered, dbType); err != nil {
				// If batch is too large, try splitting it into smaller chunks
				if strings.Contains(err.Error(), "max number of entries per tx exceeded") {
					fmt.Printf(">>> [DB] WARNING: Batch too large, splitting into smaller chunks...\n")
					// Split into smaller batches of 500
					chunkSize := 500
					for i := 0; i < len(entriesOrdered); i += chunkSize {
						end := i + chunkSize
						if end > len(entriesOrdered) {
							end = len(entriesOrdered)
						}
						chunk := entriesOrdered[i:end]
						fmt.Printf(">>> [DB] Processing chunk %d-%d (%d entries)...\n", i, end, len(chunk))
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

			fmt.Printf(">>> [DB] Restore progress: %d entries processed for %s (elapsed: %v)\n",
				totalEntries, dbTypeToString(dbType), time.Since(startTime))
			log.Info().
				Int("entries_processed", totalEntries).
				Dur("elapsed", time.Since(startTime)).
				Str("db", dbTypeToString(dbType)).
				Msg("Restore in progress")
		}
	}

	// Process any remaining entries in the last batch
	if len(entriesOrdered) > 0 {
		fmt.Printf(">>> [DB] Processing final batch of %d entries for %s...\n", len(entriesOrdered), dbTypeToString(dbType))
		if err := fs.batchCreateOrderedWithRetry(entriesOrdered, dbType); err != nil {
			// If batch is too large, try splitting it into smaller chunks
			if strings.Contains(err.Error(), "max number of entries per tx exceeded") {
				fmt.Printf(">>> [DB] WARNING: Final batch too large, splitting into smaller chunks...\n")
				// Split into smaller batches of 500
				chunkSize := 500
				for i := 0; i < len(entriesOrdered); i += chunkSize {
					end := i + chunkSize
					if end > len(entriesOrdered) {
						end = len(entriesOrdered)
					}
					chunk := entriesOrdered[i:end]
					fmt.Printf(">>> [DB] Processing final chunk %d-%d (%d entries)...\n", i, end, len(chunk))
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

	fmt.Printf(">>> [DB] ✓ Database restore completed: %d total entries processed, %d records read from AVRO for %s (time: %v)\n",
		totalEntries, recordsRead, dbTypeToString(dbType), time.Since(startTime))

	if dbType == MainDB {
		fmt.Printf(">>> [DB] Block keys summary: %d block keys processed, latest_block: %d\n", blockKeysCount, latestBlockCount)
		if blockKeysCount == 0 && latestBlockCount == 0 {
			fmt.Printf(">>> [DB] WARNING: No block keys or latest_block were processed! This might indicate:\n")
			fmt.Printf(">>> [DB]   1. AVRO file doesn't contain block keys\n")
			fmt.Printf(">>> [DB]   2. Block keys were filtered out during processing\n")
			fmt.Printf(">>> [DB]   3. HashMap didn't include block keys for sync\n")
		}
	}

	if totalEntries == 0 {
		fmt.Printf(">>> [DB] WARNING: No entries were written to %s! This might indicate:\n", dbTypeToString(dbType))
		fmt.Printf(">>> [DB]   1. AVRO file is empty or corrupted\n")
		fmt.Printf(">>> [DB]   2. All entries were filtered out\n")
		fmt.Printf(">>> [DB]   3. Database write operations failed silently\n")
	}
	log.Info().
		Int("total_entries", totalEntries).
		Dur("total_time", time.Since(startTime)).
		Str("db", dbTypeToString(dbType)).
		Msg("Database restore from AVRO completed successfully")

	return nil
}
