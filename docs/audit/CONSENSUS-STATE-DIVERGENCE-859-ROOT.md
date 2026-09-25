# Root cause + fix: fleet-wide P2.5 state-divergence halt at block 859

Status: **root confirmed empirically; fix designed, not yet implemented** (consensus-critical,
requires host build + 2-node gate before any fleet deploy). Owner branch: `fix/evm-rpc-parity-v3base`.

---

## 1. Symptom

All 30 validators halt at block 858; the sequencer runs ahead (862+). Each validator applying
block 859 fail-closes:

```
STATE DIVERGENCE — post-apply fingerprint does not match block-carried value; rolling back and halting
block_number=859 local_fingerprint=0x6d15ba4f… block_fingerprint=0x75069a4e…
```

`tools/fleet_audit.sh 855` shows every node byte-identical to the sequencer through **858** (block
hash, parent, tx list, state root — and therefore the P2.5 fingerprint, which is folded into the
block hash), and stuck at 858. So the fleet is healthy and unanimous through 858; only 859 fails,
fleet-wide, with all nodes computing the **same** divergent fingerprint `X` while the sequencer
stamped `Y`. The sequencer's `Y` is the outlier.

Block 859 is a plain value transfer: `0.05` from `0x69c7…6612` (nonce 40) to a **new** EOA
`0xd3d5…4a1` (`eth_getCode` = `0x`; balance/nonce 0 at 858). 860–862 are the same pattern.

## 2. Confirmed root cause

The `accounts` table has a `BEFORE UPDATE` trigger that overwrites `updated_at` with wall-clock
`NOW()` on every row change:

- `DB_OPs/thebeprofile/schema.go` — `fn_accounts_set_updated_at` / `trg_accounts_updated_at`
- `DB_OPs/thebegateway/migrations/000001_init_schema.up.sql` — same

The projection upsert (`DB_OPs/thebeprofile/apply_account.go`, `sqlUpsertAccount`) gates every
account write with a last-write-wins clause on that column:

```sql
ON CONFLICT (address) DO UPDATE SET balance_wei=…, tx_nonce=…, tx_count_sent=…, updated_at=EXCLUDED.updated_at
WHERE accounts.updated_at < EXCLUDED.updated_at
```

The apply path writes a **block-derived** `updated_at = blockTimestamp` (Processing.go:1143, 1256,
1489, 1545), but the trigger throws it away and stamps **local wall-clock `NOW()`**. Proof — the
same accounts carry different `updated_at` on different nodes:

| account | sequencer `updated_at` | follower `updated_at` |
|---|---|---|
| `0x69c7…6612` | 2026-09-25 15:12 UTC | 2026-09-25 18:00 UTC |
| `0x30242E…1DD5` | 2026-09-25 15:12 UTC | 2026-09-25 18:00 UTC |

Because `updated_at` is node-local wall-clock, the LWW gate makes **non-deterministic keep/reject
decisions** for the same block's writes:

