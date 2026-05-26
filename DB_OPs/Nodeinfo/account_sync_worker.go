// MODULE: DB_OPs/Nodeinfo/account_sync_worker
// PURPOSE: Drain the accountsync Redis stream and write account batches to ImmuDB.
//          Owns the at-least-once delivery contract: ACK only after successful DB write.
//
// CORE DATA STRUCTURES:
//   - []StreamEntry: ephemeral per runWorker iteration.
//     Bounded by AccountSyncWorkerConfig.MaxDrainItems (default 100).
//   - []dbEntry: ephemeral per processBatch call.
//     Bounded by MaxDrainItems × avg-accounts-per-payload.
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
//   - Pass the lifecycle ctx to GetAccountConnectionandPutBack — the connection auto-return
//     goroutine fires on ctx.Done(); use a scoped timeout ctx per DB write instead.
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
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/ethereum/go-ethereum/common"
	"gossipnode/DB_OPs"
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
// Stored in the stream as: {"address":"0x...","new_balance":"1000000","nonce":42}
type accountUpdateWire struct {
	Address    string `json:"address"`
	NewBalance string `json:"new_balance"` // decimal string from big.Int.String()
	Nonce      uint64 `json:"nonce"`
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
		BlockTimeout:        5 * time.Second,
		PendingIdleTimeout:  30 * time.Second,
		DBWriteTimeout:      60 * time.Second,
	}
}

// ─── Lifecycle ────────────────────────────────────────────────────────────────

// StartAccountSyncWorker creates the Redis consumer group, registers the streamer
// for use by WriteAccounts and BatchUpdateAccounts, and launches the background
// drain worker.
//
// MUST be called exactly once from main.go (or the lifecycle coordinator) before
// any WriteAccounts or BatchUpdateAccounts calls. If this function is not called,
// both methods return an error immediately.
//
// The worker exits when ctx is cancelled. Unacked entries remain in the Redis PEL
// and are reclaimed by the next StartAccountSyncWorker call via XAUTOCLAIM.
//
// Time: O(1) — one XGROUP CREATE round trip + goroutine spawn.
func StartAccountSyncWorker(ctx context.Context, streamer RedisStreamer, cfg AccountSyncWorkerConfig) error {
	if err := streamer.EnsureConsumerGroup(ctx, accountSyncStream, accountSyncGroup); err != nil {
		return fmt.Errorf("StartAccountSyncWorker: create consumer group %q on stream %q: %w",
			accountSyncGroup, accountSyncStream, err)
	}
	setStreamer(streamer)
	go runWorker(ctx, streamer, cfg)
	return nil
}

// ─── Worker loop ─────────────────────────────────────────────────────────────

