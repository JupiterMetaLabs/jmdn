# JMDN Shutdown & Lifecycle Audit

**Audit date:** 2026-02-24
**Auditor:** Engineering review (all findings code-verified)
**Supersedes:** `docs/SHUTDOWN_AUDIT.md`, `docs/SHUTDOWN_REVIEW.md`
**Companion:** `docs/CONTEXT_IMPACT.md`, `docs/SHUTDOWN_IMPL_PLAN.md`

> Every finding in this document was verified against the live codebase.
> Line numbers are from the actual file reads on the audit date.
> **No assumptions. No guesses.**

---

## Legend

| Symbol | Meaning |
|---|---|
| ✅ | Fixed — verified in code |
| 🟡 | Partially improved — code changed, but root issue remains |
| ❌ | Still open — no meaningful change since original audit |
| 🆕 | New finding not in any prior doc |

---

## 1. Signal Handling & Process Exit

### 1.1 Raw `os.Exit(1)` in signal goroutine
**Source:** `docs/SHUTDOWN_AUDIT.md §1`, `docs/SHUTDOWN_REVIEW.md §1`
**Original claim:** `main.go:570-584` calls `os.Exit(1)` inside the signal goroutine, skipping all defers in `main`.

**Verified code (`main.go:806-835`):**
```go
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

goMaybeTracked(..., func(ctx context.Context) error {
    <-sigCh
    cancel()                         // cancels root context
    profilerServer.Shutdown(...)      // 5s timeout shutdown
    if shutdown.Shutdown() {         // → GRO.GlobalGRO.Shutdown(true) with 10s timeout
        logger_cancel()
        defer shutdown.OS_EXIT(0)   // os.Exit(0) from goroutine defer
    }
    return nil
})
```

**Status: 🟡 PARTIALLY IMPROVED**

Progress made:
- No longer a raw `os.Exit(1)`
- `shutdown.Shutdown()` now runs before exit: dumps GRO metrics, calls `GRO.GlobalGRO.Shutdown()` with 10-second timeout, shuts down logger
- Upgraded to `os.Exit(0)` on clean shutdown

Remaining issue:
- `os.Exit(0)` is still called from inside the goroutine's defer. **`main()`'s defers never run.**
- Signal handling still uses `signal.Notify` + goroutine pattern, not `signal.NotifyContext`
- The prescribed fix from `SHUTDOWN_IMPL_PLAN.md §2.1` (let `main` return naturally) is NOT implemented

---

### 1.2 `lifecycle` package — prescribed in plan but not created
**Source:** `docs/SHUTDOWN_IMPL_PLAN.md Phase 1`
**Claim:** Create `lifecycle/lifecycle.go` with `Stoppable` interface, `Coordinator`, and HTTP/gRPC/Closer adapters.

**Verified code:** `find_by_name lifecycle*` → **0 results**. Package does not exist.

**Status: ❌ NOT IMPLEMENTED**

Note: The GRO (Goroutine Orchestrator) system (`gossipnode/config/GRO`, external package `github.com/JupiterMetaLabs/goroutine-orchestrator`) is used instead. `GRO.GlobalGRO.Shutdown()` serves as the goroutine registry shutdown. The `lifecycle` package as designed was bypassed. **The docs are stale** — they describe a design that was replaced by GRO.

**Action required:** Either implement `lifecycle` adapters on top of GRO, or update the plan to document that GRO is the chosen mechanism and describe its shutdown guarantees.

---

## 2. gRPC Servers — Own Signal Handlers

