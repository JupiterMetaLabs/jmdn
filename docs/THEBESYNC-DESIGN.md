# ThebeSync (FastSync v4) — Design

**Status:** design for review — no engine code yet (Track B is design-gated).
**Supersedes:** the FastsyncV2 / JMDN-FastSync multi-phase engine (Merkle bisection +
per-account reconcile), which was shaped for ImmuDB's key/SeekKey model.
**Branch context:** `feat/thebe-sc-layer`. Author env: no Go toolchain/CGO — every build
phase is host-verified.

---

## 1. Why redesign

The current engine syncs by **comparing state** across nodes: build a Merkle/MMR accumulator,
bisect to find divergent block ranges, fetch headers/data, then **recompute account balances by
replaying transactions** (reconcile). That shape fits ImmuDB (a verifiable key/value store with
its own Merkle proofs and SeekKey scans). It is heavy and divergence-prone on ThebeDB, where
state is already a **single append-only canonical log** with a monotonic sequence and a hash
chain — exactly the structure log-shipping replication is built for.

ThebeSync replaces "diff the state, then reconcile" with "**ship the missing tail of the log,
verify it, replay it**." No Merkle bisection, no account-set ART diff, no reconcile pass.

---

## 2. ThebeDB primitives it builds on (verified in-tree)

- **`core.CanonicalRecord{Seq, Namespace, Type, Value []byte, Timestamp}`** — the atomic log
  unit. Every block/tx/account/receipt write is one record (`pkg/core/record.go`).
- **Append-only log with monotonic seq** — `__sys:log:<seq>`, `__sys:seq`, and a hash chain
  `__sys:hash:<seq> = SHA256(prevHash ‖ record)`, head at `__sys:chain_head` (seq+hash).
  Immutability verified separately: history is never rewritten.
- **`Store.Iterate(startSeq, fn(seq, data))`** — walks `__sys:log:<seq>` from `startSeq` to the
  end. This is the range-server for shipping.
- **Projection registry** — `thebeprofile.JMDNProfile.handlers[namespace] → applyFunc(ctx, seq,
  record, *sql.Tx)` maps each of the 8 namespaces (`account`, `block`, `tx`, `zk`, `snapshot`,
  `l1_finality`, `contract_receipt`, `contract_registry`) to the SQL projection. **Replaying a
  shipped record through this is byte-identical to how it was first written.**
- **Cursor** — the sync anchor (`sync:accounts_last_applied_block`) already tracks how far a node
  has applied; ThebeSync generalises it to a **seq (and height) high-water mark**.
- **Block-level verification primitives (verified in tree):** `BlockHash` = Keccak256 over tx
  hashes and `TxnsRoot` = SHA256 Merkle root, both recomputable from txs
  (`messaging.RecomputeBlockHashFromTxs`/`RecomputeTxnsRoot`); `StateRoot_n = Keccak256(StateRoot_{n-1}
  ‖ BlockHash_n)` (`stateRootChain`) — a state-root hash chain; genesis is the baked-in constant
  `DB_OPs.GenesisBlockHash`.
- **Advisory `ZKBlock` fields must be `ExtraData`-persisted for sync fidelity (rule).** Three fields are
  advisory (not consensus-hashed) and were originally live-path-only — they ride on the in-memory
  gossip block and were dropped by `StoreZKBlock`, so a block *read back from storage and shipped by
  ThebeSync* lost them, breaking catch-up. All three are now persisted via
  `BlockRecord.ExtraData` (write in `toBlockRecord`, read in `blockRecordToZKBlock`):
  `committee_certificate` (§4, cert re-verify), `state_fingerprint` (P2.5 halt), and
  `account_nonces` (canonical ART identities — without them, catch-up fails on the first
  new-account/contract-deploy block). **Any future advisory field the apply path reads must be added to
  this ExtraData round-trip**, or ThebeSync (which serves from storage, not the in-memory block) will
  ship it empty. The gossip path is unaffected because it carries the in-memory block directly.

---

## 3. Core idea

A joining/lagging node knows its last applied seq `S`. It asks a peer for the log tail
`[S+1 .. head]`, verifies it, appends it to its own canonical log, and projects it to SQL. The
**seq is the cursor** — sync is a resumable range copy, not a state diff.

