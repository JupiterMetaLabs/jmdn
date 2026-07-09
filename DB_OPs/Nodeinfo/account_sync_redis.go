// MODULE: DB_OPs/Nodeinfo/account_sync_redis
// PURPOSE: Define the Redis stream transport abstraction (RedisStreamer interface) and
//          adapt *redis.Client to it. Owns zero DB or business logic — pure transport.
//
// CORE DATA STRUCTURES:
//   - StreamEntry: ephemeral; one per stream message read. Count per ReadGroup call
//     is bounded by AccountSyncWorkerConfig.MaxDrainItems at the call site.
//   - pkgAccountStreamer / pkgWorkerManager (package-level): set once by InstallAccountQueue.
//     Read by every WriteAccounts / BatchUpdateAccounts call. Never replaced after set.
//
// TO MODIFY BEHAVIOR:
//   - Change stream backend: implement RedisStreamer → pass to StartAccountSyncWorker.
//   - Change stream key / consumer group name: update constants below; no logic changes.
//   - Add a new stream key: define a new constant; add a corresponding Enqueue call in
//     immudb_account_manager.go and a new case in processBatch.
//
// DO NOT:
//   - Import *redis.Client outside redisStreamerAdapter — it is the only concrete import.
//   - Store request-scoped state on redisStreamerAdapter (stateless wrapper by design).
//   - Replace pkgAccountStreamer with a per-call parameter — types.AccountManager interface
//     signatures are fixed by the external JMDN-FastSync module and cannot be changed.
//
// EXTENSION POINT: new queue backends → implement RedisStreamer; inject via StartAccountSyncWorker.
//
// CHANGE SCENARIOS:
//   Swap Redis client lib: rewrite redisStreamerAdapter methods — interface unchanged.
//   Add new stream key:    add constant + Enqueue call in account_manager — this file unchanged.
//   Change group/consumer: edit constants — no logic change required.

package NodeInfo

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ─── Stream constants ─────────────────────────────────────────────────────────

const (
	// accountSyncStream is the Redis stream key for all account sync payloads.
	accountSyncStream = "accountsync:accounts"
	// accountSyncGroup is the consumer group name. One group = one logical processor.
	accountSyncGroup = "accountsync-workers"
	// accountSyncConsumer is the consumer name within the group. Single worker model.
	accountSyncConsumer = "worker-0"
)

// syncPayloadType discriminates between WriteAccounts and BatchUpdateAccounts payloads
// stored in the same stream.
type syncPayloadType string

const (
	payloadTypeAccounts  syncPayloadType = "accounts"   // payload: []*types.Account (JSON)
	payloadTypeUpdates   syncPayloadType = "updates"    // payload: []accountUpdateWire (JSON)
	payloadTypeTxMarkers syncPayloadType = "tx_markers" // payload: []txMarkerWire (JSON) — F4 3a
)

// ─── Domain types ─────────────────────────────────────────────────────────────

// StreamEntry is a single Redis stream message with its assigned stream ID.
// ID is used for XACK after successful DB write.
// Values contains the raw message fields as returned by go-redis.
type StreamEntry struct {
	ID     string
	Values map[string]any
}

// ─── RedisStreamer interface ──────────────────────────────────────────────────

// RedisStreamer is the minimal Redis stream surface required by the account sync worker.
// It uses only domain-level types — no go-redis types leak through the interface.
// The concrete implementation is redisStreamerAdapter (wraps *redis.Client).
// Tests may substitute a mock implementing this interface.
type RedisStreamer interface {
	// Ping verifies the connection to the Redis server.
	// Time: O(1) — single PING round trip.
	Ping(ctx context.Context) error

	// Enqueue appends a message to the named stream. Returns the assigned message ID.
	// Time: O(1) — single XADD round trip.
	Enqueue(ctx context.Context, stream string, values map[string]any) (string, error)

	// EnsureConsumerGroup creates the consumer group on the stream, creating the stream
	// itself if it does not exist. Idempotent: no-op if the group already exists.
	// Time: O(1) — single XGROUP CREATE round trip.
	EnsureConsumerGroup(ctx context.Context, stream, group string) error

	// ReadGroup performs a blocking read from the stream under the given consumer group.
	// Reads at most count new (undelivered) entries; blocks up to blockDur waiting for data.
	// Returns nil, nil on timeout (no data within blockDur).
	// Read entries move to the Pending Entries List (PEL) until ACKed.
	// Time: O(count) — single XREADGROUP round trip.
	ReadGroup(ctx context.Context, stream, group, consumer string, count int64, blockDur time.Duration) ([]StreamEntry, error)

	// Ack acknowledges the given message IDs, removing them from the PEL.
	// Only call after the DB write succeeds — unACKed entries are replayed via AutoClaim.
	// Time: O(|ids|) — single XACK round trip.
	Ack(ctx context.Context, stream, group string, ids ...string) error

	// Delete removes message IDs from the stream body (XDEL), reclaiming memory.
	// Call in a pipeline with Ack after every successful DB commit. XACK alone leaves
	// the payload resident in the stream; XDEL is required to reclaim that space.
	// Time: O(|ids|) — single XDEL round trip.
	Delete(ctx context.Context, stream string, ids ...string) error

	// AutoClaim reclaims pending entries that have been idle longer than minIdle.
	// start is the minimum PEL cursor ID ("0-0" to scan from the beginning).
	// Returns reclaimed entries and the next cursor ID.
	// "0-0" as the returned cursor means the full PEL was scanned.
	// Time: O(count) — single XAUTOCLAIM round trip.
	AutoClaim(ctx context.Context, stream, group, consumer string, minIdle time.Duration, start string, count int64) ([]StreamEntry, string, error)

	// Len returns the total number of messages currently in the stream (XLEN).
	// Time: O(1).
	Len(ctx context.Context, stream string) (int64, error)

	// PendingCount returns the count of unacked messages in the PEL for the given group.
	// Time: O(1) — single XPENDING round trip.
	PendingCount(ctx context.Context, stream, group string) (int64, error)
}

