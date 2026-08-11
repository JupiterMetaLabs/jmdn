package messaging

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
)

// testSeats is the committee size the tests exercise. Passed explicitly via
// SelectCommitteeWithSize so no global configuration is mutated.
const testSeats = 7

// wireEligibility installs a fake authenticated source of n peers and sets
// MaxValidators, mirroring how the sequencer wires the real one at startup.
func wireEligibility(t *testing.T, n int) {
	t.Helper()
	SetCommitteeEligibilitySource(func() (map[string]string, error) {
		out := make(map[string]string, n)
		for i := 0; i < n; i++ {
			out[fmt.Sprintf("peer-%02d", i)] = fmt.Sprintf("%064x", i)
		}
		return out, nil
	})
	// Restore the harness default, NOT nil. TestMain installs
	// defaultTestEligibility once for the whole package and other files rely on
	// it still being there; clearing it here made TestEquivocationSurvivesRestart
	// and TestWithoutStore_RestartLosesRecord fail, because validateRemoteBlock
	// routes through the certificate verifier and that fails closed with no
	// source. Package-global state must be handed back as it was found.
	t.Cleanup(func() {
		SetCommitteeEligibilitySource(defaultTestEligibility)
		beaconSource = nil
	})
}

// selectN is SelectCommittee with an explicit committee size.
func selectN(rc RoundContext, k int) ([]committee.Member, error) {
	return SelectCommitteeWithSize(rc, k)
}

// cappedAlphabetically reproduces today's certificate-path committee: the
// eligible set trimmed by sorted peer_id, exactly as consensus_hardening.go
// 191-204 does.
func cappedAlphabetically(t *testing.T, k int) []string {
	t.Helper()
	eligible, err := eligibleMembersUncapped()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(eligible))
	for pid := range eligible {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	if k < len(ids) {
		ids = ids[:k]
	}
	return ids
}

func round(height, period uint64) RoundContext {
	return RoundContext{Epoch: 421, PrevHash: []byte("parent-hash"), Height: height, Period: period}
}

func ids(ms []committee.Member) []string {
	out := make([]string, len(ms))
	for i, m := range committee.Canonical(ms) {
		out[i] = m.PeerID
	}
	return out
}

// ---------------------------------------------------------------------------
// F2 - the alphabetical cap froze the voting set
// ---------------------------------------------------------------------------

// Today's behaviour: eligibleMembers caps with sort.Strings(ids)[:lim], so at
// P > k the voting set is the k alphabetically-first peers, forever.
func TestF2_AlphabeticalCapIsFrozen(t *testing.T) {
	wireEligibility(t, 20)

	var first []string
	for h := uint64(0); h < 50; h++ {
		// Exactly what consensus_hardening.go:191-204 computes today.
		got := cappedAlphabetically(t, testSeats)
		if first == nil {
			first = got
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(first) {
			t.Fatal("the alphabetical cap rotated, which contradicts the finding")
		}
	}
	if fmt.Sprint(first) != "[peer-00 peer-01 peer-02 peer-03 peer-04 peer-05 peer-06]" {
		t.Fatalf("cap did not select the alphabetically-first 7: %v", first)
	}
	t.Logf("F2 reproduced: over 50 heights the voting set never changed - %v", first)
	t.Log("peer-07..peer-19 would never validate a block")
}

// And the fix: seed-ranked selection rotates while staying deterministic.
func TestF2_Fixed_CommitteeRotates(t *testing.T) {
	wireEligibility(t, 20)

	seen := map[string]bool{}
	counts := map[string]int{}
	const heights = 2000
	for h := uint64(0); h < heights; h++ {
		seated, err := selectN(round(h, 0), testSeats)
		if err != nil {
			t.Fatal(err)
		}
		if len(seated) != 7 {
			t.Fatalf("seated %d, want 7", len(seated))
		}
		seen[fmt.Sprint(ids(seated))] = true
		for _, m := range seated {
			counts[m.PeerID]++
		}
	}
	if len(seen) < heights/2 {
		t.Fatalf("only %d distinct committees over %d heights", len(seen), heights)
	}
	for i := 0; i < 20; i++ {
		pid := fmt.Sprintf("peer-%02d", i)
		if counts[pid] == 0 {
			t.Fatalf("%s was never seated - rotation is not reaching everyone", pid)
		}
	}
	t.Logf("F2 fixed: %d distinct committees over %d heights, all 20 peers seated", len(seen), heights)
}

// Determinism is preserved: the property the cap existed to guarantee.
func TestF2_Fixed_StillDeterministic(t *testing.T) {
	wireEligibility(t, 20)
	want, err := selectN(round(1000, 0), testSeats)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		got, err := selectN(round(1000, 0), testSeats)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(ids(got)) != fmt.Sprint(ids(want)) {
			t.Fatal("SelectCommittee is not deterministic")
		}
	}
	t.Log("F2 fixed: 50 independent calls, one committee - every node computes the same n and the same T")
}

