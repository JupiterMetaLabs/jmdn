package DB_OPs

// thebe_missing.go — shims for functions referenced by callers outside DB_OPs that have
// no implementation yet.  Every function here either delegates to a real ThebeHandle
// method or is an honest stub that returns a descriptive error.
//
// DO NOT add business logic here.  When the underlying store method is wired up,
// replace the stub body and remove this comment.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// ── Transaction reads ────────────────────────────────────────────────────────

// GetTransactionByHash retrieves a single transaction by its hex hash.
func GetTransactionByHash(_ *config.PooledConnection, hash string) (*config.Transaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionByHash: %w", err)
	}
	rec, err := h.GetTransaction(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionByHash(%s): %w", hash, err)
	}
	return txRecordToConfig(rec), nil
}

// GetTransactionBlock retrieves the block containing a specific transaction.
func GetTransactionBlock(ctx context.Context, _ *config.PooledConnection, txHash string) (*config.ZKBlock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionBlock: %w", err)
	}
	rec, err := h.GetTransaction(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionBlock: tx not found: %w", err)
	}
	return GetZKBlockByNumber(nil, rec.BlockNumber)
}

// ReadZKBlockByHash retrieves a ZK block by hash — delegates to GetZKBlockByHash.
func ReadZKBlockByHash(ctx context.Context, conn *config.PooledConnection, blockHash string) (*config.ZKBlock, error) {
	return GetZKBlockByHash(conn, blockHash)
}

// GetZKBlockByHash retrieves a ZK block by its hash string.
func GetZKBlockByHash(_ *config.PooledConnection, blockHash string) (*config.ZKBlock, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByHash: %w", err)
	}
	rec, err := h.GetBlockByHash(ctx, blockHash)
	if err != nil {
		return nil, fmt.Errorf("GetZKBlockByHash(%s): %w", blockHash, err)
	}
	return blockRecordToZKBlock(rec)
}

// CountTransactions returns the total number of stored transactions.
// Stub — CountTransactions is not yet in store.ThebeHandle.
// Returns 0 until task #26 adds a CountTransactions SQL method.
func CountTransactions(_ *config.PooledConnection) (int64, error) {
	return 0, fmt.Errorf("CountTransactions: not yet implemented in ThebeDB")
}

// GetTransactionsPaginated returns a page of all transactions ordered by block_number DESC.
// Stub — paginated TX queries are not yet in store.ThebeHandle.
func GetTransactionsPaginated(_ *config.PooledConnection, offset, limit int) ([]*config.Transaction, int, error) {
	return nil, 0, fmt.Errorf("GetTransactionsPaginated: not yet implemented in ThebeDB")
}

// GetTransactionsByAccountPaginated returns a page of transactions for address.
// Stub — paginated TX queries are not yet in store.ThebeHandle.
func GetTransactionsByAccountPaginated(_ *config.PooledConnection, address *common.Address, offset, limit int) ([]*config.Transaction, int, error) {
	if address == nil {
		return nil, 0, fmt.Errorf("GetTransactionsByAccountPaginated: nil address")
	}
	return nil, 0, fmt.Errorf("GetTransactionsByAccountPaginated: not yet implemented in ThebeDB")
}

// ── Account reads ────────────────────────────────────────────────────────────

// GetAccountByDID retrieves an account by its DID string.
func GetAccountByDID(_ *config.PooledConnection, did string) (*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("GetAccountByDID: %w", err)
	}
	sa, err := h.GetAccountByDID(ctx, did)
	if err != nil {
		return nil, fmt.Errorf("GetAccountByDID(%s): %w", did, err)
	}
	if sa == nil {
		return nil, fmt.Errorf("key not found")
	}
	return storeAccountToDBOps(sa), nil
}

// ListAllAccounts returns up to limit accounts from ThebeDB.
func ListAllAccounts(_ *config.PooledConnection, limit int) ([]*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("ListAllAccounts: %w", err)
	}
	rows, err := h.ListAccounts(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAllAccounts: %w", err)
	}
	out := make([]*Account, len(rows))
	for i, r := range rows {
		out[i] = storeAccountToDBOps(r)
	}
	return out, nil
}