Because both nodes build the log in the same consensus order (blocks by height, with their
derived records interleaved), a receiver that appends the tail **in seq order** reproduces the
identical seqs and the identical hash chain — so integrity is verifiable end-to-end by comparing
the receiver's recomputed `chain_head` hash to the peer's advertised one.

---

## 4. Trust model — HYBRID, boundary detected from the table

**Decision: hybrid — and the boundary is a per-block property read from the table, not a pinned
height.** No `cert_activation_height` and no `genesis_hash` config knob (see below).

**Blocking discovery that shapes this (verified in tree).** The committee certificate is **not
persisted**. It rides on the ephemeral gossip envelope `BlockMessage.Data["bls_results"]`
(`messaging/blockPropagation.go:593`), is verified once on receipt by `VerifyCertificate`, then
**dropped** — `StoreZKBlock` persists only `config.ZKBlock`, which has no certificate field
(`config/ZKBlock.go`). So a sync server today **cannot re-present a certificate for any stored
block.** This is exactly why the chain has "old blocks with no certification": *no* block is
currently shippable-with-cert.

Consequently, the hybrid boundary is: **does this stored block carry a persisted certificate?**

- **Prerequisite (P-cert):** start persisting `bls_results` per block going forward — a new,
  cert-namespace record (or a column) written atomically with the block. Blocks produced before
  P-cert lands have no stored cert → they are the permanent **legacy prefix**. Blocks after it
  carry one → the **certified suffix**. The boundary is therefore *self-describing in the data*
  and needs no pinned height — answering "can't we detect it when we read the blocks?": **yes,
  detect cert-presence per block.**

**Certified block (stored cert present).** Verify `bls_results` with the existing
`VerifyCertificate` (2f+1 over the authenticated committee), recompute block-hash / tx-root from
the carried txs, apply via `ProcessBlockTransactions`, recompute P2.5 fingerprint, halt on
mismatch. A block whose height is above the first-seen stored cert but which arrives *without* a
cert is **rejected** (the "once certified, always certified" monotonic rule — also read from the
table, not from config), so a peer cannot strip a cert to downgrade a block into the legacy path.

**Legacy block (no stored cert — the old prefix).** Verified without a per-block cert, by
anchoring + re-derivation:
1. **Genesis anchor (head):** block 0's hash must equal the baked-in constant
   `DB_OPs.GenesisBlockHash` (`= Keccak256("jmdn/genesis-block/v1")`). The node already knows its
   own genesis independently of any peer — so **no `genesis_hash` config knob is needed**; reuse
   the constant. A peer cannot substitute a different genesis.
2. **State-root / hash chain:** recompute each block's hash and tx-root from its txs (integrity)
   and verify the persisted `StateRoot` chains from the parent —
   `StateRoot_n == Keccak256(StateRoot_{n-1} ‖ BlockHash_n)` (the generator's rule, already
   enforced on the live path by `stateRootChain`/`linkageDecision`). This links every legacy
   block to its parent with no gaps.
3. **First-cert tail anchor:** the first *certified* block (the lowest height with a stored cert)
   has a `PrevHash` that commits to the legacy block below it. So the 2f+1 committee signature at
   that first certified height transitively vouches for the entire legacy prefix via the hash
   chain — the prefix is pinned at both ends (genesis + first cert) and linked in between.
4. **Re-derive + fingerprint:** apply through `ProcessBlockTransactions`; recompute P2.5; halt on
   mismatch.

Tampering with any legacy block breaks the genesis linkage (head), the state-root chain, the
`PrevHash` the first certificate commits to (tail), or the P2.5 fingerprint — all fail closed.

**Authenticity gap that cannot be detected from the table.** The hash chain proves *internal
consistency*, not *authenticity*: without any stored cert, a synced node cannot itself prove the
tip is the canonical committee-certified tip. Two mitigations, in priority order:
- **After P-cert:** require the synced **tip** to carry a verifying stored cert. Once cert
  persistence is live this is always true, and the tip cert anchors the whole chain down to
  genesis — no external input needed.
