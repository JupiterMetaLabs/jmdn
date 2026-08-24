# TODO — Freeze committee snapshot + entropy (hybrid: local + seed node)

**Status: partially implemented, 2026-08-24.** Design superseded its own original approach mid-flight (see "design change" note below) — items are marked against what actually landed, not the original plan text. All new code is TDD'd, race-tested, and gated OFF by default; nothing here changes production behaviour until the relevant flag is flipped.

**Decision: hybrid (Option 3), realized as on-chain anchor + off-chain body**, not local-KV + seed-node-KV as first drafted. Anchor a 32-byte hash on the chain itself (cheap, self-verifying, no trust needed); serve the full body off-chain (seed node) and verify it against the on-chain hash. This is strictly better than the original local-KV plan: it removes the "who signs the snapshot" open question below, because the chain is the authority, not an operator-held key.

## Why this is needed

`CommitteeFor(seed, snapshot, k)` needs TWO inputs to reproduce a past committee: the frozen eligible-validator snapshot AND the epoch's entropy value. Neither was durably stored before this session — both lived only in-memory and were lost on restart:
- Snapshot: `committeeSnapshotFor(epoch)` re-reads live membership every call, no freeze (`jmdn/messaging/committee_v2.go:308-336`).
- Entropy: `committee.BeaconSource.entropy` is a bare in-memory map, wiped on restart, no backfill (`avc/committee/beacon.go:39,64-68`).
- Separately found: the SLOT counter itself (needed to even know "what epoch am I in" on rejoin) also resets to 0 on restart (`jmdn/messaging/slot_store.go`'s own header comment).

## TODO items

1. **✅ DONE — Freeze trigger.** `jmdn/messaging/committee_snapshot_anchor.go`: `maybeFreezeUpcomingSnapshot(currentSlot)`, called from `entropy_finalise.go`'s `maybeFinaliseCompletedEpochs` (both existing commit hooks get it for free). Freezes epoch `E+1`'s snapshot once slot crosses `(E+1)·N − SnapshotFreezeLookahead` (lookahead = `RevealCutoffK` = 3), cached in-process, idempotent. Gated by `JMDN_COMMITTEE_SNAPSHOT_ANCHOR` (default false).
2. **SUPERSEDED — Local persistence, snapshot body.** Original plan was a local DB_OPs-style KV keyed `committee_snapshot_epoch:<epoch>`. Replaced by items below: only the *hash* needs to be durable and node-local (it already is, for free, once it's on-chain — every synced block carries it). The full body still isn't locally cached anywhere; see item 6 — unchanged gap, just re-scoped.
3. **NOT DONE — Local/on-chain persistence, entropy value.** `ENTROPY-E` itself (not just the snapshot) still resets on restart — this is the "Tier 2" piece (wire `block.VdfProof` for real, so the sealed entropy travels with the epoch-boundary block and any node can re-derive it via `vdf.Verify` instead of trusting a live publisher). Deliberately NOT attempted this session — it touches the live async VDF-sealing pipeline (`Sequencer/vdf_seal_wiring.go`), which needs its own dedicated pass rather than being bundled in here.
4. **PARTIALLY DONE — Rejoin/load path.** Slot recovery is done (item 8). Snapshot-hash recovery is done (item 1, once synced to any block in the epoch). Snapshot BODY recovery and entropy-value recovery are still not done — a restarted node can now *verify* a body if it gets one, but can't yet *fetch* one, and still can't recover `ENTROPY-E` at all (item 3).
5. **✅ Confirmed, no work needed — "which epoch am I in."** Solved for free by normal chain sync + `EpochForSlot(slot) = slot/N`. Nothing built here because nothing was missing.
6. **NOT DONE — Seed node.** Still the actual remaining gap:
   - Implement `GetCommitteeSnapshot(epoch)` server-side — declared in `seednode.proto`, called by the client, **zero server implementation anywhere in the repo.**
   - Once implemented, it should serve a body that gets verified against the on-chain hash from item 1 (`avc/committee.HashSnapshot`) — not trusted on its own signature. This is a stronger design than originally planned: the seed node no longer needs to be trusted, only available.
   - An equivalent entropy-serving concept for item 3 once that's built.
7. **NOT DONE — Enable the flags.** `JMDN_COMMITTEE_SNAPSHOT_ANCHOR` and `consensus.require_pinned_committee` both stay off until 1-6 are complete and fleet-coordinated, same rollout discipline as M2b.
8. **✅ DONE — Slot restart recovery (found mid-session, not in the original list).** `messaging.SlotStore.SeedFromCommittedTip(tipSlot, tipHeight)` — seeds the in-memory slot counter from the last committed block's own `Slot` field on startup, refusing once the store is already live (no clobber risk). Paired write-side fix: `DB_OPs/backend/block.go`'s `toBlockRecord` now persists `Slot`/`Period` into the block record's `ExtraData` (previously dropped entirely — this was the root cause named in `slot_store.go`'s own header comment). **Remaining integration step, not done:** nothing yet calls `SeedFromCommittedTip` at actual node startup — the function and its write-side data exist and are tested, but the startup call site (read the tip block, call the seed function) has not been located/wired.

## What shipped this session (implementation detail, for `verify-m4.sh` cross-reference)

- `avc/committee/snapshot_hash.go` + tests — `HashSnapshot(Snapshot) [32]byte`, canonical/deterministic/order-independent, golden-vector pinned.
- `jmdn/messaging/slot_store.go` — `SeedFromCommittedTip`, tested (fresh-store adopt, refuse-once-live, correct subsequent advance, no regression on stale height).
- `jmdn/DB_OPs/backend/block.go` — `toBlockRecord` persists `Slot`/`Period` unconditionally (including zero values), tested, coexists with the legacy `raw` ExtraData key.
- `jmdn/config/ZKBlock.go` — new field `CommitteeSnapshotHash []byte`.
- `jmdn/messaging/committee_snapshot_anchor.go` + tests — the freeze trigger described in item 1.
- `jmdn/Block/consensus_fields.go` — stamps the frozen hash onto every block of an epoch once frozen (not just one boundary block — redundancy so a rejoining node can recover it from whichever block it syncs to first).
- `jmdn/Security/consensus_fields_hash.go` + tests — `CommitteeSnapshotHash` added to the M2b v2 preimage (that function is itself still unwired/inert by design, so this was safe to extend without touching live consensus).
- `verify-m4.sh` — 6 new regression checks for all of the above. 34/34 passing.
- Full `avc` suite green; `jmdn` `Security`/`messaging`/`Block`/`DB_OPs/backend` green including `-race`.

## Timeout-certificate end-to-end wiring (2026-08-24, separate feature — M0/§7.1c)

**Status: wired end-to-end, gated OFF by default.** Not part of the
committee-snapshot work above — tracked in this same file per standing
instruction, since it's the same running TODO. Before this, every function
in `jmdn/messaging/timeout_certificates.go` (`SignTimeoutVote`,
`TallyTimeoutVotes`, `AcceptTimeoutCertificate`) had zero non-test callers
anywhere in the repo — fully built and unit-tested, but never connected to
live consensus.

**What shipped:**

- `jmdn/messaging/timeout_gossip.go` (NEW) — the wiring itself:
  - `MaybeStartTimeoutFlow(h, height, blockVoters)` — the single integration
    point. Called from `Sequencer/consensus_statemachine.go`'s
    `BroadcastAndProcessBlock`, exactly where `consensusReached == false` is
    already decided. Signs a `TimeoutVote` for `(height, PeriodFor(height)+1)`
    and gossips it — unless this node itself already cast a block vote for
    the round (§7.1b self-check).
  - A local vote collector tallies incoming votes (own + gossiped) per
    `(height, period)`, applying `DetectTimeoutBlockVoteEquivocation` /
    `RecordTimeoutBlockVoteEquivocation` against the block-voter set before
    tallying, then calls `TallyTimeoutVotes` → on quorum, builds the
    `TimeoutCertificate`, calls `PeriodStore.AcceptTimeoutCertificate`, and
    gossips the certificate.
  - Gossip transport: two new message types (`"timeout_vote"`,
    `"timeout_certificate"`) on the EXISTING flood-broadcast mechanism in
    `jmdn/messaging/broadcast.go` (`config.BroadcastProtocol` /
    `HandleBroadcastStream`) — not a new network, two new `msg.Type` values
    dispatched from the same handler that already handles `"vote_trigger"`.
  - `AcceptIncomingTimeoutCertificate(cert)` — verifies and accepts a
    certificate received directly (gossip today), without needing to have
    seen any prior vote. This is the "single certificate proves its entire
    prefix" property (`VerifyTimeoutCertificate`'s own doc comment) actually
    used, not just documented.
  - `LatestTimeoutCertificateFor(height)` — an O(1) local cache of the
    newest certificate this node has accepted per height. This is the
    primitive a rejoin/catch-up path calls to jump straight to the correct
    period without replaying periods 1..N-1.
  - `BLS_Signer.LocalBLSKeypair()` (small addition to the existing signer
    package) — exposes this node's already-loaded committee BLS keypair so
    `MaybeStartTimeoutFlow` can sign the timeout-vote domain, which differs
    from the two vote domains `BLS_Signer` hardcodes internally.
  - Gated by `JMDN_TIMEOUT_CERT_WIRING` (default false) — same coordinated
    rollout discipline as `JMDN_M2B_HASH` / `JMDN_COMMITTEE_SNAPSHOT_ANCHOR`.
- `jmdn/messaging/timeout_gossip_test.go` (NEW) — covers: (1) mutual
  exclusion — a validator cannot sign both a timeout vote and a block vote
  for the same (height, period), both the self-check and a remote
  equivocator's exclusion from the quorum tally (with the resulting
  reputation penalty verified, not assumed); (2) direct acceptance of a
  period-5 certificate with zero prior periods ever seen, proving the
  no-replay property; (3) a real 3-host libp2p network end-to-end test —
  sign → gossip a vote over an actual stream → receive/verify/tally/certify
  → gossip the resulting certificate over another real stream → a third,
  independent host receives and verifies it. All new tests pass, package
  suite green including `-race`.

