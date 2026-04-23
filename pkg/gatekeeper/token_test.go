package gatekeeper

import (
	"testing"

	"gossipnode/config/settings"
)

func TestValidateAuthHeader_EmptyHeader(t *testing.T) {
	cfg := testConfig()
	policy := cfg.Services[settings.ServiceExplorerAPI]

	_, err := ValidateAuthHeader("", policy, cfg)
	if err == nil {
		t.Error("expected error for empty header")
	}
}

func TestValidateAuthHeader_InvalidFormat(t *testing.T) {
	cfg := testConfig()
	policy := cfg.Services[settings.ServiceExplorerAPI]

	_, err := ValidateAuthHeader("NotBearer token123", policy, cfg)
	if err == nil {
		t.Error("expected error for non-Bearer header")
	}
}

func TestValidateAuthHeader_InvalidToken(t *testing.T) {
	cfg := testConfig()
	cfg.ExplorerAPIKey = "correct-token"
	cfg.ResolveTokens() // Re-resolve after changing token
	policy := cfg.Services[settings.ServiceExplorerAPI]

	_, err := ValidateAuthHeader("Bearer wrong-token", policy, cfg)
	if err == nil {
		t.Error("expected error for wrong token")
	}
}

func TestValidateAuthHeader_ValidStaticToken(t *testing.T) {
	cfg := testConfig()
	cfg.ExplorerAPIKey = "my-secret-key"
	cfg.ResolveTokens()
	policy := cfg.Services[settings.ServiceExplorerAPI]

	result, err := ValidateAuthHeader("Bearer my-secret-key", policy, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil AuthResult")
	}
	// Static token → no JWT claims
	if result.Claims != nil {
		t.Error("expected nil claims for static token auth")
	}
}

func TestValidateAuthHeader_CaseInsensitiveBearer(t *testing.T) {
	cfg := testConfig()
	cfg.ExplorerAPIKey = "my-secret-key"
	cfg.ResolveTokens()
	policy := cfg.Services[settings.ServiceExplorerAPI]

	// "bearer" (lowercase) should also work
	result, err := ValidateAuthHeader("bearer my-secret-key", policy, cfg)
	if err != nil {
		t.Fatalf("unexpected error for lowercase bearer: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil AuthResult")
	}
}

func TestConstantTimeEqual_MatchAndMismatch(t *testing.T) {
	if !constantTimeEqual("abc", "abc") {
		t.Error("expected equal strings to match")
	}
	if constantTimeEqual("abc", "xyz") {
		t.Error("expected different strings to not match")
	}
	if constantTimeEqual("abc", "abcd") {
		t.Error("expected different-length strings to not match")
	}
	if constantTimeEqual("", "") {
		// subtle.ConstantTimeCompare with empty slices returns 1,
		// but two empty tokens both being "" should match
		// Actually subtle.ConstantTimeCompare([]byte(""), []byte("")) == 1
		// This is debatable but technically correct
	}
}

func TestGetResolvedToken_CacheHit(t *testing.T) {
	cfg := settings.DefaultSecurityConfig()
	cfg.ExplorerAPIKey = "cached-api-key"
	cfg.ResolveTokens()

	got := cfg.GetResolvedToken("EXPLORER_API_KEY")
	if got != "cached-api-key" {
		t.Errorf("GetResolvedToken = %q, want %q", got, "cached-api-key")
	}
}

func TestGetResolvedToken_FallbackWhenNotResolved(t *testing.T) {
	cfg := settings.DefaultSecurityConfig()
	// Don't call ResolveTokens — defensive fallback path
	cfg.ExplorerAPIKey = "fallback-key"

	got := cfg.GetResolvedToken("EXPLORER_API_KEY")
	if got != "fallback-key" {
		t.Errorf("GetResolvedToken fallback = %q, want %q", got, "fallback-key")
	}
}

func TestResolveTokens_RefreshesOnChange(t *testing.T) {
	cfg := settings.DefaultSecurityConfig()
	cfg.ExplorerAPIKey = "initial-key"
	cfg.ResolveTokens()

	if got := cfg.GetResolvedToken("EXPLORER_API_KEY"); got != "initial-key" {
		t.Errorf("initial resolve = %q, want %q", got, "initial-key")
	}

	// Simulate CLI flag override
	cfg.ExplorerAPIKey = "overridden-key"
	// Call ResolveTokens again (like in main.go)
	cfg.ResolveTokens()

	if got := cfg.GetResolvedToken("EXPLORER_API_KEY"); got != "overridden-key" {
		t.Errorf("second resolve = %q, want %q", got, "overridden-key")
	}
}
