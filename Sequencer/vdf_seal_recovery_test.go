package Sequencer

// Restart recovery on the sealing side: a node that already sealed (or adopted)
// ENTROPY-forEpoch before restarting must not lose the proof it needs for the
// boundary block, and must not re-run ~T_vdf of work it already did - but it
// must never use a stored proof it has not verified.

import (
	"testing"
	"time"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/JupiterMetaLabs/avc/vdf"

	"gossipnode/messaging"
)

func withPersistedProofs(t *testing.T, proofs map[uint64][]byte, published map[uint64][]byte) {
	t.Helper()
	prevL, prevP := lookupPersistedProof, publishedEntropyFor
	lookupPersistedProof = func(e uint64) ([]byte, bool) { b, ok := proofs[e]; return b, ok }
	publishedEntropyFor = func(e uint64) ([]byte, bool) { b, ok := published[e]; return b, ok }
	t.Cleanup(func() { lookupPersistedProof, publishedEntropyFor = prevL, prevP })
}

func withCommittedSlot(t *testing.T, slot uint64) {
	t.Helper()
	prev := messaging.DefaultSlotStore
	messaging.DefaultSlotStore = messaging.NewSlotStore()
	messaging.DefaultSlotStore.SeedFromCommittedTip(slot, slot)
	t.Cleanup(func() { messaging.DefaultSlotStore = prev })
}

func evalProof(t *testing.T, g vdf.Group, mix randao.Seed) (vdf.Proof, []byte) {
	t.Helper()
	p, err := vdf.Eval(g, mix[:], testDifficulty)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return p, raw
}

func sealerStarted(forEpoch uint64) (exists, running bool) {
	vdfSealersMu.Lock()
	defer vdfSealersMu.Unlock()
	s, ok := vdfSealers[forEpoch]
	if !ok {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return true, s.cancel != nil
}

// Restart after sealing, before the boundary block: the persisted proof is
// verified against the mix and served as a completed seal, instantly.
func TestOnEpochFinalised_ReusesVerifiedPersistedProof(t *testing.T) {
	resetVDFWiringState(t)
	p, g := testPipeline(t)
	SetVDFPipeline(p)
	mix := randao.Seed{0x51}
	want, raw := evalProof(t, g, mix)
	withPersistedProofs(t, map[uint64][]byte{5: raw}, nil)
	withCommittedSlot(t, 230) // inside epoch 4, boundary of 5 (slot 250) not yet built

	onEpochFinalised(4, mix)

	res, ok := SealerResultFor(5)
	if !ok || res.Err != nil {
		t.Fatalf("persisted proof must be served immediately: ok=%v err=%v", ok, res.Err)
	}
	if res.Proof.Output() != want.Output() || res.Proof.T != want.T {
		t.Fatal("served proof differs from the persisted one")
	}
	if _, running := sealerStarted(5); running {
		t.Fatal("a VDF evaluation was started although a verified proof was on disk")
	}
	if !p.Ready(5) {
		t.Fatal("reusing the proof must publish its entropy (pipeline.Accept)")
	}
}

// A persisted proof that does not verify against THIS mix is ignored and the
// node seals from scratch.
func TestOnEpochFinalised_IgnoresPersistedProofForAnotherMix(t *testing.T) {
	resetVDFWiringState(t)
	p, g := testPipeline(t)
	SetVDFPipeline(p)
	_, wrongRaw := evalProof(t, g, randao.Seed{0x99})
	withPersistedProofs(t, map[uint64][]byte{5: wrongRaw}, nil)
	withCommittedSlot(t, 230)

	mix := randao.Seed{0x52}
	onEpochFinalised(4, mix)

	if _, running := sealerStarted(5); !running {
		t.Fatal("an unverifiable stored proof must fall through to sealing")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if res, ok := SealerResultFor(5); ok {
			if res.Err != nil {
				t.Fatalf("seal failed: %v", res.Err)
			}
			if !vdf.Verify(g, mix[:], res.Proof) {
				t.Fatal("sealed proof does not verify over the real mix")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("seal never finished")
}

// Entropy already published and its boundary block already committed: there
// is nothing left to produce, so no evaluation starts. With the boundary still
// ahead, the proof is needed, so it does.
func TestOnEpochFinalised_SkipsOnlyWhenEntropyPublishedAndBoundaryCommitted(t *testing.T) {
	resetVDFWiringState(t)
	p, g := testPipeline(t)
	SetVDFPipeline(p)
	mix := randao.Seed{0x53}
	proof, _ := evalProof(t, g, mix)
	if err := p.Accept(5, mix, proof); err != nil { // published (e.g. rehydrated)
		t.Fatal(err)
	}
	withPersistedProofs(t, nil, nil) // proof itself not on disk

	withCommittedSlot(t, 260) // boundary of 5 (slot 250) committed
	onEpochFinalised(4, mix)
	if exists, _ := sealerStarted(5); exists {
		t.Fatal("sealing started for an epoch whose entropy is published and boundary committed")
	}

	resetVDFWiringState(t)
	SetVDFPipeline(p)
	withCommittedSlot(t, 230) // boundary still ahead: the proof must be produced
	onEpochFinalised(4, mix)
	if _, running := sealerStarted(5); !running {
		t.Fatal("the boundary block still needs a proof - sealing must run")
	}
}

// SealerResultFor after a restart with no in-memory sealer: the persisted proof
// is used only if its output is exactly the entropy this node published and its
// difficulty is the pinned one.
func TestSealerResultFor_FallsBackToVerifiedPersistedProof(t *testing.T) {
	resetVDFWiringState(t)
	p, g := testPipeline(t)
	proof, raw := evalProof(t, g, randao.Seed{0x61})
	out := proof.Output()

	withPersistedProofs(t, map[uint64][]byte{9: raw}, map[uint64][]byte{9: out[:]})
	res, ok := SealerResultFor(9)
	if !ok || res.Err != nil || res.Proof.Output() != out {
		t.Fatalf("matching persisted proof must be served: ok=%v", ok)
	}

	resetVDFWiringState(t)
	other := make([]byte, 32)
	withPersistedProofs(t, map[uint64][]byte{9: raw}, map[uint64][]byte{9: other})
	if _, ok := SealerResultFor(9); ok {
		t.Fatal("a persisted proof whose output differs from the published entropy must not be served")
	}

	resetVDFWiringState(t)
	withPersistedProofs(t, map[uint64][]byte{9: raw}, nil)
	if _, ok := SealerResultFor(9); ok {
		t.Fatal("without published entropy there is nothing to check the proof against - not ready")
	}

	resetVDFWiringState(t)
	SetVDFPipeline(p)
	bad := proof
	bad.T = testDifficulty + 1
	badRaw, _ := bad.MarshalBinary()
	withPersistedProofs(t, map[uint64][]byte{9: badRaw}, map[uint64][]byte{9: out[:]})
	if _, ok := SealerResultFor(9); ok {
		t.Fatal("a persisted proof with a non-pinned difficulty must not be served")
	}
}
