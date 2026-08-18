package settings

import "testing"

// baseConfig builds a NodeConfig with the default security policies and the
// production default binds (API/Facade/DID public 0.0.0.0; CLI/geth loopback).
func baseConfig() NodeConfig {
	return NodeConfig{
		Binds: BindSettings{
			API:      "0.0.0.0",
			Facade:   "0.0.0.0",
			BlockGen: "127.0.0.1",
			DID:      "0.0.0.0",
			CLI:      "127.0.0.1",
			Geth:     "127.0.0.1",
		},
		Security: DefaultSecurityConfig(),
	}
}

func flaggedSet(cfg NodeConfig) map[string]string {
	m := map[string]string{}
	for _, v := range cfg.InsecurePublicServices() {
		m[v.Service] = v.Reason
	}
	return m
}

// eth_rpc is public-by-design: NOT flagged for missing auth (it carries the
// default global rate limit). did_service (auth_type=none, public) IS flagged.
// explorer_api (token) and loopback services are not flagged.
func TestInsecurePublicServices_PublicRPCExempt(t *testing.T) {
	got := flaggedSet(baseConfig())
	if _, ok := got[ServiceEthRPC]; ok {
		t.Fatalf("eth_rpc is public-by-design + rate-limited — must NOT be flagged, got %q", got[ServiceEthRPC])
	}
	if got[ServiceDID] != "auth_type=none" {
		t.Fatalf("did_service (public, none) must be flagged for auth, got %q", got[ServiceDID])
	}
	if _, ok := got[ServiceExplorerAPI]; ok {
		t.Fatal("explorer_api is token-authed — must NOT be flagged")
	}
	if _, ok := got[ServiceCLI]; ok {
		t.Fatal("loopback services must NOT be flagged")
	}
}

// Public RPC MUST be rate-limited: flagged only when neither a per-service nor
// the global rate limit is set; not flagged once either is present.
func TestPublicRPC_RequiresRateLimit(t *testing.T) {
	cfg := baseConfig()
	cfg.Security.GlobalRateLimit = 0 // remove the global cap

	// eth_rpc default per-service RateLimit is 0 → now unprotected → flagged.
	if got := flaggedSet(cfg); got[ServiceEthRPC] != "public RPC with no rate limit" {
		t.Fatalf("public RPC with no rate limit must be flagged, got %q", got[ServiceEthRPC])
	}

	// A per-service rate limit satisfies it (global still 0).
	p := cfg.Security.Services[ServiceEthRPC]
	p.RateLimit = 25
	cfg.Security.Services[ServiceEthRPC] = p
	if _, ok := flaggedSet(cfg)[ServiceEthRPC]; ok {
		t.Fatal("per-service rate limit must satisfy the public-RPC policy")
	}

	// Or the global cap alone satisfies it.
	p.RateLimit = 0
	cfg.Security.Services[ServiceEthRPC] = p
	cfg.Security.GlobalRateLimit = 50
	if _, ok := flaggedSet(cfg)[ServiceEthRPC]; ok {
		t.Fatal("global rate limit must satisfy the public-RPC policy")
	}

	// Even with no auth and no rate limit, eth_rpc is NEVER flagged for auth.
	cfg.Security.GlobalRateLimit = 0
	if flaggedSet(cfg)[ServiceEthRPC] == "auth_type=none" {
		t.Fatal("public RPC must never be flagged for missing auth")
	}
}

// The auth check is bind-driven for a normal (non-RPC) service: loopback ok,
// public flagged.
func TestInsecurePublicServices_BindDriven(t *testing.T) {
	cfg := baseConfig()
	cfg.Binds.DID = "127.0.0.1" // did now loopback
	if _, ok := flaggedSet(cfg)[ServiceDID]; ok {
		t.Fatal("did on loopback must not be flagged")
	}
	cfg.Binds.DID = "0.0.0.0"
	if flaggedSet(cfg)[ServiceDID] != "auth_type=none" {
		t.Fatal("did on 0.0.0.0 with none must be flagged")
	}
}

// Empty bind is treated as non-loopback (fail-closed).
func TestIsLoopbackBind(t *testing.T) {
	for _, lo := range []string{"127.0.0.1", "localhost", "::1", "[::1]", "127.0.0.5"} {
		if !isLoopbackBind(lo) {
			t.Fatalf("%q should be loopback", lo)
		}
	}
	for _, pub := range []string{"0.0.0.0", "", "10.0.0.1", "192.168.1.5", "::"} {
		if isLoopbackBind(pub) {
			t.Fatalf("%q should NOT be loopback (fail-closed)", pub)
		}
	}
}

// Security disabled → no posture check.
func TestPosture_DisabledSecurityNoOp(t *testing.T) {
	cfg := baseConfig()
	cfg.Security.Enabled = false
	if len(cfg.InsecurePublicServices()) != 0 {
		t.Fatal("disabled security must flag nothing")
	}
	if err := cfg.ValidateSecurityPosture(); err != nil {
		t.Fatalf("disabled security must not error: %v", err)
	}
}

// Non-strict never errors; strict errors while did is open and passes once authed
// (eth_rpc stays open+rate-limited and never blocks strict boot).
func TestValidateSecurityPosture_StrictGate(t *testing.T) {
	cfg := baseConfig()

	if err := cfg.ValidateSecurityPosture(); err != nil {
		t.Fatalf("non-strict must not error: %v", err)
	}

	cfg.Security.StrictPosture = true
	if err := cfg.ValidateSecurityPosture(); err == nil {
		t.Fatal("strict must error while did is open on a public bind")
	}

	p := cfg.Security.Services[ServiceDID]
	p.AuthType = AuthTypeToken
	p.TokenEnv = "DID_TOKEN"
	cfg.Security.Services[ServiceDID] = p
	if err := cfg.ValidateSecurityPosture(); err != nil {
		t.Fatalf("strict must pass once did is authed (public rate-limited RPC is fine): %v", err)
	}
}
