package messaging

// An epoch whose fallback reached its deadline short of signers used to get no
// seed at all: no mix, no VDF, no proof for the next boundary block, and the
// chain halted for good. These tests pin the last resort that replaces that:
// fold the signers the window did collect, or hash the epoch's own entropy if
// it collected none, and fail closed only without that entropy.

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/prometheus/client_golang/prometheus/testutil"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/metrics"
)

func withNoMixPersistence(t *testing.T) {
	t.Helper()
	orig := entropyMixPersist
	entropyMixPersist = func(uint64, randao.Seed, string) error { return nil }
	t.Cleanup(func() {
		entropyMixPersist = orig
		ResetMixStoreForTest()
	})
}

func withNoBeacon(t *testing.T) {
	t.Helper()
	saved := beaconSource
	beaconSource = nil
	t.Cleanup(func() { beaconSource = saved })
}

func TestLastResortSeed_FoldsTheSignersCollected(t *testing.T) {
	resetAggSigStore(t)
	withNoBeacon(t)
	const epoch = 40
	start, deadline := testCollectionBounds(t, epoch)
	want := []randao.AggSig{
		{Slot: start, Sig: testAggSig(0x01)},
		{Slot: start + 2, Sig: testAggSig(0x02)},
		{Slot: start + 5, Sig: testAggSig(0x03)},
	}
	for _, s := range want {
		mustRecordAggSig(t, s.Slot, s.Sig)
	}

	if _, err := FallbackSeedForEpoch(epoch, deadline); !errors.Is(err, ErrFallbackDeadlineExceeded) {
		t.Fatalf("precondition: 3 of 5 signers at the deadline must be ErrFallbackDeadlineExceeded, got %v", err)
	}

	got, err := LastResortSeedForEpoch(epoch)
	if err != nil {
		t.Fatalf("3 signers collected must give a last-resort seed: %v", err)
	}
	expected, err := randao.FallbackFromCommittedSigners(BLS_Signer.DomainChainID(), epoch, start, deadline, 3, want)
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("seed %v, want the fold of exactly the 3 collected signers %v", got, expected)
	}
}

func TestLastResortSeed_NoSignersHashesTheEpochEntropy(t *testing.T) {
	resetAggSigStore(t)
	const epoch = 41
	entropy := fakeEntropy(0x5a, 32)
	withBeaconEntropy(t, map[uint64][]byte{epoch: entropy})

	got, err := LastResortSeedForEpoch(epoch)
	if err != nil {
		t.Fatalf("no signers but the epoch's entropy held must still give a seed: %v", err)
	}
	if got != lastResortHash(BLS_Signer.DomainChainID(), epoch, entropy) {
		t.Fatalf("seed %v is not H(domain, chain, epoch, ENTROPY(epoch))", got)
	}
	if got == (randao.Seed{}) {
		t.Fatal("zero seed")
	}
	if got == lastResortHash(BLS_Signer.DomainChainID(), epoch, fakeEntropy(0x5b, 32)) {
		t.Fatal("the seed must depend on the epoch's entropy")
	}
	if got == lastResortHash(BLS_Signer.DomainChainID(), epoch+1, entropy) {
		t.Fatal("the seed must depend on the epoch")
	}
}

func TestLastResortSeed_NoSignersNoEntropyFailsClosed(t *testing.T) {
	resetAggSigStore(t)

	t.Run("no beacon", func(t *testing.T) {
		withNoBeacon(t)
		if _, err := LastResortSeedForEpoch(42); !errors.Is(err, ErrLastResortNoEntropy) {
			t.Fatalf("got %v, want ErrLastResortNoEntropy", err)
		}
	})
	t.Run("beacon without this epoch", func(t *testing.T) {
		withBeaconEntropy(t, map[uint64][]byte{41: fakeEntropy(0x11, 32)})
		if _, err := LastResortSeedForEpoch(42); !errors.Is(err, ErrLastResortNoEntropy) {
			t.Fatalf("got %v, want ErrLastResortNoEntropy", err)
		}
	})
}

