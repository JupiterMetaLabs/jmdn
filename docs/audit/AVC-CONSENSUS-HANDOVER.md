# AVC Consensus — Audit Handover Document

| | |
|---|---|
| **Status / Verdict** | **No-Go for mainnet.** No-Go for enabling Stage 2 entropy until D-24 and D-27 are fixed with regression evidence. Go for continued devnet use — but only because the devnet sets no VDF environment variables. |
| **Date** | 2026-09-03 (three passes consolidated; rev 3 re-derived from `v3base` after all four repos moved) |
| **Scope** | Audited at `jmdn@84d0c54f` (v3base) · `avc@1c13324` (v3base) · `ThebeDB@6b8f6d1` (v3base) · `jmdt-devnet@fd52106` (`main` — **no `v3base` branch exists in that repo**). **Re-based to `jmdn@dda7c4a9`** on 2026-09-04 — see §C.2 for the drift check; all findings re-verified, one line number corrected. Entropy pipeline end-to-end, committee selection seam, vote ingest and BLS signing path, block-hash preimages, CRDT merge determinism, quorum arithmetic (all four implementations), devnet config vs code expectations. |
| **Method** | Full source trace → Go 1.26.0 toolchain installed in a clean sandbox → `go vet` + `go test -race` across avc's consensus packages (all green) → **9 proof-of-concept tests written and executed** → Go race detector on the reveal-fold path → viper precedence reproduced by execution to settle two config claims → re-derivation from `v3base` after the repos moved mid-audit. |
| **Companion file** | `avc/tests/audit/audit_poc_test.go` — executable evidence. PoCs **PASS while a defect exists**; invert each assertion when fixing to convert it into a regression test. D-24 lives in `avc/randao/zz_race_probe_test.go` because it needs unexported access and `-race`. |
| **Reproduce** | `cd avc && go test ./tests/audit/ -v` (9 PoCs, ~10s, all PASS today) · `cd avc && go test -tags defects -race -run TestAccumulatorFoldRace ./randao/` (D-24, expect two DATA RACE reports) · `cd avc && go test -race ./quorum/... ./committee/... ./crdt/... ./beacon/... ./randao/... ./vdf/...` (baseline, clean) |
| **Prior audits** | Continues `D-1…D-23` from the 2026-08-31 reports in `WORKDIR2/audits/`. New findings are **D-24…D-34**. Never renumber. |

**How to collaborate on this document:** the Findings Register (§2) is the living part — update the Status column there (rules in §2.1). Everything below §2 is the evidence body; append corrections rather than rewriting history. New audit passes append new finding IDs.

**Cross-repo note:** 7 of 11 findings are `jmdn` code, 2 are `avc`, 2 are `jmdt-devnet`. This document lives in `jmdn` because that is where most fixes land; the PoC suite lives in `avc` because that is the only module it compiles in. The register's Repo column says who owns each row.

---

## 0. Handover runbook — START HERE

**Current state (2026-09-03):** two branches carry exactly three artifacts.

| Branch | Repo | Contains |
|---|---|---|
| `audit/2026-09-03-consensus` | `jmdn` | this document (`docs/audit/AVC-CONSENSUS-HANDOVER.md`) |
| `audit/consensus-2026-09` | `avc` | `tests/audit/audit_poc_test.go` · `randao/zz_race_probe_test.go` |

`v3base` is protected by an **org-level ruleset**, so both branches reach it by
**pull request** — one PR per repo, targeting `v3base`, never `main`. Only
`squash` and `rebase` merges are configured. Nothing was pushed directly to
`v3base`.

### 0.1 Reviewer steps

1. Check out both branches:
   ```bash
   git -C jmdn checkout audit/2026-09-03-consensus
   git -C avc  checkout audit/consensus-2026-09
   ```
