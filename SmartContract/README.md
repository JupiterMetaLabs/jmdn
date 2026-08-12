# Smart Contract Module

## Overview

The Smart Contract module provides **Ethereum-compatible smart contract execution** for the JMZK Decentralized Network. It implements a full EVM (Ethereum Virtual Machine) with Ethereum-style state management, enabling deployment and execution of Solidity contracts with proper consensus verification.

## Architecture

### State Management

The module uses an **Ethereum-style StateDB** architecture:

```
┌─────────────────────────────────────────────────┐
│              StateDB (vm.StateDB)               │
│  Ethereum-compatible state management layer     │
├─────────────────────────────────────────────────┤
│ • Account balances & nonces (via DID Service)   │
│ • Contract code & storage (PebbleDB)           │
│ • Transaction journal (atomic operations)       │
│ • Access lists (EIP-2930)                      │
│ • Snapshots & reverts                          │
└─────────────────────────────────────────────────┘
         ↓                            ↓
    ┌─────────┐              ┌──────────────┐
    │ DID Svc │              │  PebbleDB    │
    │ gRPC    │              │ (Local KV)   │
    └─────────┘              └──────────────┘
```

### Key Components

#### 1. StateDB (`internal/state/`)

- **`contractsdb.go`**: Main StateDB implementation
- **`state_object.go`**: Per-account state tracking
- **`journal.go`**: Transaction journal for atomic commits/reverts
- **`access_list.go`**: EIP-2930 access list tracking
- **`state_accessors.go`**: Balance, nonce, code, storage operations

#### 2. EVM Integration (`internal/evm/`)

- **`deploy_contract.go`**: Contract deployment logic
- **`executor.go`**: EVM execution engine
- Uses injected StateDB for all state operations

#### 3. Storage Layer (`internal/storage/`)

- **`pebble.go`**: PebbleDB for contract code/storage persistence
- **`mem_store.go`**: In-memory storage for snapshots

#### 4. RPC Server (`cmd/main.go`)

- gRPC server on port `15055`
- Protobuf-based API (see `proto/smartcontract.proto`)

## Implementation Flow

### Complete Transaction Processing Flow

```
┌─────────────────────────────────────────────────────────┐
│                   Transaction Received                   │
└──────────────────────┬──────────────────────────────────┘
                       ↓
              ┌────────┴────────┐
              │ Transaction Type │
              └────────┬────────┘
       ┌──────────────┼──────────────┐
       ↓              ↓              ↓
  Type 0/1        Type 2         Type 2
  Regular         Deploy         Execute
       ↓              ↓              ↓
┌──────────────┐ ┌──────────┐ ┌──────────┐
│   Regular    │ │ Contract │ │ Contract │
│   Transfer   │ │  Deploy  │ │ Execute  │
└──────┬───────┘ └────┬─────┘ └────┬─────┘
       │              │            │
       └──────────────┼────────────┘
                      ↓
┌─────────────────────────────────────────────────────────┐
│      messaging/BlockProcessing/Processing.go            │
│      ProcessBlockTransactions(block, client, commit)    │
└──────────────────────┬──────────────────────────────────┘
                       ↓
┌─────────────────────────────────────────────────────────┐
│         Initialize StateDB (Ethereum-style)             │
│   SmartContract/internal/state/contractsdb.go           │
│   • Connect to DID Service (balances/nonces)           │
│   • Connect to PebbleDB (code/storage)                  │
│   • Create empty journal for atomicity                  │
└──────────────────────┬──────────────────────────────────┘
                       ↓
┌─────────────────────────────────────────────────────────┐
│                Transaction Execution                     │
│                                                          │
│  Regular Transfer:                                      │
│  • stateDB.SubBalance(sender, cost)                    │
│  • stateDB.AddBalance(recipient, value)                │
│                                                          │
│  Contract Deploy:                                       │
│  • ProcessContractDeployment(tx, stateDB)              │
│  • EVM.Create() with injected StateDB                   │
│                                                          │
│  Contract Execute:                                      │
│  • EVM.Call() with injected StateDB                     │
└──────────────────────┬──────────────────────────────────┘
                       ↓
              ┌────────┴────────┐
              │   Success?      │
              └────────┬────────┘
                   ┌───┴───┐
                   ↓       ↓
                  Yes      No
                   ↓       ↓
              ┌────────┐  ┌──────────┐
              │Commit? │  │  Revert  │
              └───┬────┘  │ Journal  │
                  │       └──────────┘
         ┌────────┴────────┐
         ↓                 ↓
    commitToDB=true   commitToDB=false
    (Sequencer)       (Buddy Node)
         ↓                 ↓
  ┌──────────────┐   ┌──────────────┐
  │StateDB.Commit│   │ Read-Only    │
  │→ DB_OPs      │   │ Verification │
  │→ PebbleDB    │   │ (No Persist) │
  └──────────────┘   └──────────────┘
```

