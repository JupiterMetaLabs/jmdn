package main

// Stage 6 tests, per docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md §8.
//
// newVoteCRDTCompactionHook's own coalescing/cancellation behavior is tested
// here, mirroring seed_blockhead_push_test.go exactly (same hand-rolled
// debounce shape, same reasons to test it: a burst of applied blocks during
// catch-up must not spawn one compaction pass per block, and the worker must
// not leak past context cancellation). compactConvergedVotes itself — the
// production trigger — is not covered here: it depends on live global state
// (AVCStruct.NewGlobalVariables().Get_ForListner(), messaging.AuthorizedCommittee)
// that has no test harness in this package, same honest-scope limitation
// Vote/vote_crdt_v2_test.go already documents for SubmitVote.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gossipnode/internal/reputation"
)

// A burst of applied blocks must collapse to exactly one compaction pass
// over the final tip, and a later apply after the quiet window must
// produce one more.
func TestVoteCRDTCompactionHook_CoalescesBurstAndPassesFinalTip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	var lastTip, lastK atomic.Uint64
	hook := newVoteCRDTCompactionHook(ctx, func(_ context.Context, tip, k uint64) {
		atomic.AddInt32(&calls, 1)
		lastTip.Store(tip)
		lastK.Store(k)
	}, 15*time.Millisecond, 128)

	for i := uint64(1); i <= 50; i++ {
		hook(i)
	}
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("burst of 50 applies should coalesce to 1 trigger, got %d", got)
	}
	if got := lastTip.Load(); got != 50 {
		t.Fatalf("coalesced trigger should carry the FINAL tip (50), got %d", got)
	}
	if got := lastK.Load(); got != 128 {
		t.Fatalf("expected k=128 threaded through, got %d", got)
	}

	hook(100)
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("second apply after the window should trigger once more, got %d", got)
	}
	if got := lastTip.Load(); got != 100 {
		t.Fatalf("expected tip=100 on the second trigger, got %d", got)
	}
}

// The worker goroutine must stop when its context is cancelled, never
// running a compaction pass after shutdown has begun.
func TestVoteCRDTCompactionHook_StopsOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	hook := newVoteCRDTCompactionHook(ctx, func(context.Context, uint64, uint64) {
		atomic.AddInt32(&calls, 1)
	}, 15*time.Millisecond, 128)

	cancel()
	hook(1)
	time.Sleep(60 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("no trigger expected after ctx cancel, got %d", got)
	}
}

// A nil trigger yields a safe no-op hook — never reachable in production
// (startVoteCRDTCompactionHook always passes compactConvergedVotes), kept
// as the same defensive shape newSeedBlockHeadPusher has.
func TestVoteCRDTCompactionHook_NilTriggerSafe(t *testing.T) {
	hook := newVoteCRDTCompactionHook(context.Background(), nil, time.Millisecond, 128)
	hook(1) // must not panic
}

func TestEnvUint64_DefaultsAndOverrides(t *testing.T) {
	const key = "JMDN_VOTE_CRDT_COMPACT_K_TEST_ONLY"

	if got := envUint64(key, 128); got != 128 {
		t.Errorf("unset var: got %d, want default 128", got)
	}

	t.Setenv(key, "256")
	if got := envUint64(key, 128); got != 256 {
		t.Errorf("set to 256: got %d, want 256", got)
	}

	t.Setenv(key, "not-a-number")
	if got := envUint64(key, 128); got != 128 {
		t.Errorf("unparseable value should fall back to default 128, got %d", got)
	}
}

// equivocationReputationReporter must respect reputation.Enabled (the same
// kill switch every other reputation.Observe call site in this codebase
// checks) rather than always reporting.
func TestEquivocationReputationReporter_RespectsEnabledFlag(t *testing.T) {
	orig := reputation.Enabled
	defer func() { reputation.Enabled = orig }()

	reputation.Enabled = false
	disabledPeer := "peerX-stage6-disabled-test"
	equivocationReputationReporter{}.ReportEquivocation(disabledPeer, "0xdead", 42, []int8{1, -1})
	if got := reputation.Default.Score(disabledPeer); got != reputation.Start {
		t.Fatalf("disabled reporter must not touch the score, got %v want Start=%v", got, reputation.Start)
	}

	reputation.Enabled = true
	enabledPeer := "peerX-stage6-enabled-test"
	equivocationReputationReporter{}.ReportEquivocation(enabledPeer, "0xdead", 42, []int8{1, -1})
	if got := reputation.Default.Score(enabledPeer); got >= reputation.Start {
		t.Fatalf("enabled reporter must apply the Equivocation penalty, score stayed at/above Start: got %v", got)
	}
}
