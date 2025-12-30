package metrics

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	groResultOK       = "ok"
	groResultCanceled = "canceled"
	groResultError    = "error"
)

var (
	registerRuntimeCollectorsOnce sync.Once

	// groGoroutinesRunning tracks currently-running GRO-managed goroutines.
	// Labels:
	// - app: GRO app name (e.g. "app:main")
	// - local: GRO local manager name (e.g. "local:main")
	// - thread: GRO function/thread name (e.g. "thread:facade")
	groGoroutinesRunning = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gro_goroutines_running",
			Help: "Currently running goroutines tracked by the goroutine orchestrator",
		},
		[]string{"app", "local", "thread"},
	)

	groGoroutinesStartedTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gro_goroutines_started_total",
			Help: "Total goroutines started via the goroutine orchestrator",
		},
		[]string{"app", "local", "thread"},
	)

	groGoroutinesFinishedTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gro_goroutines_finished_total",
			Help: "Total goroutines finished via the goroutine orchestrator",
		},
		[]string{"app", "local", "thread", "result"},
	)

	groGoroutineDurationSeconds = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gro_goroutine_duration_seconds",
			Help:    "Duration of goroutines tracked by the goroutine orchestrator",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"app", "local", "thread"},
	)
)

func init() {
	// Ensure the standard Go runtime + process collectors are present on our custom registry.
	// This enables baseline dashboards (e.g. go_goroutines) even before GRO-level instrumentation
	// is rolled out everywhere.
	RegisterRuntimeCollectors()
}

// RegisterRuntimeCollectors registers the standard Go runtime and process collectors
// into the application's custom Prometheus registry.
//
// This is safe to call multiple times.
func RegisterRuntimeCollectors() {
	registerRuntimeCollectorsOnce.Do(func() {
		// GoCollector provides: go_goroutines, go_threads, go_memstats_*, go_gc_duration_seconds, ...
		_ = DefaultRegistry.Register(prometheus.NewGoCollector())

		// ProcessCollector provides: process_cpu_seconds_total, process_resident_memory_bytes, ...
		_ = DefaultRegistry.Register(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	})
}

// WrapTrackedGoroutine returns a worker function that updates GRO metrics around `fn`.
//
// Usage example:
//
//	tracked := metrics.WrapTrackedGoroutine(GRO.MainAM, GRO.MainLM, GRO.FacadeThread, fn)
//	_ = localMgr.Go(GRO.FacadeThread, tracked)
func WrapTrackedGoroutine(
	appName string,
	localName string,
	threadName string,
	fn func(ctx context.Context) error,
) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		start := time.Now()
		groGoroutinesRunning.WithLabelValues(appName, localName, threadName).Inc()
		groGoroutinesStartedTotal.WithLabelValues(appName, localName, threadName).Inc()

		err := fn(ctx)

		groGoroutinesRunning.WithLabelValues(appName, localName, threadName).Dec()
		groGoroutineDurationSeconds.WithLabelValues(appName, localName, threadName).
			Observe(time.Since(start).Seconds())

		result := groResultOK
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				result = groResultCanceled
			} else {
				result = groResultError
			}
		}
		groGoroutinesFinishedTotal.WithLabelValues(appName, localName, threadName, result).Inc()
		return err
	}
}

// GoTracked spawns a goroutine via a GRO local manager and records GRO goroutine metrics.
//
// Usage example:
//
//	_ = metrics.GoTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.FacadeThread, fn)
func GoTracked(
	localMgr interfaces.LocalGoroutineManagerInterface,
	appName string,
	localName string,
	threadName string,
	fn func(ctx context.Context) error,
	opts ...interfaces.GoroutineOption,
) error {
	if localMgr == nil {
		return errors.New("metrics: localMgr is nil")
	}
	tracked := WrapTrackedGoroutine(appName, localName, threadName, fn)
	return localMgr.Go(threadName, tracked, opts...)
}
