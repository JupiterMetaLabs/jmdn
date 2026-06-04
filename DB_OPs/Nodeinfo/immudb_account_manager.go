package NodeInfo

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
	"gossipnode/DB_OPs"
	"gossipnode/config"
)

type account_manager struct{}

// Time Complexity: O(N) where N is the total number of transactions scanned or retrieved
func (am *account_manager) GetTransactionsForAccount(accountAddress string) ([]types.DBTransaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get main DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)
	cfgTxs, err := DB_OPs.GetTransactionsByAccount(conn, &addr)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions by account: %w", err)
	}

	result := make([]types.DBTransaction, 0, len(cfgTxs))
	for _, tx := range cfgTxs {
		result = append(result, configTxToDBTx(tx))
	}
	return result, nil
}

// Time Complexity: O(1)
func (am *account_manager) GetAccountBalance(accountAddress string) (*big.Int, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get account DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)
	acc, err := DB_OPs.GetAccount(conn, addr)
	if err != nil {
		if strings.Contains(err.Error(), "key not found") {
			return big.NewInt(0), 0, nil
		}
		return nil, 0, fmt.Errorf("failed to get account: %w", err)
	}

	balance := new(big.Int)
	balance.SetString(acc.Balance, 10)
	return balance, acc.Nonce, nil
}

