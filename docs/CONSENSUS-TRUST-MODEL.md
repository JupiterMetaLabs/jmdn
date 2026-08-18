# Consensus trust-model remediation (single sequencer + buddy committee)

**Operator decisions (2026-08-18):**
- **Single sequencer.** One designated node produces every block; other nodes receive, validate,
  store, and vote. Not a multi-proposer chain.
- **Buddy committee retained.** The BFT buddy-node committee still certifies (votes to finalize)
  the sequencer's blocks. So the committee-machinery findings (CON-03/06/12) are **fixed, not
  deleted** — `AVC/BFT` and the buddy path stay.

This model has TWO trust anchors, and every fix below serves one of them:
1. **Authorship** — a block is genuine iff it is the sequencer's, and its certificate is a real
   quorum of the *authenticated* committee.
2. **State agreement** — because every receiver independently applies the block (and fast-synced
   nodes derive balances and then vote), all nodes must arrive at the *same* state, or halt. Today
   they don't (reproduced: live=1000 vs synced=2000 for the same account).

## Per-finding remediation (each its own commit + gate; consensus code = host CGO build + 2-node gate)

### CON-01 · pin the committee/sequencer key; reject empty-key auth (CRITICAL)
Sites: `config/settings/defaults.go:138` (`SeedAuthorityBLSPub: ""`),
`messaging/consensus_hardening.go:248-252` (`keyAuthorized`: empty bound key → returns true for ANY
presented key), `Sequencer/consensus_statemachine.go` (`legacyBuddySource` binds peer_id→"").
Fix: (a) a validator with no pinned `SeedAuthorityBLSPub` **refuses to start** (fail-closed, no
legacy path in production); (b) `keyAuthorized` **rejects** a committee member whose bound key is
empty instead of accepting any key; (c) include `PeerID` in the signed vote preimage so a vote can't
be relabelled to another committee member's id. Verify: a certificate assembled from keys not bound
in the snapshot is rejected **even when the source reports empty bound keys**; a validator with no
pin does not boot.

### CON-02 · bind height + parent + state into the block identity (CRITICAL — the #1 fix)
Sites: `messaging/consensus_hardening.go:439` (`RecomputeBlockHashFromTxs` = `keccak(concat tx.Hash)`
— no height/parent/proposer), `AVC/.../ListenerHandler.go` (`handleVoteResultRequest` signs the
caller-supplied `targetBlockNumber`, never checked against the block).
Fix: block identity preimage = `keccak(txsRoot ‖ height ‖ parentHash ‖ proposer ‖ stateFingerprint)`
(see P2.5). Independently, `handleVoteResultRequest` **refuses to sign** when the caller's
`block_number` disagrees with the height at which `block_hash` is locally known.
**Wire-format change → needs a versioned rollout** (a `/broadcast/block/3.0.0` + block-hash v3),
because a mixed fleet computes different hashes. This is the one item that cannot be a silent
in-place change; it flips fleet-wide like the contracts flag.

