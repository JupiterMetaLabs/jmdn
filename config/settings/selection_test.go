package settings

import (
	"os"
	"testing"
)

// TestSelectionEnvBinding verifies the VRF selection secrets are populated from
// the explicit env var names (JMDN_NODE_SELECTION_MNEMONIC / JMDN_NETWORK_SALT)
// bound in Load(), and default to empty (fail-closed) when unset.
func TestSelectionEnvBinding(t *testing.T) {
	t.Setenv("JMDN_NODE_SELECTION_MNEMONIC", "test mnemonic words here")
	t.Setenv("JMDN_NETWORK_SALT", "network-salt-xyz")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Selection.Mnemonic != "test mnemonic words here" {
		t.Fatalf("mnemonic not bound from env: got %q", cfg.Selection.Mnemonic)
	}
	if cfg.Selection.Salt != "network-salt-xyz" {
		t.Fatalf("salt not bound from env: got %q", cfg.Selection.Salt)
	}
}

func TestSelectionDefaults(t *testing.T) {
	os.Unsetenv("JMDN_NODE_SELECTION_MNEMONIC")
	os.Unsetenv("JMDN_NETWORK_SALT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Mnemonic is secret and fail-closed → no default.
	if cfg.Selection.Mnemonic != "" {
		t.Fatalf("expected empty mnemonic default (fail-closed), got %q", cfg.Selection.Mnemonic)
	}
	// Salt is not secret → carries a built-in network-wide default.
	if cfg.Selection.Salt != DefaultSelectionSalt {
		t.Fatalf("expected default salt %q, got %q", DefaultSelectionSalt, cfg.Selection.Salt)
	}
}
