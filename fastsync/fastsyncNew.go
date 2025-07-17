package fastsync

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/crdt/IBLT"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
)

const (
	ALGORITHM       = "SHA128"
	IBLT_SIZE       = 50000
	IBLT_HASH_COUNT = 10
)

type MerkleRoot struct {
	MainMerkleRoot []byte `json:"main_merkle_root"`
	AccountsMerkleRoot []byte `json:"accounts_merkle_root"`
}

type SyncMessage struct {
	Type                string          `json:"type"`
	SenderID            string          `json:"sender_id"`
	TxID                uint64          `json:"tx_id,omitempty"`
	StartTxID           uint64          `json:"start_tx_id,omitempty"`
	EndTxID             uint64          `json:"end_tx_id,omitempty"`
	BatchNumber         int             `json:"batch_number,omitempty"`
	TotalBatches        int             `json:"total_batches,omitempty"`
	MerkleRoot          MerkleRoot      `json:"merkle_root,omitempty"`
	KeysCount           int             `json:"keys_count,omitempty"`
	Data                json.RawMessage `json:"data,omitempty"`
	Success             bool            `json:"success,omitempty"`
	ErrorMessage        string          `json:"error_message,omitempty"`
	Timestamp           int64           `json:"timestamp"`
	DBType              DatabaseType    `json:"db_type,omitempty"`
	MainIBLT_Params     *IBLT_Params    `json:"main_iblt_params,omitempty"`
	AccountsIBLT_Params *IBLT_Params    `json:"accounts_iblt_params,omitempty"`
}

type FastSync struct {
	host         host.Host
	mainDB       *config.ImmuClient
	accountsDB   *config.ImmuClient
	mainIBLT     *IBLT.IBLT
	accountsIBLT *IBLT.IBLT
	active       map[peer.ID]*syncState
	mutex        sync.RWMutex
}

type IBLT_MetaData struct {
	Main_SYNC_MetaData     *MetaData
	Accounts_SYNC_MetaData *MetaData
}

type MetaData struct {
	Algorithm string // SHA128
	Checksum  string
}

type TypeIBLTExchangeSYNC_Struct struct {
	IBLT_MAIN_SYNC         *IBLT.IBLT
	IBLT_Accounts_SYNC     *IBLT.IBLT
	MetaData               *IBLT_MetaData
}

type TypeIBLTExchangeClient_Struct struct {
	Client_IBLT_MAIN         *IBLT.IBLT
	Client_IBLT_Accounts     *IBLT.IBLT
	MetaData                 *IBLT_MetaData
}

type IBLT_Params struct {
	M int
	K int
	ExpectedDiffRatio float64
	SafetyFactor float64
}

type FastSync_Request struct {
	Main_IBLT_Params *IBLT_Params
	Accounts_IBLT_Params *IBLT_Params
	Main_DB_KeyCount int
	Accounts_DB_KeyCount int
}

const (
	TypeSyncRequest         = "SYNC_REQ"
	TypeSyncResponse        = "SYNC_RESP"
	TypeIBLTExchangeClient  = "IBLT_EXCHANGE_CLIENT"
	TypeIBLTExchangeSYNC    = "IBLT_EXCHANGE_SYNC"
	TypeBatchRequest        = "BATCH_REQ"
	TypeBatchData           = "BATCH_DATA"
	TypeSyncComplete        = "SYNC_COMPLETE"
	TypeSyncAbort           = "SYNC_ABORT"
	TypeVerificationRequest = "VERIFY_REQ"
	TypeVerificationResult  = "VERIFY_RESP"
	TypeBatchAck            = "BATCH_ACK"
	TypeTransferFile        = "TRANSFER_FILE"
)

func (fs *FastSync) getDB(dbType DatabaseType) *config.ImmuClient {
	if dbType == AccountsDB {
		return fs.accountsDB
	}
	return fs.mainDB
}

