package messaging

// Regression cover for D-58's actual fix: PersistEpochEntropy must not report
// success when it persisted nothing.
//
// Why this test exists rather than the one D-58's Verify clause describes.
// That clause asks to force the KV write to fail, restart, and assert the node
// names the lost epoch — which needs a live ThebeHandle and a restart harness
// that does not exist in this package (DB_OPs.RecordBeaconEntropy fails with
// "no ThebeHandle available" under `go test`). That verification is still
// owed and is recorded as owed on the D-58 row.
//
// What IS covered here is the part that made 21a4bef's gate inert: the
// function returned a plain nil whenever the beacon did not hold the epoch, so
// Sequencer/vdf_sealer.go's `if perr != nil` could only ever fire on a KV
// write error — never on "sealed but nothing landed", which is the case D-58
// is about. If anyone restores that `return nil`, these fail.

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
)

// withBeaconHolding installs a process-global beacon holding exactly the given
// epochs, and restores the previous state afterwards. Mirrors the setup
// entropy_committee_test.go already uses.
func withBeaconHolding(t *testing.T, epochs ...uint64) {
	t.Helper()
	src, err := committee.NewBeaconSource(committee.MinRetainedEpochs)
	if err != nil {
		t.Fatalf("NewBeaconSource: %v", err)
	}
	for _, e := range epochs {
		entropy := make([]byte, 32)
		entropy[0] = byte(e)
		if err := src.Publish(e, entropy); err != nil {
			t.Fatalf("Publish(%d): %v", e, err)
		}
	}
	SetBeaconSource(src)
	t.Cleanup(func() { beaconSource = nil })
}

// The core regression. An epoch the beacon does not hold must come back as
// ErrNoEntropyToPersist, NOT as nil — nil is what let the sealer believe a
// seal had been persisted when nothing was written.
func TestPersistEpochEntropy_AbsentEpochIsNotReportedAsSuccess(t *testing.T) {
	withBeaconHolding(t, 10)

	err := PersistEpochEntropy(11) // installed beacon, but epoch 11 was never published
	if err == nil {
		t.Fatal("PersistEpochEntropy returned nil for an epoch the beacon does not hold — " +
			"this is exactly the bug that made the sealer's persist gate inert (D-58)")
	}
	if !errors.Is(err, ErrNoEntropyToPersist) {
		t.Fatalf("want ErrNoEntropyToPersist, got %v", err)
	}
}

// The sentinel must survive wrapping, because the sealer distinguishes it from
// a KV write error with errors.Is. A bare fmt.Errorf without %w would break
// that silently and re-open the same hole from the other side.
func TestPersistEpochEntropy_SentinelIsUnwrappable(t *testing.T) {
	withBeaconHolding(t) // beacon installed, holds nothing

	err := PersistEpochEntropy(1)
	if !errors.Is(err, ErrNoEntropyToPersist) {
		t.Fatalf("sentinel must be reachable through errors.Is; got %v", err)
	}
	// The underlying avc cause must still be visible — the operator needs to
	// see which epoch and why, not just our wrapper.
	if !errors.Is(err, committee.ErrEntropyUnavailable) {
		t.Fatalf("underlying committee.ErrEntropyUnavailable must be preserved in the chain; got %v", err)
	}
}

// Stage 1 (no beacon installed at all) is genuinely "nothing to do" and must
// stay nil. If this ever returns the sentinel, every pre-Stage-2 node's seal
// path starts reporting failures for a condition that is normal.
func TestPersistEpochEntropy_NoBeaconInstalledIsNil(t *testing.T) {
	prev := beaconSource
	beaconSource = nil
	t.Cleanup(func() { beaconSource = prev })

	if err := PersistEpochEntropy(1); err != nil {
		t.Fatalf("no beacon installed must be a silent no-op, got %v", err)
	}
}
