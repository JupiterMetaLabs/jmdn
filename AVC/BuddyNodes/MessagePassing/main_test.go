package MessagePassing

import (
	"fmt"
	"os"
	"testing"

	"gossipnode/config/settings"
)

// TestMain initialises the global settings before any test in this package runs.
//
// WHY: settings.Get() panics with "settings.Get() called before settings.Load()"
// when globalCfg is nil (config/settings/loader.go). Production calls Load()
// during startup in main(); tests never did, so every test in this package
// panicked before asserting anything — the package reported FAIL for reasons
// that had nothing to do with the code under test, and was written off as
// "needs live infrastructure" when it does not.
//
// settings.Load() needs no fixture: it seeds viper from DefaultConfig and treats
// a missing jmdn.yaml as a non-error, so this works in a bare checkout and in
// CI. Environment variables still override, so a caller that wants different
// values can set JMDN_* before running the tests.
func TestMain(m *testing.M) {
	if _, err := settings.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: settings.Load() failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