func GetDBData_Default(db *config.ImmuClient, prefix string) ([]string, error) {
	keys, err := DB_OPs.GetAllKeys(db, prefix)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func GetDBData_Accounts(db *config.ImmuClient, prefix string) ([]string, error) {
	keys := make([]string, 0)
	DIDs, err := DB_OPs.ListAllDIDs(db, 1000)
	if err != nil {
		return nil, err
	}
	for _, DID := range DIDs {
		keys = append(keys, DID.PublicKey)
	}
	return keys, nil
}

func (fs *FastSync) MakeIBLT_Default() (*IBLT.IBLT, error) {
	keys, err := GetDBData_Default(fs.mainDB, "block:")
	if err != nil {
		return nil, err
	}
	iblt := IBLT.New(IBLT_SIZE, IBLT_HASH_COUNT)
	for _, key := range keys {
		iblt.Insert([]byte(key))
	}
	return iblt, nil
}

func (fs *FastSync) MakeIBLT_Accounts() (*IBLT.IBLT, error) {
	keys, err := GetDBData_Accounts(fs.accountsDB, "did:")
	if err != nil {
		return nil, err
	}
	iblt := IBLT.New(IBLT_SIZE, IBLT_HASH_COUNT)
	for _, key := range keys {
		iblt.Insert([]byte(key))
	}
	return iblt, nil
}

func NewFastSync(h host.Host, mainDB, accountsDB *config.ImmuClient) *FastSync {
	fs := &FastSync{
		host:       h,
		mainDB:     mainDB,
		accountsDB: accountsDB,
		active:     make(map[peer.ID]*syncState),
	}
	mainIBLT, err := fs.MakeIBLT_Default()
	if err != nil {
		log.Error().Msg("Failed to make IBLT for main database")
		return nil
	}

	accountsIBLT, err := fs.MakeIBLT_Accounts()
	if err != nil {
		log.Error().Msg("Failed to make IBLT for accounts database")
		return nil
	}

	fs.mainIBLT = mainIBLT
	fs.accountsIBLT = accountsIBLT

	h.SetStreamHandler(SyncProtocolID, fs.handleStream)
	log.Info().Msg("FastSync initialized with multi-database support")

	return fs
}

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
		case TypeBatchRequest:
			response, handleErr = fs.handleBatchRequest(peerID, msg)
		case TypeBatchData:
			response, handleErr = fs.handleBatchData(peerID, msg)
		case TypeVerificationRequest:
			response, handleErr = fs.handleVerification(peerID)
		case TypeSyncComplete:
			response, handleErr = fs.handleSyncComplete(peerID, msg)
		case TypeIBLTExchangeSYNC:
			response, handleErr = fs.handleIBLTExchangeSYNC(peerID)  //Server will send the SYNC IBLT to the client
		case TypeIBLTExchangeClient:
			response, handleErr = fs.handleIBLTExchangeClient(peerID)  //Client will send the IBLT to the server
			// case TypeTransferFile:
			// 	response, handleErr = fs.handleTransferFile(peerID, msg)
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

func (fs *FastSync) handleIBLTExchangeSYNC(peerID peer.ID) (*SyncMessage, error) {
	log.Info().
	Str("peer", peerID.String()).
	Msg("Received IBLT Exchange Client Request")

	ServerIBLT_MAIN, err := fs.MakeIBLT_Default()
	if err != nil {
		return nil, err
	}
	ServerIBLT_Accounts, err := fs.MakeIBLT_Accounts()
	if err != nil {
		return nil, err
	}

	computeIBLT_MAIN_SYNC, err := ServerIBLT_MAIN.Subtract(fs.mainIBLT)
	if err != nil {
		return nil, err
	}
	computeIBLT_Accounts_SYNC, err := ServerIBLT_Accounts.Subtract(fs.accountsIBLT)
	if err != nil {
		return nil, err
	}


	ComputerCHECKSUM_MAIN := sha1.New()
	ComputerCHECKSUM_MAIN.Write([]byte(computeIBLT_MAIN_SYNC.String()))
	ComputerCHECKSUM_MAIN_Value := ComputerCHECKSUM_MAIN.Sum(nil)
	ComputerCHECKSUM_MAIN_Value_String := hex.EncodeToString(ComputerCHECKSUM_MAIN_Value)

	ComputerCHECKSUM_Accounts := sha1.New()
	ComputerCHECKSUM_Accounts.Write([]byte(computeIBLT_Accounts_SYNC.String()))
	ComputerCHECKSUM_Accounts_Value := ComputerCHECKSUM_Accounts.Sum(nil)
	ComputerCHECKSUM_Accounts_Value_String := hex.EncodeToString(ComputerCHECKSUM_Accounts_Value)

	IBLTExchangeClientStruct := TypeIBLTExchangeSYNC_Struct{
		IBLT_MAIN_SYNC: computeIBLT_MAIN_SYNC,
		IBLT_Accounts_SYNC: computeIBLT_Accounts_SYNC,
		MetaData: &IBLT_MetaData{
			Main_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum: ComputerCHECKSUM_MAIN_Value_String,
			},
			Accounts_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum: ComputerCHECKSUM_Accounts_Value_String,
			},
		},
	}

	data, err := json.Marshal(IBLTExchangeClientStruct)
	if err != nil {
		return nil, err
	}

	return &SyncMessage{
		Type: TypeIBLTExchangeClient,
		SenderID: fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Data: data,
	}, nil

}

