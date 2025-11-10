/*
 DBPoolMetricsBuilder provides a fluent builder interface for updating database connection pool metrics.
 All changes are immediately reflected in Prometheus metrics and will appear on the Grafana dashboard.

 IMPORTANT: Pool-level metrics (total, active, idle) are tracked WITHOUT function labels to ensure
 accurate dashboard totals. Per-function metrics are tracked separately for detailed analysis.

 Usage Examples:

	// Set all metrics at once with function tracking
	metrics.NewAccountsDBMetricsBuilder().
		WithFunction("GetUserByID").
		SetAll(10, 3, 7) // total=10, active=3, idle=7

	// Chain operations with function name
	metrics.NewMainDBMetricsBuilder().
		WithFunction("CreateOrder").
		SetTotal(20).
		SetActive(5).
		SetIdle(15)

	// Track connection usage per function
	metrics.NewAccountsDBMetricsBuilder().
		WithFunction("ListAccounts").
		IncrementActive().  // Connection taken
		DecrementIdle()

	// Convenience methods for common operations
	metrics.NewMainDBMetricsBuilder().
		WithFunction("UpdateTransaction").
		ConnectionTaken()    // active++, idle--

	metrics.NewMainDBMetricsBuilder().
		WithFunction("UpdateTransaction").
		ConnectionReturned() // active--, idle++

	// Direct convenience functions
	metrics.SetAccountsDBPoolMetrics(10, 3, 7)
	metrics.IncrementAccountsDBPoolActiveWithFunction("GetUser")
	metrics.DecrementMainDBPoolActiveWithFunction("CreateOrder")
*/

package metrics

import (
	"fmt"
	"sync"
)

type DBPoolMetricsBuilder struct {
	poolType     string     // "accounts" or "main"
	functionName string     // name of the function using the connection (optional)
	mu           sync.Mutex // mutex to protect functionName in concurrent scenarios
}

// Singleton instances for DB Metrics (with mutex for thread-safety)
var (
	AccountsDBMetricsBuilder *DBPoolMetricsBuilder
	MainDBMetricsBuilder     *DBPoolMetricsBuilder
	accountsDBMutex          sync.Mutex
	mainDBMutex              sync.Mutex
)

// NewAccountsDBMetricsBuilder creates a new builder for AccountsDB connection pool metrics
// Uses singleton pattern with thread-safe initialization
func NewAccountsDBMetricsBuilder() *DBPoolMetricsBuilder {
	if AccountsDBMetricsBuilder == nil {
		accountsDBMutex.Lock()
		defer accountsDBMutex.Unlock()
		// Double-check after acquiring lock
		if AccountsDBMetricsBuilder == nil {
			AccountsDBMetricsBuilder = &DBPoolMetricsBuilder{
				poolType:     "accounts",
				functionName: "",
			}
			fmt.Println("AccountsDBMetricsBuilder initialized: ", AccountsDBMetricsBuilder)
		}
	}
	return AccountsDBMetricsBuilder
}

// NewMainDBMetricsBuilder creates a new builder for MainDB connection pool metrics
// Uses singleton pattern with thread-safe initialization
func NewMainDBMetricsBuilder() *DBPoolMetricsBuilder {
	if MainDBMetricsBuilder == nil {
		mainDBMutex.Lock()
		defer mainDBMutex.Unlock()
		// Double-check after acquiring lock
		if MainDBMetricsBuilder == nil {
			MainDBMetricsBuilder = &DBPoolMetricsBuilder{
				poolType:     "main",
				functionName: "",
			}
			fmt.Println("MainDBMetricsBuilder initialized: ", MainDBMetricsBuilder)
		}
	}
	return MainDBMetricsBuilder
}

// WithFunction sets the function name for tracking which function is using connections
// This enables per-function metrics for detailed analysis (separate from pool totals)
// NOTE: This is NOT thread-safe if multiple goroutines use the same builder instance.
// For thread-safety, create a new builder for each operation or use a mutex.
func (b *DBPoolMetricsBuilder) WithFunction(functionName string) *DBPoolMetricsBuilder {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.functionName = functionName
	return b
}

