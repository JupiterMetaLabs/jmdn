# SmartContract Module

## Overview

The SmartContract module provides smart contract compilation and execution capabilities for the JMZK network. It acts as an execution layer that interfaces with:

1.  **gETH Service** (Port 9090): For Chain Data (Code, Blocks).
2.  **DID Service** (Port 15052): For Identity Data (Balances, Nonces).

It supports Solidity compilation, EVM execution, and state management using updated **Hex String APIs**.

## Key Features

- **Dual-Client Architecture**: Bridges Chain data and Identity data.
- **Hex String API**: All byte fields (Addresses, Data, Hashes) are JSON-friendly Hex strings (`0x...`).
- **Remote Compilation**: Compile Solidity source code directly via gRPC.
- **State Management**: Supports Pebble and SQLite backends.

## gRPC Endpoints

All endpoints receive and return Hex-encoded strings (`0x...`).

### Core Operations

- `rpc CompileContract(CompileRequest) returns (CompileResponse)`
- `rpc DeployContract(DeployContractRequest) returns (DeployContractResponse)`
- `rpc ExecuteContract(ExecuteContractRequest) returns (ExecuteContractResponse)`
- `rpc CallContract(CallContractRequest) returns (CallContractResponse)`

### Information

- `rpc GetContractCode(GetContractCodeRequest) returns (GetContractCodeResponse)`
- `rpc GetStorage(GetStorageRequest) returns (GetStorageResponse)`
- `rpc ListContracts(ListContractsRequest) returns (ListContractsResponse)`

### Utilities

- `rpc EstimateGas(EstimateGasRequest) returns (EstimateGasResponse)`
- `rpc EncodeFunctionCall(EncodeFunctionCallRequest) returns (EncodeFunctionCallResponse)`
- `rpc DecodeFunctionOutput(DecodeFunctionOutputRequest) returns (DecodeFunctionOutputResponse)`

## Setup and Running (No Consensus / Local Execution)

To run the SmartContract service locally without a full consensus node, follow these steps:

### 1. Prerequisites

- **gETH Node**: Running on `localhost:9090`.
- **DID Service**: Running on `localhost:15052` (Validation of Balances).
- **Go**: Version 1.22+.

### 2. Configuration

The service connects to `localhost:9090` and `localhost:15052` by default.
Storage configuration is controlled via environment variables.

### 3. Run Service

```bash
# Run with Pebble DB (recommended)
DB_TYPE=pebble go run cmd/main.go

# Or with SQLite
DB_TYPE=sqlite go run cmd/main.go
```

The server will start on port **15055**.

## End-to-End Usage (Helper Scripts)

We provide helper scripts to automate the workflow.

### Compile and Deploy

Use `compile_and_deploy.sh` to compile a Solidity contract and deploy it immediately.

```bash
# Ensure hello_world.sol exists
./compile_and_deploy.sh
```

This script demonstrates:

1.  **compilation**: Sends source to `CompileContract`.
2.  **deployment**: Sends bytecode to `DeployContract`.
3.  **interaction**: Calls `CallContract` (Read) and `ExecuteContract` (Write).

### Manual Deployment

Use `deploy_contract.sh` to deploy pre-compiled bytecode.

```bash
./deploy_contract.sh
```

## API Usage Example (Go)

```go
import "gossipnode/SmartContract/proto"

client := proto.NewSmartContractServiceClient(conn)

// 1. Compile
compileResp, _ := client.CompileContract(ctx, &proto.CompileRequest{
    SourceCode: "contract A { ... }",
})

// 2. Deploy (using Hex Strings)
deployResp, _ := client.DeployContract(ctx, &proto.DeployContractRequest{
    Caller:   "0xf302...", // Hex String
    Bytecode: compileResp.Contract.Bytecode, // "0x6080..."
    Value:    "0x0",       // Hex String (wei)
})

fmt.Printf("Contract Address: %s\n", deployResp.Result.ContractAddress)
```

## Architecture Notes

- **Balance Check**: When a transaction is received, `SmartContract` checks the Caller's balance by querying the **DID Service**.
- **Nonce Check**: Nonces are also validated against the **DID Service**.
- **Code Retrieval**: If a contract interacts with another contract, code is fetched from **gETH**.
