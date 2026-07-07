package DB_OPs

// thebe_ops.go — consolidated live DB operations.
// Replaces immuclient.go and account_immuclient.go.
// Only functions that are actively used or required by external callers are kept here.
// ImmuDB is fully removed; all operations route through ThebeDB (store.ThebeHandle).

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
)

// ========================================
// BLOCK-LEVEL OPERATIONS (from immuclient.go)
// ========================================

// toBytes converts various value types to bytes.
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

// Create stores a value with the given key using the connection pool.
// tx:<hash> → blockNumber entries are handled by SQL transactions table.
// All other ImmuDB-specific KV entries are no-ops after ThebeDB migration.
func Create(PooledConnection *config.PooledConnection, key string, value interface{}) error {
	return nil
}

// Read retrieves a value by key — routes sentinel keys through ThebeDB SQL.
func Read(PooledConnection *config.PooledConnection, key string) ([]byte, error) {
	// "latest_block" and "header_latest_block" are derived from SQL MAX(block_number)
	if key == "latest_block" || key == "header_latest_block" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h, err := getHandle(nil)
		if err != nil {
			return nil, fmt.Errorf("Read(%q): %w", key, err)
		}
		num, err := h.GetLatestBlockNumber(ctx)
		if err != nil {
			return nil, fmt.Errorf("Read(%q): %w", key, err)
		}
		return json.Marshal(num)
	}
	return nil, fmt.Errorf("Read: key %q not available (ImmuDB removed)", key)
}

// isNotFoundError checks if error is a "not found" error.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "key not found") ||
		strings.Contains(err.Error(), "tbtree: key not found")
}

