package messaging

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"gossipnode/config/settings"
)

// Cross-process committee agreement.
//
// # Why processes and not goroutines
//
// The bug this closes (#27: 13 nodes, 13 committees) was that selection read
// something node-local - the peer's own id, its own PeerList, its own shuffle.
// A same-process test cannot see that class of defect, because every goroutine
// shares the one set of globals that would have to differ. Only a separate OS
// process has its own address space, its own map iteration order, its own
// pointer values and its own scheduling.
//
// So each "node" here is a real child process running the same test binary.
// They receive nothing but the round parameters. If any node-local value ever
// leaks into the seed or the ranking, these processes disagree and the test
// fails.
//
// TestCommitteeAgreementHarnessDetectsDisagreement is the negative control: it
// gives each child a different round, and REQUIRES the harness to report
// disagreement. Without it, a harness that silently compared nothing would pass
// the positive test and prove nothing.

const (
	childEnvKey    = "JMDN_COMMITTEE_AGREEMENT_CHILD"
	childHeightKey = "JMDN_COMMITTEE_AGREEMENT_HEIGHT"
	childPoolKey   = "JMDN_COMMITTEE_AGREEMENT_POOL"
	childSeatsKey  = "JMDN_COMMITTEE_AGREEMENT_SEATS"
)

// TestChildComputeCommittee is the child-process entry point. It is inert in a
// normal run and only does work when the parent sets childEnvKey.
func TestChildComputeCommittee(t *testing.T) {
	if os.Getenv(childEnvKey) == "" {
		t.Skip("child-process entry point; driven by TestCommitteeAgreementAcrossProcesses")
	}

	height, err := strconv.ParseUint(os.Getenv(childHeightKey), 10, 64)
	if err != nil {
		t.Fatalf("child: bad height: %v", err)
	}
	pool, err := strconv.Atoi(os.Getenv(childPoolKey))
	if err != nil {
		t.Fatalf("child: bad pool: %v", err)
	}
	seats, err := strconv.Atoi(os.Getenv(childSeatsKey))
	if err != nil {
		t.Fatalf("child: bad seats: %v", err)
	}

	// The child wires the SAME authenticated source the parent would. This
	// stands in for the seed-signed snapshot: in production every node fetches
	// it from the seed node and verifies the authority signature, so all nodes
	// hold identical bytes. That premise is what makes agreement possible at
	// all - see the note on TestCommitteeAgreementAcrossProcesses.
	wireEligibility(t, pool)

	members, err := selectN(round(height, 0), seats)
	if err != nil {
		t.Fatalf("child: selection failed: %v", err)
	}

	// Marked so the parent can find it in the child's test output.
	fmt.Printf("COMMITTEE|%s\n", strings.Join(ids(members), ","))
}

// runChildren spawns n child processes and returns each one's committee.
// heightFor lets the negative control give each child a different round.
func runChildren(t *testing.T, n, pool, seats int, heightFor func(i int) uint64) []string {
	t.Helper()

	out := make([]string, n)
	for i := range n {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChildComputeCommittee$", "-test.v")
		cmd.Env = append(os.Environ(),
			childEnvKey+"=1",
			childHeightKey+"="+strconv.FormatUint(heightFor(i), 10),
			childPoolKey+"="+strconv.Itoa(pool),
			childSeatsKey+"="+strconv.Itoa(seats),
		)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child %d failed: %v\n%s", i, err, raw)
		}

		var got string
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if after, ok := strings.CutPrefix(line, "COMMITTEE|"); ok {
				got = after
				break
			}
		}
		if got == "" {
			t.Fatalf("child %d produced no committee line\n%s", i, raw)
		}
		out[i] = got
	}
	return out
}

