# Shutdown Review – Service-by-Service Analysis (updated 2025-11-11)

This review captures the real shutdown behaviour after re-reading `main.go`, the server helpers, and the network subsystems. The previous checklist assumed lifecycle infrastructure that does not exist today; this version documents the actual state and the gaps we still have to close.

---

## Legend
- ✅ Hook implemented **and** exercised during shutdown.
- 🟡 Hook exists but is not wired into the current shutdown path (for example, relies on defers that never run).
- ❌ No viable shutdown hook.

Because `main` still calls `os.Exit(1)` on signal, every item that depends on a defer currently behaves as ❌ at runtime. We keep 🟡 markers to show where code exists but needs integration.

---

## 1. Core shutdown flow
| Component | Code reference | Status | Notes |
| --- | --- | --- | --- |
| Signal handling | `main.go:570-584` | ❌ | Uses `context.WithCancel` but immediately invokes `os.Exit(1)`, so defers never execute. |
| Lifecycle coordinator | — | ❌ | No registry/adapters yet. All shutdowns are ad-hoc. |

---

## 2. Persistence & connection pools
| Service | Code reference | Status | Notes |
| --- | --- | --- | --- |
| Main DB pool | `config/ConnectionPool.go:472-499` | 🟡 | `Close()` stops the cleanup ticker and cancels clients, but `main` never calls it (defer skipped). |
| Accounts/DID pool | Same file | 🟡 | Same behaviour as main pool. |
| Explorer DB clients | `explorer/api.go:62-66` (`CloseImmuDBServer`) | ❌ | Helper exists, yet `StartAPIServer` never invokes it, so pooled connections leak on exit. |

---

## 3. Network + background workers
| Service | Code reference | Status | Notes |
| --- | --- | --- | --- |
| libp2p host | `main.go:608-615` | 🟡 | Deferred `Host.Close()` only runs on clean exit. Needs lifecycle registration. |
| Node manager | `node/nodemanager.go:943-949` | 🟡 | `Shutdown()` cancels context and stops ticker but depends on deferred call in `main`. |
| PubSub | `Pubsub/Pubsub.go` | ❌ | No `Close()` implementation; stream handlers stay registered. |
| FastSync | `fastsync/fastsync.go`, `fastsyncNew.go` | ❌ | Workers launched on `context.Background()`; no cancellation hook. |
| Block poller | `explorer/utils.go`, launched `main.go:420` | ❌ | Infinite ticker without `Stop()` or context cancellation. |
| Yggdrasil listener | `messaging/directMSG/Socket.go`, launched `main.go:676-679` | 🟡 | Receives root context, but cancel path never executes because of `os.Exit(1)`. |

---

## 4. HTTP / WS ingress
| Service | Code reference | Status | Notes |
| --- | --- | --- | --- |
| gETH Facade HTTP | `main.go:62-71`, `gETH/Facade/rpc/http_server.go` | ❌ | Server started in goroutine, `Serve` blocks, no stored `*http.Server`. |
| gETH WS | `main.go:74-84`, `gETH/Facade/rpc/ws_server.go` | ❌ | Same issue as HTTP; no shutdown path. |
| Block REST API | `Block/Server.go:172-269` | ❌ | Uses `router.Run`; fatal on error, no `Shutdown`. |
| Explorer API | `main.go:828-831`, `explorer/api.go:167-206` | ❌ | Returns only after `ListenAndServe`; no handle exported; DB clients not released. |
| Metrics server | `metrics/metrics.go:154-163`, `main.go:712` | ❌ | `http.ListenAndServe` on default mux, no handle or shutdown logic. |

---

## 5. gRPC ingress
| Service | Code reference | Status | Notes |
| --- | --- | --- | --- |
| Block gRPC | `main.go:753-759`, `Block/grpc_server.go:134-181` | ❌ | Installs its own signal handler and blocks main shutdown; no exported handle. |
| gETH gRPC | `main.go:808-813`, `gETH/Server.go:28-77` | ❌ | Same pattern as block gRPC, uses `log.Fatal`. |
| DID gRPC | `main.go:736-742`, `DID/DID.go:535-578` | ❌ | Returns the result of `Serve`; no `GracefulStop`, no handle. |
| CLI gRPC | `CLI/CLI.go:127-134`, `CLI/GRPC_Server.go:272-294` | ❌ | `grpcServer.Serve` blocks until failure; no stop hook. |

---

## 6. Priority remediation items
1. Replace the `os.Exit(1)` signal handler with `signal.NotifyContext` and allow `main` to return naturally.
2. Introduce a lifecycle coordinator with adapters for HTTP (`Shutdown`), gRPC (`GracefulStop`/`Stop`), and simple closers (DB pools, libp2p host, node manager).
3. Refactor every server helper to return handles and remove inline signal handling.
4. Retrofit background workers (block poller, FastSync, discovery/heartbeat) to respect `context.Context` and stop tickers.
5. Add `Close()` to `StructGossipPubSub` and ensure `globalPubSub` is cleaned up.

---

## 7. Suggested implementation order
1. **Signal handling & lifecycle scaffolding.**
2. **gRPC servers** – remove internal signal traps, expose handles, register with lifecycle coordinator.
3. **HTTP servers** – return `*http.Server`, add adapter registrations, ensure `Shutdown(ctx)` uses local 8 s timeout.
4. **Background workers** – thread contexts, stop tickers, register as stoppable tasks.
5. **Persistence & messaging** – register DB pools, pubsub, FastSync components.
6. **Regression tests** – add goleak / port reuse tests once handles exist.

Completing the above will allow us to mark services as ✅ in future audits. For now, every ingress service and most background workers remain ❌ in production shutdown scenarios.
