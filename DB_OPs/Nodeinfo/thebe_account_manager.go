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
	addr := common.HexToAddress(accountAddress)
	dbtxs, err := DB_OPs.GetDBTransactionsByAccount(&addr)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions by account: %w", err)
	}

	result := make([]types.DBTransaction, 0, len(dbtxs))
	for _, t := range dbtxs {
		dbtx := configTxToDBTx(t.Tx)
		dbtx.BlockNumber = t.BlockNumber
		dbtx.TxIndex = t.TxIndex
		result = append(result, dbtx)
	}
	return result, nil
}

func (am *account_manager) GetTransactionsForAccountInRange(accountAddress string, fromBlock, toBlock uint64) ([]types.DBTransaction, error) {
	addr := common.HexToAddress(accountAddress)
	dbtxs, err := DB_OPs.GetDBTransactionsByAccountInRange(&addr, fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions in range [%d..%d]: %w", fromBlock, toBlock, err)
	}

	result := make([]types.DBTransaction, 0, len(dbtxs))
	for _, t := range dbtxs {
		dbtx := configTxToDBTx(t.Tx)
		dbtx.BlockNumber = t.BlockNumber
		dbtx.TxIndex = t.TxIndex
		result = append(result, dbtx)
	}
	return result, nil
}

// Time Complexity: O(1)
func (am *account_manager) GetAccountBalance(accountAddress string) (*big.Int, uint64, error) {
	addr := common.HexToAddress(accountAddress)
	acc, err := DB_OPs.GetAccount(nil, addr)
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
	addr := common.HexToAddress(accountAddress)

	doc, err := DB_OPs.GetAccount(nil, addr)
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

	if err := DB_OPs.SaveAccount(nil, doc); err != nil {
		return fmt.Errorf("failed to write updated account: %w", err)
	}

	return nil
}

// Time Complexity: O(1)
func (am *account_manager) CreateAccount(accountAddress string, balance *big.Int, nonce uint64) error {
	addr := common.HexToAddress(accountAddress)

	meta := make(map[string]interface{})
	if err := DB_OPs.CreateAccount(nil, accountAddress, addr, meta); err != nil {
		return fmt.Errorf("failed to create account: %w", err)
	}

	doc, err := DB_OPs.GetAccount(nil, addr)
	if err != nil {
		return fmt.Errorf("failed to read back created account: %w", err)
	}

	doc.Balance = balance.String()
	// ART IDENTITY GUARD (same contract as UpdateAccountBalance above and
	// mergeAccountForWrite rule 5): nonce == 0 means the caller carried NO
	// identity information — reconciliation's IsNewAccount branch sends exactly
	// this sentinel. Writing the 0 would freeze the account in the un-healable
	// sentinel state (enrichment must special-case it; multiple 0-keyed accounts
	// collide in nonce-set lookups). Keeping the identity DB_OPs.CreateAccount
	// just minted is the better failure mode: it is unique, and the next block
	// that touches the account carries the sequencer's canonical value, which
	// adoptCarriedNonce then heals fleet-wide.
	if nonce != 0 {
		doc.Nonce = nonce
	}
	doc.UpdatedAt = time.Now().UTC().UnixNano()

	if err := DB_OPs.SaveAccount(nil, doc); err != nil {
		return fmt.Errorf("failed to write account with correct balance/nonce: %w", err)
	}

	return nil
}

