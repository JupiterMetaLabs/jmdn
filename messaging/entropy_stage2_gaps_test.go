package messaging

// Tests for the Stage-2 gap closure of 2026-09-03:
//   gap 2 — finalised mixes are retained (prerequisite for proof adoption)
//   gap 1 — Pipeline.Accept is reachable through a registered acceptor
//   gap 5 — inbound proofs are validated (boundary slot, wrong epoch, group/T)
//   gap 3 — persisted entropy rehydrates in ascending epoch order

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"

	"gossipnode/config"
)

// ---- gap 2: mix retention -------------------------------------------------

func TestFinalisedMixIsRetained(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)

	mix := randao.Seed{0xAB, 0xCD, 0xEF}
	if !RememberFinalisedMixForTest(4, mix) {
		t.Fatal("first remember must succeed")
	}
	got, ok := FinalisedMixFor(4)
	if !ok {
		t.Fatal("mix for a finalised epoch must be retrievable — without it Pipeline.Accept is unreachable")
	}
	if got != mix {
		t.Fatalf("mix = %x, want %x", got, mix)
	}
	if _, ok := FinalisedMixFor(99); ok {
		t.Fatal("an epoch this node never finalised must report absent, not a zero mix")
	}
}

func TestMixStoreRefusesConflictingValue(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)

	RememberFinalisedMixForTest(7, randao.Seed{0x01})
	if RememberFinalisedMixForTest(7, randao.Seed{0x01}) != true {
		t.Fatal("re-recording the identical mix must be idempotent")
	}
	if RememberFinalisedMixForTest(7, randao.Seed{0x02}) != false {
		t.Fatal("a CONFLICTING mix must be refused — an epoch's mix is final, and replacing it " +
			"changes which proofs verify after the fact")
	}
	got, _ := FinalisedMixFor(7)
	if got != (randao.Seed{0x01}) {
		t.Fatalf("the first mix must survive the conflict, got %x", got)
	}
}

// ---- gaps 1 + 5: proof adoption and validation ----------------------------

// stubAcceptor records what reached the pipeline so the tests can prove a
// rejection happened BEFORE any cryptographic work, not after.
type stubAcceptor struct {
	called bool
	epoch  uint64
	mix    randao.Seed
	err    error
}

func (s *stubAcceptor) fn(forEpoch uint64, mix randao.Seed, _ []byte) error {
	s.called = true
	s.epoch = forEpoch
	s.mix = mix
	return s.err
}

func withAcceptor(t *testing.T, s *stubAcceptor) {
	t.Helper()
	SetVDFProofAcceptor(s.fn)
	t.Cleanup(func() { SetVDFProofAcceptor(nil) })
}

func boundaryBlock(epoch uint64) *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber: 100,
		Slot:        EpochBoundarySlot(epoch),
		SeedEpoch:   epoch,
		VdfProof:    []byte(`{"Y":1,"Pi":1,"T":5,"Group":"g"}`),
	}
}

func TestAcceptorIsReachedForAValidBoundaryBlock(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)
	mix := randao.Seed{0x11, 0x22}
	RememberFinalisedMixForTest(2, mix) // predecessor of epoch 3

	st := &stubAcceptor{}
	withAcceptor(t, st)

	if err := VerifyAndAcceptVDFProof(boundaryBlock(3)); err != nil {
		t.Fatalf("valid boundary block must reach the acceptor: %v", err)
	}
	if !st.called {
		t.Fatal("Pipeline.Accept was never reached — this is the zero-caller gap")
	}
	if st.epoch != 3 {
		t.Fatalf("acceptor got epoch %d, want 3", st.epoch)
	}
	if st.mix != mix {
		t.Fatalf("acceptor got mix %x, want the LOCALLY finalised %x — the mix must never come "+
			"from the block", st.mix, mix)
	}
}

func TestProofRejectedOffBoundarySlot(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)
	RememberFinalisedMixForTest(2, randao.Seed{0x11})

	st := &stubAcceptor{}
	withAcceptor(t, st)

	b := boundaryBlock(3)
	b.Slot++ // one past the boundary
	err := VerifyAndAcceptVDFProof(b)
	if !errors.Is(err, ErrProofNotOnBoundary) {
		t.Fatalf("want ErrProofNotOnBoundary, got %v", err)
	}
	if st.called {
		t.Fatal("a proof off the boundary slot must be rejected BEFORE the pipeline is touched")
	}
}

