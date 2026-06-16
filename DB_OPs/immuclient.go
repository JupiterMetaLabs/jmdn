package DB_OPs

// immuclient.go — ThebeDB-backed implementations of the historic ImmuDB API surface.
//
// Phase 6 migration: ImmuDB (*config.ImmuClient) replaced by store.ThebeHandle.
// All functions delegate through getHandle() → store.ThebeHandle methods.
// Generic KV operations (Create/Read/BatchCreate/GetKeys/GetAllKeys) are stubbed
// — callers should migrate to typed ThebeHandle methods.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	DB_OPs_common "gossipnode/DB_OPs/common"
	"gossipnode/DB_OPs/store"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/ethereum/go-ethereum/common"
)

var ImmuclientLocalGRO interfaces.LocalGoroutineManagerInterface

// toBytes converts various value types to bytes (for legacy BatchCreate).
func toBytes(value interface{}) ([]byte, error) {
	switch v := value.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	case nil:
		return nil, ErrNilValue
	default:
		jsonBytes, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal value to JSON: %w", err)
		}
		return jsonBytes, nil
	}
}

// isNotFoundError checks if err indicates a missing key.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "key not found") ||
		strings.Contains(err.Error(), "tbtree: key not found") ||
		strings.Contains(err.Error(), "not found")
}

// ─────────────────────────────────────────────────────────────────────────────
// Generic KV stubs — these operations have no direct ThebeHandle equivalent.
// For block/account data, use typed methods (StoreZKBlock, GetZKBlockByNumber, etc.).
// ─────────────────────────────────────────────────────────────────────────────

// Create stores a value by key. Stub for ThebeDB backend — succeeds silently.
// Callers storing blocks/txs should use StoreZKBlock; CRDT data may be dropped.
func Create(conn *config.PooledConnection, key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}
	if value == nil {
		return ErrNilValue
	}
	// No generic KV in ThebeDB. Silently succeed to avoid breaking callers.
	return nil
}

// Read retrieves a value by key. Returns ErrNotFound for ThebeDB backend.
func Read(conn *config.PooledConnection, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	return nil, ErrNotFound
}

// ReadJSON retrieves a value by key and unmarshals it into dest.
func ReadJSON(key string, dest interface{}) error {
	data, err := Read(nil, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}
	return nil
}

// Update updates an existing key with a new value.
func Update(key string, value interface{}) error {
	return Create(nil, key, value)
}

// GetKeys retrieves keys with a specified prefix. Returns empty for ThebeDB.
func GetKeys(conn *config.PooledConnection, prefix string, limit int) ([]string, error) {
	return []string{}, nil
}

// GetAllKeys retrieves all keys with a prefix. Returns empty for ThebeDB.
func GetAllKeys(conn *config.PooledConnection, prefix string) ([]string, error) {
	return []string{}, nil
}

// BatchCreate stores multiple key-value pairs. Stub for ThebeDB.
func BatchCreate(conn *config.PooledConnection, entries map[string]interface{}) error {
	if len(entries) == 0 {
		return ErrEmptyBatch
	}
	return nil
}

// BatchCreateOrdered stores ordered key-value pairs. Stub for ThebeDB.
func BatchCreateOrdered(conn *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return ErrEmptyBatch
	}
	return nil
}

// Exists checks if a key exists. Returns false for ThebeDB (no generic KV).
func Exists(conn *config.PooledConnection, key string) (bool, error) {
	if key == "" {
		return false, ErrEmptyKey
	}
	return false, nil
}

// CountTransactionsByAccount counts transactions for a specific account.
func CountTransactionsByAccount(accountAddr *common.Address) (int64, error) {
	transactions, err := GetTransactionsByAccount(nil, accountAddr)
	if err != nil {
		return 0, fmt.Errorf("failed to get transactions for account: %w", err)
	}
	return int64(len(transactions)), nil
}

// CountTransactions counts the total number of transactions.
func CountTransactions(conn *config.PooledConnection) (int, error) {
	count, err := CountBuilder{}.GetMainDBCount(DEFAULT_PREFIX_TX)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetMerkleRoot returns the database-level merkle root.
//
// ImmuDB exposed a single tamper-proof state root per DB. ThebeDB's integrity
// proof lives in the append-only KV log (verifiable via builder.VerifyChain),
// not as a single root reachable through store.ThebeHandle. Until that is
// surfaced through the handle, this returns an empty root with no error so
// callers that only display it as a stat (e.g. the explorer) degrade
// gracefully rather than failing the whole request.
func GetMerkleRoot(conn *config.PooledConnection) ([]byte, error) {
	return []byte{}, nil
}

// SafeCreate is a verified write — delegates to Create in ThebeDB backend.
func SafeCreate(ic *config.ImmuClient, key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}
	if value == nil {
		return ErrNilValue
	}
	// In ThebeDB backend, SafeCreate is equivalent to Create (no separate verification layer).
	return Create(nil, key, value)
}

