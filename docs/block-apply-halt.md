# Block-apply halt

A node stops applying blocks when its state holds part of a block it could not roll back. This page covers what causes that, how to see it, how to prevent it, and how to recover.

## What causes it

A block that fails during apply is rolled back to the state before the block. Two failures leave the node unable to do that:

1. **A block fails after one of its contract transactions succeeded.** Contract storage cannot be rolled back, so the node refuses to roll the block back.
2. **A rollback write fails** after the block's transaction markers were revoked.

Either way, the node's state is neither the state before the block nor the state after it. Applying another block on top would build on state no other node has. So the node stops applying blocks, and its state must be rebuilt.

The halt is recorded in the node's store (sync KV key `block_apply_halt`), so a restart does not lift it.

## How to see it

- **Log line**, once, with the reason: `BLOCK APPLY HALTED — this node holds a partially applied block and must be resynced`.
- **Every later block** fails to apply with `block apply halted: state holds a partially applied block and must be resynced`.
- **On the sequencer**, consensus rounds are not started: `CONSENSUSERROR.HALTED: not proposing (...)`. Block production stops. The block that caused the halt also raises the existing "Failed to process block locally" alert.
- **Status endpoint**, on the facade port (`ports.facade`, 8545 by default) with the admin token (`security.admin_token`):

  ```
  curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://<node>:8545/consensus/apply-halt
  ```

  It returns `{"halted": true, "reason": "..."}`.

The reason gives the block number, the block hash and the error that failed it.

## The most likely cause, and how to prevent it

While `consensus.evm_unstamped_account_revert_height` is `0` (the default), a contract transaction whose execution creates an account without a block-stamped identity fails its whole block. The reason then contains `has no block-carried ART identity`. If an earlier contract transaction in the same block succeeded, the node halts.

Setting the revert height makes that one transaction a status-0 revert instead: gas is charged, nothing else happens, and the block applies. It is a fleet-agreed consensus parameter:

- Set the same value on every node that applies blocks: the sequencer, validators, RPC nodes and exchange nodes.
- Choose a height the chain has not reached yet.
- Restart every node with the new value before the chain reaches that height.

A node still on the old value fails a block that contains such a transaction, and lags until it is restarted with the new value.

Other failures after a successful contract transaction can still halt a node: a stale nonce, a fee the sender cannot pay, or a storage error. Those need their own investigation.

## How to recover

1. **Leave the halt in place.** Do not clear it to get the node going again: its state is wrong until it is rebuilt.
2. **Prevent a repeat first.** If the reason is the identity case above, activate the revert height fleet-wide before the sequencer produces blocks again. The transactions of the failed block may be proposed again, and on the old value they would halt it again.
3. **Rebuild the node's state.** Treat it like any node whose state diverged:
   - stop the node;
   - wipe its ThebeDB store (`thebe.kv_path` and its Postgres database in `thebe.sql_dsn`), along with its other storage directories;
   - sync it from a healthy node.

   The wipe also removes the halt record. A sequencer can be rebuilt from a validator: the validators never applied the failed block.
4. **If the state was repaired another way**, first check that it matches a healthy node. With contracts enabled, the state fingerprint of the next certified block refuses a state that differs. Then lift the halt:

   ```
   curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" http://<node>:8545/consensus/apply-halt/clear
   ```

   The response, and a warning in the log, give the reason that was cleared.

If the sequencer's failed rounds left a height stuck at the vote-period cap, use the existing round-ledger recovery (`POST /consensus/round-ledger/clear`) after the rebuild.

## What the halt does not stop

The halt stops block apply and, on the sequencer, starting consensus rounds. FastSync, account sync and reconciliation keep running: they are how the node's state is rebuilt. Committee voting is not stopped either.