func TestProofRejectedWrongEpoch(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)
	RememberFinalisedMixForTest(2, randao.Seed{0x11})
	RememberFinalisedMixForTest(4, randao.Seed{0x22})

	st := &stubAcceptor{}
	withAcceptor(t, st)

	// Slot belongs to epoch 3, but the proposer declares epoch 5. Nothing
	// inside vdf.Proof names an epoch, so without this check a proof sealed
	// for one epoch publishes under another.
	b := boundaryBlock(3)
	b.SeedEpoch = 5
	err := VerifyAndAcceptVDFProof(b)
	if err == nil {
		t.Fatal("a wrong-epoch proof must be rejected")
	}
	if !errors.Is(err, ErrProofNotOnBoundary) && !errors.Is(err, ErrProofEpochMismatch) {
		t.Fatalf("want a boundary/epoch rejection, got %v", err)
	}
	if st.called {
		t.Fatal("a wrong-epoch proof must never reach the pipeline")
	}
}

func TestProofNotAdoptedWithoutLocalMix(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)
	// Deliberately record NO mix — the state a restarted or syncing node is in.

	st := &stubAcceptor{}
	withAcceptor(t, st)

	err := VerifyAndAcceptVDFProof(boundaryBlock(3))
	if !errors.Is(err, ErrMixUnavailable) {
		t.Fatalf("want ErrMixUnavailable, got %v", err)
	}
	if st.called {
		t.Fatal("a node with no independent mix must NOT hand the proof to the pipeline — " +
			"verifying against a mix taken from the block would accept any proof the proposer chose")
	}
}

func TestNoProofOrStageOneIsNotAnError(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)

	SetVDFProofAcceptor(nil)
	if err := VerifyAndAcceptVDFProof(&config.ZKBlock{BlockNumber: 1}); err != nil {
		t.Fatalf("a block with no VdfProof must be a silent no-op, got %v", err)
	}
	if err := VerifyAndAcceptVDFProof(boundaryBlock(3)); !errors.Is(err, ErrNoVDFAcceptor) {
		t.Fatalf("Stage 1 must report ErrNoVDFAcceptor, got %v", err)
	}
}

func TestAdoptionIsIdempotent(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)
	RememberFinalisedMixForTest(2, randao.Seed{0x11})

	st := &stubAcceptor{}
	withAcceptor(t, st)

	for i := 1; i <= 3; i++ {
		if err := VerifyAndAcceptVDFProof(boundaryBlock(3)); err != nil {
			t.Fatalf("adoption #%d must be idempotent, got %v", i, err)
		}
	}
}

// ---- gap 3: rehydration ordering -----------------------------------------

// TestRehydrationSurvivorSetIsOrderIndependent pins what BeaconSource
// retention ACTUALLY does.
//
// This test was originally written to prove that ascending replay keeps more
// epochs than descending. It failed, and the code was right: evictLocked uses
// cutoff = newest-retain with newest = max(all published), so the survivor set
// is {e : e >= max-retain} whatever order the values arrive in. The comments
// claiming ascending order was "load-bearing" were corrected to match.
//
// It is kept as a regression pin: if BeaconSource ever becomes order-sensitive,
// rehydration has to be revisited, and this fails loudly.
func TestRehydrationSurvivorSetIsOrderIndependent(t *testing.T) {
	entropyFor := func(e uint64) []byte {
		b := make([]byte, 32)
		b[0] = byte(e)
		return b
	}
	survivors := func(order []uint64, probe []uint64) []uint64 {
		src, err := committee.NewBeaconSource(committee.MinRetainedEpochs)
		if err != nil {
			t.Fatalf("NewBeaconSource: %v", err)
		}
		for _, e := range order {
			_ = src.Publish(e, entropyFor(e))
		}
		var kept []uint64
		for _, e := range probe {
			if src.Has(e) {
				kept = append(kept, e)
			}
		}
		return kept
	}

	probe := []uint64{5, 10, 11, 12, 20}
	cases := [][]uint64{
		{5, 10, 11, 12, 20},
		{20, 12, 11, 10, 5},
		{11, 20, 5, 12, 10},
	}
	want := survivors(cases[0], probe)
	for i, order := range cases[1:] {
		got := survivors(order, probe)
		if len(got) != len(want) {
			t.Fatalf("order %d kept %v, ascending kept %v — retention became order-sensitive, "+
				"revisit RehydrateBeaconFromDisk", i+1, got, want)
		}
		for j := range got {
			if got[j] != want[j] {
				t.Fatalf("order %d kept %v, ascending kept %v", i+1, got, want)
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("test is vacuous — nothing survived in any order")
	}
}
