# Block-apply halt

A node stops applying blocks when its state holds part of a block it could not roll back. This page covers what causes that, how to see it, how to prevent it, and how to recover.

## What causes it

A block that fails during apply is rolled back to the state before the block. Three failures leave the node unable to do that:

1. **A block fails after one of its contract transactions succeeded**, while applying a later transaction, committing a group, or because its state fingerprint does not match. Contract storage cannot be rolled back, so the node refuses to roll the block back.
2. **A rollback write fails** after the block's transaction markers were revoked.
3. **A commit fails part-way and the rollback cannot run** (its reads, the drop of the pending-apply record, or the marker revocation fail). Some of the commit's writes may have landed without a marker.

In each case, the node's state is neither the state before the block nor the state after it. Applying another block on top would build on state no other node has. So the node stops applying blocks, and its state must be rebuilt.

Not a halt on a validator: when storing the block, or computing its fingerprint, fails after every transaction of a block with a successful contract transaction was applied, the node keeps the block applied and marked (its state is the complete post-block state) and returns an error. The block is completed when it arrives again.

On the sequencer the same failure is completed or halted at once. The sequencer withholds a block it could not store, so the block never arrives again: it applies the same certified block up to three more times, which skips the marked transactions and stores it. If that still fails, it halts with a reason that starts `producer: block N (...) is applied with its contract state but could not be stored`, and no new round starts on top of it.

The halt is recorded in the node's store (sync KV key `block_apply_halt`), so a restart does not lift it. If that write fails, the node retries it on every block; the status endpoint below shows whether it has succeeded.

## How to see it

- **Log line**, once, with the reason: `BLOCK APPLY HALTED — this node holds a partially applied block and must be resynced`.
- **Every later block** fails to apply with `block apply halted: state holds a partially applied block and must be resynced`.
- **On the sequencer**, consensus rounds are not started: `CONSENSUSERROR.HALTED: not proposing (...)`. Block production stops. The block that caused the halt also raises the existing "Failed to process block locally" alert.
- **Status endpoint**, on the facade port (`ports.facade`, 8545 by default) with the admin token (`security.admin_token`):

  ```
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://<node>:8545/consensus/apply-halt
  ```

  It returns `{"halted": true, "reason": "...", "recorded": true}`. `recorded: false` means the halt is not yet in the node's store (the store was failing when it happened); the node retries on every block, and until it succeeds a restart would lift the halt. Do not restart a halted node in that state.

The reason gives the block number, the block hash and the error that failed it.

## The most likely cause, and how to prevent it

While `consensus.evm_unstamped_account_revert_height` is `0` (the default), a contract transaction whose execution creates an account without a block-stamped identity fails its whole block. The reason then contains `has no block-carried ART identity`. If an earlier contract transaction in the same block succeeded, the node halts.

Setting the revert height makes that one transaction a status-0 revert instead: gas is charged, nothing else happens, and the block applies. It is a fleet-agreed consensus parameter:

- Set the same value on every node that applies blocks: the sequencer, validators, RPC nodes and exchange nodes.
- Choose a height the chain has not reached yet.
- Restart every node with the new value before the chain reaches that height.

A node still on the old value fails a block that contains such a transaction. If an earlier contract transaction in that block succeeded, it halts, and a restart does not lift that; otherwise it lags until it is restarted with the new value.

Other failures after a successful contract transaction can still halt a node: a stale nonce, a fee the sender cannot pay, or a storage error. Those need their own investigation.

## How to recover

1. **Leave the halt in place.** Do not clear it to get the node going again: its state is wrong until it is rebuilt.
2. **Prevent a repeat first.** If the reason is the identity case above, activate the revert height fleet-wide before the sequencer produces blocks again. The transactions of the failed block may be proposed again, and on the old value they would halt it again.
3. **Rebuild the node's state.** Treat it like any node whose state diverged:
   - stop the node;
   - wipe its ThebeDB store (`thebe.kv_path`, which also holds the write outbox `outbox.db`, and its Postgres database in `thebe.sql_dsn`), along with its other storage directories;
   - sync it from a healthy node.

   The wipe also removes the halt record. A sequencer can be rebuilt from a validator: the validators never applied the failed block.
4. **Cause 3 on a validator: a restart may be enough.** If the reason ends with `the rollback could not run (discard apply: ...)`, the pending-apply record could not be dropped: restart the node once its store is healthy. Boot recovery completes an interrupted apply whose record is still there, and logs `thebedb: completed tx apply interrupted by previous shutdown` with the transaction hash. If that hash is the one in the halt reason, the validator's state is consistent again: the failed block is certified, and when it arrives again the node applies the transactions after that one. Clear the halt (next step). If the line does not appear, or this is the sequencer (it will not see the withheld block again), rebuild as in step 3.
5. **If the state was repaired another way**, first check that it matches a healthy node. With contracts enabled, the state fingerprint of the next certified block refuses a state that differs. Then lift the halt:

   ```
   curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" http://<node>:8545/consensus/apply-halt/clear
   ```

   The response, and a warning in the log, give the reason that was cleared.

If the sequencer's failed rounds left a height stuck at the vote-period cap, use the existing round-ledger recovery (`POST /consensus/round-ledger/clear`) after the rebuild.

## What the halt does not stop

The halt stops block apply, the storing of any block that was still applying when it happened, and, on the sequencer, starting consensus rounds. Catch-up sync applies blocks, so it is stopped too. Account sync, reconciliation, the write outbox and boot recovery keep writing: before the wipe in step 3 they write on top of the partial state, which is one more reason the wipe is needed; after it they help rebuild the node. Committee voting is not stopped either.