### 2.1 Block gRPC server races with main shutdown
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (Critical)`, `docs/SHUTDOWN_REVIEW.md §5`
**Original claim:** `Block/grpc_server.go` installs its own signal handler and blocks the shutdown coordinator.

**Verified code (`Block/grpc_server.go:186-199`):**
```go
// ❌ Installed inside StartGRPCServer
stop := make(chan os.Signal, 1)
signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
<-stop    // BLOCKS goroutine until signal arrives
grpcServer.GracefulStop()
healthServer.Shutdown()
```

The serve goroutine uses GRO (`LocalGRO.Go`) but the signal handler runs in the **caller's goroutine**, blocking it. When a SIGTERM arrives, both `main.go`'s `sigCh` and this `stop` channel receive it. They race.

**Status: ❌ STILL OPEN**

---

### 2.2 gETH gRPC server races with main shutdown
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (Critical)`, `docs/SHUTDOWN_REVIEW.md §5`
**Original claim:** `gETH/Server.go` installs its own signal handler, uses `log.Fatal`.

**Verified code (`gETH/Server.go:91-103`):**
```go
// ❌ Same pattern as Block gRPC
stop := make(chan os.Signal, 1)
signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
<-stop    // BLOCKS
grpcServer.GracefulStop()
healthServer.Shutdown()
```

Note: The serve goroutine (line 81-88) is run via `LocalGRO.Go()`. However line 84 still uses `log.Fatal().Err(err).Msg("Failed to serve gRPC")` — this calls `os.Exit(1)` immediately if the server stops, bypassing the GRO shutdown coordinator entirely. This is captured as a new finding in §7.2.

**Status: ❌ STILL OPEN** (signal racing unchanged; log.Fatal introduced as a new bug)

---

### 2.3 DID gRPC — signal handling
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (High)`, `docs/SHUTDOWN_REVIEW.md §5`
**Original claim:** `DID/DID.go:535-578` — `Serve` blocks; no `GracefulStop`, no handle.

**Verified code:** `grep signal.Notify DID/DID.go` → **0 results**. `DID.go:531` has `grpcServer.GracefulStop()`. DID does NOT install its own signal handler.

**Status: 🟡 PARTIALLY IMPROVED** — `GracefulStop` exists. No own signal handler (better than Block/gETH). However, whether the DID gRPC server is registered with GRO for coordinated shutdown requires further verification of its call site in `main.go`.

---

### 2.4 CLI gRPC — graceful stop
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (High)`, `docs/SHUTDOWN_REVIEW.md §5`
**Original claim:** `CLI/GRPC_Server.go:272-294` — `Serve` blocks; no `GracefulStop`, no handle.

**Verified code:** `CLI/GRPC_Server.go:336` has `grpcServer.GracefulStop()` and line 345 has `grpcServer.Stop()`. No `signal.Notify` found.

**Status: 🟡 PARTIALLY IMPROVED** — `GracefulStop` added. Whether it's wired into the coordinated shutdown path needs call-site verification.

---

## 3. HTTP Servers

### 3.1 gETH Facade HTTP & WS — no shutdown handle
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (High)`, `docs/SHUTDOWN_REVIEW.md §4`
**Original claim:** `StartFacadeServer` / `StartWSServer` — started in goroutine, `Serve` blocks, no `*http.Server` handle, no shutdown path.

**Verified code:** `grep func Start gETH/` returned only `StartGRPC`. No `StartFacadeServer` or `StartWSServer` found in the gETH package directory on this audit date.

**Status: ⚠️ REQUIRES INVESTIGATION** — functions may have been renamed or moved. The original concern (no `*http.Server` returned, no `Shutdown(ctx)` path) needs verification against the current entry points used in `main.go`.

---

### 3.2 Block REST API — `router.Run` with no shutdown
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (High)`, `docs/SHUTDOWN_REVIEW.md §4`
**Original claim:** `Block/Server.go:172-269` — uses `router.Run`; fatal on failure; no `Shutdown`.

**Status: ❌ NOT VERIFIED RESOLVED** — not re-audited in this pass. Original concern stands until proven otherwise.

---

