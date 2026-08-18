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

// With the shipped compiled defaults, eth_rpc (Facade, 0.0.0.0) and did_service
// (DID, 0.0.0.0) are auth_type=none on a public bind → flagged. explorer_api is
// token (not flagged); loopback services are not flagged.
func TestInsecurePublicServices_FlagsOpenPublicOnly(t *testing.T) {
	cfg := baseConfig()
	got := map[string]bool{}
	for _, v := range cfg.InsecurePublicServices() {
		got[v.Service] = true
	}
	if !got[ServiceEthRPC] {
		t.Fatal("eth_rpc (public, none) must be flagged")
	}
	if !got[ServiceDID] {
		t.Fatal("did_service (public, none) must be flagged")
	}
	if got[ServiceExplorerAPI] {
		t.Fatal("explorer_api is token-authed — must NOT be flagged")
	}
	if got[ServiceCLI] || got[ServiceEthGRPC] {
		t.Fatal("loopback services must NOT be flagged")
	}
}

// A loopback bind with auth none is fine; flipping the same service to a public
// bind flags it.
func TestInsecurePublicServices_BindDriven(t *testing.T) {
	cfg := baseConfig()
	cfg.Binds.Facade = "127.0.0.1" // eth_rpc now loopback
	for _, v := range cfg.InsecurePublicServices() {
		if v.Service == ServiceEthRPC {
			t.Fatal("eth_rpc on loopback must not be flagged")
		}
	}
	cfg.Binds.Facade = "0.0.0.0" // back to public
	found := false
	for _, v := range cfg.InsecurePublicServices() {
		if v.Service == ServiceEthRPC {
			found = true
		}
	}
	if !found {
		t.Fatal("eth_rpc on 0.0.0.0 must be flagged")
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

// Security disabled → no posture check (nothing flagged, no error).
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

// Default (non-strict) never errors even with violations; strict errors when a
// public service is open and returns nil once all public services are authed.
func TestValidateSecurityPosture_StrictGate(t *testing.T) {
	cfg := baseConfig()

	if err := cfg.ValidateSecurityPosture(); err != nil {
		t.Fatalf("non-strict must not error despite open services: %v", err)
	}

	cfg.Security.StrictPosture = true
	if err := cfg.ValidateSecurityPosture(); err == nil {
		t.Fatal("strict must error while eth_rpc/did are open on public binds")
	}

	// Authenticate every public service → strict passes.
	for _, svc := range []string{ServiceEthRPC, ServiceDID} {
		p := cfg.Security.Services[svc]
		p.AuthType = AuthTypeToken
		p.TokenEnv = "SVC_TOKEN"
		cfg.Security.Services[svc] = p
	}
	if err := cfg.ValidateSecurityPosture(); err != nil {
		t.Fatalf("strict must pass once public services are authed: %v", err)
	}
}
