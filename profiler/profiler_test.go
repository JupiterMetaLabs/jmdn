package profiler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// isLoopbackBind decides whether the "unauthenticated pprof on a reachable
// address" warning fires, so its edge cases are pinned here. The wildcard binds
// ("", 0.0.0.0, ::) are the ones that matter: treating them as loopback would
// silence the warning on exactly the configuration that needs it.
func TestIsLoopbackBind(t *testing.T) {
	loopback := []string{"127.0.0.1", "localhost", "::1", "[::1]", "127.0.0.53"}
	for _, addr := range loopback {
		if !isLoopbackBind(addr) {
			t.Errorf("isLoopbackBind(%q) = false, want true", addr)
		}
	}

	reachable := []string{"", "0.0.0.0", "::", "10.0.0.5", "192.168.1.10", "203.0.113.7", "example.internal"}
	for _, addr := range reachable {
		if isLoopbackBind(addr) {
			t.Errorf("isLoopbackBind(%q) = true, want false", addr)
		}
	}
}

// openFDCount reads /proc/self/fd instead of shelling out to lsof (absent from
// the runtime image). On procfs it must return a plausible positive count; on
// platforms without procfs it must error rather than report a wrong number.
func TestOpenFDCount(t *testing.T) {
	n, err := openFDCount()
	if err != nil {
		t.Skipf("no procfs on this platform: %v", err)
	}
	if n <= 0 {
		t.Errorf("openFDCount() = %d, want > 0 (a running process holds descriptors)", n)
	}
}

// The profiler must never serve http.DefaultServeMux: the net/http/pprof import
// registers onto it, so sharing it would expose pprof on every other listener
// in the process. Assert the mux we build is our own and routes what we expect.
func TestProfilerMuxIsPrivateAndRoutes(t *testing.T) {
	mux := newProfilerMux()
	if mux == nil {
		t.Fatal("newProfilerMux() = nil")
	}
	if mux == http.DefaultServeMux {
		t.Fatal("profiler must not serve http.DefaultServeMux")
	}

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/fds", "/debug/streams"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if h, pattern := mux.Handler(req); h == nil || pattern == "" {
			t.Errorf("no handler registered for %s (pattern=%q)", path, pattern)
		}
	}
}
