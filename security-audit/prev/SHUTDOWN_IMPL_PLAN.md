# Production-Grade Shutdown Implementation Plan

## Executive Summary

This document provides a comprehensive, step-by-step plan to implement production-grade graceful shutdown for the JMDT Decentralized Network. The implementation follows industry best practices, ensures no resource leaks, and provides proper lifecycle management for all services.

**Target**: Zero resource leaks, proper cleanup order, local timeouts per service, and comprehensive test coverage.

---

## Current Status (2025-11-11)
- **No lifecycle infrastructure exists yet.** There is no `lifecycle` package, no coordinator, and no adapters in the repository today.
- **Signal handling remains uncompromised.** `main.go` still calls `os.Exit(1)` from the signal goroutine, so none of the defers in `main` execute during shutdown.
- **All ingress servers still run unmanaged.** `StartFacadeServer`, `StartWSServer`, `Block.Startserver`, `StartAPIServer`, `metrics.StartMetricsServer`, and every gRPC helper launch goroutines without returning handles or providing `Shutdown/GracefulStop` hooks.
- **Background workers leak.** The block poller, FastSync routines, heartbeat ticker, and Yggdrasil listener all rely on `context.Background()` or tickers without cancellation.
- Refer to the refreshed audits in `docs/SHUTDOWN_AUDIT.md` and `docs/SHUTDOWN_REVIEW.md`, together with the context baseline in `docs/CONTEXT_IMPACT.md`, for the detailed gap analysis that feeds this plan.

The remainder of this plan stays valid, but all phases are **still outstanding** and should now be scheduled based on the priorities above.

---

## Phase 1: Core Lifecycle Infrastructure

### 1.1 Create Lifecycle Package Structure

**Location**: `lifecycle/lifecycle.go`

**Purpose**: Define the core interface and coordinator for all stoppable services.

**Design Decisions**:
- Single `Stoppable` interface for all services (simplifies registration)
- `Coordinator` manages shutdown order (LIFO stack)
- Thread-safe registration with mutex
- Global timeout (30s) + local timeouts per service (via adapters)

**Implementation**:

```go
package lifecycle

import (
    "context"
    "fmt"
    "sync"
    "time"
    
    "github.com/rs/zerolog/log"
)

// Stoppable represents any resource that can be gracefully shut down
type Stoppable interface {
    Shutdown(ctx context.Context) error
    Name() string
}

// Coordinator manages graceful shutdown of all registered services
type Coordinator struct {
    services []Stoppable
    mu       sync.Mutex
    globalTimeout time.Duration
}

// NewCoordinator creates a new shutdown coordinator
func NewCoordinator(globalTimeout time.Duration) *Coordinator {
    return &Coordinator{
        services: make([]Stoppable, 0),
        globalTimeout: globalTimeout,
    }
}

// Register adds a service to the shutdown coordinator
// Services are shut down in reverse order (LIFO - Last In First Out)
func (c *Coordinator) Register(service Stoppable) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.services = append(c.services, service)
    log.Info().
        Str("service", service.Name()).
        Int("total_services", len(c.services)).
        Msg("Registered service for graceful shutdown")
}

// Shutdown gracefully shuts down all registered services in reverse order
func (c *Coordinator) Shutdown(ctx context.Context) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    
    if len(c.services) == 0 {
        log.Info().Msg("No services registered for shutdown")
        return nil
    }
    
    // Create shutdown context with global timeout
    shutdownCtx, cancel := context.WithTimeout(ctx, c.globalTimeout)
    defer cancel()
    
    log.Info().
        Int("count", len(c.services)).
        Dur("global_timeout", c.globalTimeout).
        Msg("Starting graceful shutdown of all services")
    
    var wg sync.WaitGroup
    errChan := make(chan error, len(c.services))
    
    // Shutdown services in reverse order (last registered first)
    // This ensures: Ingress (last) → Network → Workers → Persistence → Telemetry (first)
    for i := len(c.services) - 1; i >= 0; i-- {
        service := c.services[i]
        wg.Add(1)
        go func(s Stoppable) {
            defer wg.Done()
            log.Info().Str("service", s.Name()).Msg("Shutting down service")
            if err := s.Shutdown(shutdownCtx); err != nil {
                log.Error().
                    Err(err).
                    Str("service", s.Name()).
                    Msg("Error shutting down service")
                errChan <- fmt.Errorf("%s: %w", s.Name(), err)
            } else {
                log.Info().Str("service", s.Name()).Msg("Service shut down successfully")
            }
        }(service)
    }
    
    // Wait for all shutdowns to complete or timeout
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()
    
    select {
    case <-done:
        log.Info().Msg("All services shut down successfully")
    case <-shutdownCtx.Done():
        log.Warn().
            Err(shutdownCtx.Err()).
            Msg("Shutdown timeout exceeded, some services may not have shut down cleanly")
    }
    
    // Collect errors
    close(errChan)
    var errors []error
    for err := range errChan {
        errors = append(errors, err)
    }
    
    if len(errors) > 0 {
        return fmt.Errorf("shutdown errors (%d services failed): %v", len(errors), errors)
    }
    
    return nil
}

// ServiceCount returns the number of registered services (for testing/debugging)
func (c *Coordinator) ServiceCount() int {
    c.mu.Lock()
    defer c.mu.Unlock()
    return len(c.services)
}
```