// Every node, and the same node replaying after a restart, folds the same
// committed certificates in whatever order it recorded them.
func TestLastResortSeed_SameSignersSameSeed(t *testing.T) {
	const epoch = 43
	withNoBeacon(t)
	start, _ := testCollectionBounds(t, epoch)

	resetAggSigStore(t)
	mustRecordAggSig(t, start+1, testAggSig(0x07))
	mustRecordAggSig(t, start+4, testAggSig(0x08))
	first, err := LastResortSeedForEpoch(epoch)
	if err != nil {
		t.Fatal(err)
	}

	resetAggSigStore(t)
	mustRecordAggSig(t, start+4, testAggSig(0x08))
	mustRecordAggSig(t, start+1, testAggSig(0x07))
	replayed, err := LastResortSeedForEpoch(epoch)
	if err != nil {
		t.Fatal(err)
	}
	if first != replayed {
		t.Fatalf("same signers gave %v then %v", first, replayed)
	}
}

func TestResolvePendingFallbacks_FinalisesFromLastResortAtDeadline(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)
	withNoMixPersistence(t)
	withNoBeacon(t)
	const epoch = 44

	finaliseTrackMu.Lock()
	pendingFallback[epoch] = struct{}{}
	finaliseTrackMu.Unlock()

	start, deadline := testCollectionBounds(t, epoch)
	mustRecordAggSig(t, start, testAggSig(0x01))
	mustRecordAggSig(t, start+3, testAggSig(0x02)) // 2 of 5

	var gotEpoch uint64
	var gotSeed randao.Seed
	var notified bool
	SetEpochFinalisedHook(func(e uint64, s randao.Seed) { notified, gotEpoch, gotSeed = true, e, s })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	want, err := LastResortSeedForEpoch(epoch)
	if err != nil {
		t.Fatal(err)
	}
	resolvePendingFallbacks(&config.ZKBlock{Slot: deadline, BlockNumber: deadline})

	if !notified || gotEpoch != epoch {
		t.Fatalf("epoch %d reached its deadline with 2 signers and was not finalised: the next boundary block can never carry a proof and the chain halts", epoch)
	}
	if gotSeed != want {
		t.Fatalf("finalised with %v, want the last-resort seed %v", gotSeed, want)
	}
	if mix, ok := FinalisedMixFor(epoch); !ok || mix != want {
		t.Fatalf("the mix store must hold the last-resort seed so peers' proofs verify: got %v, %v", mix, ok)
	}
	finaliseTrackMu.Lock()
	_, stillPending := pendingFallback[epoch]
	finaliseTrackMu.Unlock()
	if stillPending {
		t.Fatal("finalised epoch left pending")
	}
}

func TestResolvePendingFallbacks_LastResortFromEntropyWhenNoSigners(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)
	withNoMixPersistence(t)
	const epoch = 45
	entropy := fakeEntropy(0x33, 32)
	withBeaconEntropy(t, map[uint64][]byte{epoch: entropy})

	finaliseTrackMu.Lock()
	pendingFallback[epoch] = struct{}{}
	finaliseTrackMu.Unlock()

	var gotSeed randao.Seed
	var notified bool
	SetEpochFinalisedHook(func(_ uint64, s randao.Seed) { notified, gotSeed = true, s })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	_, deadline := testCollectionBounds(t, epoch)
	resolvePendingFallbacks(&config.ZKBlock{Slot: deadline, BlockNumber: deadline})

	if !notified {
		t.Fatal("no signers but the epoch's entropy held: the epoch must still finalise")
	}
	if gotSeed != lastResortHash(BLS_Signer.DomainChainID(), epoch, entropy) {
		t.Fatalf("finalised with %v, want the entropy-derived last-resort seed", gotSeed)
	}
}

