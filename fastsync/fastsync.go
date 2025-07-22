package fastsync

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
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
	sessionID      string // Unique ID for logging and tracking a sync session.
	peer           peer.ID
	startTime      time.Time
	mainStartTxID  uint64
	mainEndTxID    uint64
	acctsStartTxID uint64
	acctsEndTxID   uint64
	batches        int
	completed      int
	ibltMetaData   *IBLT_MetaData_Struct // Parameters negotiated for this specific sync session
	cancel         context.CancelFunc
}

// StartSync initiates synchronization with a peer
func (fs *FastSync) StartSync(peerID peer.ID) error {
	// TODO: Implement comprehensive testing for edge cases:
	// 1. Syncing with a peer that has an empty database.
	// 2. Syncing when this node has an empty database.
	// 3. Handling network interruptions during each phase of the sync.
	// 4. Gracefully handling malformed or unexpected messages from the peer.
	// 5. Syncing with a peer that is significantly ahead or behind.
	log.Info().Str("peer", peerID.String()).Msg("Starting sync with peer")

	ctx, cancel := context.WithTimeout(context.Background(), SyncTimeout)
	defer cancel()

	stream, err := fs.host.NewStream(ctx, peerID, SyncProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

	// Phase 1: IBLT Parameter Negotiation
	log.Info().Msg("Phase 1: Negotiating IBLT parameters...")
	negotiationResp, err := fs.clientPhase1_Negotiate(stream, reader, writer)
	if err != nil {
		return fmt.Errorf("phase 1 (negotiation) failed: %w", err)
	}

	// Store negotiated params for IBLT creation
	fs.IBLT_MetaData = negotiationResp.IBLT_MetaData
	log.Info().
		Int("mainM", fs.IBLT_MetaData.Main_IBLT_Params.M).
		Int("mainK", fs.IBLT_MetaData.Main_IBLT_Params.K).
		Int("accountsM", fs.IBLT_MetaData.Accounts_IBLT_Params.M).
		Int("accountsK", fs.IBLT_MetaData.Accounts_IBLT_Params.K).
		Msg("IBLT parameters negotiated.")

	// Create client-side IBLTs with agreed-upon params
	fs.mainIBLT, err = fs.MakeIBLT_Default()
	if err != nil {
		return fmt.Errorf("failed to create main IBLT: %w", err)
	}
	fs.accountsIBLT, err = fs.MakeIBLT_Accounts()
	if err != nil {
		return fmt.Errorf("failed to create accounts IBLT: %w", err)
	}

	// Phase 2: Exchange IBLTs to get the difference
	log.Info().Msg("Phase 2: Exchanging IBLTs...")
	syncIBLTs, mainChecksum, accountsChecksum, err := fs.clientPhase2_ExchangeIBLTs(stream, writer, reader)
	if err != nil {
		return fmt.Errorf("phase 2 (IBLT exchange) failed: %w", err)
	}

	// Store the diff IBLT from the server
	fs.mainIBLT = syncIBLTs.IBLT_MAIN_SYNC
	fs.accountsIBLT = syncIBLTs.IBLT_Accounts_SYNC
	log.Info().Msg("Received sync IBLTs from peer.")

	// Phase 3: Request data transfer (e.g., via BAK file)
	log.Info().Msg("Phase 3: Requesting data transfer...")
	err = fs.clientPhase3_RequestData(peerID, stream, writer, reader, mainChecksum, accountsChecksum)
	if err != nil {
		return fmt.Errorf("phase 3 (data transfer) failed: %w", err)
	}

	log.Info().Str("peer", peerID.String()).Msg("Sync process initiated successfully. Waiting for file transfer to complete.")
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

func (fs *FastSync) clientPhase1_Negotiate(stream network.Stream, reader *bufio.Reader, writer *bufio.Writer) (*SyncMessage, error) {
	// Calculate client's key counts and optimal IBLT params
	mainKeys, _ := DB_OPs.GetAllKeys(fs.mainDB, "block:")
	accountsKeys, _ := DB_OPs.GetAllKeys(fs.accountsDB, "did:")
	mainM, mainK := calcOptimalIBLTParams(len(mainKeys))
	accountsM, accountsK := calcOptimalIBLTParams(len(accountsKeys))

	// Send sync request with our proposed IBLT params
	syncReq := &SyncMessage{
		Type:      TypeSyncRequest,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		IBLT_MetaData: &IBLT_MetaData_Struct{
			Main_IBLT_Params:     &IBLT_Params{M: mainM, K: mainK},
			Accounts_IBLT_Params: &IBLT_Params{M: accountsM, K: accountsK},
			Main_DB_KeyCount:     len(mainKeys),
			Accounts_DB_KeyCount: len(accountsKeys),
		},
	}

	if err := writeMessage(writer, stream, syncReq); err != nil {
		return nil, fmt.Errorf("failed to send sync request: %w", err)
	}

	// Read response with negotiated params
	resp, err := readMessage(reader, stream)
	if err != nil {
		return nil, fmt.Errorf("failed to read sync response: %w", err)
	}

	if resp.Type != TypeSyncResponse {
		return nil, fmt.Errorf("unexpected response type: %s, expected %s", resp.Type, TypeSyncResponse)
	}

	return resp, nil
}

func (fs *FastSync) clientPhase2_ExchangeIBLTs(stream network.Stream, writer *bufio.Writer, reader *bufio.Reader) (*TypeIBLTExchangeSYNC_Struct, string, string, error) {
	// This function creates the message for the client to send its IBLT to the server.
	clientMainIBLT, err := fs.MakeIBLT_Default()
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to create client main IBLT: %w", err)
	}
	clientAccountsIBLT, err := fs.MakeIBLT_Accounts()
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to create client accounts IBLT: %w", err)
	}

	clientIBLTData := TypeIBLTExchangeClient_Struct{
		Client_IBLT_MAIN:     clientMainIBLT,
		Client_IBLT_Accounts: clientAccountsIBLT,
	}
	data, err := json.Marshal(clientIBLTData)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to marshal client IBLT data: %w", err)
	}

	clientMsg := &SyncMessage{
		Type:      TypeIBLTExchangeClient,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Data:      data,
	}

	if err := writeMessage(writer, stream, clientMsg); err != nil {
		return nil, "", "", err
	}

	serverResponse, err := readMessage(reader, stream)
	if err != nil {
		return nil, "", "", err
	}

	if serverResponse.Type != TypeIBLTExchangeSYNC {
		return nil, "", "", fmt.Errorf("unexpected response type: got %s, want %s", serverResponse.Type, TypeIBLTExchangeSYNC)
	}

	var syncData TypeIBLTExchangeSYNC_Struct
	if err := json.Unmarshal(serverResponse.Data, &syncData); err != nil {
		return nil, "", "", err
	}

	// Verify the metadata checksum
	computeMainChecksum, err := computeCHECKSUM(syncData.IBLT_MAIN_SYNC)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to compute main IBLT checksum: %w", err)
	}

	computeAccountChecksum, err := computeCHECKSUM(syncData.IBLT_Accounts_SYNC)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to compute accounts IBLT checksum: %w", err)
	}

	if syncData.MetaData.Main_SYNC_MetaData.Checksum != computeMainChecksum || syncData.MetaData.Accounts_SYNC_MetaData.Checksum != computeAccountChecksum {
		return nil, "", "", fmt.Errorf("checksum mismatch on received IBLT")
	}

	return &syncData, computeMainChecksum, computeAccountChecksum, nil
}

