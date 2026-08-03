# Architectural Decisions: StateDB Integration

## Decision Summary

**Date:** 2026-01-30  
**Status:** Implemented  
**Context:** Consensus Verification Flow

## Problem Statement

The original implementation had split execution logic:

- **Financial logic** (fees, transfers) in `messaging/BlockProcessing/Processing.go`
- **EVM logic** (contracts) in `SmartContract/internal/evm/`

This created several issues:

1. **Race conditions** between balance updates
2. **Manual rollbacks** (`rollbackBalances`) needed on failures
3. **No atomicity** - couldn't rollback partial transaction failures
4. **Inconsistent state** - different code paths for regular vs contract transactions
5. **Consensus verification impossible** - no way to run read-only execution

## Decision

Adopt **Ethereum-style StateDB** architecture as the single source of truth for ALL state changes.

### Key Principles

1. **Single State Manager**: StateDB handles all state (balances, nonces, code, storage)
2. **Buffer First, Write Later**: All changes buffered in memory, written only on `Commit()`
3. **Journal-Based Reverts**: In-memory snapshots and rollbacks, no database rollbacks
4. **Separation of Storage**:
   - Account data (balances, nonces) → DID Service via gRPC
   - Contract data (code, storage) → PebbleDB
5. **Consensus-Ready**: `commitToDB` flag enables sequencer vs buddy node modes

## Architecture

### Before (Split Logic)

```
Transaction Processing
├── Regular Transfer
│   ├── DB_OPs.DeductFromSender()      ← Direct DB write
│   ├── DB_OPs.AddToRecipient()        ← Direct DB write
│   └── If fails: rollbackBalances()   ← Manual rollback
│
└── Smart Contract
    ├── StateDB operations (in-memory)
    ├── StateDB.Commit()                ← Writes to PebbleDB only
    └── Separate gas/fee logic          ← Not in StateDB!
```

**Problems:**

- Two different code paths
- Manual error handling
- No unified rollback mechanism
- Consensus verification impossible

### After (Unified StateDB)

```
Transaction Processing
├── Initialize StateDB
│   ├── Connect to DID Service
│   ├── Connect to PebbleDB
│   └── Create transaction journal
│
├── Execute ALL Transactions via StateDB
│   ├── Regular Transfer
│   │   ├── StateDB.SubBalance(sender)
│   │   └── StateDB.AddBalance(recipient)
│   │
│   └── Smart Contract (Deploy/Execute)
│       ├── StateDB.SubBalance(sender, gas)
│       ├── EVM.Create/Call (uses StateDB)
│       └── StateDB.AddBalance(miner, fees)
│
└── Commit or Discard
    ├── if commitToDB=true: StateDB.Commit()
    └── if commitToDB=false: discard (verification only)
```

**Benefits:**

- Single code path for all transactions
- Automatic rollback via journal
- Atomic execution
- Consensus-ready

## Implementation Details

### StateDB Structure

```go
type ContractDB struct {
    // Database connections
    didClient pbdid.DIDServiceClient  // For balances/nonces
    db        storage.KVStore         // For code/storage (PebbleDB)

    // In-memory state cache
    stateObjects map[common.Address]*stateObject

    // Transaction journal (for reverts)
    journal *journal

    // Other EVM requirements
    accessList *accessList
    logs       []*types.Log
    refund     uint64
}

type stateObject struct {
    address common.Address

    // Account data (cached from DID Service)
    balance *uint256.Int
    nonce   uint64

    // Contract data (cached from PebbleDB)
    code    []byte
    storage map[common.Hash]common.Hash

    // Dirty flags (what changed)
    dirtyBalance bool
    dirtyNonce   bool
    dirtyCode    bool
    dirtyStorage map[common.Hash]struct{}
}
```

### Journal Mechanism

Every state change creates a journal entry:

```go
type journalEntry interface {
    revert(*ContractDB)
}

// Examples:
balanceChange{addr, prevBalance}
nonceChange{addr, prevNonce}
codeChange{addr, prevCode}
storageChange{addr, key, prevValue}
```

On transaction failure or snapshot revert:

