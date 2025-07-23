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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
)

const (
	ALGORITHM     = "SHA128"
	BAK_FILE_PATH = "fastsync/.temp/"
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
	host          host.Host
	mainDB        *config.ImmuClient
	accountsDB    *config.ImmuClient
	mainIBLT      *IBLT.IBLT
	accountsIBLT  *IBLT.IBLT
	active        map[peer.ID]*syncState
	mutex         sync.RWMutex
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
	isAbove20            bool
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
		case TypeBatchRequest:
			response, handleErr = fs.handleBatchRequest(peerID, msg)
		case TypeBatchData:
			response, handleErr = fs.handleBatchData(peerID, msg)
		case TypeVerificationRequest:
			response, handleErr = fs.handleVerification(peerID)
		case TypeSyncComplete:
			response, handleErr = fs.handleSyncComplete(peerID, msg)
		case TypeIBLTExchangeSYNC:
			response, handleErr = fs.handleIBLTExchangeSYNC(peerID, msg) //Server will send the SYNC IBLT to the client
		case TypeIBLTExchangeClient:
			response, handleErr = fs.handleIBLTExchangeClient(peerID) //Client will send the IBLT to the server
		case RequestFiletransfer: // This will trigger for the file transfer
			response, handleErr = fs.MakeBAKFile_Transfer(peerID, msg)
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

func (fs *FastSync) handleIBLTExchangeSYNC(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	log.Info().
		Str("peer", peerID.String()).
		Msg("Received IBLT Exchange SYNC Request")

	// Project the IBLT_MetaData_Struct from msg on to the fs.mainIBLT and fs.accountsIBLT
	// Debugging print the IBLT_MetaData
	fmt.Println("msg.IBLT_MetaData.Main_IBLT_Params", msg.IBLT_MetaData.Main_IBLT_Params)
	fmt.Println("msg.IBLT_MetaData.Accounts_IBLT_Params", msg.IBLT_MetaData.Accounts_IBLT_Params)
	fmt.Println("msg.IBLT_MetaData.Main_DB_KeyCount", msg.IBLT_MetaData.Main_DB_KeyCount)
	fmt.Println("msg.IBLT_MetaData.Accounts_DB_KeyCount", msg.IBLT_MetaData.Accounts_DB_KeyCount)
	fmt.Println("msg.IBLT_MetaData.Main_DB_KeyCount", msg.IBLT_MetaData.Main_DB_KeyCount)
	fmt.Println("msg.IBLT_MetaData.Accounts_DB_KeyCount", msg.IBLT_MetaData.Accounts_DB_KeyCount)
	fs.IBLT_MetaData = msg.IBLT_MetaData

	// Get the Client IBLT
	var tempClientIBLT TypeIBLTExchangeClient_Struct
	if err := json.Unmarshal(msg.Data, &tempClientIBLT); err != nil {
		return nil, err
	}
	
	// Compute the IBLT
	ServerIBLT_MAIN, err := fs.MakeIBLT_Default()
	if err != nil {
		return nil, err
	}
	ServerIBLT_Accounts, err := fs.MakeIBLT_Accounts()
	if err != nil {
		return nil, err
	}

	computeIBLT_MAIN_SYNC, err := ServerIBLT_MAIN.Subtract(tempClientIBLT.Client_IBLT_MAIN)
	if err != nil {
		return nil, err
	}
	computeIBLT_Accounts_SYNC, err := ServerIBLT_Accounts.Subtract(tempClientIBLT.Client_IBLT_Accounts)
	if err != nil {
		return nil, err
	}

	ComputerCHECKSUM_MAIN_Value_String, err := computeCHECKSUM(computeIBLT_MAIN_SYNC)
	if err != nil {
		return nil, fmt.Errorf("failed to compute main IBLT checksum: %w", err)
	}

	ComputerCHECKSUM_Accounts_Value_String, err := computeCHECKSUM(computeIBLT_Accounts_SYNC)
	if err != nil {
		return nil, fmt.Errorf("failed to compute accounts IBLT checksum: %w", err)
	}

	IBLTExchangeClientStruct := TypeIBLTExchangeSYNC_Struct{
		IBLT_MAIN_SYNC:     computeIBLT_MAIN_SYNC,
		IBLT_Accounts_SYNC: computeIBLT_Accounts_SYNC,
		MetaData: &IBLT_MetaData{
			Main_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  ComputerCHECKSUM_MAIN_Value_String,
			},
			Accounts_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  ComputerCHECKSUM_Accounts_Value_String,
			},
		},
	}

	data, err := json.Marshal(IBLTExchangeClientStruct)
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
		Client_IBLT_MAIN:     fs.mainIBLT,
		Client_IBLT_Accounts: fs.accountsIBLT,
		MetaData: &IBLT_MetaData{
			Main_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  ComputerCHECKSUM_MAIN_Value_String,
			},
			Accounts_SYNC_MetaData: &MetaData{
				Algorithm: ALGORITHM,
				Checksum:  ComputerCHECKSUM_Accounts_Value_String,
			},
		},
	}

	data, err := json.Marshal(Client_data)
	if err != nil {
		return nil, err
	}

	//Send the IBLT to the server to get the SYNC_IBLT
	return &SyncMessage{
		Type:      TypeIBLTExchangeClient,
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

	log.Info().Str("peer", peerID.String()).Uint64("peer_tx_id", msg.TxID).Uint64("main_tx_id", mainState.TxId).Uint64("accounts_tx_id", accountsState.TxId).Int("agreedMainM", agreedMainM).Int("agreedMainK", agreedMainK).Int("agreedAccountsM", agreedAccountsM).Int("agreedAccountsK", agreedAccountsK).Msg("Negotiated IBLT params")

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

func returnStream(fs *FastSync, peerID peer.ID) (network.Stream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	stream, err := fs.host.NewStream(ctx, peerID, SyncProtocolID)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// SendIBLTNegotiationRequest is called by the client to initiate IBLT parameter negotiation with the server.
// It sends the client's key counts and optimal IBLT params, and receives the server's negotiated response.
func (fs *FastSync) SendIBLTNegotiationRequest(peerID peer.ID) (*SyncMessage, error) {
	// Step 1: Count keys in both DBs
	mainKeys, _ := DB_OPs.GetKeys(fs.mainDB, "block:", 0)
	accountsKeys, _ := DB_OPs.GetKeys(fs.accountsDB, "did:", 0)

	// Step 2: Compute optimal IBLT params for both DBs
	mainM, mainK := calcOptimalIBLTParams(len(mainKeys))
	accountsM, accountsK := calcOptimalIBLTParams(len(accountsKeys))

	// Step 3: Construct SyncMessage with IBLT_MetaData
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

	// Step 4: Open a stream to the server peer
	stream, err := returnStream(fs, peerID)
	if err != nil {
		return nil, err
	}
	defer (stream).Close()

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

	// Step 5: Send the sync request
	if err := writeMessage(writer, stream, syncReq); err != nil {
		return nil, err
	}

	// Step 6: Wait for the response
	resp, err := readMessage(reader, stream)
	if err != nil {
		return nil, err
	}

	fmt.Println("Received IBLT negotiation response:", resp)
	return resp, nil
}

func (fs *FastSync) HandleSync(peerID peer.ID) (*SyncMessage, error) {

	stream, err := returnStream(fs, peerID)
	if err != nil {
		return nil, err
	}
	defer (stream).Close()

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

	//phase1: Should send the IBlT params to the server and get to the consesus of IBLT params
	Phase1, err := fs.SendIBLTNegotiationRequest(peerID)
	if err != nil {
		return nil, err
	}

	// Print for debugging
	fmt.Println("Phase1 IBLT_MetaData:", Phase1.IBLT_MetaData)
	fmt.Println("Phase1 IBLT_MetaData Main_IBLT_Params:", Phase1.IBLT_MetaData.Main_IBLT_Params)
	fmt.Println("Phase1 IBLT_MetaData Accounts_IBLT_Params:", Phase1.IBLT_MetaData.Accounts_IBLT_Params)
	fmt.Println("Phase1 IBLT_MetaData Main_DB_KeyCount:", Phase1.IBLT_MetaData.Main_DB_KeyCount)
	fmt.Println("Phase1 IBLT_MetaData Accounts_DB_KeyCount:", Phase1.IBLT_MetaData.Accounts_DB_KeyCount)

	// Ensure fs.IBLT_MetaData and its subfields are initialized
	if fs.IBLT_MetaData == nil {
		fs.IBLT_MetaData = &IBLT_MetaData_Struct{}
	}
	if fs.IBLT_MetaData.Main_IBLT_Params == nil {
		fs.IBLT_MetaData.Main_IBLT_Params = &IBLT_Params{}
	}
	if fs.IBLT_MetaData.Accounts_IBLT_Params == nil {
		fs.IBLT_MetaData.Accounts_IBLT_Params = &IBLT_Params{}
	}

	// Check that Phase1.IBLT_MetaData and its subfields are not nil
	if Phase1.IBLT_MetaData == nil ||
		Phase1.IBLT_MetaData.Main_IBLT_Params == nil ||
		Phase1.IBLT_MetaData.Accounts_IBLT_Params == nil {
		return nil, fmt.Errorf("Phase1 response missing IBLT_MetaData or its params")
	}

	fs.IBLT_MetaData.Main_IBLT_Params.M = Phase1.IBLT_MetaData.Main_IBLT_Params.M
	fmt.Printf("Phase1 IBLT_MetaData Main_IBLT_Params M: %v \n", Phase1.IBLT_MetaData.Main_IBLT_Params.M)

	fs.IBLT_MetaData.Main_IBLT_Params.K = Phase1.IBLT_MetaData.Main_IBLT_Params.K
	fmt.Printf("Phase1 IBLT_MetaData Main_IBLT_Params K: %v \n", Phase1.IBLT_MetaData.Main_IBLT_Params.K)

	fs.IBLT_MetaData.Accounts_IBLT_Params.M = Phase1.IBLT_MetaData.Accounts_IBLT_Params.M
	fmt.Printf("Phase2 IBLT_Metadata Accounts_IBLT_Params M: %v \n", Phase1.IBLT_MetaData.Accounts_IBLT_Params.M)

	fs.IBLT_MetaData.Accounts_IBLT_Params.K = Phase1.IBLT_MetaData.Accounts_IBLT_Params.K
	fmt.Printf("Phase2 IBLT_Metadata Accounts_IBLT_Params K: %v \n", Phase1.IBLT_MetaData.Accounts_IBLT_Params.K)

	fs.mainIBLT, err = fs.MakeIBLT_Default()
	if err != nil {
		return nil, err
	}

	fs.accountsIBLT, err = fs.MakeIBLT_Accounts()
	if err != nil {
		return nil, err
	}

	//phase2: Should compute the Client IBLT and send it to the server
	// -> Client will send the IBLT to the server to get the SYNC_IBLT
	Phase2, MainChecksum, AccountChecksum, err := fs.Phase2_Sync(Phase1, peerID, stream, writer, reader)
	if err != nil {
		return nil, fmt.Errorf("failed in Phase2_Sync: %w", err)
	}

	// Check if the metadata checksums match
	// else retry to get the IBLT from the server
	if (Phase2.MetaData.Main_SYNC_MetaData.Checksum != MainChecksum ||
		Phase2.MetaData.Accounts_SYNC_MetaData.Checksum != AccountChecksum) ||
		(Phase2.MetaData.Main_SYNC_MetaData.Algorithm != ALGORITHM ||
			Phase2.MetaData.Accounts_SYNC_MetaData.Algorithm != ALGORITHM) {
		// retry 3 times to get the valid IBLT from the server
		log.Warn().
			Str("peer", peerID.String()).
			Msg("IBLT metadata checksum mismatch, retrying IBLT exchange")
		for i := 0; i < 3; i++ {
			log.Debug().
				Str("peer", peerID.String()).
				Int("attempt", i+1).
				Msg("Retrying IBLT exchange")
			Phase2, MainChecksum, AccountChecksum, err = fs.Phase2_Sync(Phase1, peerID, stream, writer, reader)
			if err == nil &&
				(Phase2.MetaData.Main_SYNC_MetaData.Checksum == MainChecksum &&
					Phase2.MetaData.Accounts_SYNC_MetaData.Checksum == AccountChecksum) &&
				(Phase2.MetaData.Main_SYNC_MetaData.Algorithm == ALGORITHM &&
					Phase2.MetaData.Accounts_SYNC_MetaData.Algorithm == ALGORITHM) {
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get valid IBLT from server: %w", err)
	}

	// Store the IBLT received from the server
	// This is the SYNC_IBLT which is the IBLT that the client will
	// use to sync the databases
	fs.mainIBLT = Phase2.IBLT_MAIN_SYNC
	fs.accountsIBLT = Phase2.IBLT_Accounts_SYNC

	// Phase3: Request the BAK file from the server
	// Server will send the BAK file to the client

	err = fs.Phase3_FileRequest(Phase1, peerID, stream, writer, reader, MainChecksum, AccountChecksum)
	if err != nil {
		// Retry initiating the file transfer again
		log.Warn().
			Str("peer", peerID.String()).
			Msg("Failed to transfer BAK file, retrying file transfer")

		for i := 0; i < 3; i++ {
			log.Debug().
				Str("peer", peerID.String()).
				Int("attempt", i+1).
				Msg("Retrying file transfer")

			err = fs.Phase3_FileRequest(Phase1, peerID, stream, writer, reader, MainChecksum, AccountChecksum)
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("failed to transfer BAK file after retries: %w", err)
		}
	}

	// Phase4: Need to implement this before implementation first test these 3 phases

	return &SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Success:   true,
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

func (fs *FastSync) MakeBAKFile_Transfer(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// 1. Unmarshal the client's IBLT data from the message.
	// This data tells the server which keys the client has, so the server can create
	// a targeted backup containing only the missing data.
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
