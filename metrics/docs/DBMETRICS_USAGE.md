# Database Connection Pool Metrics - Usage Guide

This guide explains how to use the connection pool metrics builder to track and monitor database connections in your application.

## Table of Contents
1. [Overview](#overview)
2. [Quick Start](#quick-start)
3. [Basic Usage](#basic-usage)
4. [Function Tracking](#function-tracking)
5. [Common Patterns](#common-patterns)
6. [Grafana Dashboard](#grafana-dashboard)
7. [Best Practices](#best-practices)

## Overview

The connection pool metrics system allows you to:
- Track total, active, and idle connections for both AccountsDB and MainDB
- Monitor which functions are using connections
- View real-time metrics in Grafana dashboards
- Automatically update metrics when connections are taken/returned

## Quick Start

### 1. Initialize Pool Metrics (on startup)

```go
// In your initialization code (e.g., main.go)
poolCfg := config.DefaultConnectionPoolConfig()

// Initialize AccountsDB pool
err := DB_OPs.InitAccountsPool()
if err != nil {
    log.Fatal(err)
}

// Initialize MainDB pool
err = DB_OPs.InitMainDBPool(poolCfg)
if err != nil {
    log.Fatal(err)
}
```

### 2. Track Connection Usage in Your Functions

```go
// Example: In a function that uses database connections
func GetUserByID(userID string) (*User, error) {
    // Get connection with function tracking
    conn, err := DB_OPs.GetAccountsConnections(context.Background())
    if err != nil {
        return nil, err
    }
    defer DB_OPs.PutAccountsConnection(conn)
    
    // Track that this function took a connection
    metrics.NewAccountsDBMetricsBuilder().
        WithFunction("GetUserByID").
        ConnectionTaken()
    
    // ... use connection ...
    
    // Track that this function returned a connection
    metrics.NewAccountsDBMetricsBuilder().
        WithFunction("GetUserByID").
        ConnectionReturned()
    
    return user, nil
}
```

## Basic Usage

### Setting Initial Pool Count

When initializing the pool, set the total connection count:

```go
// In InitAccountsPool or InitMainDBPool
poolCfg := config.DefaultConnectionPoolConfig()
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("InitAccountsPool").
    SetTotal(poolCfg.MinConnections)
```

### Tracking Connection Taken

When a connection is taken from the pool:

```go
// Method 1: Using builder pattern
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUserByID").
    ConnectionTaken()  // Increments active, decrements idle

// Method 2: Using convenience function
metrics.IncrementAccountsDBPoolActiveWithFunction("GetUserByID")
```

### Tracking Connection Returned

When a connection is returned to the pool:

```go
// Method 1: Using builder pattern
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUserByID").
    ConnectionReturned()  // Decrements active, increments idle

// Method 2: Using convenience function
metrics.DecrementAccountsDBPoolActiveWithFunction("GetUserByID")
```

### Setting All Metrics at Once

When you know the exact state of the pool:

```go
// Set total=10, active=3, idle=7
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("PoolHealthCheck").
    SetAll(10, 3, 7)

// Or using convenience function
metrics.SetAccountsDBPoolMetricsWithFunction("PoolHealthCheck", 10, 3, 7)
```

## Function Tracking

### Why Track Functions?

Tracking which function uses connections helps you:
- Identify functions that hold connections too long
- Find functions that leak connections
- Optimize connection usage patterns
- Debug connection pool exhaustion

### How to Track Functions

Always use `WithFunction()` before any metric operation:

```go
// ✅ Good: Track function name
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUserByID").
    ConnectionTaken()

// ❌ Bad: No function tracking
metrics.NewAccountsDBMetricsBuilder().
    ConnectionTaken()  // Will use "unknown" as function name
```

### Example: Complete Function with Tracking

```go
func CreateOrder(order *Order) error {
    // Get connection
    conn, err := DB_OPs.GetMainDBConnection(context.Background())
    if err != nil {
        return err
    }
    defer DB_OPs.PutMainDBConnection(conn)
    
    // Track connection taken
    metrics.NewMainDBMetricsBuilder().
        WithFunction("CreateOrder").
        ConnectionTaken()
    
    // Do database work
    err = DB_OPs.Create(conn, orderKey, order)
    if err != nil {
        // Still track return even on error
        metrics.NewMainDBMetricsBuilder().
            WithFunction("CreateOrder").
            ConnectionReturned()
        return err
    }
    
    // Track connection returned
    metrics.NewMainDBMetricsBuilder().
        WithFunction("CreateOrder").
        ConnectionReturned()
    
    return nil
}
```

## Common Patterns

### Pattern 1: Simple Get/Put with Tracking

```go
func GetAccount(address string) (*Account, error) {
    conn, err := DB_OPs.GetAccountsConnections(context.Background())
    if err != nil {
        return nil, err
    }
    defer func() {
        DB_OPs.PutAccountsConnection(conn)
        // Track return in defer
        metrics.NewAccountsDBMetricsBuilder().
            WithFunction("GetAccount").
            ConnectionReturned()
    }()
    
    // Track taken
    metrics.NewAccountsDBMetricsBuilder().
        WithFunction("GetAccount").
        ConnectionTaken()
    
    // Use connection
    account, err := DB_OPs.GetAccountByDID(conn, address)
    return account, err
}
```

### Pattern 2: Using Auto-Return with Tracking

```go
func UpdateTransaction(txHash string, data interface{}) error {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    // Get connection with auto-return
    conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
    if err != nil {
        return err
    }
    
    // Track taken (auto-return will handle the return tracking)
    metrics.NewMainDBMetricsBuilder().
        WithFunction("UpdateTransaction").
        ConnectionTaken()
    
    // Use connection - it will auto-return when context is done
    err = DB_OPs.Update(conn, txHash, data)
    
    // Track returned manually (or let auto-return handle it)
    metrics.NewMainDBMetricsBuilder().
        WithFunction("UpdateTransaction").
        ConnectionReturned()
    
    return err
}
```

### Pattern 3: Batch Operations

```go
func BatchCreateAccounts(accounts []Account) error {
    conn, err := DB_OPs.GetAccountsConnections(context.Background())
    if err != nil {
        return err
    }
    defer DB_OPs.PutAccountsConnection(conn)
    
    // Track taken once
    metrics.NewAccountsDBMetricsBuilder().
        WithFunction("BatchCreateAccounts").
        ConnectionTaken()
    
    // Process all accounts with same connection
    for _, account := range accounts {
        err := DB_OPs.Create(conn, account.Key, account)
        if err != nil {
            return err
        }
    }
    
    // Track returned once
    metrics.NewAccountsDBMetricsBuilder().
        WithFunction("BatchCreateAccounts").
        ConnectionReturned()
    
    return nil
}
```

### Pattern 4: Connection Pool Health Check

```go
func CheckPoolHealth() {
    // Get current pool state
    accountsPool := DB_OPs.GetAccountsPool() // Assuming this exists
    if accountsPool != nil {
        total := accountsPool.GetPoolSize()
        active := accountsPool.GetActiveConnections()
        idle := accountsPool.GetIdleConnections()
        
        // Update metrics with actual state
        metrics.NewAccountsDBMetricsBuilder().
            WithFunction("PoolHealthCheck").
            SetAll(total, active, idle)
    }
}
```

## Grafana Dashboard

### Viewing Metrics

The Grafana dashboard (`DBConnection.json`) shows:
- **Total Pool Count**: Total connections in the pool
- **Active Connections**: Connections currently in use
- **Idle Connections**: Connections available for use

### Querying by Function

In Grafana, you can query metrics by function:

```promql
# All active connections
p2p_accounts_db_connection_pool_active

# Active connections for specific function
p2p_accounts_db_connection_pool_active{function="GetUserByID"}

# Sum of all active connections across all functions
sum(p2p_accounts_db_connection_pool_active)

# Active connections grouped by function
sum by (function) (p2p_accounts_db_connection_pool_active)
```

### Dashboard Panels

The dashboard includes:
1. **Stat Panels**: Current values for total, active, idle
2. **Time Series Graphs**: Historical trends over time
3. **Comparison Panels**: Side-by-side comparison of AccountsDB vs MainDB

## Best Practices

### 1. Always Track Function Names

```go
// ✅ Good
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUserByID").
    ConnectionTaken()

// ❌ Bad - uses "unknown" as function name
metrics.NewAccountsDBMetricsBuilder().
    ConnectionTaken()
```

### 2. Track Both Taken and Returned

```go
// ✅ Good - tracks both
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUser").
    ConnectionTaken()
// ... use connection ...
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUser").
    ConnectionReturned()

// ❌ Bad - only tracks taken
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUser").
    ConnectionTaken()
// ... use connection ...
// Missing ConnectionReturned()
```

### 3. Use Defer for Guaranteed Tracking

```go
// ✅ Good - guaranteed return tracking
conn, err := DB_OPs.GetAccountsConnections(ctx)
if err != nil {
    return err
}
defer func() {
    DB_OPs.PutAccountsConnection(conn)
    metrics.NewAccountsDBMetricsBuilder().
        WithFunction("GetUser").
        ConnectionReturned()
}()

metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUser").
    ConnectionTaken()
```

### 4. Use Consistent Function Names

```go
// ✅ Good - consistent naming
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("DB_OPs.GetUserByID").
    ConnectionTaken()

// ❌ Bad - inconsistent naming
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("getUser").
    ConnectionTaken()
// Later...
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("GetUser").
    ConnectionTaken()
```

### 5. Update Metrics After Pool State Changes

```go
// After creating a new connection
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("PoolManager").
    ConnectionCreated()  // total++, idle++

// After removing a connection
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("PoolManager").
    ConnectionRemoved()  // total--, idle--
```

## Complete Example

Here's a complete example showing best practices:

```go
package main

import (
    "context"
    "gossipnode/DB_OPs"
    "gossipnode/metrics"
)

func GetUserAccount(address string) (*Account, error) {
    // Get connection
    conn, err := DB_OPs.GetAccountsConnections(context.Background())
    if err != nil {
        return nil, err
    }
    
    // Track connection taken
    metrics.NewAccountsDBMetricsBuilder().
        WithFunction("GetUserAccount").
        ConnectionTaken()
    
    // Ensure connection is returned and tracked
    defer func() {
        DB_OPs.PutAccountsConnection(conn)
        metrics.NewAccountsDBMetricsBuilder().
            WithFunction("GetUserAccount").
            ConnectionReturned()
    }()
    
    // Use connection
    account, err := DB_OPs.GetAccountByDID(conn, address)
    if err != nil {
        return nil, err
    }
    
    return account, nil
}

func CreateTransaction(tx *Transaction) error {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    // Get connection with auto-return
    conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
    if err != nil {
        return err
    }
    
    // Track taken
    metrics.NewMainDBMetricsBuilder().
        WithFunction("CreateTransaction").
        ConnectionTaken()
    
    // Use connection
    err = DB_OPs.Create(conn, tx.Hash, tx)
    if err != nil {
        // Track return on error
        metrics.NewMainDBMetricsBuilder().
            WithFunction("CreateTransaction").
            ConnectionReturned()
        return err
    }
    
    // Track return on success
    // Note: Auto-return will also handle this, but manual tracking is explicit
    metrics.NewMainDBMetricsBuilder().
        WithFunction("CreateTransaction").
        ConnectionReturned()
    
    return nil
}
```

## Troubleshooting

### Metrics Not Showing in Grafana

1. **Check Prometheus is scraping**: Verify `/metrics` endpoint is accessible
2. **Check metric names**: Ensure they match the dashboard queries
3. **Check labels**: If using function labels, ensure queries include label filters

### Metrics Showing "unknown" Function

This happens when you don't call `WithFunction()`:

```go
// This will use "unknown" as function name
metrics.NewAccountsDBMetricsBuilder().ConnectionTaken()

// Fix: Add WithFunction()
metrics.NewAccountsDBMetricsBuilder().
    WithFunction("YourFunctionName").
    ConnectionTaken()
```

### Connection Counts Don't Match

If metrics don't match actual pool state:
1. Use `SetAll()` to sync metrics with actual pool state
2. Ensure you're tracking both `ConnectionTaken()` and `ConnectionReturned()`
3. Check for connection leaks (connections not being returned)

## Summary

- **Always use `WithFunction()`** to track which function uses connections
- **Track both taken and returned** for accurate metrics
- **Use defer** to ensure return tracking even on errors
- **Use consistent function names** for better dashboard filtering
- **Update metrics after pool state changes** (create/remove connections)

For more examples, see the test files in `DB_OPs/Tests/`.

