# Shutdown Registration Audit (updated 2025-11-11)

This is the refreshed audit of graceful-shutdown coverage after reviewing the current `main` bootstrap flow and the server helpers that were changed over the last week.

---

## 1. Snapshot Summary
- `main.go` still handles `SIGINT/SIGTERM` by calling `os.Exit(1)` inside a goroutine (`main.go:574-584`). Because of this, **every defer in `main` is skipped on Ctrl+C/systemd stop**.
- There is **no lifecycle coordinator** or central registration. All shutdowns are ad-hoc (and most are missing).
- Every long-lived server is started in a goroutine without returning a handle. None of them can be drained or shut down gracefully.
- Background services (pollers, heartbeats, discovery) depend on `context.Background()` or internal tickers without cancellation hooks.

---

## 2. What actually gets cleaned up today?
> These clean-ups only run on a normal program exit. They do **not** run when `os.Exit(1)` fires.

| Resource | Trigger in code | Notes |
| --- | --- | --- |
| libp2p host | `defer n.Host.Close()` (`main.go:609-615`) | Skipped on signal because `os.Exit(1)` aborts defers. |
| Node manager | `defer nodeManager.Shutdown()` (`main.go:724-727`) | Would stop the heartbeat ticker if it ever ran. |
| Main DB connection | `defer DB_OPs.PutMainDBConnection(mainDBClient)` (`main.go:651-653`) | Connection request rooted in `context.Background()`, so no cancellation. |
| Accounts/DID connection | `defer DB_OPs.PutAccountsConnection(didDBClient)` (`main.go:667-670`) | Same issue. |
| Logger | `defer Logger.Close()` (`main.go:567-568`) | Skipped when `os.Exit(1)` runs. |

No other subsystems perform automatic teardown.

---

## 3. Missing registrations / refactors (current state)

### HTTP servers
| Service | Source | Severity | Current behaviour |
| --- | --- | --- | --- |
| gETH Facade HTTP | `StartFacadeServer` → `main.go:62-71` | High | Spawned in goroutine, `Serve` blocks, no `*http.Server` handle, no shutdown path. |
| gETH WS | `StartWSServer` → `main.go:74-84` | High | Same as HTTP; websockets never closed. |
| Block generator | `Block.Startserver` → `main.go:745-750`, `Block/Server.go:172-269` | High | Uses `router.Run`; fatal on failure, no shutdown hook. |
| Explorer API | `StartAPIServer` → `main.go:413-424 / 828-831` | High | `explorer.ImmuDBServer.Start` returns only after `ListenAndServe`; DB clients leak on stop. |
| Metrics | `metrics.StartMetricsServer` → `main.go:712`, `metrics/metrics.go:154-163` | Medium | `http.ListenAndServe` in goroutine, global mux, no handle, port not released. |

### gRPC servers
| Service | Source | Severity | Current behaviour |
| --- | --- | --- | --- |
| DID gRPC | `startDIDServer` → `main.go:736-742`, `DID/DID.go:535-578` | High | `grpcServer.Serve` blocks; no `GracefulStop`, no exposed handle. |
| Block gRPC | `Block.StartGRPCServer` → `main.go:753-759`, `Block/grpc_server.go:134-181` | **Critical** | Installs its own signal handler, blocks shutdown coordinator, no handle. |
| gETH gRPC | `gETH.StartGRPC` → `main.go:808-813`, `gETH/Server.go:28-77` | **Critical** | Same pattern as Block gRPC; uses `log.Fatal` and signal trap. |
| CLI gRPC | `CLI.StartGRPCServer` → `CLI/CLI.go:127-134`, `CLI/GRPC_Server.go:272-294` | High | `grpcServer.Serve` blocks, no handle, runs under `context.Background()`. |

### Background services
| Component | Source | Severity | Current behaviour |
| --- | --- | --- | --- |
| Block poller | `explorer.StartBlockPoller` → `main.go:420`, `explorer/utils.go` | **Critical** | `time.NewTicker` with no `Stop`, no context, goroutine leak. |
| Node heartbeat | `node/nodemanager.go` | Medium | Uses internal context, but cancellation depends on deferred `Shutdown()` which never executes. |
| FastSync | `fastsync/fastsync.go` | Medium | Worker goroutines run under `context.Background()`, no teardown. |
| Yggdrasil listener | `directMSG.StartYggdrasilListener` → `main.go:676-679` | Medium | Receives root context, but cancel is skipped because of `os.Exit(1)`. |
| PubSub | `Pubsub/Pubsub.go` | Medium | No `Close()` method; stream handlers remain registered indefinitely. |

---

## 4. Immediate remediation priorities
1. **Eliminate `os.Exit(1)` from the signal goroutine** and switch to `signal.NotifyContext` so defers run.
2. **Introduce a lifecycle coordinator** (or equivalent) so every server/worker can register a shutdown hook.
3. **Return server handles** (`*http.Server`, `*grpc.Server`) for each service and wrap them with adapters that enforce local timeouts.
4. **Add cancellation-aware contexts** for ticker-based loops (`explorer.StartBlockPoller`, node heartbeat, discovery jobs) and ensure `ticker.Stop()` is always called.

---

## 5. Delta vs. previous audit (late October)
- No new resources were registered; the previous checklist items remain open.
- Additional issues discovered during this pass:
  - `CLI/GRPC_Server.go` still blocks on `Serve` and never calls `GracefulStop`.
  - `explorer.NewImmuDBServer` now exposes `Close`, but `StartAPIServer` never invokes it, so pooled connections leak on restart.
  - `metrics.StartMetricsServer` still binds the default HTTP mux, preventing port reuse after restart.

---

## 6. Next actions
- Track refactors in the shutdown implementation plan:
  - Return handles for HTTP/gRPC servers and register them via lifecycle adapters.
  - Add `Close()` to `StructGossipPubSub` and ensure `globalPubSub` is shut down.
  - Thread cancellable contexts into FastSync, block pollers, and discovery routines.
- Once lifecycle hooks exist, add regression tests (`go.uber.org/goleak`, port reuse checks) to ensure we do not regress.

