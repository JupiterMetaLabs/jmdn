package txstatus

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// ErrDisabled is returned when transaction-status resolution is not enabled.
// It is distinct from a `unknown` result so a caller can tell a switched-off
// feature from a negative answer.
var ErrDisabled = errors.New("transaction status resolution is disabled")

// Config controls the resolver. Every field has a conservative default applied
// by NewResolver, so a zero Config is usable.
type Config struct {
	// MempoolTimeout bounds the mempool lookup. The whole resolve is bounded by
	// this plus the two chain reads.
	MempoolTimeout time.Duration
	// ChainTimeout bounds each chain-store read.
	ChainTimeout time.Duration

	// NegativeCacheTTL / NegativeCacheSize remember conclusive `unknown`s.
	NegativeCacheTTL  time.Duration
	NegativeCacheSize int

	// RateLimitPerSec / RateLimitBurst cap the sustained lookup rate. An
	// over-limit lookup degrades to `unknown`, it does not error.
	RateLimitPerSec float64
	RateLimitBurst  int

	// BreakerFailureThreshold / BreakerCooldown stop calling an unresponsive
	// mempool.
	BreakerFailureThreshold int
	BreakerCooldown         time.Duration
}

func (c Config) withDefaults() Config {
	if c.MempoolTimeout <= 0 {
		c.MempoolTimeout = 400 * time.Millisecond
	}
	if c.ChainTimeout <= 0 {
		c.ChainTimeout = 2 * time.Second
	}
	return c
}

// Observer receives resolution outcomes. The production implementation
// increments Prometheus counters; tests use nil, which is a no-op.
//
// It is an interface rather than a direct metrics import so this package stays
// testable without a metrics registry.
// Parameters are plain strings rather than the Status/Source types so that the
// implementing package (metrics) needs no import of this one, and this package
// needs no import of it — the dependency runs in neither direction.
type Observer interface {
	// ObserveResolve is called once per completed resolve.
	ObserveResolve(status, source string, degraded bool, d time.Duration)
	// ObserveMempoolLookup is called once per attempted mempool lookup.
	// outcome is one of: found, absent, degraded, breaker_open, rate_limited.
	ObserveMempoolLookup(outcome string, d time.Duration)
	// ObserveBreakerTrips reports the cumulative trip count so the caller can
	// emit a delta.
	ObserveBreakerTrips(total int64)
	// ObserveNegativeCache is called with "hit", "miss" or "store".
	ObserveNegativeCache(event string)
}

// Resolver answers status queries by consulting, in order: the chain store, the
// mempool, the chain store again, the failed store, and the local submit log.
type Resolver struct {
	chain    ChainStore
	mempool  MempoolLookup
	failed   FailedStore // optional; nil means no rejections are known
	submits  *SubmitLog
	cfg      Config
	observer Observer

	negCache *negativeCache
	limiter  *tokenBucket
	breaker  *breaker

	// reportedTrips is the breaker trip count already emitted. Atomic because
	// Resolve runs concurrently across RPC handlers.
	reportedTrips atomic.Int64
}

// Deps holds Resolver dependencies. Only ChainStore is required: a nil
// MempoolLookup makes `queued` unreachable, a nil FailedStore makes `failed`
// unreachable, and a nil SubmitLog makes `processing` unreachable. In every
// case the resolver degrades toward `unknown` rather than guessing.
type Deps struct {
	Chain     ChainStore
	Mempool   MempoolLookup
	Failed    FailedStore
	SubmitLog *SubmitLog
	Config    Config
	Observer  Observer
}

// NewResolver builds a Resolver.
func NewResolver(d Deps) *Resolver {
	cfg := d.Config.withDefaults()
	return &Resolver{
		chain:    d.Chain,
		mempool:  d.Mempool,
		failed:   d.Failed,
		submits:  d.SubmitLog,
		cfg:      cfg,
		observer: d.Observer,
		negCache: newNegativeCache(cfg.NegativeCacheTTL, cfg.NegativeCacheSize),
		limiter:  newTokenBucket(cfg.RateLimitPerSec, cfg.RateLimitBurst),
		breaker:  newBreaker(cfg.BreakerFailureThreshold, cfg.BreakerCooldown),
	}
}

