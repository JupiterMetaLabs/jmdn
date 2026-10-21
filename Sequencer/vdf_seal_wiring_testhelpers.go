//go:build sequencertest

package Sequencer

// D-49: these three seams used to live in vdf_seal_wiring.go as plain
// exported functions — no build tag, compiling into the release binary.
// SeedSealResultForTest could inject an arbitrary vdf.Proof straight into
// the production sealer map; the same is true in spirit of the other two,
// which read and mutate that same package-level state. Gating them behind
// `sequencertest` (same pattern as internal/applygate/harness.go) removes
// them from a normal build while keeping them usable by tests in another
// package (Block/consensus_fields_test.go), which a _test.go file alone
// cannot do — a _test.go file's symbols are visible only within its own
// package's test binary.
//
// Any test file that calls one of these three must carry the same
// `//go:build sequencertest` tag, and the test command must pass
// `-tags sequencertest`, e.g.:
//   go test -tags sequencertest ./Sequencer/... ./Block/...

// SealerCancelledForTest reports whether forEpoch's sealer was cancelled.
// Test-only.
func SealerCancelledForTest(forEpoch uint64) bool {
	vdfSealersMu.Lock()
	s, ok := vdfSealers[forEpoch]
	vdfSealersMu.Unlock()
	return ok && s.Cancelled()
}

// SeedSealResultForTest injects a completed (or failed) SealResult for
// forEpoch directly into vdfSealers, bypassing Start and the background
// goroutine entirely. Test-only — lets a test in another package (e.g.
// Block/consensus_fields_test.go) exercise SealerResultFor's ok=true and
// ok=false paths without running a real ~20-minute VDF evaluation. The
// ok=false ("not ready") path needs no seam at all: simply don't seed a
// result for that epoch, and SealerResultFor already returns
// (SealResult{}, false) for any epoch with no registered sealer. Overwrites
// any sealer already registered for forEpoch.
func SeedSealResultForTest(forEpoch uint64, result SealResult) {
	s := &VDFSealer{resultCh: make(chan SealResult, 1)}
	s.resultCh <- result
	vdfSealersMu.Lock()
	vdfSealers[forEpoch] = s
	vdfSealersMu.Unlock()
}

// ClearSealerForTest removes forEpoch's registered sealer. Test-only.
//
// Needed because vdfSealers is package-level state and SeedSealResultForTest
// writes into it. Until Result was made idempotent (2026-09-03) the drain
// itself acted as accidental cleanup: a seeded result was consumed by the
// first read, so it could not leak into a later test that expected
// "not ready". With the latch that accident is gone, and tests must clean up
// explicitly — which they should always have done.
func ClearSealerForTest(forEpoch uint64) {
	vdfSealersMu.Lock()
	delete(vdfSealers, forEpoch)
	vdfSealersMu.Unlock()
}
