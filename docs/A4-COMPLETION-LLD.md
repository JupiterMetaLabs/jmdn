# A4 Completion — Low-Level Design

**Parent docs:** `A4-REPUTATION-WEIGHTING-PLAN.md` (the original phased plan), `JMDN-CRDT-VOTE-MIGRATION-LLD.md` (Stages 1-6, which this depends on).
**Status:** Design only — nothing in this document has been implemented. Landed pieces so far (uncommitted, verified working this session: build/vet/gofmt clean, `go test` passes where tests exist): `internal/reputation/selection_weight.go` (Decision A4-1's remap), `seednode/sequencer_reputation_push.go` + `reputation_seed_push.go` (Phase A4.2's push infra), wired into `main.go`.
**Repos touched by this design:** `jmdn` (all sub-stages), `avc` (§3 only, optional), `seedNodes` (§4 — spec only, cannot be implemented or compiled from this environment).

This is the completion pass: it closes every gap flagged during verification of the landed A4.2 work, plus the two structural wrinkles raised earlier (equivocation isn't sequencer-exclusive; `ListBuddy`/`UpdatePeerWeights` aren't actually auth-enforced on the seed today).

---

## 0. What's already real vs. what this document adds

| Already landed (uncommitted) | This document adds |
|---|---|
| `SelectionWeight` remap (Decision A4-1) | §1 sign-off ask on the exact constants |
| `PushReputationWeights` / `startReputationSeedPusher` (Phase A4.2 push infra) | §5 event-driven fast path for equivocation specifically |
| `main.go` wiring, unconditional like the compaction hook | §6 test coverage for all three new files |
| — | §2 the A4.1 flip itself (still `nil` today) |
| — | §3 closing the cross-node equivocation convergence gap |
| — | §4 the seed-side auth spec (for the seedNodes owner) |

---

## 1. Decision A4-1 — sign-off needed on the exact remap constants

`SelectionWeight`'s four constants (`healthyFloor=0.70`, `healthyCeil=0.94`, `faultedFloor=0.30`, anchored at `Start=0.50 -> 0.70`) are a real, working implementation of Option 1 from the original plan's three choices — but they were picked by whoever wrote the code, not confirmed with you. Concretely, this means:

- A never-observed peer starts at selection-score `0.70` (comfortably mid-band) instead of the raw `0.50` (edge).
- One `Absent` (`0.50 -> 0.40` raw) lands at `~0.60` — stays eligible.
- One `BadSignature` (`0.50 -> 0.20` raw) lands at `~0.40` — ineligible.
- One `Equivocation` (`0.50 -> Floor 0.10` raw) lands at `0.30` — ineligible, floor of the faulted band.
- A perfect-history peer (`Cap 1.00`) lands at `0.94`, clear of `MaxSelectionScore`'s `0.95` exclusive ceiling.

