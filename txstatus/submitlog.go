package txstatus

import (
	"strings"
	"sync"
	"time"
)

// SubmitRecord is jmdn's local note that it received a transaction and tried to
// forward it to the mempool.
//
// This record is the whole reason `processing` can be reported honestly. jmdn
// already sees every transaction at eth_sendRawTransaction, so writing a small
// local record at that moment turns "not in the chain store and not in the
// mempool" from an ambiguous condition into a decidable one, with no new
// cross-service dependency.
type SubmitRecord struct {
	Hash        string
	Sender      string
	Nonce       uint64
	SubmittedAt time.Time
	// Forwarded is true only if the mempool accepted the forward. When false,
	// the transaction never reached the mempool and will never be mined — the
	// resolver reports `unknown` rather than `processing`, so a wallet stops
	// polling instead of waiting forever on a transaction that does not exist
	// anywhere.
	Forwarded bool
	// ForwardErr is the forwarding failure, when Forwarded is false.
	ForwardErr string
}

// SubmitLog is a bounded, TTL-expiring set of SubmitRecords.
//
// In-memory on purpose. The record only has to outlive the worst-case
// time-to-inclusion; persisting it would mean a durable write on the hot
// submit path to buy a marginally better answer to a status query. A restart
// loses in-flight records, which degrades `processing` to `unknown` — the safe
// direction.
//
// Sizing the TTL: the orchestrator polls the mempool on an interval and only
// builds a block once enough transactions are pending, so worst-case inclusion
// is far longer than intuition suggests. Measure it before trusting a default.
type SubmitLog struct {
	mu      sync.RWMutex
	records map[string]SubmitRecord

	ttl      time.Duration
	capacity int
	now      func() time.Time
}

// NewSubmitLog builds a submit log. A ttl or capacity of zero (or less) yields
// a disabled log: Record is a no-op and Get always misses, which makes
// `processing` unreachable and every in-flight transaction report `unknown`.
func NewSubmitLog(ttl time.Duration, capacity int) *SubmitLog {
	return &SubmitLog{
		records:  make(map[string]SubmitRecord),
		ttl:      ttl,
		capacity: capacity,
		now:      time.Now,
	}
}

func (l *SubmitLog) enabled() bool {
	return l != nil && l.ttl > 0 && l.capacity > 0
}

// normalizeHash lowercases and 0x-prefixes a hash so the log is keyed
// consistently regardless of how a caller spelled it.
//
// Unlike the MRE lookup — which must send the hash verbatim because the mempool
// indexes on the submitted string — this map is jmdn's own, so normalising here
// is safe and stops `0xABC` and `0xabc` becoming two records for one
// transaction.
func normalizeHash(hash string) string {
	h := strings.ToLower(strings.TrimSpace(hash))
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "0x") {
		h = "0x" + h
	}
	return h
}

// Record stores (or replaces) the record for a hash. Safe on a nil receiver so
// call sites on the submit path need no feature check.
func (l *SubmitLog) Record(r SubmitRecord) {
	if !l.enabled() {
		return
	}
	key := normalizeHash(r.Hash)
	if key == "" {
		return
	}
	r.Hash = key
	if r.SubmittedAt.IsZero() {
		r.SubmittedAt = l.now()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.records) >= l.capacity {
		if _, replacing := l.records[key]; !replacing {
			l.evictLocked()
		}
	}
	l.records[key] = r
}

// evictLocked drops expired records, and if that freed nothing, drops the
// oldest tenth of the map. Losing a record only downgrades `processing` to
// `unknown`; it never produces a wrong answer, so an approximate policy is
// preferable to the bookkeeping of an exact LRU on the submit path.
func (l *SubmitLog) evictLocked() {
	cutoff := l.now().Add(-l.ttl)
	for k, v := range l.records {
		if v.SubmittedAt.Before(cutoff) {
			delete(l.records, k)
		}
	}
	if len(l.records) < l.capacity {
		return
	}

	drop := l.capacity / 10
	if drop < 1 {
		drop = 1
	}
	// Find the `drop` oldest by a single scan per victim. capacity/10 passes
	// over a bounded map, on a path that only runs when the log is full.
	for i := 0; i < drop && len(l.records) > 0; i++ {
		var oldestKey string
		var oldestAt time.Time
		first := true
		for k, v := range l.records {
			if first || v.SubmittedAt.Before(oldestAt) {
				oldestKey, oldestAt, first = k, v.SubmittedAt, false
			}
		}
		delete(l.records, oldestKey)
	}
}

// Get returns the live record for a hash. Expired records are treated as
// absent. Safe on a nil receiver.
func (l *SubmitLog) Get(hash string) (SubmitRecord, bool) {
	if !l.enabled() {
		return SubmitRecord{}, false
	}
	key := normalizeHash(hash)
	if key == "" {
		return SubmitRecord{}, false
	}

	l.mu.RLock()
	r, ok := l.records[key]
	l.mu.RUnlock()
	if !ok {
		return SubmitRecord{}, false
	}

	if l.now().Sub(r.SubmittedAt) >= l.ttl {
		l.mu.Lock()
		// Re-check under the write lock: a concurrent Record may have refreshed
		// it between the read and the write.
		if cur, still := l.records[key]; still && l.now().Sub(cur.SubmittedAt) >= l.ttl {
			delete(l.records, key)
		}
		l.mu.Unlock()
		return SubmitRecord{}, false
	}

	return r, true
}

// Len reports how many records are held, expired ones included until touched.
func (l *SubmitLog) Len() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.records)
}

// ─────────────────────────────────────────────────────────────────────────────
// Process-wide instance
// ─────────────────────────────────────────────────────────────────────────────

var (
	globalMu        sync.RWMutex
	globalSubmitLog *SubmitLog
)

// InitSubmitLog installs the process-wide submit log. Called once during
// startup when the feature is enabled; leaving it uncalled keeps every
// RecordSubmit call a no-op.
func InitSubmitLog(ttl time.Duration, capacity int) *SubmitLog {
	l := NewSubmitLog(ttl, capacity)
	globalMu.Lock()
	globalSubmitLog = l
	globalMu.Unlock()
	return l
}

// GlobalSubmitLog returns the process-wide submit log, or nil when unset. All
// SubmitLog methods are nil-safe, so callers need not check.
func GlobalSubmitLog() *SubmitLog {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalSubmitLog
}

// RecordSubmit writes to the process-wide submit log. This is the single call
// the transaction submit path needs; it is a no-op when the feature is off, so
// it cannot fail the submit.
func RecordSubmit(r SubmitRecord) {
	GlobalSubmitLog().Record(r)
}