// enableV2 turns on v2 for one test AND pins a seed authority key, because
// SelectCommittee refuses to run without one - see ErrLegacySourceUnderV2. The
// two go together: enabling seed-ranked selection over the unauthenticated
// legacy source would rank a set the nodes already disagree about.
func enableV2(t *testing.T) {
	t.Helper()
	cfg := settings.Get()

	prevFlag := CommitteeV2Enabled
	prevPin := cfg.Consensus.SeedAuthorityBLSPub
	CommitteeV2Enabled = true
	cfg.Consensus.SeedAuthorityBLSPub = "aa" + strings.Repeat("bb", 47)

	t.Cleanup(func() {
		CommitteeV2Enabled = prevFlag
		cfg.Consensus.SeedAuthorityBLSPub = prevPin
	})
}

// distinct counts how many different committees a set of nodes produced.
func distinct(committees []string) map[string]int {
	seen := make(map[string]int, len(committees))
	for _, c := range committees {
		seen[c]++
	}
	return seen
}

// TestCommitteeAgreementAcrossProcesses is the evidence that #27 is closed.
//
// 13 independent OS processes, one round, one committee.
//
// PREMISE, stated explicitly because the test cannot check it: every node holds
// the same eligible set. In production that comes from the authority-signed
// snapshot, and if two nodes ever hold different snapshots for the same block
// they will disagree here no matter how deterministic the ranking is. Selection
// determinism is necessary, not sufficient.
func TestCommitteeAgreementAcrossProcesses(t *testing.T) {
	const nodes, pool, seats = 13, 20, testSeats

	committees := runChildren(t, nodes, pool, seats, func(int) uint64 { return 9_000 })
	seen := distinct(committees)

	if len(seen) != 1 {
		for c, n := range seen {
			t.Errorf("  %2d node(s): %s", n, c)
		}
		t.Fatalf("%d nodes produced %d different committees; expected exactly 1", nodes, len(seen))
	}
	if got := len(strings.Split(committees[0], ",")); got != seats {
		t.Fatalf("committee size %d, want %d", got, seats)
	}
	t.Logf("%d independent processes, pool of %d, %d seats -> 1 committee: %s",
		nodes, pool, seats, committees[0])
}

// TestCommitteeAgreementHarnessDetectsDisagreement is the negative control.
//
// Same harness, but every child gets its own height. If the parent still
// reports one committee, the comparison above is not actually comparing
// anything and its pass is meaningless.
func TestCommitteeAgreementHarnessDetectsDisagreement(t *testing.T) {
	const nodes, pool, seats = 8, 20, testSeats

	committees := runChildren(t, nodes, pool, seats, func(i int) uint64 { return uint64(7_000 + i) })
	seen := distinct(committees)

	if len(seen) == 1 {
		t.Fatalf("negative control failed: %d nodes on %d DIFFERENT rounds still agreed - "+
			"the harness is not comparing committees", nodes, nodes)
	}
	t.Logf("negative control: %d nodes on %d different rounds -> %d distinct committees, as required",
		nodes, nodes, len(seen))
}

// TestSelectionAndTallyAgreeAtPoolGreaterThanK is the end-to-end statement of
// F1 through the REAL jmdn entry points.
//
// The sequencer asks SeatedPeerIDs who to poll. The verifier asks
// VerifyCertificateForRound who to count. At pool > k these were different sets
// and every seated member could sign without the certificate ever reaching
// threshold. Here they must be identical, at every height.
func TestSelectionAndTallyAgreeAtPoolGreaterThanK(t *testing.T) {
	wireEligibility(t, 20)

	enableV2(t)

	for h := uint64(4000); h < 4050; h++ {
		rc := round(h, 0)

		polled, err := SeatedPeerIDs(rc)
		if err != nil {
			t.Fatal(err)
		}
		counted, err := SelectCommittee(rc)
		if err != nil {
			t.Fatal(err)
		}
		if len(polled) != len(counted) {
			t.Fatalf("height %d: polled %d peers but the tally counts %d", h, len(polled), len(counted))
		}
		for _, m := range counted {
			if _, ok := polled[m.PeerID]; !ok {
				t.Fatalf("height %d: %s is counted by the tally but was never polled", h, m.PeerID)
			}
		}
	}
	t.Log("F1 through the real entry points: over 50 heights at pool 20 > k 7, " +
		"the polled set and the counted set are identical")
}