func (fs *FastSync) handleIBLTExchangeClient(peerID peer.ID) (*SyncMessage, error) {
	// First step Client will send the IBLT to the server to get the SYNC_IBLT
	log.Info().
	Str("peer", peerID.String()).
	Msg("Received IBLT Exchange Client Request")

	//Compute Client IBLT Checksum
	ComputerCHECKSUM_MAIN := sha1.New()
	ComputerCHECKSUM_MAIN.Write([]byte(fs.mainIBLT.String()))
	ComputerCHECKSUM_MAIN_Value := ComputerCHECKSUM_MAIN.Sum(nil)
	ComputerCHECKSUM_MAIN_Value_String := hex.EncodeToString(ComputerCHECKSUM_MAIN_Value)

	ComputerCHECKSUM_Accounts := sha1.New()
	ComputerCHECKSUM_Accounts.Write([]byte(fs.accountsIBLT.String()))
	ComputerCHECKSUM_Accounts_Value := ComputerCHECKSUM_Accounts.Sum(nil)
	ComputerCHECKSUM_Accounts_Value_String := hex.EncodeToString(ComputerCHECKSUM_Accounts_Value)

	//Send IBLT which was in the fs to the server
	Client_data := TypeIBLTExchangeClient_Struct{
		Client_IBLT_MAIN: fs.mainIBLT,
		Client_IBLT_Accounts: fs.accountsIBLT,
		MetaData: &IBLT_MetaData{
			Main_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum: ComputerCHECKSUM_MAIN_Value_String,
			},
			Accounts_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum: ComputerCHECKSUM_Accounts_Value_String,
			},
		},
	}

	data, err := json.Marshal(Client_data)
	if err != nil {
		return nil, err
	}

	return &SyncMessage{
		Type: TypeIBLTExchangeSYNC,
		SenderID: fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Data: data,
	}, nil
}

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

	// Calculate key counts
	mainKeys, _ := DB_OPs.GetKeys(fs.mainDB, "block:", 0)
	accountsKeys, _ := DB_OPs.GetKeys(fs.accountsDB, "did:", 0)
	myMainM, myMainK := calcOptimalIBLTParams(len(mainKeys))
	myAccountsM, myAccountsK := calcOptimalIBLTParams(len(accountsKeys))

	if msg.MainIBLT_Params == nil {
		msg.MainIBLT_Params = &IBLT_Params{
			M: myMainM,
			K: myMainK,
		}
	}
	if msg.AccountsIBLT_Params == nil {
		msg.AccountsIBLT_Params = &IBLT_Params{
			M: myAccountsM,
			K: myAccountsK,
		}
	}

	MainIBLT_Params := &IBLT_Params{
		M: max(myMainM, msg.MainIBLT_Params.M),
		K: max(myMainK, msg.MainIBLT_Params.K),
	}
	AccountsIBLT_Params := &IBLT_Params{
		M: max(myAccountsM, msg.AccountsIBLT_Params.M),
		K: max(myAccountsK, msg.AccountsIBLT_Params.K),
	}

	log.Info().Str("peer", peerID.String()).Uint64("peer_tx_id", msg.TxID).Uint64("main_tx_id", mainState.TxId).Uint64("accounts_tx_id", accountsState.TxId).Int("agreedMainM", MainIBLT_Params.M).Int("agreedMainK", MainIBLT_Params.K).Int("agreedAccountsM", AccountsIBLT_Params.M).Int("agreedAccountsK", AccountsIBLT_Params.K).Msg("Negotiated IBLT params")

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
		Type:                TypeSyncResponse,
		SenderID:            fs.host.ID().String(),
		TxID:                mainState.TxId,
		MerkleRoot:          MerkleRoot{MainMerkleRoot: mainState.TxHash, AccountsMerkleRoot: accountsState.TxHash},
		TotalBatches:        totalBatches,
		Data:                statesData,
		Timestamp:           time.Now().Unix(),
		MainIBLT_Params:     MainIBLT_Params,
		AccountsIBLT_Params: AccountsIBLT_Params,
	}, nil
}