// ─── Concrete adapter ─────────────────────────────────────────────────────────

// redisStreamerAdapter adapts *redis.Client to the RedisStreamer interface.
// It is the ONLY place in DB_OPs/Nodeinfo that imports a concrete Redis type.
type redisStreamerAdapter struct {
	client *redis.Client
}

// NewRedisStreamer wraps a *redis.Client as a RedisStreamer.
// Construct in main.go and pass the result to StartAccountSyncWorker.
//
// Time: O(1)
func NewRedisStreamer(client *redis.Client) RedisStreamer {
	return &redisStreamerAdapter{client: client}
}

// Time: O(1) — single PING round trip
func (r *redisStreamerAdapter) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Time: O(1) — single XADD round trip
func (r *redisStreamerAdapter) Enqueue(ctx context.Context, stream string, values map[string]any) (string, error) {
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: values,
	}).Result()
}

// Time: O(1) — single XGROUP CREATECONSUMER or XGROUP CREATE round trip.
// BUSYGROUP error means the group already exists; treated as success.
func (r *redisStreamerAdapter) EnsureConsumerGroup(ctx context.Context, stream, group string) error {
	err := r.client.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// Time: O(count) — XREADGROUP COUNT count BLOCK blockDur ms
// Redis.Nil is returned on timeout; mapped to (nil, nil) so callers don't treat it as an error.
func (r *redisStreamerAdapter) ReadGroup(ctx context.Context, stream, group, consumer string, count int64, blockDur time.Duration) ([]StreamEntry, error) {
	result, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    count,
		Block:    blockDur,
		NoAck:    false,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // timeout — no data; caller loops
		}
		return nil, err
	}
	var entries []StreamEntry
	for _, s := range result {
		for _, msg := range s.Messages {
			entries = append(entries, StreamEntry{ID: msg.ID, Values: msg.Values})
		}
	}
	return entries, nil
}

// Time: O(|ids|) — single XACK round trip
func (r *redisStreamerAdapter) Ack(ctx context.Context, stream, group string, ids ...string) error {
	return r.client.XAck(ctx, stream, group, ids...).Err()
}

// Time: O(|ids|) — single XDEL round trip
func (r *redisStreamerAdapter) Delete(ctx context.Context, stream string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.client.XDel(ctx, stream, ids...).Err()
}

// Time: O(count) — single XAUTOCLAIM round trip
// go-redis v9 XAutoClaimCmd.Result() returns ([]XMessage, string, error) — three values.
func (r *redisStreamerAdapter) AutoClaim(ctx context.Context, stream, group, consumer string, minIdle time.Duration, start string, count int64) ([]StreamEntry, string, error) {
	messages, next, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    start,
		Count:    count,
	}).Result()
	if err != nil {
		return nil, "0-0", err
	}
	var entries []StreamEntry
	for _, msg := range messages {
		entries = append(entries, StreamEntry{ID: msg.ID, Values: msg.Values})
	}
	return entries, next, nil
}

func (r *redisStreamerAdapter) Len(ctx context.Context, stream string) (int64, error) {
	return r.client.XLen(ctx, stream).Result()
}

func (r *redisStreamerAdapter) PendingCount(ctx context.Context, stream, group string) (int64, error) {
	info, err := r.client.XPending(ctx, stream, group).Result()
	if err != nil {
		return 0, err
	}
	return info.Count, nil
}

// ─── Package-level queue singleton ───────────────────────────────────────────

// pkgAccountStreamer and pkgWorkerManager are set once by InstallAccountQueue.
// Read by every WriteAccounts / BatchUpdateAccounts call. types.AccountManager
// interface signatures are fixed externally — package-level injection is the only path.
var (
	pkgAccountStreamer RedisStreamer
	pkgWorkerManager   *WorkerManager
	pkgAccountQueueMu  sync.RWMutex
)

// InstallAccountQueue stores the streamer and manager together.
// Called once from StartAccountSyncWorker during node startup.
func InstallAccountQueue(s RedisStreamer, m *WorkerManager) {
	pkgAccountQueueMu.Lock()
	pkgAccountStreamer = s
	pkgWorkerManager = m
	pkgAccountQueueMu.Unlock()
}

// getAccountQueue returns the package-level streamer and worker manager.
// Both are nil if InstallAccountQueue has not yet been called.
// Time: O(1)
func getAccountQueue() (RedisStreamer, *WorkerManager) {
	pkgAccountQueueMu.RLock()
	defer pkgAccountQueueMu.RUnlock()
	return pkgAccountStreamer, pkgWorkerManager
}
