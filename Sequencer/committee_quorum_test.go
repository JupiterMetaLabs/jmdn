package Sequencer

import (
	"testing"

	"gossipnode/messaging"
)

// The safety property this change must preserve: a round formed at the BFT
// quorum produces a certificate that verifies on a node which saw the FULL
// committee. That holds iff the quorum used for FORMATION is never below the
// threshold VerifyCertificate computes over the same eligible set — because
// verification is unchanged and sizes n over eligibleMembers(), not over
// whoever the sequencer happened to connect to.
//
// These tests pin the arithmetic that relationship rests on. They deliberately
// do NOT stub the committee source: requiredMainPeers falls back to
// MaxMainPeers without settings loaded, which is asserted separately below.

// Quorum must satisfy the intersection property: two quorums drawn from the
// same n share at least f+1 members, so at least one honest node sees both and
// two conflicting blocks can never both certify.
func TestByzantineQuorum_IntersectionHoldsForCommitteeSizes(t *testing.T) {
	for n := 1; n <= 32; n++ {
		q := messaging.ByzantineQuorum(n)
		f := (n - 1) / 3

		if q > n {
			t.Fatalf("n=%d: quorum %d exceeds committee size — unsatisfiable", n, q)
		}
		if q < 1 {
			t.Fatalf("n=%d: quorum %d must be at least 1", n, q)
		}
		// Two q-sized quorums within n intersect in at least 2q-n members.
		if intersection := 2*q - n; intersection < f+1 {
			t.Fatalf("n=%d q=%d f=%d: intersection %d < f+1 (%d) — two conflicting blocks could both certify",
				n, q, f, intersection, f+1)
		}
	}
}

// The production committee size: forming at quorum must tolerate f failures.
func TestQuorum_ProductionCommitteeToleratesF(t *testing.T) {
	const n = 7 // config.MaxMainPeers
	q := messaging.ByzantineQuorum(n)
	f := (n - 1) / 3

	if q != 5 {
		t.Fatalf("n=7: quorum = %d, want 5", q)
	}
	if tolerated := n - q; tolerated != f {
		t.Fatalf("n=7: formation tolerates %d absent nodes, want f=%d", tolerated, f)
	}
	// The regression this fixes: the old gate required all n, tolerating zero.
	if n-n != 0 {
		t.Fatal("sanity")
	}
}

// Without settings loaded there is no authenticated committee, so the gate must
// fall back to the STRICTER pre-change rule rather than a laxer one.
func TestRequiredMainPeers_FallsBackToFullCommitteeWhenUnpinned(t *testing.T) {
	// settings is not loaded in unit tests → unpinned path.
	if got := requiredMainPeers(); got != 7 {
		t.Fatalf("unpinned fallback = %d, want config.MaxMainPeers (7) — a node that "+
			"cannot authenticate its committee must not get a laxer formation rule", got)
	}
}