2. Reproduce the evidence yourself (~15s):
   ```bash
   cd avc && go test ./tests/audit/ -v
   ```
   All 9 PoCs **PASS = the defects are present** (Appendix B maps each PoC to its finding). Then the one that needs the race detector:
   ```bash
   cd avc && go test -tags defects -race -run TestAccumulatorFoldRace ./randao/
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
5. Assign an owner to each of the 3 SEV-1 rows. **D-24 and D-32 live in `avc`, not this repo** — they need an owner there.
6. Housekeeping (Appendix A.2) — **already done**, nothing to do.
7. **Merge both PRs into `v3base` once every SEV-1 and SEV-2 row has an owner** and there is no unresolved disagreement. Do not hold the merge for the *fixes* — only for ownership. The register is a living table and must sit on `v3base`, because the §0.2 fix rule requires the register row to be flipped in the *same commit* as the code change, and that is impossible while the register lives on a side branch.

### 0.2 Fix rules

Every fix — whatever branch it lands on — must contain, **atomically in the same commit**:

1. the code change;
2. the corresponding PoC assertion in `avc/tests/audit/audit_poc_test.go` **inverted** — the defect proof becomes the regression test. Each PoC's failure message already names what to do;
3. the §2.2 register row flipped to `Fixed (<commit>, <test name>)`.

A fix missing (2) or (3) is not done. Four findings have no PoC (D-26, D-28, D-30, D-31, D-34) because they need a running node or a two-node harness — for those, rule (2) means **write the test the finding's "Done when" clause describes.**

### 0.3 Fix-ordering warning

**D-24 must land before the beacon is enabled, and that ordering is the single most important sentence in this document.**

D-33 (entropy genesis bootstrap) shipped shortly before this audit and is a good fix. It also made the beacon *reachable for the first time*, which put the reveal-fold path into play — and that path is guarded by an accumulator with no synchronisation (D-24). Before D-33, `entropyAccumulatorFor` always failed and `Fold` never ran. That accident is gone.

```
D-24 (avc)  +  D-27 (jmdn)   ──────►   enabling Stage 2 entropy
                                       JMDN_AVC_VDF_MODULUS_HEX
                                       JMDN_AVC_VDF_GROUP_NAME
                                       JMDN_AVC_VDF_DIFFICULTY_T
```

Enabling the beacon before D-24 converts a silent divergence into a validator crash loop (`fatal error: concurrent map writes` is unrecoverable). Enabling before D-27 silently discards pinned bootstrap epochs. **D-25 should also land first**, so that a missing entropy value stalls loudly instead of diverging quietly.

Everything else in the register is independent and parallelisable.

### 0.4 Release gate

Flip the verdict at the top of this document to **Go** only when every SEV-1 and SEV-2 row in §2.2 reads `Fixed`. Until then:

- do **not** set `JMDN_AVC_VDF_MODULUS_HEX` / `_GROUP_NAME` / `_DIFFICULTY_T` on any fleet;
- do **not** ship `rsa-2048-testnet-ephemeral` to any network with adversaries (D-29 — it is trapdoored by construction and nothing mechanically stops it);
- do **not** trust a green `go test ./...`. Every finding in this document survives a green suite; that is the single most useful fact here.

### 0.5 Acceptance tests & pass criteria

Today the PoC suite **passes while defects exist**. As each fix lands, its assertion is inverted (§0.2 rule 2); once all rows are done, `go test ./tests/audit/ -v` passing means **defects absent** — the suite's meaning flips from proof-of-defect to regression gate.

Two PoCs are **controls** and must pass forever, before and after every fix: `TestControl1_QuorumIsByzantineSafeAtEverySize` (the quorum maths is correct — see §7) and `TestControl2_BeaconSourceFailsClosed` (avc's sink is correct; D-25 is the caller swallowing its error).

Two are **negative checks** recording claims that were tested and did *not* reproduce: `TestNegative1_MergeIsCommutativeForDistinctMaxima` and `TestNegative2_NondeterminismDoesNotFlipMembership`. They exist so those claims are not re-litigated. **If either ever fails, a real regression has occurred** — the failure message says which finding to reopen.

---

## 1. Verdict

```
DECISION: No-Go for mainnet. No-Go for enabling Stage 2 entropy in any order
          that does not fix D-24 first.

          The entropy pipeline moved from "cannot start" to "can start
          unsafely" — which is a worse resting state, not a better one.