### Detailed Path: Contract Deployment

**File Flow:**

```
1. User/Client
     ↓ gRPC: DeployContract
2. SmartContract/cmd/main.go
     ↓ Router forwards to handler
3. SmartContract/internal/router/handlers.go
     ↓ Creates transaction object
4. messaging/BlockProcessing/Processing.go
     ↓ ProcessBlockTransactions()
5. SmartContract/internal/state/contractsdb.go
     ↓ NewContractDB(didClient, pebbleDB)
6. SmartContract/internal/evm/deploy_contract.go
     ↓ ProcessContractDeployment(tx, stateDB, chainID)
7. SmartContract/internal/evm/executor.go
     ↓ NewEVMExecutor(), CreateContract()
8. go-ethereum/core/vm
     ↓ EVM.Create()

     During EVM.Create():
     • stateDB.SubBalance(deployer, gasCost)
     • stateDB.CreateAccount(contractAddr)
     • stateDB.SetCode(contractAddr, bytecode)
     • stateDB.SetState(contractAddr, storage)

9. SmartContract/internal/state/journal.go
     ↓ All changes recorded in journal
10. Back to Processing.go
     if success && commitToDB:
       stateDB.Commit()
     else if failed:
       journal.revert()
11. DB_OPs + PebbleDB
     ↓ Persist balances, nonces, code, storage
✅  Contract Deployed
```

**Code Example:**

```go
// In deploy_contract.go
func ProcessContractDeployment(
    tx *config.Transaction,
    stateDB state.StateDB,  // ← Injected, not created here
    chainID int,
) (*DeploymentResult, error) {
    // 1. Calculate contract address
    contractAddr := crypto.CreateAddress(*tx.From, tx.Nonce)

    // 2. Create EVM with injected StateDB
    executor := NewEVMExecutor(chainID)
    evm := executor.CreateEVM(stateDB, tx.From, contractAddr)

    // 3. Deploy (StateDB records all changes)
    result, err := executor.CreateContract(
        evm,
        *tx.From,
        tx.Data,  // bytecode
        tx.GasLimit,
        tx.Value,
    )

    // 4. State is NOT committed here
    // Caller (ProcessBlockTransactions) decides to commit or not

    return &DeploymentResult{
        ContractAddress: contractAddr,
        GasUsed:        result.GasUsed,
        Success:        err == nil,
    }, err
}
```

### Detailed Path: Contract Execution

**File Flow:**

```
1. User/Client
     ↓ gRPC: ExecuteContract
2. SmartContract/cmd/main.go
3. SmartContract/internal/router/handlers.go
4. messaging/BlockProcessing/Processing.go
     ↓ ProcessBlockTransactions()
5. StateDB initialization
6. SmartContract/internal/evm/executor.go
     ↓ CallContract()
7. go-ethereum/core/vm
     ↓ EVM.Call()

     During EVM.Call():
     • stateDB.GetBalance(caller)
     • stateDB.GetCode(contractAddr)  ← From PebbleDB
     • stateDB.GetState(contractAddr, key)
     • stateDB.SetState(contractAddr, key, value)
     • stateDB.SubBalance(caller, gasCost)
     • stateDB.AddBalance(recipient, value)
     • Emit events/logs

8. Journal records all changes
9. If success: continue
   If failed: journal.revert()
10. If commitToDB: stateDB.Commit()
✅  Contract Executed
```

### StateDB Lifecycle

**Phase 1: Initialization**

```go
// In ProcessBlockTransactions
stateDB := SmartContract.NewStateDB(chainID)
// Creates:
// • Empty stateObjects map
// • New transaction journal
// • Access list (EIP-2930)
```

**Phase 2: Execution**

```go
// For each state operation:
stateDB.GetBalance(addr)
  → Check cache
  → If miss: query DID Service
  → Cache result

stateDB.SubBalance(addr, amount)
  → Update in-memory balance
  → Record in journal: balanceChange{addr, prev, new}
  → Mark dirty

stateDB.SetCode(addr, code)
  → Store in memory
  → Record in journal: codeChange{addr, prevCode}
  → Mark dirty

stateDB.SetState(addr, key, value)
  → Store in memory storage map
  → Record in journal: storageChange{addr, key, prevValue}
  → Mark dirty
```