// Time Complexity: O(1) — read-modify-write to update both balance and nonce atomically.
func (am *account_manager) UpdateAccountBalance(accountAddress string, balance *big.Int, nonce uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return fmt.Errorf("failed to get account DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)

	doc, err := DB_OPs.GetAccount(conn, addr)
	if err != nil {
		if strings.Contains(err.Error(), "key not found") {
			return am.CreateAccount(accountAddress, balance, nonce)
		}
		return fmt.Errorf("failed to get account for update: %w", err)
	}

	doc.Balance = balance.String()
	doc.Nonce = nonce
	doc.UpdatedAt = time.Now().UTC().UnixNano()

	key := fmt.Sprintf("%s%s", DB_OPs.Prefix, addr)
	if err := DB_OPs.SafeCreate(conn.Client, key, doc); err != nil {
		return fmt.Errorf("failed to write updated account: %w", err)
	}

	return nil
}

// Time Complexity: O(1)
func (am *account_manager) CreateAccount(accountAddress string, balance *big.Int, nonce uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return fmt.Errorf("failed to get account DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)

	// CreateAccount atomically writes the address: KV entry AND the did: reference via ExecAll.
	// It generates its own nonce internally, so we correct it afterwards.
	meta := make(map[string]interface{})
	if err := DB_OPs.CreateAccount(conn, accountAddress, addr, meta); err != nil {
		return fmt.Errorf("failed to create account: %w", err)
	}

	// Read-modify-write to set the caller-provided balance and nonce.
	doc, err := DB_OPs.GetAccount(conn, addr)
	if err != nil {
		return fmt.Errorf("failed to read back created account: %w", err)
	}

	doc.Balance = balance.String()
	doc.Nonce = nonce
	doc.UpdatedAt = time.Now().UTC().UnixNano()

	key := fmt.Sprintf("%s%s", DB_OPs.Prefix, addr)
	if err := DB_OPs.SafeCreate(conn.Client, key, doc); err != nil {
		return fmt.Errorf("failed to write account with correct balance/nonce: %w", err)
	}

	return nil
}

// Time Complexity: O(1)
func (am *account_manager) GetAccountByAddress(accountAddress string) (*types.Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get account DB connection: %w", err)
	}

	// Strip "address:" DB key prefix if present — the external FastSync module may pass
	// DB key format; common.HexToAddress expects bare hex (0x... or unprefixed).
	accountAddress = strings.TrimPrefix(accountAddress, DB_OPs.Prefix)

	addr := common.HexToAddress(accountAddress)
	acc, err := DB_OPs.GetAccount(conn, addr)
	if err != nil {
		if strings.Contains(err.Error(), "key not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get account: %w", err)
	}
	return dbOpsToTypes(acc), nil
}

// WriteAccounts enqueues accounts to the Redis stream for async DB write.
// Returns immediately after the enqueue — the caller gets an ACK without waiting
// for the ImmuDB commit (which can take up to 15 s under load).
//
// StartAccountSyncWorker must be called before WriteAccounts or this returns an error.
// If Redis is unavailable, this fails fast — no fallback to synchronous DB write.
// At-least-once delivery is guaranteed by the worker via PEL + XAUTOCLAIM.
//
// Time: O(N) serialization + O(1) XADD round trip, where N = len(accounts).
func (am *account_manager) WriteAccounts(accounts []*types.Account) error {
	if len(accounts) == 0 {
		return nil
	}
	s, mgr := getAccountQueue()
	if s == nil {
		return fmt.Errorf("WriteAccounts: account queue not initialized; call StartAccountSyncWorker before use")
	}
	mgr.EnsureActive()
	data, err := json.Marshal(accounts)
	if err != nil {
		return fmt.Errorf("WriteAccounts: marshal accounts: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = s.Enqueue(ctx, accountSyncStream, map[string]any{
		"type": string(payloadTypeAccounts),
		"data": string(data),
	}); err != nil {
		return fmt.Errorf("WriteAccounts: enqueue to stream %q: %w", accountSyncStream, err)
	}
	return nil
}

// NewAccountNonceIterator returns a cursor-based iterator over all accounts.
// Each NextBatch call advances a seekKey cursor — O(N) total scan across all batches.
func (am *account_manager) NewAccountNonceIterator(batchSize int) types.AccountNonceIterator {
	return &immudbNonceIter{
		batchSize: batchSize,
	}
}

// ─── immudbNonceIter ─────────────────────────────────────────────────────────

// MODULE: DB_OPs/Nodeinfo (immudbNonceIter)
// PURPOSE: cursor-based iterator that pages all accounts from ImmuDB in ascending key order.
//
// CORE DATA STRUCTURES:
//   - lastKey []byte: scan cursor — key of the last returned account; nil = start of DB.
//     Fixed size (one key). Threaded across NextBatch calls so each call resumes where the
//     previous left off instead of restarting from key 0.
//
// DO NOT:
//   - Replace lastKey with an offset int — that restarts the scan from key 0 each call (O(N²)).
//   - Add an in-memory account cache on this struct — 2.7M entries exhaust heap during sync.

type immudbNonceIter struct {
	batchSize int
	lastKey   []byte // scan cursor: key of last returned account, nil = start
	done      bool
}

// Time: O(1)
func (it *immudbNonceIter) TotalAccounts() (uint64, error) {
	count, err := DB_OPs.CountAccounts(nil)
	return uint64(count), err
}

// Time: O(batchSize) ImmuDB entries; Space: O(batchSize)
func (it *immudbNonceIter) NextBatch() ([]*types.Account, error) {
	if it.done {
		return nil, nil
	}

	accs, lastKey, err := DB_OPs.ListAccountsPaginatedFrom(nil, it.batchSize, it.lastKey, "")
	if err != nil {
		return nil, fmt.Errorf("account nonce iterator: %w", err)
	}
	if len(accs) == 0 {
		it.done = true
		return nil, nil
	}

	result := make([]*types.Account, len(accs))
	for i, acc := range accs {
		result[i] = dbOpsToTypes(acc)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Nonce < result[j].Nonce
	})

	it.lastKey = lastKey
	if len(accs) < it.batchSize {
		it.done = true
	}
	return result, nil
}

// GetAccountsByNonces scans all accounts once via cursor to find those matching the given nonces.
// Time: O(N) where N = total accounts; Space: O(|nonces|)
func (it *immudbNonceIter) GetAccountsByNonces(nonces []uint64) ([]*types.Account, error) {
	if len(nonces) == 0 {
		return nil, nil
	}

	nonceSet := make(map[uint64]struct{}, len(nonces))
	for _, n := range nonces {
		nonceSet[n] = struct{}{}
	}

	result := make([]*types.Account, 0, len(nonces))
	var seekKey []byte

	for {
		accs, lastKey, err := DB_OPs.ListAccountsPaginatedFrom(nil, 1000, seekKey, "")
		if err != nil {
			return nil, fmt.Errorf("GetAccountsByNonces scan: %w", err)
		}
		if len(accs) == 0 {
			break
		}
		for _, acc := range accs {
			ta := dbOpsToTypes(acc)
			if _, ok := nonceSet[ta.Nonce]; ok {
				result = append(result, ta)
				if len(result) == len(nonces) {
					return result, nil
				}
			}
		}
		if lastKey == nil || len(accs) < 1000 {
			break
		}
		seekKey = lastKey
	}
	return result, nil
}