- Block 859's write carries `EXCLUDED.updated_at = 859's block timestamp` (early, ~09:xx UTC).
- A follower's stored `updated_at` is a **later** wall-clock (it applied 858 late on catch-up, and
  each 859 retry+rollback re-stamps `NOW()` → now 18:00 UTC).
- Gate: `18:00 < 09:xx` → **false → 859's balance/nonce updates to existing accounts are rejected**
  on the follower (sender stays at 858 nonce 39 vs the sequencer's advanced value; the recipient
  stays 0). → fingerprint `X` ≠ `Y` → rollback → retry, which re-stamps `NOW()` even later →
  **permanently unrecoverable.**

The sequencer, applying blocks in real time (its `updated_at` ≈ block time), keeps the writes, so
its state (and stamped `Y`) is correct. This is a pure determinism defect: **the ordering key the
consensus LWW gate depends on is not a function of block content.**

Why 859 and not 855–858: the followers were caught up when 855–858 applied (their stored
`updated_at` ≈ those blocks' times, so the gate passed); they fell behind at the 858/859 boundary
(the seat-fix stall), so 858 was applied late, poisoning the 859 gate.

## 3. Why it is NOT a one-line trigger removal

Within a single block, every account write uses the **same** `blockTimestamp`, and some accounts
are written **multiple times** — the coinbase and zkvm are credited **once per transaction**
(`Processing.go` `coinbaseCredits` loop + `addToRecipient(zkvm,…)`, committed per-tx by
`ApplyTxAtomic`). With a strict `<` gate and a constant block-timestamp, the 2nd/3rd credit to the
coinbase in a multi-tx block would be **rejected**. The trigger's ever-increasing `NOW()` is
currently the only thing giving those repeated intra-block writes a strictly-increasing key.

So `updated_at` is overloaded with two conflicting jobs:

- (a) **cross-node determinism** → needs a block-derived value;
- (b) **intra-block / re-delivery write ordering** → needs a strictly-increasing value.

`NOW()` satisfies (b) and breaks (a) (the bug). A naive switch to pure block-timestamp fixes (a)
and breaks (b) (under-credits coinbase/zkvm in multi-tx blocks). The fix must satisfy both.

## 4. Fix — Option 1 (recommended): block-atomic apply + deterministic ordering

Collapse each account to **one write per block** with its final accumulated value, committed
**once, only after** the block passes the P2.5 fingerprint gate. This is the `STO-26`
block-atomic-apply change already noted in `docs/audit/THEBE-AUDIT-HLD.md`, and it fixes three
things at once:

1. **Determinism** — one write per account per block with `updated_at = blockTimestamp`; no
   intra-block repeats to order, so the `<` gate is deterministic across nodes.
2. **The rollback-stub class** — nothing is committed until the block passes the gate, so a failed
   block leaves no partial account state (no balance-0 recipient stub, no poisoned `updated_at`).
3. **Re-credit blocking** — gone, because the poison (per-retry `NOW()` stamping) no longer occurs.

### 4.1 Ordering key (must be deterministic AND globally monotonic)

Use a composite that strictly increases across blocks and is identical on every node. Options:

- Preferred: keep `updated_at` as the real block time for display/history, and make the LWW gate
  compare on **`(block_number)`** instead of `updated_at` — add a `last_block` column to `accounts`,
  set it to the applying block number, and gate with `WHERE accounts.last_block < EXCLUDED.last_block`.
  Block numbers are strictly increasing and block-derived → deterministic and monotonic, with no
  same-second-block edge case. `updated_at` then becomes advisory (drop it from the gate) and the
  `NOW()` trigger can stay for display without affecting consensus.
- Alternative (no schema column): `updated_at = blockTimestamp*1e9 + block_number` (nanoseconds +
  block number as tie-break), drop the `NOW()` trigger. Simpler migration but overloads a
  `TIMESTAMPTZ` with a synthetic value; verify no reader depends on `updated_at` being real time
  (`idx_accounts_updated_at`, historical-balance reads).

Recommendation: the `last_block` column — it separates the consensus ordering key from the human
`updated_at` cleanly and has no timestamp-encoding hazards.

### 4.2 Apply-path change (block-atomic staging)

`messaging/BlockProcessing/Processing.go`:

- Replace the **per-tx** `ApplyTxAtomic` commits with a **block-level stage**: accumulate all
  account mutations across every tx (and the contract path, `contract_apply.go`) into one map
  keyed by address, holding each account's final value.
- Compute the P2.5 fingerprint over `(current committed state + staged mutations)` **without
  committing** (in-memory overlay), or commit to a scratch/transaction that is only made durable
  after the gate.
- Only if `block.StateFingerprint == fp` (or producer stamps it): commit the whole staged set +
  all per-tx markers in one atomic batch. On mismatch: discard the stage (nothing was written → no
  rollback needed, no stub).
- This subsumes the current `rollbackState` / `originalState` machinery (there is nothing to roll
  back if nothing was committed).

### 4.3 Writer audit (must all set the ordering key)

Every account writer must set the new key (`last_block`, or the deterministic `updated_at`):

- `Processing.go` apply path (sender/recipient/coinbase/zkvm/fee-recipient/new-account) — set to
  `block.BlockNumber`.
- `contract_apply.go` — same.
- `DB_OPs/thebe_missing.go` `UpdateAccount` / `rollbackState` (removed under 4.2) / `BatchRestoreAccounts`
  (`mergeAccountForWrite`) — sync/restore path: gate on `last_block` consistently.
- `DB_OPs/backend/account.go` `UpdateAccountBalance` — **currently sets `updated_at = time.Now()`**
  (wall-clock); confirm it is not on the consensus apply path, and give it a block-derived key or
  keep it off the gated path.
- Genesis/seed writers (`DB_OPs/genesis_seed.go`, `genesis_block.go`) — set `last_block = 0`.

## 5. Fix — Option 2 (smaller, if block-atomic is too large now)

Keep per-tx commits but make the ordering key deterministic-monotonic per write:
`updated_at = blockTimestamp*1e9 + txIndex`, and **drop the `NOW()` trigger**. Handles intra-block
repeats (tx_index increases per tx) and cross-node determinism. **Edge case:** two blocks in the
same wall-second regress (`blockTs*1e9` equal, `txIndex` resets) → incorporate `block_number`.
Does not fix the rollback-stub class. Only take this if Option 1 can't land in the recovery window.

## 6. Recovery runbook (independent of which fix)

The fix corrects forward behavior; it does not un-poison existing state.

1. Land the fix on `fix/evm-rpc-parity-v3base`; host build + **2-node gate** (§7) before any deploy.
2. **Pre-flight:** `select count(*) from accounts where …` empties (already 33 committed — benign,
   consistent fleet-wide) and confirm the migration (drop trigger / add `last_block`) applies clean.
3. Deploy fleet-wide **together** (consensus change).
4. **Rebuild follower state from a clean source** — their `updated_at`/gate keys are poisoned
   wall-clock and their 859 attempts left stubs. Resync each validator from the sequencer's
   authoritative state (or wipe + full sync).
5. **Sequencer: roll back to 858 and rebuild 859+** — its stamped `Y` for 859–862 was produced
   under the broken semantics; re-produce those blocks under the fix.
6. Watch `Committee-source: ordered buddy candidates by seat` (seat fix) and the P2.5 gate: no
   `STATE DIVERGENCE` on the rebuilt 859+, heads advance fleet-wide.

## 7. 2-node acceptance gate (must pass before fleet deploy)

- Two-node cluster (1 sequencer + 1 validator), contracts enabled.
- Produce a **multi-tx block** (≥3 txs, several crediting the same coinbase) → the follower applies
  it with **identical fingerprint** and correct coinbase/zkvm totals (proves intra-block ordering
  survives the fix).
- Make the follower **fall behind**, then catch up multiple blocks late → fingerprints still match
  (proves the wall-clock dependence is gone — this is the exact 859 scenario).
- Fail a block deliberately (force a fingerprint mismatch) → confirm **no partial account state**
  is left (no stub, no poisoned key) and the node retries cleanly (proves the block-atomic / stub
  fix).
- `go test ./consensushash/ ./messaging/BlockProcessing/ ./DB_OPs/...` green;
  `CGO_ENABLED=1 go build ./...`.

## 8. Related work already on the branch

- `465d3e4` — P2.5 halt leaf-logging (dumps affected-account leaves before rollback; use it during
  the 2-node gate to see the divergent leaf directly).
- Seat fix (`fix/committee-v2-seat-dial-enrichment`, PR to `v3base`) — unrelated consensus-seating
  bug fixed earlier; the sequencer already runs it (`391c3f0`).
- `05bc6a1` reverted an unconditional EIP-161 empty-account skip: **not** deployable unconditionally
  (33 committed empty accounts span all history → would break historical replay). If wanted later,
  it must be **activation-height-gated**; it is future hardening, not part of this fix.

## 9. Confidence

- Root (trigger → non-deterministic `updated_at` → non-deterministic LWW → 859 divergence): **~85%**,
  empirically supported by the per-node `updated_at` divergence + the gate SQL. To reach 100%: the
  §8 leaf-log on a clean re-apply showing the sender/coinbase leaf (not the recipient) is the
  first-attempt divergent one, and a 2-node reproduction of the catch-up-late scenario.
- The intra-block-ordering complication (why a naive trigger drop breaks coinbase/zkvm crediting):
  **confirmed** from the constant-`blockTimestamp` writes + strict `<` gate + per-tx coinbase credit.

---

## 10. IMPLEMENTED (determinism core of Option 1) — `fix/evm-rpc-parity-v3base`

Rather than the full `last_block` column + block-atomic restructure (large, cross-package, and
unbuildable in the dev sandbox), the **determinism root** is fixed with a contained, two-edit change
that keeps the existing `updated_at` plumbing (which the apply path already writes as a block-derived
value) and only removes its non-determinism:

1. **`DB_OPs/thebeprofile/schema.go`** (+ mirrored in `DB_OPs/thebegateway/migrations/000001_init_schema.up.sql`):
   `fn_accounts_set_updated_at` is now a **safety net only** — it stamps `NOW()` **only when the
   caller left `updated_at` unset** (`NULL`/epoch). A block-derived timestamp is preserved untouched,
   so `updated_at` is deterministic across nodes for every consensus write. Applied to existing DBs
   automatically on restart (the profile re-runs `CREATE OR REPLACE FUNCTION`).
2. **`DB_OPs/thebeprofile/apply_account.go`**: the LWW gate is `WHERE accounts.updated_at <=
   EXCLUDED.updated_at` (was `<`). `<=` is required because within a block the coinbase/zkvm are
   written once per tx with the SAME block timestamp; `<` dropped all but the first (under-crediting
   fees). `<=` lets same-block and same-second-block writes land in order; stale older blocks are
   still rejected; true replays are still marker-guarded upstream.

Net effect: two nodes applying the same block compute the same `updated_at` and make the same
keep/reject decisions → no wall-clock dependence → the 859 class of divergence cannot recur.

Writer-audit result (why removing the clobber is safe): the consensus apply path
(`Processing.go` `addToRecipient`/`deductFromSender`/new-account, `contract_apply.go`) writes
`updated_at = blockTimestamp`. The only wall-clock account writer is `backend.UpdateAccountBalance`
(`time.Now()`), used by genesis/seed only — one-time, early, and `<=`-compatible. The conditional
trigger still covers any writer that leaves `updated_at` unset.

**Not implemented (optional future hardening, unchanged from §4/§5):** the `last_block` column and
full block-atomic apply. They are cleaner (self-documenting key; self-healing recovery; also kills
the rollback-stub class) but require the large struct/converter threading + a build/2-node loop.
The committed change fixes the determinism root; the rollback-stub remains benign under `<=` (a
re-applied block overwrites a stub) once state is un-poisoned.

### 10.1 Recovery (the committed fix does NOT self-heal poisoned nodes)

The 30 followers currently hold `updated_at = wall-clock NOW()` (e.g. 18:00 UTC) on existing
accounts, which is **newer than block 859's embedded timestamp**, so even after this fix a re-apply
of 859 is still gated out (`18:00 <= 09:xx` is false). One-time reset per validator, in order:

1. Build + **2-node gate** (§7) on the host; deploy the new binary fleet-wide and restart (the
   `CREATE OR REPLACE` updates the trigger; the `<=` gate ships in the binary).
2. On each stuck validator, reset the poisoned key to a sentinel **after** the epoch but **before**
   any real block time, so the conditional trigger preserves it and the gate then admits 859+:
   ```sql
   UPDATE accounts SET updated_at = timestamptz '2001-01-01 00:00:00+00';
   ```
   (A value `<= to_timestamp(0)` would be re-stamped to `NOW()` by the safety-net trigger — use a
   real post-epoch sentinel like above.) Alternatively, full resync the validator.
3. Let the validator re-apply 859→tip; confirm no `STATE DIVERGENCE` and heads advance.
4. Sequencer: no rebuild needed for this fix specifically — its state was always correct (its writes
   landed); once followers admit 859 with matching fingerprints they converge to the sequencer.

### 10.2 Status

- gofmt-clean; **UNTESTED-FOR-COMPILE / UNTESTED-FOR-BEHAVIOR** in the dev sandbox (private ThebeDB
  module). Host gate: `CGO_ENABLED=1 go build ./... && go test ./messaging/BlockProcessing/ ./DB_OPs/...`
  plus the §7 2-node gate (multi-tx coinbase crediting + catch-up-late reproduction) BEFORE any
  fleet deploy.
- Consensus-relevant (changes the account LWW semantics) → deploy fleet-wide together.