func resetLastResortMixes(t *testing.T) {
	t.Helper()
	lastResortMu.Lock()
	saved, savedProofs := lastResortMixes, heldLastResortProofs
	lastResortMixes = make(map[uint64]struct{})
	heldLastResortProofs = make(map[uint64][]byte)
	lastResortMu.Unlock()
	t.Cleanup(func() {
		lastResortMu.Lock()
		lastResortMixes, heldLastResortProofs = saved, savedProofs
		lastResortMu.Unlock()
	})
}

// withDurableMixes backs the durable mix record with a map, so a test can
// "restart" by clearing memory and restoring from it.
func withDurableMixes(t *testing.T) map[uint64]randao.Seed {
	t.Helper()
	durable := make(map[uint64]randao.Seed)
	origPersist, origLoad := entropyMixPersist, entropyMixLoad
	entropyMixPersist = func(e uint64, s randao.Seed, _ string) error {
		if _, ok := durable[e]; !ok { // first writer wins, like the KV
			durable[e] = s
		}
		return nil
	}
	entropyMixLoad = func(e uint64) (randao.Seed, bool, error) {
		s, ok := durable[e]
		return s, ok, nil
	}
	t.Cleanup(func() {
		entropyMixPersist, entropyMixLoad = origPersist, origLoad
		ResetMixStoreForTest()
	})
	return durable
}

func resolveAt(t *testing.T, epoch, slot uint64) {
	t.Helper()
	finaliseTrackMu.Lock()
	pendingFallback[epoch] = struct{}{}
	finaliseTrackMu.Unlock()
	resolvePendingFallbacks(&config.ZKBlock{Slot: slot, BlockNumber: slot})
}

// A node that missed certificates its peers recorded takes the last resort
// while they fold all five. A restart, whose replay records all five, must
// bring it back to the fleet's mix: the last-resort mix must not have been
// made durable, or it would outrank the replay forever.
func TestLastResort_NotDurableSoARestartRecoversTheFleetMix(t *testing.T) {
	resetFinaliseTracking(t)
	resetLastResortMixes(t)
	withNoBeacon(t)
	durable := withDurableMixes(t)
	SetEpochFinalisedHook(nil)
	const epoch = 46
	start, deadline := testCollectionBounds(t, epoch)

	// Before the restart: this node recorded only 2 of the window's 5.
	resetAggSigStore(t)
	mustRecordAggSig(t, start, testAggSig(0x01))
	mustRecordAggSig(t, start+1, testAggSig(0x02))
	resolveAt(t, epoch, deadline)
	lastResort, ok := FinalisedMixFor(epoch)
	if !ok {
		t.Fatal("precondition: the last resort must finalise the epoch")
	}
	if _, written := durable[epoch]; written {
		t.Error("the last-resort mix was made durable: it would outrank the replay after a restart")
	}

	// Restart: memory gone, durable records restored, then the replay sees
	// all five certificates the fleet folded.
	ResetMixStoreForTest()
	lastResortMu.Lock()
	lastResortMixes = make(map[uint64]struct{})
	lastResortMu.Unlock()
	if _, err := RestoreEntropyMixes(epoch, epoch); err != nil {
		t.Fatal(err)
	}
	resetAggSigStore(t)
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start+i, testAggSig(byte(i+1)))
	}
	resolveAt(t, epoch, start+FallbackFoldBufferB-1)

	fleet, err := FallbackSeedForEpoch(epoch, start+FallbackFoldBufferB-1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := FinalisedMixFor(epoch)
	if got != fleet {
		t.Fatalf("after the restart the node holds %v, want the fleet's mix %v (last resort was %v)", got, fleet, lastResort)
	}
}

// A full fold or a mixed epoch is still made durable as before.
func TestLastResort_OtherOutcomesStillDurable(t *testing.T) {
	resetFinaliseTracking(t)
	resetLastResortMixes(t)
	durable := withDurableMixes(t)
	SetEpochFinalisedHook(nil)
	const epoch = 47
	start, _ := testCollectionBounds(t, epoch)
	resetAggSigStore(t)
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start+i, testAggSig(byte(i+1)))
	}
	resolveAt(t, epoch, start+FallbackFoldBufferB-1)
	if _, written := durable[epoch]; !written {
		t.Fatal("a full-fold mix must still be made durable")
	}
}

