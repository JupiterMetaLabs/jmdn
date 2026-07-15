# MRE v1 Proto Migration & API Modernization — Executive Brief

**What:** Migrate jmdn's gRPC client from MRE's legacy `/proto.RoutingService/*` to the current `/jmdt.proto.mre.v1.MREService/*`, then use the migration as the foundation to close a set of API-capability gaps found during the audit.

**Why now:** Three drivers.

1. **Debt.** The legacy service exists only for jmdn. jmdn's one v1-path call today is a raw `conn.Invoke` hack that decodes v1 bytes into a legacy type and survives on accidental wire compatibility.
2. **Underuse.** The audit showed jmdn uses a fraction of what the API offers — it discards most response fields it receives (fee tiers, routing/replica info, fetch metadata), ignores the `pending` block tag, and several standard Ethereum surfaces are missing or synthetic (`txpool_status`, `eth_maxPriorityFeePerGas`; `eth_feeHistory` returns constants).
3. **Correctness findings.** The implementation-level audit surfaced issues that matter beyond the migration: `txpool_content` is **disabled in production** (handler commented out — the RPC returns "method not found" while the implementation sits wired underneath); `eth_getTransactionCount` silently maps `pending`→`latest` (the exact gap class behind the recent nonce-ordering incident); and MRE's `GetFeeStatistics` — the source for `eth_gasPrice` — is a **stub** (all statistics derived from one average; 4 of 9 response fields permanently empty; the real percentile fee engine in the mempool nodes is bypassed).

**Shape of the work — three stages, independently shippable:**

| Stage | Content | Effort | Risk |
|---|---|---|---|
| **S1 · Parity migration (PoC → GA)** | Swap client to v1, byte-equivalent behavior, tests + canary. One breaking RPC (`GetMempoolStats`) with a verified field remap. | 2–3 days + soak | Low — client-only, same port/TLS, per-node rollback |
| **S2 · Correctness fixes** | Re-enable `txpool_content`; honor `pending` in `getTransactionCount` (chain nonce + pool lookahead); surface submit failures better | 2–3 days | Low-medium — user-visible RPC behavior changes, gated + canaried |
| **S3 · Capability wave (opt-in)** | Pending-tx visibility in `eth_getTransactionByHash` (PeekTransactionByHash), `txpool_status`, `eth_maxPriorityFeePerGas`, mempool health in readiness, MRE version in ops CLI | ~1 week, priority-ranked | Low — additive |

**Explicitly out of scope, filed upstream:** MRE-side defects found by the audit (fee-statistics stub, `from_replica` ignored on by-hash RPCs, no not-found signal on lookups, `limit<=0` → wrong status code, missing latency metrics on read paths, destructive all-shard fallback). These are tracked as upstream items in the tracker — S1–S3 do not depend on them, but S3's fee work is bounded by the stub until MRE fixes it.

**Orchestrator:** also a legacy caller; explicitly a follow-on with the same mapping table. MRE legacy retirement only after both migrate.

**Compatibility (field-level verified):** `Transaction` message byte-identical legacy↔v1; Submit and FeeStatistics wire-identical; Pending responses compatible on field 1 with new metadata fields; `GetMempoolStats` breaking with a documented remap sourced from MRE's own legacy shim.

**Governance:** `MRE-V1-MIGRATION-TRACKER.md` is the single source of truth — phase gates with acceptance criteria, per-file checklists, risk register, observability/canary plan, rollback per phase, and the open-items ledger. No other documents will be created for this feature.
