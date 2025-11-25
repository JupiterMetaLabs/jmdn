package Context

import (
	"context"
	"sync"
	"time"
)

var (
	globalContext context.Context               // GlobalContext is the shared parent context for the process.
	globalCancel  context.CancelFunc            // GlobalCancel cancels the GlobalContext.
	appContexts   map[string]context.Context    // appContexts stores app-level contexts
	appCancels    map[string]context.CancelFunc // appCancels stores app-level cancel functions
	ctxMu         sync.RWMutex                  // ctxMu protects concurrent access to all context maps
	signalOnce    sync.Once                     // signalOnce ensures the os signal handler is only set up once.
	isInitialized bool                          // isInitialized tracks if the global context has been initialized.
)

type ContextInterface interface {
	Init() context.Context
	Get() context.Context
	Shutdown()
	Done(ctx context.Context)
	NewChildContext() (context.Context, context.CancelFunc)
	NewChildContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc)
	SetAppName(app string)
}
