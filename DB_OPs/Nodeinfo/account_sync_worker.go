// MODULE: DB_OPs/Nodeinfo/account_sync_worker
// PURPOSE: Drain the accountsync Redis stream and write account batches to ImmuDB.
//          Owns the at-least-once delivery contract: ACK only after successful DB write.
//
// CORE DATA STRUCTURES:
//   - []StreamEntry: ephemeral per runWorker iteration.
//     Bounded by AccountSyncWorkerConfig.MaxDrainItems (default 100).
//   - []dbEntry: ephemeral per processBatch call.
//     Bounded by MaxDrainItems × maxRecordsPerMessage (producer caps each message at
//     maxRecordsPerMessage records — see immudb_account_manager.go). DID refs may add
//     up to one extra entry per account.
//     Sub-batched into chunks of MaxAccountsPerBatch before each BatchRestoreAccounts call.
//   - PEL (Redis-side, not in-process): unacked entries in flight.
//     Evicted by AutoClaim after PendingIdleTimeout; no in-process growth.
//
// TO MODIFY BEHAVIOR:
//   - Tuning (batch size, timeouts): change AccountSyncWorkerConfig fields — no code change.
//   - Add new payload type: add case in processBatch switch + enqueue helper in
//     immudb_account_manager.go. This file changes only at the switch statement.
//   - Change DB write path: edit processBatch — impacts ACK semantics and batch split.
//
// DO NOT:
//   - Start this worker from a constructor. StartAccountSyncWorker is the only entry point.
//   - ACK entries before BatchRestoreAccounts succeeds — breaks at-least-once guarantee.
//   - Acquire the DB connection via GetAccountConnectionandPutBack — its auto-return
//     goroutine fires on the scoped ctx deadline and can recycle the connection mid-write
//     (data race). Use GetAccountsConnections + defer PutAccountsConnection, and thread the
//     scoped writeCtx into BatchRestoreAccounts so the deadline bounds the DB ops directly.
//   - Replace []dbEntry with a map — sequential append + slice-of-chunks is the right
//     access pattern for BatchRestoreAccounts (ordered, fixed-size sub-batches).
//
// EXTENSION POINT: new payload types → add case in processBatch switch; add parse helper.
//
// CHANGE SCENARIOS:
//   Add payload type:   add case in processBatch switch + parse helper + enqueue in account_manager
//   Change batch limits: edit DefaultWorkerConfig or pass custom AccountSyncWorkerConfig
//   Change DB write:    edit processBatch; ACK block is the only invariant that must not move

package NodeInfo

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"sync/atomic"
	"time"

	"gossipnode/DB_OPs"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
)

// ─── dbEntry type alias ───────────────────────────────────────────────────────

// dbEntry is a type alias for the anonymous struct expected by DB_OPs.BatchRestoreAccounts.
// Using a type alias (=) ensures []dbEntry is assignment-compatible with the parameter type
// without a conversion loop. Access pattern: sequential append, read-once for sub-batching.
// Growth bound: MaxDrainItems × avg-accounts-per-payload (ephemeral per processBatch call).
type dbEntry = struct {
	Key   string
	Value []byte
}

// ─── Wire type for BatchUpdateAccounts payloads ───────────────────────────────

// accountUpdateWire is the stable JSON representation of types.AccountUpdate used
// in the stream payload. Explicit wire type prevents big.Int JSON serialization
// surprises (math/big.Int marshals as a quoted decimal string, but that behaviour
// is implementation-defined and not guaranteed across versions).
//
// Stored in the stream as: {"address":"0x...","new_balance":"1000000","nonce":42,"tx_nonce":43,"tx_count_sent":5}
type accountUpdateWire struct {
	Address     string `json:"address"`
	NewBalance  string `json:"new_balance"` // decimal string from big.Int.String()
	Nonce       uint64 `json:"nonce"`
	TxNonce     uint64 `json:"tx_nonce"`
	TxCountSent uint64 `json:"tx_count_sent"`
}

// ─── Configuration ────────────────────────────────────────────────────────────

