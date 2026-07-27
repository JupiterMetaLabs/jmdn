# Validator Rewards — Architecture Design

Status: Draft for review · Date: 2026-07-16 · Scope: jmdn (+ Sequencer-Orchestrator, FastSync)

Plan only. No implementation in this document.

## Decision (TL;DR)

Reward the **BFT buddy committee** that BLS-signs each block. Rewards are **carved out of the existing gas-fee pool** (no minting, no supply ledger). Committee membership is **gated by bonded stake**, and misbehavior is **slashed**.

**Primary architecture.** At block execution the gas fee splits into a **validator share** and a **ZKVM share**; the validator share accrues to a pool address. The reward for block N is then **paid as a native transfer transaction in block N+1**, sized by the configured distribution mode and validated by the buddy committee. Because the payout is an ordinary in-block tx, it replays deterministically on every path — no two-path arithmetic to drift. A staking contract (or native module) holds bond/unbond/withdraw/slash and eligibility state; it does **not** custody the reward pool.

This fits the codebase: the network already runs a full EVM (`SmartContract/internal/evm/`) with a `StateDB` and re-executes every block on every node (`ProcessBlockTransactions(block, client, commitToDB)` — sequencer commits, buddy nodes verify with `commitToDB=false`). That is state-machine replication, which is exactly what a reward contract needs.

A **native (non-contract) variant** is documented at the end as a simpler fallback.

### Grounding facts

- Blocks are produced by a single orchestrator and **ratified per-block by a 5-node VRF/reputation-selected committee** using aggregated BLS votes, simple majority (`Sequencer/Consensus.go:2047`, `config/constants.go MaxMainPeers=5`).
- Fees are **not burned**; today they split 50/50 coinbase/ZKVM via `config/gasfee.go`, applied in `messaging/BlockProcessing/Processing.go` and `FastsyncV2/deltas.go`.
- No staking, slashing, issuance, or supply accounting exists today — all new.
- A full EVM + `StateDB` exists (`SmartContract/`); the live path executes contracts and commits atomically. *(Likely — from `SmartContract/processing_changes.md` + `smart_contract_flow.md`; confirm against the current `Processing.go` body.)*
- `FastsyncV2/deltas.go` is **native-only** — hand-written `value + gasFee` arithmetic, no EVM. *(Certain — full file read.)*

---

## Determinism: inherited from tx replay (reward-as-next-block-tx)

The reward for block N is **materialized as an explicit transaction in block N+1**, not computed inline during N's processing. This sidesteps the `config/gasfee.go` two-path drift class entirely: the split is computed **once** — by the sequencer, when building N+1 — and frozen into a tx. Every path afterward (live execution, buddy verification, FastSync catchup) simply *replays* that tx. There is no second arithmetic implementation to drift from, which is exactly the failure `gasfee.go` documents.

Three conditions keep this clean:

1. **Native transfer.** The reward tx moves value pool → validator, so `FastsyncV2/deltas.go` replays it like any transfer (it already credits `to`/debits `from` generically). If instead the tx mutates contract storage, the separate contractDB-reconstruction question applies (see Risks) — but that is not fee-split drift.
2. **Buddy validation of the amount.** When ratifying N+1, buddy nodes recompute the expected reward from N's fee pool + signer set and check the tx matches; otherwise the sequencer could pay arbitrary amounts. This is a consensus rule — a mismatch *rejects the block*, it does not silently corrupt balances.
3. **Zero gas on the reward tx.** `deltas.go` calls `config.GasFee` unconditionally, and `gasLimit==0` triggers the `FallbackTxGasLimit` (21000). The system reward tx must carry an explicit zero fee (or a tx type `deltas.go` treats as zero-fee), or phantom gas is charged against the pool on reconciliation.

Timing note: N's reward lands in N+1, so a validator is paid one block later; the final block before a halt has its reward deferred until the next block is produced.

---

## Reward source — validator / ZKVM split