// Resolve determines the status of a transaction hash.
//
// It returns an error only for a malformed request or a chain-store failure it
// cannot work around — never because the mempool was slow or down. Callers on
// the eth_getTransactionByHash path can therefore treat any error as fatal to
// the request and any Result as safe to serve.
func (r *Resolver) Resolve(ctx context.Context, hash string) (*Result, error) {
	start := time.Now()

	key := normalizeHash(hash)
	if key == "" {
		return nil, errors.New("hash is required")
	}

	res := &Result{Hash: key, Status: StatusUnknown, Source: SourceNone}

	// ── 1. Chain store ───────────────────────────────────────────────────────
	// Authoritative and cheap. A hit ends the query with zero remote calls.
	mined, err := r.isMined(ctx, key)
	if err != nil {
		// The chain store is the one dependency we cannot work around: without
		// it we cannot distinguish mined from pending, and answering `queued`
		// or `unknown` here could contradict a block that already exists.
		r.observe(res, start)
		return nil, err
	}
	if mined {
		res.Status = StatusMined
		res.Source = SourceChain
		r.observe(res, start)
		return res, nil
	}

	// ── 2. Negative cache ────────────────────────────────────────────────────
	// Only conclusive `unknown`s land here, so a hit is a real answer and not a
	// shortcut past a degraded lookup.
	if r.negCache.has(key) {
		r.note("hit")
		res.Status = StatusUnknown
		res.Source = SourceNegativeCache
		r.observe(res, start)
		return res, nil
	}
	r.note("miss")

	// ── 3. Mempool ───────────────────────────────────────────────────────────
	mem := r.lookupMempool(ctx, key)

	if mem != nil && mem.Found {
		// C3 — re-check the chain store before reporting `queued`.
		//
		// Destructive mempool fetches delete asynchronously, so there is a
		// window in which the mempool still reports a transaction that is
		// already in a block being assembled. Without this second read we would
		// intermittently report `queued` for mined transactions: rare, timing
		// dependent, and close to impossible to reproduce from a bug report.
		// The second read wins.
		if minedNow, err2 := r.isMined(ctx, key); err2 == nil && minedNow {
			res.Status = StatusMined
			res.Source = SourceChain
			res.Detail = "mined between the first chain read and the mempool hit"
			r.observe(res, start)
			return res, nil
		}

		res.Status = StatusQueued
		res.Source = SourceMempool
		res.MempoolNode = mem.NodeID
		if mem.ShardID >= 0 {
			shard := mem.ShardID
			res.ShardID = &shard
		}
		res.Tx = mem.Tx
		r.observe(res, start)
		return res, nil
	}

	degraded := mem == nil || mem.Degraded
	degradedDetail := ""
	if mem != nil {
		degradedDetail = mem.Detail
	}

	// ── 4. Failed store ──────────────────────────────────────────────────────
	if r.failed != nil {
		if rec, ok := r.failed.Get(ctx, key); ok && rec != nil {
			res.Status = StatusFailed
			res.Source = SourceFailedStore
			res.Reason = rec.Reason
			res.MempoolNode = rec.MempoolNode
			r.observe(res, start)
			return res, nil
		}
	}

	// ── 5. Submit log — the only evidence that permits `processing` ───────────
	if rec, ok := r.submits.Get(key); ok {
		submitted := rec.SubmittedAt
		res.SubmittedAt = &submitted
		res.Source = SourceSubmitLog

		if rec.Forwarded {
			// We forwarded it and nobody has it yet: a genuine in-flight
			// window. Note that a degraded mempool answer does NOT downgrade
			// this — the submit log is independent positive evidence that the
			// transaction exists, and "the mempool could not answer" is not
			// evidence against it.
			res.Status = StatusProcessing
			res.Degraded = degraded
			if degraded {
				res.Detail = "mempool lookup inconclusive; status from local submit record"
				if degradedDetail != "" {
					res.Detail += " (" + degradedDetail + ")"
				}
			}
			r.observe(res, start)
			return res, nil
		}

		// We received it but the forward failed, so it never reached the
		// mempool and will never be mined. Reporting `processing` here would
		// make a wallet poll forever on a transaction that does not exist
		// anywhere. `unknown` lets it conclude.
		res.Status = StatusUnknown
		res.Degraded = degraded
		res.Detail = "transaction was received but forwarding to the mempool failed"
		if rec.ForwardErr != "" {
			res.Detail += ": " + rec.ForwardErr
		}
		r.observe(res, start)
		return res, nil
	}

	// ── 6. No evidence anywhere ──────────────────────────────────────────────
	res.Status = StatusUnknown
	res.Degraded = degraded
	if degraded {
		// We never saw this hash AND the mempool could not answer, so we cannot
		// call it conclusively absent — and must not cache it.
		res.Detail = "no local record and the mempool lookup was inconclusive"
		if degradedDetail != "" {
			res.Detail += " (" + degradedDetail + ")"
		}
		r.observe(res, start)
		return res, nil
	}

	// Conclusive: not mined, conclusively not pending, never submitted here.
	r.negCache.store(key)
	r.note("store")
	r.observe(res, start)
	return res, nil
}