// AccountSyncWorkerConfig holds tuning parameters for the account sync worker.
// All fields have safe production defaults; use DefaultWorkerConfig() to get them.
type AccountSyncWorkerConfig struct {
	// MaxDrainItems is the maximum number of stream entries read per XREADGROUP call.
	// Higher values coalesce more work per ImmuDB commit but increase per-batch memory.
	// Default: 100.
	MaxDrainItems int64

	// MaxAccountsPerBatch is the maximum number of accounts per single BatchRestoreAccounts call.
	// Prevents oversized ImmuDB writes. If a coalesced batch exceeds this, it is split into chunks.
	// Default: 500.
	MaxAccountsPerBatch int

	// BlockTimeout is the XREADGROUP BLOCK duration.
	// The worker goroutine sleeps inside Redis until data arrives or this duration elapses.
	// Must be short enough to allow clean ctx cancellation. Default: 5s.
	BlockTimeout time.Duration

	// PendingIdleTimeout is the minimum idle duration before XAUTOCLAIM reclaims a PEL entry.
	// Entries stuck in the PEL longer than this (due to worker crash/restart) are replayed.
	// Must exceed the worst-case BatchRestoreAccounts latency to avoid spurious reclaims.
	// Default: 30s.
	PendingIdleTimeout time.Duration

	// DBWriteTimeout bounds each GetAccountConnectionandPutBack + BatchRestoreAccounts call.
	// Must exceed the observed worst-case ImmuDB commit latency (~15 s). Default: 60s.
	DBWriteTimeout time.Duration
}

// DefaultWorkerConfig returns production-tuned defaults.
// Time: O(1)
func DefaultWorkerConfig() AccountSyncWorkerConfig {
	return AccountSyncWorkerConfig{
		MaxDrainItems:       100,
		MaxAccountsPerBatch: 500,
		BlockTimeout:        30 * time.Second,
		PendingIdleTimeout:  30 * time.Second,
		DBWriteTimeout:      60 * time.Second,
	}
}

// ─── WorkerManager — atomic lifecycle ────────────────────────────────────────

// WorkerManager manages the drain goroutine lifecycle with lock-free atomics.
// The worker starts lazily on the first WriteAccounts call and shuts down after
// BlockTimeout of idle time. Producers restart it automatically via EnsureActive.
type WorkerManager struct {
	isOnline      atomic.Bool  // true = drain goroutine is running
	resetInflight atomic.Bool  // true = a lastActivity-reset goroutine is in flight
	lastActivity  atomic.Int64 // UnixNano — last successful commit or explicit reset

	streamer RedisStreamer
	cfg      AccountSyncWorkerConfig
}

// EnsureActive is called by WriteAccounts before every XADD.
// If the worker is offline it wins a CAS to start it; if it is near its idle
// deadline it wins a CAS to extend lastActivity. Always returns immediately.
// Hot-path cost (online + healthy): two atomic loads + subtract + compare ≈ single-digit ns.
func (wm *WorkerManager) EnsureActive() {
	if !wm.isOnline.Load() {
		if wm.isOnline.CompareAndSwap(false, true) {
			wm.lastActivity.Store(time.Now().UnixNano())
			log.Printf("[accountqueue] worker offline — restarting")
			go wm.runWorker()
		}
		// CAS loss = another caller already claimed the spawn; worker is starting.
		return
	}

	// Online — check remaining idle budget. Refresh if under 50%.
	elapsed := time.Since(time.Unix(0, wm.lastActivity.Load()))
	if wm.cfg.BlockTimeout-elapsed < wm.cfg.BlockTimeout/2 {
		if wm.resetInflight.CompareAndSwap(false, true) {
			go func() {
				defer wm.resetInflight.Store(false)
				wm.lastActivity.Store(time.Now().UnixNano())
			}()
		}
	}
}

// ─── Lifecycle ────────────────────────────────────────────────────────────────

// StartAccountSyncWorker creates a WorkerManager, installs it as the package-level
// queue, and returns. The drain goroutine starts lazily on the first WriteAccounts call.
//
// MUST be called exactly once from main.go before any WriteAccounts or BatchUpdateAccounts.
// If not called, both methods log an error and skip the enqueue (no write occurs).
//
// Time: O(1) — no Redis round trip; EnsureConsumerGroup is deferred to the first runWorker call.
func StartAccountSyncWorker(logger_ctx context.Context, streamer RedisStreamer, cfg AccountSyncWorkerConfig) *WorkerManager {
	m := &WorkerManager{streamer: streamer, cfg: cfg}
	InstallAccountQueue(streamer, m)

	// Eagerly verify Redis connection in the background.
	// We do this to ensure we get a reliable success/failure log on boot,
	// because the actual worker loop is lazy and might not start if the node is already synced.
	go func() {
		ctx, cancel := context.WithTimeout(logger_ctx, 5*time.Second)
		defer cancel()
		if err := streamer.Ping(ctx); err != nil {
			log.Printf("[accountqueue] WARN: Boot-time Redis ping failed: %v. This is a one-off diagnostic, not a live health gate — Enqueue calls will fall back to direct DB writes on their own if Redis is still unreachable when they run.", err)
		} else {
			log.Printf("[accountqueue] Boot-time Redis ping succeeded — connected and authenticated. (Diagnostic only; does not guarantee later Enqueue calls will succeed.)")
		}
	}()

	return m
}