func (it *immudbNonceIter) Close() {}

// ─── helpers ─────────────────────────────────────────────────────────────────

func dbOpsToTypes(acc *DB_OPs.Account) *types.Account {
	return &types.Account{
		DIDAddress:  acc.DIDAddress,
		Address:     acc.Address,
		Balance:     acc.Balance,
		Nonce:       acc.StateID, // MAP: StateID -> Fastsync Nonce (ART Key)
		TxNonce:     acc.Nonce,   // MAP: Ethereum Nonce -> TxNonce
		TxCountSent: acc.TxCountSent,
		AccountType: acc.AccountType,
		CreatedAt:   acc.CreatedAt,
		UpdatedAt:   acc.UpdatedAt,
		Metadata:    acc.Metadata,
	}
}

// BatchUpdateAccounts enqueues account balance/nonce updates to the Redis stream for async DB write.
// Returns immediately after the enqueue.
//
// StartAccountSyncWorker must be called before BatchUpdateAccounts or this returns an error.
// If Redis is unavailable, this fails fast.
// At-least-once delivery is guaranteed by the worker via PEL + XAUTOCLAIM.
//
// Time: O(N) serialization + O(1) XADD round trip, where N = len(updates).
func (am *account_manager) BatchUpdateAccounts(updates []types.AccountUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	s, mgr := getAccountQueue()
	if s == nil {
		return fmt.Errorf("BatchUpdateAccounts: account queue not initialized; call StartAccountSyncWorker before use")
	}
	mgr.EnsureActive()
	// Convert to wire type for stable JSON serialization.
	// big.Int.String() produces a decimal string; accountUpdateWire makes the format explicit.
	wires := make([]accountUpdateWire, len(updates))
	for i, u := range updates {
		wires[i] = accountUpdateWire{
			Address:    u.Address,
			NewBalance: u.NewBalance.String(),
			Nonce:      u.Nonce,
		}
	}
	data, err := json.Marshal(wires)
	if err != nil {
		return fmt.Errorf("BatchUpdateAccounts: marshal updates: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = s.Enqueue(ctx, accountSyncStream, map[string]any{
		"type": string(payloadTypeUpdates),
		"data": string(data),
	}); err != nil {
		return fmt.Errorf("BatchUpdateAccounts: enqueue to stream %q: %w", accountSyncStream, err)
	}
	return nil
}

// configTxToDBTx converts a config.Transaction to types.DBTransaction via direct field copy.
// DB-specific fields (BlockNumber, TxIndex, CreatedAt) are zero-valued — not available from config.Transaction.
func configTxToDBTx(tx *config.Transaction) types.DBTransaction {
	return types.DBTransaction{
		Transaction: types.Transaction{
			Hash:           tx.Hash,
			From:           tx.From,
			To:             tx.To,
			Value:          tx.Value,
			Type:           tx.Type,
			Timestamp:      tx.Timestamp,
			ChainID:        tx.ChainID,
			Nonce:          tx.Nonce,
			GasLimit:       tx.GasLimit,
			GasPrice:       tx.GasPrice,
			MaxFee:         tx.MaxFee,
			MaxPriorityFee: tx.MaxPriorityFee,
			Data:           tx.Data,
			AccessList:     configAccessListToTypes(tx.AccessList),
			V:              tx.V,
			R:              tx.R,
			S:              tx.S,
		},
	}
}

// configAccessListToTypes converts config.AccessList to types.AccessList.
// Both are structurally identical but defined in separate packages.
func configAccessListToTypes(al config.AccessList) types.AccessList {
	if len(al) == 0 {
		return nil
	}
	result := make(types.AccessList, len(al))
	for i, t := range al {
		result[i] = types.AccessTuple{
			Address:     t.Address,
			StorageKeys: t.StorageKeys,
		}
	}
	return result
}
