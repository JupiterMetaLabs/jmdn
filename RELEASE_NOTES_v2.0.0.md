# JMDN v2.0.0

**Consensus and block‑propagation hardening.**

This is a major release that reworks how the network authenticates its validator
committee, scopes and verifies consensus votes, and propagates finalized blocks.
It introduces **coordinated, breaking protocol changes** and **must be deployed
network‑wide** — nodes running an earlier version will not interoperate. Please
read the *Upgrade guide* before rolling out.

---

## Highlights

- **Authenticated, epoch‑pinned validator committee.** Eligibility now comes from
  a committee snapshot signed by a pinned authority key, with each peer identity
  cryptographically bound to its committee BLS key.
- **Block‑bound voting.** Every consensus vote is scoped to exactly one block, on
  one chain, at one height — closing cross‑chain and cross‑height replay.
- **A single, authenticated certificate verifier** enforces a Byzantine
  supermajority (`ceil(2n/3)`) over the authenticated committee size for **any**
  committee size, so every consensus path applies the same threshold.
- **Gossip block propagation (now the default transport).** Finalized blocks reach
  the fleet over a gossip topic every node subscribes to, so delivery no longer
  depends on a direct connection to the producing node. The redundant direct
  per‑peer fan‑out is off by default (re‑enable with `consensus.p2p: 1`); whichever
  transports run share one fail‑closed admission gate and one exactly‑once apply.
- **Fail‑closed everywhere.** Remote blocks are fully validated before they are
  forwarded, processed, or persisted; a node with no valid committee source, or
  that is not caught up, declines to participate rather than acting on unverified
  state.

---

## ⚠ Breaking changes

- **P2P protocol and topic versions bumped to `2.0.0`.** Consensus and
  block‑propagation topics are incompatible with earlier versions.
- **Votes use a single versioned domain** binding the network chain id, the block
  height, and the canonical block hash. Earlier vote formats are no longer accepted.
- **Committee size 7, quorum `ceil(2n/3)`.** The voting committee is sized by
  `consensus.max_validators` (default 7); the Byzantine threshold is derived from
  the authenticated committee size and is correct at any `n`.
- **Peer records carry a committee BLS key.** Registration advertises `bls_pub`
  with a proof of possession, and the identity signature covers `bls_pub`
  (`JMDN_EMIT_COMMITTEE_BLS`, default on).
- **New `consensus.*` settings** — `committee_epoch_seconds`, `max_validators`,
  `block_buddy`, `p2p`, and the pinned committee authority key. These are operator
  settings and are **not** shipped in `jmdn_default.yaml`.
- **The peer‑to‑peer file‑transfer protocol has been removed**, along with its
  handler and the CLI `SendFile` surface.

---

## Added

- **Authenticated committee snapshots** verified against a pinned authority public
  key before they define the eligible validator set, with golden‑vector tests over
  the canonical encoding.
- **BLS proof of possession** binding a committee key to a peer identity.
- **Authenticated committee‑selection requests**, signed with the node's libp2p
  identity key and carried in request metadata.
- **Committee eligibility resolution on validator nodes**, with a short‑lived cache
  of the last verified snapshot to ride out transient unavailability.
- **Finalized‑block gossip propagation** (`JMDN_BLOCK_GOSSIP`) — now the default
  block transport, running the same fail‑closed admission gate as direct‑stream
  blocks; direct per‑peer fan‑out is opt‑in via `consensus.p2p`.
- **Observe‑only validator reputation model** with alert wiring.
- **Fee‑recipient groundwork** — a `FeeRecipients` block field and a shared,
  deterministic `SplitFee` helper (exact, order‑independent distribution; an empty
  set reproduces existing behaviour). The field is not yet part of the canonical
  block hash, so blocks carrying it are not admitted until that binding lands.
- **Durable equivocation records** that survive a process restart.
- **`genmnemonic`** utility; VRF selection material is sourced from config/env.
- **Richer operational signal** — block‑rejection alerts carry the concrete reason,
  the finalized‑buddies alert reports each peer's latest block, and committee
  authorization and registration paths log actionable diagnostics.

---

## Security

- **Block‑bound voting** — each vote is scoped to exactly one block, on one chain,
  at one height.
- **Single authenticated certificate verifier** — one shared verifier enforces the
  Byzantine quorum over the authenticated committee size, counting one vote per
  validator (de‑duplicated by both peer identity and committee key); every
  consensus path routes through it.
- **Authenticated committee membership** — eligibility comes from the signed epoch
  snapshot with the peer‑identity‑to‑committee‑key binding enforced at the tally; a
  node requires a valid, current committee source to take part in consensus and
  declines when one is unavailable.
- **Canonical body binding** — a received block must recompute to its claimed block
  hash and transaction root, and each transaction hash is verified against its
  contents, binding a certificate to the exact transaction set it attests.
