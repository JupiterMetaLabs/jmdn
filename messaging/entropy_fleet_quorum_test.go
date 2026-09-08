package messaging

// D-36 — the fallback fold's quorum denominator must not move when one operator
// edits their own block_buddy blocklist.
//
// THE DIVERGENCE THESE TESTS PIN. verifyCertAndAggregate resolved its pool with
// committeeSnapshotFor, whose chain runs
//
//	committeeSnapshotFor -> pinnedEligibleForEpoch -> eligibleMembersUncapped
//	  -> eligibleMembersUncappedForEpoch -> subtracts blockedBuddies()
//
// and then derived TWO fleet-agreed quantities from the filtered result: which
// signers may be counted, and aggCertQuorum(len(snap.Members)).
//
// With a 7-member pool and one blocked peer, the blocklisting node computed
// n=6, ByzantineQuorum(6)=4, while every peer computed n=7, quorum=5. A
// 4-signer PrevAggCert was therefore folded on one node and rejected on the
// rest. FallbackSeedForEpoch folds the lowest B in-window slots, so gaining or
// losing one slot shifts the whole subset — a different XOR seed, a different
// DeriveSeed, a DIFFERENT COMMITTEE. Silently, with no error on either side.
//
// VerifyCertificate has always taken the opposite approach and says why
// (consensus_hardening.go, CON-12): the denominator is the fleet-agreed pool
// and the blocklist only removes a VOTER, so "blocking can only make quorum
// HARDER, never lower the bar". The entropy fold inverted that.

import (
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
)

// blockedTestPeer picks a real member of the test committee to blocklist, so
// the test exercises removal of an ACTUAL pool member rather than a no-op.
func blockedTestPeer(t *testing.T) string {
	t.Helper()
	if len(defaultCommitteePeerIDs) == 0 {
		t.Skip("no default committee peer IDs in this harness")
	}
	return defaultCommitteePeerIDs[0]
}

func memberCount(t *testing.T, snap committee.Snapshot) int {
	t.Helper()
	return len(snap.Members)
}

// THE REGRESSION TEST. The fleet snapshot must be the same size with and
// without a local blocklist entry; the filtered one must shrink. If both
// shrink, the denominator is local again and nodes will diverge.
func TestFleetSnapshotSizeIsIndependentOfLocalBlocklist(t *testing.T) {
	const epoch = 8

	fleetBefore, err := fleetCommitteeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("fleetCommitteeSnapshotFor (no blocklist): %v", err)
	}
	localBefore, err := committeeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("committeeSnapshotFor (no blocklist): %v", err)
	}
	if memberCount(t, fleetBefore) != memberCount(t, localBefore) {
		t.Fatalf("with no blocklist the two views must agree: fleet=%d local=%d",
			memberCount(t, fleetBefore), memberCount(t, localBefore))
	}

	withBlockBuddy(t, blockedTestPeer(t))

	fleetAfter, err := fleetCommitteeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("fleetCommitteeSnapshotFor (with blocklist): %v", err)
	}
	localAfter, err := committeeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("committeeSnapshotFor (with blocklist): %v", err)
	}

	t.Logf("pool sizes — fleet: %d -> %d, local: %d -> %d",
		memberCount(t, fleetBefore), memberCount(t, fleetAfter),
		memberCount(t, localBefore), memberCount(t, localAfter))

	if memberCount(t, fleetAfter) != memberCount(t, fleetBefore) {
		t.Fatalf("D-36 REGRESSION: the FLEET pool shrank from %d to %d because of a LOCAL "+
			"block_buddy entry. Any threshold derived from it now differs between nodes, so two "+
			"honest nodes fold different certificate sets and seat different committees.",
			memberCount(t, fleetBefore), memberCount(t, fleetAfter))
	}
	if memberCount(t, localAfter) >= memberCount(t, localBefore) {
		t.Fatalf("the FILTERED pool did not shrink (%d -> %d) — the blocklist is not being "+
			"applied at all, so this test proves nothing about the fleet/local distinction",
			memberCount(t, localBefore), memberCount(t, localAfter))
	}
}

// The threshold itself, which is what actually diverges.
func TestAggCertQuorumIsIndependentOfLocalBlocklist(t *testing.T) {
	const epoch = 8

	fleetBefore, err := fleetCommitteeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("fleet snapshot: %v", err)
	}
	quorumBefore := aggCertQuorum(len(fleetBefore.Members))

	withBlockBuddy(t, blockedTestPeer(t))

	fleetAfter, err := fleetCommitteeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("fleet snapshot (blocklisted): %v", err)
	}
	quorumAfter := aggCertQuorum(len(fleetAfter.Members))

	t.Logf("quorum: %d -> %d (pool %d -> %d)",
		quorumBefore, quorumAfter, len(fleetBefore.Members), len(fleetAfter.Members))

	if quorumAfter != quorumBefore {
		t.Fatalf("D-36 REGRESSION: the Byzantine quorum for the entropy fold moved from %d to %d "+
			"because of a LOCAL blocklist entry. This node now accepts a certificate its peers "+
			"reject (or vice versa), producing a different fallback seed for the same epoch. "+
			"Blocking must only ever make quorum HARDER — never change the bar.",
			quorumBefore, quorumAfter)
	}
}

// Blocking must still stop the blocked peer's signature from COUNTING — the fix
// must not have turned the blocklist into a no-op for the fold.
func TestBlocklistStillRemovesTheVoterFromTheFoldPool(t *testing.T) {
	const epoch = 8
	blocked := blockedTestPeer(t)

	withBlockBuddy(t, blocked)

	// The numerator view (what verifyCertAndAggregate counts) must exclude it.
	local, err := committeeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("committeeSnapshotFor: %v", err)
	}
	for _, m := range local.Members {
		if m.PeerID == blocked {
			t.Fatalf("blocked peer %s is still in the filtered pool — blocking no longer removes "+
				"a voter, so the blocklist has become decorative", blocked)
		}
	}

	// ...while the denominator view still counts its seat.
	fleet, err := fleetCommitteeSnapshotFor(epoch)
	if err != nil {
		t.Fatalf("fleetCommitteeSnapshotFor: %v", err)
	}
	found := false
	for _, m := range fleet.Members {
		if m.PeerID == blocked {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("blocked peer %s is absent from the FLEET pool — its seat must still size n, "+
			"or the denominator is local again (numerator ⊆ denominator is the invariant)", blocked)
	}
}
