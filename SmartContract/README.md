# SmartContract Module

## Overview

The SmartContract module provides smart contract compilation and execution capabilities for the JMZK network. It supports Solidity compilation, EVM execution, and state management for smart contracts.

## Purpose

The SmartContract module enables:
- Solidity contract compilation
- EVM contract execution
- Contract deployment
- Contract state management
- ABI encoding and decoding
- Gas estimation

## Key Components

### 1. Compiler
**File:** `compiler.go`

Solidity contract compilation:
- `CompileSolidity`: Compile Solidity source files
- `CompiledContract`: Compiled contract structure
- Standard JSON input/output format
- Optimizer configuration

### 2. EVM Executor
**File:** `evm.go`

EVM execution environment:
- `EVMExecutor`: EVM execution manager
- `NewEVMExecutor`: Create new EVM executor
- `DeployContract`: Deploy smart contract
- `ExecuteContract`: Execute contract function
- `ExecutionResult`: Execution result structure

### 3. State Database
**File:** `statedb.go`

Contract state management:
- State database implementation
- Account balance management
- Storage management
- Nonce management

### 4. State Database Helper
**File:** `statedbHelper.go`

State database utilities:
- Helper functions for state operations
- Account management
- Storage operations

### 5. EVM Helper
**File:** `EVMHelper.go`

EVM utility functions:
- Hash function implementation
- Block context management
- Transaction context management

## Key Functions

### Compile Solidity

```go
// Compile Solidity contract
func CompileSolidity(sourcePath string) (map[string]*CompiledContract, error) {
    // Read source code
    // Create standard JSON input
    // Run solc compiler
    // Parse output
    // Return compiled contracts
}
```

### Deploy Contract

```go
// Deploy smart contract
func (e *EVMExecutor) DeployContract(state vm.StateDB, caller common.Address,
    code []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error) {
    // Create EVM instance
    // Deploy contract
    // Return execution result
}
```

### Execute Contract

```go
// Execute contract function
func (e *EVMExecutor) ExecuteContract(state vm.StateDB, caller common.Address,
    contractAddr common.Address, input []byte, value *big.Int, gasLimit uint64) (*ExecutionResult, error) {
    // Create EVM instance
    // Call contract function
    // Return execution result
}
```

## Usage

### Compile Contract

```go
import "gossipnode/SmartContract"

// Compile Solidity contract
contracts, err := SmartContract.CompileSolidity("./SmartContract/example/SimpleToken.sol")
if err != nil {
    log.Fatal(err)
}

// Access compiled contract
contract := contracts["SimpleToken"]
fmt.Printf("Bytecode: %s\n", contract.Bytecode)
fmt.Printf("ABI: %s\n", contract.ABI)
```

### Deploy Contract

```go
import "gossipnode/SmartContract"

// Create EVM executor
executor := SmartContract.NewEVMExecutor(chainID)

// Deploy contract
result, err := executor.DeployContract(state, caller, code, value, gasLimit)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Contract deployed at: %s\n", result.ContractAddr.Hex())
```

### Execute Contract

```go
// Execute contract function
result, err := executor.ExecuteContract(state, caller, contractAddr, input, value, gasLimit)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Return data: %x\n", result.ReturnData)
fmt.Printf("Gas used: %d\n", result.GasUsed)
```

## Integration Points

### Block Module
- Executes smart contracts in blocks
- Processes contract transactions

### gETH Module
- Provides contract execution via gRPC
- Handles contract calls

### Database (DB_OPs)
- Stores contract state
- Manages contract accounts

### Config Module
- Uses chain ID configuration
- Accesses transaction structures

## Configuration

Smart contract configuration:
- Solidity compiler path (default: `solc`)
- Optimizer settings (default: enabled, 200 runs)
- EVM version (default: london)
- Artifacts directory (default: `./SmartContract/artifacts`)

## Error Handling

The module includes comprehensive error handling:
- Compilation errors
- Execution errors
- State errors
- Gas estimation errors

## Logging

Smart contract operations are logged to:
- Application logs
- Compilation logs
- Execution logs

## Security

- Contract validation
- Gas limit enforcement
- State isolation
- Input sanitization

## Performance

- Efficient EVM execution
- Optimized state management
- Fast compilation
- Gas optimization

## Testing

Test files:
- `SmartContract_test.go`: Smart contract tests
- Compilation tests
- Execution tests

## Example Contract

See `SmartContract/example/SimpleToken.sol` for an example Solidity contract.

## Future Enhancements

- Enhanced compiler support
- Improved EVM execution
- Better gas estimation
- Performance optimizations
- Additional contract languages