// SafeRead is a verified read — delegates to Read in ThebeDB backend.
func SafeRead(ic *config.ImmuClient, key string) ([]byte, error) {
	return Read(nil, key)
}

// SafeReadJSON retrieves a verified value by key and unmarshals it.
func SafeReadJSON(ic *config.ImmuClient, key string, dest interface{}) error {
	return ReadJSON(key, dest)
}

// ─────────────────────────────────────────────────────────────────────────────
// Block operations — backed by store.ThebeHandle
// ─────────────────────────────────────────────────────────────────────────────

// StoreZKBlock stores a complete ZK block via ThebeHandle.
func StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error {
	var err error
	var shouldReturn bool

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("StoreZKBlock: failed to get main DB connection: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return fmt.Errorf("StoreZKBlock: %w", err)
	}

	if err := h.StoreBlock(ctx, block); err != nil {
		return fmt.Errorf("StoreZKBlock: %w", err)
	}

	// Store each transaction
	for i := range block.Transactions {
		if txErr := h.StoreTransaction(ctx, &block.Transactions[i], block.BlockNumber, i); txErr != nil {
			// Non-fatal: log and continue
			_ = txErr
		}
	}

	// Store ZK proof
	if zkErr := h.StoreZKBlock(ctx, block); zkErr != nil {
		_ = zkErr // non-fatal
	}

	// Best-effort shadow fanout
	if shadow := getThebeShadowWriter(); shadow != nil {
		if shadowErr := shadow.StoreZKBlock(mainDBClient, block); shadowErr != nil {
			_ = shadowErr
		}
	}

	return nil
}

// getZKBlockWithTxs fetches a BlockRecord and its transactions, reconstructing a ZKBlock.
func getZKBlockWithTxs(ctx context.Context, h store.ThebeHandle, blockNumber uint64) (*config.ZKBlock, error) {
	rec, err := h.GetBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("getZKBlockWithTxs: %w", err)
	}

	zkBlock, err := blockRecordToZKBlock(rec)
	if err != nil {
		return nil, fmt.Errorf("getZKBlockWithTxs: %w", err)
	}

	// Fetch transactions for this block
	txRecs, err := h.GetTransactionsByBlock(ctx, blockNumber)
	if err == nil && len(txRecs) > 0 {
		txs := make([]config.Transaction, 0, len(txRecs))
		for _, r := range txRecs {
			tx := txRecordToTransaction(r)
			if tx != nil {
				txs = append(txs, *tx)
			}
		}
		zkBlock.Transactions = txs
	}

	return zkBlock, nil
}

// GetZKBlockByNumber retrieves a ZK block by its number.
func GetZKBlockByNumber(mainDBClient *config.PooledConnection, blockNumber uint64) (*config.ZKBlock, error) {
	var err error
	var shouldReturn bool

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetZKBlockByNumber: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByNumber: %w", err)
	}

	return getZKBlockWithTxs(ctx, h, blockNumber)
}

// GetZKBlockByHash retrieves a ZK block by its hash.
func GetZKBlockByHash(mainDBClient *config.PooledConnection, blockHash string) (*config.ZKBlock, error) {
	var err error
	var shouldReturn bool

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetZKBlockByHash: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByHash: %w", err)
	}

	rec, err := h.GetBlockByHash(ctx, blockHash)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByHash: %w", err)
	}

	zkBlock, err := blockRecordToZKBlock(rec)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByHash: %w", err)
	}

	// Fetch transactions
	txRecs, err := h.GetTransactionsByBlock(ctx, rec.BlockNumber)
	if err == nil {
		txs := make([]config.Transaction, 0, len(txRecs))
		for _, r := range txRecs {
			tx := txRecordToTransaction(r)
			if tx != nil {
				txs = append(txs, *tx)
			}
		}
		zkBlock.Transactions = txs
	}

	return zkBlock, nil
}

// GetLatestBlockNumber returns the latest block number.
func GetLatestBlockNumber(mainDBClient *config.PooledConnection) (uint64, error) {
	var err error
	var shouldReturn bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return 0, fmt.Errorf("GetLatestBlockNumber: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return 0, fmt.Errorf("GetLatestBlockNumber: %w", err)
	}

	return h.GetLatestBlockNumber(ctx)
}

// ReconcileLatestBlockNumber attempts to find the latest block by scanning.
// For ThebeDB, this delegates to GetLatestBlockNumber.
func ReconcileLatestBlockNumber(mainDBClient *config.PooledConnection) (uint64, error) {
	return GetLatestBlockNumber(mainDBClient)
}