### CON-03 · vote-requester authz — authoritative source + fail-closed (CRITICAL) — PARTIAL (c6c02ac)
Sites: `AVC/.../consensus_vote_authz.go` (`enforceVoteRequesterAuth` env, default false; the old
`empty buddy set → return true` fail-open).
**Correction to the audit's "anchor on the snapshot" fix (missing-case, verified):** the requester is
the SEQUENCER, and a buddy resolves the sequencer mainly from the self-declared, unsigned
`msg.SequencerID` (`subscriptionService.go:803-825` Path 2) because buddies commonly hold no buddy
list (Path 1 fails). So the sequencer is frequently ABSENT from `currentBuddySet()` — and, per CON-01,
not guaranteed in the committee snapshot either. Defaulting the gate on and fail-closing against
either set would **reject the legitimate sequencer and halt the chain.** The authoritative requester
set must therefore be **committee snapshot ∪ pinned sequencer peer_id** — which makes CON-03's
default-on **coupled to CON-01's pin.**
Done (c6c02ac): `authorizedRequesterSource` (returns set + `ok`) + `AuthorizedRequesterSet` composer
+ `SetAuthorizedRequesterSource`. `voteRequesterAuthorized`: master switch (default-off, non-breaking)
→ injected authorizer → authoritative source **when it resolves** (definitive: a non-member is
rejected even with an empty buddy set — the fail-open is closed) → liveness-preserving legacy fallback
only when the source is indeterminate. Decision logic proven by isolation harness (9/9 branches PASS
CGO-off); in-package test host-gated (package pulls badger/redis/sqlite, won't compile in-sandbox).
Remaining (CON-01-coupled, host + 2-node gated): wire `SetAuthorizedRequesterSource` to the live
committee source ∪ pinned sequencer, then flip `enforceVoteRequesterAuth` default ON. Verify: with a
resolved source, an unauthenticated request is refused and the pinned sequencer is accepted; an
indeterminate source preserves liveness.

### CON-04 · certify catch-up / FastSync blocks (CRITICAL)
Sites: `FastsyncV2/catchup.go:70` (`HandleCatchUpSync` writes blocks with no `VerifyCertificate` /
`checkEquivocation`); the fastsync wire has no `bls_results` field. A fresh node's entire chain is
taken on trust from its sync peer.
Fix (single-sequencer makes this cheaper): transport the sequencer signature (or the committee
certificate) with each synced block and verify it on apply; anchor sync to a sequencer-signed
checkpoint. Until then, catch-up peers are **trusted infrastructure** and that must be documented,
not implicit. Verify: a synced block with an absent/invalid signature is rejected.

### CON-06 · deterministic committee selection + verify the VRF proof (HIGH) — PARTIAL (6b22304)
Sites (all verified against the tree; `selectWithRegionDiversity` is in `vrf.go:205`, not
`filter.go:88`): `AVC/NodeSelection/pkg/selection/vrf.go:104` (`buildRoundMessage` = `"<nodeID>:<salt>"`
— no round → constant VRF output, no rotation), `vrf.go` region list built from Go map iteration
before the seeded shuffle (non-deterministic committee run-to-run), `vrf.Verify` called nowhere. The
selector is live (`Router.GetBuddyNodes → selection.GetBuddyNodesWithNodes → SelectMultipleBuddies`).
Done (6b22304):
- **Determinism (in-place, shipped):** sort the region list into a canonical order before the seeded
  shuffle + a `peer_id` tiebreak on the intra-region score sort → selection is now a deterministic
  function of (inputs, seed). Safe / cannot fork: selection is the sequencer's LOCAL choice
  distributed via the seed-signed snapshot, and no node recomputes or verifies it today, so there is
  no cross-node algorithm to keep byte-identical. Proven by a real assertion test (identical selected
  order across 9 runs; the pre-existing test only logged and said results "may differ").
- **Round binding (primitive, dormant):** `BuildVRFRoundMessage` (domain-tagged `jmdn/vrf-round/v1`,
  binds node+salt+round). Threading `round`/epoch/height through the selection interface — so the
  committee actually rotates per round — is a consensus-cadence change and is **gated** (not shipped
  in-place; the default `buildRoundMessage` is byte-unchanged).
- **Verify (primitive, additive):** `VerifyVRFProof` wraps `vrf.Verify`, fail-closed. Wiring the
  receive path to carry the vrf hash and reject unverified proofs is the gated step.
`Sequencer/committee_quorum.go:23-28` states the safety premise: the committee must be the same fixed
authenticated set on every node — today that agreement comes from the seed-signed snapshot, not from
each node recomputing this VRF. Remaining (gated): thread round + wire the receive-path verify.
Residual: the determinism fix assumes the upstream node list order (from the seed fetch) is itself
deterministic; if it is not, selection can still vary — verify that source order separately.

### CON-12 · compute quorum `n` from the authenticated snapshot, not local config (MEDIUM)
Sites: `messaging/consensus_hardening.go:167-184` (`block_buddy` blocklist + `max_validators`) →
`:346` (`n := len(committee)` after local trimming). A node that blocklists members silently
requires fewer votes than the fleet.
Fix: `n` = the authenticated snapshot committee size on every node; treat blocklisted members as
**non-voters** (numerator), never shrink the denominator. Verify: blocklisting a member does not
lower this node's threshold below the fleet's.

### CON-11 / CON-05-residual · BFT COMMIT proof binding (HIGH; behind CON-07)
`AVC/BFT/bft/security_helpers.go:41` (`DigestCommit` excludes `PrepareProof` → a relay can splice a
proof onto a validly-signed COMMIT); `checkAndMarkSeq` still runs before proof validation. Fix:
include `PrepareProof` in `DigestCommit`; validate `prepare.Round==msg.Round &&
prepare.BlockHash==msg.BlockHash`; move `checkAndMarkSeq` after proof validation. These live in the
BFT engine which is currently inert (CON-07: `NewBuddyService` never constructed) — fix **with**
CON-07/10/17 as one change, or the fixes activate dead code piecemeal.

## P2.5 · state fingerprint in the block header (the reproduced-divergence fix)
Because receivers independently apply and fast-synced nodes independently derive+vote, the block
header must carry a **canonical accounts+contract-state fingerprint** the committee signs. Each node
recomputes it after applying and **halts on mismatch** instead of serving wrong balances. In the
single-sequencer model this is the cheap, correct substitute for a full Merkle-Patricia trie root —
it *detects* divergence (which is all a single-sequencer chain needs), it closes B2/EVM-A1, and it
is the `stateFingerprint` term CON-02 folds into the block identity. **Do this early** — it is the
one that stops silent ledger divergence today, independent of contracts.

## Ordering
1. **P2.5 state fingerprint** (stops silent divergence; feeds CON-02).
2. **CON-01** (pin sequencer libp2p key, reject empty-key) + **CON-03** (authz on/fail-closed —
   partial landed c6c02ac; default-on flip coupled to CON-01's pin) — authorship anchor.
3. **CON-02** (block identity v3) — **versioned rollout**, flips fleet-wide.
4. **CON-04** (certify sync) — **DEFERRED (operator, 2026-08-18)**: catch-up/FastSync certification
   held for now; until wired, catch-up peers remain trusted infrastructure (document, do not assume).
5. **CON-06 / CON-12** (committee determinism + quorum sizing).
6. **CON-07 + CON-11 + CON-05-residual + CON-10 + CON-17** as one BFT-engine change.

## Gates (all consensus code)
Host CGO build (`../ThebeDB` present) + `go test`/`-race` + a **2-node determinism/authorship test**:
a forged-key certificate is rejected; a cross-height replay is rejected; two nodes applying the same
block produce the same state fingerprint; a mismatch halts rather than serves.

## Operator answers (2026-08-18) — folded in

**Q1 sequencer identity — there is NO sequencer key today; authorship = committee certificate only.**
Verified: ZKBlock proto has no signature/proposer field (Block/proto/block.proto:8-30); `SequencerID`
is a self-declared unsigned string used only for routing (Consensus.go:44, subscriptionService.go:
808-821); the only pin is `SeedAuthorityBLSPub`, which pins the seed authority for committee
SNAPSHOTS, not the sequencer. **So CON-01 is net-new, not hardening: choose an identity → add a
signature field → sign → verify → pin.** Chosen identity: the sequencer's BLS key already in the
committee snapshot (`CommitteeSnapshotEntry.bls_pub`) — already authenticated (PoP at registration,
epoch-frozen, covered by the authority signature), so pinning is free. **BLOCKER to verify first:**
the sequencer is a proposer, not necessarily a committee member, so its bls_pub may be absent from
the eligible set — confirm it has a registered entry (register a non-eligible identity entry, or
fall back to a pinned host key). CON-04's "verify the sequencer signature on synced blocks" is
likewise net-new (nothing to verify today).

**Q2 block-hash v3 — YES, versioned cutover. Qualifications (from the answer, all verified):**
1. Bump only the STREAM protocol `/broadcast/block/3.0.0` (side-by-side handlers, graceful). Do NOT
   bump the pubsub TOPIC `pubsub-block-propagation/2.0.0` — a topic-name change hard-partitions the
   fleet (no negotiation).
2. There are TWO live canonical-hash impls: `Security.RecomputeBlockHashFromContents` (re-derives tx
   hashes from contents — SAFE) and `messaging.RecomputeBlockHashFromTxs` (trusts claimed tx.Hash).
   v3's txns_root basis MUST be the Security (contents) one; collapse the two during v3.
3. Select v2/v3 by BLOCK HEIGHT (a cutover height in config), NOT build version — so a resync after
   cutover still validates historical (pre-cutover) blocks with the v2 preimage. Keep the v2 func.
4. Enforce the protocol/height gate BEFORE computing the hash, not after (a v2 node and a v3 node
   compute different hashes → reject each other on CheckBlockHash; the gate is what prevents a fork).
5. Check whether AVC/BFT PREPARE/COMMIT computes a block hash independently — if so it's a third
   impl needing the same cutover (not verified this session).

## CON-01 blocker RESOLVED (2026-08-18) — identity is the sequencer's libp2p key, not a committee bls_pub

The Q1 answer chose the committee-snapshot `bls_pub` as the sequencer identity, gated on confirming
the sequencer has a registered snapshot entry. **Traced the tree; the blocker is real, and it flips
the choice:**

- The sequencer is the PROPOSER, structurally distinct from the voting buddies. `NewConsensus` is
  invoked **only on the sequencer** (Sequencer/consensus_statemachine.go:145-153 comment) and it
  SELECTS the buddy committee, which **excludes self** (consensus_vote_authz.go:15;
  subscriptionService.go:828 "node is the sequencer itself — no non-self peer"). So the sequencer's
  presence in `CommitteeSnapshotEntry` is not guaranteed — the committee-bls_pub identity may simply
  be absent. Confirmed: **do NOT hang authorship on committee membership.**
- The sequencer ALREADY HAS an authenticated identity: its **libp2p identity key**. On startup it
  registers that key (`seednode.SetSequencerSignKey`, consensus_statemachine.go:150) and signs seed
  selection (`ListBuddy`) requests with it via `committee.SignSequencerRequest` (domain
  `jmdt/seed-auth/v1`, contracts.go:234-252). Likely: the seed pins the sequencer's peer_id as
  `SEQUENCER_PEER_ID` and refuses all other callers (based on the jmdn-side contract comment
  sequencer_listbuddy.go:63-68; not independently verified against the seed repo — to confirm, read
  seedNodes SequencerAuthenticator.Verify).

**Decision (net-new, single sequencer):** sign the block with the sequencer's **libp2p identity
key**. Add a `sequencer_sig` field to the block; the sequencer signs the v3 block hash (or its
preimage) with `host.Peerstore().PrivKey(host.ID())`. Verifiers **pin the sequencer peer_id** in
config (the same value the seed uses as `SEQUENCER_PEER_ID`, distributed OOB exactly like
`SeedAuthorityBLSPub`) and require the signature to verify against that peer_id — which turns today's
self-declared, unsigned `msg.SequencerID` (Consensus.go:44) into an authenticated field.
- **Why libp2p, not BLS:** the key is guaranteed to exist on the sequencer, is already the
  authoritative sequencer credential the seed enforces, and — Likely, for Ed25519/secp256k1/ECDSA
  libp2p identities — the pubkey is inlined in the peer_id (self-certifying), so verifiers derive the
  verify key from the pinned peer_id with no separate key distribution. To confirm: check the
  sequencer's identity key type (RSA identities hash rather than inline the key; jmdn default is
  Likely Ed25519). If RSA, distribute the sequencer pubkey alongside the pinned peer_id.
- **CON-04 falls out cheaper:** the same libp2p `sequencer_sig` travels with synced/catch-up blocks,
  so "verify the sequencer signature on synced blocks" reuses this one verifier.
- **Empty-key fail-closed still applies** to the *committee* auth path (keyAuthorized), unchanged —
  that guards vote/certificate authenticity, a separate anchor from block authorship.

Remaining CON-01 build (CGO + 2-node gated, not emitted blind): add the block `sequencer_sig` field
+ sign on propose + verify-against-pinned-peer_id on receive, on the `/broadcast/block/3.0.0`
handler alongside the v3 hash cutover.

## Primitives landed (DONE, dormant)
- **`consensushash.BlockHashV3`** (89ea5d6): domain-tagged keccak over chain+height+parent+state+txns
  +time, pure-Go, unit-proven (empty-block collision fixed, height/parent/state/chain/time binding,
  domain separation). NOT wired — the receive-path cutover (protocol 3.0.0 handler, height-selected
  v2/v3 in CheckBlockHash, collapse the two hash impls) is the CGO + 2-node-gated next step.
- **`consensushash.StateFingerprintV1` + `StateFingerprinterV1`** (9dd47e6, P2.5): canonical
  domain-tagged (`jmdn/state-fingerprint/v1`) keccak over full account + contract state; batch +
  streaming forms with identical encoding; 9 unit tests PASS CGO-off (order independence over 50
  shuffles, per-field binding, `""`==`"0"`, case-insensitive addr, account/contract section
  separation, unambiguous length-prefix framing, streaming==batch). It is the `stateFingerprint`/
  `stateRoot` term CON-02 folds in. NOT wired — recompute-after-apply + halt-on-mismatch + header
  carriage is the CGO + 2-node-gated step; it also supersedes the SHA-256/no-domain/accounts-only
  `DB_OPs.ComputeAccountStateFingerprint` operator diff tool.
