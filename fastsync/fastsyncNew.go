package fastsync

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/crdt/IBLT"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
)

const (
	ALGORITHM       = "SHA128"
	BAK_FILE_PATH   = "fastsync/.temp/"
)

type MerkleRoot struct {
	MainMerkleRoot     []byte `json:"main_merkle_root"`
	AccountsMerkleRoot []byte `json:"accounts_merkle_root"`
}

type SyncMessage struct {
	Type          string                `json:"type"`
	SenderID      string                `json:"sender_id"`
	TxID          uint64                `json:"tx_id,omitempty"`
	StartTxID     uint64                `json:"start_tx_id,omitempty"`
	EndTxID       uint64                `json:"end_tx_id,omitempty"`
	BatchNumber   int                   `json:"batch_number,omitempty"`
	TotalBatches  int                   `json:"total_batches,omitempty"`
	MerkleRoot    MerkleRoot            `json:"merkle_root,omitempty"`
	KeysCount     int                   `json:"keys_count,omitempty"`
	Data          json.RawMessage       `json:"data,omitempty"`
	Success       bool                  `json:"success,omitempty"`
	ErrorMessage  string                `json:"error_message,omitempty"`
	Timestamp     int64                 `json:"timestamp"`
	DBType        DatabaseType          `json:"db_type,omitempty"`
	IBLT_MetaData *IBLT_MetaData_Struct `json:"iblt_meta_data,omitempty"`
}

type FastSync struct {
	host         host.Host
	mainDB       *config.ImmuClient
	accountsDB   *config.ImmuClient
	mainIBLT     *IBLT.IBLT
	accountsIBLT *IBLT.IBLT
	active       map[peer.ID]*syncState
	mutex        sync.RWMutex
	IBLT_MetaData *IBLT_MetaData_Struct
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
	IBLT_MAIN_SYNC     *IBLT.IBLT
	IBLT_Accounts_SYNC *IBLT.IBLT
	MetaData           *IBLT_MetaData
}

type TypeIBLTExchangeClient_Struct struct {
	Client_IBLT_MAIN     *IBLT.IBLT
	Client_IBLT_Accounts *IBLT.IBLT
	MetaData             *IBLT_MetaData
}

type IBLT_Params struct {
	M                 int
	K                 int
	ExpectedDiffRatio float64
	SafetyFactor      float64
}

type IBLT_MetaData_Struct struct {
	Main_IBLT_Params     *IBLT_Params
	Accounts_IBLT_Params *IBLT_Params
	Main_DB_KeyCount     int
	Accounts_DB_KeyCount int
	isAbove20 			 bool
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
	RequestFiletransfer     = "REQUEST_FILE_TRANSFER"
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
	iblt := IBLT.New(fs.IBLT_MetaData.Main_IBLT_Params.M, fs.IBLT_MetaData.Main_IBLT_Params.K)
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
	iblt := IBLT.New(fs.IBLT_MetaData.Accounts_IBLT_Params.M, fs.IBLT_MetaData.Accounts_IBLT_Params.K)
	for _, key := range keys {
		iblt.Insert([]byte(key))
	}
	return iblt, nil
}

func NewFastSync(h host.Host, mainDB, accountsDB *config.ImmuClient) *FastSync {
	// TODO: For production environments, consider implementing a peer authentication
	// mechanism here. This could involve checking connecting peer IDs against an
	// allowlist or using a more advanced authentication protocol to prevent
	// unauthorized nodes from initiating syncs.
	fs := &FastSync{
		host:       h,
		mainDB:     mainDB,
		accountsDB: accountsDB,
		active:     make(map[peer.ID]*syncState),
	}

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
		case TypeIBLTExchangeClient:
			response, handleErr = fs.handleIBLTExchange(peerID, msg)
		case RequestFiletransfer: // This will trigger for the file transfer
			response, handleErr = fs.MakeBAKFile_Transfer(peerID, msg)
		default:
			log.Warn().Str("type", msg.Type).Msg("Unknown message type")
			continue
		}

		if handleErr != nil {
			log.Error().Err(handleErr).
				Str("msg_type", msg.Type).
				Str("peer", peerID.String()). // Session ID might not exist yet
				Msg("Error handling message")

			// Send abort message
			abortMsg := &SyncMessage{
				Type:         TypeSyncAbort,
				SenderID:     fs.host.ID().String(),
				ErrorMessage: handleErr.Error(),
				Timestamp:    time.Now().Unix(),
			}

			writeMessage(writer, stream, abortMsg)
			return
		}

		if response != nil {
			if err := writeMessage(writer, stream, response); err != nil {
				log.Error().Err(err).Msg("Failed to send response")
				break
			}
		}
	}
}

