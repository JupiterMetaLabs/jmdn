# Bounded Account Enqueue (Chunking) — Implementation Phases

Mode: **Prod**. Scope: **accounts only**, consumer-side (`DB_OPs/Nodeinfo`). No library change, no `go.mod` bump.

## Problem (evidence)
- Library client receive handler `core/sync/sync_protocols.go:666 HandleAccountsSyncData` ACKs each page (`:713`, before any DB) then accumulates **all** pages into one in-memory `batch` and calls `WriteAccountsBatch` **once at EOF** (`:720`).
- That single call hands `account_manager.WriteAccounts` (`immudb_account_manager.go:170`) the entire batch (up to millions). It does one `json.Marshal` + one `XADD` → a single huge message. Redis caps a bulk string at `proto-max-bulk-len` (512 MiB default): the message stalls or is rejected → EOF write fails *after* all pages were ACKed → server session fails → dispatcher retries the range (`DispatchACKTimeout=10s`, `DispatchMaxRetries=3`) → dead-letter storm → sync never converges.
- Consumer enqueue path is already async (returns before ImmuDB commit); the worker drain/ACK/XDEL contract is correct. The ONLY defect is the unbounded single message.

## SOLID Gates
- **S** — Invariant owned by the producer methods: "deliver an account/update batch to the stream as bounded, individually-valid messages." (Worker owns the separate "ACK only after commit" invariant — unchanged.)
- **O** — New record kinds extend via the existing `syncPayloadType` tag + a `processBatch` case; the generic helper accepts any `[]T`. No switch edit needed to change chunk size (const) or add a caller.
- **I** — Helper depends only on `RedisStreamer.Enqueue` (1 method of the 8-method interface) — minimal surface; no new interface added.
- **D** — Helper depends on the `RedisStreamer` interface, not `*redis.Client`. No new concrete cross-package import. (Pre-existing `DB_OPs` import in the worker is out of scope.)

## Pattern Selection
Primary pattern: **none new** — bounded iteration inside the established Producer (account_manager) → Adapter (RedisStreamer) structure.
Why: the fix is a loop + size bound, not a new abstraction. Adding a Strategy/Builder would be ceremony.
Trade-off: chunk size is a const, not injected — promote to config later via the documented extension point if needed.
Anti-pattern avoided: a "MessageChunker" service object / new interface for a 12-line helper.

## Phase 1.0: Bounded enqueue helper + const + timeout
- What: add `maxRecordsPerMessage` const (500), `enqueueTimeout(chunks)` helper, and generic `enqueueRecordsChunked[T any](ctx, s RedisStreamer, ptype syncPayloadType, items []T) error` in `immudb_account_manager.go`. Best-effort over chunks, `errors.Join` aggregation.
- Data structures: input `[]T` (sequential, read-once, O(1) re-slicing into fixed chunks — no map, no copy). Bound: each marshalled message holds ≤ `maxRecordsPerMessage` records.
- Inputs: none (uses existing `RedisStreamer`, stream constants).
- Done when: helper compiles; never marshals more than `maxRecordsPerMessage` records into one message.
- Status: [x]

## Phase 1.1: Rewire WriteAccounts + BatchUpdateAccounts
- Trigger: 1.0 helper exists; both producers must use it instead of the single `json.Marshal`+`XADD`.
- What: replace the one-shot marshal/enqueue in `WriteAccounts` (`:170`) and `BatchUpdateAccounts` (`:324`) with chunk-count computation + `enqueueRecordsChunked`. Sized context via `enqueueTimeout`.
- Done when: both methods enqueue N records as `ceil(N/500)` messages; error wraps record + message counts.
- Status: [x]

## Phase 1.2: Docs — module headers / function docs
- Trigger: behavior of the two interface methods changed; worker `writeEntries` bound is now finite.
- What: update `WriteAccounts`/`BatchUpdateAccounts` doc comments (chunking + best-effort semantics); update `account_sync_worker.go` module header `[]dbEntry` growth-bound note (now `MaxDrainItems × maxRecordsPerMessage`, previously unbounded).
- Done when: doc comments reflect chunking; worker header bound corrected.
- Status: [x]

## Phase 1.3: White-box test
- Trigger: helper is unexported; needs same-package test.
- What: `account_sync_enqueue_test.go` (package NodeInfo). Mock `RedisStreamer` records `Enqueue` payloads. Table: 0,1,499,500,501,1000,2500 → assert message count = ceil(n/500), each decoded chunk ≤ 500, sum == n, correct type tag. Failure case: every 3rd chunk errors → `errors.Join` non-nil AND remaining chunks still enqueued (best-effort).
- Deviation (documented): craftcode Phase 6 wants tests under `tests/`; Go package-internal visibility forces a same-dir `_test.go`. Matches existing repo convention (`DB_OPs/sqlops/sqlops_test.go`).
- Done when: `go test ./DB_OPs/Nodeinfo/ -run Enqueue` passes (no live infra needed — mock streamer).
- Status: [x]

## Phase 2.0: Library durable-before-ACK (scope B — JMDN-FastSync)
Repo: `../JMDN-FastSync` (NOT this repo). Files:
- `common/types/constants/accounts_constants.go` — added `AccountReceiveFlushThreshold = 20_000` (documents the bound; the per-page rewrite makes peak receive memory one page/stream).
- `core/sync/sync_protocols.go HandleAccountsSyncData` — rewrote the receive loop from "accumulate whole stream → one `WriteAccountsBatch` at EOF" to **per-page: read → `WriteAccountsBatch` (WAL + Redis enqueue) → ACK**. Success = `BatchAck` (Ok); persist failure = `ErrBatchAck` (Ok=false) → dispatcher retries page → dead-letter on repeated failure.
- What it fixes: the client previously buffered the entire diff range (up to ~2.7M accounts, ~10 streams) in one slice → OOM. The server's 200k nonce-buffer cap does NOT bound the client. Now receive memory = one page (~3k) per stream.
- Server impact: **none** — stateless dispatcher unchanged; still one-ack-per-page contract; NAK rides the pre-existing retry→DLQ path (`DispatcherCallbacks.go:134`, `run.go handleFailure`). ACK now reflects true durability.
- Done when: `JMDN-FastSync` builds + vets clean; `jmdn` builds end-to-end against it.
- Status: [x] (code) / [ ] (published — see Integration)

## Phase 2.1: Integration / publish
- Local verify (done): `go mod edit -replace github.com/JupiterMetaLabs/JMDN-FastSync=../JMDN-FastSync` in `jmdn/go.mod`; `CGO_ENABLED=1 go build ./...` → exit 0. **This replace is DEV-ONLY — must be reverted before merge.**
- Production path (NOT yet done — requires user): commit + push `JMDN-FastSync` (branch `fix/accountsync/performance`), obtain the new pseudo-version, then `go get github.com/JupiterMetaLabs/JMDN-FastSync@<new-version>` in `jmdn` and remove the `replace`. Until then the library change is not in any published artifact.
- Status: [ ]

## Non-goals (explicit)
- No fix to `parseUpdatesPayload` AccountType/DIDAddress behavior (separate concern).
- No worker config or drain-logic change.
- No change to the server dispatcher (statelessness preserved).
</content>
</invoke>
