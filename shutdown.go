package main

import (
	"bytes"
	"fmt"
	"gossipnode/config/GRO"
	"os"
	"runtime/pprof"
	"time"
)

const gracefulShutdownTimeout = 10 * time.Second

func shutdown() {
	done := make(chan struct{})
	go func() {
		GRO.GlobalGRO.Shutdown(true)
		close(done)
	}()

	select {
	case <-done:
		// graceful shutdown completed
	case <-time.After(gracefulShutdownTimeout):
		fmt.Printf(
			"Graceful shutdown timed out after %s; forcing shutdown...\n",
			gracefulShutdownTimeout,
		)

		// Best-effort diagnostic: dump all goroutines so we can see what's stuck.
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
		_, _ = fmt.Fprintf(os.Stderr, "\n==== goroutine dump (shutdown timeout) ====\n%s\n", buf.String())

		GRO.GlobalGRO.Shutdown(false)
	}

	// main() doesn't otherwise unblock on SIGINT; exit after shutdown attempt.
	os.Exit(0)
}
