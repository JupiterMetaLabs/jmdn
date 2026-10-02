# EVM-created accounts have no block-carried ART identity (testnet block 936)

## Symptom

`testnet-seq` (`v2.0.1-579-gcec52d4`), block 936, 2026-10-02:

```
contract tx 0x93f0…e5cf: new account 0x622B…bEE3E has no block-carried ART identity
Block apply+store failed - block not applied (rolled back, withheld from peers)
Failed to broadcast or process block locally  consensus_reached=true
```

`0x93f0…` is `DIDRegistry.registerDID(…, wallet=0x622B…, 1e18)`. It calls
`SettlementContract.transferReward`, which does `payable(wallet).call{value}`. That
internal transfer is what creates the wallet account.

## Root cause

- **Stamping happens before execution.** `DB_OPs.EnrichBlockAccountNonces` runs
  before `consensus.Start` (`Block/Server.go`). It stamps an ART identity only for
  `tx.From`, `tx.To`, `CreateAddress(from, tx.Nonce)` and `FeeRecipients`.
- **The check happens after execution.** `applyContractTx` adds every
  `ExecResult.BalanceChanges` address to the touched set. That covers an internal
  `CALL{value}`, a SELFDESTRUCT beneficiary, and a value-funded CREATE/CREATE2
  child. The check then fails closed for any new address that has no carried
  identity.
- **The whole block is lost.** Committee members vote before anyone executes the
  block. The sequencer applies it only after quorum, fails, rolls back, withholds
  the block and retries the same height. The same transaction fails again on every
  retry, so the height stalls.

Reproduction test: `TestInternalCreate_ReproLegacy` produces the production error
string exactly.

## Fix

### A. Sequencer pre-consensus prediction (wire-compatible)

- `BlockProcessing.PredictContractCreatedAccounts` executes every contract tx in
  the block through `execbridge.ExecuteTx`.
  - Nothing is committed: `CommitState` is never called, and balances are only
    returned.
  - It uses the same `contractExecContext` as the apply path, so the EVM sees the
    same block number, hash, time, coinbase, tx index and gas limit.
- `DB_OPs.EnrichBlockAccountNoncesWithPredicted` stamps every predicted address in
  the existing ordinal pass. Existing accounts get their stored identity; new
  accounts get the next ordinals, assigned in ascending-address order.
- `Block/Server.go` runs the prediction immediately before enrichment. A state-read
  error during simulation rejects the proposal (fail closed), the same way an
  enrichment error does.
- Validators only read `AccountNonces`, so a pre-fix validator applies these blocks
  unchanged. `TestReplayNewBlocksOnOldValidator` replayed the new sequencer's
  blocks on `v3base` (db8c266) and every fingerprint matched.

**Limitation.** Each tx is simulated against the pre-block state. If a tx depends
on an earlier tx in the same block, the simulation can miss an account it creates.

### B. Deterministic revert backstop (consensus rule; height-gated)

Setting: `consensus.evm_unstamped_account_revert_height`. The default is `0`, which
keeps the legacy behaviour.

From that height on, a contract tx that would create an unstamped account is
applied as an EVM revert:

- execution effects are discarded;
- gas is charged;
- the sender nonce is bumped;
- the receipt has status 0.

The rest of the block applies normally.

**Determinism.** The branch depends only on three inputs, and every node has the
same ones:

- the block-carried `AccountNonces`, which are the same bytes on every node;
- the deterministic EVM result;
- whether the account already exists, which is the same for any node at this height
  that has not diverged. A diverged node is caught by the `StateFingerprint` halt.

Infrastructure errors and stale nonces still fail the whole block, as before.

## Rollout

1. **A on its own needs no coordinated upgrade.** Deploy it to the sequencer first;
   old validators accept its blocks.
2. **B is a consensus rule change.** Upgrade every node, then set the same
   `evm_unstamped_account_revert_height` on all of them, at a height the chain has
   not reached. Until that height every node keeps the legacy behaviour.
   - Activating on only some nodes splits them: in the
     `TestInternalCreate_BackstopRevertsOnlyTheTx` scenario, an old node fails a
     block that new nodes apply.

## Tests

```
APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
APPLYGATE_DEPLOY_BYTECODE=<SimpleStorage bin> \
CGO_ENABLED=1 go test -tags applygate ./messaging/BlockProcessing/ -v
```

Each scenario uses two independent ThebeDB stores: A is the sequencer and B a
validator replaying A's blocks. Fingerprint equality is checked on every block, and
ART identity equality is checked for each new account.

`TestInternalCreate_*` covers:

- the legacy reproduction;
- `Forward.pay` / `payTwo` (two fresh accounts in one tx);
- CREATE and CREATE2 with value, plus a zero-value CREATE;
- a SELFDESTRUCT beneficiary;
- the SuperJ register shape (`WelcomePaid` emitted, wallet balance 1e18);
- the backstop on and off;
- prediction having no side effects.

Fixtures: `testdata/InternalCreate.sol`.

## Residual risks

- A tx that fails before execution for an infrastructure or nonce reason still
  fails the whole block, by design.
- A successful contract tx can credit third parties that are outside
  `affectedAccountsForBlock`, and contract storage has no undo. So when a later tx
  in the same block fails, the in-loop rollback restores only part of the state
  (pre-existing; D-858 B2 guards only the store-failure path).
- `AccountNonces` are not hash-covered. A relay that strips the predicted entries
  would turn a success on the sequencer into a revert on validators; the fingerprint
  check would catch it and halt. This trust model is unchanged.
- An account created by a zero-value CREATE gets no ledger account until it first
  receives value (unchanged).
