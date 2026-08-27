# FastSync Implementation Plan

**Status:** planning (no code yet — per "plan first, then decide")
**Branch context:** `feat/thebe-sc-layer` @ `143b803`
**Author aid:** grounded in the current tree; inferences labelled.
**Constraint:** author environment has no Go toolchain/CGO and spans two repos
(`jmdn` + `JMDN-FastSync`, the latter pinned). Every phase ends with a host build/test gate.

---

## 1. Verified current state (not the stale docs)

FastSync is **far more built than the docs claim** — the `JMDN-FastSync/CLAUDE.md` status
table ("AccountSync not created", "NewAccountNonceIterator needs DB impl") is **stale**.

Confirmed on `feat/thebe-sc-layer`:

- **Engine wired:** `FastsyncV2` (jmdn) wraps the library and wires every protocol router —
  prior/header/data/PoTS/accountSync/availability/reconcile — via `SetSyncVars` +
  `SetupNetworkHandlers` (`FastsyncV2/fastsyncv2.go`).
- **Storage adapter complete:** `DB_OPs/Nodeinfo` implements the full `BlockInfo` surface —
  `NewBlockIterator`, `NewHeadersWriter`, `NewDataWriter`, `NewAccountManager`, `AUTH`, and
  `NewAccountNonceIterator` (`thebe_account_manager.go:329`, concrete `thebeNonceIter`).
- **AccountSync (Phase 5) implemented** in the library: `HandleAccountsSync` + `ACCOUNTS_SYNC`
  with real phases in `core/protocol/router/data_router.go`, and `core/accountsync/` exists.
- **Disabled by default:** `config.FastSync.Enabled = false` — comment: *"pending the ThebeDB
  FastSync redesign (log-shipping model)."*

**The real blocker is not missing code — it's three things:**

1. **Work is spread across branches.** The freshest FastSync work is on **`fix/Fastsync`**
   (newer lib pin `v0.0.0-20260601052219-40e74741de7c`, bounded AccountSync enqueue, makefile)
   and **`chore/retire-fastsync-v1`** (V1 retirement, `latest_block` single-writer F6, recon
   entry-gate B1/B2). `feat/thebe-sc-layer` pins the **older** lib `d27320b363ba` and does not
   have those fixes.
2. **Never validated on ThebeDB.** The engine was written against ImmuDB; the `Nodeinfo`
   adapter was ported to ThebeDB but the "pending redesign" note means it is not yet trusted.
3. **The "log-shipping redesign" is undocumented** — no design doc exists (the referenced
   `docs/FASTSYNC_V3_MIGRATION_PLAN.md` is gone). *Assumed:* it means sync by shipping the
   ThebeDB append-only canonical log (seq-ordered) + replay, replacing Merkle-bisection +
   per-account reconcile. If wrong: the redesign scope changes and Track B below is different.

---

## 2. The decision that gates everything: reconcile vs redesign

Two fundamentally different directions. **This is the first decision to make.**

- **Reuse** the existing multi-phase engine (Merkle bisection → header → data → reconcile →
  PoTS + AccountSync). It exists and is wired; the work is reconciliation + validation + enable.
- **Redesign** to ThebeDB-native **log-shipping**: a joining node fetches the peer's canonical
  log from its last seq and replays via the profile projector — no Merkle bisection, no
  per-account reconcile. Simpler and a better fit for an append-only-log store, but it is a new
  engine and needs the SQL projection/watermark story sorted first (open ThebeDB findings).

**Recommendation: reuse first (Track A), redesign later (Track B) as a separate epic.** The
engine is built; getting a validated, enabled FastSync on ThebeDB now is high-value and
unblocks multi-node testing. The redesign is the right long-term shape but is a larger bet that
also depends on unresolved ThebeDB projection guarantees.

---

## 3. Track A — reconcile + validate + enable the existing engine (recommended first)

### A0. Branch reconciliation (do this before any FastSync code) — **feasibility gate**
- Establish the relationship between `feat/thebe-sc-layer`, `fix/Fastsync`, and
  `chore/retire-fastsync-v1`: merge-base, ahead/behind, and the conflict set (dry-run merge, no
  commit) — same recon discipline used for the thebe-sc merge.
- Decide the integration order. `chore/retire-fastsync-v1` (V1 retirement + `latest_block` F6 +
  recon gate) and `fix/Fastsync` (newer lib pin + bounded enqueue) both look like they belong
  under FastSync-on-ThebeDB.
- **Verify:** a written recon report — merge-base commit, per-file conflict list, and a proposed
  merge order — before creating any integration branch.

### A1. Pin + build hygiene
- Repin `JMDN-FastSync` to the newest validated commit (from `fix/Fastsync`), add it to `go.sum`
  (close B-02 for this dep), and confirm `go build ./...` with `CGO_ENABLED=1`.
- **Verify:** `go build ./...` clean; `go mod verify` clean; pin is a real commit, not a local `replace`.

