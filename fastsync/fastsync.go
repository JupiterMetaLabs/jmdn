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
)

// Constants for FastSync
const (
	SyncProtocolID  = config.SyncProtocol
	SyncBatchSize   = 200
	MaxRetries      = 3
	SyncTimeout     = 5 * time.Minute
	RequestTimeout  = 30 * time.Second
	ResponseTimeout = 60 * time.Second
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

	// Process CRDTs
	if err := fs.storeCRDTs(nil, batchData.CRDTs); err != nil { // Pass nil for crdtEngine
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
			Type:        TypeBatchAck,
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
		Timestamp:  time.Now().Unix(),
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

	if err := fs.storeCRDTs(nil, batchData.CRDTs); err != nil { // Pass nil for crdtEngine
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
		Timestamp:   time.Now().Unix(),
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
	msg.Type = TypeHashMapExchangeSYNC

	// Debug: Log the message being sent
	log.Info().
		Str("peer_id", peerID.String()).
		Str("message_type", msg.Type).
		Str("message_data", string(msg.Data)).
		Int("data_length", len(msg.Data)).
		Msg("Sending Phase2_Sync request")

	if err := writeMessage(writer, stream, msg); err != nil {
		log.Error().Err(err).Msg("Failed to write Phase2_Sync message")
		return nil, "", "", err
	}

	log.Info().Msg("Phase2_Sync message sent successfully, waiting for response...")

	Phase2_Response, err := readMessage(reader, stream)
	if err != nil {
		return nil, "", "", err
	}

	// Debug: Log the received response
	log.Info().
		Str("response_type", Phase2_Response.Type).
		Str("response_data", string(Phase2_Response.Data)).
		Int("data_length", len(Phase2_Response.Data)).
		Msg("Received Phase2_Response")

	expectedData := json.RawMessage([]byte(`"Message From Server"`))
	if !bytes.Equal(Phase2_Response.Data, expectedData) {
		log.Error().
			Str("expected", string(expectedData)).
			Str("received", string(Phase2_Response.Data)).
			Int("expected_len", len(expectedData)).
			Int("received_len", len(Phase2_Response.Data)).
			Msg("Response data mismatch")
		return nil, "", "", fmt.Errorf("unexpected response data: expected '%s', got '%s'", string(expectedData), string(Phase2_Response.Data))
	}

	return Phase2_Response, Phase2_Response.HashMap_MetaData.Main_HashMap_MetaData.Checksum, Phase2_Response.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum, nil
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
			Timestamp:        time.Now().Unix(),
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

func (fs *FastSync) PushDataToDB(msg *SyncMessage, dbType DatabaseType, dbPath string) error {
	// Get the appropriate database client
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

	// Ensure the backup file exists
	fileInfo, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		log.Info().Str("path", dbPath).Msg("AVRO file does not exist, skipping restore.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to stat avro file %s: %w", dbPath, err)
	}
	if fileInfo.Size() == 0 {
		log.Info().Str("path", dbPath).Msg("AVRO file is empty, skipping restore.")
		return nil
	}

	// Open the backup file
	file, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open avro file: %w", err)
	}
	defer file.Close()

	startTime := time.Now()
	log.Info().
		Str("db", dbTypeToString(dbType)).
		Str("file", filepath.Base(dbPath)).
		Msg("Starting database restore from AVRO backup")

	// Create an OCF reader for the Avro file
	ocfReader, err := goavro.NewOCFReader(file)
	if err != nil {
		return fmt.Errorf("failed to create avro ocf reader for %s: %w", dbPath, err)
	}

	entriesMap := make(map[string]interface{})
	batchSize := 1000 // Process in batches of 1000 entries
	totalEntries := 0

	// Read records from the Avro file
	for ocfReader.Scan() {
		record, err := ocfReader.Read()
		if err != nil {
			log.Warn().Err(err).Msg("failed to read avro record")
			continue
		}

		recordMap, ok := record.(map[string]interface{})
		if !ok {
			log.Warn().Msgf("unexpected avro record type: %T", record)
			continue
		}

		key, keyOk := recordMap["Key"].(string)
		value, valueOk := recordMap["Value"].(string)

		if !keyOk || !valueOk {
			log.Warn().Msg("avro record has missing or invalid Key/Value fields")
			continue
		}

		entriesMap[key] = []byte(value)

		if len(entriesMap) >= batchSize {
			if err := fs.batchCreateWithRetry(entriesMap, dbType); err != nil {
				return fmt.Errorf("failed to push batch to DB: %w", err)
			}
			totalEntries += len(entriesMap)
			entriesMap = make(map[string]interface{}) // reset map

			// Log progress
			log.Info().
				Int("entries_processed", totalEntries).
				Dur("elapsed", time.Since(startTime)).
				Str("db", dbTypeToString(dbType)).
				Msg("Restore in progress")
		}
	}

	// Process any remaining entries in the last batch
	if len(entriesMap) > 0 {
		if err := fs.batchCreateWithRetry(entriesMap, dbType); err != nil {
			return fmt.Errorf("failed to push final batch to DB: %w", err)
		}
		totalEntries += len(entriesMap)
	}

	log.Info().
		Int("total_entries", totalEntries).
		Dur("total_time", time.Since(startTime)).
		Str("db", dbTypeToString(dbType)).
		Msg("Database restore from AVRO completed successfully")

	return nil
}
