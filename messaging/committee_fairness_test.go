package messaging

import (
	"math"
	"testing"
)

// TestSeatFrequencyIsUniform checks that the Stage 1 draw is FAIR, not merely
// deterministic.
//
// Determinism and fairness are separate properties and the old code had the
// first without the second: the alphabetical cap gave every node the same
// answer every time, and that answer was always the same seven peers. A draw
// that reproducibly favoured peer-00 would pass every agreement test in this
// package and still be broken.
//
// # Reading the bound
//
// With uniform weights each peer's seat count is Binomial(heights, k/n). At
// heights=20000, k=7, n=20 that is mean 7000 and sd sqrt(20000*0.35*0.65) ~= 67,
// so one peer's expected relative deviation is ~0.96%. Taking the WORST of 20
// peers pulls roughly 2-2.5 sd, i.e. ~2-2.4%. The 5% bound therefore passes
// honest sampling noise comfortably while still failing a draw with real bias -
// a peer seated 10% more often than its share would trip it.
//
// This is a statistical test with a fixed seed sequence, so it is deterministic:
// it cannot flake, and if it ever fails the distribution genuinely moved.
func TestSeatFrequencyIsUniform(t *testing.T) {
	const pool, seats, heights = 20, 7, 20000
	wireEligibility(t, pool)
	enableV2(t)

	count := make(map[string]int, pool)
	for h := range uint64(heights) {
		ms, err := SelectCommittee(round(h, 0))
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) != seats {
			t.Fatalf("height %d seated %d members, want %d", h, len(ms), seats)
		}
		for _, m := range ms {
			count[m.PeerID]++
		}
	}

	want := float64(heights) * float64(seats) / float64(pool)
	worst, worstID, total := 0.0, "", 0
	for id, c := range count {
		total += c
		if d := math.Abs(float64(c)-want) / want; d > worst {
			worst, worstID = d, id
		}
	}

	// Every round must issue exactly k seats. A mismatch means the draw is
	// dropping or double-counting members, which would silently change n and
	// therefore the threshold.
	if total != heights*seats {
		t.Fatalf("seat accounting: %d seats issued over %d heights, expected %d",
			total, heights, heights*seats)
	}
	if len(count) != pool {
		t.Fatalf("only %d of %d peers were ever seated - the draw is not covering the pool",
			len(count), pool)
	}
	if worst > 0.05 {
		t.Fatalf("worst seat-frequency deviation %.4f (%s) exceeds 5%% - the draw is biased",
			worst, worstID)
	}
	t.Logf("pool=%d k=%d over %d heights: %.0f seats expected each, worst deviation %.4f (%s)",
		pool, seats, heights, want, worst, worstID)
}