// isMined performs one bounded chain-store read.
func (r *Resolver) isMined(ctx context.Context, hash string) (bool, error) {
	if r.chain == nil {
		return false, errors.New("chain store is not configured")
	}
	readCtx, cancel := context.WithTimeout(ctx, r.cfg.ChainTimeout)
	defer cancel()
	return r.chain.IsMined(readCtx, hash)
}

// lookupMempool performs one guarded, bounded mempool lookup.
//
// It never returns an error. Every failure mode — rate limited, breaker open,
// deadline, transport error — comes back as a degraded MempoolResult, because a
// status query must not fail just because the mempool is unwell.
func (r *Resolver) lookupMempool(ctx context.Context, hash string) *MempoolResult {
	if r.mempool == nil {
		return &MempoolResult{Degraded: true, Detail: "mempool lookup is not configured"}
	}

	if !r.limiter.allow() {
		r.observeLookup("rate_limited", 0)
		return &MempoolResult{Degraded: true, Detail: "status lookup rate limit exceeded"}
	}

	if !r.breaker.allow() {
		r.observeLookup("breaker_open", 0)
		r.reportTrips()
		return &MempoolResult{Degraded: true, Detail: "mempool lookup circuit breaker is open"}
	}

	callCtx, cancel := context.WithTimeout(ctx, r.cfg.MempoolTimeout)
	defer cancel()

	start := time.Now()
	out, err := r.mempool.Lookup(callCtx, hash)
	elapsed := time.Since(start)

	switch {
	case err != nil:
		r.breaker.recordFailure()
		r.reportTrips()
		r.observeLookup("degraded", elapsed)
		return &MempoolResult{Degraded: true, Detail: "mempool lookup failed: " + err.Error()}
	case out == nil:
		r.breaker.recordFailure()
		r.reportTrips()
		r.observeLookup("degraded", elapsed)
		return &MempoolResult{Degraded: true, Detail: "mempool lookup returned no result"}
	case out.Degraded:
		r.breaker.recordFailure()
		r.reportTrips()
		r.observeLookup("degraded", elapsed)
		return out
	case out.Found:
		r.breaker.recordSuccess()
		r.observeLookup("found", elapsed)
		return out
	default:
		r.breaker.recordSuccess()
		r.observeLookup("absent", elapsed)
		return out
	}
}

// ─── observability helpers (all nil-Observer safe) ───────────────────────────

func (r *Resolver) observe(res *Result, start time.Time) {
	if r.observer == nil {
		return
	}
	r.observer.ObserveResolve(string(res.Status), string(res.Source), res.Degraded, time.Since(start))
}

func (r *Resolver) observeLookup(outcome string, d time.Duration) {
	if r.observer == nil {
		return
	}
	r.observer.ObserveMempoolLookup(outcome, d)
}

func (r *Resolver) note(event string) {
	if r.observer == nil {
		return
	}
	r.observer.ObserveNegativeCache(event)
}

// reportTrips forwards the breaker's cumulative trip count when it has moved,
// so the metric counts openings rather than requests served while open.
func (r *Resolver) reportTrips() {
	if r.observer == nil {
		return
	}
	total := r.breaker.tripCount()
	if prev := r.reportedTrips.Swap(total); total > prev {
		r.observer.ObserveBreakerTrips(total)
	}
}