// Time Complexity: O(1)
func (am *account_manager) GetAccountByAddress(accountAddress string) (*types.Account, error) {
	// Strip "address:" DB key prefix if present — the external FastSync module may pass
	// DB key format; common.HexToAddress expects bare hex (0x... or unprefixed).
	accountAddress = strings.TrimPrefix(accountAddress, DB_OPs.Prefix)

	addr := common.HexToAddress(accountAddress)
	acc, err := DB_OPs.GetAccount(nil, addr)
	if err != nil {
		if strings.Contains(err.Error(), "key not found") || strings.Contains(err.Error(), "no rows") {
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
		// Redis not available — write directly to ThebeDB synchronously.
		// Slower (~15 s/batch) but correct; no external dependency required.
		log.Printf("[accountqueue] Redis not available — writing %d accounts directly to ThebeDB", len(accounts))
		return writeAccountsDirect(accounts)
	}
	chunks := chunkCount(len(accounts))
	ctx, cancel := context.WithTimeout(context.Background(), enqueueTimeout(chunks))
	defer cancel()
	if err := enqueueRecordsChunked(ctx, s, payloadTypeAccounts, accounts); err != nil {
		// Redis is configured but unreachable (server down, connection refused, etc).
		// Fall back to direct ThebeDB write rather than dropping the accounts entirely.
		// Do NOT call EnsureActive — no point starting the worker if Redis is down.
		log.Printf("[accountqueue] Redis enqueue failed (%v) — falling back to direct ThebeDB write for %d accounts", err, len(accounts))
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
	log.Printf("[accountqueue] direct write complete: %d accounts written to ThebeDB", len(accounts))
	return nil
}

// NewAccountNonceIterator returns a cursor-based iterator over all accounts.
// Each NextBatch call advances a seekKey cursor — O(N) total scan across all batches.
func (am *account_manager) NewAccountNonceIterator(batchSize int) types.AccountNonceIterator {
	return &thebeNonceIter{
		batchSize: batchSize,
	}
}

// ─── thebeNonceIter ─────────────────────────────────────────────────────────

// MODULE: DB_OPs/Nodeinfo (thebeNonceIter)
// PURPOSE: offset-based iterator that pages all accounts from SQL (ThebeDB) in ascending created_at order.
//
// CORE DATA STRUCTURES:
//   - offset int: SQL OFFSET counter — advances by len(batch) on each NextBatch call.
//
// DO NOT:
//   - Add an in-memory account cache on this struct — 2.7M entries exhaust heap during sync.

type thebeNonceIter struct {
	batchSize int
	offset    int
	done      bool
}

// Time: O(1)
func (it *thebeNonceIter) TotalAccounts() (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return DB_OPs.CountAccountsCtx(ctx)
}

// Time: O(batchSize) SQL rows; Space: O(batchSize)
func (it *thebeNonceIter) NextBatch() ([]*types.Account, error) {
	if it.done {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	accs, err := DB_OPs.ListAccountsPaginatedCtx(ctx, it.batchSize, it.offset)
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

	it.offset += len(accs)
	if len(accs) < it.batchSize {
		it.done = true
	}
	return result, nil
}

// GetAccountsByNonces returns accounts matching any of the given nonces via ThebeDB.
// Time: O(|nonces|) SQL query; Space: O(|nonces|)
func (it *thebeNonceIter) GetAccountsByNonces(nonces []uint64) ([]*types.Account, error) {
	if len(nonces) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	accs, err := DB_OPs.GetAccountsByNonces(ctx, nonces)
	if err != nil {
		return nil, fmt.Errorf("GetAccountsByNonces: %w", err)
	}

	result := make([]*types.Account, len(accs))
	for i, acc := range accs {
		result[i] = dbOpsToTypes(acc)
	}
	return result, nil
}

func (it *thebeNonceIter) Close() {}

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
		log.Printf("[accountqueue] BatchUpdateAccounts: queue not initialized — writing %d updates directly to ThebeDB", len(updates))
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
		log.Printf("[accountqueue] Redis enqueue failed (%v) — falling back to direct ThebeDB write for %d updates", err, len(updates))
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

// BlockReconRef references one stored block whose outstanding balance effects
// reconciliation wants applied. The hash pins block identity between enqueue
// and apply.
type BlockReconRef struct {
	BlockNumber uint64
	BlockHash   string
}

// EnqueueBlockRecons ships block references to the account sync stream for
// the drain worker to apply via DB_OPs.ApplyBlockRecon. Returns the refs that
// could NOT be handed off (enqueue failure) plus the aggregated error; refs
// that were handed off will be applied exactly once regardless of retries
// (ApplyBlockRecon is a no-op for a block whose txs are all marked).
//
// Redis-unavailable fallback applies the blocks directly and synchronously —
// slower, but the same exactly-once apply path.
func EnqueueBlockRecons(refs []BlockReconRef) ([]BlockReconRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	s, mgr := getAccountQueue()
	if s == nil {
		log.Printf("[accountqueue] EnqueueBlockRecons: queue not initialized — applying %d blocks directly", len(refs))
		return applyBlockReconsDirect(refs)
	}

	now := time.Now().UTC().Unix()
	wires := make([]blockReconWire, len(refs))
	for i, r := range refs {
		wires[i] = blockReconWire{BlockNumber: r.BlockNumber, BlockHash: r.BlockHash, EnqueuedAt: now}
	}

	// Chunked enqueue with per-chunk failure tracking so callers know exactly
	// which blocks must be retried on the next reconciliation run.
	var failed []BlockReconRef
	var errs []error
	chunks := chunkCount(len(wires))
	ctx, cancel := context.WithTimeout(context.Background(), enqueueTimeout(chunks))
	defer cancel()
	for start := 0; start < len(wires); start += maxRecordsPerMessage {
		end := start + maxRecordsPerMessage
		if end > len(wires) {
			end = len(wires)
		}
		data, err := json.Marshal(wires[start:end])
		if err != nil {
			errs = append(errs, fmt.Errorf("marshal block recon chunk [%d:%d]: %w", start, end, err))
			failed = append(failed, refs[start:end]...)
			continue
		}
		id, err := s.Enqueue(ctx, accountSyncStream, map[string]any{
			"type": string(payloadTypeBlockRecon),
			"data": string(data),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("enqueue block recon chunk [%d:%d]: %w", start, end, err))
			failed = append(failed, refs[start:end]...)
			continue
		}
		noteEnqueuedID(id)
	}
	if len(errs) == 0 {
		mgr.EnsureActive()
		return nil, nil
	}
	mgr.EnsureActive()
	return failed, errors.Join(errs...)
}

// applyBlockReconsDirect is the Redis-unavailable fallback: apply each block
// synchronously through the same exactly-once path the worker uses.
func applyBlockReconsDirect(refs []BlockReconRef) ([]BlockReconRef, error) {
	var failed []BlockReconRef
	var errs []error
	for _, r := range refs {
		if _, err := DB_OPs.ApplyBlockRecon(nil, r.BlockNumber, r.BlockHash); err != nil {
			failed = append(failed, r)
			errs = append(errs, fmt.Errorf("block %d: %w", r.BlockNumber, err))
		}
	}
	return failed, errors.Join(errs...)
}

// writeTxMarkersDirect is the Redis-unavailable fallback for marker writes.
func writeTxMarkersDirect(txHashes []string, appliedAt int64) error {
	markers := make(map[string]int64, len(txHashes))
	for _, h := range txHashes {
		markers[h] = appliedAt
	}
	return DB_OPs.WriteTxProcessedMarkers(nil, markers)
}

// batchUpdateAccountsDirect writes account balance updates synchronously to ThebeDB,
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
// DB-specific fields (BlockNumber, TxIndex) are set by callers from DB_OPs.DBTx metadata.
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