### A2. ThebeDB adapter correctness (the real risk)
- Audit `DB_OPs/Nodeinfo` against the library's `BlockInfo`/`AccountManager`/`AUTH`/
  `AccountNonceIterator` contracts on ThebeDB: block/header/data iterators return the right
  ranges in the right order; writers are idempotent and WAL-first; `NewAccountNonceIterator`
  pages deterministically (same `ORDER BY LOWER(address)` fix as the fingerprint — a divergent
  iteration order breaks the account-set diff).
- **Verify:** unit tests per adapter method against a ThebeDB test store (host-gated, CGO).

### A3. `latest_block` single-writer + reconcile entry-gate
- Port `chore/retire-fastsync-v1`'s `latest_block` monotonic single-writer (F6) and the recon
  entry-gate on queue quiescence (B1/B2) onto the integration branch — these are the
  divergence-safety fixes that make sync results trustworthy.
- **Verify:** unit tests for the monotonic writer (never regresses) and the entry gate.

### A4. Serving + syncing gates
- Confirm the flag wiring: `FastSync.Enabled` gates serving (`SetupNetworkHandlers`), pulling
  (`PullAllowed`), catch-up, and the `syncmonitor`. Ensure a disabled node neither serves nor
  pulls (safe default) and an enabled node does both.
- **Verify:** with the flag off, no FastSync stream handlers are registered; with it on, all are.

### A5. 2-node sync gate (the acceptance test)
- Reuse the `local-2node-gate` harness: node B (fresh) fast-syncs from node A (has N blocks +
  contract state), then assert **identical state fingerprint** (the `ORDER BY LOWER(address)`
  one) and identical balances/receipts/`eth_getCode` — i.e. a fast-synced node is byte-identical
  to one that applied every block. Add the different-insertion-order case (a synced node has a
  different `created_at` history) — this is exactly what FastSync must get right.
- **Verify:** post-sync fingerprints match; the negative (perturbed peer) is rejected.

### A6. Enable
- Flip `FastSync.Enabled` default-on **only after A5 passes**, homogeneous-fleet caveat like contracts.

---

## 4. Track B — log-shipping redesign (later epic, design-doc first)

1. **ADR / design doc** (for review before code): sync = fetch canonical log `[lastSeq+1 .. head]`
   from a peer, verify the hash chain (append-only immutability — already guaranteed by
   `badger_store` seq + `__sys:hash` chain), replay through the profile projector to rebuild SQL.
   Compare against the current engine; state what it removes (Merkle bisection, per-account
   reconcile) and what it needs (a durable SQL projection watermark — an open ThebeDB finding).
2. **Prereqs:** resolve the ThebeDB projection guarantees the redesign leans on (watermark,
   SQL-before-KV ordering) — these are open T-items; the redesign should not be built on them
   unresolved.
3. **Phased build** behind a second flag, A/B-tested against Track A on the 2-node gate.

Do **not** start Track B code until the ADR is reviewed.

---

## 5. Track C — AccountSync completion (fold into A, not separate)

The library's AccountSync is implemented (server `ACCOUNTS_SYNC`, client `core/accountsync/`,
jmdn `NewAccountNonceIterator`). Remaining, per the (stale) status table, to re-verify against
the newer `fix/Fastsync` pin: `HandleAccountsSync` stream registration, the server diff
computation `case true`, and the `AccountSyncEvent` WAL type. These are best validated as part of
**A2/A5** (adapter correctness + the 2-node gate exercising a zero-tx account), not as a separate
track — AccountSync only matters once the base engine syncs.

---

## 6. Recommended sequence

1. **A0 branch recon** (feasibility gate — no code).
2. **A1 pin/build**, **A2 adapter audit + tests**, **A3 single-writer/entry-gate port**.
3. **A4 flag gates**, **A5 2-node sync gate** (acceptance).
4. **A6 enable** (default-on, homogeneous fleet).
5. **Track B** design doc — in parallel, review-gated; build after A ships.

---

## 7. Risks / open questions

- **Branch divergence (highest):** the FastSync work on `fix/Fastsync` /
  `chore/retire-fastsync-v1` may conflict with the thebe-sc-layer changes; A0 must quantify this
  before any integration. Untangling three long-lived branches is the main schedule risk.
- **Adapter correctness on ThebeDB:** the engine was ImmuDB-era; the deterministic iteration
  order (accounts/blocks) is the class of bug that silently breaks the set diff — A2 is the hot spot.
- **Redesign premises unresolved:** log-shipping leans on ThebeDB projection/watermark guarantees
  that are open findings — Track B is gated on those.
- **No toolchain here:** every code phase is written grounded and host-verified by you; the
  2-node gate (A5) is the real proof.
- **Pin/creds:** `JMDN-FastSync` is private and behind a pseudo-version; CI needs `GOPRIVATE`/creds
  to build it (same as the ThebeDB pin issue).

**Decision needed from you:** confirm **Track A first** (reuse+validate+enable) with Track B as a
later design-gated epic — or say if you want the log-shipping redesign designed up front instead.
Once you pick, I'll start with **A0 (branch reconciliation recon report)**, which is read-only and
needs no build.
