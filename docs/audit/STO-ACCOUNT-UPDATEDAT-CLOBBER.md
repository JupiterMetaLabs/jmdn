# STO — `storeAccount` clobbers caller `updated_at` (rollback-safety trap)

Status: **OPEN — tracked ticket, do NOT drive-by fix.**
Severity: Medium (silent state corruption under rollback/replay; not a live crash).
Component: `DB_OPs/thebe_ops.go` — `storeAccount`, `StorePropagatedAccount`.
Filed from: PR review B3. Confirmed against source on branch base `816ba8f`.

## The defect

`storeAccount` overwrites the account's `updated_at` with wall-clock `now()`
regardless of what the caller passed:

```go
// DB_OPs/thebe_ops.go
func storeAccount(PooledConnection *config.PooledConnection, KeyDoc *Account) error {
    ...
    now := time.Now().UTC().UnixNano()          // line ~380
    return h.CreateAccount(ctx, &store.Account{
        ...
        CreatedAt: KeyDoc.CreatedAt,
        UpdatedAt: now,                          // line ~391  <-- clobber
    })
}
```

`StorePropagatedAccount` (line ~353/362) does the same `now := time.Now()...`
clobber — and its doc comment claims it "perfectly preserv[es] ... other
properties," which is now false for `updated_at`.

## Why it is a trap (not a trivial fix)

`updated_at` is used as a recency signal. If a rollback or snapshot-restore path
writes an OLDER account state back through `storeAccount`, the restored row gets
a NEWER `updated_at` than the state it represents. Any later logic that treats
"higher `updated_at` wins" (reconciliation, last-writer conflict resolution,
monitoring/freshness checks) then prefers the rolled-back-over value, silently
re-corrupting the state the rollback was meant to repair.

The naive fix — "honor `KeyDoc.UpdatedAt` instead of `now()`" — is NOT safe on
its own, because:

1. There is currently **no** account snapshot-restore primitive in the tree
   (`grep -rn "RollbackState\|RestoreAccountFromSnapshot" DB_OPs/` returns
   nothing). The rollback semantics that would consume a caller-supplied
   `updated_at` are undefined, so the correct value to preserve is undefined.
2. Every existing caller relies on the `now()` stamp today. Flipping to
   caller-supplied timestamps changes the recency ordering of the entire
   account table and must be validated end-to-end, not per-callsite.

## Required order of work (do these BEFORE touching `storeAccount`)

1. **Re-derive the RCA.** Do not assume this ticket's mechanism is the whole
   story. Confirm exactly which read paths order on `updated_at` and what a
   rollback is expected to restore.
2. **Land the landing-guard test first:** `TestRollbackState_RestoresOlderSnapshot`
   (placeholder committed alongside this ticket in
   `DB_OPs/Tests/thebe_ops_updatedat_guard_test.go`, currently `t.Skip`). It must
   assert: after applying account state at T2 and then restoring a snapshot
   captured at T1 (T1 < T2), a subsequent read returns `updated_at == T1`, NOT
   `now()`. This test must FAIL against today's `storeAccount` and PASS only
   after the fix — that is what makes it a guard.
3. Only then change `storeAccount`/`StorePropagatedAccount` to honor the
   caller's `updated_at`, and fix the `StorePropagatedAccount` doc comment.

## RECOVERY WARNING — do not run blind

Do **NOT** run any operational recovery of the form:

```sql
-- §10.1 recovery — DO NOT RUN until the RCA above is re-derived
UPDATE accounts SET updated_at = <derived_ts> WHERE ...;
```

until the RCA is re-derived. First run the read-only check to see what is
actually affected:

```sql
-- SELECT check FIRST (read-only): find rows whose updated_at is implausibly
-- newer than the block that last touched them.
SELECT address, updated_at
FROM accounts
ORDER BY updated_at DESC
LIMIT 200;
```

A mass `UPDATE` of `updated_at` computed from a wrong RCA would itself re-stamp
every row and destroy the very evidence needed to reconstruct the correct
timestamps. Read first, derive, guard-test, then fix.
