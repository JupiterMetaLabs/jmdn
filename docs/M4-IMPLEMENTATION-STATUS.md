# M4 (Entropy / RANDAO / VDF) — Implementation Status Tracker

**No single file like this existed before now.** The closest things are `AVC-Architecture-End-to-End.md` §9–§12 (the authoritative but 142KB narrative doc) and `verify-m4.sh` (an executable check, not a document). This file is a new, deliberately short tracker meant to be re-verified against `verify-m4.sh` and updated in place — not rewritten from memory — every time something lands.

**Verified against actual code** on 2026-08-24 by reading the real files and running the real tests/`verify-m4.sh` (28/28 checks pass) — not against an older doc or an earlier conversation summary. Where a status below differs from an existing doc or a status you were given elsewhere, the difference is called out explicitly rather than silently overwritten.

**Updated in place, 2026-08-27:** the "VDF governance/checks" row below was closed this session (`messaging.ValidateVDFTimingParams`, and `VdfProof` attachment in `Block/consensus_fields.go`) — see correction 5 below. Re-verified with `bash verify-m4.sh` (58/58 pass) plus the full `messaging`/`Block`/`Sequencer` test suites and `go build`/`go vet` in both `avc` and `jmdn`, not just the script's own checks.

**How to re-check this yourself:** `cd ~/Block && bash verify-m4.sh` — every line below maps to a check in that script or to a `grep`/`go test` run against the file cited.

---

## DESIGN

```
DESIGN
├── reveal mechanism           ✅ settled → Ed25519 (Decision A, §4.3)
├── CRDT redesign              ✅ designed, 0% implemented
├── fallback formula           ✅ settled → count-based aggSig fold + two-phase
│                                  finalisation (§10 decisions 11/11b, amended 2026-08-24)
└── snapshot architecture      ✅ designed (seedNodes historical snapshot service,
                                   resolved-by-design 2026-08-17), 0% implemented
```

**Correction from your snapshot:** "fallback target" was `⚠️ B1-dependent`. That was true as of 2026-08-20. As of this session, B1 is no longer the open question on the design side — B1 is now an *implementation* gate (see below), and the design itself was amended twice since: once to narrow the fold to `[K,K+B)`, once more (2026-08-24) to make `B` a count with a separate slot deadline, fixing a halt vector the fixed window had. The design question is closed; what's open is a rollout decision (turn the flag on) and one unrelated proof (task #8, below).

---

## IMPLEMENTATION

```
IMPLEMENTATION
├── M0.1 slot                        ✅ implemented, tested (slot_store.go)
├── M0.2 timeout/period              ✅ implemented, tested (SlotStore.AdvanceOnCommit)
├── M3 epoch calculation             ✅ implemented, tested (EpochForSlot)
├── M2b hash gate                    ✅ implemented, gated OFF by default (JMDN_M2B_HASH)
├── Ed25519 reveal                   ✅ implemented, wired end-to-end
│                                       (main.go installs identity → SetNodeIdentity;
│                                       RevealsForBlock → block.RandaoReveals → fold;
│                                       proven by TestEndToEnd_ProduceToBlockToFold)
├── RevealPush                       ✅ implemented, wired end-to-end
│                                       (blockPropagation.go calls PushOwnRevealForSlot;
│                                       node.go registers the receive handler at startup)
├── persisted aggSig (B1)            ✅ implemented, gated OFF by default (JMDN_AVC_AGG_CERT)
│                                       — the certificate is recorded, re-verified, and
│                                       hash-covered; not yet a fleet-wide rollout
├── historical snapshot server       ❌ not built (seedNodes has committee/signature
│                                       services; no snapshot-serving file exists)
├── slot restart fail-closed         ❌ not built (slot_store.go has no restart guard)
├── fallback state machine           ✅ implemented, tested, race-tested (2026-08-24)
│                                       — count-based collection + two-phase decide/
│                                       resolve-pending split; 0 data races under -race
│                                       — BUT still produces no real seeds fleet-wide
│                                       until the B1 flag above is turned on: the logic
│                                       is correct, it just has nothing to collect yet
├── live jmdn CRDT redesign          ❌ not built
└── VDF governance/checks            ✅ built AND wired (closed this session)
    ├── CheckDelay replaced by CheckBiasResistance + CheckLiveness
    │   (avc/vdf/vdf.go) — wired via messaging.ValidateVDFTimingParams
    │   (messaging/vdf_timing_params.go), called from main.go at startup;
    │   enforces the adopted S=20/3 bias-resistance/liveness bounds using
    │   vdf.AdoptedSpeedup() (never the rounded 6.67), hard-exits on a
    │   bad N/K/s_min/T_vdf combination — same discipline as
    │   ValidateFallbackWindowParams
    └── VdfProof is declared on ZKBlock, hash-covered, AND now assigned a
        real value on the epoch-boundary block (Block/consensus_fields.go,
        reading Sequencer.SealerResultFor via the newly-added
        messaging.EpochBoundarySlot / avc/vdf.Proof.MarshalBinary) — fails
        closed via Sequencer.ErrVDFProofNotReady if the proof isn't ready,
        or on the sealer's own result.Err, rather than proposing with a
        missing/zero entropy value
```

