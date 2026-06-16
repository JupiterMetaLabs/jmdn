package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
)

// Account is the DB_OPs-level account type.
// Fields mirror store.Account exactly for backward compatibility.
type Account struct {
	// Legacy DID fields (for backward compatibility)
	DIDAddress string `json:"did,omitempty"`

	// New PublicKey based fields
	Address common.Address `json:"address"`
	Balance string         `json:"balance,omitempty"`
	Nonce   uint64         `json:"nonce"`

	// Account metadata
	AccountType string `json:"account_type"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`

	// Optional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type AccountsSet struct {
	Accounts map[string]*Account
}

func NewAccountsSet() *AccountsSet {
	return &AccountsSet{
		Accounts: make(map[string]*Account),
	}
}

func (s *AccountsSet) Add(address common.Address) {
	s.Accounts[address.Hex()] = nil
}

var counter uint64

func PutNonceofAccount() (uint64, error) {
	ts := uint64(time.Now().UTC().UnixNano())
	c := atomic.AddUint64(&counter, 1)
	return ts<<16 | (c & 0xFFFF), nil
}

// getHandle extracts the store.ThebeHandle from a PooledConnection.
func getHandle(conn *config.PooledConnection) (store.ThebeHandle, error) {
	if conn == nil || conn.Client == nil {
		return nil, fmt.Errorf("getHandle: nil connection or client")
	}
	h, ok := conn.Client.(store.ThebeHandle)
	if !ok {
		return nil, fmt.Errorf("getHandle: Client does not implement store.ThebeHandle (type: %T)", conn.Client)
	}
	return h, nil
}

// storeAccountToStore converts DB_OPs.Account to store.Account.
func storeAccountToStore(a *Account) *store.Account {
	if a == nil {
		return nil
	}
	return &store.Account{
		DIDAddress:  a.DIDAddress,
		Address:     a.Address,
		Balance:     a.Balance,
		Nonce:       a.Nonce,
		AccountType: a.AccountType,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
		Metadata:    a.Metadata,
	}
}

// storeAccountFromStore converts store.Account to DB_OPs.Account.
func storeAccountFromStore(a *store.Account) *Account {
	if a == nil {
		return nil
	}
	return &Account{
		DIDAddress:  a.DIDAddress,
		Address:     a.Address,
		Balance:     a.Balance,
		Nonce:       a.Nonce,
		AccountType: a.AccountType,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
		Metadata:    a.Metadata,
	}
}

