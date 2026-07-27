// Tests for the observe-only reputation model. The properties
// under test are the SAFETY properties of the design, not just arithmetic:
//
//  1. Dissent is NEVER penalized (no rubber-stamp incentive).
//  2. Correct dissent (reject on a failed block) is rewarded.
//  3. Only objective faults (absence, bad sig, equivocation) lose score.
//  4. The floor holds: no fault sequence drives a score below Floor.
//  5. Decay heals: a penalized peer recovers toward Start with time.
package reputation

import (
	"math"
	"testing"
	"time"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDeltaTable(t *testing.T) {
	cases := []struct {
		e    Event
		want float64
	}{
		{AgreeFinalized, +0.02},
		{RejectNotFinalized, +0.02},
		{MinorityDissent, 0},
		{Absent, -0.10},
		{BadSignature, -0.30},
		{Equivocation, -0.50},
		{Event("unknown"), 0},
	}
	for _, c := range cases {
		if got := Delta(c.e); !almost(got, c.want) {
			t.Errorf("Delta(%s) = %v, want %v", c.e, got, c.want)
		}
	}
}

func TestClampBounds(t *testing.T) {
	if got := clamp(-1); !almost(got, Floor) {
		t.Errorf("clamp(-1) = %v, want Floor %v", got, Floor)
	}
	if got := clamp(2); !almost(got, Cap) {
		t.Errorf("clamp(2) = %v, want Cap %v", got, Cap)
	}
	if got := clamp(0.42); !almost(got, 0.42) {
		t.Errorf("clamp(0.42) = %v, want 0.42", got)
	}
}

func TestDecayPullsTowardStart(t *testing.T) {
	// One epoch keeps 90% of the distance from Start.
	if got := DecayScore(1.0, 1); !almost(got, Start+(1.0-Start)*0.9) {
		t.Errorf("DecayScore(1.0, 1 epoch) = %v", got)
	}
	if got := DecayScore(0.10, 1); !almost(got, Start+(0.10-Start)*0.9) {
		t.Errorf("DecayScore(0.10, 1 epoch) = %v", got)
	}
	// No time → no change; Start is a fixed point.
	if got := DecayScore(0.8, 0); !almost(got, 0.8) {
		t.Errorf("DecayScore(0.8, 0) = %v, want 0.8", got)
	}
	if got := DecayScore(Start, 100); !almost(got, Start) {
		t.Errorf("DecayScore(Start, 100) = %v, want Start", got)
	}
	// Many epochs → converges to Start from both sides.
	if got := DecayScore(1.0, 200); math.Abs(got-Start) > 1e-6 {
		t.Errorf("DecayScore(1.0, 200) = %v, want ≈Start", got)
	}
}

func TestClassifyRoundTruthTable(t *testing.T) {
	committee := []string{"A", "B", "C", "D", "E"}
	votes := map[string]bool{
		"A": true,  // agree
		"B": false, // reject
		"C": true,
		// D absent
		"E": false,
	}

	// Finalized round: agree→AgreeFinalized, reject→MinorityDissent (0),
	// absent→Absent.
	got := ClassifyRound(committee, votes, true)
	want := map[string]Event{
		"A": AgreeFinalized,
		"B": MinorityDissent,
		"C": AgreeFinalized,
		"D": Absent,
		"E": MinorityDissent,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("finalized: %s = %s, want %s", id, got[id], w)
		}
	}

	// Failed round: reject→RejectNotFinalized (+), agree→MinorityDissent (0).
	got = ClassifyRound(committee, votes, false)
	want = map[string]Event{
		"A": MinorityDissent,
		"B": RejectNotFinalized,
		"C": MinorityDissent,
		"D": Absent,
		"E": RejectNotFinalized,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("not finalized: %s = %s, want %s", id, got[id], w)
		}
	}
}

