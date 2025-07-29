package fastsync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	hashmap "gossipnode/crdt/HashMap"
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
	BAK_FILE_PATH = "fastsync/.temp/"
)

type MerkleRoot struct {
	MainMerkleRoot     []byte `json:"main_merkle_root"`
	AccountsMerkleRoot []byte `json:"accounts_merkle_root"`
}

// Need to change from IBLT to HashMap

type SyncMessage struct {
	Type             string                      `json:"type"`
	SenderID         string                      `json:"sender_id"`
	TxID             uint64                      `json:"tx_id,omitempty"`
	StartTxID        uint64                      `json:"start_tx_id,omitempty"`
	EndTxID          uint64                      `json:"end_tx_id,omitempty"`
	BatchNumber      int                         `json:"batch_number,omitempty"`
	TotalBatches     int                         `json:"total_batches,omitempty"`
	MerkleRoot       MerkleRoot                  `json:"merkle_root,omitempty"`
	KeysCount        int                         `json:"keys_count,omitempty"`
	Data             json.RawMessage             `json:"data,omitempty"`
	Success          bool                        `json:"success,omitempty"`
	ErrorMessage     string                      `json:"error_message,omitempty"`
	Timestamp        int64                       `json:"timestamp"`
	DBType           DatabaseType                `json:"db_type,omitempty"`
	HashMap          *TypeHashMapExchange_Struct `json:"hashmap_sync,omitempty"`
	HashMap_MetaData *HashMap_MetaData           `json:"hashmap_meta_data,omitempty"`
}

type FastSync struct {
	host             host.Host
	mainDB           *config.ImmuClient
	accountsDB       *config.ImmuClient
	active           map[peer.ID]*syncState
	mutex            sync.RWMutex
	HashMap_MetaData *HashMap_MetaData
}

type HashMap_MetaData struct {
	Main_HashMap_MetaData     *MetaData
	Accounts_HashMap_MetaData *MetaData
}

type MetaData struct {
	KeysCount int
	Checksum  string
}

type TypeHashMapExchange_Struct struct {
	MAIN_HashMap     *hashmap.HashMap
	Accounts_HashMap *hashmap.HashMap
}

const (
	TypeSyncRequest           = "SYNC_REQ"
	TypeSyncResponse          = "SYNC_RESP"
	TypeHashMapExchangeClient = "HASHMAP_EXCHANGE_CLIENT"
	TypeHashMapExchangeSYNC   = "HASHMAP_EXCHANGE_SYNC"
	TypeBatchRequest          = "BATCH_REQ"
	TypeBatchData             = "BATCH_DATA"
	TypeSyncComplete          = "SYNC_COMPLETE"
	TypeSyncAbort             = "SYNC_ABORT"
	TypeVerificationRequest   = "VERIFY_REQ"
	TypeVerificationResult    = "VERIFY_RESP"
	TypeBatchAck              = "BATCH_ACK"
	TypeTransferFile          = "TRANSFER_FILE"
	RequestFiletransfer       = "REQUEST_FILE_TRANSFER"
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
	DIDs, err := DB_OPs.GetAllKeys(db, prefix)
	if err != nil {
		return nil, err
	}
	return DIDs, nil
}

func (fs *FastSync) MakeHashMap_Default() (*hashmap.HashMap, error) {
	keys, err := GetDBData_Default(fs.mainDB, "block:")
	if err != nil {
		return nil, err
	}
	MAP := hashmap.New()
	for _, key := range keys {
		MAP.Insert(key)
	}

	return MAP, nil
}

