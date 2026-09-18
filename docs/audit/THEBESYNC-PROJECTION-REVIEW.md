# Review: thebesync catch-up projection loss → tx_count_sent fingerprint corruption (D-64…D-67)

**Date:** 2026-09-18 · **Branch:** `fix/thebesync-projection-and-txstats` (off `v3base` @ 44f052f).
**Incident (testnet, 30 validators):** every block applied via catch-up (822, 827–835) got a `blocks`
row but no `snapshots`/`transactions` rows; the missing tx rows made `tx_count_sent` (a state-fingerprint
field) go one low, causing `STATE DIVERGENCE` on the next block; catch-up took two reconcile cycles per
block and the second pass stored the block **unverified**, advancing the head past the fail-closed gate.

**Decision: SEV-1 consensus-liveness + silent-corruption defect chain. Root cause is a snapshot write that
was gated on a ZK proof; the projection loss then corrupted a fingerprint field that is derived from that
same projection, and a marker-before-verify ordering let the fail-closed check be skipped on retry.**

## Confirmed mechanism (verified by inspection)

1. `DB_OPs.StoreZKBlock` wrote the block, then wrote the snapshot **only when the block carried a ZK proof**
   (`thebe_ops.go:163`, snapshot lives inside the proof-gated `backend.StoreZKBlock`), then wrote each tx.
   `snapshots` is the FK parent of `transactions` (`thebeprofile/schema.go` `fk_txn_snapshot`), so a
   proofless block → no `snapshots` row → every `applyTransaction` violates the FK (23503).
2. Catch-up blocks arrive proofless: `GetZKBlockByNumber` restores `ProofHash`/`StarkProof` **only** from
   the `zk_proofs` table via `GetZKProof`, gated `if err == nil` (`thebe_ops.go:248`) — a failed/absent
   proof read was silently swallowed; `StoreBlock` persists the block via `toBlockRecord` (no `proof_hash`),
   and `blockRecordToZKBlock` does not restore `ProofHash` from `extra_data`. So a node missing its own
   `zk_proofs` row serves a proofless block, and the loss cascades node-to-node.
3. `tx_count_sent` (and `tx_nonce`) were re-derived per sender from the `transactions` projection via
   `RefreshAccountTxStats` (`thebe_ops.go:180` → `reader.go:222 sqlRefreshAccountTxStats`). A missing tx row
   drops the count by one. `AccountLeaf.TxCountSent` is in the state fingerprint
   (`consensushash/state_fingerprint.go`), so the sender's leaf flips → `STATE DIVERGENCE`. The apply path
   already maintains both fields authoritatively (`account_recon.go` `TxCountSent++`, `TxNonce=nonce+1`;
   persisted by `apply_account.go`; monotonic-guarded by `merge_account.go`), so the refresh was a redundant
   recompute-from-a-rebuildable-view that clobbered correct values.
4. `ProcessBlockTransactions` wrote the block-processed marker (`Processing.go:520`) **before** the
   fingerprint check (`:565`), and did not roll back the applied account writes on a failed check. Pass 1
   failed the check (state applied + block marked); pass 2 hit the marker, took the "already processed,
   skipping" path (no fingerprint check), and stored the block unverified. The head advanced.

## Findings register (proposed D-64…D-67; existing rows unchanged)

