# MRE v1 Migration & API Modernization — Technical Tracker

Living document — single source of truth. Update statuses/checkboxes in place; create no additional docs (executive brief: `MRE-V1-MIGRATION.md`).

**Branch:** `feat/mre-v1-proto-migration` (off main) · **MRE proto pin:** `e808b96` · **Evidence standard:** every claim file:line-verified; anything else marked ASSUMED.

---

## 1. API truth audit — v1 RPC name vs implementation (VERIFIED)

Implementation traced handler → service → component → downstream mempool node for all 9 MREService RPCs.

| RPC | Verdict | Key findings |
|---|---|---|
| SubmitTransaction | **MATCHES** | Primary blocking + replicas async fire-and-forget (`transaction.go:93-101`); replica errors discarded. Failed submit → gRPC **OK** with `success=false` (`:31-47`). All response fields populated; `total_replicas` actually counts ALL nodes incl. primary (`:121`) — naming mismatch. Async Postgres hash→shard lookup save (`:144-160`) |
| SubmitTransactions | **MISLEADING** | Not a batch: N parallel single-submits, unbounded goroutines (`transaction.go:185-206`); downstream real batch RPC never used. Always gRPC OK; inspect `success`/`error` |
| GetTransactionByHash | **PARTIAL/MISLEADING** | Destructive at node (cache+DB; primary-DB delete **async** `mempool.go:290-294`); MRE-side cleanup deletes only the Postgres pointer (`service.go:90-99`). Lookup-first, else **destructive scatter to ALL shards** (`transaction.go:264`, `pool.go:425`). `from_replica` request field **silently ignored** (`grpc_v1.go:93`). Miss → empty `Transaction{hash}` — no error, no found flag |
| PeekTransactionByHash | **MATCHES** | Truly side-effect-free (`mempool.go:257-273`). `from_replica` also ignored (`grpc_v1.go:106`). Fallback is serial over `shardMapper` shards (`transaction.go:314-317`), no latency metrics |
| GetPendingTransactions | **MATCHES** | Destructive; node deletes returned rows in **fire-and-forget goroutine** (`mempool.go:428-447`). `limit`/`from_replica` honored end-to-end. Response metadata (`total_fetched/rounds_used/duration_ms`) real. `limit<=0` → `codes.Internal` (should be InvalidArgument) |
| PeekPendingTransactions | **MATCHES** | Zero side effects confirmed (cache Peek, no delete branch). Can overlap concurrent destructive reads (async-delete race) |
| GetMempoolStats | **MATCHES (live)** | Fresh scatter-gather per call; node recomputes (`mempool.go:585-591`). `healthy_nodes` = "GetStats call succeeded", not a health probe (`stats.go:26`). `nodes[].stats.routing` **never populated** (commented out, `mempool.go:616`) |
| GetFeeStatistics | **MISLEADING (STUB)** | No downstream call — reads NATS-fed shard cache (`stats.go:93`): `mean=min=median=avg`, `max=2×avg`, recommended = avg×{1,1,2,4}; **35 gwei hardcoded fallback** (`stats.go:74,94-104`). 4 of 9 fields always empty (`fee_distribution`, `priority_fee_ratio`, `fee_by_tx_type`, `historical_trend`). Real percentile engine in mempool nodes (`fee_analysis.go`) bypassed. Confirmed by MRE's own `FEE_ARCHITECTURE.md:98` |
| GetNodeVersion | **MATCHES** | MRE **self**-version (not mempool nodes); git fields empty unless built with `-ldflags` (`version.go:8-14`) |

**Cross-cutting:** failed submits never map to gRPC error codes; by-hash miss indistinguishable from found; no `mre.rpc.latency`/`error` metrics on Peek/Pending/Stats pool calls (only Submit + destructive Get record them).

---

## 2. jmdn use-case map (VERIFIED, full stacks in commit history of this audit)