// ENTROPY(E+1) is sealed from mix(E). When mix(E) was the last resort, neither
// that entropy nor its proof may be written either, or the restart would
// rehydrate the diverged value straight back.
func TestLastResort_DerivedEntropyAndProofNotDurable(t *testing.T) {
	resetLastResortMixes(t)
	const epoch = 48
	markLastResortMix(epoch)
	withBeaconEntropy(t, map[uint64][]byte{
		epoch:     fakeEntropy(0x01, 32),
		epoch + 1: fakeEntropy(0x02, 32),
	})

	var entropyWrites []uint64
	origEntropy := recordBeaconEntropyFn
	recordBeaconEntropyFn = func(_ *config.PooledConnection, e uint64, _ []byte) error {
		entropyWrites = append(entropyWrites, e)
		return nil
	}
	t.Cleanup(func() { recordBeaconEntropyFn = origEntropy })
	proofWrites := captureVDFProofWrites(t)

	if err := PersistEpochEntropy(epoch + 1); err != nil {
		t.Fatal(err)
	}
	if err := PersistVDFProof(epoch+1, testVDFProofEncoding(t)); err != nil {
		t.Fatal(err)
	}
	if len(entropyWrites) != 0 || len(*proofWrites) != 0 {
		t.Fatalf("entropy/proof sealed from the last-resort mix of epoch %d were written: entropy %v, %d proof(s)", epoch, entropyWrites, len(*proofWrites))
	}

	// ENTROPY(epoch) was sealed from mix(epoch-1), a normal mix: still durable.
	if err := PersistEpochEntropy(epoch); err != nil {
		t.Fatal(err)
	}
	if err := PersistVDFProof(epoch, testVDFProofEncoding(t)); err != nil {
		t.Fatal(err)
	}
	if len(entropyWrites) != 1 || entropyWrites[0] != epoch || len(*proofWrites) != 1 {
		t.Fatalf("entropy and proof for epoch %d must still be written: entropy %v, %d proof(s)", epoch, entropyWrites, len(*proofWrites))
	}
}

func TestLastResortMixes_Bounded(t *testing.T) {
	resetLastResortMixes(t)
	for e := uint64(0); e < 100; e++ {
		markLastResortMix(e)
	}
	lastResortMu.Lock()
	n := len(lastResortMixes)
	lastResortMu.Unlock()
	if n > lastResortRetainEpochs+1 {
		t.Fatalf("%d epochs retained, want at most %d", n, lastResortRetainEpochs+1)
	}
	if !sealedFromLastResortMix(100) || sealedFromLastResortMix(50) {
		t.Fatal("the newest marks must be kept and old ones pruned")
	}
}

// withLastResortBoundaryAcceptor installs a VDF acceptor that accepts a proof
// only over want, as the real one does for a proof sealed from another mix.
func withLastResortBoundaryAcceptor(t *testing.T, want randao.Seed) {
	t.Helper()
	SetVDFProofAcceptor(func(_ uint64, mix randao.Seed, _ []byte) error {
		if mix != want {
			return errors.New("proof does not verify over this mix")
		}
		return nil
	})
	t.Cleanup(func() { SetVDFProofAcceptor(nil) })
}

// captureEntropyWrites backs recordBeaconEntropyFn with a map.
func captureEntropyWrites(t *testing.T) map[uint64][]byte {
	t.Helper()
	written := make(map[uint64][]byte)
	orig := recordBeaconEntropyFn
	recordBeaconEntropyFn = func(_ *config.PooledConnection, e uint64, v []byte) error {
		if _, ok := written[e]; !ok { // first writer wins, like the KV
			written[e] = append([]byte(nil), v...)
		}
		return nil
	}
	t.Cleanup(func() { recordBeaconEntropyFn = orig })
	return written
}