SEV-1:  3  — D-24 accumulator race · D-25 silent salt fallback · D-26 vote ingest
SEV-2:  3  — D-27 bootstrap eviction · D-28 hash binding · D-29 trapdoored pin
SEV-3:  4  — D-30 bloom filter · D-31 watermark TOCTOU · D-32 merge nondeterminism
             · D-34 unbounded maps
SEV-3/4 devnet: 9 — §6

Fixed since 2026-08-31:   1 — D-33 (entropy genesis gap). Good fix; armed D-24.
Withdrawn after testing:  4 — §7.2
Verified sound:           4 — §7.1, including the quorum arithmetic

Audited:  entropy pipeline end-to-end (randao → VDF → beacon → committee);
          committee selection seam; vote ingest and BLS signing; block-hash
          preimages; CRDT merge determinism; quorum arithmetic (4 impls);
          devnet config vs code expectations.
NOT inspected: jmdn/AVC/BFT (divergent vendored fork of avc/bft — the original
          was audited, the fork was not); libp2p pubsub signing configuration;
          ThebeDB beyond pkg/kv and pkg/checkpoint; MRE; seedNodes.
NOT executed: jmdn's own build and test suite — the audit sandbox exhausted its
          filesystem on jmdn's dependency graph (libp2p, go-ethereum, duckdb,
          pgx). All jmdn findings are source-traced, not compiler-verified.
          This is the audit's main weakness. See Appendix C.1.
