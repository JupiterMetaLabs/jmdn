# Stage-2 Gap Closure — implementation record

**Branch:** `feat/consensus-audit` (base `da4967b`) · **Date:** 2026-09-03
**Verified with:** go1.26.0 — `go build ./...`, `go vet ./...`, package tests, all green.

Closes gaps 1–5 of the 8 listed in `AVC_STAGE2_FULL_CONSENSUS_FLOW.md` §0.5.
Gaps 6–8 were already closed on this branch by `da4967b`.

| Gap | Was | Now | Files |
|---|---|---|---|
| **2** Mix retention | `notifyEpochFinalised` handed the mix to the sealing hook and dropped it. No node held a mix. | Bounded store keyed by closed epoch; idempotent, refuses a conflicting value. | `messaging/entropy_mix_store.go` (new), `entropy_finalise.go` |
| **1** Proof adoption | `beacon.Pipeline.Accept` had **zero callers**. Every non-sealing node paid a full VDF evaluation. | Acceptor seam registered from `InstallAVCBeaconFromEnv`; `Accept` now reached on the block path. | `messaging/entropy_vdf_accept.go` (new), `Sequencer/vdf_accept_wiring.go` (new), `Sequencer/beacon_install.go` |
| **5** Proof validation | No group / difficulty / epoch check anywhere. Wrong-epoch replay unguarded. | Five ordered checks: boundary slot → slot-epoch binding → independent mix → group/`T` → `vdf.Verify`. Rejections publish nothing and never reach the pipeline. | `messaging/entropy_vdf_accept.go` |
| **4** Sync recovery | `thebesync` recorded nothing; a synced node's aggregate store was empty for its whole catch-up range, silently. | Sync applies the same entropy effects through one shared definition. | `messaging/entropy_block_effects.go` (new), `thebesync/apply.go`, `broadcast.go`, `blockPropagation.go` |
| **3** Entropy persistence | `BeaconSource` was RAM-only and **not recomputable** (the mix is gone by restart). | Entropy persisted on both seal and adopt; rehydrated at startup before the sink goes live. | `DB_OPs/beacon_entropy.go` (new), `messaging/entropy_persist.go` (new), `Sequencer/vdf_sealer.go`, `beacon_install.go` |

## Design decisions worth review

**The mix is never taken from the block.** `Accept(forEpoch, mix, proof)` re-derives the VDF
challenge from the mix. A mix supplied by the same party as the proof would verify any proof that
party chose. So the verifier holds its own, and a node without one declines to adopt
(`ErrMixUnavailable`) rather than guessing. This is why gap 2 had to land before gap 1.

**A bad proof does not reject the block.** `VdfProof` is covered by the M2b `ConsensusHash`, so a
relay cannot forge one — a bad proof is a proposer fault. Rejecting the block would turn an entropy
problem into a liveness problem. The block is accepted and contributes no entropy.

**Sync does not finalise epochs.** `RecordSyncedBlockEntropy` runs steps 1, 2 and 4 but omits
`maybeFinaliseCompletedEpochs`. Finalising during a replay of thousands of blocks would launch one
background VDF evaluation per crossed epoch boundary. The node needs the *aggregate state*, which
steps 1–2 rebuild. Consequence, stated not hidden: during sync, adoption usually reports
`ErrMixUnavailable` — the correct fail-closed answer.

**Rehydration is fatal only on conflict.** No records, or an unreadable epoch, is a soft miss.
A *conflicting* value fails installation: it means the durable state is corrupt or from another
network.

## Two claims of mine that the code disproved

1. **"Ascending rehydration order is required."** False. `evictLocked` uses
   `cutoff = newest - retain` with `newest = max(published)`, so the survivor set is
   order-independent. A test I wrote to prove the opposite failed; the comments in three files were
   corrected and `TestRehydrationSurvivorSetIsOrderIndependent` pins the real behaviour.
2. **Prefix scan for restore.** `DB_OPs.GetAllKeys` is a stub that always errors
   ("ImmuDB removed"). My first version called it and broke every beacon install — caught by
   `Sequencer`'s existing tests. Replaced with a monotonic `beacon_entropy_newest` pointer plus a
   bounded probe of the retention window.

## Tests added

`messaging/entropy_stage2_gaps_test.go` — mix retained / conflict refused; acceptor reached with the
**locally** finalised mix; rejected off-boundary; rejected wrong-epoch; not adopted without a local
mix; Stage-1 and no-proof are no-ops; adoption idempotent; retention order-independence.

## Still open — not addressed here

- RANDAO reveal generation (`block.RandaoReveals` is always empty, so the NORMAL path is unreachable)
- Genesis/epoch-0 entropy bootstrap
- Mainnet modulus digest (`Digest: ""`) — human
- Difficulty `T` calibration against a measured `ŝ` — human
- `origin/feat/thebe-sc-avc-a3` has 2 newer commits adding jmdn-side VDF network pins; not merged here