// ─── Worker loop ─────────────────────────────────────────────────────────────

// runWorker is the drain loop running as a method on WorkerManager.
// It exits when BlockTimeout elapses with no data AND lastActivity is stale.
// defer sets isOnline=false so even a panic marks the worker offline.
func (wm *WorkerManager) runWorker() {
	defer wm.isOnline.Store(false)
	log.Printf("[accountqueue] worker started (stream=%s group=%s consumer=%s)",
		accountSyncStream, accountSyncGroup, accountSyncConsumer)
	defer log.Printf("[accountqueue] worker stopped")

	if err := wm.streamer.EnsureConsumerGroup(context.Background(), accountSyncStream, accountSyncGroup); err != nil {
		log.Printf("[accountqueue] ERROR: EnsureConsumerGroup: %v — worker exiting", err)
		return
	}

	// Reclaim any entries left unACKed by a prior worker run.
	if err := reclaimPending(wm.streamer, wm.cfg); err != nil {
		log.Printf("[accountqueue] WARN: startup reclaimPending error: %v", err)
	}

	for {
		entries, err := wm.streamer.ReadGroup(
			context.Background(),
			accountSyncStream, accountSyncGroup, accountSyncConsumer,
			wm.cfg.MaxDrainItems,
			wm.cfg.BlockTimeout,
		)
		if err != nil {
			log.Printf("[accountqueue] ReadGroup error: %v — retrying in 1s", err)
			time.Sleep(time.Second)
			continue
		}
		if entries == nil {
			// BlockTimeout elapsed with no data — check idle window.
			if time.Since(time.Unix(0, wm.lastActivity.Load())) >= wm.cfg.BlockTimeout {
				log.Printf("[accountqueue] worker idle for %s — going offline", wm.cfg.BlockTimeout)
				return
			}
			// lastActivity was refreshed by a concurrent EnsureActive reset; keep going.
			continue
		}

		if err := processBatch(wm.streamer, entries, wm.cfg); err != nil {
			// Do NOT ACK. Entries remain in PEL and are replayed by reclaimPending on next start.
			// BatchRestoreAccounts is LWW-idempotent — replays are safe.
			log.Printf("[accountqueue] processBatch error: %v — %d entries remain in PEL for retry",
				err, len(entries))
		} else {
			wm.lastActivity.Store(time.Now().UnixNano())
		}
	}
}

// reclaimPending reclaims and processes all PEL entries whose idle time exceeds
// cfg.PendingIdleTimeout. Called once on worker startup to replay entries left
// unACKed by a previous crash.
//
// Iterates via cursor until the full PEL is scanned ("0-0" returned as next cursor).
// Each DB op uses context.Background() with cfg.DBWriteTimeout — no external cancellation.
//
// Time: O(PEL size / MaxDrainItems) XAUTOCLAIM round trips + processBatch cost per page.
func reclaimPending(s RedisStreamer, cfg AccountSyncWorkerConfig) error {
	cursor := "0-0"
	for {
		entries, next, err := s.AutoClaim(
			context.Background(),
			accountSyncStream, accountSyncGroup, accountSyncConsumer,
			cfg.PendingIdleTimeout,
			cursor,
			cfg.MaxDrainItems,
		)
		if err != nil {
			return fmt.Errorf("XAUTOCLAIM cursor=%s: %w", cursor, err)
		}

		if len(entries) > 0 {
			log.Printf("[accountqueue] reclaiming %d pending entries (cursor=%s)", len(entries), cursor)
			if err := processBatch(s, entries, cfg); err != nil {
				return fmt.Errorf("process reclaimed entries at cursor=%s: %w", cursor, err)
			}
		}

		// "0-0" means the full PEL was scanned — no more pending entries.
		if next == "0-0" || next == "" {
			break
		}
		cursor = next
	}
	return nil
}

// ─── Batch processor ─────────────────────────────────────────────────────────

