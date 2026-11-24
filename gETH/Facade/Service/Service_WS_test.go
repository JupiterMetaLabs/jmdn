package Service

import (
	"context"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fastPoll(t *testing.T) {
	t.Helper()
	prev := newHeadsPollInterval
	newHeadsPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { newHeadsPollInterval = prev })
}

// Subscribe/unsubscribe cycles must never leave more than one poller running,
// and the poller must stop once the last subscriber leaves.
func TestNewHeads_SinglePollerAcrossCycles(t *testing.T) {
	fastPoll(t)
	s := &ServiceImpl{}
	for i := 0; i < 3; i++ {
		_, stop, err := s.SubscribeNewHeads(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "poller start", func() bool { return activeNewHeadsPollers.Load() == 1 })
		stop()
		stop() // idempotent
		waitFor(t, "poller stop", func() bool { return activeNewHeadsPollers.Load() == 0 })
	}
	if n := GetActiveSubscriptionsCount(); n != 0 {
		t.Fatalf("subscriptions left registered: %d", n)
	}
}

// Cancelling the caller's context releases the subscription and closes its
// channel even if the caller never calls stop.
func TestNewHeads_ContextCancelReleases(t *testing.T) {
	fastPoll(t)
	s := &ServiceImpl{}
	ctx, cancel := context.WithCancel(context.Background())
	ch, _, err := s.SubscribeNewHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitFor(t, "subscription removal", func() bool { return GetActiveSubscriptionsCount() == 0 })
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected the channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after context cancel")
	}
	waitFor(t, "poller stop", func() bool { return activeNewHeadsPollers.Load() == 0 })
}
