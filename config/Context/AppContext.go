package Context

import (
	"context"
	"fmt"
	"time"
)

type AppContext struct {
	GlobalContext *GlobalContext
	App           string
}

func GetAppContext(app string) ContextInterface {
	// Get global context and ensure it's initialized
	gc := GetGlobalContext()
	gc.Init()

	// Type assert to get the concrete GlobalContext instance
	// Since GetGlobalContext() always returns *GlobalContext, this is safe
	globalCtx, ok := gc.(*GlobalContext)
	if !ok {
		// Fallback: create a new GlobalContext if type assertion fails
		globalCtx = &GlobalContext{}
	}

	return &AppContext{
		GlobalContext: globalCtx,
		App:           app,
	}
}

// SetAppName sets the name of the app for the app context.
func (ac *AppContext) SetAppName(app string) {
	ac.App = app
}

// InitApp initializes an app-level context as a child of the global context.
// If an app context already exists for this app name, it returns the existing one.
func (ac *AppContext) Init() context.Context {
	ctxMu.Lock()
	defer ctxMu.Unlock()

	// Ensure global context is initialized
	if globalContext == nil {
		globalContext, globalCancel = context.WithCancel(context.Background())
		if appContexts == nil {
			appContexts = make(map[string]context.Context)
		}
		if appCancels == nil {
			appCancels = make(map[string]context.CancelFunc)
		}
		ac.GlobalContext.setupSignalHandler()
	}

	// Check if app context already exists and is valid
	if ctx, exists := appContexts[ac.App]; exists && ctx.Err() == nil {
		fmt.Printf("App context for '%s' already initialized\n", ac.App)
		return ctx
	}

	// Create new app-level context
	appCtx, appCancel := context.WithCancel(globalContext)
	appContexts[ac.App] = appCtx
	appCancels[ac.App] = appCancel

	fmt.Printf("Initialized app-level context for: %s\n", ac.App)
	return appCtx
}

// GetApp returns the app-level context for the current app, initializing it if needed.
func (ac *AppContext) Get() context.Context {
	ctxMu.RLock()
	ctx, exists := appContexts[ac.App]
	ctxMu.RUnlock()

	if exists && ctx.Err() == nil {
		return ctx
	}
	// Initialize the app-level context and return the new child context
	return ac.Init()
}

// ShutdownApp cancels the app-level context for the current app.
func (ac *AppContext) Shutdown() {
	ctxMu.Lock()
	defer ctxMu.Unlock()

	if cancel, exists := appCancels[ac.App]; exists && cancel != nil {
		fmt.Printf("Shutting down app-level context for: %s\n", ac.App)
		cancel()
		delete(appCancels, ac.App)
		delete(appContexts, ac.App)
	}
}

// NewChildContextForAppWithTimeout creates a child context with timeout from the app-level context.
func (ac *AppContext) NewChildContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ac.Get(), timeout)
}

// NewChildContextForApp creates a child context derived from the app-level context.
func (ac *AppContext) NewChildContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(ac.Get())
}