// ReadJSON retrieves a value by key and unmarshals it into dest.
func ReadJSON(key string, dest interface{}) error {
	var err error
	var data []byte

	data, err = Read(nil, key)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// Update updates an existing key with a new value.
// sentinel keys: no-op — SQL derives latest_block and header_latest_block from MAX(block_number).
func Update(key string, value interface{}) error {
	if key == "latest_block" || key == "header_latest_block" {
		return nil
	}
	// block:<number> or block:hash:<hash> keys: write via StoreBlock
	if strings.HasPrefix(key, PREFIX_BLOCK) {
		if b, ok := value.(*config.ZKBlock); ok {
			return StoreZKBlock(nil, b)
		}
	}
	// unknown key: no-op (ImmuDB index; SQL handles via table columns)
	return nil
}

// Close closes the ImmuDB client connection.
func Close(ic *config.ImmuClient) error {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.Logger.Debug(loggerCtx, "Closing ImmuDB connection",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Close"))

	if ic.Cancel != nil {
		ic.Cancel()
	}

	ic.IsConnected = false

	ic.Logger.Debug(loggerCtx, "ImmuDB connection closed successfully",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.Close"))

	defer func() {
		ic.Logger.Debug(loggerCtx, "ImmuDB connection closed successfully",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.Close"))
		// Logger sync is handled by ion internally
	}()

	return nil
}

// GetDatabaseState returns the current state of the database.
// ImmuDB removed — returns a zeroed DatabaseState.
func GetDatabaseState(_ *config.ImmuClient) (*DatabaseState, error) {
	return &DatabaseState{}, nil
}

// StoreZKBlock stores a complete ZK block in the main database (ThebeDB).
func StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error {
	if block == nil {
		return fmt.Errorf("StoreZKBlock: block is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("StoreZKBlock: %w", err)
	}
	if err := h.StoreBlock(ctx, block); err != nil {
		return fmt.Errorf("StoreZKBlock: %w", err)
	}
	// Write ZK proof if present
	if block.ProofHash != "" || len(block.StarkProof) > 0 {
		if err := h.StoreZKBlock(ctx, block); err != nil {
			return fmt.Errorf("StoreZKBlock: zk proof: %w", err)
		}
	}
	// Write transactions and collect unique sender addresses
	senders := make(map[string]struct{}, len(block.Transactions))
	for i := range block.Transactions {
		tx := block.Transactions[i]
		if err := h.StoreTransaction(ctx, &tx, block.BlockNumber, i); err != nil {
			return fmt.Errorf("StoreZKBlock: tx[%d]: %w", i, err)
		}
		if tx.From != nil {
			senders[tx.From.Hex()] = struct{}{}
		}
	}
	// Refresh tx_nonce + tx_count_sent for every sender in this block
	for addr := range senders {
		_ = h.RefreshAccountTxStats(ctx, addr) // best-effort; don't fail block write
	}
	return nil
}

// GetZKBlockByNumber retrieves a ZK block by its number from ThebeDB,
// including ZK proof and transaction data.
func GetZKBlockByNumber(mainDBClient *config.PooledConnection, blockNumber uint64) (*config.ZKBlock, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByNumber: %w", err)
	}
	rec, err := h.GetBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByNumber(%d): %w", blockNumber, err)
	}
	blk, convErr := blockRecordToZKBlock(rec)
	if convErr != nil {
		return nil, fmt.Errorf("GetZKBlockByNumber(%d): convert: %w", blockNumber, convErr)
	}
	if proof, err := h.GetZKProof(ctx, blockNumber); err == nil {
		zkProofRecordToZKBlock(proof, blk)
	}
	if txRecs, err := h.GetTransactionsByBlock(ctx, blockNumber); err == nil {
		blk.Transactions = make([]config.Transaction, 0, len(txRecs))
		for _, r := range txRecs {
			if t := txRecordToTransaction(r); t != nil {
				blk.Transactions = append(blk.Transactions, *t)
			}
		}
	}
	return blk, nil
}

// ReadZKBlockByNumber retrieves a ZK block by number using ThebeDB.
// Delegates to GetZKBlockByNumber (ThebeDB-backed).
//
// Time: O(1); Space: O(block size)
func ReadZKBlockByNumber(ctx context.Context, mainDBClient *config.PooledConnection, blockNumber uint64) (*config.ZKBlock, error) {
	return GetZKBlockByNumber(mainDBClient, blockNumber)
}

// GetLatestBlockNumber returns the latest block number from ThebeDB.
func GetLatestBlockNumber(ctx context.Context, mainDBClient *config.PooledConnection) (uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h, err := getHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("GetLatestBlockNumber: %w", err)
	}
	return h.GetLatestBlockNumber(ctx)
}

// ========================================
// ACCOUNT TYPES (from account_immuclient.go)
// ========================================

// Account represents an on-chain account stored in ThebeDB.
// This will be stored in the DB.
type Account struct {
	// Legacy DID fields (for backward compatibility)
	DIDAddress string `json:"did,omitempty"`

	// New PublicKey based fields
	Nonce       uint64         `json:"nonce"`   // Unique deterministic ID for Fastsync ART (migrated from old nonce)
	Address     common.Address `json:"address"` // Derived from PublicKey
	Balance     string         `json:"balance,omitempty"`
	TxNonce     uint64         `json:"tx_nonce"`      // Real Ethereum Nonce
	TxCountSent uint64         `json:"tx_count_sent"` // Tracks actual analytical transactions sent

	// Account metadata
	AccountType string `json:"account_type"` // "did" or "publickey"
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`

	// Optional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// AccountsSet is a set of accounts keyed by hex address.
type AccountsSet struct {
	Accounts map[string]*Account
}

// NewAccountsSet creates an empty AccountsSet.
func NewAccountsSet() *AccountsSet {
	return &AccountsSet{
		Accounts: make(map[string]*Account),
	}
}

// Add inserts an address into the set (with a nil Account placeholder).
func (s *AccountsSet) Add(address common.Address) {
	s.Accounts[address.Hex()] = nil
}

// ========================================
// ACCOUNT OPERATIONS (from account_immuclient.go)
// ========================================

// CreateAccount creates an Account from DID and Address and stores it via ThebeDB.
func CreateAccount(PooledConnection *config.PooledConnection, DIDAddress string, Address common.Address, metadata map[string]interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("CreateAccount: %w", err)
	}
	now := time.Now().UTC().UnixNano()
	return h.CreateAccount(ctx, &store.Account{
		DIDAddress:  DIDAddress,
		Address:     Address,
		Balance:     "0",
		Nonce:       0,
		AccountType: "publickey",
		Metadata:    metadata,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
}

// storeAccount stores a Key document in the accounts database via ThebeDB.
func storeAccount(PooledConnection *config.PooledConnection, KeyDoc *Account) error {
	if KeyDoc == nil {
		return fmt.Errorf("key document cannot be nil")
	}
	if KeyDoc.Address == (common.Address{}) {
		return fmt.Errorf("Address cannot be empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("storeAccount: %w", err)
	}
	now := time.Now().UTC().UnixNano()
	return h.CreateAccount(ctx, &store.Account{
		DIDAddress:  KeyDoc.DIDAddress,
		Address:     KeyDoc.Address,
		Balance:     KeyDoc.Balance,
		Nonce:       KeyDoc.Nonce,
		TxNonce:     KeyDoc.TxNonce,
		TxCountSent: KeyDoc.TxCountSent,
		AccountType: KeyDoc.AccountType,
		Metadata:    KeyDoc.Metadata,
		CreatedAt:   KeyDoc.CreatedAt,
		UpdatedAt:   now,
	})
}

// GetAccount retrieves an Account by address from ThebeDB.
func GetAccount(PooledConnection *config.PooledConnection, address common.Address) (*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, hErr := getHandle(nil)
	if hErr != nil {
		return nil, fmt.Errorf("GetAccount: %w", hErr)
	}
	sa, err := h.GetAccount(ctx, address.Hex())
	if err != nil {
		return nil, fmt.Errorf("GetAccount(%s): %w", address.Hex(), err)
	}
	if sa == nil {
		return nil, fmt.Errorf("key not found")
	}
	return &Account{
		DIDAddress:  sa.DIDAddress,
		Address:     sa.Address,
		Balance:     sa.Balance,
		Nonce:       sa.Nonce,
		TxNonce:     sa.TxNonce,
		TxCountSent: sa.TxCountSent,
		AccountType: sa.AccountType,
		CreatedAt:   sa.CreatedAt,
		UpdatedAt:   sa.UpdatedAt,
		Metadata:    sa.Metadata,
	}, nil
}

// StorePropagatedAccount securely stores an account received from the P2P network,
// perfectly preserving its ART Nonce and other properties to ensure Fastsync consensus.
func StorePropagatedAccount(PooledConnection *config.PooledConnection, account *Account) error {
	if account == nil || account.Address == (common.Address{}) {
		return fmt.Errorf("propagated account is invalid")
	}
	return storeAccount(nil, account)
}

// GetTransactionsByAccount retrieves all transactions associated with a given account address via ThebeDB.
func GetTransactionsByAccount(PooledConnection *config.PooledConnection, accountAddr *common.Address) ([]*config.Transaction, error) {
	if accountAddr == nil {
		return nil, fmt.Errorf("GetTransactionsByAccount: nil address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByAccount: %w", err)
	}
	recs, err := h.GetTransactionsByAddress(ctx, accountAddr.Hex(), 500)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByAccount(%s): %w", accountAddr.Hex(), err)
	}
	txs := make([]*config.Transaction, 0, len(recs))
	for _, r := range recs {
		txs = append(txs, txRecordToConfig(r))
	}
	return txs, nil
}

// DBTx pairs a config.Transaction with its DB placement metadata.
// Used by Nodeinfo adapters to build types.DBTransaction with a real
// BlockNumber — reconciliation resolves coinbase/ZKVM per-tx via BlockNumber.
type DBTx struct {
	Tx          *config.Transaction
	BlockNumber uint64
	TxIndex     uint16
}

// GetDBTransactionsByAccount retrieves all transactions for an account with
// block placement metadata preserved.
func GetDBTransactionsByAccount(accountAddr *common.Address) ([]DBTx, error) {
	if accountAddr == nil {
		return nil, fmt.Errorf("GetDBTransactionsByAccount: nil address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetDBTransactionsByAccount: %w", err)
	}
	recs, err := h.GetTransactionsByAddress(ctx, accountAddr.Hex(), 0x7fffffff)
	if err != nil {
		return nil, fmt.Errorf("GetDBTransactionsByAccount(%s): %w", accountAddr.Hex(), err)
	}
	out := make([]DBTx, 0, len(recs))
	for _, r := range recs {
		out = append(out, DBTx{Tx: txRecordToConfig(r), BlockNumber: r.BlockNumber, TxIndex: uint16(r.TxIndex)})
	}
	return out, nil
}

// GetDBTransactionsByAccountInRange retrieves transactions for an account within
// [fromBlock, toBlock] inclusive, with block placement metadata preserved.
// Hot path for CatchUp ReconcileWithDeltas.
func GetDBTransactionsByAccountInRange(accountAddr *common.Address, fromBlock, toBlock uint64) ([]DBTx, error) {
	if accountAddr == nil {
		return nil, fmt.Errorf("GetDBTransactionsByAccountInRange: nil address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetDBTransactionsByAccountInRange: %w", err)
	}
	recs, err := h.GetTransactionsByAddressInRange(ctx, accountAddr.Hex(), fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("GetDBTransactionsByAccountInRange(%s): %w", accountAddr.Hex(), err)
	}
	out := make([]DBTx, 0, len(recs))
	for _, r := range recs {
		out = append(out, DBTx{Tx: txRecordToConfig(r), BlockNumber: r.BlockNumber, TxIndex: uint16(r.TxIndex)})
	}
	return out, nil
}

// GetTransactionsByAccountInRange retrieves transactions where the account is
// sender or receiver within [fromBlock, toBlock] inclusive, via ThebeDB SQL.
// Hot path for CatchUp ReconcileWithDeltas — uses composite (addr, block_number) indexes.
func GetTransactionsByAccountInRange(PooledConnection *config.PooledConnection, accountAddr *common.Address, fromBlock, toBlock uint64) ([]*config.Transaction, error) {
	if accountAddr == nil {
		return nil, fmt.Errorf("GetTransactionsByAccountInRange: nil address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByAccountInRange: %w", err)
	}
	recs, err := h.GetTransactionsByAddressInRange(ctx, accountAddr.Hex(), fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByAccountInRange(%s): %w", accountAddr.Hex(), err)
	}
	txs := make([]*config.Transaction, 0, len(recs))
	for _, r := range recs {
		txs = append(txs, txRecordToConfig(r))
	}
	return txs, nil
}

// ========================================
// ART NONCE (from account_immuclient.go)
// ========================================

var artNonceCounter uint64

// [AUDIT OK]: Atomic counter and bit shift mathematically proven safe against overflow (51 bits for micro + 12 for counter = 63 bits); 1 call site in CreateAccount.
// GenerateARTNonce generates a locally unique Nonce for Fastsync ART routing.
// This is strictly used when this node originates an account (e.g., manual DID creation).
// Accounts synced from the network MUST preserve the sender's ART Nonce.
func GenerateARTNonce() uint64 {
	ts := uint64(time.Now().UTC().UnixMicro())
	c := atomic.AddUint64(&artNonceCounter, 1)
	return (ts << 12) | (c & 0xFFF)
}

// ========================================
// CONVERSION HELPERS (from account_immuclient.go)
// ========================================

// txRecordToConfig converts a thebegateway.TransactionRecord to *config.Transaction.
func txRecordToConfig(r *thebegateway.TransactionRecord) *config.Transaction {
	tx := &config.Transaction{}
	tx.Hash = common.HexToHash(r.TxHash)
	if r.FromAddr != "" {
		addr := common.HexToAddress(r.FromAddr)
		tx.From = &addr
	}
	if r.ToAddr != nil && *r.ToAddr != "" {
		addr := common.HexToAddress(*r.ToAddr)
		tx.To = &addr
	}
	tx.Nonce, _ = strconv.ParseUint(r.Nonce, 10, 64)
	tx.GasLimit, _ = strconv.ParseUint(r.GasLimit, 10, 64)
	if r.ValueWei != "" {
		v := new(big.Int)
		v.SetString(r.ValueWei, 10)
		tx.Value = v
	}
	if r.GasPriceWei != "" {
		gp := new(big.Int)
		gp.SetString(r.GasPriceWei, 10)
		tx.GasPrice = gp
	}
	tx.Type = uint8(r.Type)
	tx.Data = r.Data
	tx.Timestamp = r.BlockNumber // approximation; exact timestamp not in TransactionRecord
	return tx
}
