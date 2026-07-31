package NodeInfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
)

type account_manager struct{}

// ─── Bounded enqueue (producer side) ──────────────────────────────────────────
//
// The library's AccountSync receive path (sync_protocols.go HandleAccountsSyncData)
// accumulates every page of a sync session and calls WriteAccounts ONCE at EOF with
// the whole batch — potentially millions of records. Packing that into a single XADD
// risks exceeding Redis proto-max-bulk-len (512 MiB) and stalls/fails the enqueue; a
// failed enqueue at EOF (after all pages were ACKed) collapses the session and drives
// the dispatcher into a retry→dead-letter storm. We split into fixed-size messages so
// every XADD is small and fast, and the worker's per-drain memory stays bounded.

// maxRecordsPerMessage caps how many account/update records are packed into one Redis
// stream message (one XADD). 500 mirrors AccountSyncWorkerConfig.MaxAccountsPerBatch so
// a single message maps to roughly one ImmuDB sub-batch; at ~300 B/record a message is
// ~150 KB — three orders of magnitude under Redis's 512 MiB bulk limit.
const maxRecordsPerMessage = 500

// enqueueTimeout scales the enqueue deadline with chunk count: a 10 s base plus 5 ms per
// chunk covers large syncs (e.g. 2000 chunks → ~20 s) without an unbounded wait. The
// server is not blocked on this enqueue (pages were already ACKed), so a generous,
// bounded budget is safe.
//
// Time: O(1)
func enqueueTimeout(chunks int) time.Duration {
	return 10*time.Second + time.Duration(chunks)*5*time.Millisecond
}

// enqueueRecordsChunked splits items into chunks of at most maxRecordsPerMessage,
// marshals each chunk to JSON, and XADDs it to the account sync stream tagged ptype.
// Best-effort: every chunk is attempted and errors are aggregated (errors.Join), so a
// single transient XADD failure does not drop the remaining chunks. Any chunk that
// fails to enqueue is backfilled by the worker's LWW write on a later sync /
// reconciliation — strictly safer than the previous all-or-nothing single message.
//
// Time: O(N) marshal + O(ceil(N/maxRecordsPerMessage)) XADD round trips, N = len(items).
// Space: O(maxRecordsPerMessage) per message — never the whole batch at once.
// DS: input []T re-sliced in place into fixed-size windows; no intermediate copy.
func enqueueRecordsChunked[T any](ctx context.Context, s RedisStreamer, ptype syncPayloadType, items []T) error {
	var errs []error
	for start := 0; start < len(items); start += maxRecordsPerMessage {
		end := start + maxRecordsPerMessage
		if end > len(items) {
			end = len(items)
		}
		data, err := json.Marshal(items[start:end])
		if err != nil {
			errs = append(errs, fmt.Errorf("marshal chunk [%d:%d]: %w", start, end, err))
			continue
		}
		id, err := s.Enqueue(ctx, accountSyncStream, map[string]any{
			"type": string(ptype),
			"data": string(data),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("enqueue chunk [%d:%d]: %w", start, end, err))
			continue
		}
		// Record the XADD ID so reconciliation can wait for drain
		// confirmation before advancing the anchor.
		noteEnqueuedID(id)
	}
	return errors.Join(errs...)
}

// chunkCount returns the number of messages len(n) records split into maxRecordsPerMessage.
// Time: O(1)
func chunkCount(n int) int {
	return (n + maxRecordsPerMessage - 1) / maxRecordsPerMessage
}

