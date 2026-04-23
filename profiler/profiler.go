package profiler

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/rs/zerolog/log"
)

var globalHost host.Host

// RegisterHost registers the libp2p host for stream profiling
func RegisterHost(h host.Host) {
	globalHost = h
}

func StartProfiler(bindAddr string, port string) *http.Server {
	if port == "" || port == "0" {
		return nil
	}

	// Verify security policy allows binding to 0.0.0.0
	addr := fmt.Sprintf("%s:%s", bindAddr, port)

	// Register custom handlers
	http.HandleFunc("/debug/fds", fdHandler)
	http.HandleFunc("/debug/streams", streamHandler)

	// Create server with explicit timeouts
	server := &http.Server{
		Addr: addr,
		// Handler: nil, // Uses DefaultServeMux -> imports _ "net/http/pprof"
		ReadTimeout: 10 * time.Second,
		// MUST be > 30s because "go tool pprof" defaults to 30s CPU profiles
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("Profiler server panic")
			}
		}()

		log.Info().Str("addr", fmt.Sprintf("http://%s:%s/debug/pprof/", bindAddr, port)).Msg("Starting profiler server")
		log.Info().Str("addr", fmt.Sprintf("http://%s:%s/debug/fds", bindAddr, port)).Msg("FD Monitor available")
		log.Info().Str("addr", fmt.Sprintf("http://%s:%s/debug/streams", bindAddr, port)).Msg("Stream Monitor available")

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Str("addr", addr).Msg("Profiler server error")
		}
	}()

	return server
}

// fdHandler returns the current number of open file descriptors
func fdHandler(w http.ResponseWriter, r *http.Request) {
	pid := os.Getpid()

	// Use lsof to count FDs. This works on Mac and Linux (if lsof is installed)
	// We use sh -c to simple pipe usage
	out, err := exec.Command("sh", "-c", fmt.Sprintf("lsof -p %d | wc -l", pid)).Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to run lsof: %v", err), http.StatusInternalServerError)
		return
	}

	countStr := strings.TrimSpace(string(out))
	count, err := strconv.Atoi(countStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to parse FD count: %v", err), http.StatusInternalServerError)
		return
	}

	// Adjust count (wc -l includes header line, so actual FDs is count-1)
	// But usually lsof output includes a header "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME"
	// So we subtract 1. If count is 0, return 0.
	if count > 0 {
		count = count - 1
	}

	w.Header().Set("Content-Type", "application/json")
	// Return simple JSON
	fmt.Fprintf(w, `{"pid": %d, "fd_count": %d}`, pid, count)
}

// streamHandler returns a breakdown of active streams by protocol
func streamHandler(w http.ResponseWriter, r *http.Request) {
	if globalHost == nil {
		http.Error(w, "Host not registered", http.StatusServiceUnavailable)
		return
	}

	protocolCounts := make(map[string]int)
	totalStreams := 0

	for _, conn := range globalHost.Network().Conns() {
		for _, s := range conn.GetStreams() {
			proto := string(s.Protocol())
			if proto == "" {
				proto = "unknown"
			}
			protocolCounts[proto]++
			totalStreams++
		}
	}

	w.Header().Set("Content-Type", "application/json")

	// Manually build JSON to avoid complicated struct definition inside function
	var parts []string
	for p, c := range protocolCounts {
		parts = append(parts, fmt.Sprintf(`"%s": %d`, p, c))
	}

	jsonBody := fmt.Sprintf(`{"total_streams": %d, "protocols": {%s}}`, totalStreams, strings.Join(parts, ", "))
	fmt.Fprint(w, jsonBody)
}