Extend the single source of truth (`config/gasfee.go`); do not fork the arithmetic.

Today: `GasFee` → coinbase `half + remainder`, ZKVM `half`.

New: split into a validator share (to the staking contract) and the remainder (coinbase/ZKVM as today), integer-deterministic:

```
validatorShare = GasFee * VALIDATOR_BPS / 10000     // e.g. 2000 bps = 20%
rest           = GasFee - validatorShare
coinbase      += rest/2 + rest%2
zkvm          += rest/2
// validatorShare → staking contract address
```

Add `VALIDATOR_BPS` + a `SplitFee(gasFee) (validator, coinbase, zkvm *big.Int)` helper so every call site uses identical math. Remainder policy fixed (to coinbase) as today.

Effect: coinbase/ZKVM revenue drops by `VALIDATOR_BPS`. That is the funding source — confirm with whoever owns those addresses.

---

## Primary architecture — staking/rewards contract

### Execution flow (reward for block N lands in block N+1)

**During block N:**

1. **Consensus on input** — BFT committee agrees on N and its tx order (existing); the yes-signer set + block-bound BLS aggregate are recorded in N (see "What must be stored in the block").
2. **Fee split** — `SplitFee` yields `validatorShare` for each tx; it accrues to the staking pool address (native credit, exactly like coinbase today).

**When building block N+1:**

3. **Reward tx (the trigger)** — the sequencer emits a system transaction in N+1 that pays N's yes-signers from the pool, per the configured distribution mode (flat or stake-weighted). Contracts do not self-fire and rewards are protocol-initiated; materializing the payout as a tx is what makes it replay deterministically (see Determinism). The tx carries zero gas.
4. **Buddy validation** — buddies ratifying N+1 recompute the expected payout from N's fee pool + signer set and reject N+1 if the reward tx does not match.
5. **Apply = replay** — live execution, buddy verification, and catchup all apply the reward tx the same way they apply any tx. Slashing and stake mutations are likewise emitted as txs (user-submitted for bond/unbond/withdraw; system/evidence txs for slash), so they inherit the same replay determinism.

### What must be stored in the block

- **`signerBitmap`** — the BLS-verified yes-voter set from `VerifyConsensusWithBLS` (`Sequencer/Consensus.go:1973`, which already builds `r.PeerID` + `r.Agree` and passes `blsResults` to `ProcessBlockLocally`), encoded as a bitmap over the epoch's canonical committee ordering, plus the BLS aggregate. This is the system call's argument; it must be consensus data, not live state, so every node's call receives the same input.

### Prerequisite: bind BLS votes to the block (blocker for slashing + trustworthy attribution)

Today the vote message is a **constant string** — `SignMessage` signs literally `"vote:1"` / `"vote:-1"` (`AVC/BuddyNodes/MessagePassing/BLS_Signer/Signer.go:47`), not bound to any block. Two consequences:

- The stored `signerBitmap` is only as trustworthy as the sequencer that recorded it — the aggregate does not self-prove *which* block was ratified.
- Equivocation is undetectable: a constant message can't distinguish a node signing two conflicting blocks.

Change the signed message to bind the vote to the block. **Block number alone is insufficient** — two conflicting blocks at the same height would hash to the same message and produce identical signatures, so equivocation still couldn't be proven. Include the block hash and domain separation:

```
msg = keccak256(DOMAIN ‖ chainID ‖ epoch ‖ height ‖ blockHash ‖ vote)
```

`Signer.go` builds this preimage; `BLS_Verifier` reconstructs it identically. With this, the aggregate over yes-voters is a self-verifying proof that a specific block was ratified (strengthening attribution), and two valid signatures at one height over different `blockHash` values are cryptographic equivocation evidence (enabling slashing).

### Contract responsibilities (EVM state)

