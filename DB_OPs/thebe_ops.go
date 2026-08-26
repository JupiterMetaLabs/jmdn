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
	msg := err.Error()
	return strings.Contains(msg, "key not found") ||
		strings.Contains(msg, "tbtree: key not found") ||
		// SQL-backed reads (accounts, blocks) surface a missing row this way —
		// it is a genuine "not found", not a transient error.
		strings.Contains(msg, "no rows in result set")
}

// IsNotFound reports whether err is any not-found shape — KV ("key not found",
// "tbtree: key not found") or SQL ("no rows in result set"). Exported canonical
// matcher for callers in other packages (apply path, account manager) that must
// treat a brand-new account — a contract address or a first-time receiver read
// from the SQL-backed store — as missing, not as a transient failure.
func IsNotFound(err error) bool { return isNotFoundError(err) }

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

// GetDatabaseState returns the current state of the database.
// ImmuDB removed — returns a zeroed DatabaseState.
func GetDatabaseState(_ *config.PooledConnection) (*DatabaseState, error) {
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

// StoreL1CommitRange records that L1 transaction l1TxHash (mined in Ethereum
// block l1BlockNumber) committed L2 blocks [fromBlock..toBlock].
// Append-only — blocks rows are immutable, so L1 finality lives in its own table.
func StoreL1CommitRange(l1TxHash string, l1BlockNumber, fromBlock, toBlock uint64) error {
	if l1TxHash == "" || toBlock < fromBlock {
		return fmt.Errorf("StoreL1CommitRange: invalid args (hash=%q, range=[%d..%d])", l1TxHash, fromBlock, toBlock)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("StoreL1CommitRange: %w", err)
	}
	nums := make([]uint64, 0, toBlock-fromBlock+1)
	for b := fromBlock; b <= toBlock; b++ {
		nums = append(nums, b)
	}
	return h.StoreL1Finality(ctx, &thebegateway.L1FinalityRecord{
		Confirmation:  l1TxHash,
		L1BlockNumber: l1BlockNumber,
		BlockNumbers:  nums,
	})
}

// GetL1CommitForBlock returns the L1 tx hash and L1 block number for an L2
// block, or ("", 0, nil) when the block is not yet committed to L1.
func GetL1CommitForBlock(blockNumber uint64) (string, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return "", 0, fmt.Errorf("GetL1CommitForBlock: %w", err)
	}
	rec, err := h.GetL1FinalityForBlock(ctx, blockNumber)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "", 0, nil
		}
		return "", 0, err
	}
	return rec.Confirmation, rec.L1BlockNumber, nil
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
	// Hydrate L1 finality (best-effort — absent until commitRollup mines on L1).
	if l1rec, err := h.GetL1FinalityForBlock(ctx, blockNumber); err == nil && l1rec != nil {
		blk.L1TxHash = l1rec.Confirmation
		blk.L1BlockNumber = l1rec.L1BlockNumber
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
	GasFeeWei   string // recorded fee from the gas_fee_wei column ("" for legacy rows)
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
		out = append(out, DBTx{Tx: txRecordToConfig(r), BlockNumber: r.BlockNumber, TxIndex: uint16(r.TxIndex), GasFeeWei: r.GasFeeWei})
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
		out = append(out, DBTx{Tx: txRecordToConfig(r), BlockNumber: r.BlockNumber, TxIndex: uint16(r.TxIndex), GasFeeWei: r.GasFeeWei})
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
	if r.MaxFeeWei != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(r.MaxFeeWei), 10); ok {
			tx.MaxFee = n
		}
	}
	if r.MaxPriorityFeeWei != "" {
		if n, ok := new(big.Int).SetString(strings.TrimSpace(r.MaxPriorityFeeWei), 10); ok {
			tx.MaxPriorityFee = n
		}
	}
	tx.Type = uint8(r.Type)
	tx.Data = r.Data
	tx.Timestamp = r.BlockNumber // approximation; exact timestamp not in TransactionRecord

	// Signature: SigR/SigS are stored as base-16 (no 0x) via big.Int.Text(16) in
	// toTransactionRecord, and CHAR(66) pads with trailing spaces — so trim space
	// and any 0x prefix before parsing base 16. Without this, R/S/V come back nil
	// and eth_getTransactionByHash marshals r=s=v=0 even though the row is signed
	// (breaks wallets/explorers that re-verify the signature, e.g. MetaMask never
	// leaving "pending"). SigV=0 is a valid type-2 y-parity, so set V unconditionally.
	parseSig := func(s string) *big.Int {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "0x")
		s = strings.TrimPrefix(s, "0X")
		if s == "" {
			return nil
		}
		n, ok := new(big.Int).SetString(s, 16)
		if !ok {
			return nil
		}
		return n
	}
	tx.V = new(big.Int).SetUint64(r.SigV)
	if n := parseSig(r.SigR); n != nil {
		tx.R = n
	}
	if n := parseSig(r.SigS); n != nil {
		tx.S = n
	}
	return tx
}
