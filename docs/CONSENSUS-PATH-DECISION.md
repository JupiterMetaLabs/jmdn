# BFT or Direct Poll — AVC Consensus Architecture Decision

**Date:** 2026-08-28
**Question:** Should the buddy committee reach and report its block decision via the BFT PREPARE/COMMIT engine (the old jmdn design), or via the current direct poll-and-count path?

Every claim below was verified against current source in `~/Block/jmdn`, not inherited from status docs.

---

## Recommendation

**Keep the current direct poll-and-count path. Do not wire the BFT PREPARE/COMMIT engine.**

BFT costs roughly **1.67× the worst-case wall clock** (50s vs 30s) and **~84 extra messages per round**, adds a new no-recovery failure mode, and provides **no Byzantine guarantee the sequencer does not already enforce** over the identical seven nodes.

---

## The two paths

### Path A — Direct poll-and-count  ✅ LIVE TODAY

Buddies converge their vote views through a CRDT gossip round, each tallies independently, signs its own conclusion, and replies to the sequencer. The sequencer independently re-verifies every buddy signature and applies the Byzantine threshold itself.

```
CRDT gossip (<=30s) -> tally -> sign -> report -> sequencer verifies -> ceil(2K/3)
```

### Path B — BFT PREPARE/COMMIT  ❌ DEAD CODE

Adds two buddy-to-buddy broadcast rounds *before* reporting: a PREPARE phase waiting for a 2f+1 threshold, then a COMMIT phase waiting for another. Only then does the buddy sign and report — through the same reporting step Path A already uses.

**Currently unreachable.** The engine is fully built (`AVC/BFT/bft/`), but nothing in the repository ever *sends* a `Type_BFTRequest` — the constant appears only in the handlers that receive it and in its own definition. There is no sender, so this path cannot execute in the current build.

---

## The three axes, side by side

| Axis | Path A — Direct poll | Path B — BFT |
|---|---|---|
| **Worst-case wall clock** | ✅ ~30s + 1 RTT | ❌ ~50s + 1 RTT (30 + 10 + 10) |
| **CPU per buddy** | O(V) BLS verifies | O(V + K) — same order, extra per-phase verifies |
| **Messages per round** | ✅ O(K) gossip + K reports | ❌ + 2·O(K²) ≈ **84 extra** at K=7 |
| **Byzantine threshold** | ceil(2K/3) = 5 of 7 | ceil(2K/3) = 5 of 7 — *identical* |
| **Slow-node behaviour** | ✅ Tolerated — quorum from the other 5 | ❌ Hard error, no retry, no view change |
| **Scales to V validators** | ✅ Yes — tally is already O(V) | ❌ No benefit — still only the 7 buddies |

`V` = validators casting votes · `K` = buddy committee size (7 today: `MaxMainPeers` = `consensus.max_validators` = 7)

---

## 1. Worst-case time complexity

```
Path A:  [==== vote collection 30s ====]                                  = 30s
Path B:  [==== vote collection 30s ====][PREPARE 10s][COMMIT 10s]         = 50s
```

Both paths begin with the same vote-collection window — a 30-second CRDT gossip round (`CRDTSyncHandler.go:311`) during which buddies exchange full state so they converge on the same set of votes before anyone tallies.

Path B then stacks two more waits *in sequence*, each with a hard 10-second ceiling (`DefaultPrepareTimeout`, `DefaultCommitTimeout` — `AVC/BFT/bft/constants.go:23-24`). COMMIT cannot begin until PREPARE reaches its threshold, so these do not overlap. That is the full 20-second addition on the critical path.

**CPU cost is the same order in both.** The dominant term is verifying every counted vote's BLS signature — `verifyTallySignatures`, O(V) — plus a single linear tally pass (`MajorityDecision`, `vote_validation.go:65`: one loop, no weighting, no network calls). BFT adds O(K) verifies per phase, which at K=7 is negligible beside O(V). **The difference is latency and message volume, not computation.**

---

## 2. Speed

Path A is faster by construction, and the gap is structural rather than tunable: BFT's two phases are two additional *sequential network round trips with quorum waits* layered in front of a reporting step Path A performs directly.

Message volume compounds it. Each BFT phase is a broadcast among the committee — at K=7 that is 42 messages per phase, 84 across both, per block, on top of the gossip traffic both paths already carry.

**The convergence BFT would buy is already bought.** The purpose of forcing buddies to agree before reporting is to stop them reporting divergent views. That is precisely what the 30-second CRDT gossip round exists to do. Adding PREPARE/COMMIT means paying twice for one guarantee.

---

## 3. Security

**Both paths enforce the identical Byzantine threshold over the identical set:** `ceil(2n/3)` where n = 7, giving 5. BFT does not raise the fault tolerance, widen the participant set, or change f.

### What is already enforced on Path A

- **Every counted vote is individually verified.** `verifyTallySignatures` drops any vote whose BLS signature fails, before tallying or equivocation checking. A forged vote is never counted.
- **Equivocation is detected and excluded** — a peer casting two conflicting values for one block is removed from the clean set.
- **The sequencer trusts no buddy's word.** It independently re-verifies each buddy's signature against the block hash and height (`VerifyForBlock`), checks committee membership, and enforces the peer-ID↔BLS-key binding from the authenticated snapshot.
- **Fail-closed throughout.** A missing or failing committee source authorises nobody rather than defaulting open.
- **Blocks are re-verified on arrival** by every receiving node on the propagation path — the sequencer's count is not the only check.

