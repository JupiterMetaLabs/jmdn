# EVM P4 — contract-state root: design + options

**Status:** IMPLEMENTED (2026-08-19) — Option B. ThebeDB `kv.Store.ScanPrefix` (2d264d2) +
`contractDB.ComputeStorageRoot`/`FoldAllContracts` + `DB_OPs` fold hook (ee0c0a7), gated on
`cfg.Contracts.Enabled`. Option A (MPT) deferred; Option-C incremental cache planned for DEX scale —
see §12. Host gate: `CGO_ENABLED=1 go build ./... && go test ./DB_OPs/contractDB/`.

**Author context:** written after P0–P3 + P2.5 landed (contract execution is wired end-to-end behind
`cfg.Contracts.Enabled`; account state is divergence-checked and halts on mismatch). P4 closes the
remaining gap: **contract STORAGE divergence is not yet detected.**

---

## 1. Problem

P2.5 commits a post-apply **account** fingerprint to the block and halts a receiver whose recompute
disagrees (`ZKBlock.StateFingerprint`, `DB_OPs.ComputeAccountStateFingerprintV1`). That covers account
balances/nonces and — cheaply — contract *existence* + *code*, but NOT contract *storage*. So today two
nodes could apply the same contract call, diverge on a storage slot (e.g. a token balance mapping),
and neither the fingerprint nor the block would notice. For contracts with non-trivial storage this is
a silent-fork risk of exactly the class P2.5 was built to stop — just one layer deeper.

P4 must produce, deterministically on every node, a **commitment to all contract storage** that the
block carries and receivers verify (halt on mismatch), the same way P2.5 does for accounts.

## 2. Current state (verified against the tree)

- `contractDB.GetStorageRoot(addr) common.Hash` → returns `common.Hash{}` — a **stub**. No real root
  exists anywhere.
- Contract storage is **flat KV**: `contract:storage:<addr>:<slot>` → 32-byte value, written via
  `StateBatch.SaveStorage` / read via `StateRepository.GetStorage(addr, key)` (point lookups only).