// CreateAccount creates an account in ThebeDB.
func CreateAccount(conn *config.PooledConnection, DIDAddress string, Address common.Address, metadata map[string]interface{}) error {
	if DIDAddress == "" || Address == (common.Address{}) {
		return fmt.Errorf("DIDAddress and Address cannot be empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("CreateAccount: failed to get connection: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("CreateAccount: %w", err)
	}

	nonce, err := PutNonceofAccount()
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixNano()
	return h.CreateAccount(ctx, &store.Account{
		DIDAddress:  DIDAddress,
		Address:     Address,
		Balance:     "0",
		Nonce:       nonce,
		AccountType: "user",
		CreatedAt:   now,
		UpdatedAt:   now,
		Metadata:    metadata,
	})
}

// storeAccount stores a pre-built Account via ThebeHandle.
func storeAccount(conn *config.PooledConnection, KeyDoc *Account) error {
	if KeyDoc == nil {
		return fmt.Errorf("key document cannot be nil")
	}
	if KeyDoc.DIDAddress == "" || KeyDoc.Address == (common.Address{}) {
		return fmt.Errorf("DIDAddress and Address cannot be empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("storeAccount: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("storeAccount: %w", err)
	}

	sa := storeAccountToStore(KeyDoc)
	sa.UpdatedAt = time.Now().UTC().UnixNano()
	return h.CreateAccount(ctx, sa)
}

// BatchCreateAccountsOrdered stores multiple accounts in order.
func BatchCreateAccountsOrdered(conn *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries cannot be empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("BatchCreateAccountsOrdered: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("BatchCreateAccountsOrdered: %w", err)
	}

	for _, e := range entries {
		if e.Key == "" || e.Value == nil {
			return fmt.Errorf("BatchCreateAccountsOrdered: invalid entry (empty key or nil value)")
		}
		if !strings.HasPrefix(e.Key, Prefix) {
			continue
		}
		var acc store.Account
		if jsonErr := json.Unmarshal(e.Value, &acc); jsonErr != nil {
			return fmt.Errorf("BatchCreateAccountsOrdered: unmarshal failed for %s: %w", e.Key, jsonErr)
		}
		if createErr := h.CreateAccount(ctx, &acc); createErr != nil {
			return fmt.Errorf("BatchCreateAccountsOrdered: %w", createErr)
		}
	}
	return nil
}

// BatchRestoreAccounts applies entries with LWW conflict resolution.
func BatchRestoreAccounts(conn *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries cannot be empty")
	}

	var err error
	var shouldReturn bool
	ctx := context.Background()

	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("BatchRestoreAccounts: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("BatchRestoreAccounts: %w", err)
	}

	writeCtx, writeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer writeCancel()

	applied := 0
	for _, e := range entries {
		if e.Key == "" || e.Value == nil {
			return fmt.Errorf("BatchRestoreAccounts: invalid entry")
		}
		if !strings.HasPrefix(e.Key, Prefix) {
			continue
		}
		var incoming store.Account
		if jsonErr := json.Unmarshal(e.Value, &incoming); jsonErr != nil {
			continue
		}
		addrHex := strings.TrimPrefix(e.Key, Prefix)
		existing, getErr := h.GetAccount(writeCtx, addrHex)
		if getErr == nil && existing != nil {
			if existing.UpdatedAt > incoming.UpdatedAt {
				continue
			}
			if existing.UpdatedAt == incoming.UpdatedAt && existing.Balance == incoming.Balance {
				continue
			}
		}
		if createErr := h.CreateAccount(writeCtx, &incoming); createErr != nil {
			return fmt.Errorf("BatchRestoreAccounts: write failed for %s: %w", e.Key, createErr)
		}
		applied++
	}
	logger(log.DB_OPs_AccountConnectionPool).Debug(context.Background(),
		"BatchRestoreAccounts completed", ion.Int("applied", applied))
	return nil
}

// loadAccountByKey reads an account by raw key bytes (address: or did: prefix).
func loadAccountByKey(conn *config.PooledConnection, key []byte, logFn string) (*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", logFn, err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", logFn, err)
	}

	keyStr := string(key)
	var sa *store.Account
	if strings.HasPrefix(keyStr, DIDPrefix) {
		did := strings.TrimPrefix(keyStr, DIDPrefix)
		sa, err = h.GetAccountByDID(ctx, did)
	} else {
		addrHex := strings.TrimPrefix(keyStr, Prefix)
		sa, err = h.GetAccount(ctx, addrHex)
	}
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%s: %w", logFn, err)
	}
	return storeAccountFromStore(sa), nil
}

// GetAccountByDID retrieves an account by DID string.
func GetAccountByDID(conn *config.PooledConnection, did string) (*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetAccountByDID: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return nil, fmt.Errorf("GetAccountByDID: %w", err)
	}

	sa, err := h.GetAccountByDID(ctx, did)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("GetAccountByDID: %w", err)
	}
	return storeAccountFromStore(sa), nil
}

// GetAccount retrieves an account by Ethereum address.
func GetAccount(conn *config.PooledConnection, address common.Address) (*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetAccount: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return nil, fmt.Errorf("GetAccount: %w", err)
	}

	sa, err := h.GetAccount(ctx, address.Hex())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("GetAccount: %w", err)
	}
	return storeAccountFromStore(sa), nil
}

// UpdateAccountBalance updates the balance of an account.
func UpdateAccountBalance(conn *config.PooledConnection, address common.Address, newBalance string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("UpdateAccountBalance: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("UpdateAccountBalance: %w", err)
	}
	return h.UpdateAccountBalance(ctx, address.Hex(), newBalance)
}