// Time Complexity: O(N) where N is the total number of transactions scanned or retrieved
func (am *account_manager) GetTransactionsForAccount(accountAddress string) ([]types.DBTransaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

func (am *account_manager) GetTransactionsForAccountInRange(accountAddress string, fromBlock, toBlock uint64) ([]types.DBTransaction, error) {
	conn, err := DB_OPs.GetMainDBConnectionandPutBack(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get main DB connection: %w", err)
	}

	addr := common.HexToAddress(accountAddress)
	cfgTxs, err := DB_OPs.GetTransactionsByAccountInRange(conn, &addr, fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions in range [%d..%d]: %w", fromBlock, toBlock, err)
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
	// ART IDENTITY GUARD (mirrors mergeAccountForWrite rule 5 for this DIRECT
	// write path): nonce here is the account's identity nonce — the Fastsync
	// AccountSync set key — and 0 means the caller carried no identity
	// information (e.g. reconciliation of a receiver-only account). Never zero
	// a stored identity; a non-zero value adopts (heals to the caller's
	// canonical identity).
	if nonce != 0 {
		doc.Nonce = nonce
	}
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

// WriteAccounts enqueues accounts to the Redis stream for async DB write, split into
// fixed-size messages of at most maxRecordsPerMessage (see enqueueRecordsChunked).
// Returns immediately after the enqueue — the caller gets an ACK without waiting for
// the ImmuDB commit (which can take up to 15 s under load).
//
// The library hands this the entire end-of-stream batch (up to millions of accounts);
// chunking keeps each XADD small so it never exceeds Redis's bulk-string limit and the
// enqueue cannot fail the whole session. Enqueue is best-effort across chunks: a
// partial failure returns an aggregated error but does not drop successful chunks; the
// worker's LWW write backfills the rest on a later sync.
//
// StartAccountSyncWorker must be called before WriteAccounts or this returns an error.
// At-least-once delivery is guaranteed by the worker via PEL + XAUTOCLAIM.
//
// Time: O(N) serialization + O(ceil(N/maxRecordsPerMessage)) XADD round trips, N = len(accounts).
func (am *account_manager) WriteAccounts(accounts []*types.Account) error {
	if len(accounts) == 0 {
		return nil
	}
	s, mgr := getAccountQueue()
	if s == nil {
		// Redis not available — write directly to ImmuDB synchronously.
		// Slower (~15 s/batch) but correct; no external dependency required.
		log.Printf("[accountqueue] Redis not available — writing %d accounts directly to ImmuDB", len(accounts))
		return writeAccountsDirect(accounts)
	}
	chunks := chunkCount(len(accounts))
	ctx, cancel := context.WithTimeout(context.Background(), enqueueTimeout(chunks))
	defer cancel()
	if err := enqueueRecordsChunked(ctx, s, payloadTypeAccounts, accounts); err != nil {
		// Redis is configured but unreachable (server down, connection refused, etc).
		// Fall back to direct ImmuDB write rather than dropping the accounts entirely.
		// Do NOT call EnsureActive — no point starting the worker if Redis is down.
		log.Printf("[accountqueue] Redis enqueue failed (%v) — falling back to direct ImmuDB write for %d accounts", err, len(accounts))
		return writeAccountsDirect(accounts)
	}
	// Enqueue succeeded — ensure the drain worker is running to process it.
	mgr.EnsureActive()
	return nil
}

// writeAccountsDirect writes accounts synchronously to ImmuDB without going through Redis.
// Used when the Redis queue is not configured. Uses the same dbEntry/BatchRestoreAccounts
// path as the worker so the write is LWW-idempotent.
func writeAccountsDirect(accounts []*types.Account) error {
	entries := make([]dbEntry, 0, len(accounts)*2)
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		dbAcc := &DB_OPs.Account{
			DIDAddress:  acc.DIDAddress,
			Address:     acc.Address,
			Balance:     acc.Balance,
			Nonce:       acc.Nonce,
			TxNonce:     acc.TxNonce,
			TxCountSent: acc.TxCountSent,
			AccountType: acc.AccountType,
			CreatedAt:   acc.CreatedAt,
			UpdatedAt:   acc.UpdatedAt,
			Metadata:    acc.Metadata,
		}
		val, err := json.Marshal(dbAcc)
		if err != nil {
			return fmt.Errorf("writeAccountsDirect: marshal %s: %w", acc.Address.Hex(), err)
		}
		entries = append(entries, dbEntry{Key: DB_OPs.Prefix + acc.Address.Hex(), Value: val})
		if acc.DIDAddress != "" {
			entries = append(entries, dbEntry{Key: DB_OPs.DIDPrefix + acc.DIDAddress, Value: val})
		}
	}

	const batchSize = 500
	// Generous timeout: 60 s base + 2 s per batch to cover ImmuDB commit latency.
	timeout := 60*time.Second + time.Duration(len(entries)/batchSize+1)*2*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := DB_OPs.GetAccountsConnections(ctx)
	if err != nil {
		return fmt.Errorf("writeAccountsDirect: get connection: %w", err)
	}
	defer DB_OPs.PutAccountsConnection(conn)

	for i := 0; i < len(entries); i += batchSize {
		end := i + batchSize
		if end > len(entries) {
			end = len(entries)
		}
		if err := DB_OPs.BatchRestoreAccounts(ctx, conn, entries[i:end]); err != nil {
			return fmt.Errorf("writeAccountsDirect: batch [%d:%d]: %w", i, end, err)
		}
	}
	log.Printf("[accountqueue] direct write complete: %d accounts written to ImmuDB", len(accounts))
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
		Nonce:       acc.Nonce,
		TxNonce:     acc.TxNonce,
		TxCountSent: acc.TxCountSent,
		AccountType: acc.AccountType,
		CreatedAt:   acc.CreatedAt,
		UpdatedAt:   acc.UpdatedAt,
		Metadata:    acc.Metadata,
	}
}

// BatchUpdateAccounts enqueues account balance/nonce updates to the Redis stream for
// async DB write, split into fixed-size messages of at most maxRecordsPerMessage.
// Returns immediately after the enqueue. Best-effort across chunks (see WriteAccounts).
//
// StartAccountSyncWorker must be called before BatchUpdateAccounts or this returns an error.
// At-least-once delivery is guaranteed by the worker via PEL + XAUTOCLAIM.
//
// Time: O(N) serialization + O(ceil(N/maxRecordsPerMessage)) XADD round trips, N = len(updates).
func (am *account_manager) BatchUpdateAccounts(updates []types.AccountUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	s, mgr := getAccountQueue()
	if s == nil {
		log.Printf("[accountqueue] BatchUpdateAccounts: queue not initialized — writing %d updates directly to ImmuDB", len(updates))
		return batchUpdateAccountsDirect(am, updates)
	}

	// Convert to wire type for stable JSON serialization.
	// big.Int.String() produces a decimal string; accountUpdateWire makes the format explicit.
	// UpdatedAt is stamped HERE (producer side, at update-computation time) and carried
	// through the stream — it is the LWW ordering key. The drain worker must never
	// re-stamp it, or replayed stale entries would overwrite newer data.
	now := time.Now().UTC().UnixNano()
	wires := make([]accountUpdateWire, len(updates))
	for i, u := range updates {
		wires[i] = accountUpdateWire{
			Address:     u.Address,
			NewBalance:  u.NewBalance.String(),
			Nonce:       u.Nonce,
			TxNonce:     u.TxNonce,
			TxCountSent: u.TxCountSent,
			UpdatedAt:   now,
		}
	}

	chunks := chunkCount(len(wires))
	ctx, cancel := context.WithTimeout(context.Background(), enqueueTimeout(chunks))
	defer cancel()
	if err := enqueueRecordsChunked(ctx, s, payloadTypeUpdates, wires); err != nil {
		log.Printf("[accountqueue] Redis enqueue failed (%v) — falling back to direct ImmuDB write for %d updates", err, len(updates))
		return batchUpdateAccountsDirect(am, updates)
	}
	mgr.EnsureActive()
	return nil
}

// EnqueueAppliedTxMarkers enqueues tx_processed markers for RECON-applied txs.
// MUST be called AFTER the corresponding BatchUpdateAccounts: stream
// FIFO + the drain's markers-last ordering then guarantee markers never commit
// before the balances they describe.
//
// appliedAt is the producer-side Unix-seconds marker value (must be > 0).
// Redis-unavailable fallback: writes markers directly — safe, because in that
// mode BatchUpdateAccounts also wrote its balances directly and synchronously,
// so the ordering guarantee holds degenerately.
func EnqueueAppliedTxMarkers(txHashes []string, appliedAt int64) error {
	if len(txHashes) == 0 {
		return nil
	}
	if appliedAt <= 0 {
		return fmt.Errorf("EnqueueAppliedTxMarkers: applied_at must be positive, got %d", appliedAt)
	}

	wires := make([]txMarkerWire, len(txHashes))
	for i, h := range txHashes {
		wires[i] = txMarkerWire{Hash: h, AppliedAt: appliedAt}
	}

	s, mgr := getAccountQueue()
	if s == nil {
		log.Printf("[accountqueue] EnqueueAppliedTxMarkers: queue not initialized — writing %d markers directly", len(txHashes))
		return writeTxMarkersDirect(txHashes, appliedAt)
	}

	chunks := chunkCount(len(wires))
	ctx, cancel := context.WithTimeout(context.Background(), enqueueTimeout(chunks))
	defer cancel()
	if err := enqueueRecordsChunked(ctx, s, payloadTypeTxMarkers, wires); err != nil {
		log.Printf("[accountqueue] Redis enqueue failed (%v) — writing %d tx markers directly", err, len(txHashes))
		return writeTxMarkersDirect(txHashes, appliedAt)
	}
	mgr.EnsureActive()
	return nil
}

// writeTxMarkersDirect is the Redis-unavailable fallback for marker writes.
func writeTxMarkersDirect(txHashes []string, appliedAt int64) error {
	markers := make(map[string]int64, len(txHashes))
	for _, h := range txHashes {
		markers[h] = appliedAt
	}
	return DB_OPs.WriteTxProcessedMarkers(nil, markers)
}

// batchUpdateAccountsDirect writes account balance updates synchronously to ImmuDB,
// bypassing Redis. Used when Redis is unavailable.
func batchUpdateAccountsDirect(am *account_manager, updates []types.AccountUpdate) error {
	for _, u := range updates {
		if u.IsNewAccount {
			if err := am.CreateAccount(u.Address, u.NewBalance, u.Nonce); err != nil {
				return fmt.Errorf("batchUpdateAccountsDirect: create %s: %w", u.Address, err)
			}
		} else {
			if err := am.UpdateAccountBalance(u.Address, u.NewBalance, u.Nonce); err != nil {
				return fmt.Errorf("batchUpdateAccountsDirect: update %s: %w", u.Address, err)
			}
		}
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