**Phase 3: Snapshots (for EVM subcalls)**

```go
snapshot := stateDB.Snapshot()
// Records current journal length

// Try risky operation
err := someOperation()

if err != nil {
    stateDB.RevertToSnapshot(snapshot)
    // Replays journal entries in reverse
    // Restores all previous values
}
```

**Phase 4: Commit or Discard**

```go
if commitToDB {  // Sequencer mode
    stateDB.Commit()
    // For each dirty stateObject:
    //   - Write balance to DB_OPs
    //   - Write nonce to DB_OPs
    //   - Write code to PebbleDB
    //   - Write storage to PebbleDB
    // Clear journal
} else {  // Buddy mode
    // Just discard StateDB
    // No database writes
    // Return verification result
}
```

### Critical Implementation Details

#### 1. State Object Architecture

```go
// Each accessed address gets a stateObject
type stateObject struct {
    address  common.Address

    // Account data (from DID Service)
    balance *uint256.Int
    nonce   uint64

    // Contract data (from PebbleDB)
    code    []byte
    storage map[common.Hash]common.Hash

    // Change tracking
    dirtyBalance bool
    dirtyNonce   bool
    dirtyCode    bool
    dirtyStorage map[common.Hash]struct{}
}

// On first access:
stateDB.getOrCreate(addr)
  → Query DID Service for balance/nonce
  → Query PebbleDB for code/storage
  → Create stateObject with loaded data
  → Cache in stateObjects map
```

#### 2. Transaction Journal

```go
// Every state change creates a journal entry
type journalEntry interface {
    revert(*ContractDB)
}

// Examples:
type balanceChange struct {
    address common.Address
    prev    *uint256.Int
}

func (ch balanceChange) revert(s *ContractDB) {
    s.getStateObject(ch.address).setBalance(ch.prev)
}

type storageChange struct {
    address common.Address
    key     common.Hash
    prev    common.Hash
}

func (ch storageChange) revert(s *ContractDB) {
    s.getStateObject(ch.address).setState(ch.key, ch.prev)
}
```

#### 3. Two-Phase Commit Pattern

```go
// Phase 1: Execute (all in memory)
for _, tx := range block.Transactions {
    snapshot := stateDB.Snapshot()

    err := executeTx(tx, stateDB)

    if err != nil {
        // Revert this transaction only
        stateDB.RevertToSnapshot(snapshot)
    }
    // Successful changes remain in StateDB
}

// Phase 2: Commit (only if commitToDB=true)
if commitToDB {
    for addr, obj := range stateDB.stateObjects {
        if obj.dirtyBalance {
            DB_OPs.UpdateAccountBalance(addr, obj.balance)
        }
        if obj.dirtyNonce {
            DB_OPs.UpdateAccountNonce(addr, obj.nonce)
        }
        if obj.dirtyCode {
            pebbleDB.Set(codeKey(addr), obj.code)
        }
        for key := range obj.dirtyStorage {
            pebbleDB.Set(storageKey(addr, key), obj.storage[key])
        }
    }
}
```

### Integration with Consensus

**Sequencer Node (Produces Blocks)**

```go
// In messaging/blockPropagation.go
func PropagateZKBlock(block *config.ZKBlock) {
    // Execute with commit
    err := ProcessBlockTransactions(
        block,
        accountsClient,
        commitToDB=true,  // ← Persist to database
    )

    if err != nil {
        log.Error("Block execution failed")
        return
    }

    // State is now persisted
    // Broadcast block to network
    BroadcastBlock(block)
}
```

**Buddy Node (Verifies Blocks)**

```go
// In AVC/BuddyNodes/MessagePassing/ListenerHandler.go
func HandleBlockStream(block *config.ZKBlock) {
    // Execute without commit (read-only)
    err := ProcessBlockTransactions(
        block,
        accountsClient,
        commitToDB=false,  // ← No database writes
    )

    if err == nil {
        // Verification passed
        Vote(block.Hash, approve=true)
    } else {
        // Verification failed
        Vote(block.Hash, approve=false)
    }
}
```

## Features

### ✅ Implemented

- **Contract Compilation**: Solidity → bytecode via `solc`
- **Contract Deployment**: Deterministic address calculation
- **Contract Execution**: Full EVM with gas metering
- **State Management**: Atomic transactions with journal-based reverts
- **Storage Operations**: GetCode, GetStorage, SetStorage
- **Access Lists**: EIP-2930 support
- **Gas Estimation**: Accurate gas calculations
- **Event Logs**: Contract event emission and retrieval
- **Consensus Ready**: `commitToDB` parameter for sequencer vs buddy modes