- **State:** `bonded[addr]`, `rewardAddress[peerID]`, `jailedUntil[addr]`, unbonding queue. Reward payout is **not** custodied by the contract — it is a native transfer tx from the pool address (see flow). The pool can be a plain protocol address, so rewards stay pure native txs and dodge the contractDB-reconstruction risk.
- **User functions:** `bond`, `unbond`, `withdraw` — ordinary signed transactions.
- **Slashing:** `slash(evidence)` — verifies two BLS votes at one `height` over different `blockHash` (needs on-chain BLS verify / precompile), burns stake, jails.
- **Eligibility read:** committee selection reads `bonded` + `jailedUntil` from the epoch snapshot. Because that is contract storage, catchup must reconstruct it (see Risks) — or staking state is held natively instead.

### Reward distribution mode (per-block pool → signers)

The pool for a block is split among that block's yes-signers by one of two modes, selected by a `DISTRIBUTION_MODE` parameter (contract storage, so it is governable and reconstructs deterministically):

1. **Flat split** — equal share per yes-signer. `share = pool / k`, `k` = number of yes-signers; leftover wei assigned by fixed committee order (largest-remainder). Simple and equalizing; rewards participation, not capital.
2. **Stake-weighted** — `share_i = pool * stake_i / Σ stake` over yes-signers, using epoch-snapshot stake; leftover wei by largest-remainder in fixed order. Rewards capital at risk.

Both are deterministic (integer arithmetic, fixed ordering) so live, buddy, and catchup paths agree.

> **Sybil interaction — decide with the multi-node section.** Flat split is a Sybil vector: an operator who splits stake into N `MIN_STAKE` nodes and lands multiple seats collects N equal shares for the same capital. Stake-weighted split is Sybil-neutral: N nodes holding `S/N` earn the same total as one node holding `S`. If flat split is chosen, the Sybil pressure must be absorbed elsewhere — stake-weighted *selection* + stake-weighted *quorum* (below), a per-operator seat bound (hard, see below), or a higher `MIN_STAKE` that makes extra nodes costly. Stake-weighted split is the safer default; flat split is viable only alongside strong stake-weighted selection.

### PeerID → reward address binding

PeerID is **not** configured — it is derived from the node's autogenerated libp2p key (`node/node.go loadOrCreatePrivateKey()`), as today. The operator declares only `RewardAddress` in `jmdn.yaml` (existing Viper loader, `config/settings/loader.go`, e.g. `Bootstrap.RewardAddress`). The node submits a `bond` tx **signed by that key**, binding `PeerID → RewardAddress` in contract state. Because it is key-signed, a node can only bind its *own* PeerID — self-authenticated, no spoofing. `jmdn.yaml` is input to the bond tx; it is never read at payout time.

### Staking, epochs, slashing

- **Epochs:** introduce `EPOCH_BLOCKS`. Snapshot the eligible stake set at each boundary. For the epoch: VRF committee **eligibility** = `bonded >= MIN_STAKE` and not jailed (add to `AVC/NodeSelection/pkg/selection/filter.go` alongside `MinSelectionScore` / `MinReputationScore`); reward **weights** = frozen snapshot. Snapshot derives from chain history, so it reconstructs deterministically.
- **Unbonding:** `unbond` starts a delay; funds locked until `height + UNBOND_DELAY`; `withdraw` matures them.
- **Slashing conditions:** (1) equivocation — two validly-BLS-signed votes at one `height` over different `blockHash`, self-evident cryptographic evidence (requires the block-bound vote message above); (2) liveness (phase-in) — persistent absence from committees the node was selected for, derivable from per-block signer bitmaps. Slashed stake is **burned** (no supply ledger to update; avoids a profit incentive to slash peers). Jail sets `jailedUntil = height + JAIL_BLOCKS`.

### Multi-node operators & Sybil resistance

One operator running N validators is first-class (redundancy, geo, throughput). Payout side already supports it: N autogenerated PeerIDs, each bonded `MIN_STAKE`, all `RewardAddress` fields may point at one address so rewards aggregate; total locked = N × `MIN_STAKE`.