func (fs *FastSync) handleIBLTExchange(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// Get sync state for this peer
	fs.mutex.RLock()
	state, exists := fs.active[peerID]
	fs.mutex.RUnlock()
	if !exists {
		return nil, fmt.Errorf("no active sync session found for peer %s", peerID)
	}

	log.Info().Str("peer", peerID.String()).Str("session", state.sessionID).Msg("Handling IBLT exchange request")

	var clientIBLTs TypeIBLTExchangeClient_Struct
	if err := json.Unmarshal(msg.Data, &clientIBLTs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal client IBLT data: %w", err)
	}

	// Create server IBLTs using the negotiated parameters from the sync state
	// Main IBLT
	mainKeys, err := GetDBData_Default(fs.mainDB, "block:")
	if err != nil {
		return nil, err
	}
	serverMainIBLT := IBLT.New(state.ibltMetaData.Main_IBLT_Params.M, state.ibltMetaData.Main_IBLT_Params.K)
	for _, key := range mainKeys {
		serverMainIBLT.Insert([]byte(key))
	}

	// Accounts IBLT
	accountsKeys, err := GetDBData_Accounts(fs.accountsDB, "did:")
	if err != nil {
		return nil, err
	}
	serverAccountsIBLT := IBLT.New(state.ibltMetaData.Accounts_IBLT_Params.M, state.ibltMetaData.Accounts_IBLT_Params.K)
	for _, key := range accountsKeys {
		serverAccountsIBLT.Insert([]byte(key))
	}

	// Compute the difference (server - client)
	mainDiffIBLT, err := serverMainIBLT.Subtract(clientIBLTs.Client_IBLT_MAIN)
	if err != nil {
		return nil, fmt.Errorf("failed to subtract main IBLT: %w", err)
	}
	accountsDiffIBLT, err := serverAccountsIBLT.Subtract(clientIBLTs.Client_IBLT_Accounts)
	if err != nil {
		return nil, fmt.Errorf("failed to subtract accounts IBLT: %w", err)
	}

	// Create checksums for the diff IBLTs
	mainChecksum, err := computeCHECKSUM(mainDiffIBLT)
	if err != nil {
		return nil, fmt.Errorf("failed to compute main diff IBLT checksum: %w", err)
	}
	accountsChecksum, err := computeCHECKSUM(accountsDiffIBLT)
	if err != nil {
		return nil, fmt.Errorf("failed to compute accounts diff IBLT checksum: %w", err)
	}

	// Prepare the response payload
	syncIBLTs := TypeIBLTExchangeSYNC_Struct{
		IBLT_MAIN_SYNC:     mainDiffIBLT,
		IBLT_Accounts_SYNC: accountsDiffIBLT,
		MetaData: &IBLT_MetaData{
			Main_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  mainChecksum,
			},
			Accounts_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  accountsChecksum,
			},
		},
	}

	data, err := json.Marshal(syncIBLTs)
	if err != nil {
		return nil, err
	}

	return &SyncMessage{
		Type:      TypeIBLTExchangeSYNC,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Data:      data,
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

	// Calculate server key counts and optimal IBLT params
	mainKeys, _ := DB_OPs.GetKeys(fs.mainDB, "block:", 0)
	accountsKeys, _ := DB_OPs.GetKeys(fs.accountsDB, "did:", 0)
	serverMainM, serverMainK := calcOptimalIBLTParams(len(mainKeys))
	serverAccountsM, serverAccountsK := calcOptimalIBLTParams(len(accountsKeys))

	// Defensive: Ensure msg.IBLT_MetaData and its fields are not nil
	var clientMainM, clientMainK, clientAccountsM, clientAccountsK int
	var clientMainKeyCount, clientAccountsKeyCount int
	if msg.IBLT_MetaData != nil {
		if msg.IBLT_MetaData.Main_IBLT_Params != nil {
			clientMainM = msg.IBLT_MetaData.Main_IBLT_Params.M
			clientMainK = msg.IBLT_MetaData.Main_IBLT_Params.K
		} else {
			clientMainM = serverMainM
			clientMainK = serverMainK
		}
		if msg.IBLT_MetaData.Accounts_IBLT_Params != nil {
			clientAccountsM = msg.IBLT_MetaData.Accounts_IBLT_Params.M
			clientAccountsK = msg.IBLT_MetaData.Accounts_IBLT_Params.K
		} else {
			clientAccountsM = serverAccountsM
			clientAccountsK = serverAccountsK
		}
		clientMainKeyCount = msg.IBLT_MetaData.Main_DB_KeyCount
		clientAccountsKeyCount = msg.IBLT_MetaData.Accounts_DB_KeyCount
	} else {
		clientMainM = serverMainM
		clientMainK = serverMainK
		clientAccountsM = serverAccountsM
		clientAccountsK = serverAccountsK
	}

	// Negotiate: take max for each param
	agreedMainM := max(serverMainM, clientMainM)
	agreedMainK := max(serverMainK, clientMainK)
	agreedAccountsM := max(serverAccountsM, clientAccountsM)
	agreedAccountsK := max(serverAccountsK, clientAccountsK)

	// Generate a unique session ID for logging and tracking.
	// This helps differentiate concurrent sync sessions from different peers.
	sessionID, err := newSessionID(peerID)
	if err != nil {
		// Fallback to a simpler ID if random generation fails
		sessionID = fmt.Sprintf("%s-%d", peerID.String()[:8], time.Now().UnixNano())
		log.Warn().Err(err).Msg("Could not generate secure random session ID, using fallback.")
	}

	log.Info().Str("peer", peerID.String()).Uint64("peer_tx_id", msg.TxID).Uint64("main_tx_id", mainState.TxId).Uint64("accounts_tx_id", accountsState.TxId).Int("agreedMainM", agreedMainM).Int("agreedMainK", agreedMainK).Int("agreedAccountsM", agreedAccountsM).Int("agreedAccountsK", agreedAccountsK).Msg("Negotiated IBLT params")

	// Create sync context
	ctx, cancel := context.WithTimeout(context.Background(), SyncTimeout)

	// Create and store sync state, including negotiated IBLT parameters
	state := &syncState{
		sessionID: sessionID,
		peer:      peerID,
		startTime: time.Now(),
		ibltMetaData: &IBLT_MetaData_Struct{
			Main_IBLT_Params:     &IBLT_Params{M: agreedMainM, K: agreedMainK},
			Accounts_IBLT_Params: &IBLT_Params{M: agreedAccountsM, K: agreedAccountsK},
			Main_DB_KeyCount:     clientMainKeyCount,
			Accounts_DB_KeyCount: clientAccountsKeyCount,
		},
		cancel: cancel,
	}

	// Store sync state
	fs.mutex.Lock()
	fs.active[peerID] = state
	fs.mutex.Unlock()

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

	// Return sync response with negotiated params and server key counts
	return &SyncMessage{
		Type:       TypeSyncResponse,
		SenderID:   fs.host.ID().String(),
		TxID:       mainState.TxId,
		MerkleRoot: MerkleRoot{MainMerkleRoot: mainState.TxHash, AccountsMerkleRoot: accountsState.TxHash},
		Data:       statesData,
		Timestamp:  time.Now().Unix(),
		IBLT_MetaData: &IBLT_MetaData_Struct{
			Main_IBLT_Params:     &IBLT_Params{M: agreedMainM, K: agreedMainK},
			Accounts_IBLT_Params: &IBLT_Params{M: agreedAccountsM, K: agreedAccountsK},
			Main_DB_KeyCount:     len(mainKeys),
			Accounts_DB_KeyCount: len(accountsKeys),
		},
	}, nil
}