// SetTotal sets the total number of connections in the pool (pool-level metric only)
func (b *DBPoolMetricsBuilder) SetTotal(count int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.Set(float64(count))
	} else {
		MainDBConnectionPoolCount.Set(float64(count))
	}
	return b
}

// SetActive sets the number of active (in-use) connections (pool-level metric only)
func (b *DBPoolMetricsBuilder) SetActive(count int) *DBPoolMetricsBuilder {
	b.mu.Lock()
	functionName := b.functionName
	b.mu.Unlock()

	if b.poolType == "accounts" {
		if functionName != "" {
			AccountsDBConnectionPoolActive.WithLabelValues(functionName).Set(float64(count))
		} else {
			AccountsDBConnectionPoolActive.WithLabelValues("unknown").Set(float64(count))
		}
	} else {
		if functionName != "" {
			MainDBConnectionPoolActive.WithLabelValues(functionName).Set(float64(count))
		} else {
			MainDBConnectionPoolActive.WithLabelValues("unknown").Set(float64(count))
		}
	}
	return b
}

// SetIdle sets the number of idle (available) connections (pool-level metric only)
func (b *DBPoolMetricsBuilder) SetIdle(count int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.Set(float64(count))
	} else {
		MainDBConnectionPoolIdle.Set(float64(count))
	}
	return b
}

// SetAll sets all three metrics at once (total, active, idle) - pool-level only
func (b *DBPoolMetricsBuilder) SetAll(total, active, idle int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		UpdateAccountsDBConnectionPoolMetrics(total, active, idle)
	} else {
		UpdateMainDBConnectionPoolMetrics(total, active, idle)
	}
	return b
}

// IncrementTotal increments the total connection count by 1 (pool-level)
func (b *DBPoolMetricsBuilder) IncrementTotal() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.Inc()
	} else {
		MainDBConnectionPoolCount.Inc()
	}
	return b
}

// DecrementTotal decrements the total connection count by 1 (pool-level)
func (b *DBPoolMetricsBuilder) DecrementTotal() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.Dec()
	} else {
		MainDBConnectionPoolCount.Dec()
	}
	return b
}

// IncrementActive increments the active connection count by 1 (pool-level)
func (b *DBPoolMetricsBuilder) IncrementActive() *DBPoolMetricsBuilder {
	b.mu.Lock()
	functionName := b.functionName
	b.mu.Unlock()

	if b.poolType == "accounts" {
		if functionName != "" {
			AccountsDBConnectionPoolActive.WithLabelValues(functionName).Inc()
		} else {
			AccountsDBConnectionPoolActive.WithLabelValues("unknown").Inc()
		}
	} else {
		if functionName != "" {
			MainDBConnectionPoolActive.WithLabelValues(functionName).Inc()
		} else {
			MainDBConnectionPoolActive.WithLabelValues("unknown").Inc()
		}
	}
	return b
}

// DecrementActive decrements the active connection count by 1 (pool-level)
func (b *DBPoolMetricsBuilder) DecrementActive() *DBPoolMetricsBuilder {
	b.mu.Lock()
	functionName := b.functionName
	b.mu.Unlock()

	if b.poolType == "accounts" {
		if functionName != "" {
			AccountsDBConnectionPoolActive.WithLabelValues(functionName).Dec()
		} else {
			AccountsDBConnectionPoolActive.WithLabelValues("unknown").Dec()
		}
	} else {
		if functionName != "" {
			MainDBConnectionPoolActive.WithLabelValues(functionName).Dec()
		} else {
			MainDBConnectionPoolActive.WithLabelValues("unknown").Dec()
		}
	}
	return b
}

// IncrementIdle increments the idle connection count by 1 (pool-level)
func (b *DBPoolMetricsBuilder) IncrementIdle() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.Inc()
	} else {
		MainDBConnectionPoolIdle.Inc()
	}
	return b
}

// DecrementIdle decrements the idle connection count by 1 (pool-level)
func (b *DBPoolMetricsBuilder) DecrementIdle() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.Dec()
	} else {
		MainDBConnectionPoolIdle.Dec()
	}
	return b
}

// AddToTotal adds a specific value to the total connection count (pool-level)
func (b *DBPoolMetricsBuilder) AddToTotal(delta int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.Add(float64(delta))
	} else {
		MainDBConnectionPoolCount.Add(float64(delta))
	}
	return b
}

