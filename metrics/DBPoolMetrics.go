package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// POOL-LEVEL METRICS (No Labels) - For Dashboard Totals
// =============================================================================
// These metrics track the overall pool state and are what your Grafana dashboard should use

var (
	// AccountsDB Pool-Level Metrics (No Labels)
	AccountsDBConnectionPoolCount = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "accounts_db_connection_pool_count",
			Help: "Total number of connections in the AccountsDB pool",
		},
	)

	AccountsDBConnectionPoolActive = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "accounts_db_connection_pool_active",
			Help: "Number of active connections in the AccountsDB pool",
		},
		[]string{"function"},
	)

	AccountsDBConnectionPoolIdle = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "accounts_db_connection_pool_idle",
			Help: "Number of idle connections in the AccountsDB pool",
		},
	)

	// MainDB Pool-Level Metrics (No Labels)
	MainDBConnectionPoolCount = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "main_db_connection_pool_count",
			Help: "Total number of connections in the MainDB pool",
		},
	)

	MainDBConnectionPoolActive = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "main_db_connection_pool_active",
			Help: "Number of active connections in the MainDB pool",
		},
		[]string{"function"},
	)

	MainDBConnectionPoolIdle = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "main_db_connection_pool_idle",
			Help: "Number of idle connections in the MainDB pool",
		},
	)
)

// =============================================================================
// PER-FUNCTION METRICS (With Labels) - For Detailed Analysis
// =============================================================================
// These metrics track per-function connection usage for detailed analysis

var (
	// AccountsDB Per-Function Metrics
	AccountsDBConnectionsByFunction = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "accounts_db_connections_by_function",
			Help: "Active connections tracked per function for AccountsDB",
		},
		[]string{"function"},
	)

	AccountsDBConnectionTakesTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "accounts_db_connection_takes_total",
			Help: "Total number of times connections were taken from AccountsDB pool per function",
		},
		[]string{"function"},
	)

	AccountsDBConnectionReturnsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "accounts_db_connection_returns_total",
			Help: "Total number of times connections were returned to AccountsDB pool per function",
		},
		[]string{"function"},
	)

	// MainDB Per-Function Metrics
	MainDBConnectionsByFunction = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "main_db_connections_by_function",
			Help: "Active connections tracked per function for MainDB",
		},
		[]string{"function"},
	)

	MainDBConnectionTakesTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "main_db_connection_takes_total",
			Help: "Total number of times connections were taken from MainDB pool per function",
		},
		[]string{"function"},
	)

	MainDBConnectionReturnsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "main_db_connection_returns_total",
			Help: "Total number of times connections were returned to MainDB pool per function",
		},
		[]string{"function"},
	)
)

// =============================================================================
// HELPER FUNCTIONS FOR POOL-LEVEL METRICS
// =============================================================================

// UpdateAccountsDBConnectionPoolMetrics updates all three AccountsDB pool metrics at once
func UpdateAccountsDBConnectionPoolMetrics(total, active, idle int) {
	AccountsDBConnectionPoolCount.Set(float64(total))
	// Set active for all existing function labels (or use "pool_state" as default)
	AccountsDBConnectionPoolActive.WithLabelValues("pool_state").Set(float64(active))
	AccountsDBConnectionPoolIdle.Set(float64(idle))
}

// UpdateMainDBConnectionPoolMetrics updates all three MainDB pool metrics at once
func UpdateMainDBConnectionPoolMetrics(total, active, idle int) {
	MainDBConnectionPoolCount.Set(float64(total))
	// Set active for all existing function labels (or use "pool_state" as default)
	MainDBConnectionPoolActive.WithLabelValues("pool_state").Set(float64(active))
	MainDBConnectionPoolIdle.Set(float64(idle))
}

// UpdateAccountsDBConnectionPoolMetricsWithFunction is now deprecated - use UpdateAccountsDBConnectionPoolMetrics instead
func UpdateAccountsDBConnectionPoolMetricsWithFunction(functionName string, total, active, idle int) {
	// Ignore function name for pool-level metrics
	UpdateAccountsDBConnectionPoolMetrics(total, active, idle)
}

// UpdateMainDBConnectionPoolMetricsWithFunction is now deprecated - use UpdateMainDBConnectionPoolMetrics instead
func UpdateMainDBConnectionPoolMetricsWithFunction(functionName string, total, active, idle int) {
	// Ignore function name for pool-level metrics
	UpdateMainDBConnectionPoolMetrics(total, active, idle)
}
