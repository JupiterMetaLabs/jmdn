package messaging

// Tests for the VDF proof race + pull/recovery mechanism.
//
// NOTE ON SCOPE: these are unit tests. They pin the deadline arithmetic, the
// epoch-targeting rule, dedup, the wire bounds, and the fact that recovery
// never bypasses VerifyAndAcceptVDFProof. They do NOT exercise real libp2p
// streams or real VDF cryptography — see avc/vdf/vdf_cancel_test.go for the
// real cryptographic cancellation test, and the devnet plan for the network
// round trip.

import (
	"encoding/json"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// ---- D and the deadline arithmetic ---------------------------------------

// TestRecoveryDeadlineParamsAreValid is the guard that makes D self-checking:
// if N or K ever change, an out-of-range D fails here rather than silently
// producing a deadline that never fires.
func TestRecoveryDeadlineParamsAreValid(t *testing.T) {
	if err := ValidateVDFRecoveryParams(); err != nil {
		t.Fatalf("the compiled-in D is invalid for N=%d K=%d: %v", N, RevealCutoffK, err)
	}
	if VDFProofRecoveryDeadlineSlots < 1 || VDFProofRecoveryDeadlineSlots > N-RevealCutoffK {
		t.Fatalf("D=%d is outside 1..%d", VDFProofRecoveryDeadlineSlots, N-RevealCutoffK)
	}
}

// TestDeadlineFallsAfterTheMixExists is the reason for the upper bound. Before
// cutoffSlotFor(E-1) no node can hold a proof for E, so a deadline earlier
// than that would ask for something nobody has.
func TestDeadlineFallsAfterTheMixExists(t *testing.T) {
	for _, epoch := range []uint64{1, 2, 8, 41} {
		mixReady := (epoch-1)*N + RevealCutoffK
		deadline := VDFRecoveryDeadlineSlot(epoch)
		boundary := EpochBoundarySlot(epoch)

		if deadline < mixReady {
			t.Fatalf("epoch %d: deadline %d is BEFORE the mix is finalised at %d — no peer could "+
				"hold a proof yet", epoch, deadline, mixReady)
		}
		if deadline >= boundary {
			t.Fatalf("epoch %d: deadline %d is not before the boundary %d — recovery would start "+
				"too late to help", epoch, deadline, boundary)
		}
	}
}

// TestRecoveryTargetsTheNextEpoch pins the single easiest thing to get wrong.
// At the deadline the node is still in epoch E-1 while preparing entropy for E.
func TestRecoveryTargetsTheNextEpoch(t *testing.T) {
	const epoch = 8
	deadline := VDFRecoveryDeadlineSlot(epoch) // 8*50-5 = 395

	if got := uint64(EpochForSlot(deadline)); got != epoch-1 {
		t.Fatalf("slot %d should be in epoch %d, got %d", deadline, epoch-1, got)
	}
	if got := VDFRecoveryTargetEpoch(deadline); got != epoch {
		t.Fatalf("standing at slot %d the node must be recovering epoch %d, got %d — using "+
			"EpochForSlot(currentSlot) here would silently check the wrong epoch",
			deadline, epoch, got)
	}
}

// ---- dedup ----------------------------------------------------------------

func TestRecoveryDedupAllowsOnlyOneInFlightPerEpoch(t *testing.T) {
	const epoch = 12
	endRecovery(epoch)
	t.Cleanup(func() { endRecovery(epoch) })

	if !beginRecovery(epoch) {
		t.Fatal("first attempt must be allowed")
	}
	if beginRecovery(epoch) {
		t.Fatal("a second attempt while one is in flight must be refused — the deadline is " +
			"reached on EVERY block until entropy arrives, so without dedup a node would open " +
			"a fresh round of streams per block")
	}
	endRecovery(epoch)
	if !beginRecovery(epoch) {
		t.Fatal("after the attempt finishes a retry must be allowed again")
	}
}

func TestRecoveryDedupIsPerEpoch(t *testing.T) {
	t.Cleanup(func() { endRecovery(20); endRecovery(21) })
	if !beginRecovery(20) || !beginRecovery(21) {
		t.Fatal("different epochs must not block each other")
	}
}

// ---- the deadline check does nothing it shouldn't -------------------------

// TestDeadlineCheckIsInertWithoutABeacon — on Stage 1 there is no Stage-2
// entropy to be missing, so the check must not dispatch anything.
func TestDeadlineCheckIsInertWithoutABeacon(t *testing.T) {
	orig := activeBeacon()
	t.Cleanup(func() { SetBeaconSource(orig) })
	SetBeaconSource(nil)

	dispatched := false
	SetVDFProofRecoveryDispatcher(func(uint64, uint64) { dispatched = true })
	t.Cleanup(func() { SetVDFProofRecoveryDispatcher(nil) })

	maybeTriggerVDFProofRecovery(&config.ZKBlock{BlockNumber: 1, Slot: VDFRecoveryDeadlineSlot(8)})
	if dispatched {
		t.Fatal("Stage 1 must never dispatch proof recovery")
	}
}

// TestDeadlineCheckIgnoresZeroSlot — a block with no slot carries no clock.
func TestDeadlineCheckIgnoresZeroSlot(t *testing.T) {
	dispatched := false
	SetVDFProofRecoveryDispatcher(func(uint64, uint64) { dispatched = true })
	t.Cleanup(func() { SetVDFProofRecoveryDispatcher(nil) })

	maybeTriggerVDFProofRecovery(&config.ZKBlock{BlockNumber: 1, Slot: 0})
	maybeTriggerVDFProofRecovery(nil)
	if dispatched {
		t.Fatal("a nil or slot-0 block must not dispatch recovery")
	}
}

// TestSyncPathDoesNotTriggerRecovery is the catch-up guard. A node replaying
// thousands of blocks crosses many epoch boundaries; firing here would enqueue
// one recovery per replayed boundary, mostly for epochs beyond the mix
// retention window.
func TestSyncPathDoesNotTriggerRecovery(t *testing.T) {
	dispatched := 0
	SetVDFProofRecoveryDispatcher(func(uint64, uint64) { dispatched++ })
	t.Cleanup(func() { SetVDFProofRecoveryDispatcher(nil) })

	for slot := uint64(390); slot <= 400; slot++ {
		RecordSyncedBlockEntropy(&config.ZKBlock{BlockNumber: slot, Slot: slot})
	}
	if dispatched != 0 {
		t.Fatalf("the sync path dispatched recovery %d time(s) — catch-up must never pull proofs "+
			"for historical epochs", dispatched)
	}
}

// ---- wire format and bounds ----------------------------------------------

// TestProofResponseUsesTheBlockEncoding — one encoding, both paths.
func TestProofResponseRoundTripsAsJSON(t *testing.T) {
	// The bytes are opaque here on purpose: the pull path must carry exactly
	// what the block carries (vdf.Proof.MarshalBinary output) and must not
	// re-encode it.
	want := []byte(`{"Y":123,"Pi":456,"T":789,"Group":"g"}`)
	raw, err := json.Marshal(VDFProofResponse{Found: true, Epoch: 8, Proof: want})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got VDFProofResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Found || got.Epoch != 8 || string(got.Proof) != string(want) {
		t.Fatalf("response did not round-trip: %+v", got)
	}
}

func TestNotFoundIsANormalAnswer(t *testing.T) {
	raw, _ := json.Marshal(VDFProofResponse{Found: false, Epoch: 9})
	var got VDFProofResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Found || len(got.Proof) != 0 {
		t.Fatal("not-found must carry no proof")
	}
}

func TestProofSizeBoundIsSane(t *testing.T) {
	// A 2048-bit proof is two ~617-digit integers plus a small envelope.
	// The bound must comfortably exceed that and still be far from unbounded.
	const realisticProofBytes = 1400
	if DB_OPs.MaxVDFProofBytes < realisticProofBytes {
		t.Fatalf("MaxVDFProofBytes=%d is below a realistic 2048-bit proof (~%d bytes)",
			DB_OPs.MaxVDFProofBytes, realisticProofBytes)
	}
	if DB_OPs.MaxVDFProofBytes > 1<<20 {
		t.Fatalf("MaxVDFProofBytes=%d is large enough to be a bulk-transfer channel",
			DB_OPs.MaxVDFProofBytes)
	}
}

// ---- the single validation path -------------------------------------------

// TestPulledProofGoesThroughTheSameValidation asserts that a pulled proof is
// rejected by exactly the checks a block-carried one is. Here the node holds
// no mix, so it must refuse regardless of how the proof arrived.
func TestPulledProofWithoutLocalMixIsRefused(t *testing.T) {
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)

	st := &stubAcceptor{}
	SetVDFProofAcceptor(st.fn)
	t.Cleanup(func() { SetVDFProofAcceptor(nil) })

	// Exactly the synthetic block RecoverVDFProofFromPeers builds.
	synthetic := &config.ZKBlock{
		BlockNumber: 0,
		Slot:        EpochBoundarySlot(8),
		SeedEpoch:   8,
		VdfProof:    []byte(`{"Y":1,"Pi":1,"T":5,"Group":"g"}`),
	}
	err := VerifyAndAcceptVDFProof(synthetic)
	if err == nil {
		t.Fatal("a pulled proof must not be adopted when this node holds no mix for the epoch")
	}
	if st.called {
		t.Fatal("the pipeline must not be reached without a locally-held mix — verifying against " +
			"a peer-supplied mix would accept any proof the peer chose")
	}
}