- **Committee snapshot freshness** — snapshots outside the current epoch window are
  declined.
- **Fail‑closed block admission** — remote blocks are fully validated before they
  are forwarded, processed, or persisted, and a block is recorded in the duplicate
  filter only after it validates.
- **Chain linkage with authenticated catch‑up** — parent hash, height, and state
  root are checked against local state; height gaps route to authenticated
  catch‑up, and a node trailing the head beyond a configurable bound abstains from
  voting (while a transient inability to read its own tip fails open, so a read
  hiccup does not stall the network).
- **Bounded network reads** — the direct block‑propagation stream and the gossip
  topic enforce the same maximum message size and a read deadline.
- **Signed BFT engine messages** — PREPARE/COMMIT are signed and verified.
- **Authenticated vote‑result requests** at the stream boundary (opt‑in).
- **Provisioned signing keys** — vote signing uses the node's provisioned BLS key
  and never auto‑mints one; `JMDN_BLS_KEY_FILE` overrides the default path.
- **Stricter transaction field validation** — negative numeric fields are declined
  at ingress, on remote admission, and at execution.
- **Operator‑supplied VRF selection material** is required for node and committee
  selection.

---

## Changed

- **Sync monitoring** tightens the propagation‑lag tolerance so a node that stops
  advancing triggers catch‑up promptly.
- **`block_number` is a plain `uint64`** across the vote‑request path.
- **Documentation consolidated** — stale per‑folder `README` files removed in
  favour of the top‑level docs; internal code comments tidied.
- **Dead code removed** — an unused fallback path, file‑transfer metrics, and an
  unused parsed‑transaction field.
- **Block propagation defaults to gossip‑only.** The redundant direct per‑peer
  fan‑out is disabled unless `consensus.p2p: 1` (or
  `JMDN_DIRECT_BLOCK_PROPAGATION=1`) is set; gossip already reaches the whole fleet,
  and sending the same block over both transports was the source of the duplicate
  apply addressed by the exactly‑once fix under **Fixed**.

---

## Fixed

- **Consensus vote requests** carry `block_number` as a numeric value on both
  sides, so vote collection is unaffected by JSON type coercion.
- **Vote aggregation** continues through a peer‑weights authorization denial
  instead of aborting the round.
- **Quorum is computed over the main voting committee** — the peers that actually
  vote — so the threshold matches the voting set.
- **Deterministic block application** — a finalized block is applied
  all‑or‑nothing: any transaction error (including a stale nonce) rolls the block
  back so every node applies the identical transaction sequence, with catch‑up
  re‑applying the block.
- **Exactly‑once block application** — applying a finalized block is serialized per
  block hash, so a block delivered over more than one path (e.g. gossip and direct,
  or a re‑gossip) is applied once instead of concurrently. This closes an
  account‑balance divergence in which the same block's transactions could be
  credited twice.
- **Committee selection** requests the full eligible pool and is pinned to the
  signed committee.
- **Peer registration** refreshes the committee BLS key for existing aliases.
- Normalize propagated account state on receipt.
- `eth_*` transaction marshaling/handler corrections.

---

## Dependencies

- Bump `golang.org/x/crypto` 0.49.0 → 0.52.0.
- Upgrade `mattn/go-sqlite3` to v1.14.48.

---

## Upgrade guide

1. **Deploy network‑wide.** The protocol version, vote domain, and registration
   payload changes are not backward compatible; nodes on earlier versions will not
   interoperate. Coordinate the cutover across the fleet.
2. **Align network parameters** — identical `chain_id` and
   `committee_epoch_seconds`, NTP‑synced clocks, and a persistent
   `config/bls.json` provisioned on each node **before** registration.
3. **Set the production committee authority key** before going live.
4. **Committee sizing** — ensure at least the quorum count of the `n = 7` voting
   committee is reachable (5 of 7), or lower `consensus.max_validators` to match
   the deployment.
5. **Bootstrap** from the current snapshot (see `DOCKER.md` / `bootstrap_sync.sh`)
   and set `catch_up_from_block` to match the snapshot tip.

### Rollback levers

- Clear the pinned committee authority key to return to the previous eligibility
  source.
- `JMDN_ENFORCE_SYNC_GATE=0` disables the consensus vote sync‑gate.
- `consensus.p2p: 1` (or `JMDN_DIRECT_BLOCK_PROPAGATION=1`) re‑enables direct
  per‑peer block propagation alongside gossip; leave at `0` for gossip‑only.
- `JMDN_BLOCK_GOSSIP=0` disables the gossip fan‑out. The receive/broadcast guard
  then falls back to direct propagation, so a block always has a delivery path.

---

## Supported versions

| Version | Supported |
|---------|-----------|
| v2.0.x  | ✅ Active |
| v1.x    | ❌ No |

Report security issues per `SECURITY.md`.
