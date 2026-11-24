package messaging

// Tests for entropy_committee.go — Stage A of the M4 pipeline
// (AVC-M4-Entropy-Reveal-Pipeline-Design.md §A, Architecture §4.7).
//
// NOTE ON INDEXING: entropy is published and looked up under the SAME epoch
// number the committee is being selected for (beacon.EpochEntropy(epoch),
// no offset) — that is what Architecture §4.7 means by "ENTROPY-E seeds
// committee_E." See entropy_committee.go's file-level correction comment for
// why the first version of this code got that backwards.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
)

// withBeaconEntropy installs a fresh BeaconSource with the given
// (epoch -> entropy) values published, and restores beaconSource to nil on
// cleanup — same "hand global state back as found" discipline as
// wireEligibility in committee_v2_test.go.
func withBeaconEntropy(t *testing.T, entropyByEpoch map[uint64][]byte) {
	t.Helper()
	src, err := committee.NewBeaconSource(committee.MinRetainedEpochs)
	if err != nil {
		t.Fatalf("NewBeaconSource: %v", err)
	}
	for epoch, entropy := range entropyByEpoch {
		if err := src.Publish(epoch, entropy); err != nil {
			t.Fatalf("Publish(%d): %v", epoch, err)
		}
	}
	SetBeaconSource(src)
	t.Cleanup(func() { beaconSource = nil })
}

func fakeEntropy(tag byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = tag
	}
	return out
}

// --- EntropyCommitteeSeed ---------------------------------------------------

func TestEntropyCommitteeSeed_Deterministic(t *testing.T) {
	entropy := fakeEntropy(0xAB, 32)
	s1 := EntropyCommitteeSeed(7, 5, entropy)
	s2 := EntropyCommitteeSeed(7, 5, entropy)
	if s1 != s2 {
		t.Fatalf("same inputs produced different seeds: %s vs %s — every node must derive the identical seed", s1, s2)
	}
}

func TestEntropyCommitteeSeed_VariesWithEntropyInput(t *testing.T) {
	s1 := EntropyCommitteeSeed(7, 5, fakeEntropy(0x01, 32))
	s2 := EntropyCommitteeSeed(7, 5, fakeEntropy(0x02, 32))
	if s1 == s2 {
		t.Fatal("different entropy inputs produced the same seed — entropy is not actually being mixed in")
	}
}

func TestEntropyCommitteeSeed_VariesWithEpoch(t *testing.T) {
	entropy := fakeEntropy(0xCD, 32)
	s1 := EntropyCommitteeSeed(7, 5, entropy)
	s2 := EntropyCommitteeSeed(7, 6, entropy)
	if s1 == s2 {
		t.Fatal("different epochs produced the same seed — epoch is not actually being mixed in (every epoch would seat the identical committee)")
	}
}

func TestEntropyCommitteeSeed_VariesWithChainID(t *testing.T) {
	entropy := fakeEntropy(0xEF, 32)
	s1 := EntropyCommitteeSeed(7, 5, entropy)
	s2 := EntropyCommitteeSeed(8, 5, entropy)
	if s1 == s2 {
		t.Fatal("different chain IDs produced the same seed — two independent networks sharing the same entropy would seat the same committee")
	}
}

// Pins the exact byte layout Architecture §4.7 specifies:
//
//	entropySeed_E = SHA256(domain || u64:chainID || len:ENTROPY-E || u64:epoch)
//
// built here independently of EntropyCommitteeSeed's own implementation
// (spelling the order out by hand) rather than by re-checking the function
// against itself — this is the "byte-exact golden vector" Low-Level-Design
// M4.1 calls for.
func TestEntropyCommitteeSeed_MatchesArchitectureSection4_7ByteLayout(t *testing.T) {
	chainID := uint64(7)
	epoch := uint64(100)
	entropy := fakeEntropy(0x9C, 32)

	h := sha256.New()
	committee.WriteField(h, []byte("jmdt/entropy-committee/v1"))
	committee.WriteU64(h, chainID)
	committee.WriteField(h, entropy)
	committee.WriteU64(h, epoch)
	var want committee.Seed
	copy(want[:], h.Sum(nil))

	got := EntropyCommitteeSeed(chainID, epoch, entropy)
	if got != want {
		t.Fatalf("EntropyCommitteeSeed = %s, want %s (domain||chainID||entropy||epoch order per Architecture §4.7)", got, want)
	}
}

// --- SelectEntropyCommittee: fail-closed paths ------------------------------

func TestSelectEntropyCommittee_NoBeaconInstalled_FailsClosed(t *testing.T) {
	beaconSource = nil // explicit: this is today's live-system default
	_, err := SelectEntropyCommittee(5)
	if !errors.Is(err, ErrNoBeaconInstalled) {
		t.Fatalf("SelectEntropyCommittee(5) with no beacon = %v, want ErrNoBeaconInstalled", err)
	}
}

