package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Transaction-status resolution metrics.
//
// What these are for: the status path answers "where is this transaction?" by
// consulting the chain store, then the mempool fleet, then local records. The
// two things an operator needs to see are (a) the mix of answers — a rising
// share of `unknown` means the resolver has stopped being able to tell, not
// that transactions stopped existing — and (b) whether the guards are firing,
// because a tripped breaker or a saturated rate limit silently converts real
// answers into `unknown`.
var (
	// TxStatusResolvedCounter counts resolutions by answer. The `degraded`
	// label matters as much as `status`: a degraded answer is "we could not
	// tell", and a burst of them is an incident even while the status label
	// looks unremarkable.
	TxStatusResolvedCounter = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "jmdn_tx_status_resolved_total",
			Help: "Transaction status resolutions by resulting status, answering source, and whether the answer was degraded",
		},
		[]string{"status", "source", "degraded"},
	)

	// TxStatusResolveDuration is end-to-end resolve latency. The status label
	// separates the cheap chain-store hit from the paths that call out.
	TxStatusResolveDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "jmdn_tx_status_resolve_duration_seconds",
			Help:    "End-to-end transaction status resolution latency in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		},
		[]string{"status"},
	)

	// TxStatusMempoolLookupCounter counts mempool lookup attempts by outcome:
	// found, absent, degraded, breaker_open, rate_limited. breaker_open and
	// rate_limited are lookups that never left the process.
	TxStatusMempoolLookupCounter = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "jmdn_tx_status_mempool_lookup_total",
			Help: "Mempool by-hash lookup attempts by outcome (found, absent, degraded, breaker_open, rate_limited)",
		},
		[]string{"outcome"},
	)

	// TxStatusMempoolLookupDuration is the latency of lookups that actually
	// reached the mempool.
	TxStatusMempoolLookupDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "jmdn_tx_status_mempool_lookup_duration_seconds",
			Help:    "Latency of mempool by-hash lookups that reached the network, in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		},
		[]string{"outcome"},
	)

	// TxStatusBreakerTripsCounter counts breaker OPENINGS, not requests served
	// while open — the resolver emits the delta of a cumulative count.
	TxStatusBreakerTripsCounter = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "jmdn_tx_status_breaker_trips_total",
			Help: "Number of times the mempool lookup circuit breaker opened",
		},
	)

	// TxStatusNegativeCacheCounter counts negative-cache hit/miss/store. Only
	// conclusive unknowns are ever stored, so a high store rate means genuine
	// probing for nonexistent hashes.
	TxStatusNegativeCacheCounter = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "jmdn_tx_status_negative_cache_total",
			Help: "Transaction status negative cache events (hit, miss, store)",
		},
		[]string{"event"},
	)

	// TxStatusSubmitLogSize tracks how many in-flight submit records are held.
	// If this pins at the configured capacity, records are being evicted before
	// their TTL and in-flight transactions will report `unknown` instead of
	// `processing`.
	TxStatusSubmitLogSize = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "jmdn_tx_status_submit_log_records",
			Help: "Number of transaction submit records currently held in memory",
		},
	)
)

// TxStatusObserver implements txstatus.Observer against the metrics above.
//
// It lives here rather than in the txstatus package so that package stays free
// of a metrics dependency and can be tested without a registry.
type TxStatusObserver struct{}

// ObserveResolve records one completed resolution.
func (TxStatusObserver) ObserveResolve(status, source string, degraded bool, d time.Duration) {
	degradedLabel := "false"
	if degraded {
		degradedLabel = "true"
	}
	TxStatusResolvedCounter.WithLabelValues(status, source, degradedLabel).Inc()
	TxStatusResolveDuration.WithLabelValues(status).Observe(d.Seconds())
}

// ObserveMempoolLookup records one mempool lookup attempt. Attempts that were
// short-circuited locally (breaker_open, rate_limited) carry no duration, so
// they are counted but not timed — otherwise they would drag the latency
// histogram toward zero and hide a genuinely slow mempool.
func (TxStatusObserver) ObserveMempoolLookup(outcome string, d time.Duration) {
	TxStatusMempoolLookupCounter.WithLabelValues(outcome).Inc()
	if d > 0 {
		TxStatusMempoolLookupDuration.WithLabelValues(outcome).Observe(d.Seconds())
	}
}

// ObserveBreakerTrips records breaker openings. The resolver passes a
// cumulative total and only calls this when it has increased, so adding 1 here
// would undercount a multi-step jump; the caller guarantees monotonicity.
func (TxStatusObserver) ObserveBreakerTrips(_ int64) {
	TxStatusBreakerTripsCounter.Inc()
}

// ObserveNegativeCache records a hit, miss or store.
func (TxStatusObserver) ObserveNegativeCache(event string) {
	TxStatusNegativeCacheCounter.WithLabelValues(event).Inc()
}

// SetTxStatusSubmitLogSize publishes the current submit-log occupancy.
func SetTxStatusSubmitLogSize(n int) {
	TxStatusSubmitLogSize.Set(float64(n))
}