| Surface | Status today | MRE RPC | Gaps found |
|---|---|---|---|
| `eth_sendRawTransaction` | Live, async submit (`handlers.go:229` → `Server.go:253` goroutine → `gRPCclient.go:173`) | SubmitTransaction | Returns locally-computed hash **before** MRE accepts; MRE rejection logged, never surfaced; `mempool_node/replica_mempools/total_replicas` + MRE hash logged then dropped from return; no retry |
| `txpool_content` | **HANDLER DISABLED** — commented out `handlers.go:510-524` → `-32601`; impl fully wired underneath (`Service.go:843`, v1 raw-invoke Peek, limit 5000) | PeekPendingTransactions (raw invoke) | Unreachable in prod; `queued` hardcoded `{}`; no sort; fetch metadata unused |
| `txpool_status` / `txpool_inspect` | NOT IMPLEMENTED | — | Candidates (stats / peek+summary) |
| `eth_gasPrice` | Live (`Service.go:709-731`) | GetFeeStatistics | Uses only `RecommendedFees.Standard`; wrapper discards everything else (`gRPCclient.go:608`); floor comment says 35 gwei, code compares 20 gwei (`Service.go:728-731`); **upstream stub bounds quality** |
| `eth_maxPriorityFeePerGas` | NOT IMPLEMENTED | — | Candidate (blocked on upstream fee stub for real values) |
| `eth_feeHistory` | Live but **synthetic** (`Service.go:768-818`): constant baseFee, ratio 0.5, rewards 0x0 | none | Candidate for real data (upstream-bounded) |
| `eth_estimateGas` | Heuristic only (`Service.go:640`); MRE-based code commented (`:648-669`, `gETH_Middleware.go:161-180`) | none | Acceptable; optional later |
| `eth_getTransactionByHash` | Chain-DB only (`Service.go:418`) — pending txs return error | none | **Candidate:** PeekTransactionByHash → geth-standard pending visibility (null blockNumber) |
| `eth_getTransactionReceipt` | Chain-only, null for unmined — correct | none | none |
| `eth_getTransactionCount` | **`pending` tag silently mapped to `latest`** (`handlers.go:537-539`), tag then ignored (`Service.go:91`) | none | **Candidate (correctness):** pending nonce = chain nonce + sender's pool txs. Same gap class as the nonce-ordering incident |
| CLI `mempoolstats` | Live (`CLI.go:478-510`) | GetFeeStatistics + GetMempoolStats | Prints `PriorityFeeRatio` (always 0 — stub); MerkleRoot always `""`; discards RecommendedFees |
| Health/readiness | ImmuDB-only (`explorer/health.go`); gRPC health hardcoded SERVING (`gETH/Server.go:73-75`) | none | **Candidate:** mempool connectivity via GetMempoolStats healthy_nodes |
| Explorer HTTP API | Chain-only; `/api/v1/node/version` returns jmdn's own version (`api.go:510`) | none | Candidate: pending-tx endpoint; MRE version via GetNodeVersion |
| Destructive GetPendingTransactions | **0 callers in jmdn** (wrapper exists `gRPCclient.go:422`) | — | Keep it that way (orchestrator-only operation) |

---

## 3. Opportunity register (Task A × Task B)

| ID | v1 capability | jmdn use case | Value | Effort | Decision |
|---|---|---|---|---|---|
| OP-1 | typed PeekPendingTransactions | Replace raw-invoke hack; re-enable `txpool_content` | High (prod-visible RPC currently dead) | S | **ACCEPT → S2** |
| OP-2 | PeekTransactionByHash | Pending-tx visibility in `eth_getTransactionByHash` | High (wallet/exchange UX; standard behavior) | M | **ACCEPT → S3** (needs miss = empty-tx handling; upstream U-3 would improve) |
| OP-3 | PeekPendingTransactions filtered by sender | `eth_getTransactionCount("pending")` honest pending nonce | High (correctness; incident-adjacent) | M | **ACCEPT → S2** |
| OP-4 | GetMempoolStats (aggregated) | `txpool_status` RPC | Medium | S | **ACCEPT → S3** |
| OP-5 | GetMempoolStats healthy_nodes | Readiness/health surfacing mempool connectivity | Medium (ops) | S | **ACCEPT → S3** (document that healthy = RPC-reachable only) |
| OP-6 | GetNodeVersion | CLI ops command + explorer version endpoint enrichment | Low-medium (ops) | S | **ACCEPT → S3** (requires MRE built with ldflags — verify in env) |
| OP-7 | SubmitTxResponse routing fields | Structured submit-result logging/metrics (shard placement visibility) | Medium (debugging incidents) | S | **ACCEPT → S1** (log fields already received; add structured metric) |
| OP-8 | GetPendingResponse total_fetched/rounds/duration | txpool_content metadata + client-side observability | Low | S | **ACCEPT → S2** (free with OP-1) |
| OP-9 | Richer FeeStatistics for gasPrice/maxPriorityFee/feeHistory | Real fee market data | High potential | — | **REJECT for now** — upstream stub (U-1); revisit after MRE fixes |
| OP-10 | SubmitTransactions (batch) | none in jmdn (single-tx RPC flow) | — | — | **REJECT** — also misleading impl (N singles) |
| OP-11 | from_replica on by-hash reads | — | — | — | **REJECT** — server ignores the param (U-2) |
| OP-12 | Destructive GetTransactionByHash / GetPendingTransactions | — | — | — | **REJECT** — destructive ops belong to the orchestrator only |

