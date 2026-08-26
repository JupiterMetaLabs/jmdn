package txstatus

import (
	"testing"
	"time"
)

// ─── negativeCache ───────────────────────────────────────────────────────────

func TestNegativeCache_StoreHitExpire(t *testing.T) {
	c := newNegativeCache(100*time.Millisecond, 10)
	now := time.Now()
	c.now = func() time.Time { return now }

	if c.has("0xabc") {
		t.Error("empty cache reported a hit")
	}
	c.store("0xabc")
	if !c.has("0xabc") {
		t.Error("stored entry not found")
	}

	now = now.Add(150 * time.Millisecond)
	if c.has("0xabc") {
		t.Error("entry outlived its TTL")
	}
	if c.len() != 0 {
		t.Error("expired entry not dropped on access")
	}
}

func TestNegativeCache_RespectsCapacity(t *testing.T) {
	c := newNegativeCache(time.Minute, 4)
	for i := 0; i < 40; i++ {
		c.store(string(rune('a'+i%26)) + string(rune('0'+i/26)))
		if c.len() > 4 {
			t.Fatalf("cache grew to %d past capacity 4", c.len())
		}
	}
}

func TestNegativeCache_DisabledWhenUnsized(t *testing.T) {
	for name, c := range map[string]*negativeCache{
		"zero ttl":  newNegativeCache(0, 10),
		"zero size": newNegativeCache(time.Minute, 0),
		"nil":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			c.store("0xabc")
			if c.has("0xabc") {
				t.Error("disabled cache reported a hit")
			}
			if c.len() != 0 {
				t.Error("disabled cache reported entries")
			}
		})
	}
}

// ─── tokenBucket ─────────────────────────────────────────────────────────────

func TestTokenBucket_BurstThenReject(t *testing.T) {
	b := newTokenBucket(10, 3)
	now := time.Now()
	b.now = func() time.Time { return now }
	b.lastFill = now

	for i := 0; i < 3; i++ {
		if !b.allow() {
			t.Fatalf("call %d within burst was rejected", i)
		}
	}
	if b.allow() {
		t.Error("bucket allowed a call past its burst")
	}
}

func TestTokenBucket_RefillsAndCaps(t *testing.T) {
	b := newTokenBucket(10, 2) // 10/s => 1 token per 100ms
	now := time.Now()
	b.now = func() time.Time { return now }
	b.lastFill = now

	b.allow()
	b.allow()
	if b.allow() {
		t.Fatal("bucket should be empty")
	}

	now = now.Add(100 * time.Millisecond)
	if !b.allow() {
		t.Error("one token should have refilled")
	}

	// A long idle period must not mint unbounded tokens.
	now = now.Add(time.Hour)
	if !b.allow() || !b.allow() {
		t.Error("refill after idle should restore the burst")
	}
	if b.allow() {
		t.Error("refill exceeded the burst cap")
	}
}

func TestTokenBucket_DisabledAlwaysAllows(t *testing.T) {
	for name, b := range map[string]*tokenBucket{
		"zero rate":  newTokenBucket(0, 10),
		"zero burst": newTokenBucket(10, 0),
		"nil":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 50; i++ {
				if !b.allow() {
					t.Fatalf("disabled limiter rejected call %d", i)
				}
			}
		})
	}
}

// ─── breaker ─────────────────────────────────────────────────────────────────

func TestBreaker_TripsAtThreshold(t *testing.T) {
	b := newBreaker(3, time.Minute)
	now := time.Now()
	b.now = func() time.Time { return now }

	b.recordFailure()
	b.recordFailure()
	if !b.allow() {
		t.Error("breaker opened below its threshold")
	}
	b.recordFailure()
	if b.allow() {
		t.Error("breaker did not open at its threshold")
	}
	if b.tripCount() != 1 {
		t.Errorf("trips = %d, want 1", b.tripCount())
	}
}

func TestBreaker_SuccessClearsStreak(t *testing.T) {
	b := newBreaker(3, time.Minute)
	b.recordFailure()
	b.recordFailure()
	b.recordSuccess()
	b.recordFailure()
	b.recordFailure()
	if !b.allow() {
		t.Error("a success between failures should have cleared the streak")
	}
	if b.tripCount() != 0 {
		t.Errorf("trips = %d, want 0", b.tripCount())
	}
}

func TestBreaker_HalfOpenAdmitsOneProbeThenRecovers(t *testing.T) {
	b := newBreaker(1, 100*time.Millisecond)
	now := time.Now()
	b.now = func() time.Time { return now }

	b.recordFailure()
	if b.allow() {
		t.Fatal("breaker should be open")
	}

	now = now.Add(150 * time.Millisecond)
	if !b.allow() {
		t.Fatal("cooldown elapsed: one probe should be admitted")
	}
	if b.allow() {
		t.Error("more than one probe was admitted while half-open")
	}

	b.recordSuccess()
	if !b.allow() || !b.allow() {
		t.Error("a successful probe should close the breaker")
	}
}

func TestBreaker_FailedProbeReopens(t *testing.T) {
	b := newBreaker(1, 100*time.Millisecond)
	now := time.Now()
	b.now = func() time.Time { return now }

	b.recordFailure()
	now = now.Add(150 * time.Millisecond)
	if !b.allow() {
		t.Fatal("probe should be admitted")
	}
	b.recordFailure()
	if b.allow() {
		t.Error("a failed probe should re-open the breaker")
	}
	if b.tripCount() != 2 {
		t.Errorf("trips = %d, want 2", b.tripCount())
	}
}

func TestBreaker_DisabledAlwaysAllows(t *testing.T) {
	for name, b := range map[string]*breaker{
		"zero threshold": newBreaker(0, time.Minute),
		"zero cooldown":  newBreaker(3, 0),
		"nil":            nil,
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				b.recordFailure()
			}
			if !b.allow() {
				t.Error("disabled breaker rejected a call")
			}
			if b.tripCount() != 0 {
				t.Errorf("trips = %d on a disabled breaker", b.tripCount())
			}
		})
	}
}
