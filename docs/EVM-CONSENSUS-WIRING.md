# Wiring the EVM into consensus — design + phased plan

**Status:** Phase 0 landed (dormant seam). Phases 1–4 pending approval.
**Constraint (operator, 2026-08-17):** non-breaking; a NEW external AVC module will connect to
jmdn via adapters, so the EVM↔consensus seam must be an interface the external module can register
into — jmdn must not hard-depend on AVC internals, and AVC must not have to edit
`messaging/BlockProcessing`.

## Why a naive wire is unsafe (grounded in the audit)

Contract execution is currently unreachable from `main` (EVM-01). But the apply path
(`messaging/BlockProcessing/Processing.go`) runs on EVERY node and must be byte-for-byte
deterministic. The existing EVM injects, into what would become the apply path:

- **HTTP + `time.Now()` block context** — `evm/context.go:115` (`GET /api/latest-block`), `:79`
  (`GET /api/block/<n>`), `evm.go:56,126` (`time.Now()`). Non-fleet-uniform → instant fork. (EVM-02)
- **Go map-range commit order** — `contractdb.go:112` (`for addr,obj := range c.stateObjects`) →
  nondeterministic write order; **empty state root** at `:180`. (EVM-09)
- **Wrong fork** — silently runs London, not Shanghai vs a spec peer. (EVM-29)
- **Wrong gas** — `gasUsed = gasLimit - leftOver` skips intrinsic gas + refunds. (EVM-30/31)
- **Commit outside `LockStateApply`, not atomic with the account write.** (contractdb.go:105)

So "non-breaking" has exactly one safe meaning: ship the whole thing behind a **default-off flag**
(`cfg.Contracts.Enabled`, mirroring `cfg.Thebe.Enabled` and the FastSync flags, which shipped
dormant until validated), make it deterministic BEFORE anyone enables it, and gate enablement on a
2-node determinism test. A heterogeneously-flagged fleet forks (some nodes execute, some transfer),
so the flag flips fleet-wide, once, after validation.

## The seam (Phase 0 — DONE, `execbridge/execbridge.go`)

```
messaging/BlockProcessing  ──calls──▶  execbridge.ContractExecutor  ◀──registers──  EVM bridge (today)
        (consensus apply)                    (interface)                            External AVC module (later)
```

- `ContractExecutor` interface: `IsContractTx(tx)` (pure, deterministic) + `ExecuteTx(ctx, tx,
  BlockExecContext) (*ExecResult, error)`.
- `BlockExecContext`: ChainID, BlockNumber, BlockHash, ParentHash, Time (block ts), Coinbase,
  TxIndex, GasLimit — **all block-derived**; the interface forbids wall-clock/network I/O.
- `ExecResult`: Handled, Success, ContractAddress, GasUsed, BalanceChanges (absolute — folded back
  through the ONE centralized fee/merge path so the executor is never a second balance writer),
  StateRoot, Err.
- Default = a **no-op** executor: `IsContractTx` always false → apply path unchanged → merge is
  non-breaking. `SetExecutor` registers a real one only when the flag is on.
- Modelled on the two existing seams: `config.SetGlobalHandleFactory` + `store.ThebeHandle` (storage
  behind an interface+flag) and `DB_OPs/Nodeinfo/thebe_adapter.go` implementing JMDN-FastSync's
  `BlockInfo` (external repo defines the interface; jmdn supplies a thin adapter). The external AVC
  module registers via `execbridge.SetExecutor` exactly the same way.

Phase 0 changed no behavior: the seam is dormant, `cfg.Contracts.Enabled=false`, nothing calls the
EVM. Verified: `go build ./execbridge/ ./config/settings/` green (CGO off).

## Phases 1–4 (pending approval — each default-off, each its own gate)

**P1 — deterministic EVM bridge (SmartContract/execbridgeimpl or similar).** Implement
`ContractExecutor` over the existing EVM, but with `BlockExecContext` as the ONLY source of block
context — delete the HTTP/`time.Now()` path for the apply path (EVM-02); fix fork to Shanghai
(EVM-29); charge intrinsic gas + credit refunds via `core.StateTransition` or an equivalent, and
feed `GasUsed` back through `config.GasFee`/`SplitFee` (EVM-30/31); make `CommitToDB` iterate a
**sorted** key set and compute a real state root (EVM-09); take `LockStateApply` and commit contract
state atomically with the account write. Registered only when `cfg.Contracts.Enabled`.

