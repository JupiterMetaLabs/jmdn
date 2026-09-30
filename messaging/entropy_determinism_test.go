package messaging

// Cross-node determinism of the entropy inputs: nothing local (the operator's
// block_buddy blocklist, how far this node has read the chain) may change
// which reveals are expected or which certificates feed the fallback seed.

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"gossipnode/config"
)

func certFrom(t *testing.T, prevHash string, prevHeight uint64, peers ...string) []config.CertSigner {
	t.Helper()
	out := make([]config.CertSigner, 0, len(peers))
	for _, p := range peers {
		r := testMembers[p].blockVoteAt(t, prevHash, 1, prevHeight)
		out = append(out, config.CertSigner{PeerID: r.PeerID, PubKey: r.PubKey, Signature: r.Signature})
	}
	return out
}

// A7: a PrevAggCert signed by a peer THIS node blocklists must fold exactly as
// it does on every other node. Before the fix the whole certificate was
// rejected here ("not in the eligible pool") and kept everywhere else, so this
// node's fallback window, fallback seed and entropy all diverged.
func TestFallbackCertificateFoldIgnoresLocalBlocklist(t *testing.T) {
	const prevHeight = 777
	prevHash := "0x" + fmt.Sprintf("%064x", 0xabc)
	cert := certFrom(t, prevHash, prevHeight, "peerA", "peerB", "peerC")

	fleet, err := verifyCertAndAggregate(cert, prevHeight, prevHash, EpochForHeight(prevHeight))
	if err != nil {
		t.Fatalf("baseline certificate must verify: %v", err)
	}

	withBlockBuddy(t, "peerB")
	local, err := verifyCertAndAggregate(cert, prevHeight, prevHash, EpochForHeight(prevHeight))
	if err != nil {
		t.Fatalf("a local block_buddy entry changed whether a committed certificate folds: %v", err)
	}
	if !bytes.Equal(fleet, local) {
		t.Fatal("a local block_buddy entry changed the folded aggregate")
	}

	// The count rule is still enforced (2 of 4 is below quorum 3).
	if _, err := verifyCertAndAggregate(cert[:2], prevHeight, prevHash, EpochForHeight(prevHeight)); err == nil {
		t.Fatal("a below-quorum certificate must still be refused")
	}
}

// A4: the entropy committee's anchored pool (who is EXPECTED to reveal) must
// not depend on the local blocklist.
func TestEntropyAnchoredPoolIgnoresLocalBlocklist(t *testing.T) {
	env := withAnchoring(t, 860)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 1000, signedSnapshot(t, env.auth, 500, 29, 0))); err != nil {
		t.Fatal(err)
	}
	env.setTip(870)
	withBlockBuddy(t, "peer-005")

	pool, handled, err := entropyAnchoredPool(21)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if _, ok := pool["peer-005"]; !ok || len(pool) != 29 {
		t.Fatalf("blocklisted member removed from the entropy committee pool (len %d): nodes would disagree on the expected reveal set", len(pool))
	}
}

// A5: before the chain can no longer add an anchor at or before the cutoff,
// the answer is not final and must not be given.
func TestEntropyAnchoredPoolNotFinalBeforeCutoff(t *testing.T) {
	env := withAnchoring(t, 860)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 1000, signedSnapshot(t, env.auth, 500, 29, 0))); err != nil {
		t.Fatal(err)
	}
	env.setTip(870)
	cutoff := uint64(21*N - SnapshotFreezeLookahead)

	env.setNextSlot(cutoff) // a block could still land AT the cutoff
	if _, handled, err := entropyAnchoredPool(21); !handled || !errors.Is(err, ErrEntropyPoolNotFinal) {
		t.Fatalf("want ErrEntropyPoolNotFinal, got handled=%v err=%v", handled, err)
	}
	env.setNextSlot(cutoff + 1)
	if _, _, err := entropyAnchoredPool(21); err != nil {
		t.Fatalf("final once the next committable slot is past the cutoff: %v", err)
	}
}

// A5: an anchor this node has committed but cannot read must fail closed, not
// be skipped in favour of an OLDER anchor that only this node would pick.
func TestEntropyAnchoredPoolFailsClosedOnUnreadableCommittedAnchor(t *testing.T) {
	env := withAnchoring(t, 860)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 1000, signedSnapshot(t, env.auth, 500, 29, 0))); err != nil {
		t.Fatal(err)
	}
	// Height 880 (slot 1030 <= epoch 21's cutoff 1047) is committed - tip 899 -
	// but neither its record nor its block is readable on this node.
	env.setTip(899)

	pool, handled, err := entropyAnchoredPool(21)
	if err == nil {
		t.Fatalf("returned a %d-member pool from the OLDER anchor instead of failing closed", len(pool))
	}
	if !handled {
		t.Fatal("must be handled (fail closed), not fall back to the live source")
	}
}

// Pre-activation (non-anchored) pool: the local blocklist must not change the
// entropy committee either.
func TestEntropyCommitteeLegacyPoolIgnoresLocalBlocklist(t *testing.T) {
	ids := make([]string, 5)
	for i := range ids {
		_, ids[i] = newTestIdentity(t)
	}
	withBeaconEntropy(t, map[uint64][]byte{8: fakeEntropy(0x88, 32)})
	wireEligibilityWithPeers(t, ids)

	before, err := SelectEntropyCommittee(8)
	if err != nil {
		t.Fatal(err)
	}
	withBlockBuddy(t, ids[0])
	after, err := SelectEntropyCommittee(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("local blocklist changed the entropy committee: %d -> %d members", len(before), len(after))
	}
	for i := range before {
		if before[i].PeerID != after[i].PeerID {
			t.Fatal("local blocklist changed the entropy committee")
		}
	}
}

func TestNoteCommittedSlotIsMonotonic(t *testing.T) {
	savedHW, savedSlot := committedSlotHW.Load(), DefaultSlotStore
	committedSlotHW.Store(0)
	DefaultSlotStore = NewSlotStore()
	t.Cleanup(func() { committedSlotHW.Store(savedHW); DefaultSlotStore = savedSlot })

	noteCommittedSlot(40)
	noteCommittedSlot(12)
	if got := committedSlotHighWater(); got != 40 {
		t.Fatalf("high-water went backwards: %d", got)
	}
	if !EntropyBoundaryCommitted(0) || EntropyBoundaryCommitted(1) {
		t.Fatal("boundary committed: epoch 0 (slot 0) yes, epoch 1 (slot 50) no")
	}
}
