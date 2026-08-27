# Sync-monitor reporting: MMR fingerprint → tip StateRoot (ThebeDB-native)

**Status:** implemented on `feat/thebe-sc-layer`, host-built. For review.
**Scope:** how each node computes the root it reports to the seednode for out-of-sync
detection. Nothing about the seednode's comparison logic or the catch-up path changes.
**Author aid:** grounded in the current tree; the one load-bearing assumption is called out.

---

## 1. Decision

Replace the sync monitor's **O(N) Merkle-tree-over-block-hashes** fingerprint with an **O(1)
read of the tip block's `StateRoot`**, reported to the seednode unchanged.

`StateRoot` is already the cumulative commitment the MMR root was reconstructing, so this is a
semantic drop-in — the value is *read* instead of *rebuilt* — that also removes the monitor's last
dependency on the retired `JMDN-FastSync` library.

---

## 2. Why change it

The monitor reports `(head, root)` to the seednode every cycle; the seednode compares the node's
root against the sequencer's to decide "in sync?". Today `root` is built by
`merkle.Fingerprinter.Compute(blockInfo)`:

- **O(N):** it reads block hashes `0..N` and (re)builds a Merkle tree; the incremental builder
  amortizes but still re-hashes the tip every cycle.
- **Old-library coupling:** it needs `gossipnode/internal/merkle` (MMR) and the retired
  `fastsync_types.BlockInfo` adapter (`DB_OPs/Nodeinfo`) — the last thing pinning the old
  `JMDN-FastSync` engine into the sync path.

ThebeDB already maintains an equivalent cumulative value for free.

---

## 3. Why `StateRoot` is the right value

Every block carries, and the generator computes deterministically (enforced on the receive path
by `stateRootChain` / `linkageDecision` in `messaging/consensus_hardening.go`):

```
StateRoot_n = Keccak256( StateRoot_{n-1} ‖ BlockHash_n )
```

Properties that make it a correct replacement for the MMR root:

- **Cumulative:** it folds the entire block-hash history into one 32-byte value — a divergence in
  *any* prior block changes the tip `StateRoot`, exactly like the accumulator did.
- **Consensus-identical:** it is derived from consensus block hashes by a deterministic rule, so
  all honest nodes at the same height hold the identical value.
- **Already stored + O(1):** it is a column on the tip block — one row read, no tree, no rescan.

The seednode only compares a 32-byte root + a head; it does not care how the root is produced, so
**its side needs no change**.

---

## 4. Data flow — before / after

```mermaid
flowchart LR
    subgraph BEFORE [Before: O(N) MMR rebuild]
      A1[read block hashes 0..N] --> A2[rebuild Merkle tree] --> A3[root]
      A3 --> A4[ReportBlockState head, root]
    end
    subgraph AFTER [After: O(1) tip StateRoot]
      B1[GetZKBlockByNumber tip] --> B2[tip.StateRoot] --> B3[ReportBlockState head, root]
    end
```

Both feed the **same** `seedClient.ReportBlockState(head, root)`; the seednode comparison is
untouched.

---

## 5. Design

A small interface in the monitor, implemented by the host over the block store:

```go
// internal/syncmonitor
type ChainReporter interface {
    // TipState returns the local tip height and its 32-byte StateRoot.
    TipState(ctx context.Context) (head uint64, root []byte, err error)
    // LastBlockReceivedAt is the propagation-guard signal (Fix 2);
    // zero time disables the guard for that cycle.
    LastBlockReceivedAt() time.Time
}
```

```go
// gossipnode/thebesync — implementation over DB_OPs
func (ChainReporter) TipState(ctx context.Context) (uint64, []byte, error) {
    head, err := DB_OPs.GetLatestBlockNumber(ctx, nil)   // O(1) marker read
    if err != nil { return 0, nil, err }
    blk, err := DB_OPs.GetZKBlockByNumber(nil, head)      // O(1) row read
    if err != nil { return 0, nil, err }
    return head, blk.StateRoot.Bytes(), nil
}
func (ChainReporter) LastBlockReceivedAt() time.Time { return DB_OPs.LastBlockStoredAt() }
```

