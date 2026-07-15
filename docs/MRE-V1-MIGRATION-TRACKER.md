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

---

## 5. Delivery plan — stages, gates, acceptance criteria

### Stage S1 — Parity migration `[status: pending]`
Goal: v1 client, behavior byte-equivalent. No user-visible change.

**Phase 0 · Tooling** — entry: this doc approved.
- [ ] Vendor `types.proto` + `mre.proto` (MRE @ `e808b96`) into `Mempool/proto/v1/`; rewrite `go_package` (`MRE/...` → `gossipnode/...`) and the internal `import "proto/common/v1/types.proto"` path
- [ ] `make generate-proto` — pinned protoc-gen-go/protoc-gen-go-grpc (note MRE uses v1.36.11/v6.33.1 vs jmdn legacy v1.36.10/v5.29.3)
- [ ] Exit gate: `go build ./...` green; generated code committed

**Phase 1 · Client swap** — entry: Phase 0 gate.
- [ ] `Singleton_RoutingClient.go`: retype to `mrev1.MREServiceClient`; typed Peek replaces raw invoke (**atomic with retype — same PR**, risk R-2); GetMempoolStats remap (`QueueCount←aggregated.total_cache_size`, `DbCount←aggregated.total_primary_txns` — source MRE `mapper.go:174-178`); `emptypb` standardization
- [ ] `gRPCclient.go`: package-swap converters + SubmitTransaction; OP-7 structured submit-result logging; drop vestigial `MempoolServiceClient` field; execute O-1 wrapper decision; de-leak `GasFeeStats`
- [ ] `CLI/CLI.go:500-507` stats remap; O-2 MerkleRoot line decision
- [ ] Exit gate: compile green; all unit tests green

**Phase 2 · Verification** — entry: Phase 1 gate.
- [ ] Unit: converter round-trip, all 17 fields, type-2 fees + GasPrice→MaxFee fallback (`gRPCclient.go:806`); mocked client via `SetRoutingClient`
- [ ] Integration (dev stack): submit e2e; peek returns it; CLI stats sane; gasPrice = Standard
- [ ] Parity: legacy vs v1 `GetFeeStatistics` byte-compare on live dev MRE; stats remap vs shard counts
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
- O-1 unused wrappers: delete vs deprecate one release? (Recommend delete; confirm no out-of-tree tooling)
- O-2 CLI MerkleRoot line: drop vs replace with `healthy_nodes/node_count`?
- O-3 ~~GetNodeVersion wiring~~ → resolved: ACCEPTED as OP-6 (S3)
- O-4 `txpool_content` re-enable: was it disabled deliberately (load? correctness?) — **need history/context from team before S2** (`git log` the comment-out)
- O-5 timeout budget: 5s client timeout vs worst-case multi-round fetch — raise client timeout for peek or cap limit?

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