func computeCHECKSUM(iblt *IBLT.IBLT) (string, error) {
	if iblt == nil {
		return "", fmt.Errorf("cannot compute checksum of a nil IBLT")
	}
	ComputerCHECKSUM := sha1.New()
	ComputerCHECKSUM.Write([]byte(iblt.String()))
	ComputerCHECKSUM_Value := ComputerCHECKSUM.Sum(nil)
	ComputerCHECKSUM_Value_String := hex.EncodeToString(ComputerCHECKSUM_Value)
	if ComputerCHECKSUM_Value_String == "" {
		return "", fmt.Errorf("failed to compute checksum for IBLT")
	}
	return ComputerCHECKSUM_Value_String, nil
}

func (fs *FastSync) clientPhase3_RequestData(peerID peer.ID, stream network.Stream, writer *bufio.Writer, reader *bufio.Reader, mainChecksum string, accountChecksum string) error {
	// phase3: receive BAK File from the server
	// -> Server will send the BAK file to the client
	// -> Client will receive the BAK file and store it in the BAK_FILE_PATH
	// -> Client will append the transactions in the BAK file

	// 1. Send the request to the server to send the BAK File. Change the msg type to RequestFiletransfer

	// The IBLTs sent here are the *diff* IBLTs received from the server in phase 2
	IBLT_data := &TypeIBLTExchangeSYNC_Struct{
		IBLT_MAIN_SYNC:     fs.mainIBLT,
		IBLT_Accounts_SYNC: fs.accountsIBLT,
		MetaData: &IBLT_MetaData{
			Main_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  mainChecksum,
			},
			Accounts_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  accountChecksum,
			},
		},
	}

	// 2. Convert the IBLT data to bytes to send in SyncMessagdata
	data, err := json.Marshal(IBLT_data)
	if err != nil {
		return fmt.Errorf("failed to marshal IBLT data: %w", err)
	}

	requestMsg := &SyncMessage{
		Type:      RequestFiletransfer,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Data:      data,
	}

	// 3. Populate the message with the data and send it
	if err := writeMessage(writer, stream, requestMsg); err != nil {
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