```

**The one-sentence summary.** Consensus is wired. The *new AVC protocols* largely are not — and the gap is not "unfinished" but "finished, plumbed, and until recently unreachable"; the fix that made them reachable also armed the most serious defect.

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
| **D-24** | 1 | `avc` | `randao.Accumulator` has no synchronisation; jmdn calls `Fold` from two concurrent commit hooks | race probe | S | `Open` |
| **D-25** | 1 | `jmdn` | `SeedSourceFor` silently falls back to the Stage-1 salt; two nodes seat different committees | PoC 1 | S | `Open` |
| **D-26** | 1 | `jmdn` | Vote CRDT keyed on a self-declared sender; requester chooses the signing target; guard off and unwired | — | L | `Open` |
| **D-27** | 2 | `jmdn` | Bootstrap epochs silently evicted when the pinned list exceeds `retain` | PoC 2, 3 | S | `Open` |
| **D-28** | 2 | `jmdn` | `ConsensusHash` binds neither `PrevHash` nor `BlockNumber`, even under M2b | — | M | `Open` |
| **D-29** | 2 | `jmdn` | Trapdoored testnet VDF modulus with no mechanical mainnet guard | — | S | `Open` |
| **D-30** | 3 | `jmdn` | Bloom dedup filter is lock-free and saturates to 83% FP in ~14h | — | M | `Open` |
| **D-31** | 3 | `jmdn` | Epoch watermark TOCTOU duplicates finalisation; duplicate seal blocks a goroutine forever | — | M | `Open` |
| **D-32** | 3 | `avc` | `extractNodeID` tie-break reads a Go map → nondeterministic merge | PoC 4, 5 | S | `Open` |
| **D-33** | — | `jmdn` | Entropy genesis bootstrap — **shipped**; persistence and an observe rung remain | — | M | `Fixed (b5e305a8) — remainder Open` |
| **D-34** | 3 | `jmdn` | Unbounded maps on the block-receive path (`seenHeights` et al) | — | M | `Open` |

**Devnet items** (§6) are tracked as a group rather than individually numbered: 3 × SEV-3, 6 × SEV-4, all in `jmdt-devnet`.

---

## 3. SEV-1 evidence

### D-24 — `randao.Accumulator` is unsynchronised; the block-apply path is not serialised

**Repo:** `avc` · **PoC:** `randao/zz_race_probe_test.go` · **Blocks:** enabling the beacon

avc states its precondition in prose, and the premise is false:

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
$ cd avc && go test -tags defects -race -run TestAccumulatorFoldRace ./randao/
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

**Repo:** `jmdn` · **PoC:** none (needs a running node) · **Live today** · **Effort:** L, splittable

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

**Repo:** `jmdn` · **PoC:** none · **Effort:** S

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

**Repo:** `avc` · **PoC:** 4, 5

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

- **The distinct `EntropyEpoch` type works.** It was hypothesised that the beacon is stored under slot epochs and looked up with block epochs, which would make `Has()` permanently false and the D-25 fallback permanent. It is not: `messaging/committee_v2.go:178` sets `EntropyEpoch(EpochForSlot(b.Slot))`, and the named type (`avc/committee/seed.go:41`) exists precisely so a block-counted value cannot compile into that slot. `messaging/entropy_committee.go:136-149` then declines to reuse `committeeSnapshotFor` for the same reason. **This is RC-5's remedy already working at one junction — extend it to `SelectionPeriod` and the wall-clock epoch, which lack it.**
- **The one-epoch lag and ENTROPY-E indexing are correct.** `onEpochFinalised(closedEpoch)` seals for `closedEpoch + 1` (`Sequencer/vdf_seal_wiring.go:88`), matching `avc/beacon/beacon.go:92`'s requirement and the convention recorded at `messaging/entropy_committee.go:26-39` — which includes a written note of a previous off-by-one that was caught and fixed. Selection for epoch E cannot be seeded by epoch E's own reveals.
- **Quorum arithmetic, all four implementations.** Executed across n = 1…500: zero safety violations (`2q−n > f`), zero liveness violations (`q ≤ n−f`), zero disagreements between `avc/quorum`, `avc/bft`, `jmdn/AVC/BFT/bft` and `jmdn/messaging`. n=5→4, 7→5, 100→67, 101→68. The denominator comes from the authenticated committee, never from votes received (`messaging/consensus_hardening.go:466-471`) — which matters more than the formula. Locked by `TestControl1`.
  *One latent divergence:* `jmdn ByzantineQuorum(n<1)` returns **1** (`consensus_hardening.go:368-371`) while `avc Threshold` returns **0**. Both are guarded upstream; align them if either guard is ever removed.
- **The test suites are green and race-clean.** `WORKDIR2/AUDIT-TRACKER.md:370-374` states that no tests have been run anywhere in any repo. **That is now out of date.** avc's `quorum`, `committee`, `crdt`, `crdt/votes`, `beacon`, `randao` and `vdf` all pass under `-race`, as do ThebeDB's `pkg/kv` and `pkg/checkpoint`. **Every defect in this document survives a green suite** — that is the more useful finding.
  *Coverage gaps:* ThebeDB's `internal/merkle`, `pkg/eventlog` and `pkg/eventlog/wal` have **no test files at all** — a Merkle tree and a write-ahead log.

### 7.2 Claims tested and withdrawn

Recorded so they are not re-litigated. Two are locked by negative PoCs (§0.5).

| Claim | How it was tested | Outcome |
|---|---|---|
| CRDT `LWWSet.Merge` is non-commutative | Wrote the test | **Withdrawn.** `A.Merge(B)` equals `B.Merge(A)`; the tie-break is order-stable when the extracted node ids differ. Locked by `TestNegative1`. |
| Merge nondeterminism flips vote-set membership | Targeted construction, 600 iterations | **Withdrawn.** 600/600 identical. Cannot happen while the vote keyspace has no `Remove`. Locked by `TestNegative2`. |
| Devnet stalls permanently at tip 0 (mounted yaml `fastsync.enabled: false` beating env `true`) | Rebuilt `loader.go`'s exact viper v1.21.0 sequence and executed it | **Withdrawn.** Env wins for bools and durations. Was rated Critical in an earlier pass; downgraded to SEV-4 documentation drift. |
| All five devnet nodes self-identify as sequencer | Same execution | **Withdrawn.** Env wins, so node-1's `false` and the validators' `true` both apply. Role assignment is correct today; the fragility remains as a SEV-4. |

### 7.3 Root-cause patterns

Eleven findings, five underlying causes. Each pattern has more than one instance, which is how it is known to be a pattern rather than a bug. **Fixing instances without fixing patterns will regenerate them.**

| # | Pattern | Instances | Structural remedy |
|---|---|---|---|
| RC-1 | **Fail-closed contract, fail-open caller.** avc packages fail closed and say so imperatively; jmdn's callers were written to preserve liveness. At every seam, liveness silently won. | D-25 · D-26(b) · D-26(d) | Propagate errors across the seam. A default that trades safety for liveness must be a named config value with a startup warning, never a fallthrough. |
| RC-2 | **No shadow rung on the rollout ladder.** Every gate has two states: off, where behaviour silently differs, and on, fail-closed and hard. | D-33 remainder · `COMMITTEE_V2` · `AGG_CERT` · `SNAPSHOT_ANCHOR` · `M2B_HASH` | Add an `observe` state: compute the new value, log it beside the old, export a divergence metric, keep acting on the old. Promotion becomes evidence-driven. |
| RC-3 | **Preconditions in prose, not in types or tests.** Critical invariants stated in comments that no build step checks. | D-24 ("already serialised" — false) · D-31 ("Start at most once" — false) · D-29 ("never on mainnet") · D-32 ("no Remove") · 3 stale "NOT WIRED" comments | Where a precondition can be enforced, enforce it (a mutex, a distinct type, an unexported constructor). Where it cannot, write the test that fails when it is violated. **A comment is not a mechanism.** |
| RC-4 | **Recursive design with no base case.** Steady state was designed; epoch zero was not. | D-33 (now fixed) · `linkageDecision` rejects every block at `localTip == 0` | Every recursive protocol value needs a genesis provision decided alongside the recurrence, plus persistence so a restart is not a fresh base case. |
| RC-5 | **Two collections, one concept, different lifetimes.** | D-27 (`bootstrapEpochs` vs `entropy`) · D-34 (`seenHeights` vs `EquivocationStore`) | One source of truth. Where a cache mirrors a store, derive it or make the divergence impossible to represent. |

**Three doc comments assert the negation of the code** and should be fixed opportunistically: `Sequencer/vdf_sealer.go:10-17`, `messaging/entropy_reveal.go:38-39` and `messaging/entropy_committee.go:55-59` all claim there is no production caller for things `Sequencer/beacon_install.go:245-247` demonstrably calls. `avc/randao/fallback_aggsig.go:59-72` still declares blocker B1 open; `messaging/entropy_aggsig.go:194` closed it.

---

## 8. Remediation order

```
GATE 1 — must land before the beacon is enabled
  D-24  avc   Accumulator mutex + race-enabled two-block test          S
  D-27  jmdn  retention derived from the pinned list + self-verify      S

