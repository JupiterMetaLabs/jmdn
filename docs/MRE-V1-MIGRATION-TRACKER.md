# MRE v1 Proto Migration — Technical Tracker

Living document. Single source of truth for the migration: surface inventory, API mapping, phased plan with status, and open items. Update checkboxes as work lands; do not create additional docs for this feature (executive brief: `MRE-V1-MIGRATION.md`).

**Branch:** `feat/mre-v1-proto-migration` · **MRE proto source pin:** `e808b96` (Mempool-Routing-Engine @ main)

---

## 1. Surface inventory (VERIFIED, file:line)

### 1.1 Generated code & tooling

| Item | Location | Note |
|---|---|---|
| Legacy proto | `Mempool/proto/mempool.proto` (package `proto`, go_package `./proto`) | Defines RoutingService (9 RPCs) + MempoolService (10 RPCs) + messages |
| Legacy generated | `Mempool/proto/mempool.pb.go`, `mempool_grpc.pb.go` | protoc-gen-go v1.36.10, protoc v5.29.3 |
| v1 source (MRE repo) | `proto/mre/v1/mre.proto`, `proto/common/v1/types.proto` | go_package `MRE/proto/mre/v1;mrev1` / `MRE/proto/common/v1;commonv1` — **must be rewritten** to `gossipnode/Mempool/proto/...` when vendored; mre.proto imports `proto/common/v1/types.proto` — include path must be preserved or adjusted |
| MRE toolchain | protoc-gen-go v1.36.11, protoc-gen-go-grpc v1.6.0, protoc v6.33.1 | Newer than jmdn's; pin jmdn's regen target to one toolchain |
| protoc target in jmdn | **none** (no Makefile target, no buf, no go:generate) | Added in Phase 0 |
| v1 generated in jmdn | none | Added in Phase 0 |

### 1.2 Client layer (the only 2 files importing `gossipnode/Mempool/proto`)

| Component | file:line | Migration action |
|---|---|---|
| `RoutingClient{client pb.RoutingServiceClient}` | `Block/Singleton_RoutingClient.go:22-25` | Retype → `mrev1.MREServiceClient` |
| `NewRoutingServiceClient` — gatekeeper TLS (`ServiceMempool`/`"mempool_client"`), `grpc.NewClient(addr, WithTransportCredentials)` | `:28-110` (creds `:64`, dial `:75`, stub `:91`) | Only the stub constructor changes |
| `PeekPendingTransactions` — raw `conn.Invoke("/jmdt.proto.mre.v1.MREService/PeekPendingTransactions")`, decodes into legacy `pb.TransactionBatch` (field-1 wire compat) | `:215-226` | Replace with typed v1 stub → `commonv1.GetPendingResponse` |
| `GetMempoolStats` → `*pb.MREStats` | `:228-271` (call `:244`) | **Breaking** — remap per §2 |
| `GetFeeStatistics` → `*pb.FeeStatistics` | `:158-208` (call `:174`) | Package swap |
| `empty.Empty` import style (`ptypes/empty`) | `:13,174,244` | Standardize on `emptypb` |
| `MempoolClient` — field typed `pb.MempoolServiceClient` (`:33-36`, built `:87`) but methods delegate to RoutingClient singleton | `Block/gRPCclient.go` | Drop vestigial field |
| `MempoolClient.SubmitTransaction` | `gRPCclient.go:116-249` (call `:173`) | Package swap (`SubmitTxResponse` wire-identical) |
| `convertToPbTransaction` (all 17 tx fields; GasPrice→MaxFee fallback `:806`) + `convertAccessListToPb` | `:746-831` | Package swap only — Transaction identical |
| Unused wrappers: `SubmitTransactions` `:252-361`, `GetTransaction` `:364-419`, `GetPendingTransactions` `:422-480`, `GetFeeStatisticsFromRouting` `:551-615`, `WrapperGetFeeStatistics` `:679-743` | | See open item O-1 |
| `GasFeeStats{RecommendedFees *pb.RecommendedFees}` — proto type leaks into public Block API | `:541-547` | Retype (plain struct preferred) |

### 1.3 Downstream consumers

| Consumer | file:line | Reads | Impact |
|---|---|---|---|
| `Service.TxPoolContent` | `gETH/Facade/Service/Service.go:844-868` | `batch.GetTransactions()` + Transaction getters via anonymous interface `:877-936` | None — v1 getters identical |
| `Service.GasPrice` | `Service.go:715` | `RecommendedFees.Standard` | None — populated in v1 |
| CLI fee display | `CLI/CLI.go:489-498` | Min/Max/Median/MeanFee, PriorityFeeRatio | None — wire-identical; PriorityFeeRatio is 0 in both mappers today |
| CLI stats display | `CLI/CLI.go:500-507` | `QueueCount`, `DbCount`, `MerkleRoot` | **Update** — remap per §2; MerkleRoot always `""` (see O-2) |
| `Block/Server.go:258` | `SubmitToMempool` | success/error only | None |
| `gETH_Middleware.go:161-180` | commented-out `_EstimateGas` | — | Delete or ignore (O-1) |