func TestClassifyIgnoresNonCommitteeVotes(t *testing.T) {
	// A vote from a peer outside the committee must not be classified.
	got := ClassifyRound([]string{"A"}, map[string]bool{"A": true, "X": false}, true)
	if len(got) != 1 {
		t.Fatalf("classified %d peers, want 1 (non-committee vote leaked in)", len(got))
	}
	if _, ok := got["X"]; ok {
		t.Fatal("non-committee peer X was classified")
	}
}

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func TestStoreStartsAtStartAndApplies(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := NewStoreWithClock(fixedClock(now))
	old, cur := s.Observe("P", AgreeFinalized)
	if !almost(old, Start) {
		t.Errorf("first observation old = %v, want Start", old)
	}
	if !almost(cur, Start+0.02) {
		t.Errorf("after AgreeFinalized = %v, want %v", cur, Start+0.02)
	}
}

// SAFETY PROPERTY 1: dissent against a finalized outcome costs nothing.
func TestDissentNeverPenalized(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := NewStoreWithClock(fixedClock(now))
	for i := 0; i < 50; i++ {
		if _, cur := s.Observe("dissenter", MinorityDissent); !almost(cur, Start) {
			t.Fatalf("round %d: persistent dissenter score = %v, want unchanged Start %v", i, cur, Start)
		}
	}
}

// SAFETY PROPERTY 2: rejecting a block that failed to finalize is REWARDED.
func TestCorrectRejectRewarded(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := NewStoreWithClock(fixedClock(now))
	_, cur := s.Observe("P", RejectNotFinalized)
	if cur <= Start {
		t.Errorf("correct reject score = %v, want > Start", cur)
	}
}

// SAFETY PROPERTY 3: absence is penalized more than a round's reward.
func TestAbsentPenalized(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := NewStoreWithClock(fixedClock(now))
	_, cur := s.Observe("P", Absent)
	if !almost(cur, Start-0.10) {
		t.Errorf("after Absent = %v, want %v", cur, Start-0.10)
	}
}

// SAFETY PROPERTY 4: the floor holds under any fault barrage.
func TestFloorHolds(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := NewStoreWithClock(fixedClock(now))
	var cur float64
	for i := 0; i < 20; i++ {
		_, cur = s.Observe("byzantine", Equivocation)
	}
	if !almost(cur, Floor) {
		t.Errorf("after 20 equivocations = %v, want exactly Floor %v", cur, Floor)
	}
	if cur < Floor {
		t.Fatal("score fell below Floor")
	}
}

// SAFETY PROPERTY 5: a penalized peer heals toward Start over epochs.
func TestDecayHealsBetweenObservations(t *testing.T) {
	at := time.Unix(1_000_000, 0)
	s := NewStoreWithClock(func() time.Time { return at })

	s.Observe("P", BadSignature) // 0.50 → 0.20
	if got := s.Score("P"); !almost(got, 0.20) {
		t.Fatalf("after BadSignature = %v, want 0.20", got)
	}

	// 10 epochs later, no events: score decays toward Start.
	at = at.Add(10 * EpochSeconds * time.Second)
	healed := s.Score("P")
	wantHealed := Start + (0.20-Start)*math.Pow(0.9, 10)
	if !almost(healed, wantHealed) {
		t.Errorf("after 10 idle epochs = %v, want %v", healed, wantHealed)
	}
	if healed <= 0.20 {
		t.Error("score did not heal upward")
	}

	// The next observation applies decay BEFORE the delta.
	_, cur := s.Observe("P", AgreeFinalized)
	if !almost(cur, wantHealed+0.02) {
		t.Errorf("decay-then-delta = %v, want %v", cur, wantHealed+0.02)
	}
}

func TestSnapshotDecaysToNow(t *testing.T) {
	at := time.Unix(1_000_000, 0)
	s := NewStoreWithClock(func() time.Time { return at })
	s.Observe("P", Equivocation) // → 0.10 (clamped: 0.50-0.50=0.00→Floor)
	at = at.Add(5 * EpochSeconds * time.Second)
	snap := s.Snapshot()
	want := Start + (Floor-Start)*math.Pow(0.9, 5)
	if got := snap["P"]; !almost(got, want) {
		t.Errorf("snapshot after 5 epochs = %v, want %v", got, want)
	}
}

func TestUnknownPeerScoreIsStart(t *testing.T) {
	s := NewStoreWithClock(fixedClock(time.Unix(1_000_000, 0)))
	if got := s.Score("nobody"); !almost(got, Start) {
		t.Errorf("unknown peer score = %v, want Start", got)
	}
}