---

## 4. Upstream findings ledger (MRE / mempool defects — out of scope here, to file)

| ID | Defect | Evidence | Suggested fix |
|---|---|---|---|
| U-1 | GetFeeStatistics is a stub; real fee engine bypassed; 35 gwei fallback; 4/9 fields empty | `stats.go:74-104`, `FEE_ARCHITECTURE.md:98` | Route through downstream `MempoolService.GetFeeStatistics` aggregation |
| U-2 | `from_replica` silently ignored on Get/PeekTransactionByHash | `grpc_v1.go:93,106` | Honor or remove from proto |
| U-3 | By-hash miss returns empty tx, no found flag / NotFound | `transaction.go:274` | Add `found` field or NotFound status |
| U-4 | Failed submits return gRPC OK | `transaction.go:31-47` | Map to InvalidArgument/Unavailable |
| U-5 | `limit<=0` → Internal | `pending.go:16-18` | InvalidArgument |
| U-6 | Destructive all-shard fallback consumes from every node | `transaction.go:264`, `pool.go:425-434` | Peek-first fallback, destructive only on confirmed shard |
| U-7 | No latency/error metrics on Peek/Pending/Stats pool calls | `pool.go:395-503` | Add `mre.rpc.latency/error` uniformly |
| U-8 | `nodes[].stats.routing` never populated | `mempool.go:616` | Populate or drop from proto |
| U-9 | Async destructive deletes race concurrent peeks (dup delivery) | `mempool.go:290-294,428-447` | Ack protocol (see TX-ordering proposal P1-B) |
| U-10 | SubmitTransactions = unbounded goroutine fan-out | `transaction.go:185-203` | Bounded worker pool or true downstream batch |
| U-11 | Node-level merkle roots (`GetPrimaryMerkleRoot`/`GetAllMerkleRoots`, mempool `mempool.go:717-764`) never surfaced through MREService; legacy stats hardcode `""` | MRE `mapper.go:177` | If pool-integrity monitoring is wanted: per-node roots in `NodeStats`. Cross-shard deterministic aggregate REJECTED by operator (separate effort, not optimal) |

---

## 5. Delivery plan — stages, gates, acceptance criteria

### Stage S1 — Parity migration `[status: pending]`
Goal: v1 client, behavior byte-equivalent. No user-visible change.

**Phase 0 · Tooling** — entry: this doc approved. **[DONE 2026-07-15 — awaiting operator review]**
- [x] Vendor `types.proto` + `mre.proto` (MRE @ `e808b96`) into **`proto/v1/{common,mre}/`** (operator decision 2026-07-15: all protos under top-level `proto/<version>/<service>` — future services `proto/v1/{mempool,seednode,orchestrator}`; legacy `Mempool/proto/` migrates there at cleanup); `go_package` → `gossipnode/proto/v1/...`; provenance headers added
- [x] `make generate-proto` added (Makefile); generated with protoc 33.1 + protoc-gen-go v1.36.11 + protoc-gen-go-grpc v1.6.0 (exact MRE toolchain match)
- [x] Exit gate: `go build ./Mempool/...` + `go vet` green; **go.mod/go.sum unchanged** (repo already pins protobuf v1.36.11 / grpc v1.79.3)
- [ ] Operator code review + commit consent