// Epoch 0 is NOT a special case — it fails closed the exact same way any
// epoch with no published ENTROPY-E does. This is also, today, the state of
// the network's real first epoch: nothing publishes a genesis/bootstrap
// entropy value yet, so it fails closed here rather than seating a
// committee off an absent value.
func TestSelectEntropyCommittee_NoEntropyPublishedForEpoch_FailsClosed(t *testing.T) {
	withBeaconEntropy(t, nil) // beacon exists, nothing published
	wireEligibility(t, 20)

	_, err := SelectEntropyCommittee(0)
	if err == nil {
		t.Fatal("expected an error when ENTROPY-0 was never published, got nil")
	}
	if !errors.Is(err, committee.ErrEntropyUnavailable) {
		t.Fatalf("SelectEntropyCommittee(0) = %v, want it to wrap committee.ErrEntropyUnavailable", err)
	}
}

func TestSelectEntropyCommittee_EmptyEligiblePool_FailsClosed(t *testing.T) {
	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0x11, 32)})
	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
		return map[string]string{}, nil
	})
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })

	_, err := SelectEntropyCommittee(5)
	if err == nil {
		t.Fatal("expected an error for an empty eligible pool, got nil")
	}
}

// --- SelectEntropyCommittee: the live-draw path -----------------------------

func TestSelectEntropyCommittee_ReturnsExactlyMMembersFromEligiblePool(t *testing.T) {
	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0x22, 32)})
	wireEligibility(t, 20)

	members, err := SelectEntropyCommittee(5)
	if err != nil {
		t.Fatalf("SelectEntropyCommittee(5): %v", err)
	}
	if len(members) != EntropyCommitteeSize {
		t.Fatalf("len(members) = %d, want %d", len(members), EntropyCommitteeSize)
	}

	wired := make(map[string]bool, 20)
	for i := 0; i < 20; i++ {
		wired[fmt.Sprintf("peer-%02d", i)] = true
	}
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		if !wired[m.PeerID] {
			t.Fatalf("committee contains %q, which is not in the wired eligible pool", m.PeerID)
		}
		if seen[m.PeerID] {
			t.Fatalf("committee contains %q twice", m.PeerID)
		}
		seen[m.PeerID] = true
	}
}

func TestSelectEntropyCommittee_DeterministicAcrossCalls(t *testing.T) {
	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0x33, 32)})
	wireEligibility(t, 20)

	m1, err := SelectEntropyCommittee(5)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	m2, err := SelectEntropyCommittee(5)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(m1) != len(m2) {
		t.Fatalf("committee size changed between calls: %d vs %d", len(m1), len(m2))
	}
	for i := range m1 {
		if m1[i].PeerID != m2[i].PeerID {
			t.Fatalf("committee order changed between calls at index %d: %q vs %q — every node must derive the identical committee", i, m1[i].PeerID, m2[i].PeerID)
		}
	}
}

func TestSelectEntropyCommittee_DifferentEpochsSeatDifferentCommittees(t *testing.T) {
	wireEligibility(t, 20)

	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0x44, 32)})
	m1, err := SelectEntropyCommittee(5)
	if err != nil {
		t.Fatalf("epoch 5: %v", err)
	}

	withBeaconEntropy(t, map[uint64][]byte{6: fakeEntropy(0x55, 32)})
	m2, err := SelectEntropyCommittee(6)
	if err != nil {
		t.Fatalf("epoch 6: %v", err)
	}

	same := len(m1) == len(m2)
	if same {
		for i := range m1 {
			if m1[i].PeerID != m2[i].PeerID {
				same = false
				break
			}
		}
	}
	if same {
		t.Fatal("two epochs with different ENTROPY-E values produced an identical committee")
	}
}

func TestSelectEntropyCommittee_CacheKeyedByEpoch_RolloverChangesTheSet(t *testing.T) {
	// Architecture §4.7's explicit implementation requirement: "compute at
	// epoch 9, roll to epoch 10, assert the set changed." This
	// implementation does not cache at all (recompute is ~30-340us per
	// §4.7's own benchmark, "cost is a non-issue") — which trivially
	// satisfies the requirement (no cache means no stale-cache bug is
	// possible) but the required behavioural assertion should still be
	// pinned in case caching is added later for performance.
	wireEligibility(t, 20)
	withBeaconEntropy(t, map[uint64][]byte{9: fakeEntropy(0x66, 32), 10: fakeEntropy(0x77, 32)})

	nine, err := SelectEntropyCommittee(9)
	if err != nil {
		t.Fatalf("epoch 9: %v", err)
	}
	ten, err := SelectEntropyCommittee(10)
	if err != nil {
		t.Fatalf("epoch 10: %v", err)
	}
	if len(nine) == len(ten) {
		same := true
		for i := range nine {
			if nine[i].PeerID != ten[i].PeerID {
				same = false
				break
			}
		}
		if same {
			t.Fatal("epoch 9's and epoch 10's committees are identical — rollover must change the set")
		}
	}
}

func TestSelectEntropyCommittee_SeatsEveryoneWhenPoolSmallerThanM(t *testing.T) {
	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0x66, 32)})
	wireEligibility(t, 5) // fewer than EntropyCommitteeSize(13)

	members, err := SelectEntropyCommittee(5)
	if err != nil {
		t.Fatalf("SelectEntropyCommittee(5): %v", err)
	}
	if len(members) != 5 {
		t.Fatalf("len(members) = %d, want 5 (k >= n seats everyone, same rule block committees follow)", len(members))
	}
}