```go
for i := len(journal) - 1; i >= snapshot; i-- {
    journal[i].revert(stateDB)
}
```

### Two-Phase Commit

```go
// Phase 1: Execute (everything in memory)
stateDB.SubBalance(sender, cost)
stateDB.AddBalance(recipient, value)
// ... more operations ...

// Phase 2: Persist (only if commitToDB=true)
if commitToDB {
    for addr, obj := range stateDB.stateObjects {
        if obj.dirtyBalance {
            DB_OPs.UpdateAccountBalance(addr, obj.balance)
        }
        if obj.dirtyCode {
            pebbleDB.Set(codeKey(addr), obj.code)
        }
        // ... etc
    }
}
```

## Consensus Integration

### Sequencer Mode

```go
ProcessBlockTransactions(block, client, commitToDB=true)
// Executes transactions
// Commits all state changes to database
// State is now persisted
```

### Buddy Node Mode

```go
ProcessBlockTransactions(block, client, commitToDB=false)
// Executes transactions (read-only)
// Verifies execution matches expected result
// Does NOT commit to database
// Returns verification result for voting
```

## Alternatives Considered

### Alternative 1: Keep Split Logic

**Rejected because:**

- Doesn't solve atomicity problem
- Can't support consensus verification
- Requires complex manual rollback code

### Alternative 2: Database Transactions

**Rejected because:**

- ImmuDB doesn't support traditional ACID transactions
- Would require complete DB refactor
- Performance concerns with distributed DB transactions

### Alternative 3: Event Sourcing

**Rejected because:**

- Over-engineered for current needs
- Major architectural change
- Not Ethereum-compatible

## Consequences

### Positive

✅ **Atomic execution** - All-or-nothing transaction processing  
✅ **Consensus-ready** - Read-only verification supported  
✅ **Ethereum-compatible** - Follows go-ethereum patterns  
✅ **Simplified code** - Single code path for all transactions  
✅ **Better testing** - Can test without database  
✅ **No manual rollbacks** - Journal handles all reverts

### Negative

⚠️ **Memory usage** - All state changes buffered in memory  
⚠️ **Learning curve** - Team needs to understand StateDB model  
⚠️ **Migration effort** - Existing code needs updates

### Mitigations

- Memory: Reasonable for block-sized batches
- Learning: This document + code comments
- Migration: Incremental, backwards compatible

## Files Changed

| File                                              | Type     | Change                              |
| ------------------------------------------------- | -------- | ----------------------------------- |
| `SmartContract/internal/state/contractsdb.go`     | Modified | Refactored to use stateObject model |
| `SmartContract/internal/state/state_object.go`    | New      | Per-account state tracking          |
| `SmartContract/internal/state/journal.go`         | New      | Transaction journal                 |
| `SmartContract/internal/state/state_accessors.go` | New      | State operations                    |
| `SmartContract/internal/storage/mem_store.go`     | New      | In-memory storage                   |
| `messaging/BlockProcessing/Processing.go`         | Modified | Added `commitToDB` parameter        |
| `SmartContract/internal/evm/deploy_contract.go`   | Modified | Inject StateDB instead of creating  |

## Validation

### Unit Tests

- [x] Journal revert mechanism
- [x] State object dirty tracking
- [x] Snapshot and revert

### Integration Tests

- [x] Regular transfer with StateDB
- [x] Contract deployment with StateDB
- [x] Contract execution with StateDB
- [x] Failed transaction rollback

### Manual Testing

- [x] Sequencer commits state (`commitToDB=true`)
- [ ] Buddy verifies without committing (`commitToDB=false`)
- [x] Node logs show proper StateDB usage

## References

- [Go-Ethereum StateDB](https://github.com/ethereum/go-ethereum/blob/master/core/state/statedb.go)
- [Ethereum Yellow Paper](https://ethereum.github.io/yellowpaper/paper.pdf) - State transition
- [`evm_update.md`](./evm_update.md) - Original refactor specification

## Future Improvements

1. **Trie-based state root** - Currently not calculating Merkle root
2. **State preloading** - Batch-load accounts at block start
3. **Parallel execution** - Independent transactions in parallel
4. **State pruning** - Remove old state to save space
