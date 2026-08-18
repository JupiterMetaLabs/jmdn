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

### CON-03 · vote-requester authz default-on, fail-closed (CRITICAL)
Sites: `AVC/.../consensus_vote_authz.go:20` (`enforceVoteRequesterAuth` env, default false), `:87`
(`if !enforce { return true }`), `:97` (empty buddy set → "fail open" → true).
Fix: default the gate ON; anchor on the authenticated snapshot, not the cached buddy list; **abstain**
on an unknown set (abstaining is safe; signing for an unknown caller is not). Verify: with the env
unset, an unauthenticated vote-result request is refused; an empty set abstains, never signs.

### CON-04 · certify catch-up / FastSync blocks (CRITICAL)
Sites: `FastsyncV2/catchup.go:70` (`HandleCatchUpSync` writes blocks with no `VerifyCertificate` /
`checkEquivocation`); the fastsync wire has no `bls_results` field. A fresh node's entire chain is
taken on trust from its sync peer.
Fix (single-sequencer makes this cheaper): transport the sequencer signature (or the committee
certificate) with each synced block and verify it on apply; anchor sync to a sequencer-signed
checkpoint. Until then, catch-up peers are **trusted infrastructure** and that must be documented,
not implicit. Verify: a synced block with an absent/invalid signature is rejected.

### CON-06 · deterministic committee selection + verify the VRF proof (HIGH)
Sites: `AVC/NodeSelection/pkg/selection/vrf.go:104` (`buildRoundMessage` = `"<nodeID>:<salt>"` — no
round/epoch/height → constant VRF output forever), `filter.go:88` (`selectWithRegionDiversity`
iterates a Go map → non-deterministic committee, run-to-run), `vrf.Verify` called nowhere.
Fix: include round/epoch/height in the VRF message; replace map-iteration selection with a total
order (sort by VRF output then peer_id); **call `vrf.Verify`** and reject an unverified proof.
`Sequencer/committee_quorum.go:23-28` states the safety premise: the committee must be the same
fixed authenticated set on every node — this finding is what breaks that. Verify: identical inputs →
identical committee across runs; a round changes the VRF output; a bad proof is rejected.

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
2. **CON-01** (pin key, reject empty-key) + **CON-03** (authz on/fail-closed) — authorship anchor.
3. **CON-02** (block identity v3) — **versioned rollout**, flips fleet-wide.
4. **CON-04** (certify sync).
5. **CON-06 / CON-12** (committee determinism + quorum sizing).
6. **CON-07 + CON-11 + CON-05-residual + CON-10 + CON-17** as one BFT-engine change.

## Gates (all consensus code)
Host CGO build (`../ThebeDB` present) + `go test`/`-race` + a **2-node determinism/authorship test**:
a forged-key certificate is rejected; a cross-height replay is rejected; two nodes applying the same
block produce the same state fingerprint; a mismatch halts rather than serves.

## One input needed to start CON-01/02
- **Sequencer signing identity:** does the sequencer already sign each block with a pinned key
  (which key/where), or is authorship established only via the committee certificate today? CON-01's
  "pin" target and CON-04's "sequencer signature on synced blocks" both depend on this.
- **Block-hash v3 rollout:** OK to introduce `/broadcast/block/3.0.0` + a v3 block-hash preimage
  (height+parent+state) as a fleet-wide versioned cutover (old nodes stay on 2.0.0 until migrated),
  the same non-breaking pattern used elsewhere?