The danger is on selection/voting. As the base system stands, splitting stake is a *Sybil advantage*: selection is uniform VRF (N nodes = N tickets) and quorum is headcount (`needed := (validTotal/2)+1`, `Consensus.go:2047`), so an operator winning ≥3 of 5 seats reaches quorum alone. Per-operator caps are unenforceable — N PeerIDs look like N operators.

**Required mitigation before rewards go live — stake-weight both dimensions:**

- VRF selection probability ∝ bonded stake (not uniform).
- Quorum = fraction of *committee stake* in agreement (not headcount).

Then N nodes holding `S/N` each behave like one node holding `S` — splitting yields no advantage while honest multi-node operators are not penalized. Keep `MIN_STAKE` (anti-dust) and the reputation gate as friction. Note this is independent of the reward **distribution** mode above: stake-weighted distribution is Sybil-neutral on its own, whereas flat distribution relies on this stake-weighted selection/quorum to stay safe.

---

## Code touch-points (primary / contract-based)

| Concern | File | Change |
|---|---|---|
| Fee split arithmetic | `config/gasfee.go` | Add `VALIDATOR_BPS`, `SplitFee()` |
| Accrue validator share | `messaging/BlockProcessing/Processing.go` | Credit `validatorShare` to pool address (native, like coinbase) |
| Emit reward tx in N+1 | sequencer block-build path | System native-transfer tx paying N's signers from pool; zero gas |
| Validate reward tx | buddy verify path | Recompute expected payout from N; reject N+1 on mismatch |
| Signer set in block | `config/ZKBlock.go` | Store signer bitmap + BLS aggregate |
| Signer set source | `Sequencer/Consensus.go` (~1973) | Emit ordered yes-voter set into the block |
| Block-bound vote message | `BLS_Signer/Signer.go:47`, `BLS_Verifier` | Replace constant `"vote:±1"` with `keccak(DOMAIN‖chainID‖epoch‖height‖blockHash‖vote)` |
| Staking contract | `SmartContract/` (new contract) | bond/unbond/withdraw, slash; holds stake/eligibility state (not the reward pool) |
| Eligibility gate | `AVC/NodeSelection/pkg/selection/filter.go` | `bonded >= MIN_STAKE` + jail check |
| Stake-weighted selection/quorum | `AVC/NodeSelection/...`, `Sequencer/Consensus.go` | Weight VRF probability and quorum by stake |
| Reward address config | `jmdn.yaml` + `config/settings/loader.go` | Add `Bootstrap.RewardAddress`; feeds bond tx only |
| Catchup EVM state | `FastsyncV2/` + `contractDB` sync | Ensure contract state reconstructs on catchup (see Risks) |

---

## Rollout (plan, sequenced by feasibility)

1. **Confirm catchup reconstructs EVM/contract state** for existing contract txs (feasibility killer — do first). Resolve the `deltas.go` native-only gap.
2. `SplitFee` with `VALIDATOR_BPS = 0`. No behavior change; verify balance/catchup parity.
3. Bind BLS votes to the block (`keccak(DOMAIN‖chainID‖epoch‖height‖blockHash‖vote)`) and persist `signerBitmap` + BLS aggregate into blocks. No payouts yet. Verify reconstructable and that the aggregate self-verifies.
4. Deploy staking contract; enable bond/unbond/withdraw + epoch snapshot + eligibility gate. No rewards, no slashing.
5. Stake-weight selection + quorum. Verify committee behavior with a multi-node operator.
6. Turn on `VALIDATOR_BPS > 0`; sequencer emits the N+1 reward tx, buddies validate it. Verify live vs buddy vs catchup parity on a replayed range (gate).
7. Enable equivocation slashing. Liveness slashing last.

---

## Alternative — native module (simpler, not primary)