**Testing Requirements**:
- Test registration order
- Test LIFO shutdown order
- Test timeout handling
- Test concurrent registration
- Test error collection

---

### 1.2 Create Lifecycle Adapters

**Location**: `lifecycle/adapters.go`

**Purpose**: Provide adapters for different service types with local timeouts.

**Design Decisions**:
- HTTP servers: 8s local timeout (enough for graceful drain)
- gRPC servers: 10s local timeout (graceful stop can take longer)
- Closers (DB pools, libp2p): 5s local timeout (simple close operations)
- Each adapter implements `Stoppable` interface

**Implementation**:

```go
package lifecycle

import (
    "context"
    "time"
    
    "net/http"
    "google.golang.org/grpc"
    
    "github.com/rs/zerolog/log"
)

// HTTPServerAdapter adapts an *http.Server to Stoppable with local timeout
type HTTPServerAdapter struct {
    Name     string
    Server   *http.Server
    LocalTO  time.Duration // Local timeout for this specific server
}

// NewHTTPServerAdapter creates a new HTTP server adapter
func NewHTTPServerAdapter(name string, server *http.Server, localTimeout time.Duration) *HTTPServerAdapter {
    return &HTTPServerAdapter{
        Name:    name,
        Server:  server,
        LocalTO: localTimeout,
    }
}

func (a *HTTPServerAdapter) Name() string {
    return a.Name
}

func (a *HTTPServerAdapter) Shutdown(ctx context.Context) error {
    if a.Server == nil {
        return nil
    }
    
    // Create local context with shorter timeout
    localCtx, cancel := context.WithTimeout(ctx, a.LocalTO)
    defer cancel()
    
    log.Info().
        Str("service", a.Name).
        Dur("local_timeout", a.LocalTO).
        Msg("Shutting down HTTP server")
    
    if err := a.Server.Shutdown(localCtx); err != nil {
        log.Error().
            Err(err).
            Str("service", a.Name).
            Msg("HTTP server shutdown error")
        return err
    }
    
    log.Info().Str("service", a.Name).Msg("HTTP server shut down successfully")
    return nil
}

// GRPCServerAdapter adapts a *grpc.Server to Stoppable with local timeout
type GRPCServerAdapter struct {
    Name     string
    Server   *grpc.Server
    LocalTO  time.Duration
}

// NewGRPCServerAdapter creates a new gRPC server adapter
func NewGRPCServerAdapter(name string, server *grpc.Server, localTimeout time.Duration) *GRPCServerAdapter {
    return &GRPCServerAdapter{
        Name:    name,
        Server:  server,
        LocalTO: localTimeout,
    }
}

func (a *GRPCServerAdapter) Name() string {
    return a.Name
}

func (a *GRPCServerAdapter) Shutdown(ctx context.Context) error {
    if a.Server == nil {
        return nil
    }
    
    // Create local context with shorter timeout
    localCtx, cancel := context.WithTimeout(ctx, a.LocalTO)
    defer cancel()
    
    log.Info().
        Str("service", a.Name).
        Dur("local_timeout", a.LocalTO).
        Msg("Shutting down gRPC server")
    
    // gRPC GracefulStop is blocking, so we need to run it in a goroutine
    stopped := make(chan struct{})
    go func() {
        a.Server.GracefulStop()
        close(stopped)
    }()
    
    select {
    case <-stopped:
        log.Info().Str("service", a.Name).Msg("gRPC server shut down gracefully")
        return nil
    case <-localCtx.Done():
        log.Warn().
            Str("service", a.Name).
            Msg("gRPC server shutdown timeout exceeded, forcing stop")
        a.Server.Stop()
        return localCtx.Err()
    case <-ctx.Done():
        // Parent context cancelled
        log.Warn().
            Str("service", a.Name).
            Msg("Global shutdown timeout exceeded, forcing gRPC server stop")
        a.Server.Stop()
        return ctx.Err()
    }
}

// CloserAdapter adapts a simple Close function to Stoppable
type CloserAdapter struct {
    Name    string
    CloseFn func() error
}

// NewCloserAdapter creates a new closer adapter
func NewCloserAdapter(name string, closeFn func() error) *CloserAdapter {
    return &CloserAdapter{
        Name:    name,
        CloseFn: closeFn,
    }
}

func (a *CloserAdapter) Name() string {
    return a.Name
}

func (a *CloserAdapter) Shutdown(ctx context.Context) error {
    if a.CloseFn == nil {
        return nil
    }
    
    log.Info().Str("service", a.Name).Msg("Closing resource")
    
    // Simple close operations should be fast, but we still respect context
    done := make(chan error, 1)
    go func() {
        done <- a.CloseFn()
    }()
    
    select {
    case err := <-done:
        if err != nil {
            log.Error().
                Err(err).
                Str("service", a.Name).
                Msg("Error closing resource")
            return err
        }
        log.Info().Str("service", a.Name).Msg("Resource closed successfully")
        return nil
    case <-ctx.Done():
        log.Warn().
            Str("service", a.Name).
            Msg("Close operation timed out")
        return ctx.Err()
    }
}
```

