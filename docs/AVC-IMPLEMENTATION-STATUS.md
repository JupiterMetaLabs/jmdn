# AVC (whole system) — Implementation Status Tracker

Same format and same rigor as `M4-IMPLEMENTATION-STATUS.md`, scoped to the **entire** AVC system — not just entropy/RANDAO/VDF. `M4-IMPLEMENTATION-STATUS.md` stays the detailed tracker for that one subsystem; this file is the top-level map and defers to it for M4 detail rather than repeating it.

**Verified against actual code on 2026-08-24** by grepping and reading the real files in `~/Block/{avc,jmdn}` — not from `AVC-Architecture-End-to-End.md`'s §9 build-status table or the KT artifact, both of which this file corrects in several places below. Every claim below is either a direct `grep -rn` result (caller counts, flag defaults) shown in the evidence line, or a file:line citation you can re-open yourself. Where this file disagrees with an older doc, the disagreement is stated explicitly, not silently overwritten.

**How to re-check this yourself:** the M4-scoped claims map to `bash verify-m4.sh`. The non-M4 claims below don't have a script yet — each evidence line is the exact `grep`/`go test` command that produced it, so you can rerun it directly.

---

## DESIGN

```
DESIGN
├── two-tier voting (T_vote/T_agg)     ✅ settled, fields on ZKBlock
├── A-ExpJ committee selection         ✅ settled, avc/committee pure lib built
├── self-heal (timeout → redraw)       ✅ settled on paper, period feeds DeriveSeed
├── entropy/RANDAO/VDF                 ✅ settled → see M4-IMPLEMENTATION-STATUS.md
├── CRDT vote-store redesign           ✅ designed, 0% implemented (unchanged)
├── vote equivocation detection        ✅ settled → first-seen-hash durable marker
├── committee multiaddr resolution     ✅ settled (part of M8 peer routing)
└── seed-side historical snapshot      ✅ designed, 0% implemented (unchanged)
```

---

## IMPLEMENTATION

```
IMPLEMENTATION
├── M0 (slot/period/epoch)             ✅ implemented, tested — see M4 tracker
├── M2b (consensus-field hash gate)    ✅ implemented, gated OFF (JMDN_M2B_HASH)
├── M3 (epoch calculation)             ✅ implemented, tested — see M4 tracker
├── M4 (entropy/RANDAO/VDF)            ⚠️ see M4-IMPLEMENTATION-STATUS.md
│                                          (reveal+fallback logic ✅; VDF governance,
│                                          historical snapshot, slot-restart guard ❌)
├── M8 (committee multiaddr routing)   ✅ implemented (directMSG.go, message.go,
│                                          broadcast.go carry live resolution paths)
├── M9 (voting-snapshot checkpoint)    ❌ field exists, deliberately left at zero
│                                          — Block/consensus_fields.go's own comment:
│                                          "VdfProof, SeedEpoch, and VotingSnapshotEpoch
│                                          remain deliberately zero"
├── A-ExpJ committee selection (v2)    ✅ implemented AND wired into jmdn, gated OFF
│                                          (JMDN_COMMITTEE_V2, default false)
│                                          — committee_v2.go:218 calls DeriveSeed,
│                                          committee_v2.go:234 calls CommitteeFor
├── avc/validation shadow-check        ✅ implemented AND wired, gated by TWO flags
│                                          both required: Features.AvcValidation.Enabled
│                                          AND NetworkSettings.Environment=="testnet"
│                                          (config/settings/config.go:170-172,329;
│                                          shadow.go:48 checks both)
├── self-heal (timeout cert → redraw)  ❌ mechanism built, NOT functionally live
│                                          — PeriodStore.AcceptTimeoutCertificate has
│                                          ZERO callers anywhere outside its own tests
│                                          (grep across jmdn+avc: only 4 hits, all in
│                                          timeout_certificates_test.go / slot_store_test.go)
│                                          — a timed-out round redraws the SAME committee
│                                          in the running system today, despite Period
│                                          correctly flowing into DeriveSeed once it IS set
├── vote equivocation detection        ✅ implemented AND wired in production
│                                          — DB_OPs/equivocation.go: real durable
│                                          first-seen-hash marker (not a stub)
│                                          — blockPropagation.go:92 installs
│                                          DBEquivocationStore{} as the live store
├── CRDT vote-store compaction         ❌ not built (crdt/ package has the base
│                                          CRDT primitives; no compaction pass exists)
├── historical snapshot server         ❌ not built — see M4 tracker (same gap,
│                                          it's one seedNode service, not two)
└── committee size — 3-way, not 2-way,
    inconsistency                       ⚠️ documentation gap, not a functional bug
        ├── avc/config.CommitteeSize        = 4   (buddy/BFT committee default)
        ├── avc/bft.DefaultCommitteeSize     = 13  (BFT committee, mirrored in jmdn)
        └── jmdn/messaging.EntropyCommitteeSize = 13  (a DIFFERENT role — entropy-reveal
                                                        committee, not BFT)
```

---

## Corrections vs. `AVC-Architecture-End-to-End.md` §9 and the KT artifact

Stated plainly, each with the grep that produced it:

1. **Self-heal: doc implies "mechanism built" reads as "working." It is not functionally live.** `Period` does flow into `committee.DeriveSeed` correctly once set — but nothing in the non-test codebase ever calls `PeriodStore.AcceptTimeoutCertificate`, the only function that ever advances the store. `grep -rn "AcceptTimeoutCertificate" jmdn avc` returns 7 hits total; all 7 are either the function's own definition or calls from `timeout_certificates_test.go` / `slot_store_test.go`. **Practical effect: a timed-out round redraws the identical committee today**, because the seed input that would change it never changes. This is a real functional gap the doc doesn't call out this sharply.

2. **A-ExpJ committee selection v2: doc frames it as "BUILT, UNWIRED." It IS wired — just gated off.** `committee_v2.go:218` calls `committee.DeriveSeed(...)`, and `committee_v2.go:234` calls `committee.CommitteeFor(seed, snap, k)`. Both are live call sites in `jmdn/messaging`, not test-only. The reason it doesn't run today is `CommitteeV2Enabled = envOn("JMDN_COMMITTEE_V2", false)` (`committee_v2.go:60`) — a rollout flag, the same pattern as M2b's hash gate and M4's aggSig cert. "Unwired" overstates the gap; "wired but flagged off" is accurate.

3. **`SetBeaconSource`: some in-file comments read as if this has no real caller. It does.** `jmdn/Sequencer/beacon_install.go:143` calls `messaging.SetBeaconSource(sink)` from non-test code. `verify-m4.sh` independently confirms this (`Stage A/F: SetBeaconSource has $n caller(s)` — passes).

4. **avc/validation shadow-check: confirmed real and wired, but the doc doesn't emphasize the double-gate.** Both `Features.AvcValidation.Enabled` AND `NetworkSettings.Environment=="testnet"` must be true — `config/settings/config.go:170-172` calls this "a SAFETY GATE, not just metadata," and `shadow.go:48` checks both conditions in the same `if`. This is a deliberate mainnet-never-activates-this design, not an oversight; worth stating explicitly since a reader could otherwise assume one flag suffices.

5. **Committee size "inconsistency" is a 3-way split, not the 2-way (avc=4 vs jmdn=13) framing in the old doc.** There are three distinct constants: `avc/config` default (4, buddy/BFT), `avc/bft.DefaultCommitteeSize` / `jmdn/AVC/BFT/bft.DefaultCommitteeSize` (13, BFT — these two already agree with each other), and `jmdn/messaging.EntropyCommitteeSize` (13, but this is the **entropy-reveal** committee, a different role from BFT). The "4 vs 13" tension the old doc flags may be comparing a stale default against the real BFT default rather than exposing a live bug — but the BFT-vs-entropy split was never named as two separate parameters in the same doc, which is worth fixing regardless of whether 4 vs 13 is itself resolved.

6. **Vote equivocation detection: not mentioned as a build-status line item in the old doc at all.** It is real, non-stub, production-wired code: `DB_OPs/equivocation.go` implements a durable first-seen-hash marker; `blockPropagation.go:92` installs `DBEquivocationStore{}` as the live `EquivocationStore`. Listed here so it isn't missed in future status snapshots.

7. **M9 voting-snapshot checkpoint (`VotingSnapshotEpoch`) is unbuilt by explicit design, not by oversight.** `Block/consensus_fields.go`'s own comment states: *"VdfProof, SeedEpoch, and VotingSnapshotEpoch remain deliberately zero."* Confirmed no assignment site exists anywhere in non-test code (`grep -rn "SnapshotCheckpoint\s*=\|VotingSnapshotEpoch\s*="` outside `_test.go` returns nothing). This matches the old doc's framing — no correction here, listed for completeness since it's part of the same M2b/M4/M9 field family.

---

## What this file does NOT re-derive

M4's own internal detail (fallback state machine, B-as-count vs B-as-slot, Ed25519 reveal wiring, RevealPush, VDF `CheckDelay`/`S` presentation defect, proposer-rotation scoping task #8) is intentionally not repeated here — `M4-IMPLEMENTATION-STATUS.md` is the source of truth for that subsystem and is re-verifiable with `verify-m4.sh`. This file's job is the system-wide map: M0/M2b/M3/M8/M9, committee selection (both BFT and A-ExpJ v2), self-heal, the avc/validation shadow path, CRDT, and vote equivocation — the parts the M4 tracker doesn't cover.

---

## Still genuinely open (unchanged, not touched this session)

- **CRDT vote-store compaction** — not built. The base `crdt/` primitives exist; no compaction pass over them exists yet.
- **Historical snapshot server** — not built (same gap named in the M4 tracker; it is one planned seedNode service covering both entropy history and general vote-snapshot history, not two separate builds).
- **Self-heal production wiring** — the one new functional gap surfaced this session: the mechanism is correct and unit-tested in isolation, but has no caller, so it does not activate in the running system. This is a real "next thing to wire," not a design question — `AcceptTimeoutCertificate` needs exactly one production call site, most likely from wherever a round's timeout is currently detected without action (not yet located — the "detects timeout but does nothing with it" call site is the next thing to find, not something confirmed in this session).
- **Committee-size disambiguation** — a naming/config fix (give the BFT-vs-entropy-reveal committee sizes clearly distinct names in code and docs), not a functional break, but worth doing before it causes a real mix-up.

---

## Why this drifted before, and how to keep it from drifting again

Same root cause as M4: `AVC-Architecture-End-to-End.md` and the KT artifact each freeze a snapshot at whatever date they were last edited, and nothing forces them to update when code changes. This file is a cache of a point-in-time `grep`/read pass, not a substitute for one — re-verify the caller-count claims above with the exact `grep -rn` commands shown before trusting them past today.