// processBatch deserializes all stream entries, merges their accounts into a flat
// list, writes to ImmuDB in sub-batches of MaxAccountsPerBatch, and ACKs all
// entries only after every sub-batch succeeds.
//
// Poison pill handling: entries with undecodable payloads (parse error or unknown type)
// are ACKed immediately and discarded. They will never succeed and must not block the queue.
//
// At-least-once guarantee:
//   - goodIDs are ACKed only after BatchRestoreAccounts succeeds for all chunks.
//   - If any chunk fails, goodIDs are not ACKed → entries stay in PEL → replayed on restart.
//   - Replay safety: BatchRestoreAccounts uses LWW (UpdatedAt timestamp) — duplicate writes
//     overwrite with the same data and do not corrupt state.
//
// Time: O(N/MaxAccountsPerBatch) BatchRestoreAccounts round trips, where N = total accounts.
// Space: O(N) — ephemeral []dbEntry freed after ACK.
func processBatch(s RedisStreamer, entries []StreamEntry, cfg AccountSyncWorkerConfig) error {
	var (
		writeEntries []dbEntry // accounts to persist to ImmuDB
		goodIDs      []string  // stream IDs to ACK+XDEL after successful DB write
		poisonIDs    []string  // stream IDs to ACK+XDEL immediately (unrecoverable)
	)

	for _, entry := range entries {
		payloadType, _ := entry.Values["type"].(string)
		dataStr, _ := entry.Values["data"].(string)

		switch syncPayloadType(payloadType) {
		case payloadTypeAccounts:
			parsed, err := parseAccountsPayload(dataStr)
			if err != nil {
				log.Printf("[accountqueue] WARN: poison pill — undecodable accounts entry %s: %v", entry.ID, err)
				poisonIDs = append(poisonIDs, entry.ID)
				continue
			}
			writeEntries = append(writeEntries, parsed...)
			goodIDs = append(goodIDs, entry.ID)

		case payloadTypeUpdates:
			parsed, err := parseUpdatesPayload(dataStr)
			if err != nil {
				log.Printf("[accountqueue] WARN: poison pill — undecodable updates entry %s: %v", entry.ID, err)
				poisonIDs = append(poisonIDs, entry.ID)
				continue
			}
			writeEntries = append(writeEntries, parsed...)
			goodIDs = append(goodIDs, entry.ID)

		default:
			log.Printf("[accountqueue] WARN: poison pill — unknown payload type %q in entry %s", payloadType, entry.ID)
			poisonIDs = append(poisonIDs, entry.ID)
		}
	}

	// ACK + XDEL poison pills immediately — unrecoverable, must not block the PEL.
	if len(poisonIDs) > 0 {
		ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.Ack(ackCtx, accountSyncStream, accountSyncGroup, poisonIDs...); err != nil {
			log.Printf("[accountqueue] WARN: failed to ACK %d poison pills: %v", len(poisonIDs), err)
		} else if err := s.Delete(ackCtx, accountSyncStream, poisonIDs...); err != nil {
			log.Printf("[accountqueue] WARN: failed to XDEL %d poison pills: %v", len(poisonIDs), err)
		}
		cancel()
	}

	if len(writeEntries) == 0 {
		return nil
	}

	// Scope a timeout to this DB write. writeCtx bounds connection acquisition AND
	// (threaded into BatchRestoreAccounts) every GetAll/ExecAll inside the write.
	writeCtx, writeCancel := context.WithTimeout(context.Background(), cfg.DBWriteTimeout)
	defer writeCancel()

	// Acquire explicitly and return on processBatch exit — NOT via
	// GetAccountConnectionandPutBack. That helper's auto-return goroutine fires when
	// writeCtx hits its deadline, which can recycle the connection back into the pool
	// while a multi-chunk BatchRestoreAccounts is still issuing gRPC on it (data race).
	conn, err := DB_OPs.GetAccountsConnections(writeCtx)
	if err != nil {
		return fmt.Errorf("get account DB connection: %w", err)
	}
	defer DB_OPs.PutAccountsConnection(conn)

	// Write in sub-batches to bound individual ImmuDB commit size.
	// All chunks must succeed before any ACK is issued.
	start := time.Now()
	for i := 0; i < len(writeEntries); i += cfg.MaxAccountsPerBatch {
		end := i + cfg.MaxAccountsPerBatch
		if end > len(writeEntries) {
			end = len(writeEntries)
		}
		if err := DB_OPs.BatchRestoreAccounts(writeCtx, conn, writeEntries[i:end]); err != nil {
			return fmt.Errorf("BatchRestoreAccounts chunk [%d:%d] of %d: %w", i, end, len(writeEntries), err)
		}
	}
	commitDur := time.Since(start)

	// All sub-batches succeeded — ACK + XDEL in one pipeline round-trip.
	// XACK removes entries from the PEL; XDEL removes the payload from the stream body.
	// Without XDEL, ACKed entries accumulate in the stream indefinitely.
	// Replay safety: BatchRestoreAccounts is LWW-idempotent if ACK fails and entries replay.
	ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ackCancel()
	if err := s.Ack(ackCtx, accountSyncStream, accountSyncGroup, goodIDs...); err != nil {
		log.Printf("[accountqueue] WARN: ACK failed for %d entries after successful DB write: %v — will be reclaimed and re-written (safe, LWW)", len(goodIDs), err)
	} else if err := s.Delete(ackCtx, accountSyncStream, goodIDs...); err != nil {
		log.Printf("[accountqueue] WARN: XDEL failed for %d entries after ACK: %v", len(goodIDs), err)
	} else {
		log.Printf("[accountqueue] wrote %d accounts from %d entries in %s; ACKed + XDELed",
			len(writeEntries), len(goodIDs), commitDur.Round(time.Millisecond))
	}

	return nil
}