func (fs *FastSync) MakeHashMap_Accounts() (*hashmap.HashMap, error) {
	keys, err := GetDBData_Accounts(fs.accountsDB, "did:")
	if err != nil {
		return nil, err
	}
	MAP := hashmap.New()
	for _, key := range keys {
		MAP.Insert(key)
	}

	return MAP, nil
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
		case TypeBatchRequest:
			response, handleErr = fs.handleBatchRequest(peerID, msg)
		case TypeBatchData:
			response, handleErr = fs.handleBatchData(peerID, msg)
		case TypeVerificationRequest:
			response, handleErr = fs.handleVerification(peerID)
		case TypeSyncComplete:
			response, handleErr = fs.handleSyncComplete(peerID, msg)
		case TypeHashMapExchangeSYNC:
			response, handleErr = fs.handleHashMapExchangeSYNC(peerID, msg) //Server will send the SYNC HashMap to the client
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

func CheckChecksum(temp *hashmap.HashMap, checksum string) bool {
	computedChecksum := temp.Fingerprint()
	return computedChecksum == checksum
}

func (fs *FastSync) handleHashMapExchangeSYNC(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	log.Info().
		Str("peer", peerID.String()).
		Msg("Received HashMap Exchange SYNC Request")

	// First check the metadata of the client is it valid or not
	if !CheckChecksum(msg.HashMap.MAIN_HashMap, msg.HashMap_MetaData.Main_HashMap_MetaData.Checksum) {
		return nil, fmt.Errorf("invalid main HashMap checksum")
	}
	if !CheckChecksum(msg.HashMap.Accounts_HashMap, msg.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum) {
		return nil, fmt.Errorf("invalid accounts HashMap checksum")
	}

	// First Compute the hashmap of the server
	ServerHashMap_Main, err := fs.MakeHashMap_Default()
	if err != nil {
		return nil, err
	}
	ServerHashMap_Accounts, err := fs.MakeHashMap_Accounts()
	if err != nil {
		return nil, err
	}

	// Compute SYNC_HashMap -> ServerHashMap_Main - ClientHashMap_Main
	SYNC_Keys_Main := ServerHashMap_Main.Subtract(msg.HashMap.MAIN_HashMap)
	SYNC_Keys_Accounts := ServerHashMap_Accounts.Subtract(msg.HashMap.Accounts_HashMap)

	// Compute Hashmap from the keys
	SYNC_HashMap_MAIN := hashmap.New()
	for _, key := range SYNC_Keys_Main {
		SYNC_HashMap_MAIN.Insert(key)
	}

	SYNC_HashMap_Accounts := hashmap.New()
	for _, key := range SYNC_Keys_Accounts {
		SYNC_HashMap_Accounts.Insert(key)
	}

	// Debugging
	fmt.Println("SYNC_HashMap_MAIN: ", SYNC_HashMap_MAIN.Size())
	fmt.Println("SYNC_HashMap_Accounts: ", SYNC_HashMap_Accounts.Size())

	return &SyncMessage{
		Type:      TypeHashMapExchangeSYNC,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Data:      json.RawMessage([]byte(`"Message From Server"`)),
		HashMap: &TypeHashMapExchange_Struct{
			MAIN_HashMap:     SYNC_HashMap_MAIN,
			Accounts_HashMap: SYNC_HashMap_Accounts,
		},
		HashMap_MetaData: &HashMap_MetaData{
			Main_HashMap_MetaData: &MetaData{
				KeysCount: SYNC_HashMap_MAIN.Size(),
				Checksum:  SYNC_HashMap_MAIN.Fingerprint(),
			},
			Accounts_HashMap_MetaData: &MetaData{
				KeysCount: SYNC_HashMap_Accounts.Size(),
				Checksum:  SYNC_HashMap_Accounts.Fingerprint(),
			},
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

func (fs *FastSync) Phase1_SYNC(peerID peer.ID) (*SyncMessage, error) {

	// First make hashmap of Main DB
	MAIN_HashMap, err := fs.MakeHashMap_Default()
	if err != nil {
		return nil, err
	}

	// Second make hashmap of Accounts DB
	ACCOUNTS_HashMap, err := fs.MakeHashMap_Accounts()
	if err != nil {
		return nil, err
	}

	// Compute the Metadata for the both Maps

	ComputeCHECKSUM_MAIN_Value := MAIN_HashMap.Fingerprint()
	ComputeCHECKSUM_ACCOUNTS_Value := ACCOUNTS_HashMap.Fingerprint()

	MAIN_HashMap_Metadata := &MetaData{
		KeysCount: MAIN_HashMap.Size(),
		Checksum:  ComputeCHECKSUM_MAIN_Value,
	}
	ACCOUNTS_HashMap_Metadata := &MetaData{
		KeysCount: ACCOUNTS_HashMap.Size(),
		Checksum:  ComputeCHECKSUM_ACCOUNTS_Value,
	}

	return &SyncMessage{
		Type:      TypeSyncRequest,
		SenderID:  fs.host.ID().String(),
		TxID:      0,
		Timestamp: time.Now().Unix(),
		Data:      json.RawMessage([]byte(`"Message From Client"`)),
		HashMap: &TypeHashMapExchange_Struct{
			MAIN_HashMap:     MAIN_HashMap,
			Accounts_HashMap: ACCOUNTS_HashMap,
		},
		HashMap_MetaData: &HashMap_MetaData{
			Main_HashMap_MetaData:     MAIN_HashMap_Metadata,
			Accounts_HashMap_MetaData: ACCOUNTS_HashMap_Metadata,
		},
	}, nil
}

func (fs *FastSync) HandleSync(peerID peer.ID) (*SyncMessage, error) {

	stream, err := returnStream(fs, peerID)
	if err != nil {
		return nil, err
	}
	defer (stream).Close()

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

	//phase1: Should make the HashMap of accounts and main DB
	Phase1, err := fs.Phase1_SYNC(peerID)
	if err != nil {
		return nil, fmt.Errorf("failed in Phase1_SYNC: %w", err)
	}

	//phase2: Should compute the Client IBLT and send it to the server
	// -> Client will send the IBLT to the server to get the SYNC_IBLT
	Phase2, MainChecksum, AccountChecksum, err := fs.Phase2_Sync(Phase1, peerID, stream, writer, reader)
	if err != nil {
		return nil, fmt.Errorf("failed in Phase2_Sync: %w", err)
	}

	// Check if the metadata checksums match
	// else retry to get the HashMap from the server
	if Phase2.HashMap.MAIN_HashMap.Fingerprint() != MainChecksum ||
		Phase2.HashMap.Accounts_HashMap.Fingerprint() != AccountChecksum {
		// retry 3 times to get the valid HashMap from the server
		log.Warn().
			Str("peer", peerID.String()).
			Msg("HashMap metadata checksum mismatch, retrying HashMap exchange")
		for i := range 3 {
			log.Debug().
				Str("peer", peerID.String()).
				Int("attempt", i+1).
				Msg("Retrying HashMap exchange")
			Phase2, MainChecksum, AccountChecksum, err = fs.Phase2_Sync(Phase1, peerID, stream, writer, reader)
			if err == nil &&
				(Phase2.HashMap_MetaData.Main_HashMap_MetaData.Checksum == MainChecksum &&
					Phase2.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum == AccountChecksum) {
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get valid HashMap from server: %w", err)
	}

	// Debugging
	fmt.Println("HashMap metadata checksums match")
	fmt.Println("Main HashMap size: ", Phase2.HashMap.MAIN_HashMap.Size())
	fmt.Println("Accounts HashMap size: ", Phase2.HashMap.Accounts_HashMap.Size())
	fmt.Println("Main HashMap metadata: ", Phase2.HashMap_MetaData.Main_HashMap_MetaData)
	fmt.Println("Accounts HashMap metadata: ", Phase2.HashMap_MetaData.Accounts_HashMap_MetaData)

	// Phase3: Request the BAK file from the server
	// Server will send the BAK file to the client

	// Debugging
	if Phase2.HashMap.Accounts_HashMap.Size() < 10 {
		fmt.Println("Less than 10 keys found in Accounts HashMap", Phase2.HashMap.Accounts_HashMap)
	} else {
		fmt.Println("More than 10 keys found in Accounts HashMap", Phase2.HashMap.Accounts_HashMap.Keys()[0:10])
	}

	err = fs.Phase3_FileRequest(Phase2, peerID, stream, writer, reader)
	if err != nil {
		// Retry initiating the file transfer again
		log.Warn().
			Str("peer", peerID.String()).
			Msg("Failed to transfer BAK file, retrying file transfer")

		for i := range 3 {
			log.Debug().
				Str("peer", peerID.String()).
				Int("attempt", i+1).
				Msg("Retrying file transfer")

			err = fs.Phase3_FileRequest(Phase1, peerID, stream, writer, reader)
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("failed to transfer BAK file after retries: %w", err)
		}
	}

	// Phase4: Push the Transactions from BAK file to the DB

	// 1. Push the Main DB Transactions from BAK file to the DB - if no maindb then skip
	if Phase2.HashMap.MAIN_HashMap != nil && Phase2.HashMap.MAIN_HashMap.Size() > 0 {
		if err := fs.PushDataToDB(Phase2, MainDB, "fastsync/.temp/main.bak"); err != nil {
			log.Error().Err(err).Msg("Failed to push Main DB transactions")
			return nil, fmt.Errorf("failed to push Main DB transactions: %w", err)
		}
		log.Info().Msg("Successfully pushed Main DB transactions")
	} else {
		log.Info().Msg("Skipping Main DB push - no data to push")
	}

	// 2. Push the Accounts DB Transactions from BAK file to the DB - if no accountsdb then skip
	if Phase2.HashMap.Accounts_HashMap != nil && Phase2.HashMap.Accounts_HashMap.Size() > 0 {
		if err := fs.PushDataToDB(Phase2, AccountsDB, "fastsync/.temp/accounts.bak"); err != nil {
			log.Error().Err(err).Msg("Failed to push Accounts DB transactions")
			return nil, fmt.Errorf("failed to push Accounts DB transactions: %w", err)
		}
		log.Info().Msg("Successfully pushed Accounts DB transactions")
	} else {
		log.Info().Msg("Skipping Accounts DB push - no data to push")
	}

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
	// 1. Check if the message contains valid HashMap data
	if msg.HashMap == nil {
		return nil, fmt.Errorf("message is missing HashMap data")
	}

	if msg.HashMap_MetaData == nil || msg.HashMap_MetaData.Main_HashMap_MetaData == nil || msg.HashMap_MetaData.Accounts_HashMap_MetaData == nil {
		return nil, fmt.Errorf("message is missing HashMap metadata")
	}

	// 2. Ensure the temporary backup directory exists.
	if err := os.MkdirAll(BAK_FILE_PATH, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	mainBakPath := BAK_FILE_PATH + "main.bak"
	accountsBakPath := BAK_FILE_PATH + "accounts.bak"

	// 2. Use defer to ensure backup files are cleaned up even if errors occur.
	defer func() {
		if err := os.Remove(mainBakPath); err != nil && !os.IsNotExist(err) {
			log.Error().Err(err).Str("path", mainBakPath).Msg("Failed to remove temporary backup file")
		}
		if err := os.Remove(accountsBakPath); err != nil && !os.IsNotExist(err) {
			log.Error().Err(err).Str("path", accountsBakPath).Msg("Failed to remove temporary backup file")
		}
	}()

	// 3. Create targeted backups using the client's HashMap.
	mainCfg := DB_OPs.Config{
		Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
		Username:   config.DBUsername,
		Password:   config.DBPassword,
		Database:   config.DBName,
		OutputPath: mainBakPath,
	}

	log.Info().
		Str("peer", peerID.String()).
		Int("keys", msg.HashMap.MAIN_HashMap.Size()).
		Msg("Creating targeted backup from MAIN HashMap")

	err := DB_OPs.BackupFromHashMap(mainCfg, msg.HashMap.MAIN_HashMap)
	if err != nil {
		return nil, fmt.Errorf("failed to backup main database: %w", err)
	}

	// Transfer the main DB backup file
	log.Info().Str("peer", peerID.String()).Str("file", mainBakPath).Msg("Transferring main DB backup file")
	err = TransferBAKFile(fs.host, peerID, mainBakPath, "fastsync/.temp/main.bak")
	if err != nil {
		return nil, fmt.Errorf("failed to transfer main database: %w", err)
	}

	// Process accounts DB if it has entries
	if msg.HashMap.Accounts_HashMap != nil && msg.HashMap.Accounts_HashMap.Size() > 0 {
		accountsCfg := DB_OPs.Config{
			Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
			Username:   config.DBUsername,
			Password:   config.DBPassword,
			Database:   config.AccountsDBName,
			OutputPath: accountsBakPath,
		}

		log.Info().
			Str("peer", peerID.String()).
			Int("keys", msg.HashMap.Accounts_HashMap.Size()).
			Msg("Creating targeted backup from Accounts HashMap")

		err := DB_OPs.BackupFromHashMap(accountsCfg, msg.HashMap.Accounts_HashMap)
		if err != nil {
			return nil, fmt.Errorf("failed to backup accounts database: %w", err)
		}

		// Transfer the accounts DB backup file
		log.Info().Str("peer", peerID.String()).Str("file", accountsBakPath).Msg("Transferring accounts DB backup file")
		err = TransferBAKFile(fs.host, peerID, accountsBakPath, "fastsync/.temp/accounts.bak")
		if err != nil {
			return nil, fmt.Errorf("failed to transfer accounts database: %w", err)
		}
	}

	// 6. Send a completion message back on the control stream.
	// Debugging
	fmt.Println("File transfers complete. Sending SyncComplete.")
	log.Info().Str("peer", peerID.String()).Msg("File transfers complete. Sending SyncComplete.")
	return &SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().Unix(),
		Success:   true,
	}, nil
}