If the contract path is blocked (e.g. catchup can't reconstruct EVM state, or on-chain BLS verification is impractical), do it natively:

- No staking contract. Reward/slash/stake accounting is native code over `DB_OPs`/ThebeDB.
- `SplitFee` credits signers directly (stake-weighted, fixed committee order, integer largest-remainder for the leftover wei).
- Bond/Unbond/Withdraw/Slash are **native transaction types**, applied atomically like `ApplyTxAtomic`.
- **Cost:** the reward/slash arithmetic must be mirrored in **both** `Processing.go` and `FastsyncV2/deltas.go`, kept byte-identical forever — the exact class of drift `config/gasfee.go` warns against. A single shared helper (like `SplitFee`) is mandatory, and a replay-parity gate is the release blocker.

**Trade-off:** native avoids EVM/contract complexity and on-chain BLS verification, but reintroduces the two-path mirroring hazard and hard-codes policy (reward %, slash %, unbonding) into protocol code — every parameter change is a protocol upgrade. The contract path centralizes accounting in one deterministic state machine and makes parameters governable, at the cost of requiring EVM state to reconstruct on every path.

---

## Open questions

- How does FastSync catchup reconstruct `contractDB` today — re-execute EVM, or state-sync contract storage? (No longer gates rewards, since payout is a native tx; still gates contract-held staking/eligibility state. Decide: contract vs native staking state.)
- Is there an on-chain BLS-verify precompile, or must slashing evidence be verified natively then reported to the contract?
- `DISTRIBUTION_MODE`: flat vs stake-weighted (see Reward distribution mode). Flat requires stake-weighted selection/quorum to be Sybil-safe.
- Parameter values: `VALIDATOR_BPS`, `MIN_STAKE`, `EPOCH_BLOCKS`, `UNBOND_DELAY`, `SLASH_EQUIV_BPS`, `JAIL_BLOCKS`.
- Does coinbase (sequencer/orchestrator) keep a share, or is its portion folded into the validator share?
- Rewards on rejected/orphaned blocks — only finalized blocks pay (recommended).
- Should the L1 commitment (`ZKVM-L1-Push`) attest the signer bitmap, or is in-block inclusion enough?

## Risks

- **Reward payout: resolved by reward-as-next-block-tx.** The payout is a native transfer tx in N+1, computed once and replayed on every path — no `gasfee.go`-style two-path drift. Residual conditions: native transfer (not contract-storage mutation), buddy validates the amount, and the tx carries zero gas (else `deltas.go` charges the 21000 fallback). See Determinism.
- **Staking state reconstruction on catchup.** Rewards no longer depend on contract state, but committee *eligibility* (bonded stake, jail) does if held in the contract. `deltas.go` is native-only and never runs the EVM, so contract storage may not reconstruct on catchup. This likely affects contract txs already, independent of rewards. Mitigation: confirm how catchup rebuilds `contractDB`, or hold staking state natively (rollout step 1).
- **Signer set not in block.** Buddy validation of the N+1 reward tx (and slashing) needs N's signer set + block-bound aggregate persisted in N. Mitigation: bind votes to the block and persist bitmap + aggregate before enabling payouts (step 3 before step 6).
- **On-chain BLS verification.** Slashing needs aggregate-signature verification in EVM; may require a precompile or native pre-check. Confirm before phase 7.
- **Sybil / committee capture.** Uniform VRF + headcount quorum let one operator split stake and capture a majority. Mitigation: stake-weight selection and quorum before rewards go live.
- **Revenue shift.** Coinbase/ZKVM lose `VALIDATOR_BPS` of fees. Confirm with stakeholders.
- **Native-variant drift.** If the fallback is chosen, reward/slash logic mirrored in `Processing.go` and `deltas.go` will drift unless a single shared helper + parity gate enforce equality.
- **Slashing griefing / false evidence.** Only cryptographically self-evident equivocation slashes in phase 7; liveness deferred until the bitmap history is trusted.
