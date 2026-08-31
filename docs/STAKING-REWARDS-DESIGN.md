# Buddy Staking Rewards — Architecture & Implementation Plan

**Goal:** reward buddy nodes that participate in consensus by splitting each block's
coinbase gas share to their operator wallet address, proportional to the wallet's
on-chain balance ("stake"), with a baseline floor so a zero-balance participant
still earns. Penalising stays reputation-based (already implemented). Operators
declare a wallet address in `config.yaml`; the binding is immutable (like peer_id
/ alias). One address may back many nodes; a node cannot change its address.

## What already exists (cross-checked — this is mostly wiring, not new build)

- **Deterministic fee split:** `config.SplitFee(gasFee, coinbase, recipients []FeeRecipient{Addr, Weight})` (`config/gasfee.go`) splits the coinbase share by integer weight, exact-wei, canonical order. It is the SINGLE source of truth, already called on the live apply path (`messaging/BlockProcessing/Processing.go:927`) AND catch-up reconciliation (`DB_OPs/account_recon.go:117`). **No change needed.**
- **Block field:** `ZKBlock.FeeRecipients []FeeRecipient` (`config/ZKBlock.go:56`) is plumbed end-to-end but **never populated** (empty → single coinbase credit). Populating it = the feature.
- **Buddy participation per block:** `CertSigner{PeerID, PubKey, Signature}` — the buddies whose signatures the block carries. NOTE the cert in block N is over block **N-1** (`CertSigner` doc), so block N's signers certified N-1; block N rewards them.
- **Reward-address reporting:** `GetBlocksByRewardAddress` + SQL index already exist (`DB_OPs/thebegateway/reader.go:481`) — the schema anticipated per-block reward addresses.
- **Penalising:** `internal/reputation` (Event/Delta/ClassifyRound/DecayScore → `SelectionWeight` → seed `peer.Weights` → committee selection). Faults lower selection weight → dropped from committee. **Keep as-is; no slashing.**
- **Committee identity source:** the seed-authority-signed `CommitteeSnapshot` (peer_id → bls_pub), verified externally. This is where the reward-address binding rides (see seedNode changes).

## What is new
1. A wallet/reward address bound to each node (config + seed, immutable).
2. An authenticated buddy `peer_id → reward_address` map (extend the committee snapshot).
3. A deterministic weight = f(reward-address balance at parent state) with a floor.
4. Sequencer populates `block.FeeRecipients`; every node **validates** it.

---

## Design

### 1. Address binding (config + seed, immutable)
- `config.yaml`: `consensus.reward_address: "0x…"` (a 20-byte hex address). Validated with `common.IsHexAddress` at boot; empty = node does not claim rewards (still participates, just no fee weight).
- Registration: the node sends its `reward_address` to the seed at registration/heartbeat, signed by its identity key (reuse the existing signed peer-record path — the identity signature already covers the payload). The seed stores it and **rejects any later change** to a different address (first-write-wins, exactly like alias/bls_pub uniqueness in `enforceCommitteeKeyBinding`). One address MAY map to many peer_ids; a peer_id maps to exactly one address forever.
- The seed includes `reward_address` in each `CommitteeSnapshotEntry` and re-signs the snapshot (the snapshot is already committee-authenticated) — so the mapping is external, authenticated, and fleet-agreed.

### 2. Reward-map resolution (jmdn, read side)
- Extend jmdn's committee-snapshot mirror (`seednode/committee/contracts.go` `CommitteeEntry`) with `RewardAddress` and include it in the canonical bytes (byte-exact with the seed). Add `RewardAddrByPeer() map[string]common.Address` (like `BLSPubByPeer`).
- The eligibility/committee source already resolves the authenticated snapshot; expose the peer→reward-address map from it.

