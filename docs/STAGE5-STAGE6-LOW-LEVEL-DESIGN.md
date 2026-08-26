# Stage 5 & Stage 6 — Low-Level Design

**Parent doc:** `docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md` §7 (Stage 5), §8 (Stage 6)
**Depends on:** Stage 4 (`STAGE4-IMPLEMENTATION-DESIGN.md`) — both stages build directly on `processVotesFromCRDT_v2` and `avcvotes.TallyBlock`.
**Status:** Design only — **not yet implemented**. This is the plan to review before code is written, same as the Stage 3.5 LLD before it.

---

## Part A — Stage 5: BLS verification on the new tally

### A.1 The gap this closes

`avcvotes.TallyBlock` **authenticates** a vote (checks the voter's claimed pubkey against the committee snapshot) but its own doc comment is explicit that it does **not cryptographically verify the BLS signature**. Nothing else does either, on the v2 path: `processVotesFromCRDT_v2` (Stage 4) currently takes every peer in `tally.SingleVotePeers()` on trust and feeds it straight into `MajorityDecision`.

Concretely, today a malicious peer that knows another committee member's public key (public info) could write a forged `VoteRecord` with a fabricated `BLSSignature` string, and — as long as `PeerID`/`BLSPubKeyHex` match the committee snapshot — it counts. `isAuthorizedVote` never inspects the signature bytes themselves.

### A.2 What already exists to build on

Stage 2's dual-write (`Vote/Trigger.go`) already signs each vote individually at cast time:

```go
blsResp, signed, blsErr := BLS_Signer.SignMessageForBlock(
    vt.Vote.Vote, BLS_Signer.DomainChainID(), zkBlock.BlockNumber, blockHash)
```

The verifier side already exists too, symmetric to the signer, and is already used elsewhere in the codebase (`messaging/consensus_hardening.go`, `Sequencer/Consensus.go`):

```go
func VerifyForBlock(resp BLS_Signer.BLSresponse, chainID, height uint64, bindings string, vote int8) error
```

Both derive their signed bytes from the same place (`CanonicalVoteMessageV3`), so signer and verifier cannot drift by construction. Stage 5 is "just" wiring an existing, already-proven verification function into the new tally path — not building new crypto.

### A.3 Design

Inside `processVotesFromCRDT_v2` (`AVC/BuddyNodes/MessagePassing/Structs/Utils.go`), between `tally.SingleVotePeers()` and the `MajorityDecision` call, verify each counted peer's backing `VoteRecord` and drop any that fail:

```go
single := tally.SingleVotePeers()

verified := make(map[string]int8, len(single))
for peerID, voteVal := range single {
    recs := tally.Signatures[peerID]
    if len(recs) != 1 {
        continue // defensive: SingleVotePeers already implies exactly one
    }
    rec := recs[0]
    resp := BLS_Signer.BLSresponse{
        Signature: rec.BLSSignature,
        PubKey:    rec.BLSPubKeyHex,
        PeerID:    rec.PeerID,
    }
    if err := BLS_Verifier.VerifyForBlock(resp, BLS_Signer.DomainChainID(), height, targetBlockHash, voteVal); err != nil {
        logger().Warn(logger_ctx, "BLS verification failed for counted vote — excluding (v2 path)",
            ion.String("peer_id", peerID), ion.Err(err),
            ion.String("function", "Structs.processVotesFromCRDT_v2"))
        continue // NOT counted — same posture as an equivocating peer
    }
    verified[peerID] = voteVal
}
// MajorityDecision(verified) instead of MajorityDecision(single)
```

**Scope, per the LLD's own guidance ("only verify signatures for votes you are counting"):** this only verifies `SingleVotePeers()` — the set already headed for `MajorityDecision` — not every element in `tally.Signatures`, which is bounded by the existing ingest cap (`maxElementsPerPeerPerBlock = 3`) but still unnecessary CPU for votes that will never be counted anyway (e.g. an equivocator's second value).

**Equivocating peers are a deliberate exception to that scope, deferred to A4:** the LLD notes "on an equivocation pair, verify both — you need to confirm the double-sign is real before penalising." That verification isn't needed yet because `ApplyEquivocationPolicy` is called with `reporter=nil` (A4 not started) — nothing acts on an equivocation verdict today besides excluding the peer from the count, which already happens via `SingleVotePeers()` regardless of signature validity. **Action item for whoever wires the A4 reporter:** verify both of an equivocator's records before reporting `Equivocation` to reputation, so a forged second vote can't be used to frame an honest peer.