// ListAllAccounts retrieves all accounts. Returns empty list for ThebeDB backend.
func ListAllAccounts(conn *config.PooledConnection, limit int) ([]*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = ctx

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("ListAllAccounts: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return nil, fmt.Errorf("ListAllAccounts: %w", err)
	}

	// Time: O(n) — single SQL scan ordered by created_at; n = rows returned (limit<=0 → all).
	storeAccounts, err := h.ListAccounts(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAllAccounts: %w", err)
	}
	out := make([]*Account, 0, len(storeAccounts))
	for _, sa := range storeAccounts {
		out = append(out, storeAccountFromStore(sa))
	}
	return out, nil
}

// ListAccountsPaginated returns paginated accounts. Not natively supported; returns empty.
func ListAccountsPaginated(conn *config.PooledConnection, limit, offset int, extendedPrefix string) ([]*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = ctx

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("ListAccountsPaginated: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	logger(log.DB_OPs_AccountConnectionPool).Warn(context.Background(),
		"ListAccountsPaginated: not fully implemented for ThebeDB backend")
	return []*Account{}, nil
}

// CountAccounts returns the total number of accounts.
func CountAccounts(conn *config.PooledConnection) (int, error) {
	count, err := CountBuilder{}.GetAccountsDBCount(Prefix)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetTransactionsByAccount retrieves all transactions for a given account.
func GetTransactionsByAccount(conn *config.PooledConnection, accountAddr *common.Address) ([]*config.Transaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetTransactionsByAccount: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByAccount: %w", err)
	}

	records, err := h.GetTransactionsByAddress(ctx, accountAddr.Hex(), 1000)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionsByAccount: %w", err)
	}

	txs := make([]*config.Transaction, 0, len(records))
	for _, r := range records {
		block, blockErr := h.GetBlock(ctx, r.BlockNumber)
		if blockErr != nil {
			continue
		}
		zkBlock, reconErr := blockRecordToZKBlock(block)
		if reconErr != nil {
			continue
		}
		for i := range zkBlock.Transactions {
			if zkBlock.Transactions[i].Hash.Hex() == r.TxHash {
				cp := zkBlock.Transactions[i]
				txs = append(txs, &cp)
				break
			}
		}
	}
	return txs, nil
}

func isTransactionInvolvingAccount(tx config.Transaction, accountAddr *common.Address) bool {
	if tx.From != nil && *tx.From == *accountAddr {
		return true
	}
	if tx.To != nil && *tx.To == *accountAddr {
		return true
	}
	return false
}

// CheckNonceDuplicate checks if a transaction with the same (from, nonce) already exists.
func CheckNonceDuplicate(conn *config.PooledConnection, fromAddr *common.Address, nonce uint64) (bool, error) {
	ctx := context.Background()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return false, fmt.Errorf("CheckNonceDuplicate: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return false, fmt.Errorf("CheckNonceDuplicate: %w", err)
	}
	return h.CheckNonceDuplicate(ctx, fromAddr.Hex(), nonce)
}

// GetLatestNonce retrieves the latest nonce for a given account.
func GetLatestNonce(conn *config.PooledConnection, fromAddr *common.Address) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return 0, fmt.Errorf("GetLatestNonce: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return 0, fmt.Errorf("GetLatestNonce: %w", err)
	}
	return h.GetLatestNonce(ctx, fromAddr.Hex())
}