### 🔄 Transaction Flow

#### Standalone Mode (Default)

```go
// Sequencer commits all state changes
ProcessBlockTransactions(block, accountsClient, commitToDB=true)
  → StateDB initialized
  → All transactions executed
  → State changes committed to DB
  → Balances/nonces persisted
```

#### Consensus Mode (When Enabled)

```go
// Sequencer (commits state)
ProcessBlockTransactions(block, accountsClient, commitToDB=true)
  → Execute transactions
  → Commit state to database

// Buddy Nodes (verify only)
ProcessBlockTransactions(block, accountsClient, commitToDB=false)
  → Execute transactions (read-only)
  → Verify state root matches
  → No database commits
  → Vote on consensus
```

## API Endpoints

### gRPC Service (Port 15055)

#### Contract Management

```protobuf
// Compile Solidity source code
CompileContract(CompileRequest) → CompileResponse

// Deploy a contract
DeployContract(DeployContractRequest) → DeployContractResponse

// Execute a contract function (state-changing)
ExecuteContract(ExecuteContractRequest) → ExecuteContractResponse

// Call a contract function (read-only)
CallContract(CallContractRequest) → CallContractResponse
```

#### State Queries

```protobuf
// Get contract bytecode
GetContractCode(GetContractCodeRequest) → GetContractCodeResponse

// Get storage slot value
GetStorage(GetStorageRequest) → GetStorageResponse

// Estimate gas for operation
EstimateGas(EstimateGasRequest) → EstimateGasResponse
```

#### Utilities

```protobuf
// Encode function call data
EncodeFunctionCall(EncodeFunctionCallRequest) → EncodeFunctionCallResponse

// Decode function output
DecodeFunctionOutput(DecodeFunctionOutputRequest) → DecodeFunctionOutputResponse
```

## Usage Examples

### Deploying a Contract

```go
// Compile contract
compileResp, err := client.CompileContract(ctx, &proto.CompileRequest{
    SourceCode: soliditySource,
})

// Deploy
deployResp, err := client.DeployContract(ctx, &proto.DeployContractRequest{
    Bytecode:        compileResp.Contract.Bytecode,
    Caller:          callerAddress,
    GasLimit:        3000000,
    Value:           big.NewInt(0).Bytes(),
    ConstructorArgs: encodedArgs,
    Abi:             compileResp.Contract.Abi,
})

contractAddr := deployResp.Result.ContractAddress
```

### Calling a Contract

```go
// Encode function call
encodeResp, err := client.EncodeFunctionCall(ctx, &proto.EncodeFunctionCallRequest{
    AbiJson:      contractAbi,
    FunctionName: "setValue",
    Args:         [][]byte{valueBytes},
})

// Execute (state-changing)
execResp, err := client.ExecuteContract(ctx, &proto.ExecuteContractRequest{
    ContractAddress: contractAddr,
    Caller:          callerAddress,
    Input:           encodeResp.EncodedData,
    GasLimit:        100000,
    Value:           big.NewInt(0).Bytes(),
})

// Call (read-only)
callResp, err := client.CallContract(ctx, &proto.CallContractRequest{
    ContractAddress: contractAddr,
    Caller:          callerAddress,
    Input:           getValueCalldata,
})
```

## Integration with Block Processing

Smart contracts are processed during block execution:

```go
// In messaging/BlockProcessing/Processing.go
func ProcessBlockTransactions(block *config.ZKBlock, accountsClient *config.PooledConnection, commitToDB bool) error {
    // Initialize StateDB
    stateDB := SmartContract.NewStateDB(chainID)

    // Process each transaction
    for _, tx := range block.Transactions {
        if tx.Type == 2 { // Smart contract
            if tx.To == nil {
                // Contract deployment
                ProcessContractDeployment(tx, stateDB, chainID)
            } else {
                // Contract execution
                ProcessContractExecution(tx, stateDB, chainID)
            }
        } else {
            // Regular transfer using StateDB
            stateDB.SubBalance(tx.From, totalCost)
            stateDB.AddBalance(tx.To, tx.Value)
        }
    }

    // Commit only if commitToDB=true (sequencer mode)
    if commitToDB {
        stateDB.Commit()
    }
}
```

## Directory Structure

