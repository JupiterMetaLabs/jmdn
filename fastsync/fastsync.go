package fastsync

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/crdt"
	"gossipnode/crdt/IBLT"
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
	MainDB DatabaseType = 0
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
	mainState, err := DB_OPs.GetDatabaseState(fs.mainDB)
	if err != nil {
		return nil, fmt.Errorf("failed to get main database state: %w", err)
	}

    accountsState, err := DB_OPs.GetDatabaseState(fs.accountsDB)
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
	// Calculate key counts (could be cached)
	mainKeys, _ := DB_OPs.GetKeys(fs.mainDB, "block:", 0)
	accountsKeys, _ := DB_OPs.GetKeys(fs.accountsDB, "did:", 0)
	mainM, mainK := calcOptimalIBLTParams(len(mainKeys))
	accountsM, accountsK := calcOptimalIBLTParams(len(accountsKeys))
	log.Info().Str("peer", peerID.String()).Uint64("main_tx_id", mainState.TxId).Uint64("accounts_tx_id", accountsState.TxId).Msg("Starting sync with peer")
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	MainIBLT_Params := &IBLT_Params{
		M: mainM,
		K: mainK,
	}
	AccountsIBLT_Params := &IBLT_Params{
		M: accountsM,
		K: accountsK,
	}
	stream, err := fs.host.NewStream(ctx, peerID, SyncProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()
	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)
	// Send sync request with IBLT params
	syncReq := SyncMessage{
		Type:                TypeSyncRequest,
		SenderID:            fs.host.ID().String(),
		TxID:                mainState.TxId,
		Timestamp:           time.Now().Unix(),
		IBLT_MetaData:       &IBLT_MetaData_Struct{
			Main_IBLT_Params:     MainIBLT_Params,
			Accounts_IBLT_Params: AccountsIBLT_Params,
		},
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
		// Use agreed IBLT params from server

		log.Info().Int("mainM", mainM).Int("mainK", mainK).Int("accountsM", accountsM).Int("accountsK", accountsK).Msg("Agreed IBLT params for sync")
		// TODO: Use these params for all IBLT operations
		return fs.processSyncWithIBLTParams(peerID, stream, reader, writer, response, MainIBLT_Params.M, MainIBLT_Params.K, AccountsIBLT_Params.M, AccountsIBLT_Params.K)
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
		db := fs.getDB(dbState.Type)
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

// Add processSyncWithIBLTParams as a stub for now
func (fs *FastSync) processSyncWithIBLTParams(peerID peer.ID, stream network.Stream, reader *bufio.Reader, writer *bufio.Writer, response *SyncMessage, mainMinM int, mainMinK int, accountsMinM int, accountsMinK int) error {
	if response.IBLT_MetaData == nil {
		response.IBLT_MetaData = &IBLT_MetaData_Struct{
			Main_IBLT_Params: &IBLT_Params{
				M: max(mainMinM, response.IBLT_MetaData.Main_IBLT_Params.M),
				K: max(mainMinK, response.IBLT_MetaData.Main_IBLT_Params.K),
			},
			Accounts_IBLT_Params: &IBLT_Params{
				M: max(accountsMinM, response.IBLT_MetaData.Accounts_IBLT_Params.M),
				K: max(accountsMinK, response.IBLT_MetaData.Accounts_IBLT_Params.K),
			},
		}
	}
	// TODO: Use these params for all IBLT operations in the sync process
	return fs.processSync(peerID, stream, reader, writer, response)
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

// Helper function for min value
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Helper for max
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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

// --- IBLT parameter negotiation logic ---
// Helper to calculate optimal IBLT params
func calcOptimalIBLTParams(keyCount int) (m, k int) {
	return IBLT.OptimalIBLTParams(keyCount, 1.0, 2.0)
}

func computeCHECKSUM(iblt *IBLT.IBLT) (string, error) {
	ComputerCHECKSUM := sha1.New()
	ComputerCHECKSUM.Write([]byte(iblt.String()))
	ComputerCHECKSUM_Value := ComputerCHECKSUM.Sum(nil)
	ComputerCHECKSUM_Value_String := hex.EncodeToString(ComputerCHECKSUM_Value)
	if ComputerCHECKSUM_Value_String == "" {
		return "", fmt.Errorf("failed to compute checksum for IBLT")
	}
	return ComputerCHECKSUM_Value_String, nil
}

func isAbove20Percent(fs *FastSync) (bool,error) {
	computeServerKeys, err := DB_OPs.GetKeys(fs.mainDB, "block:", 0)
	if err != nil {
		return false, err
	}
	computeServerKeyCount := len(computeServerKeys)
	computeServerAccountsKeys, err := DB_OPs.GetKeys(fs.accountsDB, "did:", 0)
	if err != nil {
		return false, err
	}
	computeServerAccountsKeyCount := len(computeServerAccountsKeys)

	//debugging
	fmt.Println("computeServerKeyCount", computeServerKeyCount)
	fmt.Println("computeServerAccountsKeyCount", computeServerAccountsKeyCount)
	fmt.Println("fs.IBLT_MetaData.Main_DB_KeyCount", fs.IBLT_MetaData.Main_DB_KeyCount)
	fmt.Println("fs.IBLT_MetaData.Accounts_DB_KeyCount", fs.IBLT_MetaData.Accounts_DB_KeyCount)
	
	return fs.IBLT_MetaData.Main_DB_KeyCount > int(float64(computeServerKeyCount)*0.2) || fs.IBLT_MetaData.Accounts_DB_KeyCount > int(float64(computeServerAccountsKeyCount)*0.2), nil
}

func (fs *FastSync) Phase2_Sync(msg *SyncMessage, peerID peer.ID, stream network.Stream, writer *bufio.Writer, reader *bufio.Reader) (*TypeIBLTExchangeSYNC_Struct, string, string, error) {
	Phase2, err := fs.handleIBLTExchangeClient(peerID)
	if err != nil {
		return nil, "", "", err
	}

	// Send the IBLT to the server to get the SYNC_IBLT
	Phase2.Type = TypeIBLTExchangeSYNC
	Phase2.IBLT_MetaData = msg.IBLT_MetaData

	if err := writeMessage(writer, stream, Phase2); err != nil {
		return nil, "", "", err
	}

	Phase2_Response, err := readMessage(reader, stream)
	if err != nil {
		return nil, "", "", err
	}

	var data TypeIBLTExchangeSYNC_Struct
	if err := json.Unmarshal(Phase2_Response.Data, &data); err != nil {
		return nil, "", "", err
	}

	// Verify the metadata checksum
	computeMainChecksum, err := computeCHECKSUM(data.IBLT_MAIN_SYNC)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to compute main IBLT checksum: %w", err)
	}
	
	computeAccountCheksum, err := computeCHECKSUM(data.IBLT_Accounts_SYNC)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to compute accounts IBLT checksum: %w", err)
	}

	return &data, computeMainChecksum, computeAccountCheksum, nil
}


