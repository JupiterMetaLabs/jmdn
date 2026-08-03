package adapters

import (
	"os"
	"testing"

	"gossipnode/config/settings"
)

// TestMain loads jmdn settings before any test runs. jmdn's Security functions
// call logger() internally, which lazily initializes logging from
// settings.Get() and panics if settings.Load() was never called. The real node
// calls Load() at startup (main.go); tests must do the same. This mirrors
// jmdn's own messaging/blockPropagation_test.go TestMain.
func TestMain(m *testing.M) {
	if _, err := settings.Load(); err != nil {
		panic("adapters test: load settings: " + err.Error())
	}
	code := m.Run()
	// settings.Load() writes key material into ./config as a side effect; clean
	// it up so the test leaves no artifacts (same as jmdn's own TestMain).
	_ = os.Remove("config/bls.json")
	_ = os.Remove("config/peer.json")
	_ = os.Remove("config")
	os.Exit(code)
}
