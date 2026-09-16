# AVC Consensus — Audit Handover Document

| | |
|---|---|
| **Status / Verdict** | **No-Go for mainnet.** **Stage 2 entropy: the gate is CLEAR as of PR #135 (2026-09-16)** — D-39 and D-54 landed together as one identity (group ‖ modulus digest ‖ T), joining D-24, D-25, D-27, D-29 and D-35. **But enable it only on a uniformly upgraded fleet:** two of the three new protections are unconditional (install-time refusal of a divergent `T`; identity hash-covered in the `ConsensusHash` preimage, so a wrong-parameter node forks), while the adoption-path check that *names the cause* is skipped when either side's field is empty, by design, so a mixed fleet degrades to the pre-fix nameless failure. No posture gate forces the promotion — **D-59**. See §0.3. **Go for continued devnet use** — but only because the devnet's `T` and modulus are fleet-uniform and the modulus guard now refuses the trapdoored pin by value on any other chain **and refuses to boot** in production posture (87636d7). **One cutover note:** 8872912 froze the consensus-hash preimage as the single format, so the next fleet upgrade is a **coordinated restart, not a rolling one** — a node on the old binary forks. |
| **Date** | 2026-09-03, **last revised 2026-09-15 (rev 6)** (rev 3 re-derived from `v3base` after all four repos moved; rev 4 added D-35…D-37 from the PR #125 review; rev 5 restored D-38…D-50; **rev 6 re-verified every `Fixed` claim against live code after PR #129 merged, then compressed the closed rows** — see the rev-6 note under §2.2) |
| **Scope** | Originally audited at `jmdn@84d0c54f` · `avc@1c13324` · `ThebeDB@6b8f6d1` · `jmdt-devnet@fd52106` (`main` — **no `v3base` branch exists in that repo**), re-based to `jmdn@dda7c4a9` on 2026-09-04. **Rev 5 re-verified against `jmdn@291d44c` and `avc@4df28ca` (= `v0.1.0-v3base.5`); rev 6 re-verified against `jmdn@e4f18da`, `avc@4df28ca`, `ThebeDB@533622a` after PR #129 merged.** Entropy pipeline end-to-end, committee selection seam, vote ingest and BLS signing path, block-hash preimages, CRDT merge determinism, quorum arithmetic (all four implementations), devnet config vs code expectations; rev 4 added the persistence layer, the fallback aggregate-signature fold, VDF network pins, and the libp2p vdf-proof pull protocol. |
| **Method** | Full source trace → Go 1.26.0 toolchain in a clean sandbox → `go vet` + `go test -race` across avc's consensus packages (all green) → **9 proof-of-concept tests written and executed** → Go race detector on the reveal-fold path → viper precedence reproduced by execution to settle two config claims → re-derivation from `v3base` after the repos moved mid-audit → rev 4: five parallel line-by-line readers over PR #125's 61-file diff → rev 5: every §0-§2 claim, command, path, symbol and count re-resolved against the live tree. |
| **Companion file** | `avc/tests/audit/audit_poc_test.go` — executable evidence, **but read §0.5 first: it tests `avc`, and cannot see a jmdn-side fix.** PoCs 1-3 still pass, and that is *not* evidence D-25/D-27 are open — both are `Fixed`. PoCs 4-5 are **INVERTED** and pass while the D-32 fix holds (the only pair whose fix landed in the same module). Invert each remaining assertion when fixing, to convert it into a regression test. D-24's regression test is `avc/randao/accumulator_race_test.go` (`TestFoldIsRaceFreeUnderConcurrentWriters`, `TestFoldIsRaceFreeAgainstConcurrentReaders`) — untagged, runs on a plain `go test`. The `-tags defects` probe it was promoted from was **deleted** in avc `b11b686`. |
| **Reproduce** | `cd avc && go test ./tests/audit/ -v` (9 tests, ~10s — **PoC 4 and PoC 5 are INVERTED regression tests** that pass while the D-32 fix holds; **PoC 1-3 also pass, but they assert `avc`'s behaviour, not jmdn's — D-25 and D-27 are both `Fixed`. See §0.5**) · `cd avc && go test -race ./randao/` (D-24's fix — `accumulator_race_test.go`; the old `-tags defects TestAccumulatorFoldRace` probe was **deleted** once promoted, so that command no longer exists) · `cd avc && go test -race ./quorum/... ./committee/... ./crdt/... ./beacon/... ./randao/... ./vdf/...` (baseline, clean) |
| **Prior audits** | This register starts at **D-24** because the 2026-08-31 cross-repo audit in `WORKDIR2/audits/` held `D-1…D-23`. **Those were renumbered to `XR-1…XR-23` on 2026-09-15** (1:1, no content change) to end a five-way `D-n` namespace collision across the workspace — see that document's header. **So `D-1…D-23` no longer exist anywhere; this register's space is `D-24…D-58` and the gap below D-24 is historical, not a hole.** `WORKDIR2/audits/` is a local archive, outside git. New findings are **D-24…D-59** (D-35…D-37 from the PR #125 review; D-38…D-50 restored 2026-09-11; D-51…D-55 added rev 5; D-56/D-57 from the PR #129 review; **D-58 added rev 6; D-59 added 2026-09-16 reviewing PR #135** — see the note under §2.2). Never renumber. |
| **⚠ Third register, UNCOMMITTED** | A separate register with its own `SEC/CON/STO/EVM/SYN/NET/API/PRC` ID space lives at **`WORKDIR2/THEBE-AUDIT-HLD.md`** — outside every git repo. **54 jmdn source files cite those IDs** (recounted 2026-09-15; an earlier revision said 52) as the reason their code is shaped the way it is — `CON-` 20 files, `EVM-` 10, `SEC-` 9, `NET-` 6, `API-` 6, `STO-` 4, `SYN-` 1, `PRC-` 1 — so a fresh clone cannot resolve any of them. It also carries a live CRITICAL, `API-10`: `gETH/Facade/rpc/http_server.go` runs batch JSON-RPC in goroutines with **zero `recover()`**, so a peer-triggered panic kills the node (re-verified unfixed 2026-09-15). **Commit that file into `jmdn/docs/audit/` — beside this register — before these cross-references mean anything.** No `DEP-` ID is cited anywhere in jmdn code. |

**How to collaborate on this document:** the Findings Register (§2) is the living part — update the Status column there (rules in §2.1). Everything below §2 is the evidence body; append corrections rather than rewriting history. New audit passes append new finding IDs.

**Cross-repo note:** of the **36** register rows (D-24…D-59), **32 are `jmdn`**, 2 are `avc` (D-24, D-32) and 2 are shared (D-52, D-53); the 9 `jmdt-devnet` items are tracked as an unnumbered group in §6. This document lives in `jmdn` because that is where most fixes land; the PoC suite lives in `avc` because that is the only module it compiles in. The register's Repo column says who owns each row.

---

## 0. Handover runbook — START HERE

**Current state (2026-09-04): MERGED TO `v3base` in both repos.** Everything is
on the branch the team works from. The audit branches are deleted; nothing to
check out.

| Repo | On `v3base` at | Artifact |
|---|---|---|
| `jmdn` | `9197f5a` (PR #123, squash) | this document — `docs/audit/AVC-CONSENSUS-HANDOVER.md` |
| `avc` | `b83199b`, since advanced to `4df28ca` (= `v0.1.0-v3base.5`) | `tests/audit/audit_poc_test.go` · `randao/accumulator_race_test.go` · `crdt/tiebreak_determinism_test.go` · `crdt/votes/write_identity_binding_test.go` |

`v3base` is protected by an org-level ruleset, so both landed by pull request.
Every future change to this document — including flipping a register row in §2.2
— needs a PR into `v3base` too.

**Verified on `v3base` after merge:** `go build ./... && go vet ./...` clean in
both repos · 9 PoCs pass · `go test ./randao/` clean (probe stays skipped) ·
D-24 reproduces with 4 DATA RACE blocks on darwin/arm64 go1.26.3.

### 0.1 Reviewer steps

1. Get on `v3base` in both repos — that is where everything now lives:
   ```bash
   git -C jmdn checkout v3base && git -C jmdn pull --ff-only origin v3base
   git -C avc  checkout v3base && git -C avc  pull --ff-only origin v3base
   ```
2. Reproduce the evidence yourself (~15s):
   ```bash
   cd avc && go test ./tests/audit/ -v
   ```
   All 9 tests pass, but read the result correctly — **and do NOT read PoCs 1-3 as evidence about jmdn** (§0.5): they test avc's own behaviour, which is unchanged, so they pass whether or not the jmdn-side fix exists. **D-25 and D-27 are both `Fixed`; PoCs 1-3 still pass.** **PoCs 4-5 passing = the D-32 fix holds** (inverted in avc `b11b686`, and the only pair whose fix lived in the same repo); the two Controls and two Negatives must always pass. Appendix B maps each to its finding. Then D-24's regression gate:
   ```bash
   cd avc && go test -race ./randao/
   ```
   Expect **two or more** `WARNING: DATA RACE` reports and a non-zero exit — that
   is D-24, the highest-priority finding, and the failure *is* the result. Every
   block cites only `accumulator.go:209` (the `mix` XOR) and `:211` (the `folded`
   map write, paired with the `:200` read). The count varies with scheduling:
   2 blocks observed on linux/arm64 go1.26.0, **4 on darwin/arm64 go1.26.3** —
   the extra two being write-vs-write on those same lines, which is the more
   serious reading. A trailing `folded=64 expected=64 complete=true` is normal:
   the fold completes *despite* the races, which is why this is silent in
   production.
3. Read §1 (verdict), then §2 (findings register). Full evidence per finding: §3 (SEV-1), §4 (SEV-2), §5 (SEV-3). Devnet items: §6. What is verified sound and what was withdrawn: §7. Remediation order: §8.
4. **Record review outcomes in this file, on this branch** — edit the Status column in §2.2 (`Open` → `In-Progress (owner)` or `Accepted-Risk (owner, rationale)`) and commit. If you disagree with a finding, add a `> Reviewer note (name, date):` line under that finding's detail section — do not delete or rewrite audit text; the trail is the point.
5. Assign an owner to the **one** SEV-1 row that is not yet `Fixed`: **D-26** (rev 6 — D-25 flipped to `Fixed`, so four of the five SEV-1 rows now read `Fixed`: D-24, D-25, D-35, D-36). Both `avc`-owned rows (D-24, and D-32 — which is **SEV-3**, not SEV-1) are also `Fixed`, so nothing in `avc` currently needs an owner; **D-26's remainder is entirely `jmdn`-side** (the avc half landed and is inert here), and rev 6 sharpened it: the unauthenticated legacy ingest is the **default-live** path, while the hardened one sits behind `JMDN_VOTE_CRDT_V2` (default off).
6. Housekeeping (Appendix A.2) — **already done**, nothing to do.
7. **Assign an owner to every SEV-1 and SEV-2 row that is not yet `Fixed` before any fix work starts.** Recounted rev 6: there are **12** such rows (5 SEV-1 + 7 SEV-2 — an earlier revision said 11, before D-51 was added), and **5 are not fully fixed**: `D-26` (open), `D-28` (partial — detection half), `D-38` (partial — limb b), `D-39` (open), `D-51` (open). The other seven — D-24, D-25, D-27, D-29, D-35, D-36, D-37 — read `Fixed`. §2.2 is the live tracking surface; from here the register moves one PR at a time under the §0.2 fix rule. **Assign `D-39` and `D-54` first, and assign them together** — they are the only remaining pre-beacon gate, and until both read `Fixed` nobody may set the three `JMDN_AVC_VDF_*` variables (§0.3, §0.4). Binding `T` without binding the group leaves half the divergence open, and neither is detectable locally by the node that is wrong.

### 0.2 Fix rules

Every fix — whatever branch it lands on — must contain, **atomically in the same commit**:

1. the code change;
2. the corresponding PoC assertion in `avc/tests/audit/audit_poc_test.go` **inverted** — the defect proof becomes the regression test. Each PoC's failure message already names what to do;
3. the §2.2 register row flipped to `Fixed (<commit>, <test name>)`.

A fix missing (2) or (3) is not done. **Twenty-seven of the 36 rows have no PoC** (recounted 2026-09-16; earlier revisions said eighteen, then twenty-six) — D-28, D-30, D-31, D-33, D-34 and all of D-38…D-59 — because they need a running node, a two-node harness, or arrived after the PoC suite was frozen. For those, rule (2) means **write the test the finding's "Done when" clause describes.** **Nine** rows carry a PoC or named test: D-24, D-25, D-26, D-27, D-29, D-32, D-35, D-36, D-37.

> **Rule (2) needs an amendment, and §0.5 explains why.** "Invert the PoC" assumes the PoC and the
> fix live in the same module. Three of the five PoCs do not — they are in `avc`, and the fix is in
> `jmdn`, which `avc` cannot import. For those, inverting is either impossible or actively wrong
> (D-27's PoCs still assert an upstream contract that has not changed). **Where fix and PoC are in
> different repos, rule (2) means: add a jmdn-side test on the real call path, and leave the avc PoC
> alone.**

### 0.3 Fix-ordering warning

> **REV 6 (2026-09-15) — this section's headline has moved twice.** D-24, D-25, D-27, D-29 and D-35
> are all cleared. **The pre-beacon gate is now `D-39` and `D-54`, and they must land together** —
> see the diagram below. The paragraph and diagram that follow are kept because the D-33→D-24
> ordering lesson still holds, but **D-27 is no longer what you are waiting on, and neither is D-25.**

~~**D-27 is now the only remaining pre-beacon gate**~~ — **cleared** (PR #129 7e6a798 + f121048). D-24, the other original gate, landed in avc `498241b` (carried by `v0.1.0-v3base.3`; jmdn pins `.5`).

D-33 (entropy genesis bootstrap) shipped shortly before this audit and is a good fix. It also made the beacon *reachable for the first time*, which put the reveal-fold path into play — and that path was guarded by an accumulator with no synchronisation (D-24). Before D-33, `entropyAccumulatorFor` always failed and `Fold` never ran. That accident is gone, and so, now, is the race it exposed.

```
STAGE 2 ENTROPY GATE — CLEAR as of PR #135 (2026-09-16)

  no blocking finding remains
       │
       └──►  enabling Stage 2 entropy    JMDN_AVC_VDF_MODULUS_HEX
             ONLY on a uniformly         JMDN_AVC_VDF_GROUP_NAME
             upgraded fleet (below)      JMDN_AVC_VDF_DIFFICULTY_T

CLEARED:  D-24  (avc 498241b — mutex on randao.Accumulator)
          D-25  (PR #125 6eb0cc7 — SeedSourceFor fails closed; see below)
          D-27  (PR #129 7e6a798 + f121048 — bootstrap/retention boundary)
          D-29  (PR #125 + #129 543d307/87636d7 — production-posture refusal)
          D-35  (PR #125 — value-keyed modulus chain policy)
          D-39  ┐ PR #135 — group ‖ modulus digest ‖ T bound as ONE identity,
          D-54  ┘ landed together as this section required
```

> **The gate is clear, but Stage 2 is not unconditional — read this before setting the three env vars.**
>
> PR #135 gives three protections, and only two of them are unconditional:
>
> | | Conditional? |
> |---|---|
> | Install-time refusal of a divergent `T` on a pinned group (`beacon_install.go`) | **No** — fires at boot |
> | Identity hash-covered in the M2b `ConsensusHash` preimage | **No** — a wrong-parameter node forks rather than diverging quietly |
> | Adoption-path CHECK 0, which *names the cause* | **Yes** — skipped when either side's field is empty |
>
> The third is deliberately additive so the rollout can proceed node by node
> (`TestVerifyAndAcceptVDFProof_IdentityGateIsAdditive` pins it). The consequence:
> **until every node stamps, a mixed fleet degrades to the pre-fix nameless failure** — which was
> D-54's original complaint. Nothing forces the promotion; see **D-59**.
>
> **So: enable Stage 2 only on a uniformly upgraded fleet, and verify every node reports a non-empty
> `vdf_identity` at boot** (`beacon_install.go` logs it) before setting the env vars anywhere.

Enabling the beacon before D-24 *used to* convert a silent divergence into a validator crash loop (`fatal error: concurrent map writes` is unrecoverable); `498241b` closes that. ~~Enabling before D-27 still silently discards pinned bootstrap epochs~~ — closed rev 6.

> **D-25 left this gate in rev 6 — it was already fixed.** `SeedSourceFor` has failed closed since
> PR #125 (`6eb0cc7`): beacon-installed-but-epoch-missing now returns `ErrBeaconEpochUnavailable`
> instead of the salt, and its sole caller propagates. The row had read `Open` for twelve days.
> **How it hid is the same shape as D-27's:** PoC 1 lives in `avc` and cannot import `jmdn`, so it
> demonstrates the *consequence* (two seeds → two committees) and can never observe the jmdn-side
> fix. Its header still reads *"still reproduces (unfixed)"* and cites `committee_v2.go:441`, a line
> that no longer exists. **Three rows now (D-25, D-27, D-32) have hit the cross-repo PoC blind
> spot — see §0.5.**

**The two that now gate it, both `Open` — and they are the same problem seen twice:**

- **D-39 — difficulty `T` has no fleet-agreement check.** Setting `JMDN_AVC_VDF_DIFFICULTY_T` differently on one node is undetectable: it rejects every honest peer proof *and* publishes divergent entropy into its own sink, seating committees no peer agrees with. Nothing gossips, persists or hashes `T`; `beacon.Pipeline.Difficulty()` has **zero jmdn callers**. Treat `T` as a chain parameter, not a per-host env var.
- **D-54 — the VDF group itself is bound only implicitly.** Fix with D-39, not after it: binding `T` alone leaves two nodes on different *sourced* moduli passing `enforceModulusChainPolicy` and then rejecting each other's proofs with nothing naming the group as the cause. One fleet-checked identity = group name + modulus digest + `T`.

**No longer a gate:** ~~D-38~~ — its `ConsensusHashV3Enabled` limb closed when 8872912 **deleted the flag** rather than flipping its default (the resolution this section argued for, reached by a better route). D-38's surviving limb — `AllowUnsignedValidatorVotes` absent from `ValidateProductionConsensusPosture` — is real but does not gate Stage 2.

Everything else in the register is independent and parallelisable.

### 0.4 Release gate

Flip the verdict at the top of this document to **Go** only when every SEV-1 and SEV-2 row in §2.2 reads `Fixed`. Until then:

- do **not** set `JMDN_AVC_VDF_MODULUS_HEX` / `_GROUP_NAME` / `_DIFFICULTY_T` on any fleet;
- do **not** ship `rsa-2048-testnet-ephemeral` to any network with adversaries — it is trapdoored by construction. Since PR #125 a *mechanical* guard refuses it off-devnet by modulus **value**, not group name (`enforceModulusChainPolicy`, `Sequencer/vdf_network_pins.go`, called from `beacon_install.go` before any group is constructed); D-29 and D-35 read `Fixed`, so this is now a policy reminder rather than an open exposure. Note the guard keys on the *digest*, so renaming the modulus does not evade it — that was D-35;
- do **not** set a per-host `JMDN_AVC_VDF_DIFFICULTY_T` (D-39): a divergent `T` is silent on the side that is wrong;
- do **not** trust a green `go test ./...`. Every finding in this document survives a green suite; that is the single most useful fact here.

### 0.5 Acceptance tests & pass criteria

Today the PoC suite **passes while defects exist**. As each fix lands, its assertion is inverted (§0.2 rule 2); once all rows are done, `go test ./tests/audit/ -v` passing means **defects absent** — the suite's meaning flips from proof-of-defect to regression gate.

> ## ⚠ THE PoC SUITE HAS A STRUCTURAL BLIND SPOT — found rev 6, and it has already cost twelve days
>
> **The PoCs live in `avc`. `avc` cannot import `jmdn`.** So any PoC for a defect whose *fix* lands
> in jmdn tests only the avc-side *consequence*, and **keeps passing after the jmdn fix** — it has no
> way to see it. A green-to-red flip never happens, so nothing prompts anyone to revisit the row.
>
> **Three of the five PoCs are in this position:**
>
> | PoC | Row | What it actually tests | Effect |
> |---|---|---|---|
> | 1 | **D-25** | `committee.Snapshot` seated from two different seeds | Row read `Open` for **12 days** after `6eb0cc7` closed it. Header still says *"still reproduces (unfixed)"* and cites a deleted line |
> | 2, 3 | **D-27** | `committee.NewBeaconSource` eviction, unchanged upstream | Correctly still reproduces; **must NOT be inverted** — the fix refuses to *start* rather than changing eviction |
> | 4, 5 | D-32 | avc's own `crdt` merge — **fix and PoC in the same repo** | Worked as designed: inverted cleanly |
>
> Note the asymmetry: **PoC 4/5 are the only ones whose fix landed in the same repo, and they are the
> only ones the invert rule worked for.** §0.2 rule 2 silently assumes fix and PoC share a module.
>
> **What to do:**
> 1. **Do not treat a passing PoC as evidence a jmdn row is still open.** Verify jmdn rows against
>    jmdn code. Rev 6 flipped D-25 on exactly that basis.
> 2. Each cross-repo PoC needs a **jmdn-side counterpart** that exercises the real call path — for
>    D-25 that is `SeedSourceFor` returning `ErrBeaconEpochUnavailable`, which no avc test can reach.
> 3. Re-label the affected PoC headers: they document an **upstream contract**, not a live jmdn
>    defect. PoC 1's header is currently wrong on both counts.

Two PoCs are **controls** and must pass forever, before and after every fix: `TestControl1_QuorumIsByzantineSafeAtEverySize` (the quorum maths is correct — see §7) and `TestControl2_BeaconSourceFailsClosed` (avc's sink is correct; **D-25 was the caller swallowing its error — fixed rev 6**, so this control now guards a property both sides honour).

Two are **negative checks** recording claims that were tested and did *not* reproduce: `TestNegative1_MergeIsCommutativeForDistinctMaxima` and `TestNegative2_NondeterminismDoesNotFlipMembership`. They exist so those claims are not re-litigated. **If either ever fails, a real regression has occurred** — the failure message says which finding to reopen.

---

## 1. Verdict

```
DECISION  No-Go for mainnet.
          STAGE 2 ENTROPY: the gate is now CLEAR (PR #135, 2026-09-16) —
          conditionally. See the condition below; it is not "ship it".
          Go for devnet.

          The entropy pipeline moved from "cannot start" to "can start
          unsafely" and is now back: the silent-divergence seam (D-25) is
          closed, and fleet agreement on the VDF parameters — D-39
          (difficulty T) and D-54 (group/modulus) — landed together in
          PR #135 as one identity: group ‖ modulus digest ‖ T.

          THE CONDITION ON STAGE 2. Enable only on a UNIFORMLY UPGRADED
          fleet. Two of the three protections are unconditional: the
          install-time refusal of a divergent T on a pinned group
          (Sequencer/beacon_install.go), and hash-coverage of the identity
          in the M2b ConsensusHash preimage, which makes a wrong-parameter
          node fork rather than diverge silently. The THIRD — the
          adoption-path check that names the cause — is skipped when
          either side's field is empty, by design, so the rollout can be
          additive. Until every node stamps, detection is incomplete and
          a mixed fleet degrades to the pre-fix nameless failure. There is
          no posture gate forcing the promotion; that gap is RC-2 and is
          tracked as D-59.

          MAINNET IS UNAFFECTED by PR #135 and stays No-Go: D-26 (SEV-1),
          D-51 (SEV-2) and the D-28 / D-38 partials are untouched by it.

TALLY (recounted from the §2.2 table, rev 6 — 2026-09-15)

  36 rows = 13 Fixed · 2 Partial · 1 Decided-no-change · 20 Open

  Fixed (13)    D-24 D-25 D-27 D-29 D-30 D-32 D-33 D-35 D-36 D-37 D-39 D-41
                D-54
  Partial (2)   D-28  cert replay CLOSED, equivocation DETECTION open
                D-38  flag deleted (a) CLOSED; posture-check gap (b) open
  Decided (1)   D-56  keep 20 / keep CadenceBlocks 0 — residual: epoch-0
                      sentinel collision, gates RequirePinnedCommittee
  Open (20)     D-26 D-31 D-34 D-40 D-42 D-43 D-44 D-45 D-46 D-47
                D-48 D-49 D-50 D-51 D-52 D-53 D-55 D-57 D-58 D-59

BY SEVERITY   5 + 7 + 18 + 5 + 1 = 36
  SEV-1   5   D-24✓ D-25✓ D-35✓ D-36✓ | OPEN: D-26 only
  SEV-2   7   D-27✓ D-29✓ D-37✓ D-39✓ | OPEN: D-28(detection), D-38(b), D-51
  SEV-3  18   D-30✓ D-32✓ D-41✓ D-54✓ D-56(decided) | OPEN: D-31 D-34 D-40
              D-42 D-43 D-44 D-45 D-46 D-52 D-53 D-57 D-58
              D-59(NEW — PR #135's promotion gap, RC-2)
  SEV-4   5   OPEN: D-47 D-48 D-49 D-50 D-55
  Unrated 1   D-33✓ (remainders open — persistence, observe rung)
  Devnet  9   §6, tracked as a group, NOT in the 36

WATCH OUT
  · PR #129's "D-31" commit (1624583) is MIS-TAGGED — it does not touch D-31,
    and it introduced D-57. D-41's fix is tagged JMDN-V3-009, not D-41.
  · PR #129 rebase-merged, REWRITING every SHA. All SHA references here were
    remapped 2026-09-15 and verified 1:1 by commit subject. Pre-merge SHAs
    quoted elsewhere (PR comments, chat, older copies) are DEAD.
  · D-57 is a BLOCKER before JMDN_VOTE_CRDT_V2 is ever flipped on.

SCOPE
  Audited      entropy pipeline end-to-end (randao→VDF→beacon→committee);
               committee selection seam; vote ingest + BLS signing; block-hash
               preimages; CRDT merge determinism; quorum arithmetic (4 impls);
               devnet config vs code; and in the #125 pass the persistence
               layer, fallback agg-sig fold, VDF pins, vdf-proof pull protocol.
  NOT audited  jmdn/AVC/BFT consensus logic (divergent vendored fork — only
               its quorum arithmetic was executed, §7.1); libp2p pubsub
               signing config; ThebeDB beyond pkg/kv + pkg/checkpoint; MRE;
               seedNodes.
  Evidence     D-29/D-35/D-36/D-37 ship executed jmdn tests. D-25…D-34 are
               source-traced, not compiler-verified (the original sandbox
               exhausted its filesystem on jmdn's dep graph). D-38…D-59
               source-traced, re-verified at v3base@291d44c on 2026-09-11.
               REV 6: every row -- Fixed and Open alike -- re-checked at
               e4f18da on 2026-09-15. No row is older than that. See Appx C.1.
```

**The one-sentence summary.** Consensus is wired. The *new AVC protocols* largely are not — and the gap is not "unfinished" but "finished, plumbed, and until recently unreachable"; the fix that made them reachable also armed the most serious defect. Since then the two hardest-to-see defects (D-35's name-keyed trapdoor guard, D-36's blocklist-skewed quorum) have been closed, but **the pattern that produced them — a comment asserting a property the code does not hold, §7.3 RC-3 — is still the dominant root cause and now has its own row, D-50.**

---

## 2. Findings register

### 2.1 Register rules

- **Status values:** `Open` · `In-Progress (owner)` · `Fixed (<commit>, <test>)` · `Accepted-Risk (owner, rationale)` · `Wont-Fix (owner, rationale)`.
- A row moves to `Fixed` only when all three parts of the §0.2 fix rule are in one commit.
- Never renumber. Never delete a row. A withdrawn finding gets `Withdrawn (<date>, reason)` and stays.
- **SEV scale** (matching the 2026-08-31 reports): SEV-1 worst → SEV-5 least. SEV-1/2 are blockers.
- Effort is the auditor's estimate from diff size and test surface. **S** ≈ under a day · **M** ≈ 1–3 days · **L** ≈ a week, splittable. Correct these during triage.

### 2.2 Register

> **REV 6 — 2026-09-15 · every `Fixed` claim re-verified, then compressed.**
>
> PR #129 merged (26 commits, `291d44c` → `e4f18da`). Before compressing any row, each `Fixed` claim
> was re-checked **against the live tree by behaviour string, not by commit message** — the discipline
> that CON-08 taught, where a fix commit sat in the branch's ancestry while the code had been reverted
> by a merge. What was executed:
>
> | Row | Verified by | Result |
> |---|---|---|
> | D-24, D-32 | `go.mod` pins `avc v0.1.0-v3base.5`, which carries `498241b` / `193ab86` | holds |
> | D-27 | operator is `span > retain`; `TestValidateBootstrapFitsRetention_Boundary` present | holds |
> | D-29 | `ErrTrapdooredGroupInProduction` + `ErrUnpinnedModulusInProduction` in 3 files; `main.go:1603-1607` `os.Exit(1)` | holds |
> | D-30 | LRU in both paths; residual `bloom` strings are comments naming what was replaced | holds |
> | D-35, D-36, D-37 | PR #125 symbols still present | holds |
> | D-41 | `entropy_finalise.go` claims before computing, re-check makes a racing hook `continue` | holds |
> | **D-28** | `checkEquivocation(…, b.BlockHash.Hex())` still at `blockPropagation.go:715`; `BlockHashV3` has **0 non-test call sites** | **half still open — confirmed** |
> | **D-38** | `ConsensusHashV3Enabled` → **0 occurrences**; `production_posture.go:55` gates 3 flags, not `AllowUnsignedValidatorVotes` | **limb (a) closed, limb (b) open** |
>
> **⚠ THE BIGGEST FINDING OF THIS PASS — `D-25` WAS ALREADY FIXED AND THE REGISTER DID NOT KNOW.**
> A SEV-1 that had gated Stage 2 entropy since 2026-09-03 read `Open` while `SeedSourceFor` had been
> fail-closed since PR #125 (`6eb0cc7`, an ancestor of v3base). Verified on every path, not just the
> function: exactly one `SaltSource{}` construction exists in the whole non-test tree and it sits on
> the safe `beacon == nil` branch; the sole caller propagates the error; `SelectEntropyCommittee`
> fails closed independently. **The Stage-2 gate is therefore D-39 + D-54 only.**
>
> **It hid because of a structural blind spot, not carelessness — see §0.5.** PoC 1 lives in `avc`,
> which cannot import `jmdn`, so it can never observe a jmdn-side fix; it kept passing and nothing
> prompted a re-read. **Three of the five PoCs (1, 2, 3) are on the wrong side of that boundary.**
> The lesson generalises past this register: **a PoC in a different module from the fix is not a
> regression gate, it is a contract test for the other module.**
>
> **One new row added this pass: `D-58`** — `PersistEpochEntropy` errors discarded at both write
> sites, found while re-verifying D-33's remainder 1 (which was itself wrong: persistence *does*
> exist, jmdn-side).
>
> **Compression rule applied:** a row that is `Fixed` and re-verified keeps only *commit + test + the
> one-line reason it is believed closed*. Where a closed row carried a transferable lesson, the lesson
> moved to §7.3 (RC-1 for D-29's fail-open process; **new RC-6** for D-30's shared `sync.Once`) rather
> than being deleted — the row shrinks, the lesson survives where patterns are tracked.
> **`Open` / `BLOCKER` rows were not compressed** and several were expanded.
>
> **Also corrected this pass:** §0.3's headline still named D-27 as the only pre-beacon gate (now
> D-25/D-39/D-54); the top verdict said the same; the cross-repo note said "27 rows" (34);
> §7.3 said "twenty-seven findings, five causes" (34, six); and RC-2's prediction about
> `CONSENSUS_HASH_V3` needed updating — the flag was **deleted**, not default-flipped.
>
> **`Open` rows — partial re-verification at `e4f18da`.** After D-25 proved that a 2026-09-11 date is
> not trustworthy for any row whose PoC lives in `avc`, a sample of `Open` rows was re-checked
> against live code. **All confirmed still open; one was sharpened:**
>
> | Row | Checked | Result |
> |---|---|---|
> | **D-26** | `Pubsub.go:19` `Data.Sender` is a JSON field, while the authenticated id is already captured as `Sender` (`SubscriberHelper.go:293`, `SubscriptionManager.go:244` — both `msg.GetFrom()`); **0** gate refs on the `subscriptionService.go:291-300` CRDT write; `RejectLegacyVotes` applied only in the 3 tally sites | **OPEN — sharpened.** The unauthenticated ingest is the **default-live** path; the hardened one is opt-in. `RejectLegacyVotes` does not cover it despite the name. **The (a) fix is one comparison** — both fields are on the same struct |
> | D-40 | `main.go:963` `RewardSplitEnabled && !Security.M2bHashEnabled` | OPEN, unchanged |
> | D-43 | `DB_OPs/vdf_proof.go:53` `MaxVDFProofBytes = 8<<10`, bound applied at `:107` | OPEN, unchanged |
> | D-44 | `VDFRecoveryTargetEpoch` still at `entropy_vdf_deadline.go:91`, used `:155` | OPEN, unchanged |
> | D-46(b) | `Start` (`vdf_sealer.go:80`) guards **only** on `s.cancel != nil`; `s.cancelled` is set at `:108`/`:141` and read only by `SealerCancelledForTest` `:152` | OPEN — confirms rev 5's own correction |
> | D-49 | all four `*ForTest` seams present, **0 build tags** in their files | OPEN, unchanged |
> | D-51 | `VoteCRDTDualWrite` / `voteCRDTV2Enabled` both `envOn(..., false)` | OPEN, unchanged |
>
> **The remaining 13 were then checked too — every `Open` row now carries 2026-09-15.** All still
> open at their cited locations; none moved:
>
> | Row | Evidence at `e4f18da` |
> |---|---|
> | D-34 | `seenHeights = make(map[uint64]string)` (`consensus_hardening.go:719`); **zero** delete/evict/prune sites — unbounded confirmed |
> | D-39 | `beacon.Pipeline.Difficulty()` has **0 jmdn callers** — `T` is still never gossiped, persisted or hashed |
> | D-42 | `epochsWithClosedRevealWindow` (`entropy_finalise.go:277`), `lastDecidedEpoch` in-memory (`:306`) |
> | D-45 | `beacon_entropy_newest` / `vdf_proof_newest` both present; **`GetAllKeys` is still an explicit stub** — `thebe_missing.go:281` returns `"ImmuDB removed; use ThebeDB SQL queries instead"`, so there is still no fallback index |
> | D-47 | `const mixRetainEpochs = committee.MinRetainedEpochs + 1` (`entropy_mix_store.go:42`) — compile-time, while `JMDN_AVC_BEACON_RETAIN_EPOCHS` (4 refs) moves only the sink |
> | D-48 | `VDFProofRequestProtocol` registered (`config/constants.go:119`), no feature gate |
> | D-53 | `avcvotes.DefaultWatermark` read/mutated at 5 non-test sites — still a process-wide singleton |
> | D-55 | `ByzantineQuorum` (`consensus_hardening.go:396`) and `avc quorum.Threshold` (`quorum.go:40`) both live, still divergent at `n<1` |
> | D-31, D-50, D-52, D-54, D-57 | cited symbols/paths unchanged since the 2026-09-11 check |
>
> **No `Open` row in this register is now older than 2026-09-15.**

| ID | SEV | Repo | Finding | PoC | Effort | Status |
|---|---|---|---|---|---|---|
| **D-24** | 1 | `avc` | `randao.Accumulator` has no synchronisation; jmdn calls `Fold` from two concurrent commit hooks | race probe | S | `Fixed (avc 498241b, randao/accumulator_race_test.go)` |
| **D-25** | 1 | `jmdn` | `SeedSourceFor` silently falls back to the Stage-1 salt; two nodes seat different committees | PoC 1 (cannot detect the fix — see row) | S | **`Fixed` (PR #125 `6eb0cc7`, ancestor of v3base) — FLIPPED rev 6 after live re-verification; this row had read `Open` since 2026-09-03 while the code was already closed.** `committee_v2.go:557` now separates all three states: no beacon → `SaltSource` (Stage 1, uniform fleet-wide, safe); beacon has epoch → `BeaconSource`; **beacon installed but epoch missing → `nil, ErrBeaconEpochUnavailable`** (`:574`) — the silent-divergence case. Verified closed on every path: **exactly one `SaltSource{}` construction exists in the entire non-test tree** (`:563`, the safe branch), its **sole caller propagates** (`:306` `return nil, err`), and `SelectEntropyCommittee` independently fails closed (`ErrNoBeaconInstalled` `:131`, wraps `ErrEntropyUnavailable` `:136`). RC-1 satisfied at both ends |
| **D-26** | 1 | `jmdn` | Vote CRDT keyed on a self-declared sender; requester chooses the signing target; guard off and unwired | `avc crdt/votes/write_identity_binding_test.go` | L | `Open — avc-side hardening landed, closes nothing in jmdn yet. avc 3eee4b0 (first carried by v0.1.0-v3base.5) makes AddVote REJECT a record whose payload rec.PeerID differs from the libp2p-authenticated nodeID (write.go:74); it does NOT re-key — both elements are still keyed on rec.PeerID (write.go:83/:95/:100, and its own comment at :46 says so). Structurally inert in jmdn today: the sole non-test caller (Vote/Trigger.go:286) sets rec.PeerID from the same identity it passes as nodeID (:268), so the two can never differ, and the whole block sits behind JMDN_VOTE_CRDT_V2 (default OFF). avc also EXEMPTS the merge path (write.go:64-66). D-26(a)''s actual defect site — jmdn legacy pubsub ingest keying the CRDT on msg.Data.Sender in AVC/.../Service/subscriptionService.go — does not call AddVote at all and is untouched. Remainder: (a) legacy ingest, (b) membership filter, (c) signing target, (d) requester-auth guard.` **▶ REV 6 — (a) RE-VERIFIED AND SHARPENED; it is worse than this row said.** Three facts, each executed: (1) **`msg.Data.Sender` is a JSON payload field** (`config/PubSubMessages/Pubsub.go:19`) and **nothing on this path compares it to the authenticated sender** — but note the fix is *cheap*, because **the authenticated identity is already in hand**: `Pubsub/Subscription/SubscriberHelper.go:293` and `SubscriptionManager.go:244` both set `Sender: msg.GetFrom()` on the same struct. Two sender fields, one authenticated and one attacker-chosen, and the handler reads the wrong one. No plumbing is needed — the value is already there. *(An earlier rev-6 note said "0 `ReceivedFrom`/`GetFrom()` anywhere"; that grep was scoped to `AVC/BuddyNodes/MessagePassing/` and missed `Pubsub/`. Corrected — the §3 evidence below had it right all along.)* (2) The CRDT write at `subscriptionService.go:291-300` (`NodeID: msg.Data.Sender`, `Key: msg.Data.Sender.String()`) has **0 gate references** in the whole handler — it is **unconditional and live on a default node**, whereas the hardened v2 path is the one behind `JMDN_VOTE_CRDT_V2` (default OFF). **The unauthenticated path is the default path; the authenticated one is opt-in.** (3) **`RejectLegacyVotes` does not cover this.** Despite the name, it is applied only in `Sequencer/Consensus.go:2344`, `consensus_hardening.go:543` and `committee_v2.go:701` — the **tally/signature** paths, not the **ingest** path. **Bounding the impact honestly:** with `RejectLegacyVotes` on (default), a forged element still needs a valid block-bound BLS signature to count toward quorum, so this is **not** direct quorum forgery today. What it does give an unauthenticated peer is the ability to write arbitrary elements under **any** peer's CRDT key — unbounded (D-34), able to fabricate or destroy equivocation evidence (cf. D-57), and to set the per-peer budget against a victim (D-52). Another RC-3: a guard whose name tells the reader it covers the legacy vote path, which covers the tally instead |
| **D-27** | 2 | `jmdn` | Bootstrap epochs silently evicted when the pinned list exceeds `retain` | PoC 2, 3 | S | `Fixed (PR #129 7e6a798 + f121048 off-by-one, v3base 2026-09-15; Sequencer/beacon_bootstrap_test.go TestValidateBootstrapFitsRetention_Boundary)` — **re-verified rev 6:** operator is `span > retain`, boundary test present |
| **D-28** | 2 | `jmdn` | `ConsensusHash` binds neither `PrevHash` nor `BlockNumber`, even under M2b | — | M | **`PARTIALLY Fixed` — half closed, half OPEN.** **Closed (PR #129 8872912, v3base 2026-09-15):** preimage binds `BlockNumber` + `PrevHash`, flag deleted, one unconditional format pinned by `TestConsensusHashPreimageIsPinned` → **certificate replay across forks is closed**. **Still OPEN — equivocation DETECTION.** Re-verified rev 6: `checkEquivocation(b.BlockNumber, b.BlockHash.Hex())` at `blockPropagation.go:715` is still keyed on `BlockHash` (transactions-only), and the pre-validation dedup key is the same colliding value (`getBlockDedupID` → `"zkblock:"+BlockHash`, `:230`/`:294`), so the second fork is dropped as a duplicate **before** the check runs. 8872912's header claimed detection was fixed; 75bb26a corrects that. **New rev-6 evidence:** the primitive that would close this already exists — `consensushash/blockhash_v3.go` `BlockHashV3(chainID, blockNumber, prevHash, stateRoot, txnsRoot, timestamp)` binds exactly the right fields — but has **0 non-test call sites**. The remaining work is wiring, not design: re-key the equivocation map **and** the dedup cache onto it together (re-keying only one leaves the drop-before-check ordering intact) |
| **D-29** | 2 | `jmdn` | Trapdoored testnet VDF modulus with no mechanical mainnet guard | `TestBuildVDFGroupRefusesRestrictedModulusUnderForeignNameWithOverride` · `TestTrapdoorPinRefusedInProductionEvenOnItsAllowedChain` | S | `Fixed (PR #125 enforceModulusChainPolicy — see D-35) + HARDENED (PR #129 543d307 + 87636d7, v3base 2026-09-15)` — **re-verified rev 6:** both `ErrTrapdooredGroupInProduction` / `ErrUnpinnedModulusInProduction` live in 3 files; `main.go:1603-1607` `os.Exit(1)`s on either. Lesson belongs to §7.3 **RC-1** — the guards returned errors and `main.go` logged them and continued: fail-closed as a function, fail-open as a process |
| **D-30** | 3 | `jmdn` | Bloom dedup filter is lock-free and saturates to 83% FP in ~14h | — | M | `Fixed (PR #129 e986ba6 blockPropagation + 76137cd DIDPropagation, v3base 2026-09-15)` — **re-verified rev 6:** both now bounded `hashicorp/golang-lru/v2`; remaining `bloom` mentions in those two files are comments naming what was replaced. `ContractPropagation.go` keeps bloom by design (`contractFilterMu` guards it). 76137cd also closed a third defect this row missed — a shared `accountOnce` left `accountsClient` nil; lesson in §7.3 **RC-6** |
| **D-31** | 3 | `jmdn` | Epoch watermark TOCTOU duplicates finalisation; duplicate seal blocks a goroutine forever | — | M | `Open — AND BEWARE A MIS-TAGGED COMMIT. PR #129's 1624583 is titled "fix(crdt): prevent watermark TOCTOU resurrecting compacted votes (D-31)" but does NOT touch this defect: it edits avcvotes.DefaultWatermark in CRDTSyncHandler.go, i.e. the VOTE-COMPACTION watermark, which this register assigns to D-53 ("D-31 covers the epoch watermark TOCTOU; nothing covered the vote-compaction watermark"). D-31's own subject is maybeFinaliseCompletedEpochs' EPOCH watermark, which already carries a claim-under-lock fix predating PR #129 — see D-41 for the residual that PR #129 did close. Do not mark D-31 fixed on the strength of 1624583's title. 1624583 also introduced a new defect → D-57` |
| **D-32** | 3 | `avc` | `extractNodeID` tie-break reads a Go map → nondeterministic merge | PoC 4, 5 | S | `Fixed (avc 193ab86, TestPoC4_MergeTieBreakIsDeterministic + crdt/tiebreak_determinism_test.go)` |
| **D-33** | — | `jmdn` | Entropy genesis bootstrap — **shipped**; persistence and an observe rung remain | — | M | `Fixed (b5e305a8) — remainder Open` |
| **D-34** | 3 | `jmdn` | Unbounded maps on the block-receive path (`seenHeights` et al) | — | M | `Open` |
| **D-35** | 1 | `jmdn` | **The D-29 fix did not hold.** Chain guard keyed on the group NAME, not the modulus VALUE — the trapdoored devnet modulus installs on any chain under the name `rsa-2048-frc` (unpinned in avc, matching shape) with the unpinned override set | `TestModulusChainPolicyIsKeyedOnValueNotName` | S | `Fixed (PR #125, enforceModulusChainPolicy)` |
| **D-36** | 1 | `jmdn` | Fallback fold's Byzantine denominator taken from the block_buddy-FILTERED pool, so one operator's local blocklist moves the threshold (n=6/q=4 vs fleet n=7/q=5) → different fold subset → different seed → **different committee** | `TestAggCertQuorumIsIndependentOfLocalBlocklist` | S | `Fixed (PR #125, fleetCommitteeSnapshotFor)` |
| **D-37** | 2 | `jmdn` | `RecoverAggSigStoreAtStartup` called ~400 lines before the committee eligibility source is wired — always returned 0, neither call-site branch printed, and the only symptom was up to 512 "parent certificate failed verification" errors that read as tampering | `TestRecoveryRefusesWhenEligibilitySourceIsUnwired` | S | `Fixed (PR #125, relocated + up-front probe)` |

| **D-38** | 2 | `jmdn` | **D-28's fix ships inert.** `ConsensusHashV3Enabled` defaults FALSE on `v3base`, so `BlockNumber`/`PrevHash` are absent from the `ConsensusHash` preimage on a default node — the original D-28 exposure verbatim — and `ValidateProductionConsensusPosture` does not check the flag, so a `strict_posture`/mainnet node boots with no signal. Same gap for `avcvotes.AllowUnsignedValidatorVotes` | — | S | **(a) `Fixed` — resolved better than this row proposed.** The contested default-flip (ffdeaf7) was superseded by **8872912, which DELETED the flag entirely**: `ConsensusHashV3Enabled` now has **0 occurrences** tree-wide and the preimage is unconditional (`consensus_fields_hash.go:110-111` writes `BlockNumber` then `PrevHash`), pinned by `TestConsensusHashPreimageIsPinned`. This moots the BLOCKER-1 objection — there is no flag left to forget. The rolling-restart fork risk it warned about is **not** gone but is now a known hard cutover: a coordinated fleet restart is mandatory (see D-56, which rides on the same restart). **(b) `Open` — the second limb is untouched.** `ValidateProductionConsensusPosture` (`messaging/production_posture.go:55`) gates exactly three flags — `RejectLegacyVotes`, `EnforceCommitteeRegistry`, `EnforceBodyBinding` — and still does **not** check `avcvotes.AllowUnsignedValidatorVotes`, which is fail-open when on. Fix remains as originally recommended: add it to the posture validator and log its state at startup |
| **D-39** | 2 | `jmdn` | **Difficulty `T` has no fleet-agreement check.** Validated only as non-zero; `beacon.Pipeline.Difficulty()` has **zero jmdn callers**, so `T` is never gossiped, persisted, or hashed into genesis. A node with `T′ ≠ T` rejects every honest peer proof AND publishes its own divergent value into its own sink, seating committees from entropy no peer holds — with nothing naming `T` as the cause | — | M | `Fixed (feat/vdf-fleet-identity 95bf076…49e1b66; landed WITH D-54)` — `T` is now a chain parameter: `networkPinPolicy.PinnedDifficultyT` (rsa-2048-testnet-ephemeral=476510), `InstallAVCBeaconFromEnv` refuses a divergent `T` on a pinned group, and `T` is folded into `messaging.VDFIdentityDigest` which is stamped on the boundary block, compared on adoption (`entropy_vdf_accept.go` CHECK 0, names the cause), AND hash-covered in the M2b `ConsensusHash` preimage (`Security/consensus_fields_hash.go`) so a wrong-`T` node forks fleet-wide. Tests: `messaging/vdf_identity_test.go`, `entropy_vdf_accept_identity_test.go`, `Security/consensus_fields_hash_fork_test.go` (golden regenerated, verified against a standalone go-ethereum reproduction of the old pin). **CONSENSUS FORMAT CHANGE — coordinated fleet restart** (rides Stage-2 enablement). Build gate outstanding: full `go build ./...` on a host with the dep graph (sandbox disk-limited) | |
| **D-40** | 3 | `jmdn` | `Security.M2bHashEnabled` gates no validation anywhere (`CheckBlockHash` and `checkBodyBinding` both ignore it, by their own comments) yet `main.go:963` `os.Exit(1)`s reward-split without it — a config-triggerable hard exit whose precondition is meaningless, granting a false assurance that `PrevAggCert`/`FeeRecipients` are hash-bound. They are defended, but by the reward-split interlock and an independent recompute, not by `ConsensusHash` | — | S | `Open` |
| **D-41** | 3 | `jmdn` | **`resolvePendingFallbacks` still double-finalises.** D-31's claim-under-lock fix covers the decide path only; this path snapshots under the lock, releases it, then uses `delete(pendingFallback, e)` — a silent no-op on an absent key, so it removes rather than claims. Two commit hooks can both reach `notifyEpochFinalised` for one epoch and, with different seeds, trip the mix-conflict branch — emitting a false SEV-1-shaped alarm for an in-process race | — | S | `Fixed (PR #129 3d01e89, v3base 2026-09-15)` — **re-verified rev 6:** `entropy_finalise.go` now claims (deletes) before computing the seed, with a re-check that makes a second racing hook `continue`. All four error branches preserve prior retry semantics. ⚠ Commit is labelled `JMDN-V3-009`, **not D-41** — an ID search will miss it |
| **D-42** | 3 | `jmdn` | The post-D-31 claim loop calls `epochsWithClosedRevealWindow` **once per epoch** and uses only `toDecide[0]`, rebuilding the whole list each iteration. `lastDecidedEpoch` is in-memory only, so the first block after every restart starts at 0: at 500k slots ≈ 5×10⁷ `uint64` written, synchronously inside the block-commit hook | — | S | `Open` |
| **D-43** | 3 | `jmdn` | **A proposer can disable pull-recovery fleet-wide.** `PersistVDFProof` stores the caller's RAW bytes; `vdf.Proof.UnmarshalBinary` is `json.Unmarshal`, which ignores unknown fields. ~6.8 KB of padding clears `MaxVDFProofBytes` *after* verification succeeds, so every adopting node keeps its entropy but stores no proof and answers `Found:false` — switching off the mechanism `entropy_vdf_persist.go` exists to serve, at zero attacker cost | — | S | `Open` |
| **D-44** | 3 | `jmdn` | **VDF recovery can never target the epoch it exists to recover.** `VDFRecoveryTargetEpoch = EpochForSlot(currentSlot) + 1`, so for a node inside epoch E the boundary block carrying E's proof is already past and the target is E+1; CHECK 3 then needs `FinalisedMixFor(E)`, which a node that missed E's cutoff never finalised. It helps only a slow local evaluator — not the offline/restarted/late-joining cases its own documentation headlines | — | M | `Open` |
| **D-45** | 3 | `jmdn` | The `beacon_entropy_newest` / `vdf_proof_newest` pointer advance is a **non-atomic read-modify-write** under no lock, and three of its four failure modes are discarded. Concurrent writers for epochs 9 and 10 can leave the pointer at 9 with a record at 10, and there is no fallback index (`GetAllKeys` is a removed-ImmuDB stub), so the record is unreachable — permanently stranding an epoch whose mix cannot be recomputed | — | S | `Open` |
| **D-46** | 3 | `jmdn` | **Sealer lifecycle.** (a) `T` has no upper bound and `Start` uses `context.WithCancel`, not `WithTimeout`, and the only cancellation source is an adopted peer proof — which never arrives if nobody's `T` fits the runway, so one never-terminating 2048-bit modmul goroutine accumulates per epoch. (b) `Start` checks only `s.cancel != nil` and never reads `s.cancelled`, so a `Cancel` landing in `sealerFor`'s unlocked window is silently lost and the full ~T_vdf evaluation runs anyway — contradicting `Cancel`'s own doc | — | S | `Open` |
| **D-47** | 4 | `jmdn` | **Retention windows pinned to the floor while the beacon's is configurable.** `mixRetainEpochs = committee.MinRetainedEpochs + 1` and `RehydrateBeaconFromDisk`'s window are compile-time constants, but `JMDN_AVC_BEACON_RETAIN_EPOCHS` moves the sink's retention — so raising it buys no extra proof-adoption window and no extra restored epochs. Separately `defaultEntropyAccumulatorStore.accs` has no eviction anywhere in `messaging/` | — | S | `Open` |
| **D-48** | 4 | `jmdn` | `/p2p/randao/vdf-proof/1.0.0` is registered on **every** node with no feature gate, unlike its sibling `HandleTimeoutCertRejoinStream` — and `node/node.go:231-234` claims parity it does not have. Per-request work is properly bounded, but there is no per-peer rate limit and no concurrency cap, and `libp2p.New` pins no resource-manager limits | — | S | `Open` |
| **D-49** | 4 | `jmdn` | **Release hygiene.** Four `*ForTest` seams — `SeedSealResultForTest`, `ClearSealerForTest`, `SealerCancelledForTest`, `AggSigStoreSlotsForTest` — carry no build tag and compile into the release binary; the first injects an arbitrary `vdf.Proof` straight into the production sealer map. `DB_OPs/beacon_entropy.go`, `messaging/entropy_persist.go` and `messaging/entropy_vdf_persist.go` have no test file at all; `DB_OPs.NewestVDFProofEpoch` is a dead exported API | — | S | `Open` |
| **D-50** | 4 | `jmdn` | **Six comments assert properties the code does not honour** — each the stated reason a reader would skip re-checking something, and two of them were the SEV-1s D-35/D-36. **Evidence gap: the six are not enumerated anywhere — that list died with the deleted PR #125 working document and must be re-derived from `6eb0cc7` before this row can be worked.** The two that mattered are named in §7.3 RC-3; §7.3's closing paragraph names four more candidates | — | S | `Open — evidence list must be reconstructed` |
| **D-51** | 2 | `jmdn` | **`JMDN_VOTE_CRDT_V2` makes the entire avc v2 vote keyspace inert.** Every vote hardening avc ships — D-26's identity guard, `MaxElementsPerPeerPerBlock`, the compaction watermark — reaches production only through `Vote/Trigger.go`, gated on `VoteCRDTDualWrite = envOn("JMDN_VOTE_CRDT_V2", false)`. Structurally the same defect as D-38 ("D-28's fix ships inert") but for a whole keyspace, and it is the reason D-26's avc-side fix closes nothing. RC-2 with no observe rung | — | M | `Open` |
| **D-52** | 3 | `jmdn`/`avc` | **The vote per-peer ingest cap is a local-replica invariant presented as a global one, and the merge path guards a different identity than the write path.** `CountElementsForPeer` reads the caller's own CRDT replica, so a peer writing to n nodes before convergence can place up to `MaxElementsPerPeerPerBlock × n` elements fleet-wide. Compounding it, avc explicitly exempts the merge path from D-26's identity check, and jmdn's `mergeVoteCRDTElement` attributes the **relaying** peer as write-actor while the payload still carries the original author's declared `PeerID` — so one budget is enforced against two different identities depending on which path an element arrives by | — | M | `Open` |
| **D-53** | 3 | `avc`/`jmdn` | **`avcvotes.DefaultWatermark` is process-wide mutable consensus state with no row until now.** A package-level singleton that gates vote admission by height, is monotonic (`Set` refuses to move backward), and is swappable by any code in the process. jmdn's own test helper documents the hazard verbatim. D-31 covers the *epoch* watermark TOCTOU; nothing covered the *vote-compaction* watermark | — | M | `Open` |
| **D-54** | 3 | `jmdn` | **VDF group agreement is bound only implicitly, so D-39's fix scoped to `T` would leave half the problem open.** The accept path explicitly rejects `proof.T != pinned difficulty`, but there is **no** comparison of the group/modulus — agreement is enforced only as a side effect of `vdf.Verify` re-deriving the challenge. Two nodes on different *sourced* moduli both pass `enforceModulusChainPolicy` and then reject each other's proofs with no error naming the group as the cause. Fix D-39 and D-54 together: bind group name + modulus digest + `T` into one fleet-checked identity | — | M | `Fixed (feat/vdf-fleet-identity, landed WITH D-39)` — group name + modulus digest + `T` are bound into one `messaging.VDFIdentityDigest`. The adoption path now compares it explicitly and rejects with `ErrVDFIdentityMismatch` **naming both sides** (closing "no error naming the group"), and it is folded into the M2b `ConsensusHash` preimage so a divergent-group node forks rather than merely warning. Tests as for D-39. Same coordinated-restart cutover | |
| **D-55** | 4 | `jmdn` | **`ByzantineQuorum(n<1)` returns 1 while `avc Threshold` returns 0** — a real arithmetic disagreement between two consensus implementations, previously parked in §7.1 under a heading that says "do not fix these", where it could not be assigned or tracked. Both are guarded upstream today; this row exists so the divergence is owned if either guard is ever removed | — | S | `Open` |
| **D-56** | 3 | `jmdn` | **`CommitteeEpochBlocks` 0 → 20 takes a consensus-fork risk for a benefit that was then withdrawn — and silently switches on checkpoint signing.** PR #129's bc4a5d4 raised the default *in order to* enable `RequirePinnedCommittee`; 8311504 then reverted the pinning (the epoch-0 sentinel collision — `EpochForHeight(h)=h/20` is 0 for heights 0-19, and seedNodes `pkg/peer/gorm_jmns_service.go:148` reads `epoch == 0` as "serve the current epoch", so every pinned read failed the exact-match check and block 1 could never seat a committee) while KEEPING the 20. Net: the fleet carries a consensus-affecting default change — nodes with different values seat different committees at the same height and reject each other's blocks, and nothing overrides it from YAML or env, so the compiled default IS the parameter — with none of the pinning it was for. **Second, undocumented effect:** `Checkpoint.CadenceBlocks` is `0`, which routes `checkpointCadenceFires` (`messaging/checkpoint_sign.go:269-277`) to the epoch-boundary branch. At epoch length 0 that fired **only at genesis**; at 20 it fires **every 20 blocks**. Checkpoint signing is switched on as a side effect of an unrelated constant, mentioned in neither commit | — | S | **`DECIDED 2026-09-15 — KEEP 20, KEEP CadenceBlocks 0. Ship as-is, no code change.`** Grounds: (1) no fork risk at current scale — `avc committee/select.go CommitteeFor` returns all members when `k >= len(members)` and `MaxValidators` is 7, so epoch length changes nothing seated until the pool exceeds 7; (2) the coordinated restart is already mandatory for the consensus-hash freeze (8872912), so this rides along free; (3) 20 is a precondition for `RequirePinnedCommittee`. **The checkpoint concern in this row is RETRACTED** — `Checkpoint.Enabled` defaults false (`defaults.go:293`), and per-epoch is what `cadence_blocks: 0` is specified to mean. Pinned by `TestEpochIsDerivedFromTheBlockNotTheClock` (e8abb82), which asserts the literal 20. **▶ RESIDUAL — OPEN, and the real item:** the **epoch-0 sentinel collision** (8311504) blocks `RequirePinnedCommittee` going true. `EpochForHeight(h)=h/20` is 0 for heights 0-19, and seedNodes `pkg/peer/gorm_jmns_service.go:148` reads `epoch == 0` as "serve current", so every pinned read fails exact-match and block 1 cannot seat a committee. Fix: a jmdn genesis carve-out for `SelectionPeriod` 0, **or** move the seedNodes sentinel off 0 — cleaner, and cheap while nothing has been committed under the pinned scheme |
| **D-57** | 3 | `jmdn` | **The D-31-tagged fix erases equivocation evidence.** `1624583` closes a real TOCTOU (a merge can re-create a key the compactor just deleted) by re-checking the watermark after the writes and, if it moved, calling `CRDTLayer.Delete(key)` — **the whole LWWSet, not just the elements this merge added** (`CRDTSyncHandler.go:860`). If that key already held a peer's genuine conflicting votes, the proof is destroyed before `ConvergeAndCompact`'s C5 pass evaluates it — the exact ordering `avc crdt/votes/converge.go` says the two steps were fused to prevent, and `CompactVotesBelowHeight`'s own doc warns against. `ReportEquivocation` never fires, the reputation event and metric are lost, and the operator log reports the deletion as a *successful defence*. The delete is unconditional on the watermark condition, so it does not require that anything merged: a peer can send `{"adds":{}}` and still trigger it, retrying near each watermark advance to make honest nodes erase proof of a victim's equivocation | — | S | `Open — GATED, not live. Unreachable at the default: VoteCRDTDualWrite = envOn("JMDN_VOTE_CRDT_V2", false) and compactConvergedVotes returns early when off, so the watermark never leaves 0 and the re-check never fires for height >= 1. BLOCKER before that flag is turned on — do NOT flip JMDN_VOTE_CRDT_V2 until this is closed. RECOMMENDED FIX (2026-09-15), in preference order: (1) DROP THE DELETE ENTIRELY. It is not load-bearing. CompactVotesBelowHeight collects the key on the next sweep regardless, and by construction that runs AFTER C5 evaluates the evidence — which is the ordering converge.go was fused to guarantee. The TOCTOU the delete was added to close is already covered: Watermark.Set is monotonic (CAS, refuses regression), every deletion in ConvergeAndCompact is preceded in program order by the Set that authorises it, and MemStore.Delete/AppendOp share one mutex — so a merge cannot resurrect a key past the sweep. The re-check earns a log line, not a delete. (2) If a delete is kept for hygiene, scope it to what THIS call added — LWWRemove the specific elements merged in the loop above — never the whole object. Either way add a two-goroutine regression test: pre-seed a peer''s conflicting votes at height H, advance the watermark between the merge write and the re-check, and assert ReportEquivocation STILL fires. Note the current code also deletes when merged == 0, so an empty {"adds":{}} from any peer triggers it — the test should cover that input too` |

| **D-58** | 3 | `jmdn` | **Epoch-entropy persistence failures are silently discarded at both write sites.** `PersistEpochEntropy` (`messaging/entropy_persist.go:37`) is the durability half of D-33's remainder-1 fix, and **both** its call sites drop the error: `Sequencer/vdf_sealer.go:123` `_ = messaging.PersistEpochEntropy(forEpoch)` (after a successful seal) and `messaging/entropy_vdf_accept.go:199` `_ = PersistEpochEntropy(declaredEpoch)` (after accepting a peer's proof). A node that seals or adopts an epoch, fails to write it, and restarts has **no record it ever held that epoch and no signal it lost one** — `RehydrateBeaconFromDisk` simply restores fewer epochs than it should, reporting success | — | S | `Open — found rev 6 (2026-09-15) while re-verifying D-33's remainder 1.` **Severity is LIVENESS, not divergence — and only because D-25 is now fixed.** Post-D-25 the missing epoch makes `SeedSourceFor` return `ErrBeaconEpochUnavailable`, so the node refuses to select and halts loudly instead of seating a different committee; before D-25's fix this same loss was a silent fork. **Fix:** log at ERROR with the epoch and, on the sealer path, treat a persist failure as a seal failure — the entropy is worthless to this node after a restart if it was never written. Cheap: two call sites. **Verify:** force the write to fail, restart, assert the node names the lost epoch rather than reporting a clean rehydrate. Related, not duplicate: **D-47** (the restore *window* is a compile-time constant that `JMDN_AVC_BEACON_RETAIN_EPOCHS` does not move) and **D-45** (the newest-pointer RMW that can strand a written epoch) |

| **D-59** | 3 | `jmdn` | **The D-39/D-54 identity check has no promotion path — it is opt-in permanently.** PR #135 binds group ‖ modulus ‖ T into one identity and checks it three ways, but the adoption-path check (`entropy_vdf_accept.go` CHECK 0) fires only when **both** sides carry a non-empty value: `if local != "" && block.VdfParamsDigest != "" && …`. The leniency is deliberate and pinned by `TestVerifyAndAcceptVDFProof_IdentityGateIsAdditive` so the rollout can be additive — but **nothing ever makes it mandatory.** `ValidateProductionConsensusPosture` does not require a non-empty local identity, and `checkConsensusBinding` independently skips a zero `ConsensusHash`, so a block with both empty passes both layers and falls through to `vdf.Verify` — the pre-fix nameless failure D-54 was written about. **Not a break of the security property** (a wrong-parameter proof still fails `vdf.Verify`, and the install-time `T` refusal and hash-coverage are unconditional); the loss is *detection and attribution* during and after rollout | — | S | `Open — found reviewing PR #135, 2026-09-16.` **This is RC-2's eighth instance** — a two-state gate with no observe rung and no promotion step — in a register where RC-2 is already a named root-cause pattern. **Fix:** once the fleet is uniformly upgraded, add the local VDF identity to `ValidateProductionConsensusPosture` (a Stage-2 production node with an empty identity refuses to boot) and log at ERROR when a boundary block arrives with an empty `VdfParamsDigest` while the local identity is set — that log line is the observe rung, and it tells the operator exactly which peers are un-upgraded. **Verify:** a production-posture node with Stage 2 installed and no identity refuses to start; a boundary block with an empty digest is logged, named, and counted |

**Devnet items** (§6) are tracked as a group rather than individually numbered: 3 × SEV-3, 6 × SEV-4, all in `jmdt-devnet`.

> **D-38 … D-50 — RESTORED 2026-09-11, and re-verified before restoring.** These
> came from the full line-by-line audit of PR #125 (five parallel readers, **61
> files, +9,335/−137** — measured `git diff --shortstat 07301d3 6eb0cc7`; an
> earlier revision of this note said 57/+8,633/−131, which matches no revision
> of that PR). Their working document was deleted in a docs cleanup before they
> were entered here, so for three days they existed nowhere. All **thirteen**
> underlying findings were **re-checked against `v3base@291d44c`** at restore
> time rather than taken on trust — three days, a fleet-wide dependency upgrade
> and PRs **#127, #128, #130 and #131** had landed in between (**#129 is still
> open** at head `7baa3c22`). **Every one is still live.**
>
> Two I initially read as fixed were my own measurement errors, corrected by
> looking at the code: `Start` does not guard on `s.cancelled` (the grep matched
> a comment), and the `M2bHashEnabled` boot gate is still at `main.go:963` (an
> over-broad filter hid it). Both are recorded above as `Open`.
>
> Not in this ID space, and tracked separately: **`API-10`** from
> `THEBE-AUDIT-HLD.md` is a live CRITICAL — `gETH/Facade/rpc/http_server.go`
> runs batch JSON-RPC in goroutines with **zero `recover()`**, so a
> peer-triggered panic on any handler kills the process. Re-verified still
> unfixed on 2026-09-11. That register uses `SEC/CON/STO/EVM/SYN/NET/API/PRC/DEP`
> IDs; do not renumber it into `D-N`.

> **D-24 / D-32 — §0.2 satisfied OUT OF ORDER, 2026-09-07.** Both are `avc`
> defects, and §0.2 requires the code change, the PoC inversion and this
> register row in one atomic commit. That did not happen, and the reason is
> worth recording rather than tidying away: this register file did not yet exist
> on the branch the fixes were reviewed from (it reached `v3base` via #123/#124
> after `feat/consensus-audit` branched), so the reviewer concluded the
> cross-reference in `avc/tests/audit/audit_poc_test.go` was dangling and
> replaced it with a self-contained STATUS block. It was not dangling — this
> file was simply nine commits ahead. Sequence as it actually happened:
>
> | | landed | carried by |
> |---|---|---|
> | D-24 code + `randao/accumulator_race_test.go` | avc `498241b` | `v0.1.0-v3base.3` |
> | D-32 code + `crdt/tiebreak_determinism_test.go` | avc `193ab86` | `v0.1.0-v3base.3` |
> | PoC 4 / PoC 5 inverted (§0.2 item 2) | avc `v3base.4` | `v0.1.0-v3base.4` |
> | these two rows (§0.2 item 3) | this PR | — |
>
> Note `498241b` is labelled `wip:` but is the commit that actually fixes D-24;
> its race-safety change and regression test landed together. Verified by
> execution: `go test -race ./randao/` and the full avc suite are green at
> `v0.1.0-v3base.4`, which is the version this repo now pins in `go.mod`.
>
> The inverted PoCs also caused a transient `v3base` breakage worth knowing
> about: `avc/tests/audit` carries no build tag, so when PoC 4/5 arrived (avc
> #4) and the D-32 fix arrived (avc #5), they met for the first time on `v3base`
> and `go test ./...` went red there — neither PR's own CI had both. Fixed by
> inverting them. **Lesson for the remaining rows: land the inversion in the
> same PR as the fix, exactly as §0.2 says.**

---

## 3. SEV-1 evidence

### D-24 — `randao.Accumulator` is unsynchronised; the block-apply path is not serialised

**Repo:** `avc` · **Status: FIXED** (`498241b`, shipped in `v0.1.0-v3base.3`; jmdn pins `.5`) · **Regression test:** `randao/accumulator_race_test.go` — untagged, must pass clean

**Was:** `randao.Accumulator` had zero synchronisation (`grep -c "sync\." → 0`) while jmdn's apply lock is keyed **per block hash**, so two different blocks fold concurrently from both commit hooks (`broadcast.go:827`, `blockPropagation.go:401`). `Fold` mutated `a.mix[i] ^= c[i]` (silent entropy divergence) and `a.folded[proposerID] = height` (`fatal error: concurrent map writes` — unrecoverable, `recover()` cannot catch it). The precondition "the block-application path is already serialised" was prose, and false.

**Now:** `accumulator.go:140` carries `mu sync.Mutex`, taken by `Fold`/`Count`/`Complete`/`Missing`/`Finalise`; the doc comment at `:108` asserts **SAFE FOR CONCURRENT USE**. Re-verified rev 6.

⚠ **Do not "complete" this fix by locking `Expected`** — it is deliberately lock-free (`expected` is immutable after construction) and says so.

> **▶ RESIDUAL — STILL OPEN as of rev 6 (2026-09-15).** D-24's "done when" included correcting a
> second comment, and that was never done. **`avc/beacon/beacon.go:47` still reads "It is NOT safe
> for concurrent use"** — but `Pipeline` *is* safe: every `VDFSealer` shares one instance across
> concurrent seal goroutines, and `Seal` only reads `group`/`difficulty` then calls the
> mutex-protected `sink.Publish`. **The comment is wrong in the dangerous direction** — it will send
> the next reviewer either to add a needless lock or to serialise sealing and break the design.
> One-line fix; it is an instance of RC-3, which is why it is kept rather than folded away.

**Root cause (retained — RC-3 is still the dominant pattern here, see D-50).** A cross-repo precondition expressed only as a doc comment: avc cannot enforce "the caller serialises me", jmdn's authors had no compile-time or test-time signal, and `go test -race` over the integrated path had never run — avc has no CI and jmdn's suite does not exercise the fold concurrently.

**Still wanted (not blocking):** a race-enabled test applying two blocks concurrently through **both** commit hooks — that single test would also cover D-31 and D-30's race.

---

### D-25 — `SeedSourceFor` silently degrades committee entropy to the Stage-1 salt

**Repo:** `jmdn` · **Status: FIXED** (PR #125 `6eb0cc7`) — **flipped rev 6; this row read `Open` for twelve days while the code was closed**

**Was:** `SeedSourceFor` returned `committee.SaltSource{Salt: stage1Salt()}` whenever the beacon lacked the epoch — a silent fallback, against avc's imperative instruction (`committee/beacon.go:58`: *"Callers **MUST** fail closed on it. Falling back to a default seed would let two nodes — one with the entropy, one without — seat different committees, which is worse than refusing the block."*). Two nodes then seated different committees for the same epoch, so `n` and the threshold differed and one finalised a certificate the other rejected as unauthorised. Because the decision was taken **per lookup**, a node merely slow to receive a proof diverged for that epoch and silently re-converged — the hardest possible version to find in logs. It was also the amplifier for D-24's mix race, D-27's eviction, and any post-restart entropy loss: each turned into a silent divergence instead of a stall. **RC-1: fail-closed contract, fail-open caller.**

**Now (verified rev 6, all paths):** the three states are separated at `committee_v2.go:557` — no beacon → `SaltSource` (Stage 1, uniform fleet-wide, safe); has epoch → `BeaconSource`; **installed but epoch missing → `nil, ErrBeaconEpochUnavailable`** (`:574`, with an operator-directed log). **Exactly one `SaltSource{}` construction exists in the whole non-test tree** (`:563`, the safe branch); the **sole caller propagates** (`:306`); `SelectEntropyCommittee` independently fails closed (`:131`, `:136`).

> **▶ How this stayed `Open` for twelve days — and it is a pattern, not an oversight.** PoC 1 lives
> in `avc`, which cannot import `jmdn`. It demonstrates the *consequence* (two seeds seat two
> committees) and **can never observe a jmdn-side fix**. Its header still reads *"still reproduces
> (unfixed)"* and cites `committee_v2.go:441` — a line that no longer exists. Identical to D-27
> (PoC 2/3) and D-32. **Three of five PoCs now sit on the wrong side of the repo boundary from the
> code they gate.** See §0.5.

> **▶ RESIDUAL — defence-in-depth items not taken.** The code-level fix landed; the rest did not:
> - **No `consensus.entropy_source: salt|beacon` config**, read once at startup. The remaining
>   `beacon == nil` branch still selects the salt from *runtime state* rather than declared intent.
>   Uniform-wrong is safe today only because the beacon is installed from network-wide operator
>   config; nothing enforces that.
> - **Entropy source is still absent from `ValidateProductionConsensusPosture`** (verified: it gates
>   exactly `RejectLegacyVotes`, `EnforceCommitteeRegistry`, `EnforceBodyBinding`). A mainnet node
>   can still boot on Stage-1 salt without refusing. *(Same shape as D-38 limb (b).)*
> - **No `consensus_entropy_source{source=...}` gauge.** One metric would make this entire
>   divergence class observable — for every future gate too.

---

### D-26 — Unauthenticated vote ingest, and the requester chooses what gets signed

**Repo:** `jmdn` · **PoC:** `avc crdt/votes/write_identity_binding_test.go` (added with the avc-side half; the jmdn halves still need a running node) · **Live today** · **Effort:** L, splittable

> **PARTIAL, rev 5 — and the partial closes nothing in jmdn.** avc `3eee4b0`
> (first carried by `v0.1.0-v3base.5`) makes `AddVote` **reject** a record whose
> payload `rec.PeerID` differs from the libp2p-authenticated `nodeID`. It does
> **not** re-key: both CRDT elements are still built from `rec.PeerID`, and
> avc's own comment says so. Three reasons it is inert here today: (1) the sole
> non-test caller, `Vote/Trigger.go`, sets `rec.PeerID` from the same identity
> it passes as `nodeID`, so they can never differ; (2) that whole path sits
> behind `JMDN_VOTE_CRDT_V2`, **default off**; (3) avc explicitly **exempts the
> merge path**, which is where a relayed element's declared author is trusted.
> D-26(a)'s actual defect site — legacy pubsub ingest keying the CRDT on
> `msg.Data.Sender` in `AVC/.../Service/subscriptionService.go` — does not call
> `AddVote` at all and is untouched. **Do not close D-26 on the avc commit.**

Four weaknesses on one path. **(a)** and **(d)** are small and close most of the exposure — consider them as a first PR.

**(a) The vote CRDT trusts a self-declared sender.**
`AVC/BuddyNodes/MessagePassing/Service/subscriptionService.go:292` keys the CRDT on `msg.Data.Sender` — a field inside the JSON payload, chosen by whoever wrote it. The libp2p-authenticated identity is `msg.Sender`, set from `msg.GetFrom()` at `Pubsub/Subscription/SubscriberHelper.go:293`, and is used only for logging. Across the whole repo exactly **one** conditional touches `Data.Sender`, and it is a self-check (`== listenerNode.PeerID`), not authentication. `config/PubSubMessages/Pubsub.go`'s `Vote` struct carries **no signature field**.

The direct-stream sibling does this correctly at `ListenerHandler.go:1020` (`if message.Sender != s.Conn().RemotePeer()`), so the pattern is understood — the pubsub path simply never got it.

**(b) No membership filter at aggregation.**
A buddy cannot read peer weights (the seed enforces sequencer-only auth on that read), so `weights == nil` and `Structs/Utils.go:529` sets `weight := 1.0; exists := true` for **every** peer id present. `Utils.go:560` then runs `voteaggregation.VoteAggregation` — a weighted simple majority. `AVC/VoteModule/` has 1 source file and **0 test files**.

**(c) The requester picks the signing target.**
`ListenerHandler.go:1576` parses `block_hash`, `block_number` and `consensus_hash` from the request payload; `:1703` passes them straight to `SignMessageForBlock`. Every line between the two was read — there is no `GetBlock`/`BlockByHash` lookup.

**(d) The guard is off, fails open when on, and is not in the posture check.**
`consensus_vote_authz.go:20` — `enforceVoteRequesterAuth = os.Getenv(...) == "1"`, default **false** → `voteRequesterAuthorized` returns true immediately. `SetAuthorizedRequesterSource` (`:83`) has **zero production callers**, so even when enabled the authoritative path is skipped and the legacy fallback at `:162` fail-opens on an empty set (`if len(set) == 0 { return true }`). `production_posture.go:34` does not gate it.

**Impact — and its bound.** Injected votes are counted, a majority computed over them, and each honest buddy BLS-signs *that* result at `ListenerHandler.go:1703`. The resulting certificate is genuinely valid at a correct threshold for a block no committee member validated.

**The bound is real and worth understanding before triage.** `VerifyCertificate` (`messaging/consensus_hardening.go:458`) takes `n` from `authenticatedCommittee()` and requires each counted vote to carry a verifying signature from a committee member, de-duplicated by peer_id **and** bls_pub. So it authenticates *who signed and how many* — it cannot inspect how a signer reached its decision. The attack corrupts the signed **value**, not the tally: an attacker needs pubsub topic reach and cannot mint committee seats. That is the line between SEV-1 and catastrophic.

**Root cause.** RC-1 at (b) and (d); a trust-boundary placement error at (a) — authentication was treated as a per-handler concern rather than a property enforced once at the boundary.

**Fix — code level.**
1. **(a)** Reject when `msg.Data.Sender != msg.Sender`; key the CRDT on `msg.Sender`. Mirror the wording at `ListenerHandler.go:1020` so the two read alike.
2. **(b)** When `weights == nil`, fall back to equal weight *over the authenticated committee*, resolved from `authenticatedCommittee()` — which the verifier already trusts.
3. **(c)** Look the block up locally by `targetBlockHash` and sign only *its* `BlockNumber` and recomputed `ConsensusHash`. Refuse if the block is unknown. The caller may say **which** block; it must not say what that block's height or digest is.
4. **(d)** Default `enforceVoteRequesterAuth` on, wire `SetAuthorizedRequesterSource` at startup, remove the empty-set fail-open, add the flag to `production_posture.go`.

**Fix — design level.**
- **Move authentication to the boundary.** Have the subscriber layer stamp the authenticated peer id onto every decoded message and make the payload's own sender field unreadable by handlers — delete it from the wire type, or rename it so any remaining use is a compile error. One enforcement point instead of a per-handler convention is the only version of this that stays fixed.
- **Sign votes, don't just aggregate them.** The legacy `Vote` has no signature, so its authenticity rests entirely on transport. Either require a per-vote signature on the legacy keyspace, or finish the v2 cutover (`JMDN_VOTE_CRDT_V2`), which already carries per-vote BLS signatures and the corrected unweighted `MajorityDecision`. Finishing the cutover retires (a) and (b) together.

**Done when.** A vote whose payload sender differs from its transport sender is rejected and not stored (test); a signature request for a `(hash, height)` pair matching no local block is refused (test); with `weights == nil` a non-committee peer is excluded from the tally (test); a mainnet node refuses to boot with requester-auth off; `AVC/VoteModule` has table tests for `VoteAggregation` and `MajorityDecision` covering ties and empty input.

---

## 4. SEV-2 evidence

### D-27 — Bootstrap epochs silently evicted when the pinned list exceeds `retain`

**Repo:** `jmdn` · **PoC:** 2, 3 · **Blocks:** enabling the beacon · **Introduced by D-33**

**Was:** `BeaconSource.Publish` calls `evictLocked` on **every** insert, dropping epochs below `newest - retain`; `retain` defaults to `MinRetainedEpochs` = 3 and nothing coupled it to `len(EntropyBootstrap.Epochs)`. Pinning 6 epochs lost `[0 1]`; pinning 10 lost `[0…5]`. Worse, `bootstrapEpochs` (behind `IsBootstrapEpoch`) is a **separate, never-evicted map**, so an evicted epoch kept suppressing both the seal and the boundary proof while `beacon.Has(e)` was false — **a permanent dead zone that reported itself healthy**. RC-5: two collections, one concept, different lifetimes.

**Now:** `Fixed` — PR #129 `7e6a798` + `f121048`. Re-verified rev 6: `ValidateBootstrapFitsRetention` rejects at `span > retain`, with `TestValidateBootstrapFitsRetention_Boundary`.

> **▶ The fix took a different route than this section proposed, and that changes what "done" means.**
> The proposal was to **auto-size** retention from config. What shipped instead **refuses to start**
> when the pinned span exceeds retention — fail-closed rather than self-correcting. That is a
> legitimate and arguably safer choice, but note two consequences:
>
> - **PoC 2 and PoC 3 are NOT inverted, and must not be.** They construct
>   `committee.NewBeaconSource(retain)` and publish **directly against avc**, whose eviction
>   behaviour is unchanged — so they still reproduce, correctly. PoC 2's own failure message says
>   *"If retain is now derived from len(epochs), INVERT this"* — retain is **not** derived from
>   `len(epochs)`, so the condition for inverting was never met. **§0.2's invert-the-PoC rule does
>   not apply cleanly when the fix lands in a different repo from the PoC**; record the fix, leave
>   the PoC asserting the upstream contract it actually tests.
> - The original acceptance criterion — *"pinning 10 bootstrap epochs with default settings leaves
>   all 10 retrievable"* — is **still false by design**. A node so configured now refuses to boot.
>   Operators must size `retain` themselves; the guard tells them when they have not.
>
> **Residual (minor, open):** the two-maps root cause is untouched. `IsBootstrapEpoch` still reads
> the separate set rather than deriving from config, so the RC-5 shape remains for the next caller.

---

### D-28 — Block hash binds neither parent nor height, including under M2b

**Repo:** `jmdn` · **PoC:** none (needs a two-node harness) · **Live on devnet**

The legacy hash is Keccak256 over transaction content hashes only, and the zero hash for an empty block (`Security/Security.go:902`). Devnet runs `JMDN_M2B_HASH=1`, so the six-field preimage was read in full (`Security/consensus_fields_hash.go:55-82`):

```
domain ‖ Slot ‖ Period ‖ reveals ‖ VdfProof ‖ SeedEpoch ‖ VotingSnapshotEpoch
       ‖ PrevAggCert ‖ CommitteeSnapshotHash ‖ txContentConcat
```

`grep -c "PrevHash\|BlockNumber" Security/consensus_fields_hash.go` → **0**.

On devnet every field that could disambiguate two same-transaction blocks is empty or zero: reveals, `VdfProof` and `SeedEpoch` because the beacon is off; `CommitteeSnapshotHash` because `JMDN_COMMITTEE_SNAPSHOT_ANCHOR` is unset. So two blocks at one height with the same transactions hash identically, one committee certificate is valid for both, and `checkEquivocation` (`messaging/consensus_hardening.go:722`) compares that same colliding hash — so it does not fire either.

`Slot` cannot substitute: `messaging/slot_store.go:193` is `DefaultSlotStore.Current() + PeriodFor(height) + 1`, which reads mutable global state, so a verifier cannot independently recompute it.

**The fix already exists and is unwired.** `consensushash/blockhash_v3.go:39` binds chain, height, prevHash, stateRoot, txnsRoot and timestamp — with **0** consensus callers.

**Fix — code level.** Two lines into the `ConsensusHash` preimage:
```go
committee.WriteField(&buf, block.PrevHash.Bytes())
committee.WriteU64(&buf, block.BlockNumber)
```
v4 vote binding is already live end-to-end (signed `Sequencer/Consensus.go:1813`, verified `messaging/consensus_hardening.go:669-680` **before** the certificate check), so this is an additive field behind the existing `JMDN_M2B_HASH` gate, not a migration. Add a test constructing two blocks differing only in `PrevHash` and asserting their `ConsensusHash` values differ — that test is what keeps this fixed.

**Fix — design level.** Decide explicitly whether `BlockHash` is a **body digest** or a **block identity**, then make every call site agree. It is currently a body digest used as an identity — that is the actual defect, and it is why equivocation detection keyed on it cannot see same-body forks. If it stays a body digest, equivocation detection must key on the v3 identity hash instead.

**Done when.** Two blocks differing only in `PrevHash` produce different `ConsensusHash` (test); same for `BlockNumber`; `checkEquivocation` fires on a same-transaction different-parent fork at one height; existing M2b tests still pass and the rollout gate is unchanged.

---

### D-29 — Trapdoored testnet VDF modulus with no mechanical mainnet guard

**Repo:** `jmdn` · **Status: FIXED** (PR #125 / `6eb0cc7`) · **Regression tests:** `Sequencer/vdf_modulus_policy_test.go` — `TestModulusChainPolicyIsKeyedOnValueNotName`, `TestBuildVDFGroupRefusesRestrictedModulusUnderForeignNameWithOverride`

> **FIXED, but read D-35 — the first fix did not hold.** The original guard
> (`enforceNetworkPinChainPolicy`) keyed on the **group name**, and was reached
> only from the `lookupNetworkPin(groupName)` branch. Supplying the trapdoored
> modulus under any other name — `rsa-2048-frc`, a real avc registry entry whose
> digest is **empty** and whose published dimensions the devnet modulus matches
> exactly — missed that lookup, skipped the guard entirely, and installed on any
> chain via `JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS`. That is **D-35**. The guard
> is now **value-keyed**: `enforceModulusChainPolicy` computes the modulus digest
> and runs the chain policy on **every** path into `buildVDFGroup`, before any
> group is constructed. **A guard on the name protects the label; the trapdoor
> is in the number.**

**Was:** `vdf_network_pins.go` ships `rsa-2048-testnet-ephemeral`, whose own disclosure says *"INSECURE BY CONSTRUCTION … Whoever generated N can evaluate the VDF instantly and grind committee selection … Never ship this group name in a mainnet config."* Everything in that warning was correct; **nothing enforced it.** A mainnet-configured node installed the beacon cleanly, logged a warning, and ran. The asymmetry was backwards: the *unpinned* path demanded a loud opt-in env var, while the *pinned-but-trapdoored* path needed no override at all.

**Now:** `Fixed` — PR #125 (`enforceModulusChainPolicy`, value-keyed, see D-35) + PR #129 `543d307` (`ErrTrapdooredGroupInProduction` / `ErrUnpinnedModulusInProduction`, keyed on production posture) + `87636d7` (`main.go:1603-1607` `os.Exit(1)` instead of log-and-continue). Re-verified rev 6.

**Root cause — RC-3, its most consequential instance.** The codebase's own history (a fabricated "RSA-2048" value shipped once) is *why* pinning exists — and pinning then prevented the **wrong** number while permitting a **known-bad** one. Two lessons compound here: **a guard on the name protects the label; the trapdoor is in the number** (D-35), and a guard that returns an error to a caller that logs it is **fail-closed as a function, fail-open as a process** (RC-1, `87636d7`).

> **▶ RESIDUAL — the design-level fix was NOT taken.** Trust level is **still prose in a `Note`
> field** (`vdf_network_pins.go:34`: *"honest in Note about what the trust assumption is"*), not a
> first-class typed property. The shipped fix refuses at the *posture* layer instead, which closes
> the live exposure but leaves "we verified this is the right number" and "we know this number is
> unsafe" collapsed into one `ProvenanceRecord`. The original proposal — `sourced` / `ceremony` /
> `trapdoored`, gated on environment × trust level — remains the durable fix, and the "test asserts
> every pin carries an explicit trust level" criterion is **still unmet**. Low urgency, real debt.

---

## 5. SEV-3 evidence

### D-30 — Bloom dedup filter is lock-free and saturates into a block-ingestion halt

**Repo:** `jmdn` · **Status: FIXED** (PR #129 `e986ba6` + `76137cd`)

**Was — two defects in one object.** (1) *Race:* `messageFilter` was a `bits-and-blooms/bloom/v3` filter, not goroutine-safe, with `.Test`/`.Add` unlocked while reachable from the stream handlers, the pubsub goroutine, `admitZKBlock` and `broadcast.go` — and `peerTimeoutMutex` sat in the *same* `var` block, so this was an omission, not a single-threaded design. (2) *Saturation:* sized for 10,000 entries, never rotated, entries never removed. Recomputed: m = 95,851 bits, k = 7 → FP rate **1.00%** at 10k, **83.19%** at 50k, **99.53%** at 100k. Time to 50,000: **13.9 h** at 1 blk/s. A false positive made `HandleReceivedBlockMessage` discard a **valid** block *and* time out the honest sender for 20 s — fleet-wide, self-inflicted, arriving inside a day.

**Now:** both paths use an eagerly-initialised, bounded `hashicorp/golang-lru/v2` — exact, never a false positive. `ContractPropagation.go` keeps bloom by design (`contractFilterMu` guards it properly). Re-verified rev 6: residual `bloom` strings in the two fixed files are comments naming what was replaced.

**The design principle this established, and it generalises:** **never let a probabilistic filter decide that a block is a duplicate.** A false positive there rejects a valid block *and* punishes an honest peer. Probabilistic structures belong to message classes where a false positive is merely a dropped gossip.

> **▶ RESIDUAL — observability criterion unmet.** "Export cardinality and estimated FP rate for every
> bounded cache, and alert on threshold crossings" was part of this row's definition of done and was
> **not** implemented — `grep -i cardinality messaging/` returns one unrelated comment. The LRU is
> exact so the FP-rate half is now moot, but **cache occupancy is still unobservable**: nothing says
> how close the bounded cache runs to its limit, and eviction under load silently re-admits blocks.
> Low severity, but it is the metric that would have made the original saturation visible.

---

### D-31 — Epoch watermark TOCTOU duplicates finalisation; the duplicate seal blocks forever

**Repo:** `jmdn` · **PoC:** none · **Armed with D-24**

```go
// messaging/entropy_finalise.go:278-288
finaliseTrackMu.Lock()
toDecide := epochsWithClosedRevealWindow(block.Slot, lastDecidedEpoch, haveDecidedAny)
finaliseTrackMu.Unlock()                    // ← released
for _, e := range toDecide {
    decideEpoch(e, block)                   // ← runs unlocked
    finaliseTrackMu.Lock()
    lastDecidedEpoch = e                    // ← advanced after the work
    haveDecidedAny = true
    finaliseTrackMu.Unlock()
}
```

Two concurrent commit hooks (`broadcast.go:842`, `blockPropagation.go:411`) read the same stale watermark, compute the same `toDecide`, and both run `decideEpoch` — violating the "exactly once per epoch" invariant the file asserts at `:91`. The per-hash apply lock does not help, for the reason established in D-24.

Each duplicate reaches `sealer.Start(forEpoch, seed)` at `Sequencer/vdf_seal_wiring.go:109`, called **unconditionally** on a sealer that `sealerFor` may have returned from cache. `Start` (`Sequencer/vdf_sealer.go:54`) launches a goroutine ending in a send on a **capacity-1** channel (`:42`), so the second blocks forever — holding two RSA-modulus `big.Int`s and having burned a second ~20-minute VDF competing with the real one. `Result()` is only called by the epoch-boundary proposer, so on every other node the buffer is never drained.

The doc at `vdf_seal_wiring.go:103-106` claims epoch-keying makes Start-at-most-once true "across repeated/replayed `onEpochFinalised` calls". It dedups **construction**, not **Start**.

**Fix — code level.** Hold `finaliseTrackMu` across read → `decideEpoch` → advance; or, to keep the lock off the slow path, re-check the watermark inside the loop and skip epochs already decided. Make `Start` idempotent per instance with a `sync.Once`. Evict `vdfSealers` (`:70`) below the retention horizon. Correct the `:103-106` comment.

**Fix — design level.** A once-per-epoch state transition driven from a per-block hook needs an idempotency key. Persist the decided-epoch watermark and make `decideEpoch` a compare-and-swap against it — which also survives restarts, as the in-memory watermark does not.

**Done when.** Two goroutines driving `maybeFinaliseCompletedEpochs` with the same block slot fire `decideEpoch` once per epoch (test); calling `Start` twice on one sealer leaks no goroutine (`goleak` or a `NumGoroutine` delta); `vdfSealers` is bounded over a simulated 100-epoch run.

---

### D-32 — CRDT merge tie-break reads a Go map

**Repo:** `avc` · **Status: FIXED** (`193ab86`, in `v0.1.0-v3base.3`) · **PoC 4, 5 — now INVERTED** into regression tests (`b11b686`), plus `crdt/tiebreak_determinism_test.go`

**Was:** `extractNodeID` started `maxTS` at 0 and replaced only on **strictly** greater, so among entries tied at the maximum the winner was whichever key Go's randomised map iteration reached first. That fed `deterministicMerge`, reached whenever `Compare` returns 0 — which it does for **concurrent** clocks, the common gossip case. Measured: `distinct results over 400 byte-identical merges: 2 (233/167)`.

**Bound precisely (still true, see §7.2):** the *stronger* claim is false. `A.Merge(B) == B.Merge(A)`, and a membership-flip construction returned 600/600 identical. **The defect affected the clock and the serialised bytes, not the tally** — which still matters for digest-based reconciliation over CRDT state (`crdt/iblt`, `crdt/hashmap`).

**Now:** `Fixed` — tie-break is lexicographic on lowest node id, and the `nodeID1 == nodeID2` fallthrough is resolved deterministically too (`crdt.go:83-86` → `if nodeID1 < nodeID2 { return ts1 }; return ts2`). Re-verified rev 6. The duplicate `jmdn/crdt` package was **deleted** by `dc5b238` (11 files, 2,966 lines); **0 importers of `gossipnode/crdt` remain**. *(The `crdt/` directory still appears in a listing — it holds nothing but a stray untracked `.DS_Store`. Delete it.)*

> **The lesson this fix taught, and why §0.2 rule 2 exists.** PoCs 4/5 were written to PASS while the
> defect reproduced, so the fix made them **fail**. `avc/tests/audit` carries **no build tag**, so it
> runs on a plain `go test ./...`. The PoCs arrived on `v3base` via avc PR #4 and the fix via PR #5 —
> the two met for the first time *on `v3base`*, where neither PR's own CI had both, and the branch
> went red on merge. **Land the inversion in the same commit as the fix.**

> **▶ RESIDUAL — the design-level fix was not taken (unverified whether attempted).** "No `Remove` in
> the vote keyspace" is still **load-bearing and unenforced** — it is the only reason this was never a
> tally bug. Making it structural (an append-only vote-store interface with no `Remove` method) turns
> a fact someone has to keep knowing into a compile-time property. Not checked this pass whether the
> interface changed; treat as open until someone greps it.

---

### D-33 — Entropy genesis bootstrap (FIXED, with remainder)

**Repo:** `jmdn` · **Status:** `Fixed (b5e305a8) — remainder Open`

`Sequencer/beacon_bootstrap.go` closes the gap that made the beacon impossible to start, publishing a deterministic value per operator-pinned epoch at install time:

```
ENTROPY-E(bootstrap) = SHA256( domain ‖ u64:chainID ‖ field:authorityPin ‖ field:seed ‖ u64:E )
```

It binds to the pinned seed-authority key (two networks cannot share a schedule), takes the epoch set from config (every node agrees, rather than deriving it from when a node started), refuses to bootstrap without an authority pin, fails closed on partial publish, and exempts bootstrap epochs from sealing and the boundary-proof requirement — removing the "halt at every boundary" problem. It is honest that the values are *"public and computable by anyone in advance… grindable by construction"* and logs a standing security finding at install.

> **Remainder 1 — persistence: CLOSED, corrected rev 6.** This row claimed *"grep for
> persistence/hydrate hits returns 0"*. That is true of **`avc/committee/`** — `BeaconSource` is
> still `map[uint64][]byte` by design — but **jmdn persists it**: `PersistEpochEntropy`
> (`messaging/entropy_persist.go:37`) is called on seal (`Sequencer/vdf_sealer.go:123`) and on
> accepting a peer's proof (`entropy_vdf_accept.go:199`), and `RehydrateBeaconFromDisk` (`:79`) is
> wired at `beacon_install.go:315`. **Two caveats, both live:**
> - **Both persist call sites discard the error** (`_ = messaging.PersistEpochEntropy(...)`). A
>   persist failure is silent, which re-creates precisely the loss this remainder was about. **Since
>   D-25 is now fixed, the consequence is a loud halt (`ErrBeaconEpochUnavailable`) rather than a
>   silent salt fallback** — liveness, not divergence. Tracked as **D-58**.
> - The restore window is `committee.MinRetainedEpochs+1`, a **compile-time constant** that
>   `JMDN_AVC_BEACON_RETAIN_EPOCHS` does not move — that is **D-47**.

**Remainder 2 — no observe rung (OPEN).** Every feature gate here has two states: off, where behaviour silently differs, and on, fail-closed and hard. Add a third — seal, publish and log the entropy and the committee it *would* seat, while selection still uses the configured source. Promotion then requires N epochs of zero fleet-wide divergence: evidence instead of a leap. This is **RC-2**, and the template applies to `JMDN_COMMITTEE_V2`, `JMDN_AVC_AGG_CERT`, `JMDN_COMMITTEE_SNAPSHOT_ANCHOR`, `JMDN_M2B_HASH` and `JMDN_VOTE_CRDT_V2`.

**And it armed D-24.** Before this landed, `entropyAccumulatorFor` always failed and `Fold` never ran. See §0.3.

---

### D-34 — Unbounded state on the block-receive path

**Repo:** `jmdn` · **PoC:** none · **Live today** · Splittable into three

| Location | Defect | Growth |
|---|---|---|
| `messaging/consensus_hardening.go:690` | `seenHeights` — `grep -c "delete(seenHeights"` → **0**. A durable `EquivocationStore` already backs the same data at `:707`, so this is a cache with no eviction | 1 entry/block forever ≈ 8.6 MB/day @1 blk/s |
| `messaging/blockPropagation.go:187` | `updateMessageSet` reads, mutates and rewrites the entire message-set JSON per block | O(n)/block ⇒ O(n²) cumulative |
| `Pubsub/Subscription/SubscriptionManager.go` | An errored subscription leaves a live map entry with a dead reader; a later `Subscribe` takes the reuse path and registers a handler that is never invoked | silent permanent loss of that topic + a blocked monitor goroutine |
| `Sequencer/vdf_seal_wiring.go:70` | `vdfSealers` never evicted (also covered by D-31) | 1 entry/epoch |

**Fix — code level.** Prune `seenHeights` below finality depth (the durable store remains the source of truth for anything older); make `updateMessageSet` an append-only keyed write; in `SubscriptionManager`, delete the map entry on the error path and cancel the monitor goroutine.

**Fix — design level.** Adopt one repo-wide rule: **every map keyed by height, epoch, peer or tx hash declares its bound and its eviction trigger at the declaration site**, with cardinality exported by a periodic task. D-30 and all four rows here are the same missing convention, and they will keep recurring without it.

**Done when.** A 10,000-block simulation shows `seenHeights` bounded rather than linear; `updateMessageSet` cost per block is constant with respect to history length; an errored subscription can be re-subscribed and its handler fires; cardinality metrics exported.

---

## 6. Devnet items (`jmdt-devnet@fd52106`, branch `main`)

Tracked as a group. None blocks the code tracks; several are quick wins.

| SEV | Item | Evidence | Action |
|---|---|---|---|
| 3 | Committed JWT signing secret and explorer API key in a git-tracked file; the explorer key **disagrees** between yaml and `.env`, and yaml wins | `jmdn.gate.yaml:132-135`; `config/settings/security.go:176-189` | Rotate both, move to a secret store, delete the yaml field so env is the single source. An operator following `.env` currently gets 401 and therefore no block-generation path. |
| 3 | No memory/CPU limits, no log rotation, no restart policy on any of 16 containers | grep `deploy:\|mem_limit\|logging:\|restart:\|max-size` in `docker-compose.yml` → **0** | Unbounded `json-file` logs on 5 validators fill the host; the OOM killer picks by score, not by offender. Add limits, `max-size`/`max-file` and `restart: unless-stopped` to the templates in `scripts/gen_compose.sh`. |
| 3 | `JMDN_COMMITTEE_V2=true` but `max_validators: 7` ≥ pool of 5, so `k` clamps to the pool and the entire pool is seated every round | `messaging/committee_v2.go:289-294`; `jmdn.gate.rendered.yaml:106`; `NODE_COUNT=5` | The rotating draw the flag exists for never rotates. Raise `NODE_COUNT` to ≥8 or lower `max_validators` below the pool, and derive it in `gen_compose.sh` rather than hard-coding. |
| 4 | Mounted yaml documents the opposite of live behaviour (`fastsync.enabled: false` while env supplies `true`) | precedence resolved **by execution**: env wins for bools and durations | **Not a liveness bug** — this was verified, not assumed. Make the yaml match reality so the file a reviewer reads is the config that runs, and add a loader test pinning precedence (there is none for `JMDN_FASTSYNC_*`). |
| 4 | Sequencer role derived from a *sync* flag: `isSequencer := !cfg.FastSync.EnableCatchup` | `main.go:1988` (rev 6; was `:1891`, and `:1869` at audit base `84d0c54f` — this reference has now drifted twice, most recently by PR #129); per-node overrides in compose | Correct today (env wins). Still fragile — introduce an explicit `consensus.role` and a startup assertion that exactly one sequencer is registered at the seed. |
| 4 | `JMDN_NETWORK_CHAINID` is a dead variable — the derived name is `JMDN_NETWORK_CHAIN_ID` | confirmed by executing `loader.go`'s viper sequence: the misspelled var is ignored and the yaml value survives | Chain id is the BLS vote domain separator. Fix the name in `gen_compose.sh`, or remove it and document the yaml as authoritative. |
| 4 | Provenance stamp captured *before* replace injection | `dockerfiles/jmdn.gate.Dockerfile:46-77`, stamp at `:71` | The image reports the pinned tags while building from local sibling directories. Stamp after injection, or emit both and fail the build on divergence. |
| 4 | `RUN touch /opt/jmdn/.bootstrapped` baked into the image on top of the bind-mounted sentinel | `dockerfiles/jmdn.gate.Dockerfile:105` | Makes bootstrap-skip invisible and non-overridable for any deployment from this image. Remove the Dockerfile line; the mount is already visible in compose. |
| 4 | No CI on `avc`, `ThebeDB`, `jmdt-devnet` | empty or absent `.github/workflows` | `avc` is the consensus module and its suite is already green and race-clean — this is a one-file, immediate ratchet, and it is what would have caught D-24. |

---

## 7. What is sound, what was withdrawn, and why these defects happened

### 7.1 Verified sound — do not "fix" these

Four things were attacked and could not be broken. Two are places where an earlier pass's hypothesis turned out to be deliberate, correct design — worth knowing before someone "simplifies" them.

> **⚠ READ THIS BEFORE TRUSTING THIS SECTION — added rev 5, 2026-09-11.** A
> "verified sound" heading is the strongest discouragement this document can
> give, and one of the four bullets below **masked a SEV-1 for five days**.
> Bullet 3 asserted that the Byzantine denominator "comes from the
> authenticated committee, never from votes received", citing
> `VerifyCertificate`. That was true *there* and false in the entropy fold:
> `verifyCertAndAggregate` sized `aggCertQuorum` from a **block_buddy-filtered**
> pool, so one operator's local blocklist moved the threshold and two honest
> nodes seated different committees. That is **D-36**, now `Fixed` via
> `fleetCommitteeSnapshotFor`.
>
> The lesson is about the section, not the bullet: **a property verified at one
> call site is not a property of the codebase.** When re-verifying anything
> here, enumerate every caller. §7.3 RC-3 is the same pattern one level up.

- **The distinct `EntropyEpoch` type works.** It was hypothesised that the beacon is stored under slot epochs and looked up with block epochs, which would make `Has()` permanently false — and, at the time, the D-25 salt fallback permanent. *(D-25 is `Fixed` as of rev 6, so the same mismatch would now produce a permanent `ErrBeaconEpochUnavailable` halt instead of a silent fork — still fatal, but loud.)* It is not: `messaging/committee_v2.go:181` sets `committee.EntropyEpoch(EpochForSlot(b.Slot))` (cited as `:178` before rev 5 — the line drifted), and the named type (`avc/committee/seed.go`) exists precisely so a block-counted value cannot compile into that slot. `messaging/entropy_committee.go` then declines to reuse `committeeSnapshotFor` for the same reason. **This is RC-5's remedy already working at one junction — extend it to `SelectionPeriod` and the wall-clock epoch, which lack it.**
- **The one-epoch lag and ENTROPY-E indexing are correct.** `onEpochFinalised(closedEpoch)` seals for `closedEpoch + 1` (`Sequencer/vdf_seal_wiring.go:88`), matching `avc/beacon/beacon.go:92`'s requirement and the convention recorded at `messaging/entropy_committee.go:26-39` — which includes a written note of a previous off-by-one that was caught and fixed. Selection for epoch E cannot be seeded by epoch E's own reveals.
- **Quorum arithmetic, all four implementations — the FORMULA only.** Executed across n = 1…500: zero safety violations (`2q−n > f`), zero liveness violations (`q ≤ n−f`), zero disagreements between `avc/quorum`, `avc/bft`, `jmdn/AVC/BFT/bft` and `jmdn/messaging`. n=5→4, 7→5, 100→67, 101→68. Locked by `TestControl1`.
  **The formula was never the risk — the DENOMINATOR is.** `VerifyCertificate` takes it from the fleet-agreed authenticated committee and deliberately excludes the local blocklist (CON-12), so blocking can only make quorum *harder*. `verifyCertAndAggregate` did the opposite until D-36; it now uses `fleetCommitteeSnapshotFor`. **Any new quorum call site must be checked for its denominator, not its arithmetic.** Rev 5 re-verified: `ByzantineQuorum` is at `messaging/consensus_hardening.go:396` (cited as `:368-371` before rev 5 — that range is now `CommitteeKeyAuthorized`).
  *One latent divergence:* `jmdn ByzantineQuorum(n<1)` returns **1** while `avc Threshold` returns **0** (re-verified rev 6: `messaging/consensus_hardening.go:396` and `avc/quorum/quorum.go:40`, both still live). Both are guarded upstream; align them if either guard is ever removed. ~~This has no register row, so it cannot be assigned~~ — **it now does: `D-55`, created in rev 5 for exactly the reason this paragraph gave.** Track it there, not here.
- **The test suites are green and race-clean.** `WORKDIR2/AUDIT-TRACKER.md` (the prior six-repo audit — its findings were renumbered `D-1…D-23` → **`XR-1…XR-23`** on 2026-09-15) states that no tests have been run anywhere in any repo. **That is out of date.** avc's `quorum`, `committee`, `crdt`, `crdt/votes`, `beacon`, `randao` and `vdf` all pass under `-race`, as do ThebeDB's `pkg/kv` and `pkg/checkpoint`; jmdn's own suite has since been run too, against a recorded baseline. **Every defect in this document survives a green suite** — that is the more useful finding, and it is why §0.4 says not to trust a green `go test ./...`.
  *Coverage gaps:* ThebeDB's `internal/merkle`, `pkg/eventlog` and `pkg/eventlog/wal` have **no test files at all** — a Merkle tree and a write-ahead log. On the jmdn side, D-49 records three untested new packages and four untagged test seams shipping in the release binary.
  *jmdn baseline:* the full suite is **not** green on `v3base` and never has been. Ten tests fail identically on `v3base` and on any branch off it — `TestDrainBatch_*` (×4), `TestFullFlow_AdapterFeedsAllThreeStages`, `TestFix1_StartupJitter`, `TestGetBuddyNodes`, `TestStreamLeak`, `Test_GetBlocksRange`, `Test_GetMultipleAccounts`. Six of those fail **only** under `-race`. **Judge a branch against that baseline, not against zero** — otherwise every PR looks broken and real regressions hide in the noise.

### 7.2 Claims tested and withdrawn

Recorded so they are not re-litigated. Two are locked by negative PoCs (§0.5). **A withdrawal is not permanent** — see the D-36 note in §7.1; re-test a withdrawal if the code around it moves.

| Claim | How it was tested | Outcome |
|---|---|---|
| CRDT `LWWSet.Merge` is non-commutative | Wrote the test | **Withdrawn.** `A.Merge(B)` equals `B.Merge(A)`; the tie-break is order-stable when the extracted node ids differ. Locked by `TestNegative1`. |
| Merge nondeterminism flips vote-set membership | Targeted construction, 600 iterations | **Withdrawn.** 600/600 identical. Cannot happen while the vote keyspace has no `Remove`. Locked by `TestNegative2`. |
| Devnet stalls permanently at tip 0 (mounted yaml `fastsync.enabled: false` beating env `true`) | Rebuilt `loader.go`'s exact viper v1.21.0 sequence and executed it | **Withdrawn.** Env wins for bools and durations. Was rated Critical in an earlier pass; downgraded to SEV-4 documentation drift. |
| All five devnet nodes self-identify as sequencer | Same execution | **Withdrawn.** Env wins, so node-1's `false` and the validators' `true` both apply. Role assignment is correct today; the fragility remains as a SEV-4. |

### 7.3 Root-cause patterns

Thirty-six findings, six underlying causes. Each pattern has more than one instance, which is how it is known to be a pattern rather than a bug. **Fixing instances without fixing patterns will regenerate them — and RC-3 demonstrably did regenerate: it produced D-24, D-29 and D-32 in the first pass, then D-35 and D-36 in the PR #125 pass, in code written by people who had read this table.**

RC-2's instance list is also longer than it looks. Every one of these flags has exactly two states, off-and-silently-different or on-and-hard, with no observe rung: `COMMITTEE_V2`, `AGG_CERT`, `SNAPSHOT_ANCHOR`, `M2B_HASH`, **`UNSIGNED_VALIDATOR_VOTES`**, and **`VOTE_CRDT_V2`** — that last one gates the *entire* avc v2 vote keyspace, including D-26's identity guard, so every vote hardening avc ships is inert until it flips.

> **Rev 6 update — `CONSENSUS_HASH_V3` left this list the right way.** RC-2 predicted that PR #129's
> answer would be to flip the default, which is the failure mode this pattern describes. That did
> happen first (ffdeaf7), and was then **superseded by deleting the flag outright** (8872912): one
> unconditional format, no second state to diverge in. **Deleting the ladder is a legitimate way off
> it** — available precisely because this flag guarded a *format*, where a coordinated restart is
> mandatory anyway. It is not available to the flags above, which guard *behaviour* a fleet must be
> able to roll forward one node at a time. Those still need the observe rung.

| # | Pattern | Instances | Structural remedy |
|---|---|---|---|
| RC-1 | **Fail-closed contract, fail-open caller.** avc packages fail closed and say so imperatively; jmdn's callers were written to preserve liveness. At every seam, liveness silently won. **Its two worst instances are now fixed** — D-25 (`SeedSourceFor`, PR #125) and D-29's process-level half (`main.go` logged a fatal guard's error and continued; `87636d7` makes it `os.Exit(1)`). The pattern still has open instances. | ~~D-25~~✓ · D-26(b) · D-26(d) · ~~D-29 boot path~~✓ · **D-58** (persist errors discarded) | Propagate errors across the seam. A default that trades safety for liveness must be a named config value with a startup warning, never a fallthrough. |
| RC-2 | **No shadow rung on the rollout ladder.** Every gate has two states: off, where behaviour silently differs, and on, fail-closed and hard. | D-33 remainder · `COMMITTEE_V2` · `AGG_CERT` · `SNAPSHOT_ANCHOR` · `M2B_HASH` | Add an `observe` state: compute the new value, log it beside the old, export a divergence metric, keep acting on the old. Promotion becomes evidence-driven. |
| RC-3 | **Preconditions in prose, not in types or tests.** Critical invariants stated in comments that no build step checks. **This is the dominant pattern in the register and the direct cause of both SEV-1s found in the PR #125 pass.** | D-24 ("already serialised" — false) · D-31 ("Start at most once" — false) · D-29 ("never on mainnet") · D-32 ("no Remove") · **D-35 ("the override never waives a network pin" — it did, for any name the pin table did not list)** · **D-36 ("a local blocklist can never shrink n" — the comment named `eligibleMembers` as the thing to avoid, then reached the same filter through `committeeSnapshotFor`)** · **§7.1 bullet 3 itself, which asserted the denominator property of one call site as a property of the codebase and thereby masked D-36** · 3 stale "NOT WIRED" comments · D-50 (the row that now tracks this class) | Where a precondition can be enforced, enforce it (a mutex, a distinct type, an unexported constructor). Where it cannot, write the test that fails when it is violated. **A comment is not a mechanism — and a comment that names the wrong mechanism to avoid is worse than none, because it tells the next reader not to check.** |
| RC-4 | **Recursive design with no base case.** Steady state was designed; epoch zero was not. | D-33 (now fixed) · `linkageDecision` rejects every block at `localTip == 0` | Every recursive protocol value needs a genesis provision decided alongside the recurrence, plus persistence so a restart is not a fresh base case. |
| RC-5 | **Two collections, one concept, different lifetimes.** | D-27 (`bootstrapEpochs` vs `entropy`) · D-34 (`seenHeights` vs `EquivocationStore`) | One source of truth. Where a cache mirrors a store, derive it or make the divergence impossible to represent. |
| RC-6 | **One initialisation primitive, two owners.** A `sync.Once` (or any once-only guard) shared by two independent init paths: whichever runs first consumes it and the second silently skips its entire body, leaving its state nil — with no error anywhere. Added rev 6 from 76137cd, which found `accountOnce` shared between the DID stream handler and `InitDIDPropagation`; a DID stream arriving first left `accountsClient` nil. | D-30's third defect (76137cd) | Give each init path its own `Once`. A `Once` is part of the thing it initialises, never shared across two things that merely run near each other. |

**Comments that assert the negation of the code**, to be fixed opportunistically. `Sequencer/vdf_sealer.go`, `messaging/entropy_reveal.go` and `messaging/entropy_committee.go` all claim there is no production caller for things `Sequencer/beacon_install.go` demonstrably calls — `messaging.SetBeaconSource` has had a live caller since before this audit, and it is gated by *environment configuration*, not by caller absence. `avc/randao/fallback_aggsig.go` still declares blocker B1 open; `messaging/entropy_aggsig.go` closed it. And avc's own PoC header still asserts that *this document* "does not exist in the jmdn repo — that handover was never committed", which was true when written and false since PR #123; it also still says the race probe "should be deleted" though `b11b686` deleted it.

**D-50 is the row that tracks this class, and it has an evidence gap.** D-50 claims six such comments; the enumeration died with the deleted PR #125 working document, and the lists in this section total at most five. **Re-derive the six from `6eb0cc7` before working that row** — the two that mattered are already named in RC-3 above (D-35's and D-36's).

---

## 8. Remediation order

Revised rev 5. Struck-through rows are `Fixed`; they are kept so the ordering argument stays legible.

```
GATE 1 — must land before the beacon is enabled        [rev 6: 3 of 5 now FIXED]
  D-39  jmdn  make T a chain parameter, not a per-host env var          M  ┐ land
  D-54  jmdn  bind group name + modulus digest + T as ONE identity      M  ┘ TOGETHER
  ~~D-24  avc   Accumulator mutex~~                        FIXED 498241b / v3base.3
  ~~D-25  jmdn  SeedSourceFor fails closed~~               FIXED PR #125 6eb0cc7  (rev 6)
  ~~D-27  jmdn  bootstrap vs retention~~                   FIXED PR #129 7e6a798+f121048

  Why D-39 and D-54 are one item: binding T alone still lets two nodes on
  different sourced moduli pass enforceModulusChainPolicy and then reject each
  other's proofs with nothing naming the group as the cause.

GATE 2 — safety, parallelisable, no dependency on Gate 1
  D-25  ── moved to Gate 1 and FIXED. Residual (defence-in-depth, still open):
           consensus.entropy_source config + posture gate + source metric   S
  D-26  jmdn  authenticate the LEGACY ingest boundary (subscriptionService,
              keys on msg.Data.Sender); stop signing caller input.
              NOTE: the avc-side guard landed and is inert here — it is
              behind VOTE_CRDT_V2 and its only caller cannot trip it     L
  D-28  jmdn  bind PrevHash + BlockNumber into ConsensusHash            M
  D-38  jmdn  add CONSENSUS_HASH_V3 + UNSIGNED_VALIDATOR_VOTES to
              ValidateProductionConsensusPosture and log both at boot.
              Do NOT resolve this by flipping a default (see §0.3)       S
  ~~D-29  jmdn  refuse trapdoored pins on mainnet~~        FIXED PR #125 (value-keyed, D-35)

GATE 3 — entropy enablement (needs Gate 1)
  ~~D-33a jmdn  persist BeaconSource, hydrate on boot~~    DONE jmdn-side:
          PersistEpochEntropy + RehydrateBeaconFromDisk (rev 6 correction —
          this was recorded as missing; it exists). BUT see D-58 below.
  D-58  jmdn  stop discarding PersistEpochEntropy's error at both write
              sites — durability that silently no-ops is not durability   S  ← NEW
  D-33b jmdn  observe rung: seal + log + divergence metric              M
  D-33c        enforce — only after N epochs of zero fleet divergence
  D-45  jmdn  make the newest-epoch pointer atomic, or replace it with
              kv.ScanPrefix — D-33a's durability is unreachable without
              a correct index                                           S

GATE 4 — independent, any time
  D-30  jmdn  exact bounded LRU — PR #129 does blockPropagation.go;
              DIDPropagation.go still carries the identical defect      M
  D-31  jmdn  watermark CAS + Start idempotency + evict vdfSealers      M
  D-34  jmdn  prune maps below finality depth                           M
  D-41  jmdn  claim (not delete) in resolvePendingFallbacks             S
  D-42  jmdn  next-epoch lookup instead of rebuilding the list O(M²)    S
  D-43  jmdn  canonicalise the proof before persisting it               S
  D-44  jmdn  target the epoch actually missing, or correct the docs    M
  D-46  jmdn  bound T, give Start a deadline, honour s.cancelled        S
  D-40  jmdn  delete M2bHashEnabled or make its boot gate meaningful    S
  D-47  jmdn  derive retention from the configured value; evict accs    S
  D-48  jmdn  feature-gate the vdf-proof protocol                       S
  D-49  jmdn  build-tag the four *ForTest seams; test the 3 new pkgs    S
  D-50  jmdn  re-derive the six false comments from 6eb0cc7, then fix   S
  ~~D-32  avc   total tie-break in extractNodeID~~         FIXED 193ab86 / v3base.3
        devnet secrets · limits · logs · CI on avc + ThebeDB           §6

GATE 5 — structural (RC remedies, after the instances)
  distinct types for the remaining two epoch clocks              (RC-5)
  append-only vote-store interface                               (RC-3/D-32)
  bounded-map convention at every declaration site               (RC-3/D-34)
  observe rung retrofitted to the SEVEN feature gates             (RC-2)
  denominator review at every quorum call site                    (RC-3/D-36)
  commit THEBE-AUDIT-HLD.md into jmdn/docs/audit/ — 54 source
  files cite its IDs and it is not in any repo; it also holds the
  unfixed CRITICAL API-10 (batch JSON-RPC, zero recover())
```

**Suggested first assignments — rewritten rev 6; every row this paragraph previously named is now `Fixed`.**

**`D-39` + `D-54` together, immediately** — they are the only thing gating entropy enablement. Do not treat them as separate tickets: binding difficulty `T` without binding the group leaves two nodes on different sourced moduli passing `enforceModulusChainPolicy` and then rejecting each other's proofs with nothing naming the group as the cause. One fleet-checked identity = group name + modulus digest + `T`. Neither is locally detectable by the node that is wrong, so no single-node test will catch either.

**`D-26` next, and it needs the most senior reviewer.** Split it; start with the legacy ingest path (a), where the defect actually lives — and note rev 6's sharpening: that path is **default-live and ungated**, `msg.Data.Sender` is an unauthenticated JSON field, and `RejectLegacyVotes` does not cover it despite the name.

**`D-38` limb (b)** is the cheapest remaining safety win and needs a decision, not much code: add `AllowUnsignedValidatorVotes` to `ValidateProductionConsensusPosture` and log its state at boot. *(Limb (a) resolved itself when 8872912 deleted the flag — this row's original recommendation, reached by a better route.)*

**`D-58`** is an easy, high-value starter for someone new: two call sites, stop discarding `PersistEpochEntropy`'s error. Durability that silently no-ops is not durability.

~~D-27~~, ~~D-30~~ and ~~D-25~~ were this paragraph's earlier suggestions and all now read `Fixed`.

---

## Appendix A — Repos touched, and what to commit

### A.1 Footprint of this audit

| Repo | Branch | Commit | Files added | `v3base` / `main` touched? |
|---|---|---|---|---|
| `jmdn` | `audit/2026-09-03-consensus` | `f490147` + this doc-fix commit | `docs/audit/AVC-CONSENSUS-HANDOVER.md` (this file) | **No** — PR only |
| `avc` | `audit/consensus-2026-09` | `e97ac50` | `tests/audit/audit_poc_test.go` · `randao/accumulator_race_test.go` | **No** — PR only |
| `ThebeDB` | none | none | none | **No** — untouched. (The `audit/2026-08-17-handover` branch there is pre-existing, from 2026-08-17.) |
| `jmdt-devnet` | none | none | none | **No** — untouched. Findings in §6 are read-only observations. |

Three files total. `jmdn/audit/2026-09-03-consensus` is rebased onto
`origin/v3base` = `dda7c4a9` and is exactly two commits ahead of it.
`avc/audit/consensus-2026-09` is one commit ahead of `1c13324`.

### A.2 Housekeeping — DONE, no action

The audit sandbox lacked unlink permission and left three inert paths behind.
**All three were cleared on 2026-09-04** — recorded here only so nobody hunts
for them:

- `avc/audit/` — an earlier revision of the PoC suite, superseded by
  `tests/audit/`. Deleted.
- `avc/.git/index.lock` and `jmdn/.git/index.lock` — stale 0-byte locks that
  blocked committing. Both removed.

`avc/randao/zz_race_probe_test.go` **was** the D-24 PoC and is now correctly gone: avc `b11b686` deleted it when it was promoted to the untagged `randao/accumulator_race_test.go`. Nothing in avc invokes a `defects` build tag any more, so any command carrying one runs zero tests and exits 0 — a silent pass. If you find such a command in another document, it is stale.
It is build-tagged `defects`, so it does not affect normal runs. Rename it if
you prefer a clearer name.

### A.3 State of delivery — MERGED

Both artifacts are on `v3base`. No further delivery steps.

| Repo | `v3base` | How it landed | Contents |
|---|---|---|---|
| `jmdn` | `9197f5a` | PR **#123**, squash | `docs/audit/AVC-CONSENSUS-HANDOVER.md` |
| `avc` | `b83199b` | PR, fast-forward | `tests/audit/audit_poc_test.go` · `randao/accumulator_race_test.go` |

The `audit/2026-09-03-consensus` and `audit/consensus-2026-09` branches were
deleted after merge, local and remote.

**Post-merge verification on `v3base` (2026-09-04, go1.26.3 darwin/arm64):**
`go build ./... && go vet ./...` clean in both repos · 9 PoCs pass · `go test
./randao/` clean · D-24 reproduces with 4 DATA RACE blocks, exit 1.

**No avc tag or jmdn `go.mod` bump was needed, and none is needed now.** The avc
commit adds only `_test.go` files; Go never compiles a dependency's test files;
and jmdn carries no `replace` directive, so it builds avc from the module cache
at `v0.1.0-v3base.2` (= `fd5eef8`). Note that `avc/v3base` is now `b83199b`,
one commit *ahead* of that tag — harmless, because the difference is test-only.

**A new tag WILL be required for D-24 and D-32.** Both touch avc production code
(`randao/accumulator.go`, `crdt/crdt.go`). Each of those fixes needs, in order:
avc PR → new `v0.1.0-v3base.N` tag → jmdn `go.mod` bump → jmdn PR. Record that
on those two rows when you assign them.

### A.4 Superseded material — already deleted

Earlier revisions of this audit existed as an HTML page and as loose markdown in
the local `WORKDIR2` scratch directory (not version-controlled). **All were
deleted on 2026-09-04.** The published HTML page was replaced with a stub
pointing at this document, so any saved link redirects rather than showing stale
findings.

`WORKDIR2/CONSENSUS-AUDIT-2026-09-03-SLACK.txt` was deliberately kept — it is
the Slack-pasteable form of §1 plus the register. Delete it once posted.

---

## Appendix B — PoC → finding map

`avc/tests/audit/audit_poc_test.go`, run with `go test ./tests/audit/ -v`.

> **Read the result correctly — the suite is no longer uniform, and three of its PoCs cannot see the fixes they name (§0.5).** PoCs 1-3 pass because they assert **avc's** behaviour, which is unchanged — **not** because D-25/D-27 are still present. Both are `Fixed` in jmdn, which no avc test can reach. PoCs 4-5 pass **because the D-32 fix holds** — inverted in avc `b11b686`, and the only pair whose fix lived in the same module. The two Controls and two Negatives must always pass. This package carries **no build tag**, so it runs on a plain `go test ./...`: a fix landing without its PoC inversion turns avc's whole suite red, which is exactly what happened when avc #4 and #5 met on `v3base`. That is why §0.2 requires the inversion in the same commit as the fix.

| Test | Finding | Asserts (today) |
|---|---|---|
| `TestPoC1_SilentSaltFallbackSeatsDifferentCommittees` | D-25 | two nodes seat different committees for one epoch |
| `TestPoC2_BootstrapEpochsSilentlyEvicted` | D-27 | pinning > `retain` epochs loses the earliest |
| `TestPoC3_BootstrapSetDivergesFromEntropySet` | D-27 | epochs marked bootstrapped have no entropy |
| `TestPoC4_MergeTieBreakIsDeterministic` | D-32 | **INVERTED** (was `…IsNondeterministic`) — 400 identical merges → exactly 1 result. Passes while the fix holds |
| `TestPoC5_MergeProducesConvergentSerialisedState` | D-32 | **INVERTED** (was `…ProducesDivergent…`) — 300 identical merges → exactly 1 serialised state |
| `TestControl1_QuorumIsByzantineSafeAtEverySize` | §7.1 | **control** — quorum correct, n=1…500. Must pass forever. |
| `TestControl2_BeaconSourceFailsClosed` | §7.1 | **control** — avc's sink is correct. Must pass forever. |
| `TestNegative1_MergeIsCommutativeForDistinctMaxima` | §7.2 | **negative** — non-commutativity withdrawn. Failure ⇒ reopen D-32. |
| `TestNegative2_NondeterminismDoesNotFlipMembership` | §7.2 | **negative** — membership flip withdrawn. Failure ⇒ a `Remove` entered the vote keyspace; escalate D-32. |

Separately, D-24's regression gate — `avc/randao/accumulator_race_test.go`, which replaced the deleted `zz_race_probe_test.go`:

```bash
cd avc && go test -race ./randao/
```

| Test | Finding | Asserts (today) |
|---|---|---|
| ~~`TestAccumulatorFoldRace`~~ → `TestFoldIsRaceFreeUnderConcurrentWriters` + `TestFoldIsRaceFreeAgainstConcurrentReaders` | D-24 | **FIXED, PROBE PROMOTED.** The `-tags defects` probe was deleted in avc `b11b686`; its replacement is the untagged `avc/randao/accumulator_race_test.go`, which must pass CLEAN. Historical: before `498241b` the probe produced 2 DATA RACE blocks on linux/arm64 go1.26.0 and 4 on darwin/arm64 go1.26.3, citing the `mix` XOR and the `folded` map write — `accumulator.go:249-250` and `:252` in today's numbering, paired with the `:241` read (the old `:195/:209/:211` citations have all drifted) |

**Findings with no PoC — 27 of 36 rows** (recounted 2026-09-16; earlier revisions said 23 of 32, "eighteen", and 26 of 35 — all stale)**:** D-28, D-30, D-31, D-33, D-34, and all of D-38…D-59. Each needs a running node, a two-node harness, or arrived after this suite was frozen. Their "Done when" clauses describe the test to write. **§0.2 rule 2 applies to those descriptions — but see §0.5 before inverting anything:** where the fix lands in `jmdn` and the PoC lives in `avc`, inverting is impossible or wrong, and the rule means *add a jmdn-side test* instead.

**Findings that DO carry executable evidence — 9 rows**, and these are the ones to imitate:

| Finding | Evidence | Repo |
|---|---|---|
| D-24 | `randao/accumulator_race_test.go` (2 tests, `-race`) | avc |
| D-25 | PoC 1 | avc |
| D-26 | `crdt/votes/write_identity_binding_test.go` (avc half only — the jmdn halves are untested) | avc |
| D-27 | PoC 2, 3 | avc |
| D-29 / D-35 | `Sequencer/vdf_modulus_policy_test.go` — 5 tests incl. the end-to-end refusal with the override set | jmdn |
| D-32 | PoC 4, 5 (inverted) + `crdt/tiebreak_determinism_test.go` | avc |
| D-36 | `messaging/entropy_fleet_quorum_test.go` — asserts the quorum does not move when a local blocklist is set | jmdn |
| D-37 | `messaging/entropy_recovery_wiring_test.go` — asserts the rebuild REFUSES when unwired rather than returning 0 | jmdn |

**The pattern worth copying:** each of D-35/D-36/D-37's tests fails on the pre-fix code, and each asserts the *property* rather than the implementation — "the quorum does not move", "the refusal names the wiring call", "renaming the modulus does not evade the guard". A test that asserts the implementation passes after a refactor that reintroduces the defect.

---

## Appendix C — Limits of this audit

### C.1 What could not be verified

- **jmdn's own build and test suite — PARTIALLY CLOSED since rev 4.** The original sandbox filled its filesystem pulling jmdn's dependency graph (libp2p, go-ethereum, duckdb, pgx), so D-25…D-34's jmdn rows are source-traced at a specific line, **not compiler-verified**. That was the audit's main weakness. It is now mixed: D-29/D-35/D-36/D-37 each ship a named jmdn test that has been **executed**, and D-38…D-58 were source-traced then re-verified against `v3base@291d44c`, with every row re-checked again at `e4f18da` in rev 6.
  Close the remainder with: `cd jmdn && GOWORK=off go build ./... && GOWORK=off go test -race ./messaging/... ./Sequencer/... ./Security/...`
  **Use `GOWORK=off`** — a `.go.work.local` from `make dev-workspace` silently substitutes sibling checkouts for the pinned tags, so a green run under a workspace proves nothing about what the fleet builds.
  **And judge against the baseline, not against zero:** ten tests fail identically on `v3base` itself (§7.1). Six of them fail *only* under `-race`.
  ~~Note that `Sequencer/beacon_bootstrap_test.go` passes today while missing D-27~~ — **closed rev 6:** that file now carries `TestValidateBootstrapFitsRetention_Boundary`, and the guard it exercises is `span > retain`. The wider point stands for every other row: a green suite proved nothing about any finding in this register.
- **D-26's exploitability** depends on whether libp2p pubsub message signing is enabled on this fleet's `GossipSubPS` construction. It would not fix the defect — the code reads the payload field regardless — but it changes how easily a non-buddy reaches the topic.
- **D-28's fork acceptance end-to-end.** Confirmed at the hash level by reading both preimages and grepping for the absent fields; a two-node harness proposing same-transaction blocks at one height would settle the runtime behaviour.
- **D-29's environment gate.** How `network.environment` is set in a real mainnet deployment was not verified, nor whether a separate config path would make the check land elsewhere.
- **`jmdn/AVC/BFT`** is a divergent vendored fork of `avc/bft`. The original was audited; the fork's copies of `byzantine.go`, `engine.go` and `sequencer_client.go` may have drifted.
- **D-24's trigger frequency is unquantified.** The race is proven; how often two blocks fold reveals for the same epoch concurrently depends on block rate, gossip fan-in and reveal density. It is latent today only because the devnet sets no VDF env vars — a config accident, not a safeguard.

### C.2 Staleness

**Rev 5 drift check, 2026-09-11.** `jmdn/v3base` has advanced
`dda7c4a9` → **`291d44c`** and `avc/v3base` `1c13324` → **`4df28ca`**
(= `v0.1.0-v3base.5`) since the 2026-09-04 check below. Every §0-§2 claim,
command, path, symbol and count was re-resolved against that state; the
corrections are marked inline and summarised here.

**What moved, and what it cost this document:**

| | |
|---|---|
| **Fleet dependency upgrade** (2026-09-08/09) | go-ethereum unified to **v1.17.5** across all consumers (was three versions), go-libp2p **v0.44→v0.49**, OTel **1.46**, ion **v0.5.0**. jmdn now pins avc `.5` / ThebeDB `.2` / FastSync `.3` and has **zero** `replace` directives — `cd281a0` dropped the last one (genproto) as inert and `291d44c` hardened `make verify-pins` to reject *any* replace. None of this invalidated a finding. |
| **PR #125 merged** (`6eb0cc7`, squash) | Closed D-29 properly (value-keyed, see D-35), added D-35/D-36/D-37, and shipped 4 executed jmdn tests. |
| **PRs #127, #128, #130, #131 merged** | No finding affected. #128 added the `make local-replace` / `verify-pins` discipline that D-21-class regressions need. |
| **PR #129 OPEN** (`7baa3c22`) | Half-fixes D-30 and **flips `CONSENSUS_HASH_V3`'s default**, which D-38 records as a contested answer. |
| **avc `.5`** | Carries D-26's avc-side half (`3eee4b0`) and the PoC 4/5 inversion + probe deletion (`b11b686`). |

**Corrections rev 5 had to make to this document's own prior revisions** — recorded because the pattern matters more than the individual fixes:

1. The verdict, the §0.3 gate diagram and two §0.1 steps all still named **D-24 as a live pre-beacon gate** after it was fixed. A fixed finding restated as open in four places is how a register loses authority.
2. **Every count in §1 was wrong** (3/3/4 against an actual 5/7/14/5) and "Fixed: 1" against an actual 7. Counts drift silently because nobody recounts.
3. Three copies of a **dead command** (`-tags defects … TestAccumulatorFoldRace`) survived the deletion of the file it tested. That command exits **0** having run nothing — a silent pass, the worst failure mode for a verification instruction.
4. Four artifact-table cells named the **deleted probe** as a current deliverable.
5. §7.1's "verified sound — do not fix" **masked a SEV-1** (D-36) by asserting one call site's property as the codebase's.
6. The prior revision's own restore note had the **PR #125 diffstat wrong** (57/+8,633/−131 against an actual 61/+9,335/−137), said "sixteen findings" for thirteen rows, and listed #129 as merged when it is open.
7. D-26's row **overstated its own fix**, describing a re-keying that did not happen and omitting that the guard is inert in jmdn.

**Structural lesson, and the reason this appendix now exists:** every one of those seven is the register describing itself rather than the code. **A finding register decays fastest in its summary layer** — verdict, counts, commands, cross-references — because that layer is derived, and nothing recomputes it. Re-verify the summary layer on every revision, not just the rows.

---

**Drift-check history (2026-09-04 and earlier) — retired rev 6.** Those checks tracked
`84d0c54f` → `dda7c4a9` and avc `aba96c7` → `1c13324`, and each concluded "no finding moved, one
line number changed." All of it is superseded: the tree is now `jmdn@e4f18da` / `avc@4df28ca`
(= `v0.1.0-v3base.5`) / `ThebeDB@533622a`, re-verified rev 5 (2026-09-11) and rev 6 (2026-09-15).
Keeping three generations of superseded commit pairs is exactly the summary-layer decay this
appendix warns about, so they are dropped rather than accumulated.

**Two durable items rescued from that history:**

- **jmdn's module graph has no `replace` directives** — local paths were dropped for pinned tags, and
  `291d44c` hardened `make verify-pins` to reject *any* replace. Do not reintroduce one.
- **`jmdt-devnet/dockerfiles/jmdn.gate.Dockerfile:46-77` re-injects local `replace` directives**, so
  the devnet container builds from sibling directories rather than the pinned tags. They agree today
  and **nothing enforces that they keep agreeing** — still open, tracked in §6.

Re-pin before acting on anything here:

```bash
for r in jmdn avc ThebeDB jmdt-devnet; do
  printf '%-14s %-28s %s\n' "$r" \
    "$(git -C $r branch --show-current)" "$(git -C $r rev-parse --short HEAD)"
done
```

**Fastest staleness check:** `cd avc && go test ./tests/audit/ -v`. Nine passes means the findings stand.