- **Before P-cert / for the all-legacy bootstrap:** sync only from **seednode-vetted peers** (the
  existing catch-up monitor already selects these, per `consensus_hardening.go`), and/or accept a
  **seed/committee-signed checkpoint `(height, StateRoot)`**. This is the one anchor that comes
  from the trusted layer, not the block table. Guard the pure-legacy case behind
  `sync.allow_uncertified_bootstrap` (default false) so it can't silently become the normal trust
  level once P-cert is live.

Why not the alternatives: pure Option A is impossible today (no stored certs); pure Option B
(trust peer bytes) does no re-derivation. This hybrid re-derives + fingerprints every block,
detects the trust boundary from the data, and reduces the old-block trust surface to "baked-in
genesis + the first real committee cert + vetted-peer/checkpoint tip anchor."

---

## 5. Protocol sketch

Four steps; PoTS-style tail catch-up folded in. Sender serves from `Iterate`/block store;
receiver applies through `ProcessBlockTransactions`.

```mermaid
sequenceDiagram
    participant R as Joining node (receiver)
    participant P as Peer (sender)
    R->>P: Head(): my lastAppliedHeight = H
    P-->>R: head height Hp, chain_head hash, committee epoch
    loop batches until caught up
        R->>P: GetBlocks(from, to)  (bounded batch)
        P-->>R: ZKBlocks [from..to] (+ carried txs, stored cert if any)
        Note over R: no stored cert: genesis anchor + stateroot chain + first-cert anchor
        Note over R: stored cert: VerifyCertificate (2f+1) + recompute blockhash/tx-root
        Note over R: apply via ProcessBlockTransactions -> KV append + SQL project
        Note over R: recompute P2.5 fingerprint; HALT on mismatch
        R->>R: advance seq/height cursor (crash-safe)
    end
    R->>P: PoTS: blocks produced during sync?
    P-->>R: gap blocks -> apply same path
    Note over R: fingerprint == peer head -> synced
```

- **Head/availability handshake:** reuse the availability probe (peer reports head height +
  `chain_head`; authenticate the committee epoch so a stale/hostile peer is rejected).
- **Range fetch:** bounded batches (like HeaderSync's 1500 / DataSync's 30), 3 concurrent workers
  with failover — this concurrency machinery from the current engine is worth keeping.
- **Apply:** each block through `ProcessBlockTransactions` under the apply lock — same path,
  same fingerprint halt.