GATE 2 — safety, parallelisable, no dependency on Gate 1
  D-25  jmdn  SeedSourceFor fails closed + posture gate + metric        S
  D-26  jmdn  authenticate ingest boundary; stop signing caller input   L
  D-28  jmdn  bind PrevHash + BlockNumber into ConsensusHash            M
  D-29  jmdn  refuse trapdoored pins on mainnet                         S

GATE 3 — entropy enablement (needs Gate 1)
  D-33a jmdn  persist BeaconSource in ThebeDB, hydrate on boot          M
  D-33b jmdn  observe rung: seal + log + divergence metric              M
  D-33c        enforce — only after N epochs of zero fleet divergence

GATE 4 — independent, any time
  D-30  jmdn  lock + rotate the bloom filter                            M
  D-31  jmdn  watermark CAS + Start idempotency + evict vdfSealers       M
  D-32  avc   total tie-break in extractNodeID                          S
  D-34  jmdn  prune maps below finality depth                           M
        devnet secrets · limits · logs · CI on avc + ThebeDB           §6

GATE 5 — structural (RC remedies, after the instances)
  distinct types for the remaining two epoch clocks              (RC-5)
  append-only vote-store interface                               (RC-3/D-32)
  bounded-map convention at every declaration site               (RC-3/D-34)
  observe rung retrofitted to the other four feature gates        (RC-2)
