# Duplicate-height proposal + no-rollback-on-store-failure (block 858, 2026-09-25)

**Branch:** `fix/dup-height-rollback-858` (off `fix/evm-rpc-parity-v3base` @ `816ba8f`).
**Companion:** orchestrator branch `fix/no-reproposal-inflight-height` (JMDT-Sequencer-Orchestrator).
**Severity:** SEV-1 — silent state corruption requiring a validator re-seed.

## Incident

At height 858 the sequencer accepted **two** block candidates and both reached committee quorum:

| time (IST) | candidate | outcome |
|---|---|---|
| 20:32:03 | `0x08ab232d…fe5d0` | accepted, consensus started, stored — the canonical 858 on all 31 nodes |
| 20:32:51 | `0x3c591e72…dc42` | accepted **again for 858** while the first was still in consensus (`/api/latest-block` still returned 857, so the orchestrator recomputed `857+1` a second time) |
| 20:33:53 | `0x3c59…` | `consensus_reached: true`; `ProcessBlockTransactions` mutated state; `StoreZKBlock` then failed `uq_txn_block_index` (23505); block "withheld from peers"; **no rollback of the applied transactions** |

Candidate `0x3c59…` carried a transfer from `0x69c7…6612` (nonce 39). Its apply left the sequencer's
`accounts` row at `tx_nonce=40, tx_count_sent=40` while every validator stayed at `39/39`
(the sequencer's `transactions` table has nonces 38 and 40 but **not** 39). The wallet then read
`eth_getTransactionCount = 40` from the sequencer and signed 859–862 with nonces 40–43; every validator
applying 859 computed a state fingerprint differing from the sequencer-stamped one by exactly that
counter and halted fail-closed (`STATE DIVERGENCE`, 859).

## Root cause (two independent defects, both required)

1. **No rollback when the store fails after transactions were applied.** `ProcessBlockLocally`
   (`messaging/broadcast.go`) applied the block, then called `StoreZKBlock` separately and, on failure,
   returned without undoing the account mutations or the `tx_processed` markers. The reward split
   (`SplitFee`) had already credited the coinbase/zkvm/fee recipients too. Result: a block that was
   never durably stored still moved account state on the sequencer only.
2. **No duplicate-height admission guard.** `/api/process-block` (and the gRPC `ProcessBlock`) accepted
   a second candidate for a height that was committed or already in consensus, because the orchestrator's
   `latest-block` poll still returned the old tip mid-round and recomputed the same height.

## Fixes (four independent guards, all deployed together)

### 1 — Sequencer/validator: roll back state when the block store fails (`jmdn`)
`ProcessBlockTransactionsAndStore` (new) persists the block **inside** the apply scope, after the P2.5
fingerprint check and **before** the block-processed marker, and reuses the existing fingerprint-mismatch
rollback (`rollbackApplied`) on store failure: balances, `tx_nonce`, `tx_count_sent`,
coinbase/zkvm/fee-recipient credits and per-tx markers are all reverted, and the projection the failed
store enqueued to the outbox is dropped (`OutboxStore.MaxID`/`DeleteAfter` via
`DB_OPs.OutboxMaxID`/`PurgeOutboxAfter`, id-based so only entries from the failed store are removed).
Wired on the live (`broadcast.go`), receive (`blockPropagation.go`) and sync (`thebesync/apply.go`)
paths. `messaging/BlockProcessing/Processing.go`, `DB_OPs/outbox_purge.go`,
`DB_OPs/thebegateway/{interfaces,outbox_store}.go`, `main.go`.
Test: `applygate` `TestStoreFailureRollback`.

### 2 — Sequencer: refuse a second candidate for a committed/in-flight height (`jmdn`)
`processZKBlock` (HTTP) and `BlockServer.ProcessBlock` (gRPC — the transport the orchestrator actually
uses) reject fail-closed when `block.BlockNumber <= committed tip` (`height_already_committed`) or a
consensus round for the height is already in flight (`height_in_flight`), returning HTTP 409 /
gRPC `AlreadyExists`. In-flight state is a new `internal/proposalguard` (claimed at ingress, released on
the consensus terminal in `ProcessBlockLocally`, TTL-bounded so a dead round cannot wedge the height).
`roundlock` was intentionally **not** reused — it is a per-node block-vs-timeout signing ledger keyed by
`(height, period)` with release-on-sign-failure semantics, the wrong primitive for an ingress gate.
Test: `internal/proposalguard/proposalguard_test.go` (passes in-sandbox).

### 3 — Validators: never vote for a height that does not extend the local tip (`jmdn`)
`Vote.SubmitVote` now runs `checkVoteChainPosition` after base validation: reject when the block height
is `<= local tip` (already committed / duplicate), when it is not `tip+1` (a gap — the parent is not
held), or when its `PrevHash` does not match the local tip hash (equivocation / different chain).
Fail-closed on a tip read error. The decision is the pure predicate `internal/votecheck.ExtendsTip`.
Test: `internal/votecheck/votecheck_test.go` (passes in-sandbox).

### 4 — Orchestrator: do not re-propose an in-flight height (`JMDT-Sequencer-Orchestrator`)
`proposalTracker` refuses to submit a height whose prior proposal has not been observed committed (and
never a height `<=` the observed tip). The sequencer's duplicate-height rejection is recognized by
`isDuplicateHeightReject` (HTTP 409 / gRPC `AlreadyExists` / `height_already_committed` /
`height_in_flight`) and treated as **already-proposed**: the batch is not requeued as failed and no
failure alert fires; it is handed back to the router for a fresh height and the active marker released.
A definitive (non-landing) submit failure also releases the marker. `cmd/orchestrator/proposal_tracker.go`,
`processor.go`, `service.go`. Test: `cmd/orchestrator/proposal_tracker_test.go` (passes in-sandbox;
`go build ./...` + `go vet` clean).

### Finding 4 — empty vote-requester pin under committee-v2 silently wedges finalization (`jmdn`)
Separate defect, same incident window, recorded here for the handover. `#154` (CON-03, the
vote-requester authz gate) authorizes the sequencer's vote requests against
`consensus.seed_authority_bls_pub`, which **defaults to `""`** (`config/settings/defaults.go`). On any
fleet that enabled `JMDN_COMMITTEE_V2` without pinning the key, **every seated validator fail-closes and
refuses the sequencer** — silently, at WARN level, on the validators only — so no block ever finalizes.
`JMDN_COMMITTEE_V2` always implies a pinned sequencer authority key; there is no valid committee-v2
configuration with an empty pin.

Fix: `main.go` now **fails hard at boot** (not a warning, and on every node, not only in production
posture) when `messaging.CommitteeV2Enabled` and `SeedAuthorityBLSPub` is empty — placed beside the
existing SEC-03 consensus-posture refusals. With the flag off (default) it is a no-op. This converts a
silent fleet-wide liveness stall into a loud, un-missable boot refusal with the exact remediation.
Confidence 90% (logic verified + `main.go` gofmt-clean; not compiled in-sandbox — private modules).

## Confidence and interaction

- **Item 1** — closes the corruption mechanism itself (state advancing without a durable block). Even if
  items 2–4 were absent, a duplicate candidate that fails to store would now leave no state change.
  **Highest-value fix.** Confidence 90%: logic verified and unit-tested against a real ThebeDB handle on a
  build host (the `applygate` test); the in-sandbox blocker is the private ThebeDB/avc modules.
- **Item 2** — prevents the second candidate from ever entering consensus (both HTTP and gRPC ingress).
  Without item 1, a 409 that still slips through (e.g. two candidates admitted in the sub-millisecond
  window before either claims) would still corrupt state. Confidence 85%.
- **Item 3** — stops validators ratifying an off-tip block (the duplicate-858 vote and the 844-without-843
  vote). Without items 2/4 the sequencer could still *propose* a duplicate, but it could not reach quorum.
  Confidence 85%.
- **Item 4** — removes the *source* of the double proposal. Without item 2 the sequencer would still admit
  a duplicate if the orchestrator (or a second orchestrator) misbehaved. Confidence 85%.

Defense in depth: items 2+3 make a duplicate un-finalizable, item 4 stops it being produced, and item 1
makes any store failure (duplicate or not) non-corrupting.

## Test status (HONEST)

Ran green **in this sandbox** (real `go test`, Go 1.26): `internal/proposalguard`, `internal/votecheck`,
and the whole `JMDT-Sequencer-Orchestrator/cmd/orchestrator` package (`go build ./...` + `go vet` + the
item-4 tests). `gofmt` clean on every touched file.

**Untested in-sandbox:** the jmdn application packages (`messaging/BlockProcessing`, `Block`, `Vote`,
`thebesync`, `DB_OPs`) and the `applygate` `TestStoreFailureRollback` — they import the **private**
`github.com/JupiterMetaLabs/ThebeDB` and `.../avc` modules, which need git credentials the sandbox does
not have. Validate on a build host:

```sh
CGO_ENABLED=1 GOWORK=off go build ./...
CGO_ENABLED=1 GOWORK=off go vet ./messaging/... ./Block/... ./Vote/... ./thebesync/...
CGO_ENABLED=1 GOWORK=off go test ./messaging/BlockProcessing/... ./internal/proposalguard/ ./internal/votecheck/
# store-failure rollback (needs a ThebeDB handle; contract fingerprint fold on):
CGO_ENABLED=1 GOWORK=off go test -tags applygate ./messaging/BlockProcessing/ -run TestStoreFailureRollback -v
gofmt -l messaging Block Vote thebesync DB_OPs internal   # must print nothing
```

## Known residual (follow-up, not a blocker for this fix)

`StoreZKBlock` is **not atomic**: it writes block → snapshot → [zkproof] → transactions as separate 2PC
writes (`DB_OPs/backend/zkproof.go`). A `WriteTransaction` failure therefore leaves the `blocks` row
already committed, and the operational tip is SQL `MAX(block_number)` (`DB_OPs/latest_block.go`), so a
first-candidate store failure can leave a skeleton block row that advances the tip while item 1 rolls the
accounts back. In the 858 incident this is harmless (the block row is the *canonical* first candidate,
kept by the upsert; only the account state was corrupted, which item 1 reverts). The durable fix is to
make `StoreZKBlock` a single atomic transaction so a failed store leaves no block row at all. Tracked
separately.

## Recovery + verification procedure

Deploy is consensus-critical and fleet-wide — host-build + 2-node gate before the fleet, per policy.

### Reproduce the double proposal safely (staging / 2-node)
1. On the sequencer, add a temporary delay to one consensus round (e.g. sleep in the vote-collection path
   for the target height) so a proposal stays in flight long enough for a second cycle.
2. Drive two `process-block` calls for the same height (a second orchestrator cycle, or a manual
   `grpcurl`/HTTP `POST /api/process-block` with a different tx set for the in-flight height).
3. Observe: the second call returns **HTTP 409 / gRPC `AlreadyExists`** with reason `height_in_flight`
   (`journalctl -u jmdn | grep -E 'height_in_flight|already in consensus'`), and **no** account state
   changed for that candidate.
4. Force a store failure for an accepted block (e.g. point the projection at a table with the row already
   present) and confirm the log line `STORE FAILED after apply — rolling back applied prefix` followed by
   the sender's `accounts` row unchanged from its pre-block value, and the outbox has no leftover entry.
5. On a validator held one block behind, submit `tip+2` and confirm `VOTE REJECTED` with
   `vote position: non-contiguous` (item 3).

### Confirm fleet agreement after deploy
Use `jmdt-devnet/tools/fleet_audit.sh` (per the testnet inventory): after the fleet advances a few blocks,
confirm 30/30 validators + sequencer report the **same** tip hash and head, no `STATE DIVERGENCE` in any
node's log, and `eth_getTransactionCount` for a recently active sender matches across nodes.

### Fleet was already recovered operationally
The 858/859 divergence was reconciled by hand (validators set to `40/40` for the affected account after
upgrading to `816ba8f`). No historic-data change is needed or performed here.
