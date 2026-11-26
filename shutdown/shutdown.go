package shutdown

import (
	"bytes"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gossipnode/config/GRO"
	"gossipnode/logging"
	groMetrics "gossipnode/metrics/gro"
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

var (
	inProgress   atomic.Bool
	shutdownOnce sync.Once
	shutdownOK   bool
	// shutdownBody is the real shutdown sequence; a variable so tests can
	// replace it without a GRO runtime.
	shutdownBody = runShutdown
)

// Begin marks that a shutdown has started. Shutdown calls it first; a caller
// that starts a shutdown on another goroutine calls it before doing so, so
// InProgress is already true by the time any part of the shutdown runs.
func Begin() { inProgress.Store(true) }

// InProgress reports whether a shutdown has started. main checks it when the
// CLI returns: GRO's shutdown cancels the CLI, and main must not return then,
// because returning from main ends the process at once with status 0, before
// the shutdown has closed resources, flushed the logger and chosen the exit
// code.
func InProgress() bool { return inProgress.Load() }

// Shutdown runs the shutdown sequence (GRO, resource closers, logger) and
// reports whether it all completed cleanly. It runs once per process: a
// second caller (a signal arriving during a CLI stopService, say) waits for
// the first run and gets its result, instead of shutting GRO and the logger
// down a second time.
func Shutdown() bool {
	Begin()
	shutdownOnce.Do(func() { shutdownOK = shutdownBody() })
	return shutdownOK
}

// ShutdownAndExitInBackground starts Shutdown followed by Exit on a plain
// goroutine and returns at once. For callers that are themselves GRO-tracked
// goroutines (the CLI stopService command): calling Shutdown inline there
// makes GRO's shutdown wait on its own caller until its timeout. The caller
// should return right after this, so GRO has nothing of it left to wait for.
// InProgress is true before this returns.
func ShutdownAndExitInBackground() {
	Begin()
	SetDeadline(time.Now().Add(backgroundShutdownBudget))
	go func() { Exit(Shutdown()) }()
}

// backgroundShutdownBudget is the closers' deadline for a shutdown started
// with ShutdownAndExitInBackground, which has no watchdog of its own: the
// same 25s - 2s main gives a signal-driven shutdown.
const backgroundShutdownBudget = 23 * time.Second

func runShutdown() bool {
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

	// Release the registered resources (outbox worker, ThebeDB / Badger,
	// Postgres pools, libp2p host), newest first, each with its own time limit.
	// This runs even when the GRO step timed out: goroutines have been
	// cancelled by then, and skipping it is what left Badger unclosed on every
	// stop. See closers.go.
	notice("Closing resources...\n")
	if !RunClosers() {
		exitCode = 1
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

// exitFn is os.Exit; a variable so tests can observe the exit code.
var exitFn = os.Exit

func OS_EXIT(code int) {
	exitFn(code)
}

// Exit ends the process after Shutdown: code 0 when ok, 1 otherwise. It ALWAYS
// exits. Callers used to exit only when Shutdown returned true, so a shutdown
// that timed out left the process parked (main() waits in select {}) until the
// supervisor killed it: ~90s under systemd, 30s under Docker.
func Exit(ok bool) {
	if ok {
		OS_EXIT(0)
		return
	}
	OS_EXIT(1)
}

// ForceExitOnSignal waits for one more signal on sigCh and then exits at once
// with code 1, skipping the rest of the shutdown. Start it right after the
// first signal is received, so a second Ctrl+C / SIGTERM is an escape hatch
// when the graceful path is stuck. sigCh is read only once by the handler, so
// without this a second signal did nothing.
func ForceExitOnSignal(sigCh <-chan os.Signal) {
	sig, ok := <-sigCh
	if !ok {
		return
	}
	notice("\nsecond %v received during shutdown; exiting now\n", sig)
	OS_EXIT(1)
}
