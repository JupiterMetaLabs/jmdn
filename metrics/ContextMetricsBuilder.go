/*
ContextMetricsBuilder provides a fluent builder interface for updating context metrics.
All changes are immediately reflected in Prometheus metrics and will appear on the Grafana dashboard.

Usage Examples:

	// Track global context initialization
	metrics.NewContextMetricsBuilder().
		GlobalContextInitialized().
		GlobalContextActive()

	// Track app context initialization
	metrics.NewContextMetricsBuilder().
		WithApp("accounts").
		AppContextInitialized().
		AppContextActive()

	// Track child context creation
	metrics.NewContextMetricsBuilder().
		WithApp("accounts").
		ChildContextCreated()

	// Track child context cancellation
	metrics.NewContextMetricsBuilder().
		WithApp("accounts").
		ChildContextCancelled()

	// Track child context with timeout
	metrics.NewContextMetricsBuilder().
		WithApp("accounts").
		ChildContextCreatedWithTimeout()
*/

package metrics

import (
	"sync"
)

type ContextMetricsBuilder struct {
	app string     // app name for app-level metrics (empty for global)
	mu  sync.Mutex // mutex to protect app in concurrent scenarios
}

// Singleton instance for Context Metrics (with mutex for thread-safety)
var (
	ContextMetricsBuilderInstance *ContextMetricsBuilder
	contextMetricsMutex           sync.Mutex
)

// NewContextMetricsBuilder creates a new builder for context metrics
// Uses singleton pattern with thread-safe initialization
func NewContextMetricsBuilder() *ContextMetricsBuilder {
	if ContextMetricsBuilderInstance == nil {
		contextMetricsMutex.Lock()
		defer contextMetricsMutex.Unlock()
		// Double-check after acquiring lock
		if ContextMetricsBuilderInstance == nil {
			ContextMetricsBuilderInstance = &ContextMetricsBuilder{
				app: "",
			}
		}
	}
	// Return a new instance for each call to avoid state sharing
	return &ContextMetricsBuilder{
		app: "",
	}
}

// WithApp sets the app name for app-level metrics
func (b *ContextMetricsBuilder) WithApp(app string) *ContextMetricsBuilder {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.app = app
	return b
}

// GlobalContextInitialized sets global context as initialized (count = 1)
func (b *ContextMetricsBuilder) GlobalContextInitialized() *ContextMetricsBuilder {
	GlobalContextCount.Set(1)
	return b
}

// GlobalContextUninitialized sets global context as uninitialized (count = 0)
func (b *ContextMetricsBuilder) GlobalContextUninitialized() *ContextMetricsBuilder {
	GlobalContextCount.Set(0)
	return b
}

// GlobalContextActive sets global context as active (active = 1)
func (b *ContextMetricsBuilder) GlobalContextActive() *ContextMetricsBuilder {
	GlobalContextActive.Set(1)
	return b
}

// GlobalContextInactive sets global context as inactive (active = 0)
func (b *ContextMetricsBuilder) GlobalContextInactive() *ContextMetricsBuilder {
	GlobalContextActive.Set(0)
	return b
}

// GlobalChildContextCreated increments the counter for global child contexts created
func (b *ContextMetricsBuilder) GlobalChildContextCreated() *ContextMetricsBuilder {
	IncrementGlobalChildContextCreated()
	return b
}

// GlobalChildContextCancelled increments the counter for global child contexts cancelled
func (b *ContextMetricsBuilder) GlobalChildContextCancelled() *ContextMetricsBuilder {
	IncrementGlobalChildContextCancelled()
	return b
}

// AppContextInitialized sets app context as initialized (count = 1) for the specified app
func (b *ContextMetricsBuilder) AppContextInitialized() *ContextMetricsBuilder {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()

	if app == "" {
		return b // No-op if app is not set
	}
	AppContextCount.WithLabelValues(app).Set(1)
	return b
}

// AppContextUninitialized sets app context as uninitialized (count = 0) for the specified app
func (b *ContextMetricsBuilder) AppContextUninitialized() *ContextMetricsBuilder {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()

	if app == "" {
		return b // No-op if app is not set
	}
	AppContextCount.WithLabelValues(app).Set(0)
	return b
}

// AppContextActive sets app context as active (active = 1) for the specified app
func (b *ContextMetricsBuilder) AppContextActive() *ContextMetricsBuilder {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()

	if app == "" {
		return b // No-op if app is not set
	}
	AppContextActive.WithLabelValues(app).Set(1)
	return b
}

// AppContextInactive sets app context as inactive (active = 0) for the specified app
func (b *ContextMetricsBuilder) AppContextInactive() *ContextMetricsBuilder {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()

	if app == "" {
		return b // No-op if app is not set
	}
	AppContextActive.WithLabelValues(app).Set(0)
	return b
}

// ChildContextCreated increments the counter for app child contexts created
func (b *ContextMetricsBuilder) ChildContextCreated() *ContextMetricsBuilder {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()

	if app == "" {
		// If no app specified, track as global child context
		IncrementGlobalChildContextCreated()
	} else {
		IncrementAppChildContextCreated(app)
	}
	return b
}

// ChildContextCancelled increments the counter for app child contexts cancelled
func (b *ContextMetricsBuilder) ChildContextCancelled() *ContextMetricsBuilder {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()

	if app == "" {
		// If no app specified, track as global child context
		IncrementGlobalChildContextCancelled()
	} else {
		IncrementAppChildContextCancelled(app)
	}
	return b
}

// ChildContextCreatedWithTimeout increments the counter for app child contexts created with timeout
func (b *ContextMetricsBuilder) ChildContextCreatedWithTimeout() *ContextMetricsBuilder {
	b.mu.Lock()
	app := b.app
	b.mu.Unlock()

	if app == "" {
		return b // No-op if app is not set (timeout contexts are app-specific)
	}
	IncrementAppChildContextCreatedWithTimeout(app)
	return b
}

// Convenience functions for direct usage without builder pattern

// SetGlobalContextMetrics sets global context metrics at once
func SetGlobalContextMetrics(initialized, active bool) {
	UpdateGlobalContextMetrics(initialized, active)
}

// SetAppContextMetrics sets app context metrics at once
func SetAppContextMetrics(app string, initialized, active bool) {
	UpdateAppContextMetrics(app, initialized, active)
}