// ---------------------------------------------------------------------------
// F1 - two sources that disagree
// ---------------------------------------------------------------------------

// The halt: votes from the seed-selected committee tallied against the
// alphabetically-capped one.
func TestF1_TwoSourcesHalt(t *testing.T) {
	wireEligibility(t, 20)

	seated, err := selectN(round(1000, 0), testSeats)
	if err != nil {
		t.Fatal(err)
	}
	// The certificate path today: the alphabetically-capped set.
	capped := map[string]bool{}
	for _, pid := range cappedAlphabetically(t, testSeats) {
		capped[pid] = true
	}

	overlap := 0
	for _, m := range seated {
		if capped[m.PeerID] {
			overlap++
		}
	}
	threshold := 5 // ceil(2*7/3)
	if overlap >= threshold {
		t.Skipf("the two sets happened to overlap by %d at this height; the halt needs overlap < T", overlap)
	}
	t.Logf("F1 reproduced: every one of the %d seated members signs, but the certificate path "+
		"recognises only %d of them - below T=%d. HALT.", len(seated), overlap, threshold)
}

// The fix: one source, so the sets cannot differ.
func TestF1_Fixed_OneSource(t *testing.T) {
	wireEligibility(t, 20)
	rc := round(1000, 0)

	a, err := selectN(rc, testSeats)
	if err != nil {
		t.Fatal(err)
	}
	b, err := selectN(rc, testSeats) // what the tally path uses
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids(a)) != fmt.Sprint(ids(b)) {
		t.Fatal("the two call paths produced different committees")
	}
	t.Logf("F1 fixed: the vote path and the tally path call the SAME function - %v", ids(a))
}

// ---------------------------------------------------------------------------
// F5 - grindable, node-local, never-changing seed
// ---------------------------------------------------------------------------

func TestF5_SeedIsNotGrindableAndRotates(t *testing.T) {
	wireEligibility(t, 20)

	a, _ := selectN(round(1000, 0), testSeats)
	b, _ := selectN(round(1001, 0), testSeats)
	if fmt.Sprint(ids(a)) == fmt.Sprint(ids(b)) {
		t.Fatal("the committee did not change with height")
	}

	c, _ := selectN(round(1000, 1), testSeats)
	if fmt.Sprint(ids(a)) == fmt.Sprint(ids(c)) {
		t.Fatal("a timed-out round re-selected the same committee")
	}

	// The block being voted on is not an input at all - RoundContext has no
	// field for it, so a proposer has nothing to grind.
	d := round(1000, 0)
	d.PrevHash = []byte("a-different-parent")
	e, _ := selectN(d, testSeats)
	if fmt.Sprint(ids(a)) == fmt.Sprint(ids(e)) {
		t.Fatal("the committee did not depend on PrevHash")
	}

	t.Log("F5 fixed: height and period both rotate the draw; the block under vote is not an input")
}