// CountAccounts returns the total number of accounts as int64.
// Delegates to CountAccountsCtx (compat_connections.go).
func CountAccounts(_ *config.PooledConnection) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := CountAccountsCtx(ctx)
	return int64(n), err
}

// ── Account writes ───────────────────────────────────────────────────────────

// UpdateAccount writes a full account snapshot back to ThebeDB.
// Uses UpdateAccountBalance for the balance field and storeAccount for the
// full record (CreateAccount upsert path).  TxNonce/TxCountSent are
// preserved via storeAccount → h.CreateAccount.
func UpdateAccount(_ *config.PooledConnection, doc *Account) error {
	if doc == nil || doc.Address == (common.Address{}) {
		return fmt.Errorf("UpdateAccount: invalid account")
	}
	return storeAccount(nil, doc)
}

// UpdateAccountBalance updates an account's balance (and optionally records
// blockTimestamp as UpdatedAt).  addr is a common.Address; balance is a
// decimal string (e.g. "1000000000000000000").
func UpdateAccountBalance(_ *config.PooledConnection, addr common.Address, balance string, _ int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("UpdateAccountBalance: %w", err)
	}
	return h.UpdateAccountBalance(ctx, addr.Hex(), balance)
}

// ── Bulk account restore ─────────────────────────────────────────────────────

// BatchRestoreAccounts writes a batch of KV entries as accounts.
// entries is []struct{Key string; Value []byte} — each Value is a JSON-encoded
// Account.  LWW semantics: newer UpdatedAt wins (handled by storeAccount).
func BatchRestoreAccounts(_ context.Context, _ *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	for i, e := range entries {
		var acc Account
		if err := json.Unmarshal(e.Value, &acc); err != nil {
			return fmt.Errorf("BatchRestoreAccounts[%d] key=%s: unmarshal: %w", i, e.Key, err)
		}
		if acc.Address == (common.Address{}) {
			continue // skip malformed entries
		}
		if err := storeAccount(nil, &acc); err != nil {
			return fmt.Errorf("BatchRestoreAccounts[%d] key=%s: store: %w", i, e.Key, err)
		}
	}
	return nil
}

// ── KV-style sentinels ───────────────────────────────────────────────────────

// Exists returns true when a KV sentinel key is resolvable via Read.
// block_processed:<hash> and tx_processed:<hash> are answered via
// IsTxProcessing; all other keys fall back to Read.
func Exists(conn *config.PooledConnection, key string) (bool, error) {
	_, err := Read(conn, key)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// GetAllKeys is a stub — ImmuDB prefix scans are removed.
// Use ThebeDB SQL queries instead.
func GetAllKeys(_ *config.PooledConnection, prefix string) ([]string, error) {
	return nil, fmt.Errorf("GetAllKeys: ImmuDB removed; use ThebeDB SQL queries instead")
}

// ── Merkle ───────────────────────────────────────────────────────────────────

// GetMerkleRoot is a stub — the Merkle root is derived from the KV canonical
// log by JMDN-FastSync, not stored as a DB column.
func GetMerkleRoot(_ *config.PooledConnection) ([]byte, error) {
	return nil, fmt.Errorf("GetMerkleRoot: not available — Merkle root is derived from the KV canonical log")
}

// ── ImmuDB transaction stubs ─────────────────────────────────────────────────

// Transaction executes fn in a logical transaction context.
// ImmuDB removed — fn is invoked directly (no atomic KV batch).
// Callers use this to mark processed tx/block keys, which are now no-ops.
func Transaction(ic *config.ImmuClient, fn func(tx *config.ImmuTransaction) error) error {
	tx := &config.ImmuTransaction{Client: ic}
	return fn(tx)
}

// Set adds a KV operation to an ImmuTransaction.
// ImmuDB removed — this is a no-op; processing markers are derived from SQL state.
func Set(_ *config.ImmuTransaction, _ string, _ interface{}) error {
	return nil
}
