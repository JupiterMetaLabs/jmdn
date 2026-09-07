package Sequencer

import (
	"bytes"
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"
)

func resetBootstrapState(t *testing.T) {
	t.Helper()
	resetBootstrapEpochs()
	t.Cleanup(resetBootstrapEpochs)
}

// Two nodes with the same chain id, authority pin, seed and epoch MUST derive
// the same bytes - that is the whole point. And any one input differing MUST
// change them, or two networks could share a bootstrap committee schedule.
func TestBootstrapEntropy_DeterministicAndDomainSeparated(t *testing.T) {
	const pin = "1c79a531fc76abcdef"
	a := BootstrapEntropy(8000800, pin, "", 0)
	b := BootstrapEntropy(8000800, pin, "", 0)
	if !bytes.Equal(a, b) {
		t.Fatal("same inputs produced different bootstrap entropy")
	}
	if len(a) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(a))
	}
	// pin comparison is case/space-insensitive (hex pins are pasted both ways)
	if !bytes.Equal(a, BootstrapEntropy(8000800, "  1C79A531FC76ABCDEF ", "", 0)) {
		t.Fatal("authority pin should be normalised (trim + lowercase) before hashing")
	}
	variants := [][]byte{
		BootstrapEntropy(8000801, pin, "", 0), // chain id
		BootstrapEntropy(8000800, pin+"00", "", 0), // pin
		BootstrapEntropy(8000800, pin, "devnet-b", 0), // operator seed
		BootstrapEntropy(8000800, pin, "", 1), // epoch
	}
	for i, v := range variants {
		if bytes.Equal(a, v) {
			t.Fatalf("variant %d did not change the bootstrap entropy", i)
		}
	}
}

func TestPublishBootstrapEntropy_PublishesAllListedEpochsAndRecordsThem(t *testing.T) {
	resetBootstrapState(t)
	sink, err := committee.NewBeaconSource(committee.MinRetainedEpochs)
	if err != nil {
		t.Fatal(err)
	}
	// unsorted + duplicate on purpose
	if err := publishBootstrapEntropy(sink, 8000800, "abc123", "", []uint64{1, 0, 1}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for _, e := range []uint64{0, 1} {
		if !sink.Has(e) {
			t.Fatalf("sink should have bootstrap entropy for epoch %d", e)
		}
		if !IsBootstrapEpoch(e) {
			t.Fatalf("epoch %d should be recorded as bootstrap", e)
		}
		got, err := sink.EpochEntropy(committee.EntropyEpoch(e))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, BootstrapEntropy(8000800, "abc123", "", e)) {
			t.Fatalf("epoch %d: published bytes differ from BootstrapEntropy", e)
		}
	}
	if IsBootstrapEpoch(2) || sink.Has(2) {
		t.Fatal("epoch 2 was not listed and must not be bootstrapped")
	}
}

func TestPublishBootstrapEntropy_RefusesWithoutAuthorityPin(t *testing.T) {
	resetBootstrapState(t)
	sink, _ := committee.NewBeaconSource(committee.MinRetainedEpochs)
	err := publishBootstrapEntropy(sink, 8000800, "   ", "", []uint64{0})
	if !errors.Is(err, ErrBootstrapNeedsAuthorityPin) {
		t.Fatalf("expected ErrBootstrapNeedsAuthorityPin, got %v", err)
	}
	if sink.Has(0) || IsBootstrapEpoch(0) {
		t.Fatal("nothing must be published or recorded on refusal")
	}
}

func TestPublishBootstrapEntropy_EmptyListIsNoop(t *testing.T) {
	resetBootstrapState(t)
	sink, _ := committee.NewBeaconSource(committee.MinRetainedEpochs)
	if err := publishBootstrapEntropy(sink, 8000800, "abc", "", nil); err != nil {
		t.Fatalf("empty list must be a no-op, got %v", err)
	}
	if sink.Has(0) {
		t.Fatal("nothing should be published for an empty list")
	}
}

// A real seal for a bootstrapped epoch would Publish DIFFERENT entropy under
// the same key, which BeaconSource refuses -> SealResult.Err -> boundary
// block 503. onEpochFinalised must therefore not start a sealer for it.
func TestOnEpochFinalised_SkipsSealerForBootstrapSuccessorEpoch(t *testing.T) {
	resetVDFWiringState(t)
	resetBootstrapState(t)
	n := testFixtureModulus(t)
	t.Setenv("JMDN_AVC_VDF_MODULUS_HEX", n.Text(16))
	t.Setenv("JMDN_AVC_VDF_GROUP_NAME", "test-group")
	t.Setenv("JMDN_AVC_VDF_DIFFICULTY_T", "2")
	t.Setenv(allowUnpinnedModulusEnv, "1")
	if installed, err := InstallAVCBeaconFromEnv(); err != nil || !installed {
		t.Fatalf("install: installed=%v err=%v", installed, err)
	}

	// mark epoch 1 as bootstrap, then finalise epoch 0 (whose successor is 1)
	bootstrapEpochsMu.Lock()
	bootstrapEpochs[1] = struct{}{}
	bootstrapEpochsMu.Unlock()
	onEpochFinalised(0, randao.Seed{})

	vdfSealersMu.Lock()
	_, started := vdfSealers[1]
	vdfSealersMu.Unlock()
	if started {
		t.Fatal("a sealer was started for a bootstrap epoch; its Publish would collide with the pinned bootstrap value")
	}

	// control: a non-bootstrap successor still gets a sealer
	onEpochFinalised(1, randao.Seed{})
	vdfSealersMu.Lock()
	_, started = vdfSealers[2]
	vdfSealersMu.Unlock()
	if !started {
		t.Fatal("expected a sealer for epoch 2 (not bootstrapped)")
	}
}