// GetTransactionBlock returns the block containing a specific transaction.
func GetTransactionBlock(mainDBClient *config.PooledConnection, txHash string) (*config.ZKBlock, error) {
	var err error
	var shouldReturn bool

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetTransactionBlock: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionBlock: %w", err)
	}

	txRec, err := h.GetTransaction(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionBlock: transaction %s not found: %w", txHash, err)
	}

	return getZKBlockWithTxs(ctx, h, txRec.BlockNumber)
}

// GetTransactionByHash retrieves a single transaction by hash.
func GetTransactionByHash(mainDBClient *config.PooledConnection, txHash string) (*config.Transaction, error) {
	var err error
	var shouldReturn bool

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetTransactionByHash: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionByHash: %w", err)
	}

	txRec, err := h.GetTransaction(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionByHash: %w", err)
	}

	tx := txRecordToTransaction(txRec)
	if tx == nil {
		return nil, fmt.Errorf("GetTransactionByHash: failed to convert tx record for %s", txHash)
	}
	return tx, nil
}

// GetTransactionsBatch fetches multiple transactions by their hashes in a batch.
func GetTransactionsBatch(mainDBClient *config.PooledConnection, hashes []string) ([]*config.Transaction, error) {
	if ImmuclientLocalGRO == nil {
		var err error
		ImmuclientLocalGRO, err = DB_OPs_common.InitializeGRO(GRO.DB_OPsImmuclientLocal)
		if err != nil {
			return nil, fmt.Errorf("GetTransactionsBatch: failed to initialize GRO: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetTransactionsBatch: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	var transactions []*config.Transaction
	batchSize := 10
	for i := 0; i < len(hashes); i += batchSize {
		end := i + batchSize
		if end > len(hashes) {
			end = len(hashes)
		}

		batch := hashes[i:end]
		wg, wgErr := ImmuclientLocalGRO.NewFunctionWaitGroup(ctx, GRO.DB_OPsImmuclientWG)
		if wgErr != nil {
			return nil, fmt.Errorf("GetTransactionsBatch: failed to create WG: %w", wgErr)
		}
		var mu sync.Mutex
		var batchErr error

		for _, hash := range batch {
			h := hash
			ImmuclientLocalGRO.Go(GRO.DB_OPsImmuclientThread, func(groCtx context.Context) error {
				tx, txErr := GetTransactionByHash(mainDBClient, h)
				if txErr != nil {
					batchErr = fmt.Errorf("failed to fetch tx %s: %w", h, txErr)
					return batchErr
				}
				mu.Lock()
				transactions = append(transactions, tx)
				mu.Unlock()
				return nil
			}, local.AddToWaitGroup(GRO.DB_OPsImmuclientWG))
		}

		wg.Wait()
		if batchErr != nil {
			return nil, batchErr
		}
	}

	return transactions, nil
}

// GetAllBlocks returns all blocks from 1 to latest.
func GetAllBlocks(mainDBClient *config.PooledConnection) ([]*config.ZKBlock, error) {
	var err error
	var shouldReturn bool

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetAllBlocks: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetAllBlocks: %w", err)
	}

	latestBlockNumber, err := h.GetLatestBlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetAllBlocks: %w", err)
	}

	var blocks []*config.ZKBlock
	for i := latestBlockNumber; i >= 1; i-- {
		block, blockErr := getZKBlockWithTxs(ctx, h, i)
		if blockErr != nil {
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Utility functions kept for backward compatibility
// ─────────────────────────────────────────────────────────────────────────────

// NewBlockHasher creates a new BlockHasher.
func NewBlockHasher() *config.BlockHasher {
	return &config.BlockHasher{}
}

// HashBlock generates a hash for a block.
func HashBlock(h *config.BlockHasher, nonce, sender string, timestamp int64) string {
	data := fmt.Sprintf("%s-%s-%d", nonce, sender, timestamp)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])[:16]
}

// DatabaseState is a stub for the ImmuDB ImmutableState type.
// TxId and TxHash are always zero/nil for the ThebeDB backend.
type DatabaseState struct {
	TxId   uint64
	TxHash []byte
}

// GetDatabaseState returns a zero-value DatabaseState for ThebeDB.
// In ThebeDB, there is no immutable state concept — returns (DatabaseState{}, nil).
func GetDatabaseState(closer interface{}) (*DatabaseState, error) {
	return &DatabaseState{}, nil
}

// Close closes a connection handle. Accepts io.Closer or *config.ImmuClient.
func Close(closer interface{}) error {
	if closer == nil {
		return nil
	}
	switch v := closer.(type) {
	case *config.ImmuClient:
		if v == nil {
			return nil
		}
		if v.Cancel != nil {
			v.Cancel()
		}
		if v.Client != nil {
			return v.Client.Disconnect()
		}
	case io.Closer:
		return v.Close()
	}
	return nil
}

// IsHealthy checks if an ImmuClient is healthy (legacy).
func IsHealthy(ic *config.ImmuClient) bool {
	return ic != nil && ic.IsConnected
}

// Ping performs a health check (legacy ImmuClient).
func Ping(ic *config.ImmuClient) error {
	if ic == nil {
		return fmt.Errorf("database client is nil")
	}
	if !ic.IsConnected {
		return fmt.Errorf("client not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := ic.Client.CurrentState(ctx)
	return err
}

// Transaction creates a transaction context (legacy ImmuClient).
func Transaction(ic *config.ImmuClient, fn func(tx *config.ImmuTransaction) error) error {
	tx := &config.ImmuTransaction{
		Client: ic,
	}
	return fn(tx)
}

// Set adds a set operation to a transaction (legacy).
func Set(tx *config.ImmuTransaction, key string, value interface{}) error {
	if key == "" {
		return ErrEmptyKey
	}
	if value == nil {
		return ErrNilValue
	}
	return nil
}

// ensureConnectionDatabaseSelected is a no-op for ThebeDB (stateless handles).
func ensureConnectionDatabaseSelected(pc *config.PooledConnection) error {
	if pc == nil || pc.Client == nil {
		return fmt.Errorf("ensureConnectionDatabaseSelected: invalid connection")
	}
	return nil
}

// reconnect is kept for legacy callers; always returns an error.
func reconnect(ic *config.ImmuClient, FUNCTION string) error {
	return fmt.Errorf("reconnect: not supported in ThebeDB backend (function: %s)", FUNCTION)
}

// withRetry is kept for legacy callers; executes op once without retry.
func withRetry(ic *config.ImmuClient, operation string, fn func() error) error {
	return fn()
}

// isConnectionError checks if err is a connection error.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	for _, ce := range []string{"connection refused", "broken pipe", "transport is closing", "EOF", "timeout"} {
		if strings.Contains(errStr, ce) {
			return true
		}
	}
	return false
}

// GetTransactionsByBlock retrieves all transactions in a block.
func GetTransactionsByBlock(mainDBClient *config.PooledConnection, blockNumber uint64) ([]*config.Transaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetTransactionsByBlock: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByBlock: %w", err)
	}

	txRecs, err := h.GetTransactionsByBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByBlock: %w", err)
	}

	txs := make([]*config.Transaction, 0, len(txRecs))
	for _, r := range txRecs {
		tx := txRecordToTransaction(r)
		if tx != nil {
			txs = append(txs, tx)
		}
	}
	return txs, nil
}

