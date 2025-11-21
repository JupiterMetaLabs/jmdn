package Context

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
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
		fmt.Println("Global context already initialized")
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
}

// NewChildContext creates a child context derived from the global context.
func (gc *GlobalContext) NewChildContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(gc.Get())
}

// NewChildContextWithTimeout creates a child context with timeout from the global context.
func (gc *GlobalContext) NewChildContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(gc.Get(), timeout)
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
