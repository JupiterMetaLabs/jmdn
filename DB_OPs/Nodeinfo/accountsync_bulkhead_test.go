package NodeInfo

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gossipnode/config"
)

// prodPoolErr reproduces the exact wrapping chain seen in the sequencer log:
// "GetAccountsByNonces scan: failed to get connection from pool: failed to get
// accounts connection: maximum number of connections reached - ..."
func prodPoolErr() error {
	e := fmt.Errorf("failed to get accounts connection: %w - GetAccountConnectionandPutBack", config.ErrMaxConnectionsReached)
	return fmt.Errorf("failed to get connection from pool: %w - ListAccountsPaginatedFrom", e)
}

func testLimiter(capacity int) (*scanLimiter, *[]time.Duration) {
	var slept []time.Duration
	l := newScanLimiter(capacity)
	l.sleep = func(d time.Duration) { slept = append(slept, d) }
	return l, &slept
}

func TestScanLimiter_BoundsConcurrency(t *testing.T) {
	const capacity, callers = 3, 50
	l := newScanLimiter(capacity)
	var inFlight, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.do(func() error {
				n := inFlight.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond)
				inFlight.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	if got := peak.Load(); got != capacity {
		t.Fatalf("peak concurrent scans = %d, want exactly %d", got, capacity)
	}
}

func TestScanLimiter_RetriesPoolExhaustionThenSucceeds(t *testing.T) {
	l, slept := testLimiter(1)
	calls := 0
	err := l.do(func() error {
		calls++
		if calls < 3 {
			return prodPoolErr()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}
	if fmt.Sprint(*slept) != fmt.Sprint(want) {
		t.Fatalf("backoff = %v, want %v", *slept, want)
	}
}

func TestScanLimiter_GivesUpAfterMaxAttempts(t *testing.T) {
	l, slept := testLimiter(1)
	calls := 0
	err := l.do(func() error { calls++; return prodPoolErr() })
	if !errors.Is(err, config.ErrMaxConnectionsReached) {
		t.Fatalf("err = %v, want ErrMaxConnectionsReached", err)
	}
	if calls != poolBusyMaxAttempts {
		t.Fatalf("calls = %d, want %d", calls, poolBusyMaxAttempts)
	}
	var total time.Duration
	for _, d := range *slept {
		total += d
	}
	if len(*slept) != poolBusyMaxAttempts-1 || total != 1550*time.Millisecond {
		t.Fatalf("slept %v (total %v), want 5 sleeps totalling 1.55s", *slept, total)
	}
}

func TestScanLimiter_DoesNotRetryOtherErrors(t *testing.T) {
	l, slept := testLimiter(1)
	other := errors.New("failed to scan for accounts: deadline exceeded")
	calls := 0
	if err := l.do(func() error { calls++; return other }); !errors.Is(err, other) {
		t.Fatalf("err = %v, want %v", err, other)
	}
	if calls != 1 || len(*slept) != 0 {
		t.Fatalf("calls=%d sleeps=%d, want 1 and 0", calls, len(*slept))
	}
}

func TestScanLimiter_ReleasesSlotOnError(t *testing.T) {
	l, _ := testLimiter(1)
	_ = l.do(func() error { return errors.New("boom") })
	done := make(chan struct{})
	go func() { _ = l.do(func() error { return nil }); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slot not released after error")
	}
}

func TestAccountSyncDBSlots_Env(t *testing.T) {
	cases := map[string]int{"": 4, "8": 8, "1": 1, "16": 16, "0": 4, "17": 4, "-3": 4, "abc": 4, " 6 ": 6}
	for raw, want := range cases {
		t.Setenv(accountSyncDBSlotsEnv, raw)
		if got := accountSyncDBSlots(); got != want {
			t.Errorf("%s=%q → %d, want %d", accountSyncDBSlotsEnv, raw, got, want)
		}
	}
}
