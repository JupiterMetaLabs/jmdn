# Block finality

**Status:** phases 1–2 implemented on `feat/certified-finality`; phase 3 (the locking rule) designed here, not yet implemented.
**Code:** jmdn `v3base` at `38bd828d`.

## 1. What a certificate guarantees today

A certificate proves that 2f+1 members of the committee seated for a block's (height, period) signed that exact block. It is the list of block-bound result signatures, or their compact aggregate (`messaging/cert_compact.go`).

That one block at a height is certified relies on more than the members today:
- **Members' signing ledger** (`internal/roundlock/roundlock.go`) enforces two rules per height:
  - R1: one block per (height, period);
  - R2: never a lower period.
- **Nothing else constrains a later period** of the same height. A period change is decided by the sequencer (`messaging/proposer_period.go`), with no evidence that the earlier period failed.
- **So uniqueness also rests on the pinned sequencer following the protocol.**

Phase 3 (§4) makes it rest on the committee alone.

## 2. What "finalized" promises (phases 1–2)

`eth_getBlockByNumber("finalized")`, `eth_getLogs` with `toBlock: "finalized"`, and the same for `"safe"`, return the **highest block H on this node** for which:
1. H's stored certificate verifies **strictly** (`messaging.VerifyFinalityCertificate`):
   - against the committee seated for H's (height, period) from the **chain-anchored** pool;
   - with 2f+1 over that committee;
   - with no trusted-sequencer relaxation and no legacy or pool-wide fallback.
2. The stored block at H+1 also verifies strictly and has `PrevHash == H.BlockHash`.

If no such block is within 64 blocks of the tip, the call is an **error**, never the head. The answer is cached per tip, and a verified certificate is cached per (height, BlockHash), so polling costs one computation per new block. If the committee period is not anchored, nothing is finalized. The answer fails closed.

**What it does not promise, until phase 3 ships:** that no other certified block exists at H. Until then, uniqueness also rests on the sequencer (§1). A certified child does not change that, because committee members sign a child of whichever block reached them first.

So, until phase 3, `finalized` is not protection against a sequencer that deviates from the protocol. It does protect against everything else this node can check: uncertified blocks, unanchored committees, a broken parent link, and logs of blocks that were never stored.

`jmdt_getCertifiedBlock(height)` returns everything needed to check H independently:
- BlockHash, ConsensusHash, PrevHash, height, period and slot;
- the selection period, entropy epoch and seated committee (peer IDs and BLS keys);
- the exact vote message the +1 signatures sign;
- the stored certificate and its signers;
- the verification outcome and the `finalized` flag.

## 3. Logs and receipts (phase 1)

- **The problem:** logs are indexed and fanned out during apply (`contract_apply.go:384`), before the block is stored. A rollback doesn't remove them.
- **`eth_getLogs`:** now returns a log only if the block stored at its height has the log's BlockHash. If the block can't be read, the query fails.
- **`eth_subscribe("logs")`:** holds a log until its block is stored (up to 10 s), and drops it if a different block, or no block, ends up stored there.
- **Receipts were already safe.** `eth_getTransactionReceipt` resolves through the stored transaction row, which exists only once its block is stored. A contract receipt row has a foreign key to `blocks`, so it can't land without its block.

## 4. Phase 3: the locking rule (design, not implemented)

### 4.1 Why the simple timeout certificate is not enough

The rule removed in #196 was modelled as "Fix B" in the one-height TLA+ model:
- a member signs "timed out at p" only if it signed nothing at p;
- period p+1 needs 2f+1 such timeouts.

It is safe (runs E and D2), but **it can stall a height for good.** With 7 seats, Q = 5 and one silent Byzantine member, three honest members signing A and three timing out leaves both quorums out of reach. A member that has signed can never time out, and one that has timed out can never sign.

Letting members that signed also time out, reporting what they signed, doesn't work with single signatures. A Byzantine reporter can claim a competing value at the same period, and nothing in the timeout set shows which value could have been certified.

### 4.2 The rule: two phases, with a prepare certificate as the lock

This is PBFT / Tendermint locking with quorum certificates, adapted to a committee that changes per period. C[p] is the committee seated for (h, p), and Q = 2f+1 of |C[p]| = 3f+1.

