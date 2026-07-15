# MRE v1 Proto Migration — Executive Brief

**What:** Migrate jmdn's gRPC client from MRE's legacy `/proto.RoutingService/*` API to the current `/jmdt.proto.mre.v1.MREService/*` API.

**Why:** The legacy service is a backward-compat shim kept alive for jmdn. Migrating lets MRE eventually retire it, gives jmdn the richer v1 responses (fetch metadata, per-node stats, node version), and removes a fragile hack: `txpool_content` today calls the v1 endpoint via a raw `conn.Invoke` and decodes the reply into a legacy type, surviving only on accidental wire compatibility.

**Scope:** jmdn client-side only. No MRE changes. No config, port, or TLS changes — both services are served on the same MRE port with identical transport. The orchestrator (also a legacy caller) is an explicit follow-on, out of scope here.

**Size of the change:** Small. The entire legacy surface lives in 2 files (`Block/gRPCclient.go`, `Block/Singleton_RoutingClient.go`) with 3 downstream consumers (`gETH/Facade/Service/Service.go`, `CLI/CLI.go`, `Block/Server.go`). jmdn uses 4 RPCs in production.

**Compatibility picture (verified at proto field level):**

| RPC | Verdict |
|---|---|
| SubmitTransaction | Drop-in — request/response wire-identical |
| PeekPendingTransactions | Drop-in + removes the raw-invoke hack |
| GetFeeStatistics | Drop-in — wire-identical, mapper parity confirmed |
| GetMempoolStats | **Breaking** — response restructured; exact field remap known |

The `Transaction` message itself is byte-for-byte identical between legacy and v1 (all 17 fields, same numbers and types).

**Risk:** Low. One breaking RPC with a documented mapping; everything else is a package swap. Client-only change → canary a single node, roll back by redeploying the previous build.

**Effort:** 2–3 engineering days + canary soak.

**Plan & status:** See `MRE-V1-MIGRATION-TRACKER.md` (single source of truth — phases, per-file checklist, open items).