func boundaryBlockFor(t *testing.T, epoch uint64) *config.ZKBlock {
	t.Helper()
	slot := EpochBoundarySlot(epoch)
	return &config.ZKBlock{VdfProof: testVDFProofEncoding(t), SeedEpoch: epoch, Slot: slot, BlockNumber: slot}
}

// The next boundary block's proof verifying against this node's last-resort
// mix confirms it: the mix, ENTROPY(E+1) and the proof are all written, so a
// later restart or a recovering peer can find them.
func TestLastResort_ConfirmedByBoundaryProofBecomesDurable(t *testing.T) {
	resetLastResortMixes(t)
	durable := withDurableMixes(t)
	const epoch = 50
	seed := randao.Seed{0x50}
	RememberFinalisedMixForTest(epoch, seed)
	persistFinalisedMix(epoch, seed, mixOutcomeLastResort)
	if _, written := durable[epoch]; written {
		t.Fatal("precondition: an unconfirmed last-resort mix must not be durable")
	}

	withBeaconEntropy(t, map[uint64][]byte{epoch + 1: fakeEntropy(0x51, 32)})
	entropy := captureEntropyWrites(t)
	proofs := captureVDFProofWrites(t)
	withLastResortBoundaryAcceptor(t, seed)

	if err := VerifyAndAcceptVDFProof(boundaryBlockFor(t, epoch+1)); err != nil {
		t.Fatalf("a proof over this node's mix must be accepted: %v", err)
	}
	if got, written := durable[epoch]; !written || got != seed {
		t.Errorf("confirmed last-resort mix not durable (got %v, %v)", got, written)
	}
	if _, written := entropy[epoch+1]; !written {
		t.Errorf("ENTROPY(%d), sealed from the confirmed mix, not durable", epoch+1)
	}
	if len(*proofs) != 1 {
		t.Errorf("%d proofs written, want 1: a recovering peer could not get epoch %d from this node", len(*proofs), epoch+1)
	}
	if sealedFromLastResortMix(epoch + 1) {
		t.Error("confirmed mix still marked unconfirmed")
	}
}

// A proof over a different mix (this node diverged) confirms nothing: the
// diverged mix and anything from it stay out of durable storage.
func TestLastResort_ProofOverAnotherMixDoesNotConfirm(t *testing.T) {
	resetLastResortMixes(t)
	durable := withDurableMixes(t)
	const epoch = 52
	mine := randao.Seed{0x52}
	RememberFinalisedMixForTest(epoch, mine)
	persistFinalisedMix(epoch, mine, mixOutcomeLastResort)

	withBeaconEntropy(t, map[uint64][]byte{})
	entropy := captureEntropyWrites(t)
	proofs := captureVDFProofWrites(t)
	withLastResortBoundaryAcceptor(t, randao.Seed{0x99}) // the sealer's mix

	if err := VerifyAndAcceptVDFProof(boundaryBlockFor(t, epoch+1)); err == nil {
		t.Fatal("precondition: a proof over another mix must be rejected")
	}
	if _, written := durable[epoch]; written {
		t.Error("a diverged last-resort mix was made durable")
	}
	if len(entropy) != 0 || len(*proofs) != 0 {
		t.Errorf("written from a diverged mix: entropy %v, %d proof(s)", entropy, len(*proofs))
	}
	if !sealedFromLastResortMix(epoch + 1) {
		t.Error("an unconfirmed mix must stay marked")
	}
}

