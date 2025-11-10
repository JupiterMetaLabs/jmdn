package metrics

import (
	"fmt"
	"reflect"
)

// DBPoolMetricsBuilder provides a fluent builder interface for updating database connection pool metrics.
// All changes are immediately reflected in Prometheus metrics and will appear on the Grafana dashboard.
//
// Usage Examples:
//
//	// Set all metrics at once with function tracking
//	metrics.NewAccountsDBMetricsBuilder().
//		WithFunction("GetUserByID").
//		SetAll(10, 3, 7) // total=10, active=3, idle=7
//
//	// Chain operations with function name
//	metrics.NewMainDBMetricsBuilder().
//		WithFunction("CreateOrder").
//		SetTotal(20).
//		SetActive(5).
//		SetIdle(15)
//
//	// Track connection usage per function
//	metrics.NewAccountsDBMetricsBuilder().
//		WithFunction("ListAccounts").
//		IncrementActive().  // Connection taken
//		DecrementIdle()
//
//	// Convenience methods for common operations
//	metrics.NewMainDBMetricsBuilder().
//		WithFunction("UpdateTransaction").
//		ConnectionTaken()    // active++, idle--
//
//	metrics.NewMainDBMetricsBuilder().
//		WithFunction("UpdateTransaction").
//		ConnectionReturned() // active--, idle++
//
//	// Direct convenience functions
//	metrics.SetAccountsDBPoolMetrics(10, 3, 7)
//	metrics.IncrementAccountsDBPoolActiveWithFunction("GetUser")
//	metrics.DecrementMainDBPoolActiveWithFunction("CreateOrder")
type DBPoolMetricsBuilder struct {
	poolType     string // "accounts" or "main"
	functionName string // name of the function using the connection
}

// NewAccountsDBMetricsBuilder creates a new builder for AccountsDB connection pool metrics
func NewAccountsDBMetricsBuilder() *DBPoolMetricsBuilder {
	return &DBPoolMetricsBuilder{
		poolType:     "accounts",
		functionName: "unknown",
	}
}

// NewMainDBMetricsBuilder creates a new builder for MainDB connection pool metrics
func NewMainDBMetricsBuilder() *DBPoolMetricsBuilder {
	return &DBPoolMetricsBuilder{
		poolType:     "main",
		functionName: "unknown",
	}
}

// WithFunction sets the function name for tracking which function is using connections
// This enables per-function metrics in Prometheus/Grafana dashboards
func (b *DBPoolMetricsBuilder) WithFunction(functionName string) *DBPoolMetricsBuilder {
	b.functionName = functionName
	return b
}

// SetTotal sets the total number of connections in the pool
func (b *DBPoolMetricsBuilder) SetTotal(count int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.WithLabelValues(b.functionName).Set(float64(count))
	} else {
		MainDBConnectionPoolCount.WithLabelValues(b.functionName).Set(float64(count))
	}
	return b
}

// SetActive sets the number of active (in-use) connections
func (b *DBPoolMetricsBuilder) SetActive(count int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolActive.WithLabelValues(b.functionName).Set(float64(count))
	} else {
		MainDBConnectionPoolActive.WithLabelValues(b.functionName).Set(float64(count))
	}
	return b
}

// SetIdle sets the number of idle (available) connections
func (b *DBPoolMetricsBuilder) SetIdle(count int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.WithLabelValues(b.functionName).Set(float64(count))
	} else {
		MainDBConnectionPoolIdle.WithLabelValues(b.functionName).Set(float64(count))
	}
	return b
}

// SetAll sets all three metrics at once (total, active, idle)
func (b *DBPoolMetricsBuilder) SetAll(total, active, idle int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		UpdateAccountsDBConnectionPoolMetricsWithFunction(b.functionName, total, active, idle)
	} else {
		UpdateMainDBConnectionPoolMetricsWithFunction(b.functionName, total, active, idle)
	}
	return b
}

// IncrementTotal increments the total connection count by 1
func (b *DBPoolMetricsBuilder) IncrementTotal() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.WithLabelValues(b.functionName).Inc()
	} else {
		MainDBConnectionPoolCount.WithLabelValues(b.functionName).Inc()
	}
	return b
}

// DecrementTotal decrements the total connection count by 1
func (b *DBPoolMetricsBuilder) DecrementTotal() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.WithLabelValues(b.functionName).Dec()
	} else {
		MainDBConnectionPoolCount.WithLabelValues(b.functionName).Dec()
	}
	return b
}