func (fs *FastSync) Phase3_FileRequest(msg *SyncMessage, peerID peer.ID, stream network.Stream, writer *bufio.Writer, reader *bufio.Reader, MainChecksum string, AccountChecksum string) error {
	// Phase 3: receive BAK File from the server
	// -> Server will send the BAK file to the client
	// -> Client will receive the BAK file and store it in the BAK_FILE_PATH
	// -> Client will append the transactions in the BAK file 
	
	// 1. Send the request to the server to send the BAK File. Change the msg type to RequestFiletransfer

	msg.Type = RequestFiletransfer
	IBLT_data := &TypeIBLTExchangeSYNC_Struct{
		IBLT_MAIN_SYNC: fs.mainIBLT,
		IBLT_Accounts_SYNC: fs.accountsIBLT,
		MetaData: &IBLT_MetaData{
			Main_SYNC_MetaData: &MetaData{
			Algorithm: ALGORITHM,
			Checksum: MainChecksum,
			},
			Accounts_SYNC_MetaData: &MetaData{
			Algorithm: ALGORITHM,
			Checksum: AccountChecksum,
			},
		},
	}
	
	// 2. Convert the IBLT data to bytes to send in SyncMessagdata
	data, err := json.Marshal(IBLT_data)
	if err != nil{
		return fmt.Errorf("failed to marshal IBLT data: %w", err)
	}

	// 3. Populate the message with the data and send it
	msg.Data = data
	if err := writeMessage(writer, stream, msg); err != nil {
		return fmt.Errorf("failed to send file transfer request: %w", err)
	}

		// 4. Wait for the server's response. The server should send a SyncComplete message
	// after successfully initiating the file transfers.
	response, err := readMessage(reader, stream)
	if err != nil {
		return fmt.Errorf("failed to read response for file transfer request: %w", err)
	}


	if response.Type == TypeSyncComplete && response.Success {
		log.Info().Str("peer", peerID.String()).Msg("Server confirmed file transfer initiation. Sync complete.")
		return nil
	}

	return fmt.Errorf("unexpected response after file request: type=%s, success=%t, err=%s", response.Type, response.Success, response.ErrorMessage)
}