- **PoTS tail:** blocks produced while syncing are caught up the same way (the existing PoTS WAL
  idea still applies; simpler here because it's the same "apply more blocks" path).

---

## 6. Atomicity, idempotency, resumability

- **Append-before-project / WAL-first:** a block's KV append + SQL projection commit atomically
  (the gateway 2PC we already use); the seq/height cursor advances only after commit. A crash
  mid-sync resumes from the last committed cursor — no gaps, no double-apply (the tx-processed
  markers + monotonic `latest_block` writer already enforce this).
- **Idempotent:** re-fetching an already-applied range is a no-op (cursor + markers).
- **Contiguity:** apply strictly in height order; a gap triggers authenticated catch-up, never
  acceptance (mirrors the live path's contiguity rule).

---

## 7. What it removes vs the current engine

- MMR/Merkle accumulator build + bisection (`core/protocol/merkle`, `priorsync`).
- The account-set ART diff + SwappableART segment machinery for AccountSync.
- The per-account reconcile replay (`reconsillation/`, `FastsyncV2/reconcile_local.go`).
- The "compare two states" model entirely — replaced by "copy the certified tail + re-derive".

Kept/reused: libp2p transport + framing, bounded concurrent workers + failover, availability
handshake, heartbeat keepalive, the apply path, the profile projector, the P2.5 fingerprint,
and the `local-2node-gate` harness as the acceptance test.

---

## 8. Packages / interfaces

**Module split (decided): the engine lives in the `JMDN-FastSync` module, imported by jmdn.**
`JMDN-FastSync` must not import `gossipnode/*` (jmdn imports it → a reverse import is a build
cycle), so the engine is generic and **block-format-agnostic: blocks cross the wire as opaque
bytes**. All parsing, consensus verification, and apply stay in jmdn behind two interfaces.

New (JMDN-FastSync module, branch `feat/thebesync-v4`, package `thebesync/`):
- `wire.go` — protocol IDs (`/fastsync/v4/head`, `/fastsync/v4/getblocks`), request/response
  (`GetBlocksResponse.Blocks [][]byte` opaque), and the `BlockProvider` / `BlockApplier` interfaces.
- `server.go` — `Server{Provider}` with `HeadHandler` / `GetBlocksHandler` (read-only).
- `client.go` — `FetchHead` / `FetchBlocks` (return opaque bytes).
- `receiver.go` — `Receiver{Applier}.SyncFrom` — the head-handshake + bounded fetch + apply loop.
- `stream.go` — newline-delimited JSON framing.

New (jmdn, package `gossipnode/thebesync`):
- `provider.go` — `Provider` implements `BlockProvider` over `DB_OPs` (serves `json.Marshal(ZKBlock)`).
- `applier.go` — `Applier` implements `BlockApplier`: parse opaque bytes → `config.ZKBlock`.
- `apply.go` — the hybrid verify + apply (body binding, committee cert via
  `messaging.VerifyCertificate`, linkage) → `ProcessBlockTransactions` → `StoreZKBlock` →
  `UpdateLatestBlockMonotonic`. jmdn owns ALL consensus verification.
- `node/node.go` registers `fssync.Server{Provider: thebesync.Provider{}}` handlers. `go.mod`
  adds `replace github.com/JupiterMetaLabs/JMDN-FastSync => ../JMDN-FastSync` for local dev.

Reuse (no change): `ProcessBlockTransactions` (apply), `thebeprofile` (projection),
`consensushash` (fingerprint + cert verify inputs), `messaging.VerifyCertificate` (authenticity),
`sync_anchor` (cursor).

Retire: `FastsyncV2/` and the jmdn dependence on the multi-phase `JMDN-FastSync` library
(the `chore/retire-fastsync-v1` branch already started retiring V1 — reconcile with it).

---

## 9. Prerequisites / open dependencies

- **Projection watermark + SQL/KV ordering** (open ThebeDB findings): ThebeSync's crash-safety
  relies on the cursor reflecting *committed* SQL state. If SQL can commit before KV (or vice
  versa) without a durable watermark, resume can double-apply or skip. **This must be resolved
  first** — it's the one hard prereq. (Option A's re-derive + fingerprint halt is a strong
  backstop, but the watermark makes resume correct rather than merely detectable.)
- **P-cert — persist `bls_results` per block (hard prereq for shipping certified blocks).**
  Certificates are currently dropped after live verification (`StoreZKBlock` stores no cert). Until
  a persisted per-block cert exists, *no* block is shippable-with-cert and the whole chain is the
  legacy path. This is the change that turns "old blocks with no certification" into a real,
  detectable boundary. Must be written atomically with the block (same 2PC as the block record).
- **Committee source available to a joining node** — verifying a *stored* cert still needs the
  authenticated committee snapshot from the seed (the `jmns`/committee work). A node that can't
  establish the committee must fail closed on certified blocks (no blind trust).
- **No config pins** — `genesis_hash` is the baked-in `DB_OPs.GenesisBlockHash`; the legacy/certified
  boundary is read per-block from cert-presence. Neither is a config knob, so neither can drift or
  be mis-set. The only trusted external input is the seed-vetted peer set / optional signed
  checkpoint for the pre-P-cert bootstrap case.

---

## 10. Phased build (after A is signed off)

Status: **P0, P1, P2 built** (host build + library tests green). Engine lives in the
`JMDN-FastSync` module (branch `feat/thebesync-v4`, package `thebesync/`, opaque blocks); jmdn
supplies `Provider`/`Applier` + the hybrid verify/apply. P-cert and P2-persistence
(`StateFingerprint`) both landed via `BlockRecord.ExtraData`.

1. **P0 — serving protocol (DONE):** `GetBlocks(from,to)` server + head handshake over libp2p.
2. **P1 — receiver apply loop (DONE):** fetch → verify (body-binding + committee cert + linkage) →
   `ProcessBlockTransactions` → store → advance cursor, single-worker, contiguous.
3. **P2 — fingerprint gate (DONE by reuse):** the P2.5 halt lives inside `ProcessBlockTransactions`
   (active under `execbridge.Enabled()`). It now fires on sync because `StateFingerprint` is
   persisted per block (`ExtraData`) and served by ThebeSync, so the receiver COMPARES rather than
   re-stamps. Verify: 2-node — synced node's fingerprint == peer's; a perturbed peer halts
   (`messaging/BlockProcessing/applygate_test.go` already covers the halt in-process).
4. **P3 — failover + PoTS tail (DONE):** `Receiver.SyncFrom` takes a peer set with round-robin
   failover, plus a bounded PoTS tail loop (re-check head, apply blocks produced during sync,
   `maxTailRounds`). Unit-tested (`receiver_test.go`). Fetch-ahead concurrency deferred (optimization).
5. **P4 — retire V2 (DONE, direct cutover):** `catchup` (CLI, gRPC, and the automatic
   `syncmonitor` ReconcileFunc) now routes through `thebesync.CatchUp`. The `FastsyncV2/` package
   is deleted; the CLI `FastSyncerV2` interface/field is removed and `fastsync`/`accountsync`
   commands return "retired — use catchup". The sync monitor is decoupled from FastsyncV2 init
   (gated on `cfg.FastSync.Enabled`). **Residual:** the old `JMDN-FastSync` (Merkle-bisection)
   dependency remains, still used by `internal/merkle`, `internal/syncmonitor`, and
   `DB_OPs/Nodeinfo` for the seednode Merkle-root reporting — dropping it means porting the
   monitor's reporting off the old library (separate follow-up).
   **Update:** the sync monitor now reports the tip block's `StateRoot` (O(1) cumulative
   commitment) via a Thebe-native `ChainReporter`, replacing the O(N) MMR fingerprint. This
   orphaned `internal/merkle` entirely (no importers) and removed `fastsync_types` from the
   monitor. The old `JMDN-FastSync` dep now survives only through `DB_OPs/Nodeinfo` (the account-
   sync worker + redis streamer); once those are ported/retired, `internal/merkle` +
   `DB_OPs/Nodeinfo` can be deleted and the dependency dropped via `go mod tidy`.
