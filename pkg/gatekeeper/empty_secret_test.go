package gatekeeper

import (
	"testing"

	"gossipnode/config/settings"
)

// Regression guard for the API-03 fail-closed invariant: when NO auth secret is
// configured (empty JWTSecret AND empty resolved static token), a token-auth
// service must REJECT every presented bearer token — never accept one. Without
// the non-empty guards in ValidateAuthHeader, an empty JWT secret would verify a
// forged HS256 token and an empty static token would match a blank credential.
func TestValidateAuthHeader_EmptySecretsFailClosed(t *testing.T) {
	policy := settings.Policy{AuthType: settings.AuthTypeToken, TokenEnv: "EXPLORER_API_KEY"}
	cfg := &settings.SecurityConfig{} // JWTSecret == "", ExplorerAPIKey == ""
	cfg.ResolveTokens()

	// A syntactically valid bearer header whose token is anything at all.
	for _, tok := range []string{"anything", "", "a.forged.jwt"} {
		if _, err := ValidateAuthHeader("Bearer "+tok, policy, cfg); err == nil {
			t.Fatalf("empty secrets must reject bearer %q, but auth succeeded", tok)
		}
	}

	// A correctly configured static token still works (sanity: the guard rejects
	// only the EMPTY-secret case, not all auth).
	cfg2 := &settings.SecurityConfig{ExplorerAPIKey: "s3cret"}
	cfg2.ResolveTokens()
	if _, err := ValidateAuthHeader("Bearer s3cret", policy, cfg2); err != nil {
		t.Fatalf("a configured static token must authenticate, got %v", err)
	}
	if _, err := ValidateAuthHeader("Bearer wrong", policy, cfg2); err == nil {
		t.Fatal("a wrong static token must be rejected")
	}
}