### 3. Weight = deterministic balance snapshot at PARENT state (the hot spot)
- For block N, the weight of each rewarded buddy is derived from its reward address's **balance as of block N-1's committed post-state** — the parent tip every node already has after applying N-1. This is deterministic and non-circular (N-1's post-state is fixed before N is built). **Never** read balances at "wall-clock consensus start" — that is non-deterministic across nodes and forks.
- `FeeRecipient.Weight` is `uint64`; balances are wei (`big.Int`). Scale deterministically, fleet-uniform:
  `weight_i = BaselineWeight + min(WeightCap, balance_i_wei / WeightScaleWei)`
  with constants (e.g. `BaselineWeight=1`, `WeightScaleWei=1e18` i.e. per whole JMDN, `WeightCap` chosen to bound uint64 and whale dominance). `BaselineWeight ≥ 1` guarantees a **zero-balance participant still earns** a share. All three are consensus constants in `config/` (identical network-wide; changing them is a coordinated fork).
- Balance reads on this path MUST be fail-closed (reuse the EVM-A16 sticky-DBError pattern just landed): a read error aborts block build/validation, never silently yields 0.

### 4. Sequencer block build (populate FeeRecipients)
When building block N (sequencer only, gated by a new `consensus.reward_split_enabled` flag, default OFF):
1. Take the `CertSigner` set carried in N (the buddies who certified N-1).
2. Resolve each signer's `reward_address` from the authenticated snapshot. **The address is OPTIONAL: a signer with no bound address is simply OMITTED from `FeeRecipients`.** Its share is therefore redistributed among the signers that DO have an address (this is native `SplitFee` behavior — it splits the coinbase share across whatever recipients are present, by weight). If NO signer in the block has a bound address, `FeeRecipients` stays empty → single coinbase credit (historical fallback).
3. Read each address-having signer's reward-address balance at the parent (N-1) committed state; compute `weight_i` per §3.
4. Aggregate by address (one address backing several signers sums their weights), sort canonically, set `block.FeeRecipients` = the address-having subset only.

### 5. Validation on receive (consensus-safe — non-forgeable)
Every node, on the block-validation path (alongside the existing body/cert checks in `messaging/blockPropagation.go`), **recomputes the expected `FeeRecipients`** from `(block's CertSigners, authenticated reward-address map, parent-state balances, the §3 constants)` and rejects the block if it does not match the sequencer's `FeeRecipients` (fail-closed). This closes the redirect attack: a cheating sequencer that points fees at itself is rejected fleet-wide, because recipients are a pure function of already-agreed inputs, not the sequencer's free choice.

### 6b. Sync consistency — persist the FROZEN split (CONSENSUS-CRITICAL, R6)
The weight is balance-derived, but a syncing node must NEVER recompute it from
current (tip) balances — a reward address's balance changes over time (including
from the rewards themselves), so recomputing against the tip diverges. The split
is therefore FROZEN into the block: `FeeRecipients` carries `(address, weight)`,
and every apply path (live gossip AND ThebeSync catch-up) applies it verbatim via
`SplitFee` — no recomputation during sync.

For that to hold, the carried `FeeRecipients` MUST survive storage:
`DB_OPs/backend/block.go` `toBlockRecord` persists it to `extra_data.fee_recipients`
(JSON), and `DB_OPs/thebe_conversions.go` `blockRecordToZKBlock` rehydrates it —
exactly like `account_nonces`/`committee_certificate`. Without this, a
stored/served block returns empty `FeeRecipients` and a node syncing from it (or
restarting) applies NO fee credits → balances diverge from live-applied nodes.
(Done in R6.) Sync-path tamper protection is the P2.5 `StateFingerprint`: a
tampered `FeeRecipients` yields a different post-apply fingerprint → HALT (when
contracts/fingerprint are enabled).

