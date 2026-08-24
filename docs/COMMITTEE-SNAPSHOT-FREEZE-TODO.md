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
4. **PARTIALLY DONE — Rejoin/load path.** Slot recovery is done, including the startup call site (item 8, fully closed). Snapshot-hash recovery is done (item 1, once synced to any block in the epoch). Snapshot BODY recovery and entropy-value recovery are still not done — a restarted node can now *verify* a body if it gets one, but can't yet *fetch* one, and still can't recover `ENTROPY-E` at all (item 3).
5. **✅ Confirmed, no work needed — "which epoch am I in."** Solved for free by normal chain sync + `EpochForSlot(slot) = slot/N`. Nothing built here because nothing was missing.
6. **NOT DONE — Seed node.** Still the actual remaining gap:
   - Implement `GetCommitteeSnapshot(epoch)` server-side — declared in `seednode.proto`, called by the client, **zero server implementation anywhere in the repo.**
   - Once implemented, it should serve a body that gets verified against the on-chain hash from item 1 (`avc/committee.HashSnapshot`) — not trusted on its own signature. This is a stronger design than originally planned: the seed node no longer needs to be trusted, only available.
   - An equivalent entropy-serving concept for item 3 once that's built.
7. **NOT DONE — Enable the flags.** `JMDN_COMMITTEE_SNAPSHOT_ANCHOR` and `consensus.require_pinned_committee` both stay off until 1-6 are complete and fleet-coordinated, same rollout discipline as M2b.
8. **✅ DONE — Slot restart recovery, including the startup wiring (closed 2026-08-24, this session's second pass).** `messaging.SlotStore.SeedFromCommittedTip(tipSlot, tipHeight)` seeds the in-memory slot counter from the last committed block's own `Slot` field, refusing once the store is already live (no clobber risk) — this part shipped earlier and is unchanged. What was missing — "nothing yet calls `SeedFromCommittedTip` at actual node startup" — is now closed:
   - `jmdn/messaging/slot_store_recovery.go` (new): `RecoverSlotStoreAtStartup(getTip)` — reads this node's own committed tip and seeds `DefaultSlotStore`, fails closed on any real read error, treats a genuinely empty local chain (`ErrNoCommittedBlock`) as legitimately ready-but-unseeded (not an error), and refuses to silently adopt slot=0 for a real block that carries no persisted slot/period. `EnsureSlotStoreRecovered(getTip)` is the idempotent re-callable form for the fast-sync path. `SlotStoreReady()`/`MarkSlotStoreReady()`/`EnforceSlotRecoveryGate` (env `JMDN_ENFORCE_SLOT_RECOVERY_GATE`, default ON — this is a local per-node safety check, not a wire-format change, so unlike the `*_WIRING`/`*_AGG_CERT` flags it ships live by default) are the readiness gate consulted by both the vote and propose paths.
   - **Read-side gap found and fixed in the same pass:** the write-side persistence (`DB_OPs/backend/block.go`'s `toBlockRecord`) was already landing `Slot`/`Period` into `ExtraData`, but `DB_OPs/thebe_conversions.go`'s `blockRecordToZKBlock` — the function `GetZKBlockByNumber` actually calls — never decoded those two keys back out. Every real caller would have seen `Slot=0`/`Period=0` on every block regardless of what was persisted, silently defeating the whole fix before it could ever run. Fixed (`extraDataUint64` helper handles the `float64` JSON round-trip every real caller hits, not just a raw `uint64`).
   - **main.go wiring:** `messaging.RecoverSlotStoreAtStartup` is called before `node.NewNode()` — i.e. before the libp2p host exists and before any commit hook can possibly fire, which is what makes the ordering race-free without needing a lock. `MessagePassing.SetSlotStoreReadyFn(messaging.SlotStoreReady)` wires the vote-side gate (injected, not imported directly — `messaging` already imports `Vote`, which imports `MessagePassing`, so `MessagePassing → messaging` would be a cycle). On `cfg.Thebe.Enabled == false` (no ExtraData persistence exists at all on that path) the node is marked ready immediately, preserving that legacy configuration's pre-existing behavior rather than permanently blocking it — disclosed loudly in a startup log line, not silently applied.
   - **Fail-closed on both sides, not just detection:** `AVC/BuddyNodes/MessagePassing/consensus_sync_gate.go`'s `consensusVoteReady()` now refuses to vote while unrecovered, independent of the pre-existing block-height sync gate (a node can be fully caught up on height while its `SlotStore` is still stuck at 0). `Block/consensus_fields.go`'s `attachAVCConsensusFields` now returns an error and refuses to stamp `Slot`/propose while unrecovered — its two callers (`Block/Server.go`, `Block/grpc_server.go`) abort the block with a clear error instead of proceeding.
   - **Fast-sync/rejoin (item 4's "not done" half, for this specific gap):** bulk catch-up bypasses the live commit hooks entirely (unchanged, documented characteristic — see `slot_store.go`'s header), so `DefaultSlotStore.haveCommitted` stays `false` throughout a catch-up. The `ReconcileFunc` in `main.go` calls `messaging.EnsureSlotStoreRecovered` immediately after each successful `fastSyncerV2.HandleCatchUpSync`, so a node that started with an empty/behind local chain gets seeded from the real tip as soon as catch-up lands. **Disclosed residual, not closed:** the narrow window between a fast-sync batch finishing and that re-seed call is not itself lock-protected against a live gossip block landing in between — closing that fully needs a lock shared with the commit hooks, out of scope here; bounded today by the same block-height lag gate that already blocks voting more than 2 blocks behind.
   - Tests: `messaging/slot_store_recovery_test.go` (7 cases — real-tip seed, empty-chain ready-but-unseeded, read-failure fails closed, refuses an unrecoverable real-height tip, refuses once already live, no-ops once live, seeds correctly after a simulated fast-sync catch-up), `DB_OPs/thebe_conversions_slot_test.go` (4 cases, including a real JSON round-trip), `Block/consensus_fields_test.go`'s new `TestAttachAVCConsensusFields_FailsClosedWhenSlotStoreNotRecovered`, `AVC/BuddyNodes/MessagePassing/consensus_sync_gate_test.go`'s new `TestConsensusVoteReady_RefusesWhenSlotStoreNotRecovered` (the explicit "restarted node cannot vote at slot 0" proof). Full existing suites for `messaging`/`Block`/`DB_OPs`/`AVC/BuddyNodes/MessagePassing` still pass; `go build ./...` clean repo-wide.

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

### Production rollout checklist — before flipping `JMDN_TIMEOUT_CERT_WIRING=1`

Preconditions (verify on every node, not just one):

1. **Real BLS keys, not autogen.** Every validator must have a persistent
   committee BLS keypair loaded via `blssign.LoadBLSKeyPair()`.
   `JMDN_BLS_AUTOGEN=1` must be unset/`0` fleet-wide (it already defaults
   off — this is a "confirm it wasn't left on from a dev/test box," not a
   code change). If a node's key fails to load,
   `BLS_Signer.LocalBLSKeypair()` errors and `MaybeStartTimeoutFlow` just
   logs a warning and returns — that validator silently never casts a
   timeout vote again. This fails quiet, not loud: it will not show up
   unless someone reads logs or item 9 below (metrics) is done first.
2. **Eligibility source wired before the first round.** Already true today
   (`Sequencer.NewConsensus` calls `messaging.SetCommitteeEligibilitySource`
   unconditionally) — listed here as a precondition to confirm, not a gap.
3. **Committee/selection env vars set correctly per node**
   (e.g. `JMDN_NODE_SELECTION_MNEMONIC` and whatever else
   `eligibleMembersUncappedForEpoch`'s configured source depends on in your
   deployment). If it errors, `timeoutVotingPool` fails and NO tally can
   succeed for that node's view — this fails closed (safe), but a
   misconfigured node quietly can't participate in recovery at all.
4. **Fleet-wide, simultaneous flag flip — not gradual.** A mixed fleet
   (some nodes on, some off) is unsafe: off-nodes never sign, gossip, or
   accept anything on this path, so if enough of the pool is off, on-nodes'
   votes can never reach quorum; meanwhile on-nodes' accepted certificates
   silently diverge from off-nodes' `PeriodStore`
   (`RoundContextForBlock` reads it, which feeds committee seeding) —
   different nodes could compute different committees for the same round.
   Same discipline as `JMDN_M2B_HASH` / `JMDN_COMMITTEE_SNAPSHOT_ANCHOR`:
   one coordinated switch, not a canary percentage.

Known gaps to consciously accept or close before go-live (nothing here
blocks flipping the flag in a low-stakes/staging environment — these are
what to weigh for a real-money production fleet):

5. **No rejoin/catch-up RPC (already tracked as item 9 below).** A node
   that restarts mid-timeout-escalation for the CURRENT, not-yet-committed
   height loses all in-memory period/certificate state and restarts that
   height at period 0. It only recovers if it happens to receive a live
   gossip of a still-relevant certificate — gossip is not replayed or
   persisted. Low risk for a short escalation on a small, stable fleet;
   real risk if restarts/deploys are frequent or escalations run long.
   Decide: accept and monitor for v1, or build the catch-up RPC first.
6. **CPU cost on duplicate/replayed votes and certificates — confirmed by
   re-reading the code, not a guess.** `TallyTimeoutVotes` (called from
   `tryCertify`) re-verifies every vote's BLS signature on every call,
   INCLUDING votes that arrive after the round is already certified —
   `timeout_gossip.go`'s `markCertified` short-circuit only runs AFTER
   `TallyTimeoutVotes` has already re-verified everything (see
   `tryCertify`, `messaging/timeout_gossip.go`). Likewise,
   `PeriodStore.AcceptTimeoutCertificate` calls `VerifyTimeoutCertificate`
   (a full BLS aggregate-signature check) BEFORE checking whether the
   certificate's period is already stale/known
   (`messaging/timeout_certificates.go`). Under flood-gossip rebroadcast, a
   single certified round can cause every node to repeatedly re-verify
   signatures for messages it has already resolved — real CPU cost at
   fleet scale, not a correctness bug. Not fixed yet. Recommended
   pre-production fix (small, low-risk): check
   `LatestTimeoutCertificateFor`/an equivalent "already resolved" guard
   FIRST in both paths, before doing any signature verification. Flag if
   you want this done before go-live or as a fast-follow.
7. **Equivocation exclusion is only as complete as the caller's knowledge.**
   A pure gossip-relay node (not the one that ran the block-vote tally for
   that round) passes `blockVoters=nil` into the receive path and cannot
   itself detect a remote double-signer — see the MUTUAL EXCLUSION note at
   the top of `timeout_gossip.go`. Acceptable if the sequencer role is
   small/trusted; revisit if that assumption changes.
8. **No production alerting yet.** A certified timeout is currently only a
   zerolog line (`timeout flow: quorum reached...`) — recommend wiring an
   `Alerts.NewAlertBuilder` call (the same pattern already used for
   `Consensus.BroadcastAndProcessBlock`'s reject path) so operators see
   this in whatever alerting channel the fleet already watches, not just
   in logs someone has to go looking for.
9. **No load/chaos test yet.** Only unit tests and the 3-host functional
   network test exist. Recommend at least one staged-environment run that
   forces a real quorum timeout (e.g., kill enough buddies mid-round) and
   confirms the fleet actually recovers and keeps producing blocks, before
   trusting this in production.

**Rollback plan:** setting `JMDN_TIMEOUT_CERT_WIRING=0` fleet-wide is a
complete, safe rollback with no data migration — every entry point
(`MaybeStartTimeoutFlow` and both gossip handlers) checks the flag first
and no-ops otherwise, and all new state (`DefaultPeriodStore`'s advancement
beyond 0, the vote collector, the certificate cache) is in-memory only.
Turning the flag off returns exactly to today's pre-session behaviour: per
`timeout_certificates.go`'s own header comment, `PeriodFor` was already
being read everywhere but nothing ever advanced it past 0 before this
session's work — so "off" is not a new, untested code path, it's the
literal status quo this session started from.

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