**Default Timeouts**:
- HTTP servers: `8 * time.Second`
- gRPC servers: `10 * time.Second`
- Closers (DB pools, libp2p): `5 * time.Second`

**Testing Requirements**:
- Test HTTP server graceful shutdown
- Test gRPC server graceful stop with timeout
- Test closer adapter with fast and slow close functions
- Test timeout behavior for each adapter

---

### 1.3 Unit Tests for Lifecycle Package

**Location**: `lifecycle/lifecycle_test.go`, `lifecycle/adapters_test.go`

**Coverage Requirements**:
- Coordinator registration order
- LIFO shutdown order verification
- Timeout handling
- Error collection
- Concurrent registration safety
- Adapter behavior with various timeouts

---

## Phase 2: Signal Handling and Main Flow

### 2.1 Fix Signal Handling

**Problem**: Current code uses `os.Exit(1)` in a goroutine, which skips defers and prevents proper cleanup.

**Solution**: Use `signal.NotifyContext()` and let `main()` return naturally.

**Current Code** (lines 538-548):
```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
go func() {
    <-sigCh
    fmt.Println("\nShutdown signal received, closing connections...")
    cancel() // Cancel the context
    time.Sleep(500 * time.Millisecond)
    os.Exit(1) // ❌ BAD: Skips defers
}()
```

**New Code**:
```go
// Create root context that will be cancelled on signal
rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

// Create shutdown coordinator
shutdownCoordinator := lifecycle.NewCoordinator(30 * time.Second)

// ... initialization code ...

// Block on context cancellation (signal received)
<-rootCtx.Done()

fmt.Println("\nShutdown signal received, starting graceful shutdown...")

// Perform graceful shutdown with timeout
shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
defer shutdownCancel()

if err := shutdownCoordinator.Shutdown(shutdownCtx); err != nil {
    log.Error().Err(err).Msg("Error during shutdown")
    // Exit with error code, but defers still run
    os.Exit(1)
}

fmt.Println("Shutdown complete")
// main() returns naturally, all defers execute
```

**Benefits**:
- All defers in `main()` execute (logger cleanup, etc.)
- Proper error propagation
- Clean exit code handling

---

### 2.2 Replace log.Fatal() with Error Returns

**Problem**: `log.Fatal()` calls `os.Exit()` immediately, preventing cleanup.

**Files to Update**:
- `main.go`: lines 552, 557, 612, 625

**Pattern**:
```go
// Before:
if err := initMainDBPool(...); err != nil {
    log.Fatal().Err(err).Msg("Failed to initialize main database pool")
}

// After:
if err := initMainDBPool(...); err != nil {
    log.Error().Err(err).Msg("Failed to initialize main database pool")
    return fmt.Errorf("failed to initialize main database pool: %w", err)
}
```

