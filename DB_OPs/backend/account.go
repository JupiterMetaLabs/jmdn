package backend

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"

	"github.com/ethereum/go-ethereum/common"
)

// MODULE: DB_OPs/backend/account.go
// PURPOSE: Implement store.AccountStore by delegating to ThebeGateway (writes) and ThebeReader (reads).
// CORE DATA STRUCTURES: store.Account ↔ thebegateway.AccountRecord converters — fixed size, no growth.
// TO MODIFY BEHAVIOR: change field mapping in toAccountRecord / toStoreAccount
// DO NOT: import ImmuDB, PooledConnection, or dualdb packages
// EXTENSION POINT: add new account fields → update toAccountRecord + toStoreAccount

// CreateAccount converts store.Account → AccountRecord and writes.
// Time: O(1) — single gateway write.
func (b *thebeBackend) CreateAccount(ctx context.Context, account *store.Account) error {
	if account == nil {
		return fmt.Errorf("backend.CreateAccount: account is nil")
	}
	rec := toAccountRecord(account)
	if err := b.gw.WriteAccount(ctx, rec); err != nil {
		return fmt.Errorf("backend.CreateAccount(%s): %w", account.Address.Hex(), err)
	}
	return nil
}

// UpdateAccountBalance reads current account, updates balance, writes back.
// Time: O(1) — one read + one write.
func (b *thebeBackend) UpdateAccountBalance(ctx context.Context, address, balance string) error {
	existing, err := b.r.GetAccount(ctx, address)
	if err != nil {
		return fmt.Errorf("backend.UpdateAccountBalance(%s): read: %w", address, err)
	}
	existing.BalanceWei = balance
	existing.UpdatedAt = time.Now()
	if err := b.gw.WriteAccount(ctx, existing); err != nil {
		return fmt.Errorf("backend.UpdateAccountBalance(%s): write: %w", address, err)
	}
	return nil
}

// GetAccount retrieves an account by address.
// Time: O(1) — cache-through PK lookup.
func (b *thebeBackend) GetAccount(ctx context.Context, address string) (*store.Account, error) {
	rec, err := b.r.GetAccount(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("backend.GetAccount(%s): %w", address, err)
	}
	return toStoreAccount(rec), nil
}

// GetAccountByDID retrieves an account by DID address.
// Time: O(1) — cache-through DID index lookup.
func (b *thebeBackend) GetAccountByDID(ctx context.Context, did string) (*store.Account, error) {
	rec, err := b.r.GetAccountByDID(ctx, did)
	if err != nil {
		return nil, fmt.Errorf("backend.GetAccountByDID(%s): %w", did, err)
	}
	return toStoreAccount(rec), nil
}

// CheckNonceDuplicate returns true if the given nonce has already been used by address.
// Uses GetLatestTransactionsByAddress with limit=100 and compares nonces.
// Time: O(1) — single SQL lookup returning at most 100 rows.
func (b *thebeBackend) CheckNonceDuplicate(ctx context.Context, address string, nonce uint64) (bool, error) {
	txs, err := b.r.GetLatestTransactionsByAddress(ctx, address, 100)
	if err != nil {
		return false, fmt.Errorf("backend.CheckNonceDuplicate(%s, %d): %w", address, nonce, err)
	}
	nonceStr := strconv.FormatUint(nonce, 10)
	for _, tx := range txs {
		if tx.Nonce == nonceStr {
			return true, nil
		}
	}
	return false, nil
}

// GetLatestNonce returns the highest nonce seen for address across all stored transactions.
// Time: O(n) where n = number of recent transactions fetched (limit 100).
func (b *thebeBackend) GetLatestNonce(ctx context.Context, address string) (uint64, error) {
	txs, err := b.r.GetLatestTransactionsByAddress(ctx, address, 100)
	if err != nil {
		return 0, fmt.Errorf("backend.GetLatestNonce(%s): %w", address, err)
	}
	var latest uint64
	for _, tx := range txs {
		n, ok := new(big.Int).SetString(tx.Nonce, 10)
		if !ok {
			continue
		}
		if n.IsUint64() && n.Uint64() > latest {
			latest = n.Uint64()
		}
	}
	return latest, nil
}

