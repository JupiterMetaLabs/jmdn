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

**✅ CLOSED 2026-08-24 (second pass) — the rejoin RPC itself.** The gap
directly above (and item 9 below) is now built, not just made trivial-to-add:

- `jmdn/config/constants.go` — new `TimeoutCertRejoinProtocol` (its own
  protocol ID, same reasoning as `RevealPushProtocol`: a node that doesn't
  speak it fails the connection outright rather than silently mis-routing).
- `jmdn/messaging/timeout_rejoin.go` (NEW) — the RPC itself, one
  request/response pair over a direct libp2p stream (JSON+newline, same wire
  style as `entropy_reveal_push.go`'s RevealPush):
  - `HandleTimeoutCertRejoinStream` (server side) — answers strictly from
    this node's own already-verified local state
    (`LatestTimeoutCertificateFor`); never fetches, forwards, or trusts a
    third party.
  - `RequestLatestTimeoutCertificateFromPeers(h, peers, height)` (client
    side) — asks each peer in turn, and re-verifies whatever comes back via
    the SAME `AcceptIncomingTimeoutCertificate` gossip already uses before
    trusting it for anything. A peer's answer that fails verification (wrong
    signers, forged, stale) is rejected and the next peer is tried — the RPC
    layer adds zero new trust over gossip's own acceptance path. Querying
    several peers means one unresponsive/lying peer cannot stall a
    rejoining node.
  - Registered on every node in `node/node.go`, right alongside the other
    stream handlers.
  - Gated OFF by default (`JMDN_TIMEOUT_CERT_REJOIN`) — flip together with
    `JMDN_TIMEOUT_CERT_WIRING` once both are fleet-tested.
- `jmdn/messaging/timeout_rejoin_test.go` (NEW) — real 2-host libp2p network
  tests: legitimate "not found" (most heights never time out); a genuine
  quorum-certified certificate served and independently verified+accepted
  by the client (`PeriodStore` actually advances); a certificate signed by
  keys outside the client's eligible pool is rejected (forged-peer case);
  an unresponsive first peer does not block a second, good peer. All pass,
  including under `-race`; full `messaging` package and repo build remain
  green (the only pre-existing failures anywhere in the repo — `TestStreamLeak`,
  `TestGetBuddyNodes`, `DB_OPs/Tests`, `seednode`'s `Test_GetPeer` — are
  unrelated environmental issues, confirmed by reading each one).

**Still open, disclosed, not part of this pass:** the *orchestration* call
site — deciding WHICH heights need a rejoin request and WHICH peers to ask
(e.g. wired into `FastsyncV2`'s catch-up path or a dedicated startup check
against `PeriodStore` gaps) — is not built. What exists is the complete,
tested transport + verified-accept primitive; wiring it into an automatic
"call this on rejoin" trigger is the next step, same shape as
`messaging.EnsureSlotStoreRecovered`'s fast-sync hook in item 8 above.

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

5. **✅ Rejoin/catch-up RPC built (2026-08-24, second pass — see item 9
   below).** A node that restarts mid-timeout-escalation for the CURRENT,
   not-yet-committed height loses all in-memory period/certificate state
   and restarts that height at period 0. It can now recover on demand via
   `messaging.RequestLatestTimeoutCertificateFromPeers`, instead of only
   passively via a live gossip that happens to arrive — but nothing yet
   CALLS that function automatically on rejoin (no orchestration wired
   in). Until that orchestration exists, a restarted node still only
   recovers passively (live gossip / a fresh certification after it
   reconnects), exactly as before — the fix is available, not yet
   triggered. Decide: wire the automatic call before go-live, or accept
   passive-only recovery and monitor for v1.
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

**Item 9 — seed node / sync RPC for timeout-certificate catch-up.**
**✅ CLOSED 2026-08-24 (second pass).** `LatestTimeoutCertificateFor` is now
exposed over an actual RPC — see the "✅ CLOSED 2026-08-24 (second pass) —
the rejoin RPC itself" entry above for the full detail
(`config.TimeoutCertRejoinProtocol`, `messaging/timeout_rejoin.go`'s
`HandleTimeoutCertRejoinStream` / `RequestLatestTimeoutCertificateFromPeers`,
registered in `node/node.go`, gated by `JMDN_TIMEOUT_CERT_REJOIN`, 4 new
real-network tests in `messaging/timeout_rejoin_test.go`). A rejoining node
can now pull a peer's latest certificate on demand instead of only ever
receiving it passively via gossip.

**Still open (not this item, tracked in the "Still open, disclosed" note
above and rollout-checklist item 5):** nothing yet calls
`RequestLatestTimeoutCertificateFromPeers` automatically on
rejoin/catch-up. The transport and verified-accept logic exist; the
orchestration decision — which heights to check and which peers to
ask, wired into `FastsyncV2`'s catch-up path or a dedicated startup
check — is the next step.