**Exception**: Bootstrap failures (TLS assets, etc.) can remain fatal as they occur before any resources are created.

---

## Phase 3: Server Creation Refactoring

### 3.1 Refactor StartFacadeServer

**Current** (lines 62-72):
```go
func StartFacadeServer(port int, chainID int) {
    go func() {
        // Server started in goroutine, no handle returned
        httpServer := rpc.NewHTTPServer(HTTPServer)
        httpServer.Serve(...)
    }()
}
```

**New**:
```go
func StartFacadeServer(port int, chainID int) (*http.Server, error) {
    HTTPServer := rpc.NewHandlers(Service.NewService(chainID))
    httpServer := rpc.NewHTTPServer(HTTPServer)
    
    // Store server reference in HTTPServer struct
    srv := &http.Server{
        Addr:              fmt.Sprintf("0.0.0.0:%d", port),
        Handler:           router, // Need to expose router from HTTPServer
        ReadHeaderTimeout: 10 * time.Second,
    }
    
    // Store server reference for shutdown
    httpServer.SetServer(srv)
    
    // Start server in goroutine
    go func() {
        log.Info().Int("port", port).Msg("Starting facade server")
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Error().Err(err).Msg("Facade server error")
        }
    }()
    
    return srv, nil
}
```

**Changes Required in `gETH/Facade/rpc/http_server.go`**:
- Add `server *http.Server` field to `HTTPServer` struct
- Add `SetServer(*http.Server)` method
- Modify `Serve()` to store server reference
- Add `Shutdown(ctx context.Context) error` method

---

### 3.2 Refactor StartWSServer

Similar pattern to `StartFacadeServer`:
- Return `*http.Server`
- Store server reference in `WSServer` struct
- Add `Shutdown()` method

---

### 3.3 Refactor StartAPIServer

**Current** (lines 377-388):
```go
func StartAPIServer(address string, enableExplorer bool) error {
    server, err := explorer.NewImmuDBServer(enableExplorer)
    // ...
    go explorer.StartBlockPoller(server, 7*time.Second) // Block poller started without coordination
    return server.Start(address)
}
```

**New**:
```go
func StartAPIServer(address string, enableExplorer bool, coordinator *lifecycle.Coordinator) (*explorer.ImmuDBServer, error) {
    server, err := explorer.NewImmuDBServer(enableExplorer)
    if err != nil {
        return nil, fmt.Errorf("failed to create ImmuDB API server: %w", err)
    }
    
    // Create block poller service with stop channel
    blockPollerService := lifecycle.NewBlockPollerService("block-poller", 7*time.Second)
    go explorer.StartBlockPoller(server, 7*time.Second, blockPollerService.StopChan())
    
    // Register block poller for shutdown
    if coordinator != nil {
        coordinator.Register(blockPollerService)
    }
    
    log.Info().Str("address", address).Msg("Starting ImmuDB API server")
    
    // Start server in goroutine and return handle
    go func() {
        if err := server.Start(address); err != nil {
            log.Error().Err(err).Msg("API server error")
        }
    }()
    
    return server, nil
}
```

**Changes Required**:
- `explorer.ImmuDBServer` needs `Shutdown(ctx)` method
- Block poller needs lifecycle integration
- Return server handle instead of starting synchronously

---

### 3.4 Refactor gRPC Server Creation

**Current**: Functions like `Block.StartGRPCServer()`, `gETH.StartGRPC()` start servers in goroutines without returning handles.

**New Pattern**: All gRPC server creation functions should:
1. Return `*grpc.Server` handle
2. Start server in goroutine internally
3. Return immediately

**Example**:
```go
func StartGRPCServer(port int, h host.Host, chainID int) (*grpc.Server, error) {
    // ... setup code ...
    
    grpcServer := grpc.NewServer(...)
    
    // Start in goroutine
    go func() {
        if err := grpcServer.Serve(lis); err != nil {
            log.Error().Err(err).Msg("gRPC server stopped")
        }
    }()
    
    return grpcServer, nil
}
```

---

### 3.5 Update main.go Registration