// Two consecutive epochs with no signers, then a restart past the replay of
// the first. The second's last resort hashes ENTROPY(second), sealed from the
// first's last-resort mix; after the restart only durable entropy is held, so
// that entropy must have been written once the boundary proof confirmed it.
func TestLastResort_TwoEmptyEpochsThenRestartStillDerivesTheSeed(t *testing.T) {
	resetFinaliseTracking(t)
	resetLastResortMixes(t)
	resetAggSigStore(t)
	durable := withDurableMixes(t)
	SetEpochFinalisedHook(nil)
	const first, second = 60, 61
	entropy := captureEntropyWrites(t)
	_ = captureVDFProofWrites(t)

	// Epoch 60: no signers, last resort from ENTROPY(60).
	withBeaconEntropy(t, map[uint64][]byte{first: fakeEntropy(0x60, 32)})
	_, deadline := testCollectionBounds(t, first)
	resolveAt(t, first, deadline)
	mixFirst, ok := FinalisedMixFor(first)
	if !ok {
		t.Fatal("precondition: epoch 60 must finalise from the last resort")
	}

	// ENTROPY(61) sealed from mix(60); the boundary block of 61 carries its
	// proof, which verifies against this node's mix(60).
	if err := beaconSource.Publish(second, fakeEntropy(0x61, 32)); err != nil {
		t.Fatal(err)
	}
	withLastResortBoundaryAcceptor(t, mixFirst)
	if err := VerifyAndAcceptVDFProof(boundaryBlockFor(t, second)); err != nil {
		t.Fatal(err)
	}

	// Epoch 61: no signers either.
	_, deadline = testCollectionBounds(t, second)
	resolveAt(t, second, deadline)
	mixSecond, ok := FinalisedMixFor(second)
	if !ok {
		t.Fatal("precondition: epoch 61 must finalise from the last resort")
	}

	// Restart in epoch 62: memory gone, the beacon rehydrated from durable
	// entropy only, the replay starts at epoch 61.
	ResetMixStoreForTest()
	lastResortMu.Lock()
	lastResortMixes = make(map[uint64]struct{})
	lastResortMu.Unlock()
	withBeaconEntropy(t, entropy)
	if _, err := RestoreEntropyMixes(first, second); err != nil {
		t.Fatal(err)
	}
	if got, ok := FinalisedMixFor(first); !ok || got != mixFirst {
		t.Errorf("confirmed mix(60) not restored (got %v, %v; durable %v)", got, ok, durable)
	}

	seed, err := LastResortSeedForEpoch(second)
	if err != nil {
		t.Fatalf("after the restart epoch 61's last resort cannot be derived, so the node stalls: %v", err)
	}
	if seed != mixSecond {
		t.Fatalf("after the restart epoch 61 derives %v, want %v", seed, mixSecond)
	}
}

func resetLastResortStreak(t *testing.T) {
	t.Helper()
	lastResortMu.Lock()
	saved, savedEnd := lastResortStreak, lastResortStreakEnd
	lastResortStreak, lastResortStreakEnd = 0, 0
	lastResortMu.Unlock()
	t.Cleanup(func() {
		lastResortMu.Lock()
		lastResortStreak, lastResortStreakEnd = saved, savedEnd
		lastResortMu.Unlock()
	})
}

func TestLastResortMetrics_CountAndConsecutive(t *testing.T) {
	resetLastResortStreak(t)
	before := testutil.ToFloat64(metrics.EntropyLastResortTotal)
	consecutive := func() float64 { return testutil.ToFloat64(metrics.EntropyLastResortConsecutive) }

	noteFinalisedOutcome(70, mixOutcomeLastResort)
	if consecutive() != 1 {
		t.Fatalf("consecutive %v, want 1", consecutive())
	}
	noteFinalisedOutcome(71, mixOutcomeLastResort)
	if consecutive() != 2 {
		t.Fatalf("consecutive %v, want 2", consecutive())
	}

	// A replay re-deciding epoch 71 changes nothing and is not counted again.
	entropyQuietFinalise.Store(true)
	noteFinalisedOutcome(71, mixOutcomeLastResort)
	entropyQuietFinalise.Store(false)
	if consecutive() != 2 {
		t.Fatalf("replay changed consecutive to %v", consecutive())
	}
	if got := testutil.ToFloat64(metrics.EntropyLastResortTotal) - before; got != 2 {
		t.Fatalf("counted %v last-resort epochs, want 2", got)
	}

	noteFinalisedOutcome(72, mixOutcomeFallback)
	if consecutive() != 0 {
		t.Fatalf("a full fold must reset consecutive, got %v", consecutive())
	}
	noteFinalisedOutcome(74, mixOutcomeLastResort)
	if consecutive() != 1 {
		t.Fatalf("a non-adjacent last resort must restart at 1, got %v", consecutive())
	}
}

