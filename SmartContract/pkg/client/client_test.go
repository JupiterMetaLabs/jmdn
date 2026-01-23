package client_test

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"gossipnode/SmartContract/pkg/client"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

const (
	serverAddr = "localhost:15055"
	sourceCode = `
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract SimpleStorage {
    uint256 public storedData;
    address public owner;

    constructor() {
        owner = msg.sender;
        storedData = 100;
    }

    function set(uint256 x) public {
        storedData = x;
    }

    function get() public view returns (uint256) {
        return storedData;
    }
}
`
)

func TestClientIntegration(t *testing.T) {
	// Skip if server is not running (e.g., in CI without setup)
	// For this session, we assume server is running on 15055 as per user context
	ctx := context.Background()

	// 1. Connect
	c, err := client.NewClient(serverAddr)
	require.NoError(t, err, "Failed to create client")
	defer c.Close()

	// Wait for connection to be ready (retry for 5s)
	for i := 0; i < 10; i++ {
		err = c.CheckConnectivity(ctx)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if err != nil {
		t.Skipf("Skipping integration test: server not reachable at %s: %v", serverAddr, err)
	}

	// 2. Compile
	compileResp, err := c.CompileContract(ctx, sourceCode)
	require.NoError(t, err, "Compilation failed")
	require.NotEmpty(t, compileResp.Contract.Bytecode, "Bytecode should not be empty")
	t.Logf("Compiled contract: %s (len: %d)", compileResp.Contract.Name, len(compileResp.Contract.Bytecode))

	// 3. Deploy
	// Use a random caller to ensure unique contract address (since nonce isn't managed in test)
	// "0xTestUser" is invalid hex, let's use a real random address
	randomBytes := make([]byte, 20)
	// simple pseudo-random for test
	copy(randomBytes, []byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	caller := common.BytesToAddress(randomBytes)

	deployOpts := &client.DeployOptions{
		GasLimit: 3000000,
	}

	deployResp, err := c.DeployContract(
		ctx,
		caller.Bytes(),
		[]byte(compileResp.Contract.Bytecode), // Client expects string bytes likely, verifying this
		deployOpts,
	)
	require.NoError(t, err, "Deployment RPC failed")
	require.True(t, deployResp.Result.Success, "Deployment execution failed: %s", deployResp.Result.Error)

	contractAddr := deployResp.Result.ContractAddress
	require.NotEmpty(t, contractAddr, "Contract address should be returned")
	t.Logf("Deployed contract at: 0x%x", contractAddr)

	// 4. Get Code
	codeResp, err := c.GetContractCode(ctx, contractAddr)
	require.NoError(t, err, "GetContractCode failed")
	require.NotEmpty(t, codeResp.Code, "Contract code should exist on chain")

	// 5. Call (Read) - get()
	// Need to encode ABI manually or use basic checking?
	// Since we don't have easy ABI packing in test without import cycle or copying,
	// we will assume 0x6d4ce63c is `get()` signature (keccak256("get()")[0:4])
	// storage slot 0 should be storedData = 100

	// Let's use GetStorage for simpler verification without ABI packing if possible
	// storedData is slot 0
	storageResp, err := c.GetStorage(ctx, contractAddr, common.Hash{}.Bytes()) // Slot 0
	require.NoError(t, err, "GetStorage failed")

	val := new(big.Int).SetBytes(storageResp.Value)
	require.Equal(t, int64(100), val.Int64(), "Initial storage value mismatch")
	t.Logf("Initial storedData: %d", val.Int64())

	// 6. Execute (Write) - Not easily testable without ABI packing for `set(uint256)`
	// But we can test Execute with empty input (fallback) or just verify method exists

	// Test ListContracts
	listResp, err := c.ListContracts(ctx, 10)
	require.NoError(t, err, "ListContracts failed")
	t.Logf("Found %d contracts", len(listResp.Contracts))
}