### 1.4 Config / deploy — no changes

`network.mempool` (`config/settings/config.go:34`, flag default `localhost:15051` `main.go:744`), TLS ids (`security.go:13`), yaml (`jmdn_exchange.yaml:27,187-189`): all unchanged. MRE serves legacy + v1 on the same server/port (`app.go:448,451,582`), identical interceptors, no per-service auth.

### 1.5 Tests

None exist for this layer. `SetRoutingClient` (`Singleton_RoutingClient.go:152-155`) is the injection seam for mocks.

---

## 2. API mapping (field-level VERIFIED)

| Legacy | v1 | Wire compat | Notes |
|---|---|---|---|
| `SubmitTransaction` → `SubmitResponseMRE{success=1,hash=2,error=3,mempool_node=4,replica_mempools=5,total_replicas=6}` | `SubmitTransaction` → `SubmitTxResponse` (same fields 1–6) | ✅ identical | Drop-in |
| `SubmitTransactions` → `BatchSubmitResponse{success=1,count=2,hashes=3,error=4}` | same | ✅ identical | If kept (O-1) |
| `GetTransaction(GetTransactionRequest{hash=1})` | `GetTransactionByHash(GetTransactionByHashRequest{hash=1,from_replica=2})` — destructive; new `PeekTransactionByHash` non-destructive | ✅ compatible (request gains field 2) | Renamed; prefer Peek for lookups |
| `GetPendingTransactions` → `TransactionBatch{transactions=1}` | same method → `GetPendingResponse{transactions=1,total_fetched=2,rounds_used=3,duration_ms=4}` | ✅ field 1 identical | New metadata fields free |
| — | `PeekPendingTransactions` → `GetPendingResponse` | ✅ (already called via raw invoke) | Formalize |
| `GetMempoolStats` → `MREStats{queue_count=1:int32, db_count=2:int32, merkle_root=3:string}` | `GetMempoolStats` → `GetMempoolStatsResponse{aggregated=1:msg, nodes=2, node_count=3, healthy_nodes=4}` | ❌ **NOT compatible** (field 1 int32 vs message) | Remap (source: MRE `mapper.go:174-178`): `QueueCount ← aggregated.total_cache_size`; `DbCount ← aggregated.total_primary_txns`; `MerkleRoot` was hardcoded `""` |
| `GetFeeStatistics` → `FeeStatistics` (9 fields) | same | ✅ identical | Mapper parity verified: both leave `FeeByTxType` empty, omit `PriorityFeeRatio`/`HistoricalTrend` |
| `GetTransactionLookup` / `GetAllTransactionLookups` / `OnReceiveFromJMDT` | dropped in v1 | n/a | Legacy stubs were Unimplemented; jmdn never called them |
| — | `GetNodeVersion` → `VersionInfo{git_tag,branch,commit,build_time,go_version}` | new | Optional CLI/ops wiring (O-3) |

`Transaction` message: **byte-for-byte identical** (17 fields, same numbers/types) legacy ↔ `common.v1`. `AccessTuple` identical.
⚠️ `Metadata` fields 3/4 are **swapped** between legacy and v1 (`created_at`/`total_replicas`) — not wire-compatible. jmdn never reads Metadata today; do not decode it with the wrong package.

---

## 3. Risk register

| # | Risk | Mitigation |
|---|---|---|
| R-1 | GetMempoolStats remap wrong → CLI shows garbage | Mapping copied from MRE's own legacy shim; Phase 2 spot-check against live shard counts |
| R-2 | Intermediate state where raw-invoke Peek decodes into a mismatched type | Do Peek swap atomically with client retype (same file, same PR) |
| R-3 | Vendored protos drift from MRE | Pin source commit in proto header; CI diff check vs MRE repo (deferred D-2) |
| R-4 | Public `Block` API change breaks out-of-tree tooling | Unused-wrapper decision O-1; if in doubt, deprecate one release before deleting |
| R-5 | go_package/import-path rewrite errors in vendored protos | Compile + round-trip test catches; protoc include path documented in Makefile target |
| R-6 | Stats value changes interpreted as migration bug | `DbCount` value is identical by construction; note it currently includes orphaned-tx pollution (see TX ordering proposal) — pre-existing, unrelated |