**No reputation side effect yet.** A verification failure here is excluded from the count, logged, and nothing else — consistent with the equivocation handling already in Stage 4. Feeding this into reputation's `BadSignature` event (`-0.30` in your delta table) is an A4 concern, not Stage 5's.

### A.4 New imports needed

`AVC/BuddyNodes/MessagePassing/Structs/Utils.go` gains:
```go
BLS_Signer   "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
```
**Verified no import cycle:** neither `BLS_Signer` nor `BLS_Verifier` imports `MessagePassing` or `Structs` anywhere — they are leaf packages, checked by grep before writing this doc.

### A.5 Exit criteria (from the LLD, unchanged)

- Aggregate/individual verification passes on a real block through the new path.
- A forged signature in the CRDT is rejected, and that vote is not counted (verify by hand-crafting a `VoteRecord` with a bit-flipped signature and confirming it's excluded from `MajorityDecision`'s input).

---

## Part B — Stage 6: `ConvergeAndCompact`

### B.1 What it does and why the order matters

`avc/crdt/votes.Watermark.ConvergeAndCompact(c, tip, k, authorized, reporter)`:
1. Advances the watermark to `tip - k`.
2. For every block newly at-or-below the new watermark (and not already below the old one), runs `TallyBlock` + `ApplyEquivocationPolicy` **first**.
3. Only then deletes those blocks' `votes:`/`votesig:` keys.

Calling `CompactVotesBelowHeight` directly instead would delete evidence before anything evaluates it — a silently lost equivocation fault, not a bug that surfaces later. **Never call `CompactVotesBelowHeight`/`Watermark.CompactBelow` directly in application code; only `ConvergeAndCompact`.**

### B.2 A gap in the LLD's "use the existing onAdvance hook" recommendation

The LLD says to drive this from `DB_OPs.UpdateLatestBlockMonotonic`'s existing `onAdvance` hook. I checked where that hook is actually wired today (`main.go:1550`) and found a real gap: **it is currently registered only inside the branch where the sync monitor starts successfully** (nested under `FastSync`-enabled config, seednode client present, `syncMonitor.Start(ctx)` succeeding). A node running without a working sync monitor never gets `onAdvance` set at all today. If Stage 6's compaction hook piggybacks on that exact call site unchanged, the vote CRDT never compacts on any node that doesn't have sync-monitor running — reintroducing the unbounded-growth problem Stage 6 exists to fix, just on a subset of nodes.

**Decision 3 (new, blocks Stage 6): register the compaction hook unconditionally, independent of sync-monitor state.**

Recommendation: since `DB_OPs.onAdvance` is a single function pointer (last write wins, not a list), add a small combinator and register compaction **before** the sync-monitor block runs, then have the existing seed-push call site compose with whatever's already set instead of overwriting it:

```go
// main.go — new, near other early wiring, unconditional:
DB_OPs.SetLatestBlockAdvanceHook(newVoteCompactionPusher(ctx, voteCompactionK))

// main.go:1550 — existing call site, changed from overwrite to compose:
DB_OPs.SetLatestBlockAdvanceHook(combineAdvanceHooks(
    DB_OPs.CurrentLatestBlockAdvanceHook(), // the compaction hook set above
    startSeedBlockHeadPusher(ctx, func(c context.Context) { localMon.TriggerCheck(c) }),
))
```
This needs one small addition to `DB_OPs/latest_block.go` — a `CurrentLatestBlockAdvanceHook()` getter alongside the existing setter — plus a `combineAdvanceHooks(hooks ...func(uint64)) func(uint64)` helper in `main.go` that calls each in turn. Both are a few lines; neither changes `onAdvance`'s existing contract (non-blocking, no DB_OPs re-entrancy — a combined hook is still just a `func(uint64)` that fires the same non-blocking hooks in sequence).

**Open item for whoever implements this:** confirm the exact startup line where `VoteCRDTLayer`/committee source are already initialized, so the unconditional registration happens after those are ready — I have not traced every node-startup ordering path in `main.go`, only the specific `onAdvance` call site.

### B.3 Why this must also be async (a second contract risk)

