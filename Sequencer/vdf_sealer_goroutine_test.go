package Sequencer

// Goroutine-accounting evidence for the Start latch.
//
// WHY THIS FILE EXISTS: vdf_sealer_latch_test.go and vdf_sealer_cancel_test.go
// are named for the latch and the cancellation path, but between them they
// contain eight test functions and ZERO calls to Start — so neither actually
// exercises the thing the latch protects. The latch itself is correct
// (vdf_sealer.go:83-94 returns without launching a goroutine when s.cancel is
// already set), but nothing in the suite would have failed if it were not.
//
// The defect the latch prevents is specific: Start's goroutine ends in a send
// on resultCh, which has capacity 1 (NewVDFSealer). A second goroutine for the
// same sealer would block forever on that send on every node that never drains
// the channel — which is every node except the epoch-boundary proposer, since
// Result() is only called there. It would hold the sealer's *beacon.Pipeline,
// and its RSA-modulus big.Ints, for the process lifetime.
//
// These tests assert on goroutine counts rather than on internal state, so
// they keep working if the latch is reimplemented.

import (
	"runtime"
	"testing"
	"time"

	"github.com/JupiterMetaLabs/avc/randao"
)

// settleGoroutines waits for the scheduler to quiesce, then reports the count.
// A bare NumGoroutine() immediately after Start is racy: the new goroutine may
// not be scheduled yet, and a finished one may not be reaped yet. Polling for a
// stable value is what makes the assertion meaningful rather than flaky.
func settleGoroutines(t testing.TB) int {
	t.Helper()
	prev := -1
	stable := 0
	for i := 0; i < 200; i++ {
		n := runtime.NumGoroutine()
		if n == prev {
			if stable++; stable >= 3 {
				return n
			}
		} else {
			prev, stable = n, 0
		}
		time.Sleep(5 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// A second Start on the same sealer must launch NO second goroutine. Without
// the latch this test sees a permanent +1 — the goroutine blocked forever on
// the capacity-1 resultCh.
func TestStartTwiceLaunchesOneGoroutine(t *testing.T) {
	p, _ := testPipeline(t)
	s := NewVDFSealer(p)
	mix := randao.Seed{0x01, 0x02, 0x03}

	base := settleGoroutines(t)

	s.Start(testEpoch, mix)
	// Do NOT drain resultCh: an undrained channel plus a duplicate Start is
	// what leaks. Note the FIRST goroutine does not park — resultCh is empty,
	// so its send succeeds and it exits. Only a SECOND goroutine finds the
	// buffer full and blocks there forever.
	afterFirst := settleGoroutines(t)

	s.Start(testEpoch, mix) // duplicate — must be a no-op
	afterSecond := settleGoroutines(t)

	t.Logf("goroutines: base=%d afterFirst=%d afterSecond=%d", base, afterFirst, afterSecond)

	// Compared against BASE, not afterFirst. afterFirst is racy — it catches the
	// first goroutine either mid-evaluation (base+1) or after it has exited
	// (base) — and comparing to it makes the assertion insensitive: a leaked
	// second goroutine also sits at base+1, so `afterSecond > afterFirst` is
	// false in exactly the case the test exists to catch.
	//
	// The tolerance of 1 covers the legitimate first goroutine still finishing
	// its persistence calls. A per-call leak exceeds it immediately, and
	// TestRepeatedStartLaunchesOneGoroutine below pins that harder with 8 calls.
	if afterSecond-base > 1 {
		t.Fatalf("duplicate Start leaked goroutines: base=%d afterSecond=%d (the latch at "+
			"vdf_sealer.go:83-94 is not holding; a goroutine is now blocked forever on the "+
			"capacity-1 resultCh, pinning the sealer's *beacon.Pipeline and its RSA-modulus "+
			"big.Ints for the process lifetime)", base, afterSecond)
	}
}

// Many duplicate Starts must still launch exactly one goroutine. Guards against
// a latch that only handles the second call.
func TestRepeatedStartLaunchesOneGoroutine(t *testing.T) {
	p, _ := testPipeline(t)
	s := NewVDFSealer(p)
	mix := randao.Seed{0xAA, 0xBB}

	base := settleGoroutines(t)
	for i := 0; i < 8; i++ {
		s.Start(testEpoch, mix)
	}
	after := settleGoroutines(t)

	t.Logf("goroutines after 8 Starts: base=%d after=%d (delta=%d)", base, after, after-base)

	// One parked sender is expected; anything beyond that is a leak per call.
	if after-base > 1 {
		t.Fatalf("8 Starts leaked %d goroutines; expected at most 1 parked sender", after-base)
	}
}

// The result of the single evaluation must still be delivered after duplicate
// Starts — i.e. the latch must not cost correctness. Complements the leak
// assertions above: a latch that dropped the real evaluation would pass those
// and fail this.
func TestDuplicateStartStillDeliversOneResult(t *testing.T) {
	p, _ := testPipeline(t)
	s := NewVDFSealer(p)
	mix := randao.Seed{0x11, 0x22}

	s.Start(testEpoch, mix)
	s.Start(testEpoch, mix)

	res := waitForResult(t, s)
	if res.Err != nil {
		t.Fatalf("sealing failed after a duplicate Start: %v", res.Err)
	}
	if res.ForEpoch != testEpoch {
		t.Fatalf("result carries epoch %d, want %d", res.ForEpoch, testEpoch)
	}

	// Result is DELIBERATELY idempotent (D-31, vdf_sealer.go's own doc: "the
	// first successful receive stores the result, and every later call replays
	// it"). It used to be single-shot, which permanently broke any re-build of
	// the same boundary block. So a second call must replay the SAME result —
	// asserting not-ready here would be asserting the regression.
	again, ok := s.Result()
	if !ok {
		t.Fatal("Result reported not-ready on a second call — the D-31 latch has regressed to " +
			"single-shot, so any re-propose of this epoch's boundary block hits " +
			"ErrVDFProofNotReady permanently")
	}
	if again.ForEpoch != res.ForEpoch || again.Err != res.Err {
		t.Fatalf("the replayed result differs from the first: %+v vs %+v", again, res)
	}

	// THE ACTUAL LATCH ASSERTION. Idempotent replay cannot by itself reveal a
	// duplicate evaluation, because Result returns the latched value either
	// way. But resultCh has capacity 1 and the latch consumed the only queued
	// value, so a second goroutine's send would still be sitting in the buffer.
	// A non-empty channel here is direct evidence that the duplicate Start ran
	// its own evaluation.
	//
	// Settle first: a duplicate goroutine blocks on the FULL buffer until the
	// latch above drains it, so its send lands only just after waitForResult
	// returns. Reading len() immediately would race that send and report 0 in
	// exactly the failing case.
	settleGoroutines(t)
	if n := len(s.resultCh); n != 0 {
		t.Fatalf("%d result(s) buffered behind the latch — the duplicate Start launched its own "+
			"evaluation and delivered a second result", n)
	}
}