**Item 10 — timeout-recovery quorum draws from the same committee that
failed, not a wider T_vote pool. ❌ NOT DONE — confirmed still open,
2026-08-24 (verification pass, no code changed).**

`messaging/timeout_certificates.go:91-95`'s own comment claims
`TallyTimeoutVotes` uses "a pool-wide T_vote quorum... never the buddy
committee's smaller T_agg (recovery must not depend on the entity that
failed to reach consensus in the first place)." Traced end-to-end and
that claim does not hold for the live code path:

- `tryCertify` (`timeout_gossip.go:257-283`) calls `timeoutVotingPool(height)`
  (`timeout_gossip.go:171-181`), which calls
  `eligibleMembersUncappedForEpoch(epoch, false)`
  (`consensus_hardening.go:189`) — the SAME function that resolves the
  block-voting committee for that epoch. There is no second, independent
  pool anywhere in this codebase (`T_agg` does not appear as an actual
  value anywhere else — grepped, zero hits outside that one comment).
- In the path that is actually live today (no pinned seed authority —
  `Sequencer/consensus_statemachine.go:172`'s `legacyBuddySource`), that
  function returns `c.PeerList.MainPeers` directly, explicitly capped
  to "the voting-committee size" (`consensus_statemachine.go:135`) — the
  literal same peer set asked to vote/propose the block that timed out.
- Even the V2/pinned path doesn't yet help: `SelectCommitteeWithSize`
  (`committee_v2.go:209`) draws committees from
  `MaxValidators(7) >= pool(7)` today, a documented no-op cap
  (`committee_v2.go:262`), and the underlying wider registry it would need
  — `GetCommitteeSnapshot(epoch)` server-side — has "zero server
  implementation anywhere in the repo" (item 6 above, unchanged).

**Verdict: the finding is accurate.** A buddy committee that fails as a
whole is asked to certify its own recovery. Not a quick wiring fix like
items 8/9 above — the wider pool this needs doesn't exist as data
anywhere yet, so fixing it means first deciding where that pool comes
from (the full registered-validator set once item 6 is built, vs. a
dedicated backup/standby committee) — a consensus-critical design
decision needing the same coordinated-rollout discipline as the other
flags in this file, not something to silently pick during a
verification pass. Awaiting direction before any implementation starts.

**Item 10 stays open exactly as written above — the following section is
a separate, now-closed question about the STATE MACHINE, not about pool
composition. Do not read the section below as resolving Item 10; it does
not touch which peers can vote, only what happens with the votes once
cast.**

## Timeout-recovery period-advancement state machine — closed 2026-08-25

Design confirmed this session and now enforced by construction, not just
by convention: **Period may advance only via a verified, monotonic
`TimeoutCertificate` — never on a timer, never by any single node's
(including the sequencer's) local say-so.** This is orthogonal to Item 10
above: Item 10 is about *who is eligible to vote* (still open); this
section is about *what happens once votes exist* (now closed and tested).

Required flow, as specified and verified against the live code:

```
PERIOD 0 → Select Committee for (height, period=0) → Run Block Consensus
  SUCCESS → Commit Block
  FAILURE → Committee members create TimeoutVotes → Collect TimeoutVotes
    → quorum reached?
        NO  → Stay at Period 0. Committee unchanged. Keep collecting
              votes indefinitely — no collection deadline exists, so a
              vote received late still counts.
        YES → Build TimeoutCertificate → Verify → Period 0 → Period 1
              → re-select committee using Period 1 → restart consensus
```

- **✅ DONE — single-writer invariant made explicit and mechanically
  enforced.** `messaging.PeriodStore.periods` already had exactly one
  write site in the repo (`AcceptTimeoutCertificate`, gated on BLS
  verification of the certificate AND `cert.Period > current`, i.e.
  strict monotonicity) — confirmed by grep before any code changed. What
  was missing was (a) that invariant being stated anywhere as a rule
  rather than an accident of how the code happened to be written, and
  (b) a regression check that would catch a future second writer before
  it ships. Both added: `timeout_certificates.go`'s `PeriodStore` struct
  and `AcceptTimeoutCertificate` doc comments now spell out that this is
  the *only* valid path, explicitly ruling out "operator override" and
  "sequencer local bump" as future shortcuts. `verify-m4.sh` now greps
  the whole repo for writes to `s.periods[...]` and fails if the count
  is ever anything other than exactly 1.
- **✅ DONE — no privileged aggregator, confirmed against the live gossip
  path, not just by code reading.** `recordAndMaybeCertify`/`tryCertify`
  take no host-identity or role parameter — any node that receives
  enough gossiped votes can independently assemble and broadcast a
  certificate, and `broadcastTimeoutVote`/`broadcastTimeoutCertificate`
  flood-broadcast to every connected peer rather than addressing a
  distinguished "sequencer." The sequencer MAY collect votes and
  assemble the certificate first for latency (this is the "sequencer as
  optional aggregator" design discussed this session), but has zero
  special authority: every node still independently re-verifies via the
  same `AcceptTimeoutCertificate` path, and the peer-to-peer gossip
  fallback works identically whether or not any particular node
  (sequencer or otherwise) is reachable. Proven with a real 3-host
  libp2p network, not just unit-level function calls (test 4 below).
- **✅ DONE — no collection deadline, confirmed.** There is no dedicated
  "give up waiting for timeout votes" timer anywhere in
  `timeout_gossip.go` (grepped for `time.After|time.NewTimer|Escalat|
  expire|Expire`, zero matches). The only real timer in the system is
  `config.ConsensusTimeout = 90s` (`config/constants.go:33`), which
  governs the block-voting round itself, not timeout-vote collection.
  Votes arriving late — even much later — still count toward quorum;
  this matches the spec's "keep accepting/collecting additional
  TimeoutVotes" requirement for the NO branch exactly, and is now
  covered by a test that adds votes with a real time delay between them
  and confirms the period only advances the instant quorum is crossed.
- **✅ NEW — end-to-end test suite,
  `messaging/timeout_recovery_statemachine_test.go`.** Four tests, all
  using the exact numbers in the spec (7 committee members, quorum 5),
  all passing under `-race`:
  1. `TestTimeoutRecoveryStateMachine_QuorumReached_EveryIndependentNodeConverges`
     — three separately-constructed `PeriodStore`s, given the same
     certificate, all independently verify it and land on the same
     Period 1. Proves "verify once, agree everywhere," not "trust
     whoever assembled it."
  2. `TestTimeoutRecoveryStateMachine_QuorumNotReached_PeriodStaysFrozen`
     — 3 of 7 votes (below quorum): `TallyTimeoutVotes` returns
     `ok=false` with no error, and `PeriodFor(height)` stays at 0.
  3. `TestTimeoutRecoveryStateMachine_LateVotesArriveOverTime_QuorumReachedEventually`
     — votes 1-3 recorded, period still 0; a real 50ms sleep; vote 4,
     still 0; vote 5, period advances to 1 in the same call that crosses
     quorum, and the cached certificate has exactly the 5 signers that
     actually arrived.
  4. `TestTimeoutRecoveryStateMachine_NoPrivilegedNode_AnyPeerCanCompleteTheCertificate`
     — three real libp2p hosts; an ordinary, non-distinguished host
     collects 5 gossiped votes over an actual network connection,
     assembles and broadcasts the certificate, and a third, independent
     host receives and can independently verify it. No code path in
     this test is aware of any "sequencer" concept.
  Verified: `go test ./messaging/... -run 'TestTimeoutRecoveryStateMachine' -v -race`
  — all 4 PASS; full `go test ./messaging/... -race` — both
  `gossipnode/messaging` and `gossipnode/messaging/BlockProcessing`
  green, no regressions, 32.6s total.
- **Verdict: the state-machine question from this session's design
  discussion is closed.** The flow the user specified was, by the time
  it was specified, already correctly implemented by construction for
  every branch — this pass added the explicit invariant statement (so a
  future edit can't silently weaken it) and the proof that it actually
  holds (so "already correct" isn't just an untested claim). **Item 10
  above is unaffected and remains the real open question**: this closes
  *what the votes do once collected*, not *who is allowed to vote*.

## Fallback window (§4.2a / §10 decision 11) — verification findings, 2026-08-24

Checked against the morning's count-based-collection amendment
(`messaging/entropy_fallback_window.go`) and the two-phase finalisation
it depends on (`messaging/entropy_finalise.go`). Two items genuinely
done, two gaps found — neither previously tracked in this file.

- **✅ DONE — hard upper bound on B exists and is correct.**
  `FallbackFoldBufferB = 5`, `FallbackFoldMaxSlotOffset = 7`
  (`entropy_fallback_window.go:87,102`), derived from §7.2's liveness rule
  (`B ≤ N−K−2·T_vdf/s_min = 50−3−40 = 7`) with 2 slots of margin.
  `ValidateFallbackWindowParams()` (`:168-178`) correctly checks both the
  N/K/MaxOffset range and `MaxOffset ≥ B`. Test
  (`TestValidateFallbackWindowParams_AdoptedValuesAreUsable`) passes.
- **✅ CLOSED 2026-08-24 (third pass) — checker now wired at startup.**
  `main.go` now calls `messaging.ValidateFallbackWindowParams()` right
  before the slot-recovery block (item 8), before `node.NewNode()` —
  no `cfg`/node dependency, so it runs as early as possible. A failure
  hard-exits via `log.Fatal()` (same pattern already used elsewhere in
  `main.go` for irrecoverable startup errors) rather than failing
  closed-but-running: unlike a per-node recovery failure, a bad
  N/K/B/MaxOffset combination is a build defect that is identical on
  every node, so there is nothing to run into. Verified: `go build ./...`
  clean, `gofmt -l main.go` clean, and two new `verify-m4.sh` checks
  (function exists; has a real non-test caller) — 57/0 passing, up
  from 55.
- **✅ DONE — two-phase pending finalisation, wired to real commit
  hooks.** `decideEpoch` (`entropy_finalise.go:134-160`) never resolves a
  fallback seed at the cutoff slot itself (zero signers exist at that
  instant by construction); it marks the epoch `pendingFallback` and
  returns. `resolvePendingFallbacks` (`:167-203`) retries on every
  subsequently committed block until `FallbackFoldBufferB` signers are
  collected or `FallbackFoldMaxSlotOffset` passes.
  `maybeFinaliseCompletedEpochs` (`:278-298`) runs both and is called
  from the two real commit hooks — `broadcast.go:816` and
  `blockPropagation.go:401` (grep-confirmed). All 12 relevant tests
  re-run fresh, including the exact "ordering that used to be
  impossible" regression case named in the file's own header — pass.
- **⚠️ Stale doc comment found — B1 is actually cleared, the file
  header doesn't say so.** `entropy_fallback_window.go`'s own header
  (lines 33-47) still reads "THIS PATH CANNOT RUN TODAY — blocker B1...
  Nothing can supply this collector." That's now false:
  `entropy_aggsig.go`'s header states "Blocker B1, cleared" (same date,
  2026-08-20) and the real wiring is in place —
  `RecordCommitCertificate`/`CertificateForBlockAssembly`/
  `VerifyAndRecordPrevCert` are live-called from
  `Sequencer/Consensus.go:1519`, `Block/consensus_fields.go:86`,
  `broadcast.go:814`, `blockPropagation.go:399` — gated by
  `AggCertEnabled` (env `JMDN_AVC_AGG_CERT`, default OFF, same
  coordinated-rollout discipline as the other flags). The fallback fold
  is functionally ready end-to-end once that flag flips; the comment
  just never got updated to say so. Needs a doc fix, no code change.

## Open questions (not yet resolved)

- ~~Who signs the frozen snapshot when no seed authority key is configured?~~ Resolved by the design change above: the on-chain hash makes the seed node's signature unnecessary — the chain is the authority. Still open only for the case of a node that trusts neither the chain (hasn't synced) nor a seed node.
- `consensus.committee_epoch_blocks` defaults to `0`, which degenerates the *BFT*-committee epoch concept (separate from the entropy epoch, which already works via `EpochForSlot`). Not blocking this TODO, but flagged so it isn't confused with the entropy-epoch fix above.
- ~~Item 8's startup call site (where a real node reads its tip block at boot) was not located this session — needs a follow-up look at `main.go`/node startup sequence.~~ Resolved in the second pass, 2026-08-24: located and wired at `main.go:1299-1305`, right before `node.NewNode()`. See item 8 above for full detail.

## Proposed — regional committee selection (idea captured 2026-08-26, NOT implemented)

**❌ NOT STARTED — design only, captured from a conversation, not yet scoped or reviewed.**

- Change committee selection so it runs **after "aexpj" is done completely** —
  `TODO: confirm what "aexpj" refers to` (stage/process name unclear from the
  conversation this was captured in; get this confirmed before picking the item up
  — do not guess and start implementing against the wrong stage).
- Once that stage completes, change the selection rule itself: instead of taking a
  single global **top-K** (current behaviour — see `CommitteeFor(seed, snapshot, k)`
  / `SelectCommitteeWithSize`, `committee_v2.go`), take the **top-N candidates from
  every region**, combined via a SQL query, instead of one global cut.
- Open, unresolved by this note (needs follow-up before implementation):
  - Where "region" comes from — no per-validator region/geo field currently exists
    in the eligible-set data (`Snapshot`/`committeeSnapshotFor`) as far as this
    session verified; needs a source before a SQL query can group by it.
  - What "N per region" is, and how it interacts with the existing overall
    committee size (`MaxValidators`/quorum math elsewhere in this doc).
  - Which datastore this SQL query would run against — nothing in the currently
    verified committee-selection path (`avc/committee`, `jmdn/messaging/committee_v2.go`)
    is SQL-backed today; this would be new infrastructure, not a query against an
    existing table.
- Not evaluated yet for interaction with Item 10 above (timeout-recovery quorum
  drawing from the same committee that failed) or with the snapshot-freeze/on-chain-
  anchor design earlier in this file — do that check before implementing, since both
  touch "how the eligible/selected set is computed."
