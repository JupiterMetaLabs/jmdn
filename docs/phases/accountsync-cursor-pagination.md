# AccountSync Performance Fixes — Implementation Phases

## Context
AccountSync wall-clock >2 days on 10k blocks + 2.7M accounts.
All issues below are in this repo (`jmdn`). Issues in `JMDN-FastSync` library are tracked separately.

## SOLID Gates
**S:** Each fix owns one invariant (scan, block read, type conversion).
**O:** New scan behaviour → pass `extendedPrefix`; no existing code modified.
**I:** No fat interfaces introduced.
**D:** No new cross-package concrete imports.

## Pattern Selection
Iterator (Behavioral) for pagination; Facade (Structural) for fast block read variant.

---

## Phase 1: Cursor-based pagination — DONE
- What: Replace `offset int` with `seekKey []byte` cursor in `immudbNonceIter`.
  Add `ListAccountsPaginatedFrom` (ascending, cursor-based) in `account_immuclient.go`.
  Remove dead `nonceToAccount map` + `sync.Mutex` from iterator.
- Impact: ~365M ImmuDB scan entries → ~2.7M. O(N²) → O(N).
- Files: `DB_OPs/account_immuclient.go`, `DB_OPs/Nodeinfo/immudb_account_manager.go`
- Done when: build passes, `offset` field gone from `immudbNonceIter`. ✅

---

## Phase 2: Fix `defer ReadCancel()` inside loop in `ListAccountsPaginated` — DONE
- What: Line 1085 — `defer ReadCancel()` is inside a `for` loop. Each iteration
  schedules a cancel that only fires on function return, not on loop iteration end.
  All cancel funcs accumulate for the function lifetime → goroutine/context leak.
  Fix: call `ReadCancel()` immediately after the `Scan` call (not deferred).
- Files: `DB_OPs/account_immuclient.go`
- Done when: no `defer` inside the scan loop of `ListAccountsPaginated`.

---

## Phase 3: Add `GetZKBlockByNumberFast` (plain Get, no proof generation) — DONE
- What: `GetZKBlockByNumber` uses `VerifiedGet` — generates a cryptographic Merkle
  proof per read (5–10× slower than plain `Get`). Sync/reconciliation paths do not
  need tamper-proof guarantees. Add `GetZKBlockByNumberFast` using `ic.Client.Get`.
  Keep `GetZKBlockByNumber` (VerifiedGet) for client-facing verified queries.
- Data structures: none new; same `*config.ZKBlock` return type.
- Files: `DB_OPs/immuclient.go`
- Done when: `GetZKBlockByNumberFast` exported, compiles, uses plain `Get`.

---

## Phase 4: `GetTransactionsByAccount` uses `GetZKBlockByNumberFast` — DONE
- What: `GetTransactionsByAccount` (line 1293) loops every block 0→latestBlock,
  calling `GetZKBlockByNumber` (VerifiedGet) per block. This is called per tagged
  account during reconciliation → O(accounts × blocks) VerifiedGet calls.
  Switch to `GetZKBlockByNumberFast`. Also fix `GetTransactionsByAccountPaginated`
  (line 1576) which has the same issue.
- Data structures: none new.
- Files: `DB_OPs/account_immuclient.go`
- Done when: both functions call `GetZKBlockByNumberFast`, no `GetZKBlockByNumber`
  call remains inside a block-scan loop.

---

## Phase 5: Remove JSON round-trip in `GetTransactionsForAccount` (#15) — DONE
- What: `immudb_account_manager.go:40-48` marshals each `config.Transaction` to JSON
  then unmarshals into `types.DBTransaction` just to convert types. Direct field copy
  eliminates two allocs + two reflect traversals per transaction.
- Files: `DB_OPs/Nodeinfo/immudb_account_manager.go`
- Done when: no `json.Marshal` / `json.Unmarshal` in the tx conversion loop.

---

## Phase 6: Build verification — DONE
- What: `go build ./...` — zero errors, zero new import cycles.
- Done when: clean build across all changed packages.