**Corrections from your snapshot, stated plainly:**

1. **`persisted aggSig B1` was `❌`, is actually `✅` (flagged off).** This was the real blocker as of 2026-08-20; it was cleared earlier in this session. Marking it flat `❌` today would send you chasing work that's already done — the only remaining step is a coordinated flag flip (`JMDN_AVC_AGG_CERT=1`), the same rollout pattern as M2b's own hash gate.
2. **`correct fallback state machine` was `⚠️`, is actually `✅` (logic done, data-starved until #1 flips).** This was fixed and verified in this session's most recent work: the old design required every slot in a fixed range and could halt permanently on one timeout; the new one collects by count with a slot deadline as backstop, and finalisation no longer fires at the instant its own input is guaranteed empty. 133 tests pass, including the exact gap-tolerance scenario, and the package is race-clean.
3. **`Ed25519 reveal` was `⚠️ needs final integration`, is actually `✅`.** `main.go:1272` calls `SetNodeIdentity` at startup — the "final integration" already happened.
4. **`RevealPush` was `⚠️ still wiring`, is actually `✅`.** Both directions have real (non-test) callers: `blockPropagation.go` sends, `node.go` registers the receiver at startup.
5. **`VDF governance/checks` was `❌ not built`, is actually `✅ built AND wired` (closed this session, not by an external commit — implemented directly in this pass).** `CheckBiasResistance`/`CheckLiveness` existed and were tested already, but had zero production callers anywhere; `messaging.ValidateVDFTimingParams` (new file, called from `main.go`) is that caller. `VdfProof` is likewise no longer permanently zero — `Block/consensus_fields.go` now attaches it on the epoch-boundary block. See the IMPLEMENTATION block above for the full detail and fail-closed behavior.

**Still genuinely open, unchanged by this session:**
- Historical snapshot server, slot-restart fail-closed guard, live CRDT redesign — none of these were touched; the `❌`s above are real. (VDF governance is no longer in this list — see correction 5 above.)
- Task #8 (proposer-rotation scoping) is still unscoped, so `B=5`'s lower bound (must span ≥2 proposers' turns) remains unproven even though the mechanism around it is now correct.
- `S`'s presentation defect (printed as `6.67` in prose docs, only self-consistent as exactly `20/3`, §10 decision 12b) is now moot for anything that actually runs: the one real enforcement path (`ValidateVDFTimingParams`) calls `vdf.AdoptedSpeedup()`, never a decimal literal. The prose-doc correction itself (e.g. in `AVC-Architecture-End-to-End.md`) is still outstanding, but no live code path can be tripped by it anymore.

---

## Why this drifted before, and how to keep it from drifting again

The gap you're pointing at is real: multiple docs (`AVC-Architecture-End-to-End.md`, `AVC-Communication-Flow.md`, the KT artifact you had me open) each freeze a snapshot at whatever date they were last touched, and none of them auto-update when code changes underneath them. The fix isn't a better document — it's the executable one you already have: **`verify-m4.sh` is the only thing in this project that can't go stale**, because it greps and runs the real code every time. This tracker exists to give that script's output a name and a place to live; treat it as a cache of `verify-m4.sh`'s answer at a point in time, not a replacement for running it.