### What BFT would add — and why it is not a gain here

In classical BFT, PREPARE/COMMIT matters because each replica *applies* the decision locally, so replicas must agree before acting. In this architecture buddies apply nothing: they report to a sequencer that decides. The agreement property is therefore worth much less than it is in the setting BFT was designed for.

It also would not defend against a dishonest sequencer, since the sequencer still assembles the final certificate either way — that risk is covered by independent re-verification during block propagation, not by buddy-to-buddy agreement.

### The regression it introduces

`waitForPrepareThreshold` returns a plain error when its context expires — **no retry, no view change, no fallback** (verified directly). Today, two briefly-slow buddies are harmless: the sequencer still reaches 5 of 7. Under BFT, a buddy that misses its own PREPARE or COMMIT window fails out and never reports at all, actively shrinking the pool the sequencer can draw quorum from.

**Verdict:** Path A is at least as strong on every security property examined, and strictly better on liveness.

---

## What this means for "all validators vote"

The goal of having the full validator set cast votes that buddies collect and tally works cleanly on Path A — the tally pipeline is already O(V) and size-agnostic. BFT contributes nothing to it: PREPARE/COMMIT runs among the seven buddies regardless of how many validators voted, so the extra 20 seconds would be pure loss.

**One structural change is genuinely required first**, and it is not BFT. Today a single source (`eligibleMembers()`, capped at 7) feeds two different quorums:

- the buddy's *who may vote* set, used by `TallyBlock`;
- the sequencer's *n* in `ceil(2n/3)`, used by `VerifyCertificate`.

Widen that one source to V validators and the sequencer's threshold becomes `ceil(2V/3)` — but only K buddies ever reply, so quorum could never be met and consensus would halt.

**The two sets must be separated before validator-scale voting can be switched on:**

| Set | Who | Used for | Size |
|---|---|---|---|
| **Voters** | all validators | `TallyBlock`'s `authorized` -> majority | ~1,000 |
| **Reporters** | buddy committee | sequencer's `ceil(2n/3)` | 7 |

---

## Open question for the seed-node owner

Does the seed already serve `GetCommitteeSnapshot` for the **current** epoch?

The client side is fully built and wired (`seednode/committee_snapshot_client.go` — fetch, authority verification, TOFU pinning, TTL cache, fail-closed). The known gap is specifically **past-epoch** reads, per `config/settings/config.go`'s own note: *"Do NOT enable until the seed node can serve GetCommitteeSnapshot for a PAST epoch."*

Validator-scale voting needs only the current snapshot, so if current-epoch reads already work, this is closer than assumed. See `docs/SEED-NODE-GETCOMMITTEESNAPSHOT-HANDOFF.md` for the full server contract.

---

## Method note

Latency, message counts, thresholds and code paths verified against source. Timings are worst-case ceilings derived from configured timeouts, not measured runtimes; a load test would be needed for typical-case figures.

---

## Background — why this question came up, and what it fits into

This doc is standalone on its own claims, but if you want the surrounding picture:

**The larger effort:** jmdn's consensus committee is currently fixed at 7 members (`config.MaxMainPeers` = `consensus.max_validators` = 7), and those 7 are the *only* nodes whose votes ever get counted — everyone else can technically send a vote, but it's dropped at authorization time. There's an in-progress design (`docs/VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md`) to let the *full* validator set (potentially ~1,000 nodes) vote, with a small committee ("buddies") aggregating those votes into a compact, verifiable certificate for the sequencer, instead of the sequencer only ever hearing from 7 nodes. That LLD's §0, §4, §5, §6 sections are already implemented and tested (`avc/crdt/votes/{snapshot_order,certificate}.go`); §7 (splitting the quorum's voter-set from its reporter-set) is deliberately not yet wired, since it touches live accept/reject math.

**Why that work is currently blocked:** letting the eligible-voter set grow past 7 requires the committee snapshot to be genuinely *pinned* per epoch (`consensus.require_pinned_committee`), which itself requires the seed node's `GetCommitteeSnapshot` RPC to actually be implemented. Right now that RPC exists as a generated stub only — the real server-side logic doesn't exist yet. The exact contract it needs to satisfy is written up in `docs/SEED-NODE-GETCOMMITTEESNAPSHOT-HANDOFF.md`, meant to be handed to whoever owns that repo.

**Where this specific question fit in:** while that seed-node work is pending, the question came up of whether to *also* bring back the old jmdn design's BFT PREPARE/COMMIT engine for how the 7 buddies reach and report their decision — as opposed to the direct poll-and-count the current code already does. That BFT engine is fully implemented in this repo (`AVC/BFT/bft/`) but is dead code today: nothing ever sends the message that would trigger it. This document is the analysis of whether reviving it is worthwhile. The recommendation is no — see above.

**Other defects found along the way, tracked separately, not part of this decision:** three live-but-currently-unreachable bugs in a different, dormant code path (`avc/nodeselection/router`, `avc/sequencer/trigger` — a hardcoded VRF test key, a mis-stored "aggregate" signature that's really just one node's signature, and a single-dissenter veto bug), written up in `docs/A7-A10-IMPLEMENTATION-PLAN.md`. Independent of everything above; not yet fixed.
