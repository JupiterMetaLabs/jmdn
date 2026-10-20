//go:build applygate

package BlockProcessing_test

import (
	"os"
	"testing"

	"gossipnode/config/settings"
)

// TestMain loads jmdn settings before the apply-gate tests run: the apply path
// reads settings.Get() (chain ID, logging) and panics if Load() never ran. Mirrors
// messaging/blockPropagation_test.go and consensus/adapters/main_test.go.
func TestMain(m *testing.M) {
	if _, err := settings.Load(); err != nil {
		panic("applygate: load settings: " + err.Error())
	}
	code := m.Run()
	// settings.Load() writes key material into ./config as a side effect.
	_ = os.Remove("config/bls.json")
	_ = os.Remove("config/peer.json")
	_ = os.Remove("config")
	os.Exit(code)
}
