package Context

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var (
	globalContext context.Context    // globalContext is the shared parent context for the process.
	globalCancel  context.CancelFunc // globalCancel cancels the globalContext.

	ctxMu      sync.RWMutex // ctxMu protects concurrent access to globalContext and globalCancel.
	signalOnce sync.Once    // signalOnce ensures the os signal handler is only set up once.
)

// Init sets up the global context if it hasn't been created yet and
// returns it so callers can use it as a parent.
func Init() context.Context {
	ctxMu.Lock()
	defer ctxMu.Unlock()

	if globalContext != nil && globalContext.Err() == nil {
		return globalContext
	}

	globalContext, globalCancel = context.WithCancel(context.Background())
	setupSignalHandler()
	return globalContext
}

// Get returns the currently initialized global context, calling Init if
// needed so callers can always rely on a valid parent context.
func Get() context.Context {
	ctxMu.RLock()
	ctx := globalContext
	ctxMu.RUnlock()

	if ctx != nil {
		return ctx
	}
	return Init()
}

// Shutdown triggers the cancellation of the global context so that any
// child contexts can exit gracefully.
func Shutdown() {
	ctxMu.Lock()
	defer ctxMu.Unlock()

	if globalCancel != nil {
		globalCancel()
		globalCancel = nil
	}
	globalContext = nil
	signalOnce = sync.Once{}
}

// NewChildContext creates a child context derived from the global context so callers
// can manage lifecycle specific to a component while still honoring the
// process-wide shutdown signal.
func NewChildContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(Get())
}

func setupSignalHandler() {
	signalOnce.Do(func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			sig := <-sigCh
			log.Printf("Global context received shutdown signal: %s", sig)
			Shutdown()
			signal.Stop(sigCh)
		}()
	})
}
