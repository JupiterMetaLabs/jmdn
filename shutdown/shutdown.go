package shutdown

import (
	"bytes"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"jmdn/config/GRO"
	"jmdn/logging"
	groMetrics "jmdn/metrics/gro"
)

const gracefulShutdownTimeout = 10 * time.Second

var stdoutMutex sync.Mutex

// atomicPrint ensures stdout writes are atomic to prevent interleaving
func atomicPrint(data []byte) error {
	stdoutMutex.Lock()
	defer stdoutMutex.Unlock()
	_, err := os.Stdout.Write(data)
	return err
}

func Shutdown() bool {
	// Print metrics header
	header := "\n" + strings.Repeat("=", 80) + "\n" +
		"SHUTDOWN INITIATED - Printing GRO Metrics\n" +
		strings.Repeat("=", 80) + "\n"
	_ = atomicPrint([]byte(header))

	// Collect and print all metrics BEFORE starting any shutdown
	// This ensures metrics are fully printed before any component starts logging
	if err := groMetrics.GroMetrics(true); err != nil {
		errorMsg := fmt.Sprintf("Failed to print GRO metrics: %v\n", err)
		_ = atomicPrint([]byte(errorMsg))
	}

	// CRITICAL: Ensure all metrics output is fully flushed and written
	// Multiple syncs with delays to guarantee complete output
	os.Stdout.Sync()
	time.Sleep(200 * time.Millisecond)
	os.Stdout.Sync()
	time.Sleep(200 * time.Millisecond)
	os.Stdout.Sync()

	// Print shutdown start message AFTER metrics are complete
	shutdownMsg := "\n" + strings.Repeat("=", 80) + "\n" +
		"Starting graceful shutdown...\n" +
		strings.Repeat("=", 80) + "\n\n"
	_ = atomicPrint([]byte(shutdownMsg))
	os.Stdout.Sync()

	// NOW start the actual shutdown process
	done := make(chan struct{})
	go func() {
		GRO.GlobalGRO.Shutdown(true)
		close(done)
	}()

	// Default to success
	exitCode := 0

	select {
	case <-done:
		// graceful shutdown completed
		fmt.Println("Graceful shutdown completed successfully")
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
		exitCode = 1 // Mark as failure
	}

	// Shutdown the logger after GRO shutdown completes
	// This ensures all logs are flushed and resources are cleaned up
	fmt.Println("Shutting down logger...")
	asyncLogger := logging.NewAsyncLogger()
	if asyncLogger != nil {
		if err := asyncLogger.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to sync logger: %v\n", err)
		}
		if err := asyncLogger.Shutdown(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to shutdown logger: %v\n", err)
		}
		fmt.Println("Logger shutdown completed")
	}

	return exitCode == 0
}

func OS_EXIT(code int) {
	os.Exit(code)
}