**Phase 1 · Client swap** — entry: Phase 0 gate. **[DONE 2026-07-15 — awaiting operator review]**
- [x] NEW `Block/routing_port.go`: `MempoolRouter` port + plain domain types (`SubmitResult`, `PendingBatch`, `MempoolStatsSummary`, `FeeStats`); `PendingTx` read-only view interface (generated type satisfies it structurally — zero copy, zero proto leak); explicit error contract (transport err vs `Accepted=false` rejection)
- [x] `Singleton_RoutingClient.go`: rewritten as `mreRouter` adapter over `mrev1.MREServiceClient`; compile-time conformance (`var _ MempoolRouter`); typed Peek replaced raw invoke atomically (risk R-2 closed); GetMempoolStats remap per plan; `emptypb`; `CloseRoutingClient` added; OP-7 structured submit-result logging (primary node, total nodes)
- [x] `gRPCclient.go`: slimmed to converters (retargeted `commonv1`) + `SubmitToMempool`/`GetFeeStatisticsFromRouting` facades; O-1 executed — deleted `MempoolClient`, `NewMempoolClient`, `SubmitTransactions`, `GetTransaction`, `GetPendingTransactions`, per-client fee/stats methods, `WrapperGetFeeStatistics`, `InitMempoolClient`/`CloseMempoolClient`/`ReturnMempoolObject`; `GasFeeStats` proto leak removed (plain `FeeStats`)
- [x] `main.go`: single client init (`NewRoutingServiceClient` + `defer CloseRoutingClient`)
- [x] `Service.go`: `Recommended.Standard`; `TxPoolContent` consumes `PendingBatch`; `mempoolTxToRPCObject` takes `block.PendingTx` (anonymous interface removed)
- [x] `CLI.go`: O-2 executed — MerkleRoot → `Healthy Nodes: N/M`; dead `PriorityFeeRatio` → `Recommended (standard)`; rationale inline
- [x] `gETH_Middleware.go`: dead `_EstimateGas` block removed
- [x] Exit gate (partial): `go build`+`go vet` green for `./Block/... ./CLI/... ./gETH/... ./proto/...`; full `make build` (CGO link) deferred to operator review (sandbox disk/FD limits)
- [ ] Operator code review + commit consent

**Phase 2 · Verification** — entry: Phase 1 gate. **[unit half DONE 2026-07-15; live half BLOCKED on staging MRE]**
- [x] Unit: converter round-trip (17 fields, type-2), GasPrice→MaxFee fallback both directions, nil-safety; fakeRouter via `SetRoutingClient` seam pinning facade error contract. 7/8 green first run; 8th exposed a real defect — `GetRoutingClient` nil path initialized the global logger → `settings.Get()` panic in any pre-`Load` context (would have been a prod crash). Fixed: accessor no longer logs.
- [ ] **BLOCKED (staging MRE available ~1 day, operator 2026-07-15):** Integration on staging — submit e2e; peek returns it; CLI stats sane; gasPrice = Standard
- [ ] **BLOCKED (same):** Parity — legacy vs v1 `GetFeeStatistics` byte-compare; stats remap spot-check vs shard counts; O-5 timeout budget measurement (limit-5000 peek latency vs 5s client timeout)
- [ ] Exit gate (**GA criteria**): canary node 48h — submit success rate unchanged (±0.1%), zero new error-log signatures, gasPrice values identical, CLI output correct

**Rollback:** redeploy previous build (client-only). No data migration, no coordination.

### Stage S2 — Correctness fixes `[status: pending]` — entry: S1 GA
- [ ] Re-enable `txpool_content` handler (`handlers.go:510-524`) using typed v1 Peek; add OP-8 metadata; decide `queued` semantics (still `{}` — document why)
- [ ] `eth_getTransactionCount("pending")`: chain nonce + max(pool nonces for sender)+1 lookahead via Peek filtered by `from`; honor block tag through `Service.GetTransactionCount`
- [ ] `eth_sendRawTransaction`: define submit-failure surfacing policy (keep async return, add structured rejection metric + log; document eventual-submit semantics)
- [ ] Exit gate: RPC-conformance tests vs geth behavior for the touched methods; canary 48h; explicit release-note entries

**Rollback:** per-method feature flags (env) — disable pending-nonce lookahead / txpool_content independently.