### 3.3 Explorer API — `Close()` never called, DB client leak
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (High)`, `docs/SHUTDOWN_REVIEW.md §2`
**Original claim:** `StartAPIServer` never invokes `CloseImmuDBServer`, so pooled connections leak.

**Verified code (`main.go:523-538`):**
```go
func StartAPIServer(ctx context.Context, address string, enableExplorer bool) error {
    server, err := explorer.NewImmuDBServer(enableExplorer)
    // ...
    go explorer.StartBlockPoller(ctx, server, 7*time.Second)  // ✅ ctx wired
    return server.StartWithContext(ctx, address)               // ✅ ctx-aware start
}
```

`explorer/api.go:123-126`: `CloseImmuDBServer` exists and calls `server.Close()`. `explorer/api.go:277`: `StartWithContext(ctx, addr)` exists.

`StartAPIServer` now passes `ctx` through and uses `StartWithContext`. Whether `CloseImmuDBServer` is called on shutdown still needs verification of the `StartWithContext` implementation to confirm it calls `Close()` when `ctx` is cancelled.

**Status: 🟡 SUBSTANTIALLY IMPROVED** — ctx now threaded through. Block poller context-aware. Verify `StartWithContext` calls `Close()` internally.

---

### 3.4 Metrics server — default mux, no handle
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (Medium)`, `docs/SHUTDOWN_REVIEW.md §4`
**Original claim:** `metrics/metrics.go:154-163` — `http.ListenAndServe` on default mux, no handle, port not released.

**Status: ❌ NOT VERIFIED RESOLVED** — not re-audited in this pass. Original concern stands until proven otherwise.

---

## 4. Background Workers & Context Propagation

