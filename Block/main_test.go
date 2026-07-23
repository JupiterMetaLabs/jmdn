package Block

import (
	"fmt"
	"os"
	"testing"

	"gossipnode/config/settings"
)

// TestMain loads settings with defaults once for the package. The adapter's
// tracing/logging stack (logger() → otelsetup → settings.Get()) panics in
// any pre-Load context, so tests exercising mreRouter methods need this.
// Config resolution is default-only here (no jmdn.yaml in the test cwd).
func TestMain(m *testing.M) {
	if _, err := settings.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "settings.Load for tests: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
