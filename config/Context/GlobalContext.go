package Context

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gossipnode/metrics"
)

type GlobalContext struct{}

func GetGlobalContext() ContextInterface {
	return &GlobalContext{}
}

// SetAppName is a no-op for GlobalContext since it doesn't have an app name.
func (gc *GlobalContext) SetAppName(app string) {
	// No-op: GlobalContext doesn't have an app name
}

// Init sets up the global context if it hasn't been created yet and
// returns it so callers can use it as a parent.
func (gc *GlobalContext) Init() context.Context {
	ctxMu.Lock()
	defer ctxMu.Unlock()

	if globalContext != nil && globalContext.Err() == nil {
		return globalContext
	}

	globalContext, globalCancel = context.WithCancel(context.Background())
	isInitialized = true
	// Initialize app-level maps if they don't exist
	if appContexts == nil {
		appContexts = make(map[string]context.Context)
	}
	if appCancels == nil {
		appCancels = make(map[string]context.CancelFunc)
	}

	// Track metrics: global context initialized and active
	metrics.NewContextMetricsBuilder().
		GlobalContextInitialized().
		GlobalContextActive()

	gc.setupSignalHandler()
	return globalContext
}

// Get returns the currently initialized global context, calling Init if
// needed so callers can always rely on a valid parent context.
func (gc *GlobalContext) Get() context.Context {
	ctxMu.RLock()
	ctx := globalContext
	ctxMu.RUnlock()

	if ctx != nil {
		return ctx
	}
	return gc.Init()
}

func (gc *GlobalContext) Done(ctx context.Context) {
	// Close that particular background context
	ctx.Done()

	// Track metrics: global context child context cancelled
	metrics.NewContextMetricsBuilder().
		GlobalChildContextCancelled()
}

// Shutdown triggers the cancellation of the global context and all app-level contexts.
func (gc *GlobalContext) Shutdown() {
	ctxMu.Lock()
	defer ctxMu.Unlock()

	// Cancel all app-level contexts first
	for appName, cancel := range appCancels {
		if cancel != nil {
			log.Printf("Shutting down app-level context for: %s", appName)
			cancel()
		}
	}
	appCancels = make(map[string]context.CancelFunc)
	appContexts = make(map[string]context.Context)

	// Cancel global context
	if globalCancel != nil {
		globalCancel()
		globalCancel = nil
	}
	globalContext = nil
	isInitialized = false
	signalOnce = sync.Once{}

	// Track metrics: global context uninitialized and inactive
	metrics.NewContextMetricsBuilder().
		GlobalContextUninitialized().
		GlobalContextInactive()
}

// NewChildContext creates a child context derived from the global context.
func (gc *GlobalContext) NewChildContext() (context.Context, context.CancelFunc) {
	// Track metrics: global child context created
	metrics.NewContextMetricsBuilder().GlobalChildContextCreated()

	ctx, cancel := context.WithCancel(gc.Get())
	// Wrap cancel to track cancellation
	return ctx, func() {
		metrics.NewContextMetricsBuilder().GlobalChildContextCancelled()
		cancel()
	}
}

// NewChildContextWithTimeout creates a child context with timeout from the global context.
func (gc *GlobalContext) NewChildContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	// Track metrics: global child context created
	metrics.NewContextMetricsBuilder().GlobalChildContextCreated()

	ctx, cancel := context.WithTimeout(gc.Get(), timeout)
	// Wrap cancel to track cancellation
	return ctx, func() {
		metrics.NewContextMetricsBuilder().GlobalChildContextCancelled()
		cancel()
	}
}

// ListActiveApps returns a list of all apps with active contexts.
func (gc *GlobalContext) ListActiveApps() []string {
	ctxMu.RLock()
	defer ctxMu.RUnlock()

	apps := make([]string, 0, len(appContexts))
	for appName, ctx := range appContexts {
		if ctx.Err() == nil {
			apps = append(apps, appName)
		}
	}
	return apps
}

func (gc *GlobalContext) setupSignalHandler() {
	signalOnce.Do(func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			log.Printf("Global context received shutdown signal: %s", sig)
			gc.Shutdown()
			signal.Stop(sigCh)
		}()
	})
}