**Mutual exclusivity, verified:** `TimeoutVoteDomain`
(`"jmdt/timeout-vote/v1"`) and `BLS_Signer.BlockBoundVotePrefix`
(`"zkvote:"`) were already cryptographically distinct domains (pre-existing);
this session added the actual runtime enforcement that makes the property
matter — a node refuses to cast a timeout vote for a round it already
block-voted in, and any peer caught having done both is excluded from the
timeout quorum and reported through the existing reputation-equivocation
pipeline. `TestValidatorCannotSignBothTimeoutAndBlockVote` covers both the
self-check and the remote-exclusion path.

**Deliberately NOT built (disclosed, not silently dropped) — same precedent
as item 6 above:** a real network RPC for "ask a peer for the latest
certificate at height H" on rejoin. No such transport (gRPC or otherwise)
exists anywhere in this repo today for ANY purpose (checked `Sequencer`,
`seednode`) — building one from scratch was judged disproportionate to
bundle into this pass versus the seed-node `GetCommitteeSnapshot` gap
already tracked as its own item. What IS built is the half that makes such
an RPC trivial to add later: `LatestTimeoutCertificateFor` is the exact
function such a handler would call, and `AcceptIncomingTimeoutCertificate`
is the exact function a client would call with the response — the "jump
directly to the correct period" requirement is proven end-to-end via gossip
delivery in the network test; only the on-demand pull (vs. push-via-gossip)
transport remains open.

**New TODO item 9 — seed node / sync RPC for timeout-certificate catch-up.**
Expose `LatestTimeoutCertificateFor` over an actual RPC (natural home:
`seednode`, alongside the still-open `GetCommitteeSnapshot` item 6, since
both are "ask an authority/peer for a value I don't have locally" shaped).
Until this exists, a rejoining node recovers its period only if it happens
to receive a live gossip of the certificate (or a fresh one gets certified
after it reconnects) — not yet via an explicit pull on demand.

## Open questions (not yet resolved)

- ~~Who signs the frozen snapshot when no seed authority key is configured?~~ Resolved by the design change above: the on-chain hash makes the seed node's signature unnecessary — the chain is the authority. Still open only for the case of a node that trusts neither the chain (hasn't synced) nor a seed node.
- `consensus.committee_epoch_blocks` defaults to `0`, which degenerates the *BFT*-committee epoch concept (separate from the entropy epoch, which already works via `EpochForSlot`). Not blocking this TODO, but flagged so it isn't confused with the entropy-epoch fix above.
- Item 8's startup call site (where a real node reads its tip block at boot) was not located this session — needs a follow-up look at `main.go`/node startup sequence.