// newSessionID creates a new unique session identifier.
func newSessionID(p peer.ID) (string, error) {
	// Create a new random byte slice
	randBytes := make([]byte, 16)
	if _, err := rand.Read(randBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes for session ID: %w", err)
	}
	// Combine peer ID prefix with random hex string for a unique, traceable ID
	return fmt.Sprintf("sync-%s-%x", p.String()[:8], randBytes), nil
}

// cleanupSync removes the sync state for a peer
func (fs *FastSync) cleanupSync(peerID peer.ID) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	if state, exists := fs.active[peerID]; exists {
		state.cancel() // Cancel the context to stop any ongoing operations
		delete(fs.active, peerID)
		log.Info().Str("peer", peerID.String()).Str("session", state.sessionID).Msg("Cleaned up sync state")
	}
}

func calculateBatchCount(startTxID, endTxID uint64) int {
	if startTxID >= endTxID {
		return 1 // At least one batch even if no diff
	}

	txDiff := endTxID - startTxID
	return int((txDiff + SyncBatchSize - 1) / SyncBatchSize)
}

func (fs *FastSync) MakeBAKFile_Transfer(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// Defer the cleanup of the sync state. This ensures that the state for this
	// peer is removed from the active map when this handler finishes, preventing leaks.
	// 1. Unmarshal the client's IBLT data from the message.
	// This data tells the server which keys the client has, so the server can create
	// a targeted backup containing only the missing data.
	defer fs.cleanupSync(peerID)
	var clientIBLTs TypeIBLTExchangeSYNC_Struct
	if err := json.Unmarshal(msg.Data, &clientIBLTs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal client IBLT data for backup: %w", err)
	}

	if clientIBLTs.IBLT_MAIN_SYNC == nil || clientIBLTs.IBLT_Accounts_SYNC == nil {
		return nil, fmt.Errorf("request is missing IBLT data for backup")
	}

	// 2. Ensure the temporary backup directory exists.
	if err := os.MkdirAll(BAK_FILE_PATH, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	mainBakPath := BAK_FILE_PATH + "main.bak"
	accountsBakPath := BAK_FILE_PATH + "accounts.bak"

	// 3. Use defer to ensure backup files are cleaned up even if errors occur.
	defer func() {
		if err := os.Remove(mainBakPath); err != nil && !os.IsNotExist(err) {
			log.Error().Err(err).Str("path", mainBakPath).Msg("Failed to remove temporary backup file")
		}
		if err := os.Remove(accountsBakPath); err != nil && !os.IsNotExist(err) {
			log.Error().Err(err).Str("path", accountsBakPath).Msg("Failed to remove temporary backup file")
		}
	}()

	// 4. Create targeted backups using the client's IBLTs.
	mainCfg := DB_OPs.Config{
		Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
		Username:   config.DBUsername,
		Password:   config.DBPassword,
		Database:   config.DBName,
		OutputPath: mainBakPath,
	}

	log.Info().Str("peer", peerID.String()).Str("db", "main").Msg("Creating targeted backup from IBLT")
	err := DB_OPs.BackupFromIBLT(mainCfg, clientIBLTs.IBLT_MAIN_SYNC)
	if err != nil {
		return nil, fmt.Errorf("failed to backup main database: %w", err)
	}

	accountsCfg := DB_OPs.Config{
		Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
		Username:   config.DBUsername,
		Password:   config.DBPassword,
		Database:   config.AccountsDBName,
		OutputPath: accountsBakPath,
	}

	log.Info().Str("peer", peerID.String()).Str("db", "accounts").Msg("Creating targeted backup from IBLT")
	err = DB_OPs.BackupFromIBLT(accountsCfg, clientIBLTs.IBLT_Accounts_SYNC)
	if err != nil {
		return nil, fmt.Errorf("failed to backup accounts database: %w", err)
	}

	// 5. Transfer the generated backup files to the client.
	log.Info().Str("peer", peerID.String()).Str("file", mainBakPath).Msg("Transferring backup file")
	err = TransferBAKFile(fs.host, peerID, mainBakPath)
	if err != nil {
		return nil, fmt.Errorf("failed to transfer main database: %w", err)
	}

	log.Info().Str("peer", peerID.String()).Str("file", accountsBakPath).Msg("Transferring backup file")
	err = TransferBAKFile(fs.host, peerID, accountsBakPath)
	if err != nil {
		return nil, fmt.Errorf("failed to transfer accounts database: %w", err)
	}

	// 6. Send a completion message back on the control stream.
	log.Info().Str("peer", peerID.String()).Msg("File transfers complete. Sending SyncComplete.")
	return &SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Success:   true,
	}, nil
}

// CleanupTempFiles removes any leftover .bak files from previous, potentially
// crashed, sync sessions. This should be called on application startup.
func CleanupTempFiles() {
	log.Info().Str("path", BAK_FILE_PATH).Msg("Checking for leftover temporary files...")
	files, err := filepath.Glob(filepath.Join(BAK_FILE_PATH, "*.bak"))
	if err != nil {
		log.Error().Err(err).Msg("Failed to scan for temporary backup files")
		return
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			log.Warn().Err(err).Str("file", f).Msg("Failed to remove leftover temp backup file")
		} else {
			log.Info().Str("file", f).Msg("Removed leftover temp backup file")
		}
	}
}
