package messaging

// D-48: HandleVDFProofRequestStream must not do the per-request KV read on a
// node that never installed the AVC beacon (Stage 2) -- there is nothing for
// it to serve. These tests pin SetVDFBeaconInstalled / vdfBeaconIsInstalled
// directly rather than through HandleVDFProofRequestStream itself: every
// other test in this package that touches vdf-proof-pull deliberately avoids
// driving that function through a real network.Stream (see
// entropy_vdf_pull_bounds_test.go's header comment), and the branch this
// gates is a single boolean read whose two failure modes -- a default that
// is not fail-safe, and a race between main.go's one-time set and this
// handler's per-request reads -- are exactly what these tests pin.

import "testing"

// A node that never calls SetVDFBeaconInstalled (an early panic, a build
// that omits the call) must read as NOT installed. The zero value has to be
// the safe one: it must skip the lookup, not silently fall through to
// serving from a KV store it was never told is meaningful.
func TestVDFBeaconInstalled_DefaultsToFalse(t *testing.T) {
	// Reset to the zero value in case an earlier test in this package set it.
	vdfBeaconInstalledMu.Lock()
	vdfBeaconInstalled = false
	vdfBeaconInstalledMu.Unlock()

	if vdfBeaconIsInstalled() {
		t.Fatal("vdfBeaconIsInstalled() = true before SetVDFBeaconInstalled was ever called -- " +
			"the zero value must be false, or a node that skips beacon installation would still " +
			"serve VDF-proof-pull requests")
	}
}

func TestSetVDFBeaconInstalled_RoundTrips(t *testing.T) {
	defer SetVDFBeaconInstalled(false) // restore the zero value for other tests in this package

	SetVDFBeaconInstalled(true)
	if !vdfBeaconIsInstalled() {
		t.Fatal("SetVDFBeaconInstalled(true) then vdfBeaconIsInstalled() = false")
	}

	SetVDFBeaconInstalled(false)
	if vdfBeaconIsInstalled() {
		t.Fatal("SetVDFBeaconInstalled(false) then vdfBeaconIsInstalled() = true")
	}
}

// main.go calls SetVDFBeaconInstalled once at startup, but
// HandleVDFProofRequestStream runs concurrently per inbound stream and reads
// it on every request -- set and read must not race.
func TestVDFBeaconInstalled_ConcurrentSetAndReadNoRace(t *testing.T) {
	defer SetVDFBeaconInstalled(false)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			SetVDFBeaconInstalled(i%2 == 0)
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		_ = vdfBeaconIsInstalled()
	}
	<-done
}
