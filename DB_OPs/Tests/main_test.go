package DB_OPs_Tests

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

// TestMain skips this entire suite under `go test -short`. These are integration
// tests that require a live ImmuDB (localhost:3322) and are not part of the unit
// gate. Run them explicitly against a provisioned ImmuDB:
//
//	go test ./DB_OPs/Tests/...
func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		fmt.Println("skipping DB_OPs integration suite in -short mode (requires ImmuDB)")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