- `StateRepository` has **no iteration** method — only per-key `Get*`.
- The ThebeDB `kv.Store` interface (`pkg/kv/store.go`) exposes `Get(key)`, `PutWorm/PutDerived(key)`,
  and `Iterate(sinceSeq, fn)` — iteration is by **canonical-log sequence number, not key prefix**.
  There is **no prefix/range scan** over derived keys. (BadgerDB underneath supports prefix iteration
  natively; the *interface* just doesn't surface it.)
- The state-fingerprint primitive already has the target shape: `consensushash.ContractLeaf{Address,
  Nonce, CodeHash, StorageRoot}` and `StateFingerprinterV1.FoldContract` — P4 only needs to *populate*
  `StorageRoot` and fold contract leaves into the runtime fingerprint.
- Philosophy already adopted (P2.5, `Sequencer/committee_quorum` single-sequencer model): a **canonical
  keccak digest that DETECTS divergence and halts** is the accepted substitute for a full
  Merkle-Patricia trie — the chain does not (yet) need inclusion proofs, only agreement-or-halt.

## 3. Where P4 plugs in

```
per-contract storage root  ─┐
contract nonce + codeHash  ─┼─▶ ContractLeaf ─▶ StateFingerprinterV1 ─┐
all accounts (P2.5)        ─────▶ AccountLeaf ────────────────────────┼─▶ block.StateFingerprint
                                                                       │        │ producer stamps
                                                                       │        ▼ receiver recomputes
                                                                       └───────▶ mismatch ⇒ HALT
```

P4 = "compute a per-contract storage root" + "fold contract leaves into
`ComputeAccountStateFingerprintV1`". Everything downstream (block field, stamp, recompute, halt) is
already built by P2.5. Optionally P4 also commits a *dedicated* contract-state root field for
light-client use — only Option A gives that.

## 4. Options

### Option A — go-ethereum MPT (real Ethereum state/storage trie)

Maintain a Merkle-Patricia storage trie per contract (via `go-ethereum/trie` + a `trie.Database` over
a KV namespace); the trie root is the storage root. This is canonical Ethereum semantics.

- **Pros:** canonical root; supports Merkle inclusion/exclusion **proofs** (light clients, cross-chain,
  fraud proofs); incremental — O(log n) per slot update, no full scan; matches `eth_getProof`.
- **Cons:** large rearchitecture. `ContractDB` is a hand-rolled `vm.StateDB` over **flat** KV slots;
  an MPT means storing/pruning trie **nodes** in a new KV namespace and either replacing that storage
  layer with go-ethereum's `state.StateDB`/`state.Database` or running a parallel trie. Adds node
  storage, pruning/GC, and a second write path to keep consistent. Heaviest option by far; highest
  risk against the existing deterministic-commit work.

### Option B — sorted-scan keccak digest (flat KV, no trie)

Keep the flat KV. For each contract, scan `contract:storage:<addr>:` in **sorted key order** and fold
`(slot, value)` into a domain-tagged keccak → the contract's storage root. Fold every contract's
`{addr, nonce, codeHash, storageRoot}` into the block fingerprint.

- **Pros:** small and reuses everything — the flat layout, the `StateFingerprinterV1` pattern, the
  P2.5 stamp/verify/halt path. Standard, collision-resistant, trivial to reason about and unit-test.
  Deterministic by sort order. Conceptually identical to P2.5's account digest.
- **Cons:** needs **one ThebeDB change** — a key-prefix iterator on `kv.Store`
  (`ScanPrefix(prefix) (Iterator, error)`), which is a ThebeDB-repo task (reconciliation stop-condition:
  no ThebeDB changes from jmdn) but a natural, small Badger-backed addition. O(total storage slots)
  per block to recompute (same shape as P2.5's O(N accounts)); needs incremental optimization at scale.
  No Merkle proofs.

### Option C — incremental accumulator (no scan, no ThebeDB change)

Maintain a per-contract storage root **incrementally on write**: a commutative accumulator
`root = XOR over live slots of keccak(domain ‖ slot ‖ value)`. On `SaveStorage(addr,slot,new)`, read
the old value (already available on the commit path), `root ^= keccak(…old…)` then
`root ^= keccak(…new…)`; persist `contract:storageacc:<addr>`. Fold it as `ContractLeaf.StorageRoot`.

- **Pros:** **no ThebeDB change** (stays entirely in jmdn `contractDB`); **no O(N) scan** — O(1)
  amortized per slot write; order-independent by construction; deterministic across nodes (same writes
  ⇒ same accumulator).
- **Cons:** an XOR set-hash has weaker cryptographic properties than a Merkle/sorted-keccak digest
  (an adversary who controls slot/value pairs can craft collisions). **Adequate for divergence
  DETECTION under the honest-single-sequencer model** (the P2.5 threat model), **not** a
  proof-grade commitment. Extra persisted per-contract key kept atomic with each storage write; care
  needed for deletes (XOR out) and the empty-contract case (accumulator = 0).

## 5. Decision matrix

| Criterion | A — MPT | B — sorted-scan digest | C — incremental accumulator |
|---|---|---|---|
| Detects storage divergence | ✅ | ✅ | ✅ (non-adversarial) |
| Merkle proofs (light clients) | ✅ | ❌ | ❌ |
| ThebeDB change required | node store (large) | **1 small iterator** | **none** |
| Per-block cost | O(log n)/slot (incremental) | O(all slots) rescan | O(1)/slot write |
| Implementation size / risk | large / high | small / low | medium / medium |
| Fits existing P2.5 philosophy | over-built | **exact fit** | fit (weaker hash) |
| Cryptographic strength | strongest | strong | weakest (XOR set-hash) |

## 6. Recommendation

**Phase 1 — Option B (sorted-scan keccak digest), gated behind `cfg.Contracts.Enabled` like P2.5.**
It is the exact extension of the already-shipped, already-reasoned P2.5 account digest, it is standard
and easy to test, and its single dependency (a ThebeDB prefix iterator) is a small, natural addition.
This closes the storage-divergence gap with the least new surface and the clearest correctness story.

- If the O(N) rescan becomes a measured bottleneck, layer **Option C's incremental accumulator** *as a
  cache* that is periodically reconciled against a Phase-1 full scan — keeping B as the source of truth
  and C as the fast path.
- Adopt **Option A (MPT)** only if/when the product needs **inclusion proofs** (light clients, bridges,
  fraud proofs). It is a separate, larger initiative, not a prerequisite for consensus-safe contracts
  on a single-sequencer chain.

**If the ThebeDB iterator is not acceptable near-term,** ship **Option C** first (no ThebeDB change),
accepting the weaker set-hash for detection-only, and upgrade to B when the iterator lands.

## 7. Implementation plan (Option B)

1. **ThebeDB (repo task, filed alongside T1–T3):** add `kv.Store.ScanPrefix(prefix []byte)
   (Iterator, error)` (Badger prefix iterator) + interface method; keep `__sys:*` reserved-namespace
   guards. Verify: iterate a known prefix returns exactly the matching keys in sorted order.
2. **jmdn `StateRepository` + `KVStateRepository`:** expose `IterateStorage(addr, fn(slot, value))`
   and `IterateContracts(fn(addr))` (prefix scans over `contract:storage:` and `contract:code:` /
   `contract:meta:`), backed by the new ThebeDB iterator.
3. **`contractDB.ComputeStorageRoot(addr) common.Hash`:** replace the `GetStorageRoot` stub — sorted
   scan of the contract's slots folded through a domain-tagged keccak (`jmdn/contract-storage/v1`).
4. **`consensushash`:** already has `ContractLeaf` + `FoldContract` — no change.
5. **`DB_OPs.ComputeAccountStateFingerprintV1`:** after the account pass, iterate contracts (sorted by
   address) and `FoldContract({addr, nonce, codeHash=keccak(code), storageRoot})`. This makes the
   existing P2.5 stamp/verify/halt path automatically cover contract storage — no change to
   `ProcessBlockTransactions`.
6. **Optional dedicated field:** if a standalone contract-state root is wanted for RPC/L1, add
   `ZKBlock.ContractStateRoot` (advisory, like `StateFingerprint`).

## 8. Verification & gates

- **Unit (sandbox, CGO-off where possible):** `ComputeStorageRoot` determinism + order-independence
  over shuffled slot-write orders; empty contract → canonical empty root; a single slot change → root
  changes; contract leaf binding (addr/nonce/codeHash/storageRoot each change the fingerprint) — mirror
  the existing `StateFingerprintV1` tests.
- **2-node gate (host, with the P4 flag on):** deploy a storage-writing contract, call it on both
  nodes → identical contract-state fingerprint; then force a one-slot divergence on one node → that
  node **halts** (P2.5 path) instead of serving divergent storage.
- **Perf:** measure the full rescan at representative contract/slot counts before enabling on large
  state; if it regresses block time, gate on Option C's incremental cache.

## 9. Risks / open questions

- **ThebeDB dependency (Option B):** the prefix iterator is a ThebeDB-repo change; it must land and be
  verified there first. Option C avoids this but weakens the hash.
- **Cost at scale:** a per-block full storage rescan is O(total slots); acceptable for detection now,
  needs the incremental path before large state. Same caveat already noted for P2.5 accounts.
- **Not proof-grade (B/C):** neither B nor C yields Merkle proofs; if light-client/bridge proofs are
  ever required, that forces Option A regardless.
- **Consensus binding:** P4 rides the P2.5 advisory field until CON-02 v3 makes the state commitment
  committee-signed; until then its trust rests on the single honest sequencer (same as P2.5).
- **Determinism of `codeHash`/`nonce` sources:** must read from the same committed local state the
  EVM-A16 path uses, so producer and receiver fold identical contract leaves.

---

## 12. Roadmap-driven decision (2026-08-19) — **Option B, A deferred**

**Roadmap input:** a DEX on JMDN L2 + a **Wormhole** lock-and-mint bridge (lock JMDT on JMDN L2, mint
wJMDT on Eth / Base / BSC).

**Decision: implement Option B (sorted-scan keccak digest), and plan the Option-C incremental
accumulator as a cache before mainnet DEX load. Do NOT build Option A (MPT) for this roadmap.**

### Why the roadmap does not require A (MPT proofs)

Wormhole is an **attestation bridge, not a light-client/proof bridge** (Likely — standard Wormhole
Guardian model; confirm against the exact integration: Portal vs NTT vs custom). Its Guardian network
runs watchers on JMDN L2, observes the Wormhole core contract's **published-message events/logs** on
finalized blocks, and emits a **guardian-signed VAA**; the destination chain verifies **guardian
signatures**, NOT a Merkle proof of JMDN state. So Wormhole never consumes a JMDN storage/state
**proof** — which is the only capability Option A adds over B.

Everything Wormhole actually needs from JMDN is already in place or covered by P1c–P3:
EVM contract execution (P1c/P2), emitted **logs/receipts** (P3), the gETH/Facade **RPC** to read them,
and block **finality** (L1 commitRollup). The contract-state root is for INTERNAL divergence detection
only, and B provides that.

```
lock JMDT ─▶ Wormhole core contract on JMDN (emits LOG)
             │
             ▼  Guardians watch events on finalized blocks
        guardian-signed VAA ─▶ dest chain verifies GUARDIAN SIGS ─▶ mint wJMDT
                                     ▲
             (MPT storage proof is NOT on this path)
```

### The actual gating concern for B: DEX storage scale

A DEX is storage-heavy (AMM pools, LP positions, allowances → many slots). B recomputes over **all**
contract storage every block (O(N)), which can dominate block time at DEX volume. Mitigation is
planned, not a blocker:

1. **Ship B now** — closes the storage-divergence gap, reuses the tested `StateFingerprinterV1`,
   unblocks the DEX + Wormhole.
2. **Before mainnet DEX volume, add the Option-C incremental accumulator as a CACHE** — maintain each
   contract's storage root on write; periodically reconcile against a full B scan (B stays source of
   truth, C is the fast path). Removes the per-block rescan without adopting A's trie + node-store +
   pruning subsystem.

### When to revisit A

Only if the roadmap later adds a **trustless / non-guardian** bridge — e.g. a canonical JMDN↔Eth bridge
that posts the JMDN state root to L1 and verifies **withdrawals via on-chain storage proofs**. That
model needs A's MPT. Wormhole is not that model. If such a bridge enters the 12–18 month roadmap,
reconsider **before** DEX storage grows large, because B→A is a flat-KV→trie **data migration** best
avoided later.

### Open confirmations
- Confirm the Wormhole integration mode (Portal / NTT / custom) is guardian-attested, not a custom
  trustless verifier that reads JMDN state proofs. A custom proof-verifying bridge would flip this to A.
- Track B's full-scan cost against representative DEX state; trigger the Option-C cache before it
  regresses block time.