// BulkGetAccounts retrieves multiple accounts by address in a single query.
// Time: O(n) where n = len(addresses) — single SQL ANY() lookup.
func (b *thebeBackend) BulkGetAccounts(ctx context.Context, addresses []string) ([]*store.Account, error) {
	recs, err := b.r.BulkGetAccounts(ctx, addresses)
	if err != nil {
		return nil, fmt.Errorf("backend.BulkGetAccounts: %w", err)
	}
	out := make([]*store.Account, len(recs))
	for i, rec := range recs {
		out[i] = toStoreAccount(rec)
	}
	return out, nil
}

// ListAccounts returns accounts ordered by creation time. limit <= 0 means no cap.
// Time: O(n) — sequential SQL scan; n = rows returned.
func (b *thebeBackend) ListAccounts(ctx context.Context, limit int) ([]*store.Account, error) {
	recs, err := b.r.ListAccounts(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("backend.ListAccounts(%d): %w", limit, err)
	}
	out := make([]*store.Account, len(recs))
	for i, rec := range recs {
		out[i] = toStoreAccount(rec)
	}
	return out, nil
}

// ListAccountsPaginated returns a page of accounts ordered by created_at ASC.
func (b *thebeBackend) ListAccountsPaginated(ctx context.Context, limit, offset int) ([]*store.Account, error) {
	recs, err := b.r.ListAccountsPaginated(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("backend.ListAccountsPaginated(%d,%d): %w", limit, offset, err)
	}
	out := make([]*store.Account, len(recs))
	for i, rec := range recs {
		out[i] = toStoreAccount(rec)
	}
	return out, nil
}

// CountAccounts returns the total number of account rows.
func (b *thebeBackend) CountAccounts(ctx context.Context) (uint64, error) {
	return b.r.CountAccounts(ctx)
}

// GetAccountsByNonces batch-fetches accounts matching any of the given nonce values.
func (b *thebeBackend) GetAccountsByNonces(ctx context.Context, nonces []uint64) ([]*store.Account, error) {
	recs, err := b.r.GetAccountsByNonces(ctx, nonces)
	if err != nil {
		return nil, fmt.Errorf("backend.GetAccountsByNonces: %w", err)
	}
	out := make([]*store.Account, len(recs))
	for i, rec := range recs {
		out[i] = toStoreAccount(rec)
	}
	return out, nil
}

// toAccountRecord converts store.Account → thebegateway.AccountRecord.
func toAccountRecord(a *store.Account) *thebegateway.AccountRecord {
	accountType := int16(0)
	if a.AccountType == "publickey" {
		accountType = 1
	}
	return &thebegateway.AccountRecord{
		Address:     a.Address.Hex(),
		DIDAddress:  a.DIDAddress,
		BalanceWei:  a.Balance,
		Nonce:       strconv.FormatUint(a.Nonce, 10),
		TxNonce:     a.TxNonce,
		TxCountSent: a.TxCountSent,
		AccountType: accountType,
		Metadata:    a.Metadata,
		// Mixed-unit safe: a.CreatedAt/UpdatedAt may be seconds (live executor)
		// or nanos (sync). Normalize so the projector LWW guard compares like
		// units (RCA §3a). Mirrors DB_OPs.normalizeUpdatedAtNanos.
		CreatedAt:   store.NormalizedUnixTime(a.CreatedAt),
		UpdatedAt:   store.NormalizedUnixTime(a.UpdatedAt),
	}
}

// toStoreAccount converts thebegateway.AccountRecord → store.Account.
func toStoreAccount(r *thebegateway.AccountRecord) *store.Account {
	nonce, _ := strconv.ParseUint(r.Nonce, 10, 64)
	accountType := "did"
	if r.AccountType == 1 {
		accountType = "publickey"
	}
	return &store.Account{
		Address:     common.HexToAddress(r.Address),
		DIDAddress:  r.DIDAddress,
		Balance:     r.BalanceWei,
		Nonce:       nonce,
		TxNonce:     r.TxNonce,
		TxCountSent: r.TxCountSent,
		AccountType: accountType,
		Metadata:    r.Metadata,
		CreatedAt:   r.CreatedAt.UnixNano(),
		UpdatedAt:   r.UpdatedAt.UnixNano(),
	}
}
