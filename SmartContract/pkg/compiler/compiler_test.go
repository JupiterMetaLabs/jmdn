package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileSolidity_RejectsOversizedSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.sol")
	// Just over the cap — do not spawn solc.
	payload := strings.Repeat("a", MaxSoliditySourceBytes+1)
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := CompileSolidity(path)
	if err == nil {
		t.Fatal("expected oversized source to be rejected")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}