6. **P5 — validate + soak:** the `local-thebesync-gate/catchup_gate.sh` harness drives a real
   `catchup` between two nodes and asserts B is byte-identical to A (tip, block hashes, balances),
   including a negative (perturbed peer → P2.5 halt) check. Then multi-node soak (3–5 nodes).

Each phase is host-built (`CGO_ENABLED=1 go build ./...`) and gated on the 2-node harness.

---

## 10.5 Legacy interop (ImmuDB + old FastSync nodes)

The network will run a few legacy nodes still on ImmuDB + the old `/fastsync/v1`
Merkle-bisection engine. They must keep syncing; policy is **they sync only from the sequencer**.

- **Sequencer (ThebeDB, authoritative)** dual-serves: `/fastsync/v1` (legacy) **and** `/fastsync/v4`
  (ThebeSync). It pulls nothing.
- **New nodes (ThebeDB)** serve + pull `/fastsync/v4` only.
- **Legacy nodes (ImmuDB)** pull `/fastsync/v1` from the sequencer only.

Implementation: a `sync.serve_legacy` flag (default false) set **only on the sequencer** re-registers
the FastsyncV2 serving handlers (`FastsyncV2.NewFastsyncV2`, serve-only — never pulls). New nodes leave
it false. The old catch-up *client* stays retired everywhere; only the *server* is restored, and only on
the sequencer. This keeps the old `JMDN-FastSync` dependency in the tree until the last legacy node is
retired, at which point `serve_legacy` and the FastsyncV2 package can be removed for good.

**Constraint:** legacy nodes are frozen — they cannot be updated — so 100% of the compatibility burden
is on the sequencer's serving path.

**Audit finding (verified):** `git diff d27320b363ba..HEAD` on the JMDN-FastSync branch touches ONLY the
new `thebesync/` files — zero changes to protocol IDs, the 15 protos, the MMR/merkletree, the serving
routers, or messaging/auth/transport. So serving legacy `/fastsync/v1` from the ThebeSync branch is
byte-identical, on every wire surface, to serving from the commit the fleet already pins
(`d27320b363ba`); the `replace` directive introduces no legacy-wire drift. This is NOT a "did we break
the protocol" risk — the protocol is unchanged.

