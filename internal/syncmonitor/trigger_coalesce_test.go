package syncmonitor

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func newTestMonitor(cooldown time.Duration) *Monitor {
	return &Monitor{rootCtx: context.Background(), triggerCooldown: cooldown}
}

// A burst of gap signals while a run is in progress must collapse into one
// queued run, not one goroutine (and one seednode round trip) per signal.
func TestRequestRun_BurstCoalesces(t *testing.T) {
	m := newTestMonitor(0)
	release := make(chan struct{})
	var runs atomic.Int32
	run := func(context.Context) {
		runs.Add(1)
		<-release
	}

	m.requestRun(&m.gapQueued, run)
	waitFor(t, func() bool { return runs.Load() == 1 })

	before := runtime.NumGoroutine()
	for i := 0; i < 5000; i++ {
		m.requestRun(&m.gapQueued, run)
	}
	if extra := runtime.NumGoroutine() - before; extra > 1 {
		t.Fatalf("burst spawned %d goroutines, want at most 1", extra)
	}

	close(release)
	waitFor(t, func() bool { return runs.Load() == 2 })
	time.Sleep(50 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2 (in-progress + one queued)", got)
	}
}

// Triggered runs are spaced by the cooldown.
func TestRequestRun_Cooldown(t *testing.T) {
	const cd = 150 * time.Millisecond
	m := newTestMonitor(cd)
	var stamps []time.Time
	done := make(chan struct{}, 2)
	run := func(context.Context) {
		stamps = append(stamps, time.Now())
		done <- struct{}{}
	}
	m.requestRun(&m.gapQueued, run)
	<-done
	m.requestRun(&m.gapQueued, run)
	<-done
	if gap := stamps[1].Sub(stamps[0]); gap < cd-10*time.Millisecond {
		t.Fatalf("runs %v apart, want >= %v", gap, cd)
	}
}

// A queued run waiting out the cooldown exits when the node shuts down.
func TestRequestRun_CancelWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Monitor{rootCtx: ctx, triggerCooldown: time.Hour, lastTriggered: time.Now()}
	var runs atomic.Int32
	m.requestRun(&m.gapQueued, func(context.Context) { runs.Add(1) })
	cancel()
	waitFor(t, func() bool { return !m.gapQueued.Load() })
	if runs.Load() != 0 {
		t.Fatal("run executed after cancellation")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