```
SmartContract/
├── cmd/
│   └── main.go                 # gRPC server entry point
├── internal/
│   ├── evm/
│   │   ├── deploy_contract.go  # Deployment logic
│   │   └── executor.go         # EVM execution
│   ├── state/
│   │   ├── contractsdb.go      # StateDB implementation
│   │   ├── state_object.go     # Account state tracking
│   │   ├── journal.go          # Transaction journal
│   │   ├── access_list.go      # EIP-2930 support
│   │   └── state_accessors.go  # State operations
│   ├── storage/
│   │   ├── pebble.go          # PebbleDB persistence
│   │   └── mem_store.go       # In-memory storage
│   └── router/
│       └── handlers.go        # gRPC request handlers
├── proto/
│   └── smartcontract.proto    # Protocol definitions
├── pkg/
│   └── client/
│       └── client.go          # Go client SDK
├── artifacts/                 # Compiled contracts (ABI + bytecode)
├── processor.go              # Main processor interface
├── interface.go              # Public API
└── README.md                 # This file
```

## Configuration

### Environment Variables

```bash
# Smart Contract gRPC Server
SMART_CONTRACT_PORT=15055

# DID Service (for balance/nonce queries)
DID_SERVICE_ADDR=localhost:50051

# Storage
PEBBLE_DB_PATH=./data/contracts
```

### Chain Configuration

```go
// In config/constants.go
const (
    ChainID = 1  // Ethereum mainnet compatible
)

// EVM Configuration (Shanghai fork)
chainConfig = &params.ChainConfig{
    ChainID:             big.NewInt(1),
    HomesteadBlock:      big.NewInt(0),
    EIP150Block:         big.NewInt(0),
    EIP155Block:         big.NewInt(0),
    EIP158Block:         big.NewInt(0),
    ByzantiumBlock:      big.NewInt(0),
    ConstantinopleBlock: big.NewInt(0),
    PetersburgBlock:     big.NewInt(0),
    IstanbulBlock:       big.NewInt(0),
    BerlinBlock:         big.NewInt(0),
    LondonBlock:         big.NewInt(0),
    ShanghaiTime:        new(uint64), // Enabled
}
```

## Development

### Running the Server

```bash
# Build
cd SmartContract
go build -o smartcontract cmd/main.go

# Run
./smartcontract
```

### Testing

```bash
# Unit tests
go test ./internal/state/...
go test ./internal/evm/...

# Integration test (requires running server)
go run examples/sdk_demo/main.go
```

### Debugging

Enable debug logging:

```go
// Set log level
log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
zerolog.SetGlobalLevel(zerolog.DebugLevel)
```

## Security Considerations

### Gas Limits

- All contract operations are gas-metered
- Default limits prevent infinite loops
- Gas estimation available via `EstimateGas`

### State Isolation

- Each transaction executes in isolated StateDB snapshot
- Failed transactions revert all state changes
- No partial state commits

### Access Control

- Contract addresses are deterministic (CREATE opcode)
- Nonce-based replay protection
- Signature verification (when integrated with transaction signing)

### Atomicity

- Journal-based state management ensures atomicity
- All-or-nothing transaction execution
- Proper error propagation

## Roadmap

### Current Status

- ✅ Full EVM execution
- ✅ Ethereum-style StateDB
- ✅ Consensus verification support
- ✅ gRPC API

### Future Enhancements

- [ ] CREATE2 support for deterministic contract addresses
- [ ] Event filtering and indexing
- [ ] JSON-RPC interface (eth\_\* methods)
- [ ] Multi-node consensus integration
- [ ] Transaction pool and mempool
- [ ] Gas price oracle
- [ ] Contract upgrade patterns (proxy contracts)

## Troubleshooting

### Common Issues

**Issue:** Contract deployment fails with "gas uint64 overflow"

- **Solution**: Ensure EVM config has Shanghai enabled with proper timestamp

**Issue:** "Method not found" errors

- **Solution**: Server not running or wrong port. Check `localhost:15055`

**Issue:** State not persisting

- **Solution**: Ensure `commitToDB=true` in standalone mode

**Issue:** Nil pointer errors in transaction conversion

- **Solution**: Updated `gETH/utils.go` with nil checks (committed in latest version)

## References

- [Go-Ethereum Documentation](https://geth.ethereum.org/docs)
- [Solidity Documentation](https://docs.soliditylang.org/)
- [EVM Opcodes](https://ethereum.org/en/developers/docs/evm/opcodes/)
- [EIP-2930: Access Lists](https://eips.ethereum.org/EIPS/eip-2930)

## Contributing

When contributing to the Smart Contract module:

1. **Follow Ethereum standards**: Maintain compatibility with go-ethereum
2. **Add tests**: All new features need unit tests
3. **Document**: Update this README and inline comments
4. **Gas safety**: All operations must be gas-metered
5. **State safety**: Use journal for all state changes

## License

[Your License Here]