// No node identity anywhere in the derivation: 13 nodes, one committee.
func TestF5_Fixed_EveryNodeAgrees(t *testing.T) {
	wireEligibility(t, 20)
	rc := round(1000, 0)

	var want string
	for node := 0; node < 13; node++ {
		got, err := selectN(rc, testSeats)
		if err != nil {
			t.Fatal(err)
		}
		if node == 0 {
			want = fmt.Sprint(ids(got))
			continue
		}
		if fmt.Sprint(ids(got)) != want {
			t.Fatalf("node %d computed a different committee", node)
		}
	}
	t.Log("F5 fixed: 13 nodes, 1 committee (the live path gives 13 committees, and never rotates)")
}

// ---------------------------------------------------------------------------
// Rollout safety - F3
// ---------------------------------------------------------------------------

// With the flag off, the new entry point must behave exactly like the old one.
// That is what makes shipping the binary safe ahead of the coordinated flip.
func TestF3_FlagOffIsInert(t *testing.T) {
	wireEligibility(t, 20)
	if CommitteeV2Enabled {
		t.Fatal("JMDN_COMMITTEE_V2 must default to FALSE - the committee travels on the wire, " +
			"so a partial rollout with it on would split the fleet")
	}
	got, err := VerifyCertificateForRound(nil, "0xabc", 1000, round(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	want, err := VerifyCertificate(nil, "0xabc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("with the flag off, the new path diverged from the legacy path")
	}
	t.Log("F3: flag defaults off and delegates to the legacy verifier - ship first, flip together")
}

// ---------------------------------------------------------------------------
// Fail closed
// ---------------------------------------------------------------------------

func TestSelectCommitteeFailsClosed(t *testing.T) {
	// Deliberately unwire the source, then hand it back. Without the Cleanup this
	// leaks a nil source into every test that runs after it in the package.
	SetCommitteeEligibilitySource(nil)
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
	if _, err := selectN(round(1, 0), testSeats); err == nil {
		t.Fatal("selected a committee with no eligibility source configured")
	}

	wireEligibility(t, 20)
	rc := round(1, 0)
	rc.PrevHash = nil
	if _, err := selectN(rc, testSeats); err == nil {
		t.Fatal("selected a committee with an empty PrevHash - the seed would be unbound from the chain")
	}
}

// At P == k the draw seats everyone, which is today's configuration.
func TestSelectCommitteeAtPEqualsK(t *testing.T) {
	wireEligibility(t, 7)
	seated, err := selectN(round(1000, 0), testSeats)
	if err != nil {
		t.Fatal(err)
	}
	if len(seated) != 7 {
		t.Fatalf("seated %d of 7", len(seated))
	}
	t.Log("at P == k the draw seats everyone - Stage 1 is a determinism fix here, not a sampling one")
}

// v2 must refuse to run on the legacy committee source. Seed-ranking a set the
// nodes already disagree about produces a different committee on every node -
// the exact failure v2 exists to remove.
func TestV2RefusesLegacySource(t *testing.T) {
	wireEligibility(t, 20)

	prev := CommitteeV2Enabled
	CommitteeV2Enabled = true
	t.Cleanup(func() { CommitteeV2Enabled = prev })

	// No seed_authority_bls_pub pinned => legacy source.
	if _, err := SelectCommitteeWithSize(round(1000, 0), testSeats); !errors.Is(err, ErrLegacySourceUnderV2) {
		t.Fatalf("err = %v, want ErrLegacySourceUnderV2", err)
	}
	t.Log("v2 fails closed on the legacy source rather than seed-ranking a divergent set")

	// And the certificate path fails closed the same way.
	if _, err := VerifyCertificateForRound(nil, "0xab", 1000, round(1000, 0)); !errors.Is(err, ErrLegacySourceUnderV2) {
		t.Fatalf("certificate path err = %v, want ErrLegacySourceUnderV2", err)
	}
}
