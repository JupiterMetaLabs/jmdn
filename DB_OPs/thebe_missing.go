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

// GetZKBlockByHash retrieves a ZK block by its hash string,
// including ZK proof and transaction data.
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
	blk, convErr := blockRecordToZKBlock(rec)
	if convErr != nil {
		return nil, convErr
	}
	if proof, err := h.GetZKProof(ctx, rec.BlockNumber); err == nil {
		zkProofRecordToZKBlock(proof, blk)
	}
	if txRecs, err := h.GetTransactionsByBlock(ctx, rec.BlockNumber); err == nil {
		blk.Transactions = make([]config.Transaction, 0, len(txRecs))
		for _, r := range txRecs {
			if t := txRecordToTransaction(r); t != nil {
				blk.Transactions = append(blk.Transactions, *t)
			}
		}
	}
	return blk, nil
}

// CountTransactions returns the total number of stored transactions.
func CountTransactions(_ *config.PooledConnection) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("CountTransactions: %w", err)
	}
	n, err := h.CountTransactions(ctx)
	return int64(n), err
}

// GetTransactionsPaginated returns a page of all transactions ordered by block_number DESC.
// offset and limit follow SQL semantics (offset = skip N rows).
func GetTransactionsPaginated(_ *config.PooledConnection, offset, limit int) ([]*config.Transaction, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, 0, fmt.Errorf("GetTransactionsPaginated: %w", err)
	}
	recs, err := h.GetTransactionsPaginated(ctx, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("GetTransactionsPaginated: %w", err)
	}
	total, _ := h.CountTransactions(ctx)
	txs := make([]*config.Transaction, 0, len(recs))
	for _, r := range recs {
		txs = append(txs, txRecordToConfig(r))
	}
	return txs, int(total), nil
}

// GetTransactionsByAccountPaginated returns a page of transactions for address.
func GetTransactionsByAccountPaginated(_ *config.PooledConnection, address *common.Address, offset, limit int) ([]*config.Transaction, int, error) {
	if address == nil {
		return nil, 0, fmt.Errorf("GetTransactionsByAccountPaginated: nil address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, 0, fmt.Errorf("GetTransactionsByAccountPaginated: %w", err)
	}
	recs, err := h.GetTransactionsByAddress(ctx, address.Hex(), limit+offset)
	if err != nil {
		return nil, 0, fmt.Errorf("GetTransactionsByAccountPaginated: %w", err)
	}
	if offset >= len(recs) {
		return nil, len(recs), nil
	}
	page := recs[offset:]
	txs := make([]*config.Transaction, 0, len(page))
	for _, r := range page {
		txs = append(txs, txRecordToConfig(r))
	}
	return txs, len(recs), nil
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

// BatchRestoreAccounts applies a batch of KV entries as accounts.
// entries is []struct{Key string; Value []byte} — each Value is a JSON-encoded
// Account. EVERY entry routes through mergeAccountForWrite (merge_account.go)
// — the single pure decision point for LWW ordering (unit-normalized
// timestamps), identity-field preservation, and monotonic counters (F1–F6).
func BatchRestoreAccounts(_ context.Context, _ *config.PooledConnection, entries []struct {
	Key   string
	Value []byte
}) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries cannot be empty")
	}
	for i, e := range entries {
		if e.Key == "" || e.Value == nil {
			return fmt.Errorf("invalid entry (empty key or nil value)")
		}
		var incoming Account
		if err := json.Unmarshal(e.Value, &incoming); err != nil {
			return fmt.Errorf("BatchRestoreAccounts[%d] key=%s: unmarshal: %w", i, e.Key, err)
		}
		if incoming.Address == (common.Address{}) {
			continue // skip malformed entries
		}

		// Read existing state for the merge (nil = new account).
		var existing *Account
		if cur, err := GetAccount(nil, incoming.Address); err == nil && cur != nil {
			existing = cur
		}

		merged, shouldWrite := mergeAccountForWrite(existing, incoming)
		if !shouldWrite {
			continue // existing state wins LWW — do not clobber
		}
		if err := storeAccount(nil, &merged); err != nil {
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
// Legacy stub — fn is invoked directly (no atomic KV batch).
// Callers use this to mark processed tx/block keys, which are now no-ops.
func Transaction(fn func() error) error {
	return fn()
}

// Set records a processing-marker key. No-op — processing markers are
// derived from SQL state in ThebeDB.
func Set(_ string, _ interface{}) error {
	return nil
}