// IncrementActive increments the active connection count by 1
func (b *DBPoolMetricsBuilder) IncrementActive() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolActive.WithLabelValues(b.functionName).Inc()
	} else {
		MainDBConnectionPoolActive.WithLabelValues(b.functionName).Inc()
	}
	return b
}

// DecrementActive decrements the active connection count by 1
func (b *DBPoolMetricsBuilder) DecrementActive() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolActive.WithLabelValues(b.functionName).Dec()
	} else {
		MainDBConnectionPoolActive.WithLabelValues(b.functionName).Dec()
	}
	return b
}

// IncrementIdle increments the idle connection count by 1
func (b *DBPoolMetricsBuilder) IncrementIdle() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.WithLabelValues(b.functionName).Inc()
	} else {
		MainDBConnectionPoolIdle.WithLabelValues(b.functionName).Inc()
	}
	return b
}

// DecrementIdle decrements the idle connection count by 1
func (b *DBPoolMetricsBuilder) DecrementIdle() *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.WithLabelValues(b.functionName).Dec()
	} else {
		MainDBConnectionPoolIdle.WithLabelValues(b.functionName).Dec()
	}
	return b
}

// AddToTotal adds a specific value to the total connection count (can be positive or negative)
func (b *DBPoolMetricsBuilder) AddToTotal(delta int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolCount.WithLabelValues(b.functionName).Add(float64(delta))
	} else {
		MainDBConnectionPoolCount.WithLabelValues(b.functionName).Add(float64(delta))
	}
	return b
}

// AddToActive adds a specific value to the active connection count
func (b *DBPoolMetricsBuilder) AddToActive(delta int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolActive.WithLabelValues(b.functionName).Add(float64(delta))
	} else {
		MainDBConnectionPoolActive.WithLabelValues(b.functionName).Add(float64(delta))
	}
	return b
}

// AddToIdle adds a specific value to the idle connection count
func (b *DBPoolMetricsBuilder) AddToIdle(delta int) *DBPoolMetricsBuilder {
	if b.poolType == "accounts" {
		AccountsDBConnectionPoolIdle.WithLabelValues(b.functionName).Add(float64(delta))
	} else {
		MainDBConnectionPoolIdle.WithLabelValues(b.functionName).Add(float64(delta))
	}
	return b
}

// ConnectionTaken updates metrics when a connection is taken from the pool
// This increments active and decrements idle
func (b *DBPoolMetricsBuilder) ConnectionTaken() *DBPoolMetricsBuilder {
	return b.IncrementActive().DecrementIdle()
}

// ConnectionReturned updates metrics when a connection is returned to the pool
// This decrements active and increments idle
func (b *DBPoolMetricsBuilder) ConnectionReturned() *DBPoolMetricsBuilder {
	return b.DecrementActive().IncrementIdle()
}

// ConnectionCreated updates metrics when a new connection is created
// This increments both total and idle
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

// ExecuteBuilderMethodByName executes a builder method by name using reflection
// methodName: name of the method to call (e.g., "SetTotal", "IncrementActive", "ConnectionTaken")
// poolType: "accounts" or "main"
// args: optional arguments for methods that take parameters (e.g., SetTotal(10))
//
// Example usage:
//
//	ExecuteBuilderMethodByName("SetTotal", "main", 20)
//	ExecuteBuilderMethodByName("IncrementActive", "accounts")
//	ExecuteBuilderMethodByName("ConnectionTaken", "main")
func ExecuteBuilderMethodByName(methodName string, poolType string, args ...interface{}) error {
	// Create the appropriate builder
	var builder *DBPoolMetricsBuilder
	if poolType == "accounts" {
		builder = NewAccountsDBMetricsBuilder()
	} else if poolType == "main" {
		builder = NewMainDBMetricsBuilder()
	} else {
		return fmt.Errorf("invalid poolType: %s (must be 'accounts' or 'main')", poolType)
	}

	// Use reflection to get the method by name
	method := reflect.ValueOf(builder).MethodByName(methodName)
	if !method.IsValid() {
		return fmt.Errorf("method '%s' not found on DBPoolMetricsBuilder", methodName)
	}

	// Convert args to reflect.Value slice
	argValues := make([]reflect.Value, len(args))
	for i, arg := range args {
		argValues[i] = reflect.ValueOf(arg)
	}

	// Call the method
	results := method.Call(argValues)

	// Check if method returned an error
	if len(results) > 0 {
		lastResult := results[len(results)-1]
		if lastResult.Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			if !lastResult.IsNil() {
				return lastResult.Interface().(error)
			}
		}
	}

	return nil
}