`DB_OPs/latest_block.go`'s own doc comment is explicit: the hook **"MUST be non-blocking and MUST NOT call back into DB_OPs"** because it runs while `latestBlockMu` is held — anything slow here stalls all block application fleet-wide.

`ConvergeAndCompact` is not free: it calls `TallyBlock` (a full read + parse of both `votes:`/`votesig:` keys) for every newly-converged block, potentially several at once after a catch-up burst. Calling it **synchronously inside the hook** would violate that contract.

**Fix: mirror the existing `startSeedBlockHeadPusher` pattern exactly** — the codebase already has the correct shape for this (`seed_blockhead_push.go`), so this isn't a new pattern, just a second instance of one already reviewed and running in production:

```go
// newVoteCompactionPusher: same debounce/coalesce shape as
// newSeedBlockHeadPusher, adapted for compaction. The hook only records the
// latest tip and wakes a worker; the worker debounces, then calls
// ConvergeAndCompact exactly once per settled burst.
func newVoteCompactionPusher(ctx context.Context, k uint64) func(uint64) {
    var lastTip atomic.Uint64
    var pending atomic.Bool
    wake := make(chan struct{}, 1)

    hook := func(tip uint64) {
        lastTip.Store(tip)
        pending.Store(true)
        select {
        case wake <- struct{}{}:
        default:
        }
    }

    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            case <-wake:
            }
            select {
            case <-ctx.Done():
                return
            case <-time.After(voteCompactionDebounce):
            }
            if !pending.Swap(false) {
                continue
            }
            tip := lastTip.Load()
            authorized, err := messaging.AuthorizedCommittee()
            if err != nil {
                log.Warn().Err(err).Msg("[VoteCompaction] committee unavailable, skipping this round")
                continue
            }
            evaluated, deleted, err := avcvotes.DefaultWatermark.ConvergeAndCompact(
                DataLayer.GetVoteCRDTLayer(), tip, k, authorized, nil, // reporter=nil — A4 not wired yet
            )
            if err != nil {
                log.Warn().Err(err).Msg("[VoteCompaction] ConvergeAndCompact returned an error (partial progress kept)")
            }
            log.Debug().Uint64("tip", tip).Int("evaluated", evaluated).Int("deleted", deleted).
                Msg("[VoteCompaction] converge-and-compact pass complete")
        }
    }()

    return hook
}
```

### B.4 Constants (both are `Assumed`, per the parent LLD's own Decision 2 — never measured)

| Constant | Value | Note |
|---|---|---|
| `voteCompactionK` | `128` | Same as every other design doc in this migration. Bias large first; tighten only with real vote-arrival/catch-up lag data from testnet. |
| `voteCompactionDebounce` | `2 * time.Second` (proposed, **not in the LLD** — my addition) | Longer than the 750ms used for seed-head pushes, because `ConvergeAndCompact` does real CPU work (a `TallyBlock` per newly-converged block) where a seed push is a cheap RPC trigger. Needs the same "measure on testnet" caveat as K. |

### B.5 The reporter — land with `nil` first

Per the LLD: pass `reporter=nil` initially, confirm compaction deletes correctly (test with `GetSet` before/after), **then** wire `avcvotes.EquivocationReporter` to `reputation.Equivocation` as a separate, later change — this is explicitly an A4 concern, not Stage 6's.

### B.6 Exit criteria (from the LLD, unchanged)

- `GetSet` identical before/after compaction for keys above the watermark.
- A vote for a height at or below the watermark is refused at write time with `ErrHeightCompacted`.
- An equivocation at a converged height fires the reputation event **exactly once**, before its keys are deleted — testable once the reporter is wired; with `reporter=nil` this reduces to "the verdict is computed" (already true) with no observable side effect yet.
- Watermark regression is refused (already enforced by `Watermark.Set` itself — no new code needed to satisfy this).

---

## Part C — Combined risk note

Stage 5 and Stage 6 are independent of each other (one changes what's counted per-block; the other changes what's retained across blocks) and can be implemented and reviewed in either order, or in parallel. Both are inert while `JMDN_VOTE_CRDT_V2` stays off — Stage 5's verification only runs inside `processVotesFromCRDT_v2` (flag-gated since Stage 4), and Stage 6's compaction pass finds nothing to evaluate if nothing has been dual-written into `VoteCRDTLayer`. Neither stage needs its own separate flag; both inherit Stage 4's.

**Untested — this entire document is a design, not code.** Nothing here has been written, compiled, or run.
