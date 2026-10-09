package explorer

import (
	"sync"
	"time"
)

// statsCountTTL bounds how often the stats endpoint re-runs a COUNT(*) on the
// ThebeDB SQL projection. The explorer polls /stats far more often than the
// totals change, and a count a few seconds stale is invisible to a user,
// whereas an unbounded COUNT(*) per request is a self-inflicted load test on
// a large table.
const statsCountTTL = 10 * time.Second

// countCache memoises one int64 count with a TTL and remembers the last
// successful value so a transient DB error degrades to "slightly stale"
// instead of "0".
type countCache struct {
	mu      sync.Mutex
	value   int64
	fetched time.Time
	ok      bool
}

var (
	statsTxCount      countCache
	statsAccountCount countCache
)

// cachedCount returns the cached value when it is younger than statsCountTTL,
// otherwise calls fetch, stores the result and returns it. On fetch error the
// cache is left untouched and the error is returned.
func cachedCount(c *countCache, fetch func() (int64, error)) (int64, error) {
	c.mu.Lock()
	if c.ok && time.Since(c.fetched) < statsCountTTL {
		v := c.value
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	v, err := fetch()
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.value, c.fetched, c.ok = v, time.Now(), true
	c.mu.Unlock()
	return v, nil
}

// lastGood returns the most recent successfully fetched value, or 0.
func (c *countCache) lastGood() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}