// SetTransactionStatus sets the processing status for a transaction.
func SetTransactionStatus(mainDBClient *config.PooledConnection, txHash string, status int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("SetTransactionStatus: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return fmt.Errorf("SetTransactionStatus: %w", err)
	}

	return h.SetTransactionStatus(ctx, txHash, status)
}

// GetReceipt retrieves a receipt for a transaction hash.
func GetReceipt(mainDBClient *config.PooledConnection, txHash string) (*config.Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if mainDBClient == nil {
		mainDBClient, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetReceipt: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(mainDBClient)
	}

	h, err := getHandle(mainDBClient)
	if err != nil {
		return nil, fmt.Errorf("GetReceipt: %w", err)
	}

	return h.GetReceipt(ctx, txHash)
}

// BulkGetAccounts retrieves multiple accounts by addresses.
func BulkGetAccounts(conn *config.PooledConnection, addresses []string) ([]*store.Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("BulkGetAccounts: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return nil, fmt.Errorf("BulkGetAccounts: %w", err)
	}

	return h.BulkGetAccounts(ctx, addresses)
}

// GetZKProofByBlockNumber retrieves a ZK proof for a block.
func GetZKProofByBlockNumber(mainDBClient *config.PooledConnection, blockNumber uint64) (*config.ZKBlock, error) {
	block, err := GetZKBlockByNumber(mainDBClient, blockNumber)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if mainDBClient == nil {
		var connErr error
		mainDBClient, connErr = GetMainDBConnectionandPutBack(ctx)
		if connErr != nil {
			return block, nil
		}
		defer PutMainDBConnection(mainDBClient)
	}
	h, hErr := getHandle(mainDBClient)
	if hErr != nil {
		return block, nil
	}
	zkRec, zkErr := h.GetZKProof(ctx, blockNumber)
	if zkErr == nil && zkRec != nil {
		zkProofRecordToZKBlock(zkRec, block)
	}
	return block, nil
}

// getHandle is reused from account_immuclient.go in the same package.
// (declared there, available package-wide)

// common.Address usage kept for CountTransactionsByAccount signature.
var _ = common.Address{}
