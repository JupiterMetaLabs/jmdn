// MODULE: profiler
//
// Opt-in runtime diagnostics: pprof, mutex/block contention, open file
// descriptors and libp2p stream counts. Disabled unless ports.profiler is set,
// and bound to binds.profiler (127.0.0.1 by default).
//
// TWO THINGS TO KNOW BEFORE EDITING THIS FILE
//
//  1. Serve a PRIVATE mux, never http.DefaultServeMux. The `net/http/pprof`
//     import registers its handlers on DefaultServeMux as an import side
//     effect, so any server in this process that serves DefaultServeMux would
//     expose /debug/pprof/* on ITS port — regardless of ports.profiler. pprof
//     is attached explicitly below for that reason.
//
//  2. Contention profiling is a RUNTIME-WIDE setting, not per-request: once
//     enabled it samples on every mutex/block event for as long as it is on,
//     whether or not anyone is scraping. It is therefore enabled only after the
//     port guard passes, and reset on shutdown.
package profiler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
)

// hostRef holds the libp2p host used by the stream handler. Guarded because
// RegisterHost runs during node startup while the server may already be
// serving requests.
var hostRef struct {
	mu sync.RWMutex
	h  host.Host
}

// RegisterHost registers the libp2p host for stream profiling. Safe to call
// concurrently with request handling.
func RegisterHost(h host.Host) {
	hostRef.mu.Lock()
	hostRef.h = h
	hostRef.mu.Unlock()
}

func currentHost() host.Host {
	hostRef.mu.RLock()
	defer hostRef.mu.RUnlock()
	return hostRef.h
}

// Contention-profiling sampling rates. Sampled rather than exhaustive to keep
// overhead modest; these values reliably surface multi-second lock waits.
const (
	// mutexProfileFraction: report ~1 in N mutex contention events (1 = all).
	mutexProfileFraction = 5
	// blockProfileRateNanos: sample ~1 blocking event per N ns spent blocked.
	blockProfileRateNanos = 10_000 // 10µs
)

// isLoopbackBind reports whether bindAddr is a loopback address. An empty bind,
// 0.0.0.0 and :: mean "all interfaces" and are therefore not loopback.
func isLoopbackBind(bindAddr string) bool {
	switch bindAddr {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	case "", "0.0.0.0", "::":
		return false
	}
	if ip := net.ParseIP(bindAddr); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// newProfilerMux builds the profiler's private mux (see note 1 in the header).
func newProfilerMux() *http.ServeMux {
	mux := http.NewServeMux()

	// pprof, attached explicitly. pprof.Index also serves the named runtime
	// profiles (heap, goroutine, mutex, block, allocs, …).
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	mux.HandleFunc("/debug/fds", fdHandler)
	mux.HandleFunc("/debug/streams", streamHandler)
	return mux
}

// StartProfiler starts the diagnostics server, or returns nil when disabled
// (port unset or "0"). The caller owns the returned server; contention
// profiling is reset when it is shut down.
func StartProfiler(bindAddr string, port string) *http.Server {
	if port == "" || port == "0" {
		return nil
	}

	// Enabled only past the port guard — see note 2 in the header.
	runtime.SetMutexProfileFraction(mutexProfileFraction)
	runtime.SetBlockProfileRate(blockProfileRateNanos)

	// pprof is unauthenticated and exposes process internals (heap, goroutine
	// stacks, cmdline). Loopback is the intended posture: reach it with an SSH
	// tunnel. A non-loopback bind is honoured but logged loudly.
	if !isLoopbackBind(bindAddr) {
		logger().Warn(context.Background(), "Profiler on a NON-loopback bind and unauthenticated — restrict via firewall, or prefer 127.0.0.1 + an SSH tunnel",
			ion.String("bind", bindAddr))
	}

	addr := net.JoinHostPort(bindAddr, port)
	server := &http.Server{
		Addr:              addr,
		Handler:           newProfilerMux(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Must exceed the longest profile a client may request: `go tool pprof`
		// defaults to 30s CPU profiles and ?seconds= can ask for more.
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// A stopped profiler should stop costing anything.
	server.RegisterOnShutdown(func() {
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
		logger().Info(context.Background(), "Profiler stopped — contention profiling disabled")
	})

	go func() {
		ctx := context.Background()
		defer func() {
			if r := recover(); r != nil {
				logger().Error(ctx, "Profiler server panic",
					fmt.Errorf("%v", r),
					ion.String("recovered", fmt.Sprintf("%v", r)))
			}
		}()

		logger().Info(ctx, "Starting profiler server",
			ion.String("addr", fmt.Sprintf("http://%s:%s/debug/pprof/", bindAddr, port)))
		logger().Info(ctx, "FD Monitor available",
			ion.String("addr", fmt.Sprintf("http://%s:%s/debug/fds", bindAddr, port)))
		logger().Info(ctx, "Stream Monitor available",
			ion.String("addr", fmt.Sprintf("http://%s:%s/debug/streams", bindAddr, port)))

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger().Error(ctx, "Profiler server error", err,
				ion.String("addr", addr))
		}
	}()

	return server
}

// openFDCount returns this process's open file-descriptor count from
// /proc/self/fd. Deliberately not `lsof`: the runtime image does not ship it
// (so the old shell-out always failed in Docker), and this avoids a subprocess.
func openFDCount() (int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	// ReadDir holds one descriptor open while listing; discount it so repeated
	// calls report a stable number.
	if n := len(entries) - 1; n > 0 {
		return n, nil
	}
	return 0, nil
}

// fdHandler reports the process's open file-descriptor count.
func fdHandler(w http.ResponseWriter, r *http.Request) {
	count, err := openFDCount()
	if err != nil {
		http.Error(w, fmt.Sprintf("fd count unavailable on this platform: %v", err), http.StatusNotImplemented)
		return
	}
	writeJSON(w, map[string]any{"pid": os.Getpid(), "fd_count": count})
}

// streamHandler reports active libp2p streams grouped by protocol.
func streamHandler(w http.ResponseWriter, r *http.Request) {
	h := currentHost()
	if h == nil {
		http.Error(w, "host not registered", http.StatusServiceUnavailable)
		return
	}

	conns := h.Network().Conns()
	protocolCounts := make(map[string]int)
	total := 0
	for _, conn := range conns {
		for _, s := range conn.GetStreams() {
			proto := string(s.Protocol())
			if proto == "" {
				proto = "unknown"
			}
			protocolCounts[proto]++
			total++
		}
	}

	// Protocol IDs come from remote peers, so they are untrusted input and must
	// be encoded, never interpolated into JSON by hand.
	writeJSON(w, map[string]any{
		"total_streams": total,
		"connections":   len(conns),
		"protocols":     protocolCounts,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger().Debug(context.Background(), "Profiler: response encode failed", ion.Err(err))
	}
}
