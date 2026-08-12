package settings

// These tests pin the OPERATOR-FACING contract for the consensus-rejection
// callback: the exact env var names, and the fact that reporting is off until
// both the URL and the key are set.
//
// The env names are derived by Viper (prefix JMDN_ + key with "." -> "_"), not
// written down anywhere in code, so a rename of the config key silently changes
// the variable operators must set. That is what these assertions catch.

import (
	"testing"
	"time"
)

func TestOrchestratorEnvVarNames(t *testing.T) {
	t.Setenv("JMDN_ORCHESTRATOR_URL", "http://127.0.0.1:8092/api/block/consensus-rejected")
	t.Setenv("JMDN_ORCHESTRATOR_API_KEY", "envsecret")
	t.Setenv("JMDN_ORCHESTRATOR_HTTP_TIMEOUT", "7s")
	t.Setenv("JMDN_ORCHESTRATOR_MAX_ATTEMPTS", "5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	oc := cfg.Orchestrator

	if oc.URL != "http://127.0.0.1:8092/api/block/consensus-rejected" {
		t.Fatalf("JMDN_ORCHESTRATOR_URL did not bind: %q", oc.URL)
	}
	if oc.APIKey != "envsecret" {
		t.Fatalf("JMDN_ORCHESTRATOR_API_KEY did not bind: %q", oc.APIKey)
	}
	if oc.HTTPTimeout != 7*time.Second {
		t.Fatalf("JMDN_ORCHESTRATOR_HTTP_TIMEOUT did not bind/parse: %v", oc.HTTPTimeout)
	}
	if oc.MaxAttempts != 5 {
		t.Fatalf("JMDN_ORCHESTRATOR_MAX_ATTEMPTS did not bind/parse: %d", oc.MaxAttempts)
	}
}

// Unset => reporting disabled, with usable timeout/retry defaults so an
// operator only has to supply the URL and the key.
func TestOrchestratorDisabledByDefault(t *testing.T) {
	d := DefaultOrchestratorConfig()
	if d.URL != "" || d.APIKey != "" {
		t.Fatalf("reporting must be off by default, got %+v", d)
	}
	if d.HTTPTimeout != 10*time.Second {
		t.Fatalf("default timeout = %v, want 10s", d.HTTPTimeout)
	}
	if d.MaxAttempts != 3 {
		t.Fatalf("default max_attempts = %d, want 3", d.MaxAttempts)
	}
}
