package messaging

// J-3: the recovery round must be bounded in BOTH count and time.
//
// RecoverVDFProofFromPeers is handed h.Network().Peers() — every connected
// peer, unfiltered, in map-iteration order — and walks it sequentially with a
// per-peer timeout. Unbounded, one round costs vdfProofRequestTimeout *
// len(peers), and recoveryInFlight holds the per-epoch latch for that whole
// span while the deadline fires only VDFProofRecoveryDeadlineSlots slots before
// the boundary. A round that outlives its runway delivers the proof after the
// block that needed it and blocks any earlier retry, which defeats the
// mechanism rather than merely slowing it.
//
// These tests pin the two bounds and the shuffle. They deliberately do NOT
// exercise the network: the peer-selection policy is where the defect lived,
// and it is pure.

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func makeTestPeers(t *testing.T, n int) []peer.ID {
	t.Helper()
	out := make([]peer.ID, n)
	for i := range out {
		// peer.ID is a string type; these never touch the network, and using
		// synthetic values keeps the test free of key generation.
		out[i] = peer.ID("peer-" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
	}
	return out
}

// The cap is what bounds worst-case round cost. Without it, a node connected to
// 200 peers walks all 200.
func TestPickRecoveryPeersCapsTheSample(t *testing.T) {
	peers := makeTestPeers(t, 200)

	got := pickRecoveryPeers(peers, maxRecoveryPeers)

	if len(got) != maxRecoveryPeers {
		t.Fatalf("sample size %d, want %d — an uncapped round costs %v per peer across all %d "+
			"connected peers, which can outlive the recovery runway",
			len(got), maxRecoveryPeers, vdfProofRequestTimeout, len(peers))
	}
}

// Fewer peers than the cap must all be asked — the cap must not shrink a small
// peer set, which would reduce the chance of recovery on a small network.
func TestPickRecoveryPeersKeepsEveryPeerBelowTheCap(t *testing.T) {
	for _, n := range []int{0, 1, 3, maxRecoveryPeers - 1, maxRecoveryPeers} {
		peers := makeTestPeers(t, n)
		got := pickRecoveryPeers(peers, maxRecoveryPeers)
		if len(got) != n {
			t.Fatalf("with %d connected peers the sample was %d; every peer must be asked "+
				"below the cap", n, len(got))
		}
	}
}

// The caller's slice belongs to libp2p. Reordering it in place would be a
// visible side effect on shared state.
func TestPickRecoveryPeersDoesNotMutateTheCallersSlice(t *testing.T) {
	peers := makeTestPeers(t, 30)
	before := make([]peer.ID, len(peers))
	copy(before, peers)

	_ = pickRecoveryPeers(peers, maxRecoveryPeers)

	for i := range peers {
		if peers[i] != before[i] {
			t.Fatalf("pickRecoveryPeers reordered the caller's slice at index %d — that slice "+
				"comes from h.Network().Peers() and must not be mutated", i)
		}
	}
}

// Shuffling is load-bearing: with a cap and a deterministic prefix, one
// unlucky set of dead peers would starve recovery every epoch. Each round must
// be an independent draw.
//
// Probabilistic but not flaky: with 200 peers and a 12-wide sample, two
// successive draws being identical has probability ~1/C(200,12) per pair. Over
// 20 rounds, seeing at least two distinct samples is a certainty for any
// working shuffle.
func TestPickRecoveryPeersDrawsADifferentSampleEachRound(t *testing.T) {
	peers := makeTestPeers(t, 200)

	seen := map[string]int{}
	for i := 0; i < 20; i++ {
		sample := pickRecoveryPeers(peers, maxRecoveryPeers)
		key := ""
		for _, p := range sample {
			key += string(p) + ","
		}
		seen[key]++
	}

	if len(seen) == 1 {
		t.Fatal("20 rounds produced the same sample every time — the draw is deterministic, so a " +
			"single bad neighbourhood of peers would starve recovery every epoch instead of " +
			"only occasionally")
	}
}

// The time budget must actually bound a round independently of the cap, so that
// tuning one does not silently unbound the other.
func TestRecoveryRoundBudgetBoundsTheWorstCase(t *testing.T) {
	worstCaseUncapped := time.Duration(maxRecoveryPeers) * vdfProofRequestTimeout

	if maxRecoveryRoundBudget >= worstCaseUncapped {
		t.Fatalf("the round budget (%v) is not tighter than the per-peer worst case "+
			"(%d peers * %v = %v), so it can never bind and the count cap is the only bound",
			maxRecoveryRoundBudget, maxRecoveryPeers, vdfProofRequestTimeout, worstCaseUncapped)
	}

	// And the budget must leave room for more than a single peer, or recovery
	// degenerates into asking one node.
	if maxRecoveryRoundBudget < 2*vdfProofRequestTimeout {
		t.Fatalf("the round budget (%v) allows fewer than two full per-peer attempts (%v each) — "+
			"recovery would effectively ask a single peer",
			maxRecoveryRoundBudget, vdfProofRequestTimeout)
	}
}

// Sanity-check the deadline arithmetic this file relies on, since J-2 turns on
// these two functions being exact inverses.
func TestEpochBoundaryAndEpochForSlotAreExactInverses(t *testing.T) {
	for e := uint64(0); e < 1000; e++ {
		if got := EpochForSlot(EpochBoundarySlot(e)); got != e {
			t.Fatalf("EpochForSlot(EpochBoundarySlot(%d)) = %d, want %d", e, got, e)
		}
	}
}
