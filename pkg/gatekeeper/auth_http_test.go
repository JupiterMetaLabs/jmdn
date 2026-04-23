package gatekeeper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gossipnode/config/settings"
)

// nextOK is a trivial http.Handler that always returns 200 — used to verify
// that allowed requests reach the downstream handler.
var nextOK = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// netHTTPTestConfig returns a SecurityConfig with a tight EthRPC limit for testing.
func netHTTPTestConfig() *settings.SecurityConfig {
	cfg := settings.DefaultSecurityConfig()
	cfg.ResolveTokens()
	return &cfg
}

func TestNetHTTPMiddleware_AllowsUnderLimit(t *testing.T) {
	cfg := netHTTPTestConfig()
	cfg.GlobalRateLimit = 0 // Disable L1 to isolate L2

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	mw := NewNetHTTPMiddleware(cfg, rl, nil)
	handler := mw.Wrap(settings.ServiceEthRPC, nextOK)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:5000"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestNetHTTPMiddleware_DeniesOverLimit(t *testing.T) {
	cfg := netHTTPTestConfig()
	cfg.GlobalRateLimit = 0

	// Set a very tight limit on EthRPC
	policy := cfg.Services[settings.ServiceEthRPC]
	policy.RateLimit = 1
	policy.Burst = 1
	cfg.Services[settings.ServiceEthRPC] = policy

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	mw := NewNetHTTPMiddleware(cfg, rl, nil)
	handler := mw.Wrap(settings.ServiceEthRPC, nextOK)

	makeReq := func() *http.Response {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.2:5000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Result()
	}

	// First request uses the burst token
	if r := makeReq(); r.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on first request, got %d", r.StatusCode)
	}
	// Second request should be denied
	if r := makeReq(); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 on second request, got %d", r.StatusCode)
	}
}

func TestNetHTTPMiddleware_Returns429WithRetryAfterHeader(t *testing.T) {
	cfg := netHTTPTestConfig()
	cfg.GlobalRateLimit = 0

	policy := cfg.Services[settings.ServiceEthRPC]
	policy.RateLimit = 1
	policy.Burst = 1
	cfg.Services[settings.ServiceEthRPC] = policy

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	mw := NewNetHTTPMiddleware(cfg, rl, nil)
	handler := mw.Wrap(settings.ServiceEthRPC, nextOK)

	// Exhaust the limiter
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "10.0.0.3:5000"
	httptest.NewRecorder()
	handler.ServeHTTP(httptest.NewRecorder(), req1)

	// Second request should be 429 with Retry-After header
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.3:5000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req2)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header to be set on 429 response")
	}
}

func TestNetHTTPMiddleware_PassesWhenDisabled(t *testing.T) {
	cfg := netHTTPTestConfig()
	cfg.Enabled = false // Global kill-switch

	rl, err := NewRateLimiter(cfg, 100)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}

	mw := NewNetHTTPMiddleware(cfg, rl, nil)
	handler := mw.Wrap(settings.ServiceEthRPC, nextOK)

	// All requests should pass regardless of limits
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.4:5000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: expected 200 when disabled, got %d", i, rr.Code)
		}
	}
}
