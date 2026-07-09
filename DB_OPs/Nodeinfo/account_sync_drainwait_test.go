package NodeInfo

// Tests for drain confirmation (F5, PROBE D): stream-ID ordering, HWM
// monotonicity, and the pure confirmation decision. Every ambiguous input must
// decide NOT-confirmed — the anchor-lag direction.

import (
	"context"
	"testing"
	"time"
)

func TestParseStreamID(t *testing.T) {
	cases := []struct {
		in       string
		ms, seq  uint64
		ok       bool
	}{
		{"1783583380000-0", 1783583380000, 0, true},
		{"1783583380000-42", 1783583380000, 42, true},
		{"0-1", 0, 1, true},
		{"", 0, 0, false},
		{"17835-", 0, 0, false},
		{"-5", 0, 0, false},
		{"abc-1", 0, 0, false},
		{"1-x", 0, 0, false},
		{"1783583380000", 0, 0, false},
	}
	for _, c := range cases {
		ms, seq, ok := parseStreamID(c.in)
		if ok != c.ok || ms != c.ms || seq != c.seq {
			t.Errorf("parseStreamID(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, ms, seq, ok, c.ms, c.seq, c.ok)
		}
	}
}

func TestStreamIDGTE(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"100-0", "100-0", true},
		{"100-1", "100-0", true},
		{"100-0", "100-1", false},
		{"101-0", "100-99", true},
		{"99-99", "100-0", false},
		// Malformed on either side = NOT gte (fail toward not-confirmed):
		{"bad", "100-0", false},
		{"100-0", "bad", false},
	}
	for _, c := range cases {
		if got := streamIDGTE(c.a, c.b); got != c.want {
			t.Errorf("streamIDGTE(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDrainConfirmed(t *testing.T) {
	cases := []struct {
		name            string
		drained, target string
		want            bool
	}{
		{"empty target = direct write path, trivially confirmed", "", "", true},
		{"empty target with drain progress", "500-0", "", true},
		{"drained past target", "200-0", "100-5", true},
		{"drained exactly target", "100-5", "100-5", true},
		{"drained behind target", "100-4", "100-5", false},
		{"nothing drained yet (worker restart)", "", "100-0", false},
	}
	for _, c := range cases {
		if got := drainConfirmed(c.drained, c.target); got != c.want {
			t.Errorf("%s: drainConfirmed(%q, %q) = %v, want %v", c.name, c.drained, c.target, got, c.want)
		}
	}
}

// resetDrainProgress isolates HWM state between tests (package-level atomics).
func resetDrainProgress() {
	drainProgress.mu.Lock()
	defer drainProgress.mu.Unlock()
	drainProgress.lastEnqueued = ""
	drainProgress.lastDrained = ""
}

func TestHWMMonotonicity(t *testing.T) {
	resetDrainProgress()
	defer resetDrainProgress()

	noteEnqueuedID("100-0")
	noteEnqueuedID("99-5") // out-of-order note must not regress the HWM
	if got := LastAccountEnqueueID(); got != "100-0" {
		t.Fatalf("enqueue HWM regressed: %q", got)
	}
	noteEnqueuedID("100-1")
	if got := LastAccountEnqueueID(); got != "100-1" {
		t.Fatalf("enqueue HWM did not advance: %q", got)
	}

	noteDrainedIDs([]string{"50-0", "60-3", "55-1"})
	drainProgress.mu.Lock()
	drained := drainProgress.lastDrained
	drainProgress.mu.Unlock()
	if drained != "60-3" {
		t.Fatalf("drain HWM = %q, want 60-3 (max of batch)", drained)
	}
}

func TestWaitForAccountQueueDrain(t *testing.T) {
	resetDrainProgress()
	defer resetDrainProgress()

	// Empty target returns immediately.
	if err := WaitForAccountQueueDrain(context.Background(), ""); err != nil {
		t.Fatalf("empty target must confirm immediately: %v", err)
	}

	// Unreached target times out with NOT-confirmed.
	noteEnqueuedID("100-0")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := WaitForAccountQueueDrain(ctx, "100-0"); err == nil {
		t.Fatal("undrained target must NOT confirm")
	}

	// Reached target confirms.
	noteDrainedIDs([]string{"100-0"})
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := WaitForAccountQueueDrain(ctx2, "100-0"); err != nil {
		t.Fatalf("drained target must confirm: %v", err)
	}
}