// TestWarmupCoversEverySeatableePeer is the defect the capped warmup would have
// shipped.
//
// The warmup connects to a set of peers once. Selection then rotates across the
// whole pool every height. If the warmup only covered the capped prefix, a
// height that seats peer-15 could never collect its vote - the sequencer has no
// connection to it - and the certificate would sit below threshold forever.
func TestWarmupCoversEverySeatablePeer(t *testing.T) {
	wireEligibility(t, 20)

	enableV2(t)

	warm, err := WarmupPeerIDs()
	if err != nil {
		t.Fatal(err)
	}

	everSeated := make(map[string]struct{})
	for h := uint64(0); h < 500; h++ {
		seated, err := SelectCommittee(round(h, 0))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range seated {
			everSeated[m.PeerID] = struct{}{}
			if _, ok := warm[m.PeerID]; !ok {
				t.Fatalf("height %d seats %s, which the warmup never connected to", h, m.PeerID)
			}
		}
	}
	if len(everSeated) <= testSeats {
		t.Fatalf("only %d distinct peers were ever seated over 500 heights; "+
			"selection is not rotating and this test proves nothing", len(everSeated))
	}
	t.Logf("%d distinct peers seated across 500 heights, all covered by a warmup of %d",
		len(everSeated), len(warm))
}

// TestWarmupIsUnchangedWithFlagOff pins the rollout guarantee: shipping this
// binary with JMDN_COMMITTEE_V2 unset must not change which peers the sequencer
// warms up to.
func TestWarmupIsUnchangedWithFlagOff(t *testing.T) {
	wireEligibility(t, 20)

	if CommitteeV2Enabled {
		t.Fatal("JMDN_COMMITTEE_V2 must default to FALSE")
	}
	warm, err := WarmupPeerIDs()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := EligibleCommitteePeerIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(warm) != len(legacy) {
		t.Fatalf("flag off changed the warmup set: %d vs %d", len(warm), len(legacy))
	}
	for pid := range legacy {
		if _, ok := warm[pid]; !ok {
			t.Fatalf("flag off dropped %s from the warmup set", pid)
		}
	}
	t.Logf("flag off: warmup is the capped set of %d, unchanged", len(warm))
}

// TestEpochIsDerivedFromTheBlockNotTheClock guards the one input that would
// silently reintroduce divergence.
//
// seednode/committee.EpochForTime is unix/3600. If it were ever fed into a
// RoundContext, two nodes verifying the same block on opposite sides of an
// epoch boundary would hash different epochs into the seed and seat different
// committees - and the failure would look like a random, rare, unreproducible
// rejection an hour apart.
func TestEpochIsDerivedFromTheBlockNotTheClock(t *testing.T) {
	// Default config: committee_epoch_blocks == 0 means one epoch.
	for _, h := range []uint64{0, 1, 999, 1_000_000} {
		if got := EpochForHeight(h); got != 0 {
			t.Fatalf("height %d: epoch %d, want 0 with committee_epoch_blocks unset", h, got)
		}
	}

	// The same height must always give the same epoch, whenever it is asked.
	// A clock-derived epoch would not survive this.
	first := EpochForHeight(12345)
	for range 1000 {
		if EpochForHeight(12345) != first {
			t.Fatal("EpochForHeight is not a pure function of height")
		}
	}

	cfg := settings.Get()
	prevBlocks := cfg.Consensus.CommitteeEpochBlocks
	cfg.Consensus.CommitteeEpochBlocks = 100
	t.Cleanup(func() { cfg.Consensus.CommitteeEpochBlocks = prevBlocks })

	for h, want := range map[uint64]uint64{0: 0, 99: 0, 100: 1, 250: 2, 1000: 10} {
		if got := EpochForHeight(h); got != want {
			t.Fatalf("committee_epoch_blocks=100: height %d gave epoch %d, want %d", h, got, want)
		}
	}
	t.Log("epoch is a pure function of block height; the wall clock is not an input")
}