// AddToActive adds a specific value to the active connection count (pool-level)
func (b *DBPoolMetricsBuilder) AddToActive(delta int) *DBPoolMetricsBuilder {
	b.mu.Lock()
	functionName := b.functionName
	b.mu.Unlock()

	if b.poolType == "accounts" {
		if functionName != "" {
			AccountsDBConnectionPoolActive.WithLabelValues(functionName).Add(float64(delta))
		} else {
			AccountsDBConnectionPoolActive.WithLabelValues("unknown").Add(float64(delta))
		}
	} else {
		if functionName != "" {
			MainDBConnectionPoolActive.WithLabelValues(functionName).Add(float64(delta))
		} else {
			MainDBConnectionPoolActive.WithLabelValues("unknown").Add(float64(delta))
		}
	}
	return b
}

// AddToIdle adds a specific value to the idle connection count (pool-level)
func (b *DBPoolMetricsBuilder) AddToIdle(delta int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.Add(float64(delta))
	} else {
		MainDBConnectionPoolIdle.Add(float64(delta))
	}
	return b
}

// ConnectionTaken updates metrics when a connection is taken from the pool
// This increments active and decrements idle (pool-level)
// AND tracks per-function usage if function name is set
func (b *DBPoolMetricsBuilder) ConnectionTaken() *DBPoolMetricsBuilder {
	// Get function name atomically ONCE at the start to avoid race conditions
	b.mu.Lock()
	functionName := b.functionName
	b.mu.Unlock()

	// Update pool-level metrics using the captured functionName
	// This ensures we use the same functionName for both IncrementActive and per-function tracking
	if b.poolType == "accounts" {
		if functionName != "" {
			AccountsDBConnectionPoolActive.WithLabelValues(functionName).Inc()
		} else {
			AccountsDBConnectionPoolActive.WithLabelValues("unknown").Inc()
		}
		AccountsDBConnectionPoolIdle.Dec()
	} else {
		if functionName != "" {
			MainDBConnectionPoolActive.WithLabelValues(functionName).Inc()
		} else {
			MainDBConnectionPoolActive.WithLabelValues("unknown").Inc()
		}
		MainDBConnectionPoolIdle.Dec()
	}

	// Track per-function metrics if function name is provided
	if functionName != "" {
		if b.poolType == "accounts" {
			AccountsDBConnectionsByFunction.WithLabelValues(functionName).Inc()
			AccountsDBConnectionTakesTotal.WithLabelValues(functionName).Inc()
		} else {
			MainDBConnectionsByFunction.WithLabelValues(functionName).Inc()
			MainDBConnectionTakesTotal.WithLabelValues(functionName).Inc()
		}
	}

	return b
}

// ConnectionReturned updates metrics when a connection is returned to the pool
// This decrements active and increments idle (pool-level)
// AND tracks per-function usage if function name is set
func (b *DBPoolMetricsBuilder) ConnectionReturned() *DBPoolMetricsBuilder {
	// Get function name atomically ONCE at the start to avoid race conditions
	b.mu.Lock()
	functionName := b.functionName
	b.mu.Unlock()

	// Update pool-level metrics using the captured functionName
	// This ensures we use the same functionName for both DecrementActive and per-function tracking
	if b.poolType == "accounts" {
		if functionName != "" {
			AccountsDBConnectionPoolActive.WithLabelValues(functionName).Dec()
		} else {
			AccountsDBConnectionPoolActive.WithLabelValues("unknown").Dec()
		}
		AccountsDBConnectionPoolIdle.Inc()
	} else {
		if functionName != "" {
			MainDBConnectionPoolActive.WithLabelValues(functionName).Dec()
		} else {
			MainDBConnectionPoolActive.WithLabelValues("unknown").Dec()
		}
		MainDBConnectionPoolIdle.Inc()
	}

	// Track per-function metrics if function name is provided
	if functionName != "" {
		if b.poolType == "accounts" {
			AccountsDBConnectionsByFunction.WithLabelValues(functionName).Dec()
			AccountsDBConnectionReturnsTotal.WithLabelValues(functionName).Inc()
		} else {
			MainDBConnectionsByFunction.WithLabelValues(functionName).Dec()
			MainDBConnectionReturnsTotal.WithLabelValues(functionName).Inc()
		}
	}

	return b
}