**Pattern for All Servers**:
```go
// Facade HTTP Server
if *gETHFacade > 0 {
    facadeSrv, err := StartFacadeServer(*gETHFacade, *chainID)
    if err != nil {
        log.Error().Err(err).Msg("Failed to start facade server")
    } else if facadeSrv != nil {
        shutdownCoordinator.Register(lifecycle.NewHTTPServerAdapter(
            "facade-http",
            facadeSrv,
            8*time.Second,
        ))
    }
}

// WebSocket Server
if *gETHWSServer > 0 {
    wsSrv, err := StartWSServer(*gETHWSServer, *chainID)
    if err != nil {
        log.Error().Err(err).Msg("Failed to start WS server")
    } else if wsSrv != nil {
        shutdownCoordinator.Register(lifecycle.NewHTTPServerAdapter(
            "websocket",
            wsSrv,
            8*time.Second,
        ))
    }
}

// Explorer API Server
if *apiPort > 0 {
    apiSrv, err := StartAPIServer(fmt.Sprintf(":%d", *apiPort), *enableExplorer, shutdownCoordinator)
    if err != nil {
        log.Error().Err(err).Msg("Failed to start API server")
    } else if apiSrv != nil {
        shutdownCoordinator.Register(lifecycle.NewHTTPServerAdapter(
            "explorer-api",
            apiSrv.GetHTTPServer(), // Need to expose HTTP server
            8*time.Second,
        ))
    }
}

// Block gRPC Server
if *blockgRPC > 0 {
    blockGRPC, err := Block.StartGRPCServer(*blockgRPC, n.Host, *chainID)
    if err != nil {
        log.Error().Err(err).Msg("Failed to start block gRPC server")
    } else if blockGRPC != nil {
        shutdownCoordinator.Register(lifecycle.NewGRPCServerAdapter(
            "block-grpc",
            blockGRPC,
            10*time.Second,
        ))
    }
}

// gETH gRPC Server
if *gETHgRPC > 0 {
    gethGRPC, err := gETH.StartGRPC(*gETHgRPC, *chainID)
    if err != nil {
        log.Error().Err(err).Msg("Failed to start gETH gRPC server")
    } else if gethGRPC != nil {
        shutdownCoordinator.Register(lifecycle.NewGRPCServerAdapter(
            "geth-grpc",
            gethGRPC,
            10*time.Second,
        ))
    }
}

// DID gRPC Server
if *DIDgRPC != "" {
    didGRPC, err := startDIDServer(n.Host, *DIDgRPC)
    if err != nil {
        log.Error().Err(err).Msg("Failed to start DID gRPC server")
    } else if didGRPC != nil {
        shutdownCoordinator.Register(lifecycle.NewGRPCServerAdapter(
            "did-grpc",
            didGRPC.GetGRPCServer(), // Need to expose gRPC server
            10*time.Second,
        ))
    }
}
```

**Key Principle**: No global variables storing server handles. All registration happens immediately after creation.

---

## Phase 4: Loop and Ticker Hygiene

### 4.1 Audit All Tickers

**Files to Check** (from grep results):
1. `explorer/utils.go` - Block poller ticker
2. `node/nodemanager.go` - Heartbeat ticker
3. `config/ConnectionPool.go` - Cleanup ticker
4. `AVC/BuddyNodes/MessagePassing/Service/nodeDiscoveryService.go` - Discovery/sync tickers
5. `gETH/Facade/Service/Service_WS.go` - Block poller ticker
6. `messaging/directMSG/directMSG.go` - Yggdrasil ticker
7. `logging/log.go` - Flush ticker
8. Others from grep results

**Pattern to Enforce**:
```go
func runPoller(ctx context.Context, tick time.Duration) {
    ticker := time.NewTicker(tick)
    defer ticker.Stop() // CRITICAL: Always defer Stop()
    
    for {
        select {
        case <-ctx.Done():
            log.Info().Msg("Poller stopped via context")
            return
        case <-ticker.C:
            // Do work
            pollOnce()
        }
    }
}
```

**Changes Required**:
- All ticker-creating functions must accept `context.Context`
- All tickers must have `defer ticker.Stop()`
- All loops must check `ctx.Done()`

---

### 4.2 Fix Block Poller

**File**: `explorer/utils.go`

**Current**: Ticker created without cleanup.

**Fix**: Add context parameter, defer ticker.Stop(), check ctx.Done().

---

### 4.3 Fix Node Manager Heartbeat

**File**: `node/nodemanager.go`

**Current**: Ticker exists but may not be properly stopped in all cases.

**Fix**: Ensure `Shutdown()` always stops ticker, add context cancellation.

---

### 4.4 Fix Connection Pool Cleanup Ticker