// ─── Payload parsers ─────────────────────────────────────────────────────────

// parseAccountsPayload deserializes a payloadTypeAccounts JSON blob into a flat
// list of DB write entries ready for BatchRestoreAccounts.
//
// Time: O(N) where N = number of accounts in the payload.
// Space: O(N) — one dbEntry per account.
func parseAccountsPayload(dataStr string) ([]dbEntry, error) {
	var accs []*types.Account
	if err := json.Unmarshal([]byte(dataStr), &accs); err != nil {
		return nil, fmt.Errorf("unmarshal []*types.Account: %w", err)
	}

	// We might emit up to 2 entries per account (address: and did:)
	entries := make([]dbEntry, 0, len(accs)*2)
	for _, acc := range accs {
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
			return nil, fmt.Errorf("marshal DB_OPs.Account for address %s: %w", acc.Address.Hex(), err)
		}

		// 1. Emit the primary address key
		entries = append(entries, dbEntry{
			Key:   DB_OPs.Prefix + acc.Address.Hex(),
			Value: val,
		})

		// 2. Emit the DID key so BatchRestoreAccounts creates the bound reference
		if acc.DIDAddress != "" {
			entries = append(entries, dbEntry{
				Key:   DB_OPs.DIDPrefix + acc.DIDAddress,
				Value: val,
			})
		}
	}
	return entries, nil
}

// parseUpdatesPayload deserializes a payloadTypeUpdates JSON blob into a flat list
// of DB write entries ready for BatchRestoreAccounts.
// Reads accountUpdateWire (not types.AccountUpdate) to avoid big.Int JSON ambiguity.
//
// Time: O(N) where N = number of updates in the payload.
// Space: O(N) — one dbEntry per update.
func parseUpdatesPayload(dataStr string) ([]dbEntry, error) {
	var wires []accountUpdateWire
	if err := json.Unmarshal([]byte(dataStr), &wires); err != nil {
		return nil, fmt.Errorf("unmarshal []accountUpdateWire: %w", err)
	}
	entries := make([]dbEntry, 0, len(wires))
	for _, w := range wires {
		balance := new(big.Int)
		if _, ok := balance.SetString(w.NewBalance, 10); !ok {
			return nil, fmt.Errorf("invalid decimal balance %q for address %s", w.NewBalance, w.Address)
		}
		addr := common.HexToAddress(w.Address)
		dbAcc := &DB_OPs.Account{
			DIDAddress:  w.Address,
			Address:     addr,
			Balance:     balance.String(),
			Nonce:       w.Nonce,
			TxNonce:     w.TxNonce,
			TxCountSent: w.TxCountSent,
			AccountType: "user",
			UpdatedAt:   time.Now().UTC().UnixNano(),
		}
		val, err := json.Marshal(dbAcc)
		if err != nil {
			return nil, fmt.Errorf("marshal DB_OPs.Account for address %s: %w", w.Address, err)
		}
		entries = append(entries, dbEntry{
			Key:   DB_OPs.Prefix + addr.Hex(),
			Value: val,
		})
	}
	return entries, nil
}