// runWorker is the main drain loop. It blocks on XREADGROUP until data arrives or
// BlockTimeout elapses, then coalesces and writes to ImmuDB.
//
// Startup: reclaimPending is called first to replay any PEL entries left by a prior crash.
// Exit: clean on ctx cancellation (XREADGROUP propagates the ctx; select checks at loop top).
func runWorker(ctx context.Context, s RedisStreamer, cfg AccountSyncWorkerConfig) {
	log.Printf("[AccountSyncWorker] started (stream=%s group=%s consumer=%s)",
		accountSyncStream, accountSyncGroup, accountSyncConsumer)
	defer log.Printf("[AccountSyncWorker] stopped")

	// Replay any entries left unACKed by a previous crash before accepting new work.
	if err := reclaimPending(ctx, s, cfg); err != nil {
		if ctx.Err() == nil {
			// Log but don't fatal — new entries can still be processed.
			log.Printf("[AccountSyncWorker] WARN: startup reclaimPending error: %v", err)
		}
	}

	for {
		// Check for shutdown before blocking on Redis.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// XREADGROUP BLOCK cfg.BlockTimeout — sleeps inside Redis until new entries arrive
		// or the timeout elapses. ctx cancellation propagates through go-redis.
		entries, err := s.ReadGroup(
			ctx,
			accountSyncStream, accountSyncGroup, accountSyncConsumer,
			cfg.MaxDrainItems,
			cfg.BlockTimeout,
		)
		if err != nil {
			if ctx.Err() != nil {
				return // clean shutdown
			}
			log.Printf("[AccountSyncWorker] ReadGroup error: %v — retrying in 1s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if len(entries) == 0 {
			continue // timeout, no data — loop
		}

		log.Printf("[AccountSyncWorker] drained %d stream entries — processing", len(entries))
		if err := processBatch(ctx, s, entries, cfg); err != nil {
			if ctx.Err() != nil {
				return
			}
			// Do NOT ACK. Entries remain in PEL and are replayed by the next
			// reclaimPending call (on worker restart) or by XAUTOCLAIM.
			// BatchRestoreAccounts is LWW-idempotent — replays are safe.
			log.Printf("[AccountSyncWorker] processBatch error: %v — %d entries remain in PEL for retry",
				err, len(entries))
		}
	}
}

// reclaimPending reclaims and processes all PEL entries whose idle time exceeds
// cfg.PendingIdleTimeout. Called once on worker startup to replay entries left
// unACKed by a previous crash.
//
// Iterates via cursor until the full PEL is scanned ("0-0" returned as next cursor).
//
// Time: O(PEL size / MaxDrainItems) XAUTOCLAIM round trips + processBatch cost per page.
func reclaimPending(ctx context.Context, s RedisStreamer, cfg AccountSyncWorkerConfig) error {
	cursor := "0-0"
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		entries, next, err := s.AutoClaim(
			ctx,
			accountSyncStream, accountSyncGroup, accountSyncConsumer,
			cfg.PendingIdleTimeout,
			cursor,
			cfg.MaxDrainItems,
		)
		if err != nil {
			return fmt.Errorf("XAUTOCLAIM cursor=%s: %w", cursor, err)
		}

		if len(entries) > 0 {
			log.Printf("[AccountSyncWorker] reclaiming %d pending entries (cursor=%s)", len(entries), cursor)
			if err := processBatch(ctx, s, entries, cfg); err != nil {
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
func processBatch(ctx context.Context, s RedisStreamer, entries []StreamEntry, cfg AccountSyncWorkerConfig) error {
	var (
		writeEntries []dbEntry // accounts to persist to ImmuDB
		goodIDs      []string  // stream IDs to ACK after successful DB write
		poisonIDs    []string  // stream IDs to ACK immediately (unrecoverable parse failure)
	)

	for _, entry := range entries {
		payloadType, _ := entry.Values["type"].(string)
		dataStr, _ := entry.Values["data"].(string)

		switch syncPayloadType(payloadType) {
		case payloadTypeAccounts:
			parsed, err := parseAccountsPayload(dataStr)
			if err != nil {
				log.Printf("[AccountSyncWorker] WARN: poison pill — undecodable accounts entry %s: %v", entry.ID, err)
				poisonIDs = append(poisonIDs, entry.ID)
				continue
			}
			writeEntries = append(writeEntries, parsed...)
			goodIDs = append(goodIDs, entry.ID)

		case payloadTypeUpdates:
			parsed, err := parseUpdatesPayload(dataStr)
			if err != nil {
				log.Printf("[AccountSyncWorker] WARN: poison pill — undecodable updates entry %s: %v", entry.ID, err)
				poisonIDs = append(poisonIDs, entry.ID)
				continue
			}
			writeEntries = append(writeEntries, parsed...)
			goodIDs = append(goodIDs, entry.ID)

		default:
			log.Printf("[AccountSyncWorker] WARN: poison pill — unknown payload type %q in entry %s", payloadType, entry.ID)
			poisonIDs = append(poisonIDs, entry.ID)
		}
	}

	// ACK poison pills immediately — they are unrecoverable and must not block the PEL.
	if len(poisonIDs) > 0 {
		ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.Ack(ackCtx, accountSyncStream, accountSyncGroup, poisonIDs...); err != nil {
			log.Printf("[AccountSyncWorker] WARN: failed to ACK %d poison pills: %v", len(poisonIDs), err)
		}
		cancel()
	}

	if len(writeEntries) == 0 {
		return nil
	}

	// Use a timeout context scoped to this DB write — NOT the lifecycle ctx.
	// GetAccountConnectionandPutBack launches a goroutine that returns the connection
	// on ctx.Done(). Using the lifecycle ctx would return the connection on worker
	// shutdown rather than on write completion.
	writeCtx, writeCancel := context.WithTimeout(ctx, cfg.DBWriteTimeout)
	defer writeCancel()

	conn, err := DB_OPs.GetAccountConnectionandPutBack(writeCtx)
	if err != nil {
		return fmt.Errorf("get account DB connection: %w", err)
	}

	// Write in sub-batches to bound individual ImmuDB commit size.
	// All chunks must succeed before any ACK is issued.
	//
	// Time: O(ceil(N / MaxAccountsPerBatch)) BatchRestoreAccounts calls.
	for i := 0; i < len(writeEntries); i += cfg.MaxAccountsPerBatch {
		end := i + cfg.MaxAccountsPerBatch
		if end > len(writeEntries) {
			end = len(writeEntries)
		}
		// []dbEntry is a type alias for []struct{Key string; Value []byte} —
		// assignment-compatible with BatchRestoreAccounts parameter without conversion.
		if err := DB_OPs.BatchRestoreAccounts(conn, writeEntries[i:end]); err != nil {
			return fmt.Errorf("BatchRestoreAccounts chunk [%d:%d] of %d: %w", i, end, len(writeEntries), err)
		}
	}

	// All sub-batches succeeded — ACK the good entries, removing them from the PEL.
	// If ACK itself fails, entries remain in PEL and will be replayed.
	// Replay safety: BatchRestoreAccounts is LWW-idempotent.
	ackCtx, ackCancel := context.WithTimeout(ctx, 5*time.Second)
	defer ackCancel()
	if err := s.Ack(ackCtx, accountSyncStream, accountSyncGroup, goodIDs...); err != nil {
		log.Printf("[AccountSyncWorker] WARN: ACK failed for %d entries after successful DB write: %v — will be reclaimed and re-written (safe, LWW)", len(goodIDs), err)
	} else {
		log.Printf("[AccountSyncWorker] wrote %d accounts from %d entries; all ACKed",
			len(writeEntries), len(goodIDs))
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
	entries := make([]dbEntry, 0, len(accs))
	for _, acc := range accs {
		if acc == nil {
			continue
		}
		dbAcc := &DB_OPs.Account{
			DIDAddress:  acc.DIDAddress,
			Address:     acc.Address,
			Balance:     acc.Balance,
			Nonce:       acc.Nonce,
			AccountType: acc.AccountType,
			CreatedAt:   acc.CreatedAt,
			UpdatedAt:   acc.UpdatedAt,
			Metadata:    acc.Metadata,
		}
		val, err := json.Marshal(dbAcc)
		if err != nil {
			return nil, fmt.Errorf("marshal DB_OPs.Account for address %s: %w", acc.Address.Hex(), err)
		}
		entries = append(entries, dbEntry{
			Key:   DB_OPs.Prefix + acc.Address.Hex(),
			Value: val,
		})
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