**File**: `config/ConnectionPool.go`

**Current**: Ticker exists in struct, need to verify cleanup.

**Fix**: Ensure `Close()` method stops ticker and cleanup goroutine.

---

## Phase 5: Resource Registration Order

### 5.1 Registration Order (LIFO - Last In, First Out)

**Shutdown Order** (reverse of registration):
1. **Ingress Services** (HTTP/WS/gRPC servers) - Stop accepting new requests
2. **Network Services** (PubSub, libp2p host) - Close connections cleanly
3. **Background Workers** (pollers, heartbeat, discovery) - Stop loops
4. **Persistence** (DB pools) - Close connections
5. **Telemetry** (metrics, logs) - Flush and close

**Registration Order in main.go** (register first, shutdown last):
1. Telemetry (register first)
2. Persistence (register second)
3. Background workers (register third)
4. Network (register fourth)
5. Ingress (register last)

### 5.2 Database Pool Registration

**Location**: After pool initialization (lines 550-559)

```go
// Initialize database connection pools FIRST
if err := initMainDBPool(*enableLoki, *immudbUsername, *immudbPassword); err != nil {
    log.Error().Err(err).Msg("Failed to initialize main database pool")
    return fmt.Errorf("failed to initialize main database pool: %w", err)
}

if err := initAccountsDBPool(*enableLoki, *immudbUsername, *immudbPassword); err != nil {
    log.Error().Err(err).Msg("Failed to initialize accounts database pool")
    return fmt.Errorf("failed to initialize accounts database pool: %w", err)
}

// Register pools for shutdown (after initialization)
if mainDBPool != nil {
    shutdownCoordinator.Register(lifecycle.NewCloserAdapter(
        "main-db-pool",
        func() error {
            return mainDBPool.Close()
        },
    ))
}

accountsPool := DB_OPs.GetAccountsDBPool()
if accountsPool != nil {
    shutdownCoordinator.Register(lifecycle.NewCloserAdapter(
        "accounts-db-pool",
        func() error {
            return accountsPool.Close()
        },
    ))
}
```

### 5.3 LibP2P Host Registration

**Location**: After node creation (line 573)

```go
n, err := node.NewNode()
if err != nil {
    return fmt.Errorf("failed to create node: %w", err)
}

// Register libp2p host for shutdown
shutdownCoordinator.Register(lifecycle.NewCloserAdapter(
    "libp2p-host",
    func() error {
        return n.Host.Close()
    },
))
```

### 5.4 PubSub Registration

**Location**: After PubSub initialization (line 590)

```go
globalPubSub, err := initPubSub(n)
if err != nil {
    log.Error().Err(err).Msg("Failed to initialize PubSub system")
} else {
    // Register PubSub for shutdown
    shutdownCoordinator.Register(lifecycle.NewCloserAdapter(
        "pubsub",
        func() error {
            // Need to add Close() method to StructGossipPubSub
            return globalPubSub.Close()
        },
    ))
}
```

### 5.5 Node Manager Registration

**Location**: After node manager initialization (line 680)

```go
nodeManager, err = node.NewNodeManagerWithLoki(n, *enableLoki)
if err != nil {
    return fmt.Errorf("failed to initialize node manager: %w", err)
}

nodeManager.StartHeartbeat(*heartbeatInterval)

// Register node manager for shutdown
shutdownCoordinator.Register(lifecycle.NewCloserAdapter(
    "node-manager",
    func() error {
        nodeManager.Shutdown()
        return nil
    },
))
```

### 5.6 Metrics Server Registration

**Location**: After metrics server start (line 676)

```go
metricsSrv := metrics.StartMetricsServer(metricsAddr)
if metricsSrv != nil {
    shutdownCoordinator.Register(lifecycle.NewHTTPServerAdapter(
        "metrics",
        metricsSrv,
        8*time.Second,
    ))
}
```

**Requirement**: `metrics.StartMetricsServer()` must return `*http.Server`.

---

## Phase 6: Testing

### 6.1 Integration Test: Shutdown Leaks

**Location**: `lifecycle/shutdown_test.go`

**Requirements**:
- Use `go.uber.org/goleak` to detect goroutine leaks
- Start minimal set of services
- Trigger shutdown
- Verify no goroutines leaked