// GetTransactionsByAccountPaginated retrieves paginated transactions for a given account.
func GetTransactionsByAccountPaginated(conn *config.PooledConnection, accountAddr *common.Address, offset, limit int) ([]*config.Transaction, int, error) {
	txs, err := GetTransactionsByAccount(conn, accountAddr)
	if err != nil {
		return nil, 0, err
	}
	total := len(txs)
	if offset >= total {
		return []*config.Transaction{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return txs[offset:end], total, nil
}

// GetTransactionHashes retrieves transaction hashes with pagination (DEPRECATED).
func GetTransactionHashes(conn *config.PooledConnection, offset, limit int) ([]string, int, error) {
	transactions, total, err := GetTransactionsPaginated(conn, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	hashes := make([]string, len(transactions))
	for i, tx := range transactions {
		hashes[i] = tx.Hash.Hex()
	}
	return hashes, total, nil
}

// GetTransactionsPaginated retrieves transactions with pagination.
func GetTransactionsPaginated(conn *config.PooledConnection, offset, limit int) ([]*config.Transaction, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("GetTransactionsPaginated: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return nil, 0, fmt.Errorf("GetTransactionsPaginated: %w", err)
	}

	latest, err := h.GetLatestBlockNumber(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("GetTransactionsPaginated: %w", err)
	}

	var allTxs []*config.Transaction
	for i := uint64(0); i <= latest; i++ {
		block, blockErr := h.GetBlock(ctx, i)
		if blockErr != nil {
			continue
		}
		records, txErr := h.GetTransactionsByBlock(ctx, block.BlockNumber)
		if txErr != nil {
			continue
		}
		zkBlock, reconErr := blockRecordToZKBlock(block)
		if reconErr != nil || zkBlock == nil {
			continue
		}
		for _, r := range records {
			for j := range zkBlock.Transactions {
				if zkBlock.Transactions[j].Hash.Hex() == r.TxHash {
					cp := zkBlock.Transactions[j]
					allTxs = append(allTxs, &cp)
					break
				}
			}
		}
	}

	total := len(allTxs)
	if offset >= total {
		return []*config.Transaction{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return allTxs[offset:end], total, nil
}

// ensureAccountsDBSelected is a no-op for ThebeDB handles (stateless).
func ensureAccountsDBSelected(conn *config.PooledConnection) error {
	if conn == nil || conn.Client == nil {
		return fmt.Errorf("ensureAccountsDBSelected: nil connection")
	}
	return nil
}

// reconnectToAccountsDB is a no-op for ThebeDB handles.
func reconnectToAccountsDB(conn *config.PooledConnection) error {
	return nil
}

// CheckNonceAndGetLatest combines nonce duplicate check and latest nonce retrieval.
func CheckNonceAndGetLatest(conn *config.PooledConnection, fromAddr *common.Address, submittedNonce uint64) (bool, uint64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			return false, 0, false, fmt.Errorf("CheckNonceAndGetLatest: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutMainDBConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return false, 0, false, fmt.Errorf("CheckNonceAndGetLatest: %w", err)
	}

	hasDuplicate, err := h.CheckNonceDuplicate(ctx, fromAddr.Hex(), submittedNonce)
	if err != nil {
		return false, 0, false, fmt.Errorf("CheckNonceAndGetLatest: %w", err)
	}

	latestNonce, err := h.GetLatestNonce(ctx, fromAddr.Hex())
	if err != nil {
		return false, 0, false, fmt.Errorf("CheckNonceAndGetLatest: %w", err)
	}

	hasAnyTx := latestNonce > 0 || hasDuplicate
	return hasDuplicate, latestNonce, hasAnyTx, nil
}

// SaveAccount persists a full Account record by delegating to UpdateAccountBalance
// and, when supported, to other field setters. For ThebeDB, balance is the primary
// mutable field tracked via UpdateAccountBalance.
func SaveAccount(conn *config.PooledConnection, acc *Account) error {
	if acc == nil {
		return ErrNilValue
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	var shouldReturn bool
	if conn == nil || conn.Client == nil {
		conn, err = GetAccountConnectionandPutBack(ctx)
		if err != nil {
			return fmt.Errorf("SaveAccount: %w", err)
		}
		shouldReturn = true
	}
	if shouldReturn {
		defer PutAccountsConnection(conn)
	}

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("SaveAccount: %w", err)
	}

	return h.UpdateAccountBalance(ctx, acc.Address.Hex(), acc.Balance)
}