**Two residual checks (neither is protocol drift):**
1. *Operator fact:* confirm the frozen legacy nodes were deployed wire-compatible with `d27320b363ba`
   — `git diff <legacy_commit> d27320b363ba -- common/proto common/types/constants merkletree` (empty = ok).
2. *Adapter fidelity (the real unknown):* the unchanged protocol is now fed from ThebeDB via the
   `DB_OPs/Nodeinfo` adapter; the only open question is whether it supplies the correct block hashes in
   the correct order so the legacy node's MMR bisection converges. Gate legacy rollout on an end-to-end
   test: one legacy ImmuDB node fully syncing from the ThebeDB sequencer.

## 11. Risks

- **Prereq risk:** the projection watermark / SQL-KV ordering is a real open ThebeDB item; P1+
  resume correctness depends on it. Highest risk.
- **Seq alignment:** the design assumes appending the certified tail in order reproduces the
  peer's seqs/hash chain. If any node ever appended non-consensus records locally, its seqs
  diverge; the fingerprint halt catches it, but the recovery path (re-sync from genesis) must
  exist.
- **P-cert not done (highest trust risk):** without persisted certs, ThebeSync has no cryptographic
  tip anchor and rests on genesis + state-root chain + vetted-peer selection alone. Land P-cert (or
  a signed checkpoint) before enabling on anything but a trusted bootstrap fleet.
- **Cert-strip downgrade:** a peer omits the stored cert on a post-P-cert block to push it into the
  legacy path. Mitigated by the "once certified, always certified" monotonic rule (read from the
  table: any block above the first-seen stored-cert height must carry one) and by requiring the
  synced tip to carry a verifying cert. Validate on the 2-node gate that a stripped-cert block above
  the boundary is rejected.
- **State-root chain gap on legacy blocks:** `stateRootChain` returns `ok=false` when the parent
  StateRoot is zero (fresh/legacy parent), so the earliest legacy blocks may not be state-root
  chainable — they rest on genesis anchor + tx-body binding + `PrevHash` linkage + the first-cert
  tail anchor. Confirm the earliest post-genesis blocks carry a non-zero StateRoot before relying on
  the chain for the whole prefix.
- **Branch reconciliation:** must be squared with `chore/retire-fastsync-v1` /`fix/Fastsync` so we
  retire V1/V2 once, cleanly.
- **No toolchain here:** design + grounded code; the 2-node gate is the proof.

---

## 12. Status of decisions

1. **Name — ThebeSync (FastSync v4):** confirmed.
2. **Trust model — HYBRID, boundary detected from the table:** decided (§4). Per-block: a block
   with a persisted cert is verified via `VerifyCertificate`; a block without one (the old prefix)
   is verified by genesis anchor + state-root chain + first-cert tail anchor. Both re-derived +
   P2.5-checked. No `cert_activation_height`, no `genesis_hash` config — genesis is the baked-in
   constant and the boundary is cert-presence in the data (answering your question: yes, detected
   at read time).
3. **Design — approved to build.**

**Prereqs / still to resolve:**
- **P-cert — IMPLEMENTED (pending host build).** Certs are now persisted per block: advisory
  `config.ZKBlock.CommitteeCertificate` (JSON of `[]BLS_Signer.BLSresponse`), stashed in
  `BlockRecord.ExtraData["committee_certificate"]` (round-trips via `applyBlock` full-map marshal →
  `blocks.extra_data` JSONB → reader), stamped at both accept sites after the existing 2f+1 check
  (`ProcessBlockLocally` and the gossip receive path). Blocks before this change stay
  cert-less = the legacy prefix. Verify: `CGO_ENABLED=1 go build ./...` + a store/read test.
- **Projection-watermark / SQL-KV ordering** (§9) — ThebeDB change vs ThebeSync workaround, before
  P1 resume-correctness is claimed done.
- **Bootstrap anchor** — until P-cert lands, sync only from seed-vetted peers and/or a signed
  checkpoint; gate the pure-legacy case behind `sync.allow_uncertified_bootstrap` (default false).

Starting with **P0 (the `GetBlocks`/head serving protocol)** — small and host-testable on the
2-node harness — not the whole engine at once.