The propagation guard (Fix 2 — skip a report that races an in-flight block write) previously read
a timestamp off the old `DB_OPs/Nodeinfo` sync-struct. It is now a Thebe-native atomic
(`DB_OPs.LastBlockStoredAt()`), set on every successful `StoreZKBlock`.

---

## 6. Files changed

| File | Change |
|------|--------|
| `internal/syncmonitor/monitor.go` | New `ChainReporter` interface; `Monitor` holds it instead of `blockInfo`+`fingerprint`; `New()` takes a `ChainReporter`; `runCheck` reads `TipState` (O(1)) instead of `Compute`. Removed `fastsync_types` + `internal/merkle` imports. |
| `thebesync/reporter.go` | `ChainReporter` implementation over `DB_OPs` (tip height + `StateRoot`). |
| `DB_OPs/thebe_ops.go` | `LastBlockStoredAt()` + an atomic set on `StoreZKBlock` success (propagation-guard signal). |
| `main.go` | Wires `syncmonitor.New(thebesync.ChainReporter{}, …)`. |
| `internal/syncmonitor/monitor_test.go` | Rewritten to a `stubReporter`; all eight behavioral tests (out-of-sync, threshold, propagation guard, block-delta filter, grace period, jitter) preserved. |

**Side effect:** `internal/merkle` now has no importers (orphaned). The old `JMDN-FastSync`
dependency now survives only through `DB_OPs/Nodeinfo` (account-sync worker + redis streamer);
retiring those lets us delete `internal/merkle` + `DB_OPs/Nodeinfo` and `go mod tidy` the dep away.

---

## 7. Rollout — the review-critical point

The seednode compares each node's root against the **sequencer's** root. The switch is therefore
**fleet-coordinated**: a node on the new code reporting `StateRoot` while the sequencer still
reports the MMR root would mismatch and be flagged out-of-sync.

- **Blast radius is low:** a false out-of-sync now just triggers a ThebeSync catch-up, which is
  cheap, idempotent, and lands the node in the same place. No corruption.
- **Recommended:** deploy the sequencer first (or all nodes together). Optionally version the
  report so the seednode compares like-with-like during a mixed window (not implemented — the
  wire has a single `merkle_root []byte`; add a version field if a staged rollout is required).

---

## 8. Risks / assumptions

- **Assumed:** the local tip marker reflects a *contiguous* chain (F6 monotonic single-writer +
  the parent/height linkage gate), so reading `tip.StateRoot` faithfully represents all blocks
  ≤ tip. **If wrong:** an internal gap below the tip could be masked (the MMR rebuild would have
  caught it via a zero leaf). **Mitigation:** the linkage gate + monotonic marker do not let the
  tip advance past a gap; catch-up fills gaps before the tip moves.
- **Genesis:** genesis carries a zero/base `StateRoot`; the chain accumulates from block 1. All
  nodes seed the identical genesis, so the accumulator agrees from height 0.
- **Contracts-off note:** unrelated to this change — `StateRoot` is a block-hash accumulator, not
  the P2.5 account-state fingerprint, so it is always populated regardless of `execbridge.Enabled()`.

---

## 9. Verification

- `go build ./...` (CGO) — green.
- `go test ./internal/syncmonitor/...` — the eight behavioral tests (unchanged semantics).
- End-to-end: the `local-thebesync-gate/catchup_gate.sh` harness exercises catch-up + equality; a
  node that reports a divergent root is flagged out-of-sync and catches up.

---

## 10. Open questions for review

1. Do we want a **versioned report** for a staged rollout, or is a coordinated deploy (sequencer
   first) acceptable given catch-up is now cheap?
2. Should the monitor additionally report the **tip `BlockHash`** alongside `StateRoot` for easier
   human debugging on the seednode side, or is the single cumulative root sufficient?
3. Confirm the tip-contiguity assumption (§8) holds on all node roles (sequencer vs follower).