**Ask:** confirm these four numbers (or propose different ones — they're the only four constants that would need to change to retune this). No code change either way until you've said yes to specific values, not just the general shape.

**Known sharp edge, already documented in the code:** two `Absent` events in the same epoch land almost exactly on the `0.50` eligibility boundary — float64 rounding can tip it either way. Not fixable by more precision; it's an inherent property of landing a decision boundary exactly on a linear seam. If this matters in practice, the fix is moving the seam (e.g. anchor `Start` slightly above the two-Absence line instead of exactly at `Start`), not a numeric patch — flag only if you want to pursue it.

---

## 2. Sub-stage A4.1 — the reporter flip itself

Still outstanding. In `vote_crdt_compaction.go`:

```go
// change:
evaluated, deleted, err := avcvotes.DefaultWatermark.ConvergeAndCompact(
    listenerNode.VoteCRDTLayer, tip, k, authorized, nil)
// to:
evaluated, deleted, err := avcvotes.DefaultWatermark.ConvergeAndCompact(
    listenerNode.VoteCRDTLayer, tip, k, authorized, equivocationReputationReporter{})
```

One line. Already implemented, already covered by `TestEquivocationReputationReporter_RespectsEnabledFlag`. This is the switch that makes vote-CRDT equivocation actually reach `reputation.Default` at all — without it, `SnapshotSelectionWeights()` only ever reflects the four `Consensus.go`-sourced events (`AgreeFinalized`/`RejectNotFinalized`/`Absent`/`BadSignature`, all landed in commit `2f027f1`), never the CRDT-specific double-sign this whole migration exists to catch.

**Prerequisite, same as before:** `JMDN_VOTE_CRDT_V2` needs to be on and soaking — `compactConvergedVotes` returns early otherwise.

---

## 3. Closing the cross-node equivocation convergence gap

### 3.1 The gap, restated precisely

`ConvergeAndCompact` (Stage 6) runs on **every** node's own block-commit hook, not just the sequencer's. Each node evaluates equivocation against its **own local** CRDT copy. The sequencer only pushes **its own** `reputation.Default` (Phase A4.2, sequencer-exclusive by design — see §3.4 for why that stays true). So: if the sequencer's own local CRDT copy hasn't yet merged both of a peer's conflicting votes by the time its `ConvergeAndCompact` pass runs for that height, the sequencer never records the fault, and it never reaches the seed — even though six other buddy nodes independently detected and correctly recorded it.

### 3.2 Why this is bounded, not open-ended

The `K=128` compaction buffer (Decision 2, `JMDN-CRDT-VOTE-MIGRATION-LLD.md`) already exists as a convergence window — every node, sequencer included, delays evaluating a height until it's 128 blocks behind tip specifically so gossip has time to finish. Gossip propagation (seconds) is many orders of magnitude faster than 128 blocks' worth of wall-clock time under any normal block cadence. In steady-state operation, the sequencer's own copy should converge well within that window. The realistic failure case is a **partition or sustained sync lag on the sequencer specifically** lasting longer than the K-buffer window — a real but narrow scenario, not routine operation.

### 3.3 Design A (recommended default) — rely on K-buffer convergence, add visibility, no new plumbing

Do nothing structurally different; make the existing reliance on convergence observable instead of silent:

- In `compactConvergedVotes` (`vote_crdt_compaction.go`), log `evaluated`/`deleted` at `Info` (already does this) — add a counter metric (`vote_crdt_compaction_equivocations_reported_total`, if `metrics/` has a counter primitive already in use elsewhere — reuse that pattern rather than inventing a new metrics surface) so an operator can compare counts across nodes' logs/dashboards and notice if the sequencer's count is suspiciously lower than buddies'.
- No code path changes. This is a monitoring addition, safe to ship independently of everything else here.

**Recommendation: ship this now.** It's the honest, minimal-risk option, and matches "don't build for a failure mode you haven't observed yet."

### 3.4 Design B (stronger, not recommended yet) — evidence-based reporting, trustless at the seed

If Design A's monitoring ever shows real divergence in practice, the structurally complete fix is to stop trusting *any* single node's bookkeeping for this specific fault and let the seed verify the raw cryptographic evidence itself:

**avc side (new, small, self-contained):**
```go
// crdt/votes/proof.go
type EquivocationProof struct {
    PeerID    string
    Height    uint64
    BlockHash string
    Records   []VoteRecord // the 2+ distinct, conflicting signed records
}

// Verify independently re-checks every record's BLS signature and confirms
// at least 2 DISTINCT vote values are present. Callable by ANYONE holding
// the proof — including the seed node itself — with no need to trust
// whoever is submitting it. Chain ID is the only external input, since
// height/blockHash/vote are all inside each record already.
func (p EquivocationProof) Verify(chainID uint64) error { ... }

// NewEquivocationProof builds one from a tally already computed by
// TallyBlock, for the given equivocating peer.
func NewEquivocationProof(tally BlockTally, peerID string) (EquivocationProof, bool) { ... }
```

**jmdn side:** ANY node (not just the sequencer — this is the point, it removes the sequencer-exclusivity constraint for this one fault type) can submit an `EquivocationProof` directly to a new seed RPC. No jmdn-side signature over the submission is even required for authenticity, since the proof is self-verifying — only spam/rate-limiting matters (a submitter can't forge a fault, only waste the seed's CPU re-verifying a bogus one, which is cheap and bounded).

**seedNodes side:** new RPC, verifies the proof itself (reusing the same `blssign.BLSVerify` primitive jmdn already uses — same algorithm, needs a Go port of the verify call in the `seedNodes` module), applies the penalty directly to `peer.Weights` without needing the sequencer's `reputation.Default` push for this fault type at all.

**Why not build this now:** it's real new work across two repos (a proof type in `avc`, a new RPC + verifier in `seedNodes`), and Design A's cost is zero until evidence justifies it. Keep this section as the specced fallback, not a queued task.

---

## 4. Seed-side sequencer-auth enforcement (spec for the seedNodes owner — cannot be implemented from here)

### 4.1 Confirmed, not assumed

Checked directly against every branch in the local `seedNodes` checkout (`origin/main`, `origin/JMNS`, `origin/feature/peer-directory-reporting-services`, `origin/new-RedisStreams`, `origin/redisStream`, `origin/upsert-working`): **zero matches** for `SequencerAuthChallenge`, `SignSequencerRequest`, or any verify counterpart, anywhere. jmdn's own comment claiming this "mirrors seedNodes SignSequencerRequest" is describing intended symmetry, not existing code — worth a correction if that comment is read literally elsewhere. Both `ListBuddy` and `UpdatePeerWeights` are unauthenticated today, full stop; this needs to be built from scratch on the seed side, not wired to something that already exists.

### 4.2 What jmdn already sends (so the seed side has something real to verify against)

`seednode/committee/contracts.go`:
```go
func SequencerAuthChallenge(method, sequencerPeerID string, unixTs int64) []byte {
    return fmt.Appendf(nil, "%s|%s|%s|%d", SeqAuthVersion, method, sequencerPeerID, unixTs)
}
```
Sent as gRPC metadata headers `x-seed-auth-timestamp` / `x-seed-auth-signature`, signature over the challenge string using the sequencer's libp2p identity private key. `sequencerPeerID` in the challenge is **self-claimed by the signer** — derived from the same key that signs it (`peer.IDFromPublicKey(priv.GetPublic())`), not attested by anything else.

### 4.3 The critical design point — self-consistency is not authorization

A naive verify implementation (reconstruct the challenge, check the signature matches the claimed `sequencerPeerID`) only proves "whoever sent this controls the private key for the peer ID it claims" — **anyone** can generate a fresh keypair, derive their own peer ID, and pass that check trivially. It proves self-consistency, not that the claimed peer ID is *the* authorized sequencer.

**Required:** the seed must hold a configured, out-of-band-provisioned allowlist of exactly one trusted peer ID (the real sequencer's), and reject any request whose claimed `sequencerPeerID` doesn't match it — *in addition to* the signature check. This is the same pin-or-TOFU pattern jmdn's own `committee` package already uses for the seed's own authority key (`committee TOFU: adopted seed authority key on first use and persisted it`, seen live in this session's test output) — worth reusing that exact operational pattern (config-pinned, first-use-adopt, or explicit provisioning) rather than inventing a new one.

### 4.4 Concrete spec for the seedNodes owner

```go
// pkg/peer/sequencer_auth.go (new file, seedNodes repo)

// Configured once at startup — the ONLY peer ID this service will ever
// accept a sequencer-authenticated request from.
var trustedSequencerPeerID string // from config, pinned or TOFU'd — see §4.3

func VerifySequencerRequest(ctx context.Context, method string) error {
    md, ok := metadata.FromIncomingContext(ctx)
    if !ok { return errors.New("missing auth metadata") }
    ts := md.Get("x-seed-auth-timestamp")
    sig := md.Get("x-seed-auth-signature")
    if len(ts) == 0 || len(sig) == 0 { return errors.New("missing sequencer auth headers") }

    unixTs, err := strconv.ParseInt(ts[0], 10, 64)
    if err != nil { return fmt.Errorf("bad timestamp: %w", err) }
    if time.Since(time.Unix(unixTs, 0)).Abs() > authFreshnessWindow { // e.g. 60s, replay-window control
        return errors.New("stale sequencer auth timestamp")
    }

    challenge := fmt.Appendf(nil, "%s|%s|%s|%d", seqAuthVersion, method, trustedSequencerPeerID, unixTs)
    sigBytes, err := hex.DecodeString(sig[0])
    if err != nil { return fmt.Errorf("bad signature hex: %w", err) }
    pub, err := publicKeyFor(trustedSequencerPeerID) // resolve the PINNED peer ID's pubkey, never one from the request
    if err != nil { return err }
    ok2, err := pub.Verify(challenge, sigBytes)
    if err != nil || !ok2 { return errors.New("sequencer auth signature invalid") }
    return nil
}
```
Called from both `ListBuddy` and `UpdatePeerWeights` handlers in `cmd/jmns-service/main.go`, before doing any work. `UpdatePeerWeights` additionally needs the "who can update THIS OTHER peer's weight" question resolved — the existing self-signed `V/R/S` path (`CryptoManager.VerifyRecord`, currently a no-op) was designed for a peer updating *its own* record; the sequencer updating *someone else's* `peer.Weights` is a different trust relationship and should go through `VerifySequencerRequest` above instead of (or in addition to, gated by a distinct field) the self-signed path.

**Out of scope for jmdn to build or verify** — flagged precisely so it's ready to hand off, not attempted blind in a repo with no available compiler in this environment.

---

## 5. Event-driven fast path for confirmed equivocation

Current design: `reputation_seed_push.go` polls every 5 minutes, blind to what triggered any given score change — reasonable for the three low-severity events (`+0.02`, `-0.10`), which the existing doc comment already justifies well. Equivocation is different: it's the single highest-severity fault (`-0.50`, straight to the floor) and, per the whole B3 walkthrough, the entire point is to get a proven-bad actor out of the next committee selection. Waiting up to 5 minutes after a fault is *confirmed* (not merely suspected — Stage 5's signature verification already ran by the time `ReportEquivocation` fires) is unnecessary latency for the one event type that most needs to propagate fast.

**Design:** add a second, debounced trigger — same shape as `newVoteCRDTCompactionHook`/`newSeedBlockHeadPusher` — fired directly from `equivocationReputationReporter.ReportEquivocation`, independent of the 5-minute ticker:

```go
// vote_crdt_compaction.go, once A4.1 (§2) is wired:
func (equivocationReputationReporter) ReportEquivocation(peerID, blockHash string, height uint64, values []int8) {
    ... existing log + reputation.Default.Observe(...) ...
    triggerImmediateReputationPush() // new: signals the same debounced-worker pattern, coalesces bursts
}
```
`triggerImmediateReputationPush` reuses the exact `wake chan struct{}` + debounce-worker idiom already proven in `newVoteCRDTCompactionHook` — a burst of several equivocations in the same convergence pass collapses to one push, not one per fault. The 5-minute ticker in `reputation_seed_push.go` stays as-is, as the backstop for the routine low-severity events and for any node that never happens to observe an equivocation directly.

**Only meaningful once §2 (A4.1) is wired** — no reporter, no trigger point.

---

## 6. Test coverage — all three landed files currently have none

**`internal/reputation/selection_weight_test.go`:**
- Table test over the worked examples already in the doc comment: `Start` -> `0.70`, `Floor` -> `0.30`, `Cap` -> `0.94`, each `Delta` outcome (`Absent`, `BadSignature`, `Equivocation`) at their documented landing points.
- Monotonicity property test: `SelectionWeight` is non-decreasing as `repScore` increases across the full `[Floor, Cap]` range (catches a future constant change that accidentally breaks the ordering).
- Continuity at the seam: `SelectionWeight(Start - epsilon)` and `SelectionWeight(Start)` should differ by roughly `epsilon`'s worth, not jump — confirms the two segments actually meet at `healthyFloor`.
- Clamp test: values outside `[Floor, Cap]` (a caller bug, since `Store.Score`/`Snapshot` should never produce these, but the function clamps defensively) still return a value inside `[faultedFloor, healthyCeil]`.
- `SnapshotSelectionWeights`: seed a `Store` with 2-3 peers at different scores, confirm the returned map has the same keys as `Default.Snapshot()` with every value passed through `SelectionWeight`.

**`seednode/sequencer_reputation_push_test.go`:**
- `PushReputationWeights` returns `(0, [ErrNotSequencer])` immediately, no RPC attempted, when no sequencer key is registered — mirror the existing `ListBuddy`-side test pattern for `currentSequencerSignKey`, if one exists (check `sequencer_listbuddy_test.go` first and match its shape rather than inventing a new one).
- Empty `weights` map: returns `(0, nil)` with no RPC call.
- `sequencerAuthContextForMethod` produces headers under the correct keys (`SeqAuthTimestampHeader`/`SeqAuthSignatureHeader`) and the signature verifies against `SequencerAuthChallenge(reputationPushAuthMethod, ...)` reconstructed independently in the test — this is the one genuinely new crypto path in this file and deserves its own direct check, not just "the RPC didn't error."
- A partial-batch failure (mock/fake client failing on one peer, succeeding on others) returns the correct `accepted` count and a `failures` slice sized to match — confirms one bad peer doesn't abort the batch.

**`reputation_seed_push_test.go`:**
- `pushReputationOnce` with `reputation.Enabled = false`: no push attempted (need a way to observe this — a fake `seednode.Client`-shaped interface, or restructure `PushReputationWeights` behind a small interface `reputationPusher` so a test double can be injected without a real gRPC client; check whether `seednode.Client` is already an interface or a concrete struct — if concrete, this test needs that seam added, which is its own small piece of the work here, not just "write the test").
- `pushReputationOnce` with zero observed peers: no push, no panic.
- The `ErrNotSequencer` quiet-vs-warn log branching: assert (via a log-capturing hook, if one exists elsewhere in this codebase's tests — reuse it) that a single `ErrNotSequencer` failure logs at debug, not warn, and that a non-`ErrNotSequencer` failure does log at warn.
- `reputationPushIntervalSeconds`: default value, env override, matching `TestEnvUint64_DefaultsAndOverrides`'s existing shape in `vote_crdt_compaction_test.go` — same helper, same test pattern, don't reinvent it.

---

## 7. Build order and risk

| # | Change | Risk | Depends on |
|---|---|---|---|
| 1 | §1 sign-off on Decision A4-1 constants | none (no code) | — |
| 2 | §2 A4.1 flip | low | §1 (same values already chosen) |
| 3 | §6 tests for the 3 landed files | none (test-only) | none — can run in parallel with everything else |
| 4 | §3.3 Design A (monitoring only) | none | none |
| 5 | §5 event-driven fast path | low | §2 (needs the reporter live) |
| 6 | §4 seed-side auth spec | — (not implementable here) | seedNodes owner's own timeline |
| 7 | §3.4 Design B (evidence-based) | medium, cross-repo | only if §3.3's monitoring shows real divergence — not queued |

Nothing here is blocked on anything else in this repo; #1-#5 can all proceed independently once you've confirmed #1.

---

## 8. Exit criteria

- §2: `JMDN_VOTE_CRDT_V2=1`, a real double-signed vote injected in a test, `reputation.Default.Score(peerID)` reflects the `Equivocation` delta after `ConvergeAndCompact` runs.
- §5: same test, plus assert the seed push fires within one debounce window of `ReportEquivocation`, not waiting for the next 5-minute tick.
- §6: `go test ./internal/reputation/... ./seednode/... .` all green, no reliance on a live seed node (mock/fake the RPC boundary).
- §3.3: a counter visible in whatever metrics surface this repo already exposes (check `metrics/` before inventing a new export path).
- §4: out of scope for this repo's exit criteria — owned by seedNodes.
