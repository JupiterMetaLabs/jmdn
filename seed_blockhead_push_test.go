package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A burst of applied blocks must collapse to exactly one seednode report, and a
// later apply after the quiet window must produce one more.
func TestSeedBlockHeadPusher_CoalescesBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	hook := newSeedBlockHeadPusher(ctx, func(context.Context) {
		atomic.AddInt32(&calls, 1)
	}, 15*time.Millisecond)

	// Burst of 50 applies within one debounce window.
	for i := uint64(1); i <= 50; i++ {
		hook(i)
	}
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("burst of 50 applies should coalesce to 1 trigger, got %d", got)
	}

	// A later apply triggers exactly one more report.
	hook(100)
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("second apply after the window should trigger once more, got %d", got)
	}
}

// The worker goroutine must stop when its context is cancelled.
func TestSeedBlockHeadPusher_StopsOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	hook := newSeedBlockHeadPusher(ctx, func(context.Context) {
		atomic.AddInt32(&calls, 1)
	}, 15*time.Millisecond)

	cancel()
	hook(1) // signalled after cancel — worker should exit without triggering
	time.Sleep(60 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("no trigger expected after ctx cancel, got %d", got)
	}
}

// A nil trigger (node with no sync monitor) yields a safe no-op hook.
func TestSeedBlockHeadPusher_NilTriggerSafe(t *testing.T) {
	hook := newSeedBlockHeadPusher(context.Background(), nil, time.Millisecond)
	hook(1) // must not panic
}
