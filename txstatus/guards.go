package txstatus

import (
	"sync"
	"time"
)

// This file holds the three load guards on the status-lookup path. They exist
// because a chain-store miss here becomes a fleet-wide scatter-gather inside
// MRE, and jmdn's JSON-RPC port is the surface an explorer or a bot talks to.
// Without them, one cheap RPC turns into fleet-wide load, and an unhealthy MRE
// turns into unbounded latency on every eth_getTransactionByHash.
//
// MRE has its own copies of these. Both are needed and they protect different
// things: MRE's guard its own fleet against all callers, these guard jmdn's own
// handler latency and stop jmdn being the thing that overloads MRE.

// ─────────────────────────────────────────────────────────────────────────────
// negativeCache — remembers conclusive `unknown` answers
// ─────────────────────────────────────────────────────────────────────────────

// negativeCache remembers hashes that resolved to a CONCLUSIVE unknown, so a
// burst of probes for nonexistent hashes does not become a burst of MRE
// lookups.
//
// Only conclusive answers may be stored. A degraded answer must never be
// cached: doing so would pin a real, pending transaction to `unknown` for the
// whole TTL because one shard happened to time out.
type negativeCache struct {
	mu       sync.Mutex
	entries  map[string]time.Time
	ttl      time.Duration
	capacity int
	now      func() time.Time
}

func newNegativeCache(ttl time.Duration, capacity int) *negativeCache {
	return &negativeCache{
		entries:  make(map[string]time.Time),
		ttl:      ttl,
		capacity: capacity,
		now:      time.Now,
	}
}

func (c *negativeCache) enabled() bool { return c != nil && c.ttl > 0 && c.capacity > 0 }

func (c *negativeCache) has(hash string) bool {
	if !c.enabled() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	exp, ok := c.entries[hash]
	if !ok {
		return false
	}
	if !c.now().Before(exp) {
		delete(c.entries, hash)
		return false
	}
	return true
}

func (c *negativeCache) store(hash string) {
	if !c.enabled() || hash == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.capacity {
		if _, refresh := c.entries[hash]; !refresh {
			// At capacity with a new key: drop the map rather than grow it. The
			// TTL is seconds, so the map is short-lived anyway, and a dropped
			// map only costs extra lookups — never a wrong answer.
			c.entries = make(map[string]time.Time, c.capacity)
		}
	}
	c.entries[hash] = c.now().Add(c.ttl)
}

func (c *negativeCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// ─────────────────────────────────────────────────────────────────────────────
// tokenBucket — caps the sustained status-lookup rate
// ─────────────────────────────────────────────────────────────────────────────

// tokenBucket is a non-blocking token-bucket limiter.
//
// Non-blocking matters: an over-limit status query must degrade to `unknown`
// immediately, not queue. Queueing would convert a load problem into a latency
// problem on the RPC handler, which is exactly what the deadline rules forbid.
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	tokens   float64
	lastFill time.Time
	now      func() time.Time
}

func newTokenBucket(ratePerSec float64, burst int) *tokenBucket {
	b := &tokenBucket{rate: ratePerSec, burst: float64(burst), now: time.Now}
	b.tokens = b.burst
	b.lastFill = time.Now()
	return b
}

func (b *tokenBucket) enabled() bool { return b != nil && b.rate > 0 && b.burst > 0 }

func (b *tokenBucket) allow() bool {
	if !b.enabled() {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.lastFill = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// breaker — stops calling a mempool that is not answering
// ─────────────────────────────────────────────────────────────────────────────

// breaker is a consecutive-failure circuit breaker over the mempool lookup.
//
// When MRE is unreachable, every lookup is going to be degraded anyway, so
// continuing to call it just adds the full deadline to each request and piles
// load on an unhealthy service. Open means: answer `unknown` immediately,
// without a network call. After the cooldown one probe is admitted; success
// closes it, failure re-opens it.
type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	failures  int
	openUntil time.Time
	probing   bool
	trips     int64
	now       func() time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

func (b *breaker) enabled() bool { return b != nil && b.threshold > 0 && b.cooldown > 0 }

func (b *breaker) allow() bool {
	if !b.enabled() {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.openUntil.IsZero() {
		return true
	}
	if b.now().Before(b.openUntil) {
		return false
	}
	if b.probing {
		return false
	}
	b.probing = true
	return true
}

func (b *breaker) recordSuccess() {
	if !b.enabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
	b.probing = false
}

func (b *breaker) recordFailure() {
	if !b.enabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.probing {
		b.probing = false
		b.openUntil = b.now().Add(b.cooldown)
		b.trips++
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.failures = 0
		b.openUntil = b.now().Add(b.cooldown)
		b.trips++
	}
}

func (b *breaker) tripCount() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.trips
}