```

**Suggested first assignments.** D-24 and D-27 to one owner each, immediately — they are both **S** and they gate everything entropy-related. D-30 is a good independent starter: self-contained, testable, and it fixes a defect that is live today rather than latent. D-26 needs the most senior reviewer and should be split (a+d first).

---

## Appendix A — Repos touched, and what to commit

### A.1 Footprint of this audit

| Repo | Branch | Commit | Files added | `v3base` / `main` touched? |
|---|---|---|---|---|
| `jmdn` | `audit/2026-09-03-consensus` | `f490147` + this doc-fix commit | `docs/audit/AVC-CONSENSUS-HANDOVER.md` (this file) | **No** — PR only |
| `avc` | `audit/consensus-2026-09` | `e97ac50` | `tests/audit/audit_poc_test.go` · `randao/zz_race_probe_test.go` | **No** — PR only |
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

`avc/randao/zz_race_probe_test.go` is **not** a leftover — it is the D-24 PoC.
It is build-tagged `defects`, so it does not affect normal runs. Rename it if
you prefer a clearer name.

### A.3 State of delivery — committed, awaiting PR

Both branches are committed. `v3base` is org-protected, so each reaches it by
pull request.

| Repo | Branch | Commits ahead of its base | Base |
|---|---|---|---|
| `avc` | `audit/consensus-2026-09` | 1 — `e97ac50` | `v3base` @ `1c13324` |
| `jmdn` | `audit/2026-09-03-consensus` | 2 — `f490147` + doc corrections | `origin/v3base` @ `dda7c4a9` |

**Remaining operator steps:**

```bash
# 1. push both branches
cd /Users/naman/JM/repos/WORKDIR2/avc  && git push -u origin audit/consensus-2026-09
cd /Users/naman/JM/repos/WORKDIR2/jmdn && git push -u origin audit/2026-09-03-consensus

# 2. open one PR per repo, BASE = v3base (never main)
#    gh, if installed:
cd /Users/naman/JM/repos/WORKDIR2/avc
gh pr create --base v3base --head audit/consensus-2026-09 \
  --title "test(audit): PoC suite for D-24..D-34" \
  --body "Companion to jmdn PR. Nine PoCs; each PASSES while its defect exists. Test-only: no production code, no go.mod change, no new avc tag required."

cd /Users/naman/JM/repos/WORKDIR2/jmdn
gh pr create --base v3base --head audit/2026-09-03-consensus \
  --title "docs(audit): AVC consensus handover, findings D-24..D-34" \
  --body "No-Go for mainnet; No-Go for enabling Stage 2 entropy before D-24 and D-27. Living findings register in section 2.2. Companion PoC suite in avc@audit/consensus-2026-09."