// ConnectionCreated updates metrics when a new connection is created
// This increments both total and idle (pool-level)
func (b *DBPoolMetricsBuilder) ConnectionCreated() *DBPoolMetricsBuilder {
	return b.IncrementTotal().IncrementIdle()
}

// ConnectionRemoved updates metrics when a connection is removed from the pool
// This decrements both total and idle (assuming removed connection was idle)
func (b *DBPoolMetricsBuilder) ConnectionRemoved() *DBPoolMetricsBuilder {
	return b.DecrementTotal().DecrementIdle()
}

// ConnectionRemovedActive updates metrics when an active connection is removed
// This decrements both total and active
func (b *DBPoolMetricsBuilder) ConnectionRemovedActive() *DBPoolMetricsBuilder {
	return b.DecrementTotal().DecrementActive()
}

// Note: Prometheus Gauge metrics don't support reading values directly.
// To get current values, query the Prometheus metrics endpoint at /metrics
// or use the Grafana dashboard which reads from Prometheus.

// Convenience functions for direct usage without builder pattern

// SetAccountsDBPoolMetrics sets all AccountsDB connection pool metrics at once
func SetAccountsDBPoolMetrics(total, active, idle int) {
	NewAccountsDBMetricsBuilder().SetAll(total, active, idle)
}

// SetMainDBPoolMetrics sets all MainDB connection pool metrics at once
func SetMainDBPoolMetrics(total, active, idle int) {
	NewMainDBMetricsBuilder().SetAll(total, active, idle)
}

// IncrementAccountsDBPoolActive increments the active AccountsDB connection count
func IncrementAccountsDBPoolActive() {
	NewAccountsDBMetricsBuilder().IncrementActive()
}

// DecrementAccountsDBPoolActive decrements the active AccountsDB connection count
func DecrementAccountsDBPoolActive() {
	NewAccountsDBMetricsBuilder().DecrementActive()
}

// IncrementMainDBPoolActive increments the active MainDB connection count
func IncrementMainDBPoolActive() {
	NewMainDBMetricsBuilder().IncrementActive()
}

// DecrementMainDBPoolActive decrements the active MainDB connection count
func DecrementMainDBPoolActive() {
	NewMainDBMetricsBuilder().DecrementActive()
}

// New convenience functions with function name tracking

// IncrementAccountsDBPoolActiveWithFunction increments the active AccountsDB connection count for a specific function
func IncrementAccountsDBPoolActiveWithFunction(functionName string) {
	NewAccountsDBMetricsBuilder().WithFunction(functionName).IncrementActive()
}

// DecrementAccountsDBPoolActiveWithFunction decrements the active AccountsDB connection count for a specific function
func DecrementAccountsDBPoolActiveWithFunction(functionName string) {
	NewAccountsDBMetricsBuilder().WithFunction(functionName).DecrementActive()
}

// IncrementMainDBPoolActiveWithFunction increments the active MainDB connection count for a specific function
func IncrementMainDBPoolActiveWithFunction(functionName string) {
	NewMainDBMetricsBuilder().WithFunction(functionName).IncrementActive()
}

// DecrementMainDBPoolActiveWithFunction decrements the active MainDB connection count for a specific function
func DecrementMainDBPoolActiveWithFunction(functionName string) {
	NewMainDBMetricsBuilder().WithFunction(functionName).DecrementActive()
}

// SetAccountsDBPoolMetricsWithFunction sets all AccountsDB connection pool metrics for a specific function
func SetAccountsDBPoolMetricsWithFunction(functionName string, total, active, idle int) {
	NewAccountsDBMetricsBuilder().WithFunction(functionName).SetAll(total, active, idle)
}

// SetMainDBPoolMetricsWithFunction sets all MainDB connection pool metrics for a specific function
func SetMainDBPoolMetricsWithFunction(functionName string, total, active, idle int) {
	NewMainDBMetricsBuilder().WithFunction(functionName).SetAll(total, active, idle)
}