### Stage S3 — Capability wave (priority order) `[status: pending]` — entry: S2 GA; each item independently shippable
- [ ] OP-2 pending-tx visibility in `eth_getTransactionByHash` (flagged)
- [ ] OP-4 `txpool_status`
- [ ] OP-5 mempool health in readiness (+ document semantics)
- [ ] OP-6 `GetNodeVersion` → CLI + explorer
- [ ] Exit gate per item: conformance test + 24h canary

### Observability (applies S1 onward)
Watch on canary: jmdn submit success/error log signatures (`gRPCclient.go:194-236`), `eth_gasPrice` value distribution, txpool_content latency (limit-5000 peek against MRE `RoundTimeout=5s`×`MaxRounds=5` — worst case ~25s vs jmdn 5s client timeout ⇒ **verify timeout budget in Phase 2**), MRE-side `mre.tx.received/submitted` continuity. SLO: no p99 regression >10% on submit path.

---

## 6. Pending / Deferred / Open

**Pending:** P-1 O-1 decision blocks part of Phase 1.

**Deferred:** D-1 orchestrator migration (legacy `GetPendingTransactions`+`GetMempoolStats`; same map; MRE legacy retires after). D-2 CI proto-drift check / buf registry. D-3 MRE legacy handler removal. D-4 upstream ledger §4 → file as MRE/mempool issues (owner: Naman to triage which are pre-GA).

**Open questions:**
- O-1 ~~unused wrappers~~ → **RESOLVED (operator 2026-07-15): DELETE** — zero in-tree callers verified twice. Scope: `MempoolClient.GetTransaction`, `.GetPendingTransactions`, `.SubmitTransactions`, `.GetFeeStatistics`, `WrapperGetFeeStatistics`, commented `_EstimateGas` block; one fee path survives behind the new port.
- O-2 ~~CLI MerkleRoot line~~ → **RESOLVED (operator 2026-07-15): OMIT cleanly.** The value was never real: mempool nodes compute genuine per-store merkle roots (`mempool.go:717-764`) but MRE never calls those RPCs — legacy shim hardcodes `""` (`mapper.go:177`). CLI line replaced with `healthy: N/M nodes`. Aggregated/deterministic MRE merkle explicitly rejected as a separate non-optimal effort → tracked as U-11 only.
- O-3 ~~GetNodeVersion wiring~~ → resolved: ACCEPTED as OP-6 (S3)
- O-4 ~~`txpool_content` disabled — deliberate?~~ → **RESOLVED by operator (2026-07-15):** it was never wired properly / not prod-ready — the raw v1 `conn.Invoke` existed only because the legacy proto has no Peek RPC. The handler stayed disabled on purpose. **Mandate: build it properly in this migration** — clean implementation per design principles below.
- O-5 timeout budget: 5s client timeout vs worst-case multi-round fetch — raise client timeout for peek or cap limit?

**Design mandate (operator, 2026-07-15) — applies to all new/refactored code in S1–S3:**
clean system design; interface-driven (consumers depend on a routing-client port, not the concrete singleton); SRP (transport client / domain conversion / RPC-facade formatting as separate concerns); no proto types leaking into public package APIs; testable via injected interfaces, not global state.

**Working agreement:** every verifiable/testable checkpoint pauses for operator code review; **no commit or push without explicit operator consent**; all findings recorded in this tracker — nothing dropped.

---

## 7. Verification log

| Date | Check | Result |
|---|---|---|
| 2026-07-15 | Legacy↔v1 Transaction field diff | Identical (17 fields) |
| 2026-07-15 | Submit/Fee/Pending wire compat; MREStats incompatibility + remap | Confirmed (remap per MRE `mapper.go:174-178`) |
| 2026-07-15 | Same port/interceptors/TLS both services | Confirmed — client-only migration |
| 2026-07-15 | v1 go_package + import path rewrite needed; toolchain versions | Confirmed |
| 2026-07-15 | **RPC truth audit, all 9 v1 RPCs** (handler→service→node) | §1 table; 3 MISLEADING/PARTIAL verdicts; fee stub confirmed vs `FEE_ARCHITECTURE.md:98` |
| 2026-07-15 | **jmdn surface map, 15 surfaces** | §2 table; txpool_content disabled at `handlers.go:510-524`; `pending` tag ignored at `handlers.go:537-539`; zero destructive-pending callers |
| 2026-07-15 | Fee mapper parity legacy vs v1 (empty fields identical) | Confirmed — no parity regression in S1 |