1. **PREPARE.** The sequencer proposes X at (h, p). Each member of C[p] that accepts it signs `PREPARE(h, p, X)`, subject to rule 4. Q prepares make **PQC(h, p, X)**.
2. **COMMIT.** The sequencer sends PQC(h, p, X) to C[p]. A member that verifies it **locks on (p, X)**, persists the lock in the roundlock store before signing, and signs `COMMIT(h, p, X)`. Q commits are the **block certificate**, the same object followers verify today but in a new commit domain.
3. **TIMEOUT.** Any member of C[p] may sign `TIMEOUT(h, p, highPQC)` at any time, where highPQC is the highest-period PQC it holds for h, or none. Q timeouts make **TC(h, p)**, and highPQC(TC) is the highest PQC among them. After timing out at p, a member signs nothing more at p.
4. **New period.** A proposal at (h, p' > 1) must carry TC(h, p'−1).
   - If highPQC(TC) = PQC(h, q, X), the proposal must be X.
   - With no PQC in the TC, it may be any block.
   - A member of C[p'] signs PREPARE(h, p', Y) only if the TC verifies against C[p'−1], the value rule holds, and either it holds no lock at h, or its lock is (q, Y), or the TC's highPQC is from a period above its lock.

**Safety, with f = ⌊(|C[p]|−1)/3⌋ Byzantine members per committee.** Suppose X is certified at (h, p).
- Q members of C[p] committed, so at least f+1 honest members of C[p] hold PQC(h, p, X) and are locked on it.
- Two PQCs at the same (h, p) for different values are impossible: two Q-sets in C[p] share at least f+1 members, at least one of them honest, and an honest member prepares one value per round (R1).
- Any TC(h, p) is Q members of C[p], so it shares at least f+1 members with the committers, at least one of them honest, whose highPQC is at period p or higher. So highPQC(TC(h, p)) is PQC(h, p, X), the only PQC at p.
- The proposal at p+1 must therefore be X, and the same holds by induction for every later period.
- Members of C[p+1] that were never in C[p] are bound because they **verify the TC against C[p]** before preparing. The lock lives in the proposal's justification, not only in memory, which is what makes it work across committee rotation.

**Liveness.** With 2f+1 honest, responsive members per committee after GST, every period either commits or produces a TC. Timeouts carry no "signed nothing" precondition, so the §4.1 deadlock can't occur. The cost is one extra round-trip per block (prepare → commit).

### 4.3 Wire changes and upgrade plan

**Upgrade: a flag day at an activation height, not a rolling upgrade.** A certificate is valid only if the committee signs the same commit domain and every voter applies the same lock rule. During a rolling upgrade, old members would keep signing under R1/R2 alone, and the guarantee needs every honest member. The activation height `consensus.locking_from_height` is fleet-agreed, set in yaml, and follows the existing `header_binding_from_height` pattern. Below it the current protocol runs unchanged. From it:

| Item | Change |
|---|---|
| Vote-result stream (`config/constants.go`) | New protocol ID for PREPARE/COMMIT requests; the old ID stays served for heights below the activation height. |
| Timeout messages | New protocol ID (sequencer ← members) for `TIMEOUT(h, p, highPQC)`. |
| Proposal / vote trigger | Carries TC(h, p−1) from period 2 on, and PQC for the commit request. |
| BLS signing domains | New `zkprepare:v4` and `zkcommit:v4` messages next to `zkvote:v3`. The certificate verifier selects the domain by height. |
| Ledger store | Lock record (period, block, PQC) per height in the same durable roundlock store. |

Block and transaction hashing don't change.

### 4.4 Tests phase 3 must add

- **Equivocation (devnet, 4 validators):** a patched sequencer proposes a second block at a higher period after the first reached quorum. Honest members refuse to prepare B without a TC whose highPQC allows it, and at most one certificate exists at h.
- **Restart:** a member restarts between committing A and receiving B, reloads its lock from the store, and still refuses B.
- **Timeout path:** a round with no prepare quorum yields TC(h, p) with no PQC, and B at p+1 is certified.
- **Liveness:** the 7-seat split from §4.1 completes at p+1.

## 5. Changes in phases 1–2 that affect clients

| Surface | Change |
|---|---|
| `eth_getBlockByNumber` | `"finalized"` and `"safe"` return the certified-with-certified-child block, or an error. They no longer return the head. |
| `eth_getLogs` | `toBlock`/`fromBlock` `"finalized"`/`"safe"` resolve the same way. Logs of blocks not stored at their height are no longer returned. |
| `eth_subscribe("logs")` | A log is delivered only once its block is stored (checked every 100 ms, in arrival order), and dropped if a different block is stored at its height or none is within 10 s. The subscription keeps draining while it waits, so a busy block loses nothing. A subscription is closed if its client takes nothing for 10 s while a log is ready, or if it would hold more than 65,536 logs; a WebSocket write that blocks for 10 s closes the connection. |
| `eth_call`, `eth_getCode`, `eth_getTransactionCount`, `eth_getStorageAt` | Refuse `"finalized"` and `"safe"`: they serve the latest state whatever block is asked for, so they cannot keep the promise those tags make. |
| `jmdt_getCertifiedBlock` | New method. |
| P2P protocol IDs, message formats | **None changed.** |