**P2 — thread block context + invoke from the apply path.** Widen `processTransaction` to receive
BlockNumber/BlockHash/TxIndex (pure plumbing, no behavior change while flag off). For a tx where
`execbridge.Get().IsContractTx(tx)` is true, call `ExecuteTx`, fold `BalanceChanges`+`GasUsed` into
the existing staged `ApplyTxAtomic` commit under `LockStateApply`; deployments (`tx.To==nil`) no
longer hit the nil-recipient deref. Sequencer and buddy/verify paths both go through the same call.

**P3 — receipts, logs, contract-address propagation.** Persist receipts/logs with real
BlockNumber/TxIndex (EVM-13); reconcile with ADR-001 pull-on-demand; wire `SetSharedKVStore` so
`HasCode` works (EVM-16/25).

**P4 — state root in the block + verification.** Commit the contract-state root into the block and
verify it on the receive path (the audit's step 4 — MUST come last; a root before P1–P3 turns
silent divergence into halts).

**Enablement gate (B1-class, before the flag is ever true):** two nodes, same chain, deploy +
call + payable-transfer; assert identical contract state, identical state root, identical balances,
and identical `eth_getBalance` across both — plus a negative test (one node flag-off stays on the
transfer path and is detectably behind, never silently divergent).

## Non-breaking guarantees (how each constituency is protected)
- **Current fleet / other branches:** flag default false → no-op executor → apply path identical to
  today. Merging any phase changes nothing at runtime.
- **The external AVC module:** connects by implementing `execbridge.ContractExecutor` and calling
  `SetExecutor` at its wiring time; it never touches `messaging/BlockProcessing` and is never broken
  by it. jmdn depends only on the interface, not on AVC.
- **Rollback:** flip the flag off; the executor de-registers to no-op; behavior reverts with no data
  migration.

---

## Progress log

- **P0 — DONE (569aec6):** execbridge seam + cfg.Contracts.Enabled (default off), dormant. Verified build (CGO off).
- **P1a — DONE (ab201f8):** deterministic contract-state commit + state digest (EVM-09). Ordering
  logic proven order-independent in isolation (200 randomized runs). Host gate: CGO build of ./DB_OPs/contractDB/.
- **P1b — DONE (49717a4):** deterministic EVM execution entry — Deploy/ExecuteContractWithContext
  take block context explicitly, no time.Now/HTTP (EVM-02). Dormant (no caller yet). Host gate: CGO
  build of ./SmartContract/internal/evm/.

### Remaining (need a working CGO compiler + the 2-node determinism gate — do NOT emit blind)
- **P1c:** the execbridge.ContractExecutor impl in the SmartContract layer — construct a per-tx
  StateDB (shared repo + DID client), call Deploy/ExecuteContractWithContext with a BlockExecContext
  → DetBlockContext, CommitToDB, map GetBalanceChanges → ExecResult. Register in main.go behind
  cfg.Contracts.Enabled.
- **EVM-30/31 (gas):** intrinsic gas + refund via core.StateTransition / core.IntrinsicGas — exact
  go-ethereum API MUST be verified against a compiler (version-sensitive); not written blind.
- **EVM-29 (fork):** assert Shanghai selection (Random set / rules) — verify against a compiler.
- **P2:** thread BlockNumber/BlockHash/TxIndex through processTransaction; invoke execbridge for
  contract txs; fold BalanceChanges+GasUsed through config.GasFee/SplitFee under LockStateApply,
  atomically with ApplyTxAtomic. Deployment (To==nil) no longer hits the nil-recipient deref.
- **P3:** receipts/logs with real block context (EVM-13); SetSharedKVStore so HasCode works (EVM-16/25).
- **P4:** contract-state root into the block + verify on receive.
- **Enablement gate (before the flag is ever true):** 2-node deploy/call/payable — identical state,
  digest, balances; negative test that a flag-off node stays on the transfer path and is detectably
  behind, never silently divergent.
