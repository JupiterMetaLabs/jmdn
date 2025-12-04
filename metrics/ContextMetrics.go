package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// GLOBAL CONTEXT METRICS
// =============================================================================
// These metrics track the global context state

var (
	// GlobalContextCount tracks if the global context is initialized (0 or 1)
	GlobalContextCount = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "global_context_count",
			Help: "Number of global contexts (should be 0 or 1)",
		},
	)

	// GlobalContextActive tracks if the global context is active (0 or 1)
	GlobalContextActive = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "global_context_active",
			Help: "Number of active global contexts (should be 0 or 1)",
		},
	)

	// GlobalChildContextsCreated tracks total child contexts created from global context
	GlobalChildContextsCreated = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "global_child_contexts_created_total",
			Help: "Total number of child contexts created from global context",
		},
	)

	// GlobalChildContextsCancelled tracks total child contexts cancelled from global context
	GlobalChildContextsCancelled = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "global_child_contexts_cancelled_total",
			Help: "Total number of child contexts cancelled from global context",
		},
	)
)

// =============================================================================
// APP-LEVEL CONTEXT METRICS
// =============================================================================
// These metrics track app-level contexts per app name

var (
	// AppContextCount tracks the number of initialized app contexts per app
	AppContextCount = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "app_context_count",
			Help: "Number of initialized app contexts per app",
		},
		[]string{"app"},
	)

	// AppContextActive tracks the number of active app contexts per app
	AppContextActive = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "app_context_active",
			Help: "Number of active app contexts per app",
		},
		[]string{"app"},
	)

	// AppChildContextsCreated tracks total child contexts created per app
	AppChildContextsCreated = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "app_child_contexts_created_total",
			Help: "Total number of child contexts created per app",
		},
		[]string{"app"},
	)

	// AppChildContextsCancelled tracks total child contexts cancelled per app
	AppChildContextsCancelled = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "app_child_contexts_cancelled_total",
			Help: "Total number of child contexts cancelled per app",
		},
		[]string{"app"},
	)

	// AppChildContextsCreatedWithTimeout tracks child contexts created with timeout per app
	AppChildContextsCreatedWithTimeout = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "app_child_contexts_created_with_timeout_total",
			Help: "Total number of child contexts created with timeout per app",
		},
		[]string{"app"},
	)
)

// =============================================================================
// HELPER FUNCTIONS FOR CONTEXT METRICS
// =============================================================================

// UpdateGlobalContextMetrics updates global context metrics
func UpdateGlobalContextMetrics(initialized, active bool) {
	if initialized {
		GlobalContextCount.Set(1)
	} else {
		GlobalContextCount.Set(0)
	}
	if active {
		GlobalContextActive.Set(1)
	} else {
		GlobalContextActive.Set(0)
	}
}

// UpdateAppContextMetrics updates app-level context metrics
func UpdateAppContextMetrics(app string, initialized, active bool) {
	count := 0
	if initialized {
		count = 1
	}
	AppContextCount.WithLabelValues(app).Set(float64(count))

	activeCount := 0
	if active {
		activeCount = 1
	}
	AppContextActive.WithLabelValues(app).Set(float64(activeCount))
}

// IncrementGlobalChildContextCreated increments the counter for global child contexts created
func IncrementGlobalChildContextCreated() {
	GlobalChildContextsCreated.Inc()
}

// IncrementGlobalChildContextCancelled increments the counter for global child contexts cancelled
func IncrementGlobalChildContextCancelled() {
	GlobalChildContextsCancelled.Inc()
}

// IncrementAppChildContextCreated increments the counter for app child contexts created
func IncrementAppChildContextCreated(app string) {
	AppChildContextsCreated.WithLabelValues(app).Inc()
}

// IncrementAppChildContextCancelled increments the counter for app child contexts cancelled
func IncrementAppChildContextCancelled(app string) {
	AppChildContextsCancelled.WithLabelValues(app).Inc()
}

// IncrementAppChildContextCreatedWithTimeout increments the counter for app child contexts created with timeout
func IncrementAppChildContextCreatedWithTimeout(app string) {
	AppChildContextsCreatedWithTimeout.WithLabelValues(app).Inc()
}
