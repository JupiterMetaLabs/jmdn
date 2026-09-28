package DB_OPs

import (
	"context"
	"time"
)

// Outbox purge hooks (D-858). The store-failure rollback in
// messaging/BlockProcessing must drop any tx/snapshot projection that a FAILED
// StoreZKBlock enqueued to the outbox, so the outbox worker can never land the
// rolled-back block's rows later. These package-level hooks are wired once at
// startup by main() to the node's thebegateway OutboxStore (MaxID / DeleteAfter),
// exactly like SetOutboxRequeuer — DB_OPs must not import thebegateway directly
// (cycle risk), so the wiring is injected.
//
// Both are best-effort: when unwired (unit tests, tooling) OutboxMaxID reports
// "unknown" (-1) and PurgeOutboxAfter becomes a no-op, so the rollback still
// completes — it simply does not trim the outbox. The -1 sentinel is load-bearing:
// PurgeOutboxAfter(-1) must NEVER delete anything (a naive 0 would delete the whole
// table via "id > 0").
var (
	outboxMaxIDFn      func(context.Context) (int64, error)
	outboxPurgeAfterFn func(context.Context, int64) (int64, error)
)

// SetOutboxMaxIDFn wires the outbox high-water-mark reader. Call once at startup.
func SetOutboxMaxIDFn(fn func(context.Context) (int64, error)) { outboxMaxIDFn = fn }

// SetOutboxPurgeAfterFn wires the "delete entries with id > sinceID" hook. Call
// once at startup.
func SetOutboxPurgeAfterFn(fn func(context.Context, int64) (int64, error)) {
	outboxPurgeAfterFn = fn
}

// OutboxMaxID returns the current outbox high-water id, or -1 when the hook is
// unwired or the read fails. Callers pass the returned value to PurgeOutboxAfter;
// a -1 result makes that call a guaranteed no-op.
func OutboxMaxID() int64 {
	if outboxMaxIDFn == nil {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := outboxMaxIDFn(ctx)
	if err != nil {
		return -1
	}
	return id
}

// PurgeOutboxAfter deletes outbox entries with id > sinceID and returns the count
// removed. It is a no-op (returns 0) when sinceID < 0 (unknown high-water mark) or
// the hook is unwired, so it can only ever remove entries that appeared AFTER the
// caller sampled OutboxMaxID — i.e. the projection a failed store just enqueued.
func PurgeOutboxAfter(sinceID int64) int64 {
	if sinceID < 0 || outboxPurgeAfterFn == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := outboxPurgeAfterFn(ctx, sinceID)
	if err != nil {
		return 0
	}
	return n
}