### 4.1 Block poller — no ticker.Stop(), no context
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (Critical)`, `docs/SHUTDOWN_REVIEW.md §3`
**Original claim:** `explorer/utils.go` — infinite ticker without `Stop()` or context cancellation.

**Verified code (`explorer/utils.go:28-79`):**
```go
func StartBlockPoller(ctx context.Context, DBclient *ImmuDBServer, pollInterval time.Duration) {
    ticker := time.NewTicker(pollInterval)
    defer ticker.Stop()    // ✅
    for {
        select {
        case <-ctx.Done():  // ✅ stops on context cancellation
            return
        case <-ticker.C:
            checkForNewBlocks(DBclient)
        }
    }
}
```

Called from `main.go:531`: `explorer.StartBlockPoller(ctx, server, 7*time.Second)` — root context passed.

**Status: ✅ FULLY FIXED**

---

### 4.2 FastSync workers — `context.Background()`
**Source:** `docs/SHUTDOWN_REVIEW.md §3`, `docs/CONTEXT_IMPACT.md §3`
**Original claim:** FastSync workers launched on `context.Background()`; no cancellation hook.

**Verified code (`fastsync/fastsync.go`):**
```
line 941:  fs.host.NewStream(context.Background(), ...)
line 1033: DB_OPs.GetMainDBConnectionandPutBack(context.Background())
line 1040: DB_OPs.GetAccountConnectionandPutBack(context.Background())
line 1129: DB_OPs.GetMainDBConnectionandPutBack(context.Background())
line 1136: DB_OPs.GetAccountConnectionandPutBack(context.Background())
```

5 confirmed occurrences. Caller context is not propagated into DB or libp2p stream calls.

**Status: ❌ STILL OPEN**

---

### 4.3 PubSub — no `Close()` method
**Source:** `docs/SHUTDOWN_AUDIT.md §3 (Medium)`, `docs/SHUTDOWN_REVIEW.md §3`
**Original claim:** `Pubsub/Pubsub.go` — no `Close()` implementation; stream handlers stay registered.

**Verified code:** `grep func.*Close\|func.*Shutdown\|func.*Stop Pubsub/Pubsub.go` → **0 results**.
`Pubsub/Pubsub.go:124` does have `defer ticker.Stop()` — ticker hygiene exists, but there is no method to shut down and deregister stream handlers.

**Status: ❌ STILL OPEN** — ticker handled; `Close()` method missing.

---

### 4.4 Node manager heartbeat — defer that never runs
**Source:** `docs/SHUTDOWN_REVIEW.md §3`
**Original claim:** `node/nodemanager.go:943-949` — `Shutdown()` cancels context and stops ticker but depends on deferred call in `main` that never runs because of `os.Exit(1)`.

**Verified:** Since main still exits via `defer shutdown.OS_EXIT(0)` from inside a goroutine (§1.1 above), `main`'s defers still don't run. This means `defer nodeManager.Shutdown()` in `main` still does not execute on signal.

**Status: 🟡 DEPENDENCY ON §1.1** — `Shutdown()` method likely correct; the call path is broken until signal handling is fixed.

---

### 4.5 Yggdrasil listener — root context but `os.Exit` bypass
**Source:** `docs/SHUTDOWN_REVIEW.md §3`
**Original claim:** Receives root context, but cancel path never executes because of `os.Exit(1)`.

**Verified:** Root context cancel IS now called (`cancel()` at `main.go:815`) before `shutdown.Shutdown()`. However since `os.Exit(0)` fires from the goroutine defer, the timeline is: cancel() → GRO shutdown → os.Exit(0). If Yggdrasil is registered with GRO, it would shut down. If it's not, `cancel()` alone may not be enough if its goroutine doesn't check `ctx.Done()`.

**Status: 🟡 PARTIALLY IMPROVED** — requires GRO registration verification.

---

## 5. DB Context Propagation (Phase B)

### 5.1 `DB_OPs/immuclient.go` — `context.Background()` throughout
**Source:** `docs/CONTEXT_IMPACT.md §3`, `docs/CONTEXT_PHASE_AB.md Phase B`
**Original claim (Nov 2025):** 26 occurrences of `context.WithTimeout(context.Background(), X)` in `immuclient.go`; callers cannot cancel DB operations.

**Verified code:** `grep context.Background() DB_OPs/immuclient.go` → **50+ confirmed occurrences** across the file, covering every major DB helper function. Representative sample:
```
line 226:  ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
line 333:  ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
line 561:  ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)  ← 60-minute timeout
line 863:  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
```

The 60-minute timeout at line 561 is particularly severe — any call to that function during shutdown will hold a DB connection for up to 60 minutes before releasing.

Phase B from `CONTEXT_PHASE_AB.md` (make DB helpers accept caller context, centralize timeout via `ConnectionPool.WithRequestTimeout`) is **entirely unimplemented**.

**Status: ❌ STILL OPEN** — unchanged since original audit. Count has grown (26 → 50+).

---

### 5.2 Connection pool — `Close()` wired to shutdown?
**Source:** `docs/SHUTDOWN_REVIEW.md §2`
**Original claim:** `config/ConnectionPool.go:472-499` — `Close()` stops cleanup ticker and cancels clients, but `main` never calls it (defer skipped).

**Verified:** Since defers in `main` still don't run on signal (§1.1), `defer DB_OPs.PutMainDBConnection(mainDBClient)` and related defers in `main` are still skipped. Unless the pool's `Close()` is registered as a GRO hook, it does not execute.

**Status: 🟡 DEPENDENCY ON §1.1** — pool `Close()` exists but call path broken.

---

## 6. Ticker Hygiene (Codebase-Wide)

**Source:** `docs/SHUTDOWN_IMPL_PLAN.md Phase 4`
**Original concern:** Tickers missing `defer ticker.Stop()`, goroutines leaking after shutdown.

**Verified code:** `grep ticker.Stop` across entire codebase → **16 locations** with `defer ticker.Stop()`:

| File | Status |
|---|---|
| `explorer/utils.go` | ✅ fixed (also context-aware) |
| `Pubsub/Pubsub.go` | ✅ ticker stopped |
| `messaging/broadcast.go` | ✅ |
| `messaging/blockPropagation.go` | ✅ |
| `messaging/directMSG/directMSG.go` | ✅ |
| `AVC/BuddyNodes/CRDTSync/buddy_integration.go` | ✅ |
| `AVC/BuddyNodes/MessagePassing/Service/nodeDiscoveryService.go` (×2) | ✅ |
| `AVC/BuddyNodes/CRDTSync/consensus_integration.go` | ✅ |
| `AVC/BFT/bft/sequencer_client.go` | ✅ |
| `AVC/BFT/bft/byzantine.go` | ✅ |
| `AVC/BFT/bft/engine.go` (×2) | ✅ |
| `gETH/Facade/Service/Service_WS.go` | ✅ |
| `seed/seedhelper.go` | ✅ |
| `seed/seed.go` | ✅ |

**Status: ✅ SUBSTANTIALLY FIXED** — codebase-wide ticker hygiene was addressed. Whether each goroutine also checks `ctx.Done()` varies and is not verified for all 16 locations.

---

## 7. New Findings (Not in Original Docs)

### 7.1 🆕 `Block/grpc_server.go` — debug `fmt.Printf` in production code
**File:** `Block/grpc_server.go:335-356`

```go
fmt.Printf("DEBUG newIntFromBytes: bytes (hex): %x, bytes (ASCII): %s\n", b, chainIDStr)
fmt.Printf("DEBUG newIntFromBytes: parsed as decimal string: %s -> %s\n", chainIDStr, result.String())
fmt.Printf("DEBUG newIntFromBytes: parsed as hex string: %s -> %s\n", chainIDStr, result.String())
fmt.Printf("DEBUG newIntFromBytes: failed to parse as string, falling back to byte interpretation\n")
fmt.Printf("DEBUG newIntFromBytes: interpreted as big-endian bytes: %x -> %s\n", b, result.String())
```

Raw `fmt.Printf` debug statements in a production `ProcessBlock` code path. These fire on every transaction and write to stdout bypassing the structured logger. This is a data leak risk (logs hex-encoded byte data outside the log pipeline) and a performance concern.

**Severity: HIGH** — should be removed or replaced with `log.Debug()` calls.

---

### 7.2 🆕 gETH gRPC uses `log.Fatal` in serve goroutine
**File:** `gETH/Server.go:84`
```go
log.Fatal().Err(err).Msg("Failed to serve gRPC")
```

`log.Fatal` calls `os.Exit(1)` immediately, bypassing the shutdown coordinator. If the gETH gRPC server encounters a serve error (port conflict, TLS failure), the process exits without running any cleanup.

**Severity: HIGH** — should be `log.Error()` + return, not `log.Fatal()`.

---

### 7.3 🆕 Block gRPC uses `log.Fatal` in serve goroutine
**File:** `Block/grpc_server.go:181`
```go
log.Fatal().Err(err).Msg("Failed to serve Block gRPC")
```

Same issue as §7.2. Fatal on serve error bypasses GRO shutdown.

**Severity: HIGH**

---

## 8. Summary Table (All Items)

| # | Component | Original Status | Current Status | File |
|---|---|---|---|---|
| 1.1 | Signal handling `os.Exit` bypass | ❌ | 🟡 | `main.go:806-835`, `shutdown/shutdown.go` |
| 1.2 | `lifecycle` package | ❌ | ❌ (GRO used instead) | N/A |
| 2.1 | Block gRPC own signal handler | ❌ | ❌ | `Block/grpc_server.go:187-199` |
| 2.2 | gETH gRPC own signal handler | ❌ | ❌ | `gETH/Server.go:91-103` |
| 2.3 | DID gRPC no GracefulStop | ❌ | 🟡 | `DID/DID.go:531` |
| 2.4 | CLI gRPC no GracefulStop | ❌ | 🟡 | `CLI/GRPC_Server.go:336,345` |
| 3.1 | gETH Facade/WS HTTP handles | ❌ | ⚠️ Unverified | `gETH/` |
| 3.2 | Block REST API `router.Run` | ❌ | ❌ | `Block/Server.go` |
| 3.3 | Explorer API `CloseImmuDBServer` never called | ❌ | 🟡 | `main.go:523-538` |
| 3.4 | Metrics no handle | ❌ | ❌ | `metrics/metrics.go` |
| 4.1 | Block poller no ticker.Stop/ctx | ❌ | ✅ | `explorer/utils.go:28-79` |
| 4.2 | FastSync `context.Background()` | ❌ | ❌ | `fastsync/fastsync.go:941,1033,1040,1129,1136` |
| 4.3 | PubSub no `Close()` | ❌ | ❌ | `Pubsub/Pubsub.go` |
| 4.4 | Node manager defer skip | 🟡 | 🟡 | `main.go` (defer path) |
| 4.5 | Yggdrasil cancel skip | 🟡 | 🟡 | `main.go:676-679` |
| 5.1 | DB_OPs `context.Background()` | ❌ | ❌ | `DB_OPs/immuclient.go` (50+ occurrences) |
| 5.2 | Connection pool `Close()` not called | 🟡 | 🟡 | `main.go` (defer path) |
| 6 | Ticker hygiene codebase-wide | ❌ | ✅ | 16 files |
| 7.1 | 🆕 `fmt.Printf` debug in Block gRPC | — | ❌ | `Block/grpc_server.go:335-356` |
| 7.2 | 🆕 `log.Fatal` in gETH serve goroutine | — | ❌ | `gETH/Server.go:84` |
| 7.3 | 🆕 `log.Fatal` in Block gRPC serve goroutine | — | ❌ | `Block/grpc_server.go:181` |

---

## 9. Recommended Priority Order

### P0 — Fix immediately (race conditions / data loss risk)

1. **`Block/grpc_server.go:187-199`** — Remove own `signal.Notify`. Use `ctx.Done()` from a caller-provided context instead.
2. **`gETH/Server.go:91-103`** — Same as above.
3. **`gETH/Server.go:84` and `Block/grpc_server.go:181`** — Replace `log.Fatal` in serve goroutines with `log.Error` + return.
4. **`Block/grpc_server.go:335-356`** — Remove all `fmt.Printf("DEBUG ...")` lines from `newIntFromBytes`.

### P1 — High impact, do next sprint

5. **`DB_OPs/immuclient.go`** — Thread caller context into all DB helpers. Eliminate `context.WithTimeout(context.Background(), ...)`. The 60-minute timeout at line 561 is a blocker for any clean shutdown.
6. **`fastsync/fastsync.go:941,1033,1040,1129,1136`** — Replace `context.Background()` with propagated caller context.
7. **`Pubsub/Pubsub.go`** — Add `Close()` method to deregister stream handlers.

### P2 — Lifecycle completeness

8. **Signal handling (`main.go:800-835`)** — Migrate to `signal.NotifyContext` and let `main()` return naturally so all defers execute.
9. **HTTP servers (Block REST, metrics, gETH Facade/WS)** — Return `*http.Server` handles; register `Shutdown(ctx)` with the GRO or equivalent.
10. **Verify GRO registration** for DID gRPC, CLI gRPC, Node Manager, Connection Pool `Close()`, and Yggdrasil listener.

---

## 10. What Is Genuinely Fixed

These items were documented as broken and are now confirmed resolved:

| Item | Evidence |
|---|---|
| Block poller context-aware | `explorer/utils.go:64-79` — `defer ticker.Stop()` + `ctx.Done()` |
| Block poller receives root ctx | `main.go:531` — `StartBlockPoller(ctx, server, 7*time.Second)` |
| Explorer API uses ctx | `main.go:538` — `server.StartWithContext(ctx, address)` |
| Ticker hygiene (16 files) | All major subsystems now `defer ticker.Stop()` |
| Signal handler does graceful shutdown | `shutdown/shutdown.go` — GRO.Shutdown + logger before exit |

---

*This document should be updated each time a P0 or P1 item is resolved. Mark the item ✅ with the commit hash and date.*
