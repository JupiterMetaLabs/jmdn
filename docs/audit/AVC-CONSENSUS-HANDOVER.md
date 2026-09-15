# AVC Consensus — Audit Handover Document

| | |
|---|---|
| **Status / Verdict** | **No-Go for mainnet.** No-Go for enabling Stage 2 entropy until **D-27** is fixed with regression evidence — D-24, the other pre-beacon gate, landed in avc `498241b`. Also gate on **D-39** (a per-host difficulty `T` diverges silently) before any multi-node enablement. Go for continued devnet use — but only because the devnet's `T` and modulus are fleet-uniform and the chain-id guard now refuses the trapdoored pin elsewhere. |
| **Date** | 2026-09-03, **last revised 2026-09-11** (three passes consolidated; rev 3 re-derived from `v3base` after all four repos moved; rev 4 added D-35…D-37 from the PR #125 review; rev 5 restored D-38…D-50 and re-verified every §0-§2 claim against live code) |
| **Scope** | Originally audited at `jmdn@84d0c54f` · `avc@1c13324` · `ThebeDB@6b8f6d1` · `jmdt-devnet@fd52106` (`main` — **no `v3base` branch exists in that repo**), re-based to `jmdn@dda7c4a9` on 2026-09-04. **Rev 5 re-verified against `jmdn@291d44c` and `avc@4df28ca` (= `v0.1.0-v3base.5`).** Entropy pipeline end-to-end, committee selection seam, vote ingest and BLS signing path, block-hash preimages, CRDT merge determinism, quorum arithmetic (all four implementations), devnet config vs code expectations; rev 4 added the persistence layer, the fallback aggregate-signature fold, VDF network pins, and the libp2p vdf-proof pull protocol. |
| **Method** | Full source trace → Go 1.26.0 toolchain in a clean sandbox → `go vet` + `go test -race` across avc's consensus packages (all green) → **9 proof-of-concept tests written and executed** → Go race detector on the reveal-fold path → viper precedence reproduced by execution to settle two config claims → re-derivation from `v3base` after the repos moved mid-audit → rev 4: five parallel line-by-line readers over PR #125's 61-file diff → rev 5: every §0-§2 claim, command, path, symbol and count re-resolved against the live tree. |
| **Companion file** | `avc/tests/audit/audit_poc_test.go` — executable evidence. **PoCs 1-3 PASS while a defect exists; PoCs 4-5 are already INVERTED** and pass while the D-32 fix holds. Invert each remaining assertion when fixing, to convert it into a regression test. D-24's regression test is `avc/randao/accumulator_race_test.go` (`TestFoldIsRaceFreeUnderConcurrentWriters`, `TestFoldIsRaceFreeAgainstConcurrentReaders`) — untagged, runs on a plain `go test`. The `-tags defects` probe it was promoted from was **deleted** in avc `b11b686`. |
| **Reproduce** | `cd avc && go test ./tests/audit/ -v` (9 tests, ~10s — **PoC 4 and PoC 5 are now INVERTED regression tests** that pass while the D-32 fix holds; PoC 1-3 still reproduce D-25/D-27) · `cd avc && go test -race ./randao/` (D-24's fix — `accumulator_race_test.go`; the old `-tags defects TestAccumulatorFoldRace` probe was **deleted** once promoted, so that command no longer exists) · `cd avc && go test -race ./quorum/... ./committee/... ./crdt/... ./beacon/... ./randao/... ./vdf/...` (baseline, clean) |
| **Prior audits** | Continues `D-1…D-23` from the 2026-08-31 reports in `WORKDIR2/audits/`. New findings are **D-24…D-50** (D-35…D-37 from the PR #125 review, D-38…D-50 restored 2026-09-11 — see the note under §2.2). Never renumber. |
| **⚠ Third register, UNCOMMITTED** | A separate register with its own `SEC/CON/STO/EVM/SYN/NET/API/PRC` ID space lives at **`WORKDIR2/THEBE-AUDIT-HLD.md`** — outside every git repo. **52 jmdn source files cite those IDs** as the reason their code is shaped the way it is (e.g. `execbridge/execbridge.go` cites `EVM-02/09/29/30/31`; `config/settings/security_posture.go` cites `SEC-03`), so a fresh clone cannot resolve any of them. It also carries a live CRITICAL, `API-10`: `gETH/Facade/rpc/http_server.go` runs batch JSON-RPC in goroutines with **zero `recover()`**, so a peer-triggered panic kills the node (re-verified unfixed 2026-09-11). **Commit that file into `jmdn/audits/` before these cross-references mean anything.** No `DEP-` ID is cited anywhere in jmdn code. |

**How to collaborate on this document:** the Findings Register (§2) is the living part — update the Status column there (rules in §2.1). Everything below §2 is the evidence body; append corrections rather than rewriting history. New audit passes append new finding IDs.

**Cross-repo note:** 25 of the 27 register rows are `jmdn` code and 2 are `avc` (D-24, D-32); the 9 `jmdt-devnet` items are tracked as an unnumbered group in §6. This document lives in `jmdn` because that is where most fixes land; the PoC suite lives in `avc` because that is the only module it compiles in. The register's Repo column says who owns each row.

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
   All 9 tests pass, but read the result correctly: **PoCs 1-3 passing = D-25/D-27 are still present**; **PoCs 4-5 passing = the D-32 fix holds** (they were inverted in avc `b11b686`); the two Controls and two Negatives must always pass. Appendix B maps each to its finding. Then D-24's regression gate:
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
5. Assign an owner to the SEV-1 rows that are **not** yet `Fixed`: **D-25** and **D-26**. The other three SEV-1 rows (D-24, D-35, D-36) read `Fixed`. Both `avc`-owned rows (D-24, and D-32 — which is **SEV-3**, not SEV-1) are also `Fixed`, so nothing in `avc` currently needs an owner; D-26's remainder, however, is **jmdn**-side work (the avc half landed and is inert here).
6. Housekeeping (Appendix A.2) — **already done**, nothing to do.
7. **Assign an owner to every SEV-1 and SEV-2 row that is not yet `Fixed` before any fix work starts.** There are 11 such rows; **6 remain open**: D-25, D-26, D-27, D-28, D-38, D-39. (D-24, D-29, D-35, D-36, D-37 read `Fixed`.) Merging is done — §2.2 is now the live tracking surface, so from here the register moves forward one PR at a time under the §0.2 fix rule. The first to assign is **D-27**: it is the last remaining pre-beacon gate, and until it reads `Fixed` nobody may set the three `JMDN_AVC_VDF_*` variables (§0.3, §0.4). Assign **D-39** alongside it — a per-host `T` diverges silently and no test can catch it.

### 0.2 Fix rules

Every fix — whatever branch it lands on — must contain, **atomically in the same commit**:

1. the code change;
2. the corresponding PoC assertion in `avc/tests/audit/audit_poc_test.go` **inverted** — the defect proof becomes the regression test. Each PoC's failure message already names what to do;
3. the §2.2 register row flipped to `Fixed (<commit>, <test name>)`.

A fix missing (2) or (3) is not done. **Eighteen** findings have no PoC — D-28, D-30, D-31, D-33, D-34 and all of D-38…D-50 — because they need a running node, a two-node harness, or arrived after the PoC suite was frozen. For those, rule (2) means **write the test the finding's "Done when" clause describes.** Nine rows do carry a PoC or a named test: D-24, D-25, D-26, D-27, D-29, D-32, D-35, D-36, D-37.

### 0.3 Fix-ordering warning

**D-27 is now the only remaining pre-beacon gate, and that ordering is the single most important sentence in this document.** D-24, the other gate, landed in avc `498241b` (carried by `v0.1.0-v3base.3`; jmdn pins `.5`).

D-33 (entropy genesis bootstrap) shipped shortly before this audit and is a good fix. It also made the beacon *reachable for the first time*, which put the reveal-fold path into play — and that path was guarded by an accumulator with no synchronisation (D-24). Before D-33, `entropyAccumulatorFor` always failed and `Fold` never ran. That accident is gone, and so, now, is the race it exposed.

```
D-27 (jmdn)   ──────►   enabling Stage 2 entropy
                        JMDN_AVC_VDF_MODULUS_HEX
                        JMDN_AVC_VDF_GROUP_NAME
                        JMDN_AVC_VDF_DIFFICULTY_T

CLEARED:  D-24 (avc 498241b — mutex on randao.Accumulator)
          D-29/D-35 (jmdn PR #125 — value-keyed modulus chain policy)
```

Enabling the beacon before D-24 *used to* convert a silent divergence into a validator crash loop (`fatal error: concurrent map writes` is unrecoverable); `498241b` closes that. Enabling before D-27 still silently discards pinned bootstrap epochs. **D-25 should also land first**, so that a missing entropy value stalls loudly instead of diverging quietly.

**Two newer gates belong here too, both `Open`:**

- **D-39 — difficulty `T` has no fleet-agreement check.** Setting `JMDN_AVC_VDF_DIFFICULTY_T` to a *different* value on one node is undetectable: that node rejects every honest peer proof AND publishes its own divergent entropy into its own sink, seating committees no peer agrees with. Nothing gossips, persists or hashes `T`; `beacon.Pipeline.Difficulty()` has **zero jmdn callers**. Treat `T` as a chain parameter, not a per-host env var, before any multi-node enablement.
- **D-38 — the D-28 fix ships inert, and PR #129's answer to that is contested.** `ConsensusHashV3Enabled` defaults `false`, so the preimage gap D-28 describes is live on a default node, and `ValidateProductionConsensusPosture` does not check the flag. PR #129 flips the default to `true`, which the flag's own unchanged comment forbids ("deploy the binary everywhere with this off, then flip the whole fleet together") — a rolling restart under that default forks the network. Resolve D-38 by adding the flag to the posture check and logging its state, **not** by flipping the default.

Everything else in the register is independent and parallelisable.

### 0.4 Release gate

Flip the verdict at the top of this document to **Go** only when every SEV-1 and SEV-2 row in §2.2 reads `Fixed`. Until then:

- do **not** set `JMDN_AVC_VDF_MODULUS_HEX` / `_GROUP_NAME` / `_DIFFICULTY_T` on any fleet;
- do **not** ship `rsa-2048-testnet-ephemeral` to any network with adversaries — it is trapdoored by construction. Since PR #125 a *mechanical* guard refuses it off-devnet by modulus **value**, not group name (`enforceModulusChainPolicy`, `Sequencer/vdf_network_pins.go`, called from `beacon_install.go` before any group is constructed); D-29 and D-35 read `Fixed`, so this is now a policy reminder rather than an open exposure. Note the guard keys on the *digest*, so renaming the modulus does not evade it — that was D-35;
- do **not** set a per-host `JMDN_AVC_VDF_DIFFICULTY_T` (D-39): a divergent `T` is silent on the side that is wrong;
- do **not** trust a green `go test ./...`. Every finding in this document survives a green suite; that is the single most useful fact here.

### 0.5 Acceptance tests & pass criteria

Today the PoC suite **passes while defects exist**. As each fix lands, its assertion is inverted (§0.2 rule 2); once all rows are done, `go test ./tests/audit/ -v` passing means **defects absent** — the suite's meaning flips from proof-of-defect to regression gate.

Two PoCs are **controls** and must pass forever, before and after every fix: `TestControl1_QuorumIsByzantineSafeAtEverySize` (the quorum maths is correct — see §7) and `TestControl2_BeaconSourceFailsClosed` (avc's sink is correct; D-25 is the caller swallowing its error).

Two are **negative checks** recording claims that were tested and did *not* reproduce: `TestNegative1_MergeIsCommutativeForDistinctMaxima` and `TestNegative2_NondeterminismDoesNotFlipMembership`. They exist so those claims are not re-litigated. **If either ever fails, a real regression has occurred** — the failure message says which finding to reopen.

---

## 1. Verdict

```
DECISION: No-Go for mainnet. No-Go for enabling Stage 2 entropy until D-27
          is fixed with regression evidence. (D-24, the other pre-beacon
          gate, landed in avc 498241b — see §0.3.)

          The entropy pipeline moved from "cannot start" to "can start
          unsafely" and is now partway back: 10 of 34 rows read Fixed, but
          every remaining SEV-1/SEV-2 is a silent-divergence class.
          (The "7 of 27" here was stale in two ways before 2026-09-15: the
          denominator never matched the 32-row total below it, and PR #129
          has since closed four more.)

SEV-1:  5  — D-24 accumulator race (FIXED) · D-25 silent salt fallback
             · D-26 vote ingest (avc-side only; inert in jmdn)
             · D-35 name-keyed modulus guard (FIXED)
             · D-36 blocklist-skewed fold quorum (FIXED)
             → STILL OPEN: D-25, D-26
SEV-2:  7  — D-27 bootstrap eviction (FIXED, PR #129)
             · D-28 hash binding (PARTIAL: cert replay fixed, DETECTION open)
             · D-29 trapdoored pin (FIXED) · D-37 recovery before wiring (FIXED)
             · D-38 D-28's fix ships inert · D-39 difficulty T unagreed
             · D-51 VOTE_CRDT_V2 makes the whole avc vote keyspace inert
               (the reason D-26's avc-side fix closes nothing)
             → STILL OPEN: D-28 (detection half), D-38, D-39, D-51
SEV-3: 16  — D-30 bloom filter (FIXED, PR #129) · D-31 watermark TOCTOU
               (open — and see its row: PR #129's "D-31" commit is MIS-TAGGED)
             · D-32 merge nondeterminism (FIXED) · D-34 unbounded state
             · D-40 M2bHashEnabled gates nothing
             · D-41 resolvePendingFallbacks (FIXED, PR #129 3d01e89)
             · D-42 O(M²) restart loop · D-43 padded proof kills pull-recovery
             · D-44 recovery targets the wrong epoch · D-45 newest-pointer RMW
             · D-46 sealer lifecycle · D-52 vote ingest cap is replica-local
             · D-53 DefaultWatermark is process-wide consensus state
             · D-54 VDF group agreement bound only implicitly
             · D-56 CommitteeEpochBlocks 0→20 — DECIDED 2026-09-15: keep 20,
               keep CadenceBlocks 0, ship as-is. No fork risk below a pool of
               MaxValidators(7); the restart is already forced by the hash
               freeze. The checkpoint-cadence concern is RETRACTED (Enabled
               defaults false, and per-epoch is what cadence 0 means by
               design). Residual: the epoch-0 sentinel collision, which gates
               RequirePinnedCommittee                            (NEW)
             · D-57 the D-31-tagged fix erases equivocation evidence
               (gated behind JMDN_VOTE_CRDT_V2 — BLOCKER before that flips;
               recommended fix: drop the delete, it is not load-bearing) (NEW)
SEV-4:  5  — D-47 retention floors · D-48 ungated pull protocol
             · D-49 release hygiene · D-50 false comments
             · D-55 ByzantineQuorum(n<1) disagrees with avc Threshold
SEV-3/4 devnet: 9 — §6 (tracked as a group, not in the 34 below)
Unrated: 1 — D-33 (fixed, with remainder)

34 rows total: 10 Fixed · 1 Partial (D-28) · 1 Decided-no-change (D-56) · 22 Open.
  Recount 2026-09-15: 5 + 7 + 16 + 5 + 1 = 34. Fixed = D-24, D-27, D-29,
  D-30, D-32, D-33, D-35, D-36, D-37, D-41 = 10. 34 − 10 − 1 − 1 = 22 Open.
  PR #129 MERGED to v3base 2026-09-15 (rebase-merge, 25 commits replayed,
  trees verified identical). Every row attributed to it is therefore fixed in
  the code the fleet builds from — not just on a branch.
  NOTE: that merge REWROTE every commit SHA. All 49 SHA references in this
  file were remapped to their post-merge equivalents on 2026-09-15, each
  verified 1:1 by matching commit subjects. Pre-merge SHAs quoted anywhere
  else (older copies of this file, PR comments, chat) are dead.
Rev 5 added D-51…D-55 — five protocol-level findings that were visible in the
code but had no row, three of them surfaced by re-verifying §7.1 and §7.3
rather than by reading new code. **Two of the five exist because a previous
revision recorded the finding in prose instead of the register**, where it
could not be assigned: D-55 was parked in §7.1 under "do not fix these", and
D-54 was implicit in D-39's wording.

Fixed:                    7 — D-24, D-29, D-32, D-33, D-35, D-36, D-37
                              (+ D-26 avc-side, which closes nothing in jmdn)
Withdrawn after testing:  4 — §7.2
Verified sound:           4 — §7.1, but read 7.1's own caveat first: one of
                              its four bullets masked a SEV-1 (D-36).

Audited:  entropy pipeline end-to-end (randao → VDF → beacon → committee);
          committee selection seam; vote ingest and BLS signing; block-hash
          preimages; CRDT merge determinism; quorum arithmetic (4 impls);
          devnet config vs code expectations; and — in the PR #125 pass —
          the persistence layer, fallback aggregate-signature fold, VDF
          network pins, and the libp2p vdf-proof pull protocol.
NOT inspected: jmdn/AVC/BFT's consensus logic (divergent vendored fork of
          avc/bft — only its quorum arithmetic was executed, §7.1); libp2p
          pubsub signing configuration; ThebeDB beyond pkg/kv and
          pkg/checkpoint; MRE; seedNodes.
EXECUTION:  D-25…D-34's jmdn rows are source-traced, not compiler-verified —
          the original sandbox exhausted its filesystem on jmdn's dependency
          graph. D-29/D-35/D-36/D-37 each ship a named jmdn test that has
          been executed. D-38…D-50 are source-traced and were re-verified
          against v3base@291d44c on 2026-09-11. See Appendix C.1.
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

| ID | SEV | Repo | Finding | PoC | Effort | Status |
|---|---|---|---|---|---|---|
| **D-24** | 1 | `avc` | `randao.Accumulator` has no synchronisation; jmdn calls `Fold` from two concurrent commit hooks | race probe | S | `Fixed (avc 498241b, randao/accumulator_race_test.go)` |
| **D-25** | 1 | `jmdn` | `SeedSourceFor` silently falls back to the Stage-1 salt; two nodes seat different committees | PoC 1 | S | `Open` |
| **D-26** | 1 | `jmdn` | Vote CRDT keyed on a self-declared sender; requester chooses the signing target; guard off and unwired | `avc crdt/votes/write_identity_binding_test.go` | L | `Open — avc-side hardening landed, closes nothing in jmdn yet. avc 3eee4b0 (first carried by v0.1.0-v3base.5) makes AddVote REJECT a record whose payload rec.PeerID differs from the libp2p-authenticated nodeID (write.go:74); it does NOT re-key — both elements are still keyed on rec.PeerID (write.go:83/:95/:100, and its own comment at :46 says so). Structurally inert in jmdn today: the sole non-test caller (Vote/Trigger.go:286) sets rec.PeerID from the same identity it passes as nodeID (:268), so the two can never differ, and the whole block sits behind JMDN_VOTE_CRDT_V2 (default OFF). avc also EXEMPTS the merge path (write.go:64-66). D-26(a)''s actual defect site — jmdn legacy pubsub ingest keying the CRDT on msg.Data.Sender in AVC/.../Service/subscriptionService.go — does not call AddVote at all and is untouched. Remainder: (a) legacy ingest, (b) membership filter, (c) signing target, (d) requester-auth guard` |
| **D-27** | 2 | `jmdn` | Bootstrap epochs silently evicted when the pinned list exceeds `retain` | PoC 2, 3 | S | `Fixed (PR #129 7e6a798 ValidateBootstrapFitsRetention + f121048 off-by-one; merged to v3base 2026-09-15). 7e6a798 rejected at span >= retain, one too strict — an epoch survives iff e >= newest-retain, so span == retain sits ON the cutoff and is kept. f121048 corrects to > retain and adds TestValidateBootstrapFitsRetention_Boundary; the original pair used spans 0/5/10 vs retain 6, which give the SAME verdict under both operators` |
| **D-28** | 2 | `jmdn` | `ConsensusHash` binds neither `PrevHash` nor `BlockNumber`, even under M2b | — | M | `PARTIALLY Fixed (PR #129 8872912, merged to v3base 2026-09-15). The preimage now binds BlockNumber + PrevHash and the v2/v3 flag is deleted — one unconditional format, pinned by TestConsensusHashPreimageIsPinned. That closes CERTIFICATE REPLAY across forks. It does NOT close equivocation DETECTION: checkEquivocation is still keyed on BlockHash (blockPropagation.go:677), which is transactions-only, and the pre-validation dedup key is the same colliding value (getBlockDedupID), so the second fork is dropped as a duplicate at :295 before the check runs. 8872912's header claimed detection was fixed; 75bb26a corrects that claim. Re-keying BOTH the equivocation map and the dedup cache onto ConsensusHash is a design change — still OPEN` |
| **D-29** | 2 | `jmdn` | Trapdoored testnet VDF modulus with no mechanical mainnet guard | `TestBuildVDFGroupRefusesRestrictedModulusUnderForeignNameWithOverride` · `TestTrapdoorPinRefusedInProductionEvenOnItsAllowedChain` | S | `Fixed (PR #125 enforceModulusChainPolicy — see D-35) + HARDENED (PR #129 543d307 + 87636d7, merged to v3base 2026-09-15). #125 keyed the refusal on chain id alone, so a trapdoored pin was still installable on a PRODUCTION node whose chain id happened to match the allow-list — a copied .env was enough. 543d307 adds ErrTrapdooredGroupInProduction/ErrUnpinnedModulusInProduction keyed on production posture, tested on the pin's OWN allowed chain (otherwise #125's guard fires first and the test passes vacuously). 87636d7 then makes it real: main.go:1589 previously LOGGED those errors and continued, booting on Stage-1 salt entropy — fail-closed as a function, fail-open as a process. Now os.Exit(1)` |
| **D-30** | 3 | `jmdn` | Bloom dedup filter is lock-free and saturates to 83% FP in ~14h | — | M | `Fixed (PR #129 e986ba6 blockPropagation + 76137cd DIDPropagation; merged to v3base 2026-09-15). Both paths now use an eagerly-initialised, bounded hashicorp/golang-lru/v2 — exact, never a false positive. The half-applied state recorded here on 2026-09-11 (DIDPropagation still on bloom at head 7baa3c22) was closed by 76137cd, which also found a THIRD defect this row did not: accountOnce was SHARED with InitDIDPropagation, so a DID stream arriving first consumed the Once and InitDIDPropagation silently skipped its whole body, leaving accountsClient nil. ContractPropagation.go correctly keeps bloom — contractFilterMu guards it properly` |
| **D-31** | 3 | `jmdn` | Epoch watermark TOCTOU duplicates finalisation; duplicate seal blocks a goroutine forever | — | M | `Open — AND BEWARE A MIS-TAGGED COMMIT. PR #129's 1624583 is titled "fix(crdt): prevent watermark TOCTOU resurrecting compacted votes (D-31)" but does NOT touch this defect: it edits avcvotes.DefaultWatermark in CRDTSyncHandler.go, i.e. the VOTE-COMPACTION watermark, which this register assigns to D-53 ("D-31 covers the epoch watermark TOCTOU; nothing covered the vote-compaction watermark"). D-31's own subject is maybeFinaliseCompletedEpochs' EPOCH watermark, which already carries a claim-under-lock fix predating PR #129 — see D-41 for the residual that PR #129 did close. Do not mark D-31 fixed on the strength of 1624583's title. 1624583 also introduced a new defect → D-57` |
| **D-32** | 3 | `avc` | `extractNodeID` tie-break reads a Go map → nondeterministic merge | PoC 4, 5 | S | `Fixed (avc 193ab86, TestPoC4_MergeTieBreakIsDeterministic + crdt/tiebreak_determinism_test.go)` |
| **D-33** | — | `jmdn` | Entropy genesis bootstrap — **shipped**; persistence and an observe rung remain | — | M | `Fixed (b5e305a8) — remainder Open` |
| **D-34** | 3 | `jmdn` | Unbounded maps on the block-receive path (`seenHeights` et al) | — | M | `Open` |
| **D-35** | 1 | `jmdn` | **The D-29 fix did not hold.** Chain guard keyed on the group NAME, not the modulus VALUE — the trapdoored devnet modulus installs on any chain under the name `rsa-2048-frc` (unpinned in avc, matching shape) with the unpinned override set | `TestModulusChainPolicyIsKeyedOnValueNotName` | S | `Fixed (PR #125, enforceModulusChainPolicy)` |
| **D-36** | 1 | `jmdn` | Fallback fold's Byzantine denominator taken from the block_buddy-FILTERED pool, so one operator's local blocklist moves the threshold (n=6/q=4 vs fleet n=7/q=5) → different fold subset → different seed → **different committee** | `TestAggCertQuorumIsIndependentOfLocalBlocklist` | S | `Fixed (PR #125, fleetCommitteeSnapshotFor)` |
| **D-37** | 2 | `jmdn` | `RecoverAggSigStoreAtStartup` called ~400 lines before the committee eligibility source is wired — always returned 0, neither call-site branch printed, and the only symptom was up to 512 "parent certificate failed verification" errors that read as tampering | `TestRecoveryRefusesWhenEligibilitySourceIsUnwired` | S | `Fixed (PR #125, relocated + up-front probe)` |

| **D-38** | 2 | `jmdn` | **D-28's fix ships inert.** `ConsensusHashV3Enabled` defaults FALSE on `v3base`, so `BlockNumber`/`PrevHash` are absent from the `ConsensusHash` preimage on a default node — the original D-28 exposure verbatim — and `ValidateProductionConsensusPosture` does not check the flag, so a `strict_posture`/mainnet node boots with no signal. Same gap for `avcvotes.AllowUnsignedValidatorVotes` | — | S | `Open — CONTESTED FIX IN FLIGHT. PR #129 (7baa3c22) flips the default to true, which does close the inertness but is flagged BLOCKER 1 in that PR''s review: the flag''s own unchanged comment says "WHY A FLAG, DEFAULT OFF … deploy the binary everywhere with this off, then flip the whole fleet together", and defaulting it on inverts the failure mode — forget the env var and v3 activates on the next restart, so a rolling restart forks the network against any node still on the old binary. The cited precedent CommitteeSnapshotAnchorEnabled is still false, and every test in consensus_fields_hash_fork_test.go sets the flag explicitly so CI cannot catch either default. THIS FINDING''S RECOMMENDED FIX IS NOT A DEFAULT FLIP: add the flag to ValidateProductionConsensusPosture and log its state at startup, so the exposure is operator-visible while the rollout stays a deliberate, separate event.` |
| **D-39** | 2 | `jmdn` | **Difficulty `T` has no fleet-agreement check.** Validated only as non-zero; `beacon.Pipeline.Difficulty()` has **zero jmdn callers**, so `T` is never gossiped, persisted, or hashed into genesis. A node with `T′ ≠ T` rejects every honest peer proof AND publishes its own divergent value into its own sink, seating committees from entropy no peer holds — with nothing naming `T` as the cause | — | M | `Open` |
| **D-40** | 3 | `jmdn` | `Security.M2bHashEnabled` gates no validation anywhere (`CheckBlockHash` and `checkBodyBinding` both ignore it, by their own comments) yet `main.go:963` `os.Exit(1)`s reward-split without it — a config-triggerable hard exit whose precondition is meaningless, granting a false assurance that `PrevAggCert`/`FeeRecipients` are hash-bound. They are defended, but by the reward-split interlock and an independent recompute, not by `ConsensusHash` | — | S | `Open` |
| **D-41** | 3 | `jmdn` | **`resolvePendingFallbacks` still double-finalises.** D-31's claim-under-lock fix covers the decide path only; this path snapshots under the lock, releases it, then uses `delete(pendingFallback, e)` — a silent no-op on an absent key, so it removes rather than claims. Two commit hooks can both reach `notifyEpochFinalised` for one epoch and, with different seeds, trip the mix-conflict branch — emitting a false SEV-1-shaped alarm for an in-process race | — | S | `Fixed (PR #129 3d01e89, merged to v3base 2026-09-15). Note the commit is labelled "JMDN-V3-009", not D-41, so a search by ID will miss it — THIS row is what it closes. Now claims (deletes) BEFORE computing the seed, with a re-check so a second racing call sees the epoch already claimed and skips. Verified all four branches preserve the prior semantics exactly: nil keeps it claimed, ErrFallbackNotYetReady and the default arm restore the claim for retry, and ErrFallbackDeadlineExceeded keeps it deleted — which the OLD code also did, so no epoch newly becomes unretryable` |
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
| **D-54** | 3 | `jmdn` | **VDF group agreement is bound only implicitly, so D-39's fix scoped to `T` would leave half the problem open.** The accept path explicitly rejects `proof.T != pinned difficulty`, but there is **no** comparison of the group/modulus — agreement is enforced only as a side effect of `vdf.Verify` re-deriving the challenge. Two nodes on different *sourced* moduli both pass `enforceModulusChainPolicy` and then reject each other's proofs with no error naming the group as the cause. Fix D-39 and D-54 together: bind group name + modulus digest + `T` into one fleet-checked identity | — | M | `Open` |
| **D-55** | 4 | `jmdn` | **`ByzantineQuorum(n<1)` returns 1 while `avc Threshold` returns 0** — a real arithmetic disagreement between two consensus implementations, previously parked in §7.1 under a heading that says "do not fix these", where it could not be assigned or tracked. Both are guarded upstream today; this row exists so the divergence is owned if either guard is ever removed | — | S | `Open` |
| **D-56** | 3 | `jmdn` | **`CommitteeEpochBlocks` 0 → 20 takes a consensus-fork risk for a benefit that was then withdrawn — and silently switches on checkpoint signing.** PR #129's bc4a5d4 raised the default *in order to* enable `RequirePinnedCommittee`; 8311504 then reverted the pinning (the epoch-0 sentinel collision — `EpochForHeight(h)=h/20` is 0 for heights 0-19, and seedNodes `pkg/peer/gorm_jmns_service.go:148` reads `epoch == 0` as "serve the current epoch", so every pinned read failed the exact-match check and block 1 could never seat a committee) while KEEPING the 20. Net: the fleet carries a consensus-affecting default change — nodes with different values seat different committees at the same height and reject each other's blocks, and nothing overrides it from YAML or env, so the compiled default IS the parameter — with none of the pinning it was for. **Second, undocumented effect:** `Checkpoint.CadenceBlocks` is `0`, which routes `checkpointCadenceFires` (`messaging/checkpoint_sign.go:269-277`) to the epoch-boundary branch. At epoch length 0 that fired **only at genesis**; at 20 it fires **every 20 blocks**. Checkpoint signing is switched on as a side effect of an unrelated constant, mentioned in neither commit | — | S | `DECIDED 2026-09-15 — KEEP 20, KEEP CadenceBlocks 0. Ship as-is; no code change. Grounds, each verified: (1) NO FORK RISK AT CURRENT SCALE — avc committee/select.go CommitteeFor returns ALL members when k >= len(members), and MaxValidators is 7, so until the eligible pool EXCEEDS 7 the epoch length changes nothing about who is seated. It becomes live exactly when the fleet grows past 7, which is when epochs are wanted anyway. (2) The coordinated fleet restart is ALREADY mandatory for the consensus-hash freeze (8872912), so the epoch change rides along at zero extra cost; reverting to 0 means paying that coordination twice. (3) 20 is a precondition for RequirePinnedCommittee, which is the actual goal. THE CHECKPOINT CONCERN IN THIS ROW WAS OVERSTATED AND IS RETRACTED: Checkpoint.Enabled defaults FALSE (config/settings/defaults.go:293) and no devnet config sets it, so the cadence branch is inert; when it IS enabled it is sequencer-only and documented as a best-effort side observer that "NEVER affects block production, consensus, or the append path". Moreover per-epoch IS what cadence_blocks: 0 is designed to mean — at epoch length 0 it degenerated to genesis-only, i.e. the BROKEN state, so 20 makes it work as specified. Setting CadenceBlocks explicitly would paper over a fix. RESIDUAL, and the real open item: the epoch-0 sentinel collision (see 8311504) must be closed before RequirePinnedCommittee can go true — either a jmdn genesis carve-out for SelectionPeriod 0, or move the seedNodes sentinel off 0 (cleaner, and cheap while nothing has been committed under the pinned scheme). Pinned by TestEpochIsDerivedFromTheBlockNotTheClock (e8abb82), which asserts the literal 20 — changing the default must update that test deliberately` |
| **D-57** | 3 | `jmdn` | **The D-31-tagged fix erases equivocation evidence.** `1624583` closes a real TOCTOU (a merge can re-create a key the compactor just deleted) by re-checking the watermark after the writes and, if it moved, calling `CRDTLayer.Delete(key)` — **the whole LWWSet, not just the elements this merge added** (`CRDTSyncHandler.go:860`). If that key already held a peer's genuine conflicting votes, the proof is destroyed before `ConvergeAndCompact`'s C5 pass evaluates it — the exact ordering `avc crdt/votes/converge.go` says the two steps were fused to prevent, and `CompactVotesBelowHeight`'s own doc warns against. `ReportEquivocation` never fires, the reputation event and metric are lost, and the operator log reports the deletion as a *successful defence*. The delete is unconditional on the watermark condition, so it does not require that anything merged: a peer can send `{"adds":{}}` and still trigger it, retrying near each watermark advance to make honest nodes erase proof of a victim's equivocation | — | S | `Open — GATED, not live. Unreachable at the default: VoteCRDTDualWrite = envOn("JMDN_VOTE_CRDT_V2", false) and compactConvergedVotes returns early when off, so the watermark never leaves 0 and the re-check never fires for height >= 1. BLOCKER before that flag is turned on — do NOT flip JMDN_VOTE_CRDT_V2 until this is closed. RECOMMENDED FIX (2026-09-15), in preference order: (1) DROP THE DELETE ENTIRELY. It is not load-bearing. CompactVotesBelowHeight collects the key on the next sweep regardless, and by construction that runs AFTER C5 evaluates the evidence — which is the ordering converge.go was fused to guarantee. The TOCTOU the delete was added to close is already covered: Watermark.Set is monotonic (CAS, refuses regression), every deletion in ConvergeAndCompact is preceded in program order by the Set that authorises it, and MemStore.Delete/AppendOp share one mutex — so a merge cannot resurrect a key past the sweep. The re-check earns a log line, not a delete. (2) If a delete is kept for hygiene, scope it to what THIS call added — LWWRemove the specific elements merged in the loop above — never the whole object. Either way add a two-goroutine regression test: pre-seed a peer''s conflicting votes at height H, advance the watermark between the merge write and the re-check, and assert ReportEquivocation STILL fires. Note the current code also deletes when merged == 0, so an empty {"adds":{}} from any peer triggers it — the test should cover that input too` |

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

> **FIXED, rev 5 re-verification.** `avc/randao/accumulator.go` now carries
> `mu sync.Mutex`, taken by `Fold`, `Count`, `Complete`, `Missing` and
> `Finalise`. `Expected` is deliberately lock-free with a comment saying why
> (`expected` is immutable after construction) — so do not "complete" the fix by
> adding a lock there. The prose precondition below was the defect; avc's own
> doc comment now asserts the opposite ("**SAFE FOR CONCURRENT USE**") and
> records that it used to say the reverse. **The root-cause analysis is retained
> because RC-3 is still the dominant pattern in this register — see D-50.**

The original finding, for the record. avc stated its precondition in prose, and the premise was false:

> `avc/randao/accumulator.go:106-108` — "Accumulator derives one epoch's entropy from block-declared reveals. It is NOT safe for concurrent use; **the block-application path is already serialised**."

jmdn's apply lock is keyed **per block hash** (`messaging/BlockProcessing/Processing.go:198` — `acquireBlockApplyLock(blockHash string)` → `blockApplyLocks[blockHash]` → `l.mu.Lock()`), so two *different* blocks apply in parallel. Both commit hooks call the fold:

- `messaging/broadcast.go:827` — the sequencer's own `ProcessBlockLocally`
- `messaging/blockPropagation.go:401` — the receive path (per-stream handler / pubsub goroutine)

`messaging/entropy_reveal.go:153` `foldBlockDeclaredReveals` calls `acc.Fold` at `:170`, **outside** the accumulator-store mutex — that mutex is released by `defer` when `entropyAccumulatorFor` returns at `:104`.

`grep -c "sync\." avc/randao/accumulator.go` → **0**. `Fold` mutates two shared fields:

| Line | Mutation | Failure mode |
|---|---|---|
| `accumulator.go:209` | `a.mix[i] ^= c[i]` | non-atomic read-modify-write on a 32-byte array → **silent** entropy divergence |
| `accumulator.go:211` | `a.folded[proposerID] = height` | **map write** → `fatal error: concurrent map writes` |

**Impact.** The map race is an unrecoverable Go runtime abort — `recover()` cannot catch it, so the validator process dies. The mix race is silent: a lost XOR leaves this node's entropy different from the fleet's, producing a different committee and a certificate everyone else rejects. **The quiet one is worse**, because it reproduces D-25's split-brain through a second, independent door.

**Evidence.**
```
$ cd avc && go test -race ./randao/
WARNING: DATA RACE
  Read at 0x… by goroutine 10:  runtime.mapaccess1_faststr()
    randao.(*Accumulator).Fold()  accumulator.go:200
  Previous write at 0x… by goroutine 11:
    randao.(*Accumulator).Fold()  accumulator.go:211
WARNING: DATA RACE
  Read/write at 0x… both at      accumulator.go:209
```

**Root cause.** A cross-repo precondition expressed only as a doc comment. avc cannot enforce "the caller serialises me", jmdn's authors had no compile-time or test-time signal, and the one tool that would have caught it — `go test -race` over the integrated path — has never run, because avc has no CI and jmdn's suite does not exercise the fold concurrently.

**Fix — code level.** Add a `sync.Mutex` to `Accumulator`; take it in `Fold`, `Finalise`, `Count`, `Expected`, `Complete`, `Missing`. `Fold` is one SHA-256 plus a 32-byte XOR, so the lock is uncontended and cannot become a throughput concern. Delete the `:107` precondition sentence — a type that guards itself does not need the caller to.

*Alternative if avc must stay lock-free:* hold `defaultEntropyAccumulatorStore.mu` across the whole fold loop in `foldBlockDeclaredReveals`. Smaller diff, but it leaves the landmine armed for the next caller. Prefer the mutex.

**Fix — design level.** Audit every avc type whose doc says "not safe for concurrent use" while relying on a jmdn invariant no build step checks. `beacon.Pipeline` carries the identical sentence at `avc/beacon/beacon.go:46` and every `VDFSealer` shares one instance across concurrent seal goroutines — I checked, and `Seal` only reads `group`/`difficulty` and calls the mutex-protected `sink.Publish`, so it *is* safe. **The comment is wrong in the other direction**, which will send the next reviewer either to add a needless lock or to serialise sealing and break the design. Make the comments match the code.

**Done when.** `go test -race ./randao/` is clean with the probe enabled; the probe is inverted into a permanent regression test; a race-enabled test applies two blocks concurrently through **both** commit hooks (that one test also covers D-31 and D-30's race); `beacon.go:46` is corrected.

---

### D-25 — `SeedSourceFor` silently degrades committee entropy to the Stage-1 salt

**Repo:** `jmdn` · **PoC:** 1 · **Live today**

```go
// messaging/committee_v2.go:437-441
func SeedSourceFor(epoch committee.EntropyEpoch) committee.SeedSource {
    if beacon := activeBeacon(); beacon != nil && beacon.Has(uint64(epoch)) {
        return beacon
    }
    return committee.SaltSource{Salt: stage1Salt()}   // ← silent fallback
}
```

avc forbids exactly this, in the imperative:

> `avc/committee/beacon.go:58` — "Callers **MUST** fail closed on it. Falling back to a default seed would let two nodes — one with the entropy, one without — seat different committees, which is worse than refusing the block."

`SelectEntropyCommittee` (`messaging/entropy_committee.go:131`) fails closed correctly on the same error, three files away — so this is a caller trading safety for liveness, not a misunderstanding of what the error means.

**Impact.** Two nodes seat different committees for the same epoch, so `n` and the threshold differ and one finalises a certificate the other rejects as unauthorised. Because the decision is taken per *lookup*, a node merely slow to receive a proof diverges for that epoch and silently re-converges afterwards — the hardest possible version of this to find in logs. It is also the amplifier for D-24's mix race, D-27's eviction, and any post-restart entropy loss: each of those turns into a silent divergence instead of a stall.

**Evidence.** PoC 1, same epoch and snapshot:
```
node WITH beacon entropy     -> [peerA peerG peerD peerE]
node WITHOUT (salt fallback) -> [peerD peerC peerJ peerG]
```

**Root cause.** RC-1 (§7.3): fail-closed contract, fail-open caller.

**Fix — code level.**
1. Change the signature to `SeedSourceFor(epoch) (committee.SeedSource, error)`. Return `ErrNoBeaconInstalled` when the beacon is nil and Stage 2 is configured; return the wrapped `ErrEntropyUnavailable` when the beacon exists but lacks the epoch.
2. Propagate through the caller at `messaging/committee_v2.go:279` so the block is refused, matching `Pipeline.Ready`'s documented contract.
3. Add `consensus.entropy_source: salt|beacon`, read once at startup and logged. **A cache miss must never be able to choose the entropy source.**

**Fix — design level.** Add the entropy source to `ValidateProductionConsensusPosture` (`messaging/production_posture.go:34`, which already gates `RejectLegacyVotes`, `EnforceCommitteeRegistry`, `EnforceBodyBinding`) so a mainnet node refuses to boot on `salt`. Export a `consensus_entropy_source{source="salt|beacon"}` gauge and alert when any node reports `salt` while the fleet reports `beacon` — one metric that makes this entire class of divergence observable, for every future gate too.

**Done when.** With a beacon installed but no entropy for the epoch, validation returns an error rather than a committee; PoC 1 is inverted to assert both nodes now fail *identically*; a mainnet-environment node with `entropy_source: salt` refuses to start, naming the reason; the gauge is visible on the devnet dashboard.

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

`publishBootstrapEntropy` (`Sequencer/beacon_bootstrap.go:79`) sorts ascending and publishes each epoch. `BeaconSource.Publish` calls `evictLocked` (`avc/committee/beacon.go:109`) on **every** insert, deleting every epoch below `newest - retain`. `retain` defaults to `committee.MinRetainedEpochs` = **3** (`Sequencer/beacon_install.go:245`, sink built at `:254`). **Nothing couples `retain` to `len(cfg.Consensus.EntropyBootstrap.Epochs)`** — `beacon_bootstrap.go` mentions retention only in a comment.

```
pinned=[0 1 2]          retain=3 -> LOST=[]
pinned=[0 1 2 3 4 5]    retain=3 -> LOST=[0 1]
pinned=[0…9]            retain=3 -> LOST=[0 1 2 3 4 5]
```

**Impact.** `bootstrapEpochs` (behind `IsBootstrapEpoch`, `:116`) is a **separate map that is never evicted**. So an evicted epoch keeps suppressing both the seal (`vdf_seal_wiring.go:100-108`) and the boundary proof (`Block/consensus_fields.go`) — nothing will ever produce its entropy — while `beacon.Has(e)` is false, which sends D-25 into the salt and makes `SelectEntropyCommittee` fail closed. **A permanent dead zone that reports itself healthy.**

**Why the existing test misses it.** `Sequencer/beacon_bootstrap_test.go`'s `TestPublishBootstrapEntropy_PublishesAllListedEpochsAndRecordsThem` uses `[]uint64{1, 0, 1}` against `retain = 3`. With `newest = 1 < retain = 3`, `evictLocked` returns at its first line. The test deliberately exercises unsorted and duplicate input — good instincts — but never a list *longer* than retention, which is the only shape that triggers this.

**Why the triggering shape is the likely one.** An entropy epoch is 50 slots and a seal is ~1200 s. Anyone sizing a real cold-start window will pin more than three epochs, and nothing signals that four behaves differently from three.

**Root cause.** RC-5 (§7.3): two collections representing one concept with different lifetimes. The comment at `beacon_bootstrap.go:75-78` reasons about eviction and concludes ascending order suffices — true for the out-of-order case it considers, false for the list-length case it does not.

**Fix — code level.**
1. Size retention from config in `InstallAVCBeaconFromEnv`, before `NewBeaconSource`:
   `retain = max(retainFromEnv, uint64(len(eb.Epochs)) + committee.MinRetainedEpochs)`
2. Make `publishBootstrapEntropy` verify its own work — after the loop, assert `sink.Has(e)` for every published epoch and return an error naming any that vanished. The code already documents a partial bootstrap set as "worse than none"; this makes it enforce that.
3. Add the missing test case: `len(epochs) > retain`, asserting every listed epoch survives, plus `IsBootstrapEpoch(e) == sink.Has(e)` for all `e`.

**Fix — design level.** Derive `IsBootstrapEpoch` from the config list rather than from what was successfully published — or better, delete the second map and read the config directly. Two sources of truth for one fact is the defect; the eviction is only how it surfaced. Separately, a sink whose job is durability should not silently discard a value the caller just handed it: have `Publish` refuse, or at minimum report, an insert that immediately evicts a previously published epoch.

**Done when.** Pinning 10 bootstrap epochs with default settings leaves all 10 retrievable via `EpochEntropy`; `IsBootstrapEpoch(e) == beacon.Has(e)` holds for every configured epoch (test); `publishBootstrapEntropy` errors rather than partially succeeding; PoCs 2 and 3 inverted.

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

`Sequencer/vdf_network_pins.go:33-46` ships `rsa-2048-testnet-ephemeral`. Separating network-owned pins from avc's library registry is the right architectural call — precisely so "avc never ships a devnet trapdoor as if it were a sourced constant" — and the disclosure is exemplary:

> "OPERATOR-GENERATED throwaway modulus… The generator knew the factors p,q at creation; the private key was shredded, but this is **NOT a trusted setup and NOT trapdoor-free**." … "**INSECURE BY CONSTRUCTION** … Whoever generated N can evaluate the VDF instantly and grind committee selection … **Never ship this group name in a mainnet config.**"

Everything in that warning is correct. The gap is that nothing enforces it. `ValidateProductionConsensusPosture` gates exactly three flags (`messaging/production_posture.go:34`) and neither the VDF group name nor the entropy source is among them; there is no `Network.Environment` check anywhere in `Sequencer/beacon_install.go` or `Sequencer/vdf_network_pins.go`. A node configured with this group name on mainnet installs the beacon cleanly, logs a warning, and runs.

Note the asymmetry: the *unpinned* path requires a loud opt-in env var (`JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS`) that logs a security finding on every startup, while the *pinned-but-trapdoored* path needs no override at all — correct for provenance, exactly backwards for trapdoor risk.

**Root cause.** RC-3 (§7.3), and its most consequential instance: the codebase's own history (a fabricated "RSA-2048" value shipped once, per `avc/vdf.NewRSAGroup`'s doc comment) is *why* pinning exists — and pinning now correctly prevents the **wrong** number while permitting a **known-bad** one.

**Fix — code level.** Add `MainnetSafe bool` to the network-pin record; refuse any pin with `MainnetSafe == false` when `settings.Get().Network.Environment` is mainnet, failing closed at install with the record's own `Note` text in the error — that text is already written and is exactly what an operator needs to read. Add the group name and entropy source to `ValidateProductionConsensusPosture` alongside D-25's gate.

**Fix — design level.** Make trust level a first-class property of a VDF group rather than prose in a `Note` field: **`sourced`** (citable primary source, e.g. an RSA Factoring Challenge modulus) / **`ceremony`** (multi-party, no single holder) / **`trapdoored`** (single generator knew the factors). Gate acceptance on environment × trust level. Today "we verified this is the right number" and "we know this number is unsafe" are different axes collapsed into one `ProvenanceRecord`.

**Done when.** A node with `network.environment: mainnet` and the testnet group name refuses to install the beacon, naming the reason; the posture check reports both values; a test asserts every pin in `networkVDFPins` carries an explicit trust level.

---

## 5. SEV-3 evidence

### D-30 — Bloom dedup filter is lock-free and saturates into a block-ingestion halt

**Repo:** `jmdn` · **PoC:** none · **Live today**

Two defects in one object.

**Race.** `messageFilter` (`messaging/blockPropagation.go:40`) is a `bits-and-blooms/bloom/v3` filter, which is not goroutine-safe. `.Test` at `:150` and `.Add` at `:155` take no lock, while reachable concurrently from the stream handlers (`node/node.go:204-206`), the pubsub goroutine (`messaging/blockgossip.go:71`), `admitZKBlock` and `broadcast.go`. `peerTimeoutMutex` sits at `:39` in the *same* `var` block — so this is an omission, not a single-threaded design.

**Saturation.** Sized at `:70` for 10,000 entries, never reset or rotated; entries are never removed by design. Recomputed from the constructor parameters: m = 95,851 bits (11.7 KiB), k = 7.

| Entries | False-positive rate |
|---|---|
| 10,000 | 1.00% |
| 25,000 | 29.24% |
| 50,000 | **83.19%** |
| 100,000 | **99.53%** |

Time to 50,000: **13.9 h** at 1 blk/s, 138.9 h at 1 blk/10s.

A false positive makes `HandleReceivedBlockMessage` discard a **valid** block as a duplicate *and* time out the honest sender for 20 s. Fleet-wide, self-inflicted, and it arrives inside a day at plausible block rates.

**Fix — code level.** `sync.RWMutex` (RLock for `Test`, Lock for `Add`), matching the convention one line above; `sync.Once` for initialisation instead of the racy nil check; rotate two filters — add to the active one, test both, swap when the active reaches design capacity.

**Fix — design level.** Dedup here is correctness-critical: a false positive rejects a valid block *and* punishes an honest peer. Use an exact structure bounded by finality depth (a ring of recent block ids) for the consensus path, and reserve the probabilistic filter for non-consensus message classes where a false positive is merely a dropped gossip. **Never let a probabilistic filter decide that a block is a duplicate.** Export cardinality and estimated FP rate for every bounded cache and alert on threshold crossings.

**Done when.** `-race` clean under a concurrent Test/Add workload; a test inserts 200,000 entries and asserts the effective FP rate stays under a stated bound; consensus-path dedup no longer depends on a probabilistic structure, or the rotation bound is documented and tested; cardinality metric exported.

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

> **FIXED, and its merge taught the register a lesson worth keeping.**
> `extractNodeID` now breaks a tie **lexicographically**
> (`timestamp == maxTS && node < maxNode`), so the winner no longer depends on
> map iteration order. PoCs 4 and 5 were written to PASS while the defect
> reproduced, so the fix made them fail — and because `avc/tests/audit` carries
> **no build tag**, it runs on a plain `go test ./...`. The PoCs arrived on
> `v3base` via avc PR #4 and the fix via PR #5, so the two met for the first
> time *on `v3base`*, where neither PR's own CI had them together: the branch
> went red on merge. **§0.2's "invert the PoC in the same commit as the fix" rule
> exists because of this event.**

`extractNodeID` (`avc/crdt/crdt.go:89`) starts `maxTS` at 0 and replaces only on **strictly** greater, so among entries tied at the maximum the winner is whichever key Go's randomised map iteration reaches first. That value feeds `deterministicMerge` (`:76`) at call sites `:209` and `:224`, reached whenever `Compare` returns 0 — which it does for **concurrent** clocks (`:54`), the common gossip case. `jmdn/crdt/crdt.go` is a byte-identical copy of the same logic.

```
distinct results over 400 byte-identical merges: 2   (233 / 167)
distinct serialised states over 300:             2   (173 / 127)
```

**Bound this precisely.** The stronger version of this claim was tested and is **false** — see §7.2. `A.Merge(B)` equals `B.Merge(A)`, and a targeted membership-flip construction returned 600/600 identical. The tie-break is order-stable when the two extracted ids differ, and the vote keyspace never calls `Remove`, so `Contains` cannot be swung today. **The defect affects the clock and the serialised bytes, not the tally** — which still matters for any digest-based reconciliation over CRDT state (`crdt/iblt`, `crdt/hashmap`).

**Fix — code level.** Make `extractNodeID` total: among entries tied at the maximum, return the lexicographically smallest node id. Two lines, and it makes `deterministicMerge` live up to its name. Fix the `nodeID1 == nodeID2` fallthrough at `crdt.go:83-86`, which returns `ts2` and so depends on argument order — resolve equal ids by comparing the clocks' canonical serialisation. Apply both to the jmdn copy, or delete that copy in favour of the avc one.

**Fix — design level.** "No `Remove` in the vote keyspace" is currently load-bearing and unenforced — it is the only reason this is not a tally bug. Make it structural: expose the vote store through an append-only interface with no `Remove` method, so the invariant is a compile-time property rather than a fact someone has to keep knowing.

**Done when.** 1000 merges of identical inputs yield exactly one result (test); `A.Merge(B)` and `B.Merge(A)` are byte-identical for tied *and* concurrent clocks; the vote store's public interface offers no `Remove`; the duplicate CRDT implementation is deleted or provably identical; PoCs 4 and 5 inverted.

---

### D-33 — Entropy genesis bootstrap (FIXED, with remainder)

**Repo:** `jmdn` · **Status:** `Fixed (b5e305a8) — remainder Open`

`Sequencer/beacon_bootstrap.go` (new, 129 lines) closes the gap that made the beacon impossible to start. For each operator-pinned epoch it publishes a deterministic value at install time:

```
ENTROPY-E(bootstrap) = SHA256( domain ‖ u64:chainID ‖ field:authorityPin ‖ field:seed ‖ u64:E )
```

It does the things that matter: binds to the pinned seed-authority key so two networks cannot share a schedule; takes the epoch set from config so every node agrees rather than deriving it from when a node started; refuses to bootstrap without an authority pin; fails closed on partial publish; and exempts bootstrap epochs from both sealing (`vdf_seal_wiring.go:100-108`) and the boundary-block proof requirement (`Block/consensus_fields.go`), which removes the "halt at every boundary" problem. It is honest at `:43-46` that the values are "public and computable by anyone in advance… grindable by construction", and logs a standing security finding at install.

**Remainder 1 — persistence.** `BeaconSource` is still `map[uint64][]byte` in memory (`avc/committee/beacon.go:64`); grep for persistence/hydrate hits returns **0**. A restart re-publishes the bootstrap epochs deterministically, but every epoch *sealed* since boot is lost — and D-25 converts that loss into a silent salt fallback rather than a stall. Back it with ThebeDB keyed by epoch, honour `MinRetainedEpochs`, hydrate on boot before the first block is validated.

**Remainder 2 — no observe rung.** Every feature gate here has exactly two states: off, where behaviour silently differs, and on, which is fail-closed and hard. Add a third — seal, publish and log the entropy and the committee it *would* seat, while selection still uses the configured source. Promotion to enforce then requires N epochs of zero fleet-wide divergence: evidence instead of a leap. Use it as the template for `JMDN_COMMITTEE_V2`, `JMDN_AVC_AGG_CERT`, `JMDN_COMMITTEE_SNAPSHOT_ANCHOR` and `JMDN_M2B_HASH`.

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
| 4 | Sequencer role derived from a *sync* flag: `isSequencer := !cfg.FastSync.EnableCatchup` | `main.go:1891` (was `:1869` at audit base `84d0c54f`; shifted by `7ed2ee02`); per-node overrides in compose | Correct today (env wins). Still fragile — introduce an explicit `consensus.role` and a startup assertion that exactly one sequencer is registered at the seed. |
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

- **The distinct `EntropyEpoch` type works.** It was hypothesised that the beacon is stored under slot epochs and looked up with block epochs, which would make `Has()` permanently false and the D-25 fallback permanent. It is not: `messaging/committee_v2.go:181` sets `committee.EntropyEpoch(EpochForSlot(b.Slot))` (cited as `:178` before rev 5 — the line drifted), and the named type (`avc/committee/seed.go`) exists precisely so a block-counted value cannot compile into that slot. `messaging/entropy_committee.go` then declines to reuse `committeeSnapshotFor` for the same reason. **This is RC-5's remedy already working at one junction — extend it to `SelectionPeriod` and the wall-clock epoch, which lack it.**
- **The one-epoch lag and ENTROPY-E indexing are correct.** `onEpochFinalised(closedEpoch)` seals for `closedEpoch + 1` (`Sequencer/vdf_seal_wiring.go:88`), matching `avc/beacon/beacon.go:92`'s requirement and the convention recorded at `messaging/entropy_committee.go:26-39` — which includes a written note of a previous off-by-one that was caught and fixed. Selection for epoch E cannot be seeded by epoch E's own reveals.
- **Quorum arithmetic, all four implementations — the FORMULA only.** Executed across n = 1…500: zero safety violations (`2q−n > f`), zero liveness violations (`q ≤ n−f`), zero disagreements between `avc/quorum`, `avc/bft`, `jmdn/AVC/BFT/bft` and `jmdn/messaging`. n=5→4, 7→5, 100→67, 101→68. Locked by `TestControl1`.
  **The formula was never the risk — the DENOMINATOR is.** `VerifyCertificate` takes it from the fleet-agreed authenticated committee and deliberately excludes the local blocklist (CON-12), so blocking can only make quorum *harder*. `verifyCertAndAggregate` did the opposite until D-36; it now uses `fleetCommitteeSnapshotFor`. **Any new quorum call site must be checked for its denominator, not its arithmetic.** Rev 5 re-verified: `ByzantineQuorum` is at `messaging/consensus_hardening.go:396` (cited as `:368-371` before rev 5 — that range is now `CommitteeKeyAuthorized`).
  *One latent divergence:* `jmdn ByzantineQuorum(n<1)` returns **1** while `avc Threshold` returns **0**. Both are guarded upstream; align them if either guard is ever removed. This has no register row, so it cannot be assigned — it is parked here under a heading that says "do not fix", which is the wrong home for a real arithmetic disagreement between two consensus implementations.
- **The test suites are green and race-clean.** `WORKDIR2/AUDIT-TRACKER.md` (the prior six-repo audit, D-1…D-23) states that no tests have been run anywhere in any repo. **That is out of date.** avc's `quorum`, `committee`, `crdt`, `crdt/votes`, `beacon`, `randao` and `vdf` all pass under `-race`, as do ThebeDB's `pkg/kv` and `pkg/checkpoint`; jmdn's own suite has since been run too, against a recorded baseline. **Every defect in this document survives a green suite** — that is the more useful finding, and it is why §0.4 says not to trust a green `go test ./...`.
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

Twenty-seven findings, five underlying causes. Each pattern has more than one instance, which is how it is known to be a pattern rather than a bug. **Fixing instances without fixing patterns will regenerate them — and RC-3 demonstrably did regenerate: it produced D-24, D-29 and D-32 in the first pass, then D-35 and D-36 in the PR #125 pass, in code written by people who had read this table.**

RC-2's instance list is also longer than it looks. Every one of these flags has exactly two states, off-and-silently-different or on-and-hard, with no observe rung: `COMMITTEE_V2`, `AGG_CERT`, `SNAPSHOT_ANCHOR`, `M2B_HASH`, **`CONSENSUS_HASH_V3`** (D-38 — and PR #129's answer is to flip its default, which is the failure mode RC-2 predicts), **`UNSIGNED_VALIDATOR_VOTES`**, and **`VOTE_CRDT_V2`** — that last one gates the *entire* avc v2 vote keyspace, including D-26's identity guard, so every vote hardening avc ships is inert until it flips.

| # | Pattern | Instances | Structural remedy |
|---|---|---|---|
| RC-1 | **Fail-closed contract, fail-open caller.** avc packages fail closed and say so imperatively; jmdn's callers were written to preserve liveness. At every seam, liveness silently won. | D-25 · D-26(b) · D-26(d) | Propagate errors across the seam. A default that trades safety for liveness must be a named config value with a startup warning, never a fallthrough. |
| RC-2 | **No shadow rung on the rollout ladder.** Every gate has two states: off, where behaviour silently differs, and on, fail-closed and hard. | D-33 remainder · `COMMITTEE_V2` · `AGG_CERT` · `SNAPSHOT_ANCHOR` · `M2B_HASH` | Add an `observe` state: compute the new value, log it beside the old, export a divergence metric, keep acting on the old. Promotion becomes evidence-driven. |
| RC-3 | **Preconditions in prose, not in types or tests.** Critical invariants stated in comments that no build step checks. **This is the dominant pattern in the register and the direct cause of both SEV-1s found in the PR #125 pass.** | D-24 ("already serialised" — false) · D-31 ("Start at most once" — false) · D-29 ("never on mainnet") · D-32 ("no Remove") · **D-35 ("the override never waives a network pin" — it did, for any name the pin table did not list)** · **D-36 ("a local blocklist can never shrink n" — the comment named `eligibleMembers` as the thing to avoid, then reached the same filter through `committeeSnapshotFor`)** · **§7.1 bullet 3 itself, which asserted the denominator property of one call site as a property of the codebase and thereby masked D-36** · 3 stale "NOT WIRED" comments · D-50 (the row that now tracks this class) | Where a precondition can be enforced, enforce it (a mutex, a distinct type, an unexported constructor). Where it cannot, write the test that fails when it is violated. **A comment is not a mechanism — and a comment that names the wrong mechanism to avoid is worse than none, because it tells the next reader not to check.** |
| RC-4 | **Recursive design with no base case.** Steady state was designed; epoch zero was not. | D-33 (now fixed) · `linkageDecision` rejects every block at `localTip == 0` | Every recursive protocol value needs a genesis provision decided alongside the recurrence, plus persistence so a restart is not a fresh base case. |
| RC-5 | **Two collections, one concept, different lifetimes.** | D-27 (`bootstrapEpochs` vs `entropy`) · D-34 (`seenHeights` vs `EquivocationStore`) | One source of truth. Where a cache mirrors a store, derive it or make the divergence impossible to represent. |

**Comments that assert the negation of the code**, to be fixed opportunistically. `Sequencer/vdf_sealer.go`, `messaging/entropy_reveal.go` and `messaging/entropy_committee.go` all claim there is no production caller for things `Sequencer/beacon_install.go` demonstrably calls — `messaging.SetBeaconSource` has had a live caller since before this audit, and it is gated by *environment configuration*, not by caller absence. `avc/randao/fallback_aggsig.go` still declares blocker B1 open; `messaging/entropy_aggsig.go` closed it. And avc's own PoC header still asserts that *this document* "does not exist in the jmdn repo — that handover was never committed", which was true when written and false since PR #123; it also still says the race probe "should be deleted" though `b11b686` deleted it.

**D-50 is the row that tracks this class, and it has an evidence gap.** D-50 claims six such comments; the enumeration died with the deleted PR #125 working document, and the lists in this section total at most five. **Re-derive the six from `6eb0cc7` before working that row** — the two that mattered are already named in RC-3 above (D-35's and D-36's).

---

## 8. Remediation order

Revised rev 5. Struck-through rows are `Fixed`; they are kept so the ordering argument stays legible.

```
GATE 1 — must land before the beacon is enabled
  D-27  jmdn  retention derived from the pinned list + self-verify      S  ← ONLY REMAINING
  D-39  jmdn  make T a chain parameter, not a per-host env var          M  ← ADD: silent divergence
  ~~D-24  avc   Accumulator mutex~~                        FIXED 498471b / v3base.3

GATE 2 — safety, parallelisable, no dependency on Gate 1
  D-25  jmdn  SeedSourceFor fails closed + posture gate + metric        S
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
  D-33a jmdn  persist BeaconSource in ThebeDB, hydrate on boot          M
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
  commit THEBE-AUDIT-HLD.md into jmdn/audits/ — 52 source files
  cite its IDs and it is not in any repo; it also holds the
  unfixed CRITICAL API-10 (batch JSON-RPC, zero recover())
```

**Suggested first assignments.** **D-27** immediately — it is **S** and it is now the only thing gating entropy enablement. Pair it with **D-39**, because a fleet that enables the beacon with per-host `T` values diverges silently and no test will catch it. **D-38** is the next-cheapest real safety win and it needs a decision, not code: reject PR #129's default flip, add the two flags to the posture check instead. **D-30** remains a good independent starter but is only half-done — finish `DIDPropagation.go` by copying `ContractPropagation.go`'s own `sync.RWMutex` pattern from the same package, which drops it from M to S. **D-26** needs the most senior reviewer and should be split; start with the legacy ingest path (a), since that is where the defect actually lives.

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

> **Read the result correctly — the suite is no longer uniform.** PoCs 1-3 pass **because D-25/D-27 are still present**. PoCs 4-5 pass **because the D-32 fix holds** — they were inverted in avc `b11b686`. The two Controls and two Negatives must always pass. This package carries **no build tag**, so it runs on a plain `go test ./...`: a fix landing without its PoC inversion turns avc's whole suite red, which is exactly what happened when avc #4 and #5 met on `v3base`. That is why §0.2 requires the inversion in the same commit as the fix.

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

**Findings with no PoC — 23 of 32 rows:** D-28, D-30, D-31, D-33, D-34, and all of D-38…D-55. Each needs a running node, a two-node harness, or arrived after this suite was frozen. Their "Done when" clauses describe the test to write; §0.2 rule 2 applies to those descriptions.

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

- **jmdn's own build and test suite — PARTIALLY CLOSED since rev 4.** The original sandbox filled its filesystem pulling jmdn's dependency graph (libp2p, go-ethereum, duckdb, pgx), so D-25…D-34's jmdn rows are source-traced at a specific line, **not compiler-verified**. That was the audit's main weakness. It is now mixed: D-29/D-35/D-36/D-37 each ship a named jmdn test that has been **executed**, and D-38…D-55 were source-traced then re-verified against `v3base@291d44c`.
  Close the remainder with: `cd jmdn && GOWORK=off go build ./... && GOWORK=off go test -race ./messaging/... ./Sequencer/... ./Security/...`
  **Use `GOWORK=off`** — a `.go.work.local` from `make dev-workspace` silently substitutes sibling checkouts for the pinned tags, so a green run under a workspace proves nothing about what the fleet builds.
  **And judge against the baseline, not against zero:** ten tests fail identically on `v3base` itself (§7.1). Six of them fail *only* under `-race`.
  Note that `Sequencer/beacon_bootstrap_test.go` passes today while missing D-27, so add the `len(epochs) > retain` case before trusting a green run there.
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

**Drift check performed 2026-09-04.** `jmdn/origin/v3base` had advanced
`84d0c54f` → `dda7c4a9` (3 commits) between the audit and delivery:

```
dda7c4a9  Merge pull request #122 from feat/thebe-sc-avc-a3
f25d8f34  Merge pull request #118 from fix/consensus-reward-source-startup-wiring
7ed2ee02  fix(consensus): wire reward-address source at startup, not mid-request
```

Files changed: `Sequencer/consensus_statemachine.go` (+82/−27) and `main.go` (+22).
The change moves `SetCommitteeEligibilitySource` / `SetRewardAddressSource`
wiring from mid-request to startup.

**Impact on the findings: one line number, nothing substantive.**

- Cross-checked all 15 files cited by D-25 … D-34: **none of them changed.**
  Every finding stands at its cited line.
- `main.go:1869` → **`main.go:1891`** (`isSequencer :=`). Corrected in §6.
  It is a SEV-4 observation, not a blocker.
- `84d0c54f` is an ancestor of `dda7c4a9`, so the audit branch rebases
  fast-forward with no conflicts.

**Not checked:** `avc`, `ThebeDB` and `jmdt-devnet` drift. Those three use SSH
remotes and the auditor's environment could not authenticate; only `jmdn` (HTTPS)
was fetchable. Run `git fetch && git status` in each before merging.

`avc` moved twice during this audit (`aba96c7` → `e78b98e` → `1c13324`) and jmdn's module graph changed shape once: local `replace` directives were dropped in favour of pinned tags, so jmdn now builds `avc v0.1.0-v3base.2` = commit `fd5eef8`, whose tree is **identical** to `1c13324` (verified by empty diff). Also note that `jmdt-devnet/dockerfiles/jmdn.gate.Dockerfile:46-77` re-injects local `replace` directives, so the devnet container builds from sibling directories rather than the pinned tags — they agree today, and nothing enforces that they keep agreeing (§6).

Re-pin before acting on anything here:

```bash
for r in jmdn avc ThebeDB jmdt-devnet; do
  printf '%-14s %-28s %s\n' "$r" \
    "$(git -C $r branch --show-current)" "$(git -C $r rev-parse --short HEAD)"
done
```

**Fastest staleness check:** `cd avc && go test ./tests/audit/ -v`. Nine passes means the findings stand.