---

## 4. Phased plan & status

### Phase 0 — Tooling & codegen  `[status: pending]`
- [ ] Vendor `types.proto` + `mre.proto` from MRE @ `e808b96` into `Mempool/proto/v1/`; rewrite `go_package` → `gossipnode/Mempool/proto/v1/...`; fix `import "proto/common/v1/types.proto"` path
- [ ] Add `make generate-proto` (pin protoc-gen-go / protoc-gen-go-grpc versions; document install)
- [ ] Generate + commit; `go build ./...` green

### Phase 1 — Client swap  `[status: pending]`
- [ ] `Singleton_RoutingClient.go`: retype client to `mrev1.MREServiceClient`; typed Peek (drop raw invoke); GetMempoolStats remap; `emptypb` standardization
- [ ] `gRPCclient.go`: package-swap converters + SubmitTransaction; drop vestigial `MempoolServiceClient` field; execute O-1 decision on unused wrappers; de-leak `GasFeeStats`
- [ ] `CLI/CLI.go`: stats field remap (`:500-507`)
- [ ] `Service.go`: confirm compiles unchanged (getter interface)

### Phase 2 — Tests & verification  `[status: pending]`
- [ ] Unit: converter round-trip covering all 17 fields incl. type-2 fees + GasPrice→MaxFee fallback (`gRPCclient.go:806`); mocked client via `SetRoutingClient`
- [ ] Integration (dev stack): submit tx e2e; `txpool_content` shows it; CLI stats sane; `eth_gasPrice` returns Standard
- [ ] Live dev MRE: legacy-vs-v1 `GetFeeStatistics` byte-compare; stats remap spot-check

### Phase 3 — Rollout & cleanup  `[status: pending]`
- [ ] Canary one node; watch submit success rate, txpool_content, CLI, gasPrice (48h)
- [ ] Fleet rollout (normal release)
- [ ] Cleanup PR: delete `Mempool/proto/mempool.proto` + legacy generated code

---

## 5. Pending / Deferred / Open

### Pending (blocking a phase)
- P-1: O-1 decision (unused wrappers) blocks part of Phase 1.

### Deferred (agreed, not now)
- D-1: **Orchestrator migration** — still calls legacy `GetPendingTransactions` + `GetMempoolStats` (`internal/mempool/routing_client.go:70-81`, `cmd/orchestrator/monitoring.go:273`). Same mapping table. ~1 day. MRE legacy handler retires only after this lands.
- D-2: CI proto-drift check (or shared buf registry across repos).
- D-3: MRE-side: legacy `RoutingService` + `MREStatsLegacy` shim removal (after D-1).

### Open questions (need a human call)
- O-1: Unused wrappers — delete (`GetTransaction`, `GetPendingTransactions`, 2 of 3 fee wrappers, commented `_EstimateGas`) vs deprecate one release? Recommendation: delete; nothing in-tree calls them, but confirm no out-of-tree ops tooling does.
- O-2: CLI `Merkle Root` line prints `""` today (legacy shim hardcodes it). Drop the line, or replace with v1 `healthy_nodes/node_count`?
- O-3: Wire `GetNodeVersion` into CLI for ops visibility? (Free with the migration.)

---

## 6. Verification log

| Date | Check | Result |
|---|---|---|
| 2026-07-15 | Legacy↔v1 `Transaction` field-by-field diff | Identical (17 fields, same numbers/types) |
| 2026-07-15 | `SubmitResponseMRE` vs `SubmitTxResponse` | Identical fields 1–6 |
| 2026-07-15 | `MREStats` vs `GetMempoolStatsResponse` | Incompatible (field 1 int32 vs message); remap sourced from MRE `mapper.go:174-178` incl. `MerkleRoot: ""` |
| 2026-07-15 | Fee mapper parity (legacy `mapper.go:182-203` vs `mapper_v1.go:295`) | Both leave `FeeByTxType` empty; no `PriorityFeeRatio`/`HistoricalTrend` — no regression |
| 2026-07-15 | Both services same port/interceptors, no TLS delta (MRE `app.go:428-452,582`) | Confirmed — client-only migration |
| 2026-07-15 | jmdn importers of legacy pb | Exactly 2 files (`Block/gRPCclient.go`, `Block/Singleton_RoutingClient.go`) |
| 2026-07-15 | v1 proto go_package + internal import need rewrite when vendored | Confirmed (`MRE/proto/...;mrev1`, import `proto/common/v1/types.proto`) |
| 2026-07-15 | Toolchain versions (MRE protoc v6.33.1/gen-go v1.36.11 vs jmdn v5.29.3/v1.36.10) | Noted — pin in Makefile |
