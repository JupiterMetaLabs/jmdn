package Context

import (
	"context"
	"fmt"
	"time"

	"gossipnode/metrics"
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

	// Track metrics: app context initialized and active
	metrics.NewContextMetricsBuilder().
		WithApp(ac.App).
		AppContextInitialized().
		AppContextActive()

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

		// Track metrics: app context uninitialized and inactive
		metrics.NewContextMetricsBuilder().
			WithApp(ac.App).
			AppContextUninitialized().
			AppContextInactive()
	}
}

func (ac *AppContext) Done(ctx context.Context) {
	// Close that particular background context
	ctx.Done()

	// Track metrics: app context uninitialized and inactive
	metrics.NewContextMetricsBuilder().
		WithApp(ac.App).
		ChildContextCancelled()
}

// NewChildContextForAppWithTimeout creates a child context with timeout from the app-level context.
func (ac *AppContext) NewChildContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	// Track metrics: app child context created with timeout
	metrics.NewContextMetricsBuilder().
		WithApp(ac.App).
		ChildContextCreatedWithTimeout()

	ctx, cancel := context.WithTimeout(ac.Get(), timeout)
	// Wrap cancel to track cancellation
	return ctx, func() {
		metrics.NewContextMetricsBuilder().
			WithApp(ac.App).
			ChildContextCancelled()
		cancel()
	}
}

// NewChildContextForApp creates a child context derived from the app-level context.
func (ac *AppContext) NewChildContext() (context.Context, context.CancelFunc) {
	// Track metrics: app child context created
	metrics.NewContextMetricsBuilder().
		WithApp(ac.App).
		ChildContextCreated()

	ctx, cancel := context.WithCancel(ac.Get())
	// Wrap cancel to track cancellation
	return ctx, func() {
		metrics.NewContextMetricsBuilder().
			WithApp(ac.App).
			ChildContextCancelled()
		cancel()
	}
}
