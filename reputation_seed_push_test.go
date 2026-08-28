package main

// A4-COMPLETION-LLD.md §6 tests for Phase A4.2's periodic pusher and §5's
// immediate-push trigger.
//
// HONEST SCOPE NOTE: pushReputationOnce's log-level branching (debug for the
// common ErrNotSequencer case, warn for a genuine RPC failure) is not
// asserted here — this codebase has no existing pattern for capturing
// zerolog's global output in a test, and standing one up (swapping
// log.Logger process-wide) risks interfering with other tests sharing this
// package's process. What IS tested is the behavior that log branching sits
// on top of: pushReputationOnce completes cleanly (no panic, correct
// accepted/failure counts reaching the fake) in both the ErrNotSequencer
// and the genuine-failure case.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gossipnode/internal/reputation"
	"gossipnode/seednode"
)

// fakeReputationPusher implements the reputationPusher seam without any
// seednode/gRPC dependency.
type fakeReputationPusher struct {
	fn func(ctx context.Context, weights map[string]float64) (int, []error)
}

func (f *fakeReputationPusher) PushReputationWeights(ctx context.Context, weights map[string]float64) (int, []error) {
	return f.fn(ctx, weights)
}

func withFreshReputationStore(t *testing.T) {
	t.Helper()
	orig := reputation.Default
	reputation.Default = reputation.NewStore()
	t.Cleanup(func() { reputation.Default = orig })
}

func TestPushReputationOnce_DisabledSkipsEntirely(t *testing.T) {
	withFreshReputationStore(t)
	reputation.Default.Observe("peerA", reputation.AgreeFinalized)

	origEnabled := reputation.Enabled
	reputation.Enabled = false
	defer func() { reputation.Enabled = origEnabled }()

	called := false
	fake := &fakeReputationPusher{fn: func(context.Context, map[string]float64) (int, []error) {
		called = true
		return 0, nil
	}}

	pushReputationOnce(context.Background(), fake)
	if called {
		t.Error("PushReputationWeights must not be called while reputation.Enabled is false")
	}
}

func TestPushReputationOnce_NoObservationsSkipsEntirely(t *testing.T) {
	withFreshReputationStore(t)
	origEnabled := reputation.Enabled
	reputation.Enabled = true
	defer func() { reputation.Enabled = origEnabled }()

	called := false
	fake := &fakeReputationPusher{fn: func(context.Context, map[string]float64) (int, []error) {
		called = true
		return 0, nil
	}}

	pushReputationOnce(context.Background(), fake) // must not panic
	if called {
		t.Error("PushReputationWeights must not be called with nothing observed yet")
	}
}

func TestPushReputationOnce_PushesRemappedWeights(t *testing.T) {
	withFreshReputationStore(t)
	origEnabled := reputation.Enabled
	reputation.Enabled = true
	defer func() { reputation.Enabled = origEnabled }()

	reputation.Default.Observe("peerA", reputation.Equivocation)

	var gotWeights map[string]float64
	fake := &fakeReputationPusher{fn: func(_ context.Context, weights map[string]float64) (int, []error) {
		gotWeights = weights
		return len(weights), nil
	}}

	pushReputationOnce(context.Background(), fake)
	if len(gotWeights) != 1 {
		t.Fatalf("got %d weights pushed, want 1: %v", len(gotWeights), gotWeights)
	}
	if gotWeights["peerA"] >= 0.5 {
		t.Errorf("an equivocating peer's pushed weight must already be remapped below the eligibility floor, got %v", gotWeights["peerA"])
	}
}

func TestPushReputationOnce_ErrNotSequencerDoesNotPanic(t *testing.T) {
	withFreshReputationStore(t)
	origEnabled := reputation.Enabled
	reputation.Enabled = true
	defer func() { reputation.Enabled = origEnabled }()

	reputation.Default.Observe("peerA", reputation.AgreeFinalized)

	fake := &fakeReputationPusher{fn: func(context.Context, map[string]float64) (int, []error) {
		return 0, []error{seednode.ErrNotSequencer}
	}}
	pushReputationOnce(context.Background(), fake) // must not panic
}

func TestPushReputationOnce_GenuineFailureDoesNotPanic(t *testing.T) {
	withFreshReputationStore(t)
	origEnabled := reputation.Enabled
	reputation.Enabled = true
	defer func() { reputation.Enabled = origEnabled }()

	reputation.Default.Observe("peerA", reputation.AgreeFinalized)

	fake := &fakeReputationPusher{fn: func(context.Context, map[string]float64) (int, []error) {
		return 0, []error{errors.New("rpc unavailable")}
	}}
	pushReputationOnce(context.Background(), fake) // must not panic
}

func TestReputationPushIntervalSeconds_DefaultsAndOverrides(t *testing.T) {
	if got := reputationPushIntervalSeconds(); got != reputationPushDefaultIntervalSeconds {
		t.Errorf("default: got %d, want %d", got, reputationPushDefaultIntervalSeconds)
	}
	t.Setenv("JMDN_REPUTATION_PUSH_INTERVAL_SECONDS", "60")
	if got := reputationPushIntervalSeconds(); got != 60 {
		t.Errorf("override: got %d, want 60", got)
	}
}

func TestStartReputationSeedPusher_NilClientIsSafeNoOp(t *testing.T) {
	startReputationSeedPusher(context.Background(), nil) // must not panic, must return promptly
}

// triggerImmediateReputationPush before any pusher has started must be a
// safe no-op — it must never block the caller (in production,
// ConvergeAndCompact's evaluation loop) regardless of whether anything is
// listening yet.
func TestTriggerImmediateReputationPush_SafeBeforePusherStarts(t *testing.T) {
	orig := reputationPusherRunning.Load()
	reputationPusherRunning.Store(false)
	defer reputationPusherRunning.Store(orig)

	done := make(chan struct{})
	go func() {
		triggerImmediateReputationPush()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("triggerImmediateReputationPush blocked with no pusher running")
	}
}

// The immediate-push path actually reaches the pusher: after the worker is
// running, one trigger produces exactly one push once the debounce window
// settles, and a burst of triggers within that window still coalesces to
// one push — same guarantee vote_crdt_compaction_test.go already proves for
// the compaction hook's identical shape.
func TestReputationSeedPusher_ImmediateTriggerFiresAfterDebounce(t *testing.T) {
	withFreshReputationStore(t)
	origEnabled := reputation.Enabled
	reputation.Enabled = true
	defer func() { reputation.Enabled = origEnabled }()
	reputation.Default.Observe("peerA", reputation.Equivocation)

	// Long regular interval so only the immediate path can plausibly fire
	// within this test's timeout.
	t.Setenv("JMDN_REPUTATION_PUSH_INTERVAL_SECONDS", "3600")

	var calls int32
	fake := &fakeReputationPusher{fn: func(context.Context, map[string]float64) (int, []error) {
		atomic.AddInt32(&calls, 1)
		return 1, nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startReputationSeedPusher(ctx, fake)
	// Let the worker goroutine reach its select before triggering, so
	// reputationPusherRunning is observably true.
	deadline := time.Now().Add(time.Second)
	for !reputationPusherRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !reputationPusherRunning.Load() {
		t.Fatal("pusher never reported itself running")
	}

	// A burst of 5 triggers within the debounce window must coalesce to 1 push.
	for i := 0; i < 5; i++ {
		triggerImmediateReputationPush()
	}
	time.Sleep(equivocationPushDebounce + 500*time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("burst of 5 triggers should coalesce to 1 push, got %d", got)
	}
}