| ID | SEV | Finding | Fix | Test |
|---|---|---|---|---|
| **D-64** | 1 | Snapshot write proof-gated → proofless (every catch-up) block gets a `blocks` row, no `snapshots` row → every tx insert violates `fk_txn_snapshot`; tx+snapshot projection lost (`thebe_ops.go:163`, `backend/zkproof.go`) | `backend.StoreZKBlock` writes **block → snapshot (always) → [zkproof if present] → transactions**; `DB_OPs.StoreZKBlock` routes through the one idempotent chain (also removes the prior double-write of block+txs on the proof path) | `backend/zkproof_snapshot_test.go` (2 cases) — proofless block writes a snapshot, skips zkproof; proof block writes both |
| **D-65** | 1 | `GetZKBlockByNumber` silently swallows a ZK-proof read error (`thebe_ops.go:248` `err==nil`), serving a proofless block that (pre-D-64) loses its projection and cascades | Surface any non-"not found" proof-read error; "not found" stays legitimate (proofless blocks project fully under D-64) | *(see B residual — provider/applier round-trip test to add)* |
| **D-66** | 1 | `tx_count_sent`/`tx_nonce` (fingerprint fields) re-derived from the rebuildable `transactions` projection by `RefreshAccountTxStats` in `StoreZKBlock` → go low on any missing tx row → `STATE DIVERGENCE` (`thebe_ops.go:180`, `reader.go:222`) | Removed the refresh from `StoreZKBlock`; the apply path (`account_recon`/`apply_account`/`merge_account`) is the single source of truth. `tx_nonce` recompute (`MAX(nonce)+1`) can also disagree with the applied nonce under accepted future-nonce gaps (`Security.go:620`) — same removal | *(unit: apply increments leaf without reading tx table — to add in `DB_OPs`)* |
| **D-67** | 2 | Fingerprint check ran after the block-processed marker and left applied writes on failure → retry took the unverified "already processed" skip path, bypassing the fail-closed gate (`Processing.go:520` vs `:565`) | Check **before** the marker; on failure roll back the applied+marked prefix (revoke per-tx markers, then restore balances, under the state-apply lock) so no marker/state remains and a re-delivery re-verifies | covered by the reorder + `affected_accounts_test.go` |
| **D-67b** | 1 | **Rollback snapshot gap (found in review).** `originalState` was built from `affectedAccounts` = tx `From`/`To` + `CoinbaseAddr`/`ZKVMAddr` only; the reward split (`config.SplitFee`) credits every `block.FeeRecipients` address (the cert signers, normally none of those), which were **not** snapshotted. So BOTH rollback paths (the pre-existing in-loop `:479` **and** the new D-67 fingerprint path) restored everything except the reward credits, leaving them applied → re-delivery double-credits. Pre-existing since reward-split shipped. | Extract `affectedAccountsForBlock(block)` and include every `FeeRecipient.Addr` before `originalState` is captured (coinbase already covered → `SplitFee` fallback safe). | `affected_accounts_test.go` (2 cases): fee recipients present in the set; nil-`To`/nil-addr safe |

**Deliverable E (second-pass idempotency):** satisfied by D-64 plus the appliers already being
`ON CONFLICT DO NOTHING` (`apply_snapshot.go`, `apply_transaction.go`): a second pass over a
partially-projected block now completes the missing snapshot/tx rows instead of FK-failing to an
undrained outbox.

## Residuals / not done this pass
- **B (applier refuse):** `thebesync/apply.go` should refuse to advance the head when a store did not fully
  project (block ∧ snapshot ∧ txs) — designed, not yet wired here; and the caller's "already processed"
  skip path should re-verify. With D-67, a failed verify no longer leaves a marker, which already closes the
  observed bypass; the applier-side guard is defense-in-depth.
- **F (`reproject <from> <to>` CLI):** design only — rebuild `snapshots`/`transactions` from the KV
  canonical log for a range; replaces the operator psql copy in `jmdt-devnet` TESTNET-RUNBOOK §5c.
- **Cache note:** routing `DB_OPs.StoreZKBlock` through `backend.StoreZKBlock` bypasses the block/tx cache
  decorators on write. Safe for these append-only, immutable records (no stale positive entry can exist;
  reads populate on miss), but noted for review.

## Test status (HONEST)
**Untested: execution was not available in this environment** (no Go toolchain on PATH; empty module cache
with ~444 MB free and `/sessions` full — the touched packages pull go-ethereum/badger/postgres/duckdb and
cannot be built here). Validate on a build host, `GOWORK=off`, judged against the 10 baseline failures on
v3base (AVC-CONSENSUS-HANDOVER §7.1):

```
GOWORK=off go build ./...
GOWORK=off go test ./DB_OPs/backend/...            # D-64 (new zkproof_snapshot_test.go)
GOWORK=off go test ./DB_OPs/...                     # D-65/D-66 (thebe_ops)
GOWORK=off go test ./messaging/BlockProcessing/...  # D-67 (Processing reorder)
gofmt -l DB_OPs messaging/BlockProcessing           # must print nothing
```