// withProofStore backs the durable proof record with a map, for both writes
// and LookupVDFProof's reads.
func withProofStore(t *testing.T) map[uint64][]byte {
	t.Helper()
	stored := make(map[uint64][]byte)
	origRecord, origGet := recordVDFProofFn, getVDFProofFn
	recordVDFProofFn = func(_ *config.PooledConnection, e uint64, encoded []byte) error {
		if _, ok := stored[e]; !ok { // first writer wins, like the KV
			stored[e] = append([]byte(nil), encoded...)
		}
		return nil
	}
	getVDFProofFn = func(_ *config.PooledConnection, e uint64) ([]byte, bool, error) {
		raw, ok := stored[e]
		return raw, ok, nil
	}
	t.Cleanup(func() { recordVDFProofFn, getVDFProofFn = origRecord, origGet })
	return stored
}

// A proof sealed from an unconfirmed last-resort mix is kept out of storage,
// but LookupVDFProof must still return it: the boundary block that confirms
// the mix has to carry it, and once the sealer is gone the sequencer finds it
// only through LookupVDFProof (SealerResultFor's fallback). Before, it
// answered not-found, and the boundary block waited on its own confirmation.
func TestLastResort_HeldProofServedUntilConfirmed(t *testing.T) {
	resetLastResortMixes(t)
	_ = withDurableMixes(t)
	stored := withProofStore(t)
	const epoch = 80
	proof := testVDFProofEncoding(t)
	markLastResortMix(epoch)

	if err := PersistVDFProof(epoch+1, proof); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatal("precondition: a proof from an unconfirmed last-resort mix must not be stored")
	}
	got, ok := LookupVDFProof(epoch + 1)
	if !ok {
		t.Fatalf("the unconfirmed proof for epoch %d is not served: the boundary block that would confirm it cannot be built from it", epoch+1)
	}
	if string(got) != string(proof) {
		t.Fatal("served a different proof than the one held")
	}
	if _, ok := LookupVDFProof(epoch + 2); ok {
		t.Fatal("served a proof for an epoch nothing holds")
	}

	// Confirmed: the proof is stored and the memory copy dropped.
	confirmLastResortMix(epoch, randao.Seed{0x80})
	if err := PersistVDFProof(epoch+1, proof); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored[epoch+1]; !ok {
		t.Fatal("confirmed proof not stored")
	}
	if _, held := heldLastResortProof(epoch + 1); held {
		t.Error("stored proof still held in memory")
	}
	if _, ok := LookupVDFProof(epoch + 1); !ok {
		t.Error("stored proof not served")
	}
}

func TestHeldLastResortProofs_Bounded(t *testing.T) {
	resetLastResortMixes(t)
	proof := testVDFProofEncoding(t)
	for e := uint64(0); e < 100; e++ {
		markLastResortMix(e)
		if !holdIfSealedFromLastResortMix(e+1, proof) {
			t.Fatalf("proof for epoch %d not held", e+1)
		}
	}
	lastResortMu.Lock()
	n := len(heldLastResortProofs)
	lastResortMu.Unlock()
	if n > lastResortRetainEpochs+1 {
		t.Fatalf("%d proofs held, want at most %d", n, lastResortRetainEpochs+1)
	}
	if _, ok := heldLastResortProof(100); !ok {
		t.Fatal("the newest held proof must be kept")
	}
	if _, ok := heldLastResortProof(50); ok {
		t.Fatal("old held proofs must be pruned")
	}
}