### 6. Distribution + persistence + reporting (existing)
- Distribution: unchanged — `SplitFee` on apply (`Processing.go:927`) and recon (`account_recon.go:117`) consume `block.FeeRecipients`. Crediting a zero-balance address works (merge_account creates/updates), satisfying "0 JMDN still gets reward."
- Persistence: ensure `FeeRecipients` is persisted with the block (it's a `ZKBlock` field → `toBlockRecord`; confirm it lands in a column/ExtraData that `GetBlocksByRewardAddress` reads). The reporting query already exists.

### 7. Penalising (unchanged)
Keep `internal/reputation`: faults → lower reputation → lower `SelectionWeight` → dropped from committee. A buddy that is eligible but does NOT sign simply is not a `CertSigner` in the block, so it earns nothing that block (reward-by-participation) — a natural, fund-safe penalty with no new mechanism.

---

## Determinism & security invariants (must hold or the chain forks / fees are stealable)
1. Balance snapshot = parent (N-1) committed post-state, never wall-clock. (§3)
2. Weight scaling constants are fleet-uniform. (§3)
3. Reward-address map comes only from the authenticated, seed-signed snapshot. (§2)
4. `FeeRecipients` is recomputed and validated on receive; mismatch = reject. (§5)
5. Balance reads on this path are fail-closed. (§3)
6. Aggregation + ordering are canonical (address-sorted), matching `SplitFee`.

## Config (jmdn)
```yaml
consensus:
  reward_address: "0x…"        # this node's operator wallet (immutable once registered)
  reward_split_enabled: false  # master switch (sequencer populates + all nodes validate); default OFF
```
Constants (`config/`, consensus-critical, network-uniform): `BaselineWeight`, `WeightScaleWei`, `WeightCap`.

## Phased tasks (jmdn)
- **R1 — config + binding:** `reward_address` config + `IsHexAddress` validation; send it in the signed peer registration/heartbeat to the seed.
- **R2 — snapshot mirror:** extend `CommitteeEntry`/canonical bytes with `RewardAddress`; `RewardAddrByPeer()`. (Byte-exact with seed — coordinate via the interop vector.)
- **R3 — weight fn:** `config` constants + `StakeWeight(balanceWei) uint64` (baseline+scaled+cap), pure & tested.
- **R4 — populate (sequencer):** build `FeeRecipients` from cert signers + parent-state balances, gated by `reward_split_enabled`. Fail-closed balance reads.
- **R5 — validate (all nodes):** recompute expected `FeeRecipients` on receive; reject mismatch.
- **R6 — persist/report:** confirm `FeeRecipients` persists and `GetBlocksByRewardAddress` returns them; add an explorer/RPC read if wanted.
- **R7 — tests:** deterministic weight vector; validation reject on tampered recipients; zero-balance earns baseline; recon == apply parity.

## seedNode changes (summary — full handover is a separate doc)
- Store `reward_address` per peer; immutable (first-write-wins, reject changes), like alias/bls_pub.
- Add `reward_address` to `CommitteeSnapshotEntry` + canonical committee bytes; re-sign.
- Registration validation: address format; identity signature already covers it.
- Bump the committee-snapshot version if the canonical bytes change (coordinate with jmdn's mirror + a fresh interop vector).

## Enablement preconditions (enforced / to verify before flipping the switch)
- **M2b hash binding is REQUIRED with reward-split (enforced at boot).** R5
  recomputes `FeeRecipients` from the block's `PrevAggCert`; that is only
  tamper-evident when `PrevAggCert` is bound into the block hash, which happens
  only under M2b (`JMDN_M2B_HASH=1`, `Security.M2bHashEnabled`). With M2b off a
  relay could rewrite `(PrevAggCert, FeeRecipients)` consistently and the split
  would be accepted (a relay running a registered buddy could inflate its share).
  main.go therefore **refuses to start** with `reward_split_enabled` on and M2b
  off. Enable both together, network-wide.
- **Reward coverage follows `PrevAggCert`.** `PrevAggCert` is populated only on
  fold-window blocks (and only when the aggregate-cert path is active), so with
  the current wiring rewards accrue on those blocks; off-window blocks derive an
  empty split (single coinbase credit). Confirm this cadence is intended, or
  broaden the signer source, before rollout.
- **Catch-up threading:** the historical interlock cited catch-up not crediting
  `FeeRecipients`. That was FastsyncV2 (retired); ThebeSync catch-up routes
  through the same `ProcessBlockTransactions` → `SplitFee`. Confirm on a 2-node
  catch-up test before enabling.

## Risks / open items
- **Canonical-bytes change is a coordinated wire change** (jmdn mirror + seed must match byte-for-byte, and the committee snapshot signature covers the new field). Version-bump + interop vector required.
- **Whale dominance / fairness:** balance-proportional weight favors rich addresses; `WeightCap` + `BaselineWeight` bound it. Revisit if undesired.
- **No-address signers (DECIDED):** the address is optional. A signer with no bound address is omitted from `FeeRecipients`; its share redistributes among the address-having signers (native `SplitFee`). If none have an address, the coinbase gets the whole share. This is deterministic and validated like any other recipient set. A no-address node still participates in consensus and is still subject to reputation-based selection penalties — it just has no fee economics.
- **Balance snapshot cost:** N balance reads per block on build + validate; cache the parent-state reads.
- **Not slashing:** economic penalty is only forgone reward + reputation-driven ejection; no stake is at risk. Acceptable per decision; note for security posture.
