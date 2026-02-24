package gatekeeper

import (
	"context"
	"testing"

	"gossipnode/config/settings"

	"github.com/JupiterMetaLabs/ion"
)

func init() {
	// Override the package-level logger to avoid settings.Get() panic in tests.
	// The real logger depends on global config + OTEL which aren't initialized
	// in unit test context.
	logger = func(_ string) *ion.Ion { return nil }
}

// testConfig returns a minimal SecurityConfig for tests.
func testConfig() *settings.SecurityConfig {
	cfg := settings.DefaultSecurityConfig()
	cfg.ResolveTokens()
	return &cfg
}

// --- RateLimiter tests ---

func TestAllow_Layer2_PermitsUnderLimit(t *testing.T) {
	cfg := testConfig()
	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	// EthRPC has rate_limit=20. First request should always pass.
	if !rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.1") {
		t.Error("expected first request to be allowed")
	}
}

func TestAllow_Layer2_DeniesOverLimit(t *testing.T) {
	cfg := testConfig()
	// Set a very low limit so we can exhaust it
	policy := cfg.Services[settings.ServiceEthRPC]
	policy.RateLimit = 1
	policy.Burst = 1
	cfg.Services[settings.ServiceEthRPC] = policy

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	// First request uses the burst token
	if !rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.1") {
		t.Error("expected first request to be allowed (burst)")
	}
	// Second request should be denied (burst=1 exhausted, 1 RPS not yet refilled)
	if rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.1") {
		t.Error("expected second request to be denied")
	}
}

func TestAllow_Layer1_GlobalCapDenies(t *testing.T) {
	cfg := testConfig()
	cfg.GlobalRateLimit = 1
	cfg.GlobalBurst = 1

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	// First request passes
	if !rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.1") {
		t.Error("expected first request to be allowed")
	}
	// Second request to a DIFFERENT service should still be denied by global cap
	if rl.Allow(context.Background(), settings.ServiceExplorerAPI, "10.0.0.1") {
		t.Error("expected global cap to deny second request even for different service")
	}
}

func TestAllow_Layer1_DisabledWhenZero(t *testing.T) {
	cfg := testConfig()
	cfg.GlobalRateLimit = 0 // Disabled

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	// Should pass — only L2 applies
	if !rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.1") {
		t.Error("expected request to be allowed when global limit is disabled")
	}
}

func TestAllow_BFTExemptFromRateLimiting(t *testing.T) {
	cfg := testConfig()
	cfg.GlobalRateLimit = 0 // Disable L1 to isolate L2

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	// BFT should always pass (rate_limit=0 in default config)
	for i := 0; i < 100; i++ {
		if !rl.Allow(context.Background(), settings.ServiceBFTBuddy, "10.0.0.1") {
			t.Fatalf("BFT request %d was unexpectedly denied", i)
		}
	}
}

func TestAllow_UnknownServiceAllowed(t *testing.T) {
	cfg := testConfig()
	cfg.GlobalRateLimit = 0

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	if !rl.Allow(context.Background(), "unknown_service", "10.0.0.1") {
		t.Error("expected unknown service to be allowed")
	}
}

func TestAllow_DifferentIPsAreIndependent(t *testing.T) {
	cfg := testConfig()
	policy := cfg.Services[settings.ServiceEthRPC]
	policy.RateLimit = 1
	policy.Burst = 1
	cfg.Services[settings.ServiceEthRPC] = policy
	cfg.GlobalRateLimit = 0

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	// IP1 exhausts its limit
	rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.1")
	if rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.1") {
		t.Error("expected IP1 second request to be denied")
	}

	// IP2 should still be allowed
	if !rl.Allow(context.Background(), settings.ServiceEthRPC, "10.0.0.2") {
		t.Error("expected IP2 to be independently allowed")
	}
}

func TestRetryAfterSeconds_ReturnsExpectedValues(t *testing.T) {
	cfg := testConfig()
	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	// EthRPC = 20 RPS → ceil(1/20) = 1
	if got := rl.RetryAfterSeconds(settings.ServiceEthRPC, "10.0.0.1"); got != 1 {
		t.Errorf("RetryAfterSeconds(eth_rpc) = %d, want 1", got)
	}

	// CLI = 10 RPS → ceil(1/10) = 1
	if got := rl.RetryAfterSeconds(settings.ServiceCLI, "10.0.0.1"); got != 1 {
		t.Errorf("RetryAfterSeconds(cli_admin) = %d, want 1", got)
	}

	// Unknown service → default 1
	if got := rl.RetryAfterSeconds("unknown", "10.0.0.1"); got != 1 {
		t.Errorf("RetryAfterSeconds(unknown) = %d, want 1", got)
	}
}

func TestNewRateLimiter_DefaultCacheSize(t *testing.T) {
	cfg := testConfig()
	rl, err := NewRateLimiter(cfg, 0) // 0 should default to 1000
	if err != nil {
		t.Fatalf("NewRateLimiter with 0 cache size: %v", err)
	}
	if rl == nil {
		t.Fatal("expected non-nil RateLimiter")
	}
}
