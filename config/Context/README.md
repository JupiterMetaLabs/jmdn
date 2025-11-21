# GlobalContext Package

## Overview

The `GlobalContext` package provides a process-wide context management system that allows graceful shutdown of all components when the application receives termination signals (SIGINT, SIGTERM). It ensures that all child contexts are properly cancelled when the global context is shut down.

## Features

- **Process-wide context management**: Single global context shared across the entire application
- **Automatic signal handling**: Listens for SIGINT and SIGTERM signals for graceful shutdown
- **Child context support**: Create child contexts that automatically respect the global shutdown
- **Thread-safe**: Uses mutexes to protect concurrent access
- **Idempotent operations**: Safe to call Init/Get multiple times

## API Reference

### GetGlobalContext()

Returns a new `GlobalContext` instance. Multiple instances share the same underlying global context state.

```go
gc := GetGlobalContext()
```

### Init() context.Context

Initializes the global context if it hasn't been created yet. Sets up signal handlers for graceful shutdown. Returns the global context.

```go
ctx := gc.Init()
```

**Behavior:**
- If global context already exists and is active, returns the existing context
- If global context doesn't exist or was cancelled, creates a new one
- Sets up signal handlers (SIGINT, SIGTERM) on first initialization
- Thread-safe

### Get() context.Context

Returns the currently initialized global context. If no context exists, calls `Init()` automatically.

```go
ctx := gc.Get()
```

**Behavior:**
- Returns existing global context if available
- Automatically initializes if context doesn't exist
- Thread-safe

### NewChildContext() (context.Context, context.CancelFunc)

Creates a child context derived from the global context. The child context will be automatically cancelled when the global context is shut down.

```go
childCtx, cancel := gc.NewChildContext()
defer cancel() // Always defer cancel to avoid leaks
```

**Returns:**
- `context.Context`: The child context
- `context.CancelFunc`: Function to cancel the child context independently

**Behavior:**
- Child context is automatically cancelled when global context shuts down
- Can be cancelled independently without affecting global context
- Thread-safe

### Shutdown()

Cancels the global context and all its child contexts. Resets the global state so a new context can be initialized.

```go
gc.Shutdown()
```

**Behavior:**
- Cancels the global context
- All child contexts are automatically cancelled (cascade effect)
- Resets internal state
- Thread-safe

## Usage Examples

### Basic Usage

```go
package main

import (
    "context"
    "gossipnode/config/Context"
    "time"
)

func main() {
    // Get GlobalContext instance
    gc := Context.GetGlobalContext()
    
    // Initialize global context
    ctx := gc.Init()
    
    // Create a child context for a specific operation
    childCtx, cancel := gc.NewChildContext()
    defer cancel()
    
    // Use the child context
    doWork(childCtx)
    
    // Global context will be cancelled on SIGINT/SIGTERM
    <-ctx.Done()
}

func doWork(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return // Exit gracefully when context is cancelled
        default:
            // Do work
            time.Sleep(1 * time.Second)
        }
    }
}
```

### Child Context with Timeout

```go
gc := Context.GetGlobalContext()
gc.Init()

// Create child context
childCtx, cancel := gc.NewChildContext()
defer cancel()

// Add timeout to child context
timeoutCtx, timeoutCancel := context.WithTimeout(childCtx, 5*time.Second)
defer timeoutCancel()

// Use timeout context
select {
case <-timeoutCtx.Done():
    if timeoutCtx.Err() == context.DeadlineExceeded {
        // Timeout occurred
    } else if timeoutCtx.Err() == context.Canceled {
        // Parent was cancelled
    }
case <-time.After(10 * time.Second):
    // Work completed before timeout
}
```

### Multiple Child Contexts

```go
gc := Context.GetGlobalContext()
globalCtx := gc.Init()

// Create multiple child contexts for different components
dbCtx, dbCancel := gc.NewChildContext()
defer dbCancel()

apiCtx, apiCancel := gc.NewChildContext()
defer apiCancel()

workerCtx, workerCancel := gc.NewChildContext()
defer workerCancel()

// Each component can manage its own lifecycle
go runDatabase(dbCtx)
go runAPI(apiCtx)
go runWorkers(workerCtx)

// All will be cancelled when global context shuts down
<-globalCtx.Done()
```

### Graceful Shutdown Pattern

```go
gc := Context.GetGlobalContext()
ctx := gc.Init()

// Start components with child contexts
components := []struct{
    name string
    ctx  context.Context
    cancel context.CancelFunc
}{
    {"database", gc.NewChildContext()},
    {"api", gc.NewChildContext()},
    {"workers", gc.NewChildContext()},
}

// Start all components
for _, comp := range components {
    go runComponent(comp.name, comp.ctx)
}

// Wait for shutdown signal
<-ctx.Done()

// All child contexts are automatically cancelled
// Components should exit gracefully
```

## Best Practices

1. **Always defer cancel**: When creating child contexts, always defer the cancel function to avoid context leaks
   ```go
   childCtx, cancel := gc.NewChildContext()
   defer cancel()
   ```

2. **Initialize once**: Call `Init()` early in your application startup, or use `Get()` which initializes automatically

3. **Check context in loops**: Always check `ctx.Done()` in long-running operations
   ```go
   for {
       select {
       case <-ctx.Done():
           return
       default:
           // Do work
       }
   }
   ```

4. **Use child contexts for components**: Create separate child contexts for different components (database, API, workers) so they can be managed independently

5. **Respect cancellation**: Always respect context cancellation and exit gracefully

## Signal Handling

The package automatically sets up signal handlers for:
- **SIGINT** (Ctrl+C)
- **SIGTERM** (Termination signal)

When either signal is received:
1. The global context is cancelled via `Shutdown()`
2. All child contexts are automatically cancelled
3. Components should exit gracefully

## Thread Safety

All operations are thread-safe:
- Multiple goroutines can call `Init()`, `Get()`, `NewChildContext()` concurrently
- The underlying global state is protected by mutexes
- Child contexts can be created and cancelled from different goroutines

## Testing

See `GlobalContext_test.go` for comprehensive test examples including:
- Basic initialization and retrieval
- Child context creation
- Timeout handling
- Concurrent operations
- Shutdown behavior

## Architecture

```
GlobalContext (package-level state)
    ├── Child Context 1
    │   └── Timeout Context 1.1
    ├── Child Context 2
    │   └── Timeout Context 2.1
    └── Child Context 3
```

When `Shutdown()` is called or a signal is received:
- GlobalContext is cancelled
- All Child Contexts are cancelled (cascade)
- All Timeout Contexts are cancelled (cascade)