```

**Before merging, confirm `avc` has not drifted.** The auditor could not fetch
it — `avc`, `ThebeDB` and `jmdt-devnet` use SSH remotes and the audit
environment had no key; only `jmdn` (HTTPS) was verifiable.

```bash
cd /Users/naman/JM/repos/WORKDIR2/avc
git fetch origin v3base && git log --oneline v3base..origin/v3base
# empty  -> e97ac50 sits on a current base, merge freely
# output -> rebase onto origin/v3base, re-run the PoC suite, then merge
```

**No avc tag or jmdn `go.mod` change is needed for these two PRs.** The avc
commit adds only `_test.go` files, Go never compiles a dependency's test files,
and jmdn carries no `replace` directive — it builds avc from the module cache at
`v0.1.0-v3base.2` (= `fd5eef8`). A new tag *will* be required when D-24 and D-32
land, because those touch avc production code; note that on those rows when you
assign them.

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

`avc/tests/audit/audit_poc_test.go`, run with `go test ./tests/audit/ -v`. **All 9 pass today; passing means the defect is present.**

| Test | Finding | Asserts (today) |
|---|---|---|
| `TestPoC1_SilentSaltFallbackSeatsDifferentCommittees` | D-25 | two nodes seat different committees for one epoch |
| `TestPoC2_BootstrapEpochsSilentlyEvicted` | D-27 | pinning > `retain` epochs loses the earliest |
| `TestPoC3_BootstrapSetDivergesFromEntropySet` | D-27 | epochs marked bootstrapped have no entropy |
| `TestPoC4_MergeTieBreakIsNondeterministic` | D-32 | 400 identical merges → 2 distinct results |
| `TestPoC5_MergeProducesDivergentSerialisedState` | D-32 | 300 identical merges → 2 distinct serialised states |
| `TestControl1_QuorumIsByzantineSafeAtEverySize` | §7.1 | **control** — quorum correct, n=1…500. Must pass forever. |
| `TestControl2_BeaconSourceFailsClosed` | §7.1 | **control** — avc's sink is correct. Must pass forever. |
| `TestNegative1_MergeIsCommutativeForDistinctMaxima` | §7.2 | **negative** — non-commutativity withdrawn. Failure ⇒ reopen D-32. |
| `TestNegative2_NondeterminismDoesNotFlipMembership` | §7.2 | **negative** — membership flip withdrawn. Failure ⇒ a `Remove` entered the vote keyspace; escalate D-32. |

Separately, `avc/randao/zz_race_probe_test.go`:

```bash
cd avc && go test -tags defects -race -run TestAccumulatorFoldRace ./randao/
```

| Test | Finding | Asserts (today) |
|---|---|---|
| `TestAccumulatorFoldRace` | D-24 | two `WARNING: DATA RACE` reports at `accumulator.go:195/:211` and `:209` |

**Findings with no PoC** — D-26, D-28, D-30, D-31, D-34. Each needs a running node or a two-node harness. Their "Done when" clauses describe the test to write; §0.2 rule 2 applies to those descriptions.

---

## Appendix C — Limits of this audit

### C.1 What could not be verified

- **jmdn's own build and test suite.** The audit sandbox filled its filesystem pulling jmdn's dependency graph (libp2p, go-ethereum, duckdb, pgx). Every jmdn finding here is source-traced at a specific line, **not compiler-verified**. This is the audit's main weakness.
  Close it with: `cd jmdn && go build ./... && go test -race ./messaging/... ./Sequencer/... ./Security/...`
  Note that `Sequencer/beacon_bootstrap_test.go` passes today while missing D-27, so add the `len(epochs) > retain` case before trusting a green run there.
- **D-26's exploitability** depends on whether libp2p pubsub message signing is enabled on this fleet's `GossipSubPS` construction. It would not fix the defect — the code reads the payload field regardless — but it changes how easily a non-buddy reaches the topic.
- **D-28's fork acceptance end-to-end.** Confirmed at the hash level by reading both preimages and grepping for the absent fields; a two-node harness proposing same-transaction blocks at one height would settle the runtime behaviour.
- **D-29's environment gate.** How `network.environment` is set in a real mainnet deployment was not verified, nor whether a separate config path would make the check land elsewhere.
- **`jmdn/AVC/BFT`** is a divergent vendored fork of `avc/bft`. The original was audited; the fork's copies of `byzantine.go`, `engine.go` and `sequencer_client.go` may have drifted.
- **D-24's trigger frequency is unquantified.** The race is proven; how often two blocks fold reveals for the same epoch concurrently depends on block rate, gossip fan-in and reveal density. It is latent today only because the devnet sets no VDF env vars — a config accident, not a safeguard.

### C.2 Staleness

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