func calculateBatchCount(startTxID, endTxID uint64) int {
	if startTxID >= endTxID {
		return 1 // At least one batch even if no diff
	}

	txDiff := endTxID - startTxID
	return int((txDiff + SyncBatchSize - 1) / SyncBatchSize)
}

// handleBatchRequest processes an initial sync request
func (fs *FastSync) handleBatchRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	fs.mutex.RLock()
	_, exists := fs.active[peerID]
	fs.mutex.RUnlock()
	if !exists {
		return nil, fmt.Errorf("no active sync for peer %s", peerID)
	}
	db := fs.getDB(msg.DBType)

	// Fetch only the desired prefixes
	entries, crdts, err := fs.getBatchData(db, msg.DBType)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch data: %w", err)
	}

	// Cap entries & CRDTs
	const maxEntriesPerBatch = 100
	if len(entries) > maxEntriesPerBatch {
		entries = entries[:maxEntriesPerBatch]
	}
	const maxCRDTsPerBatch = 50
	if len(crdts) > maxCRDTsPerBatch {
		crdts = crdts[:maxCRDTsPerBatch]
	}

	// Serialize batch payload
	batchData := BatchData{Entries: entries, CRDTs: crdts, DBType: msg.DBType}
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

	fmt.Printf(
		"Sending batch data → peer=%s batch=%d entries=%d crdts=%d db=%s\n",
		peerID.String(),
		msg.BatchNumber,
		len(entries),
		len(crdts),
		dbTypeToString(msg.DBType),
	)

	return &SyncMessage{
		Type:        TypeBatchData,
		SenderID:    fs.host.ID().String(),
		BatchNumber: msg.BatchNumber,
		Data:        dataBytes,
		DBType:      msg.DBType,
		Timestamp:   time.Now().Unix(),
	}, nil
}

func (fs *FastSync) getBatchData(
	db *config.ImmuClient,
	dbType DatabaseType,
) ([]KeyValueEntry, []json.RawMessage, error) {
	var entries []KeyValueEntry
	var crdts []json.RawMessage

	keys, err := DB_OPs.GetKeys(db, "", 0) // Get all keys for IBLT
	if err != nil {
		return nil, nil, err
	}

	for _, key := range keys {
		// pick the correct prefix
		switch dbType {
		case MainDB:
			if !strings.HasPrefix(key, "block:") {
				continue
			}
		case AccountsDB:
			if !strings.HasPrefix(key, "did:") {
				continue
			}
		}

		data, err := DB_OPs.Read(db, key)
		if err != nil {
			continue
		}

		entries = append(entries, KeyValueEntry{
			Key:       []byte(key),
			Value:     data,
			Timestamp: time.Now(),
			TxID:      0,
		})
	}

	return entries, crdts, nil
}