**Implementation**:
```go
// +build integration

package lifecycle_test

import (
    "context"
    "testing"
    "time"
    
    "go.uber.org/goleak"
)

func TestShutdown_NoLeaks(t *testing.T) {
    defer goleak.VerifyNone(t)
    
    coord := NewCoordinator(5 * time.Second)
    
    // Register minimal services
    // ... setup ...
    
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    
    if err := coord.Shutdown(ctx); err != nil {
        t.Fatalf("Shutdown failed: %v", err)
    }
    
    // goleak will detect any leaked goroutines
}
```

### 6.2 Port Reuse Test

**Location**: `lifecycle/port_test.go`

**Purpose**: Verify sockets are released after shutdown.

**Implementation**:
```go
func TestShutdown_PortsReusable(t *testing.T) {
    // Start server on port
    srv := &http.Server{Addr: ":9999"}
    go srv.ListenAndServe()
    time.Sleep(100 * time.Millisecond)
    
    // Shutdown
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    srv.Shutdown(ctx)
    time.Sleep(100 * time.Millisecond)
    
    // Try to bind on same port - should succeed
    lis, err := net.Listen("tcp", ":9999")
    if err != nil {
        t.Fatalf("Port not released: %v", err)
    }
    lis.Close()
}
```

### 6.3 CI Checks

**Location**: `.github/workflows/shutdown-checks.yml` or similar

**Checks**:
1. Grep for `os.Exit` outside bootstrap code
2. Grep for `log.Fatal` outside bootstrap code
3. Run integration tests
4. Run port reuse tests

---

## Implementation Checklist

### Phase 1: Core Infrastructure
- [ ] Create `lifecycle/lifecycle.go` with Stoppable interface and Coordinator
- [ ] Create `lifecycle/adapters.go` with HTTP, gRPC, and Closer adapters
- [ ] Write unit tests for lifecycle package
- [ ] Write unit tests for adapters

### Phase 2: Signal Handling
- [ ] Replace signal goroutine with `signal.NotifyContext()`
- [ ] Update main() to block on context cancellation
- [ ] Replace `log.Fatal()` with error returns in initialization
- [ ] Ensure all defers execute on shutdown

### Phase 3: Server Refactoring
- [ ] Refactor `StartFacadeServer` to return `*http.Server`
- [ ] Update `HTTPServer` to store server reference
- [ ] Refactor `StartWSServer` to return `*http.Server`
- [ ] Update `WSServer` to store server reference
- [ ] Refactor `StartAPIServer` to return server handle
- [ ] Update `ImmuDBServer` to expose `*http.Server`
- [ ] Refactor all gRPC server creation functions
- [ ] Update main.go to register all servers directly

### Phase 4: Loop/Ticker Hygiene
- [ ] Audit all `time.NewTicker` instances
- [ ] Add `defer ticker.Stop()` to all tickers
- [ ] Add `ctx.Done()` checks to all loops
- [ ] Update functions to accept `context.Context` parameter

### Phase 5: Resource Registration
- [ ] Register DB pools after initialization
- [ ] Register libp2p host
- [ ] Register PubSub
- [ ] Register NodeManager
- [ ] Register metrics server
- [ ] Verify registration order (LIFO)

### Phase 6: Testing
- [ ] Create integration test for shutdown leaks
- [ ] Create port reuse test
- [ ] Add CI checks for `os.Exit`/`log.Fatal`
- [ ] Run full test suite

---

## Success Criteria

1. **Zero Resource Leaks**: All goroutines, tickers, and connections properly cleaned up
2. **Proper Shutdown Order**: Ingress → Network → Workers → Persistence → Telemetry
3. **Local Timeouts**: Each service has appropriate timeout (HTTP 8s, gRPC 10s, Closers 5s)
4. **No Hard Exits**: No `os.Exit()` in signal handlers or after initialization
5. **Test Coverage**: Integration tests verify no leaks and port reuse
6. **CI Validation**: Automated checks prevent regressions

---

## Notes

- This implementation maintains backward compatibility where possible
- All changes are incremental and testable
- Error handling is comprehensive but non-fatal during shutdown
- Logging is extensive for debugging production issues

---

## Timeline Estimate

- Phase 1: 2-3 hours (core infrastructure + tests)
- Phase 2: 1 hour (signal handling)
- Phase 3: 4-5 hours (server refactoring)
- Phase 4: 3-4 hours (loop/ticker fixes)
- Phase 5: 2 hours (registration)
- Phase 6: 2-3 hours (testing)

**Total**: ~14-18 hours of focused development

---

End of Implementation Plan

