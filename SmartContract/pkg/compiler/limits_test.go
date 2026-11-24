package compiler

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-solc")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunSolc_BusyWhenSlotsFull(t *testing.T) {
	prev := solcCommand
	solcCommand = writeScript(t, "echo '{}'")
	defer func() { solcCommand = prev }()

	for i := 0; i < MaxConcurrentSolc; i++ {
		solcSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < MaxConcurrentSolc; i++ {
			<-solcSlots
		}
	}()
	if _, err := runSolc("x"); !errors.Is(err, ErrCompilerBusy) {
		t.Fatalf("err = %v, want ErrCompilerBusy", err)
	}
}

func TestRunSolc_ReleasesSlot(t *testing.T) {
	prev := solcCommand
	solcCommand = writeScript(t, "echo '{}'")
	defer func() { solcCommand = prev }()

	for i := 0; i < MaxConcurrentSolc+3; i++ {
		if _, err := runSolc("x"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(solcSlots) != 0 {
		t.Fatalf("slots leaked: %d", len(solcSlots))
	}
}

func TestCompileSolidity_RejectsOversizedSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.sol")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", MaxSourceBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir()) // CompileSolidity creates ./SmartContract/artifacts
	if _, err := CompileSolidity(p); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want size rejection", err)
	}
}
