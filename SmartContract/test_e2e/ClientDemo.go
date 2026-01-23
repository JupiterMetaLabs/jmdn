package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"gossipnode/SmartContract/pkg/client"

	"github.com/ethereum/go-ethereum/common"
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

func main() {
	fmt.Println("🚀 Starting Client Demo...")
	ctx := context.Background()

	// 1. Connect
	fmt.Printf("   Connecting to %s...\n", serverAddr)
	c, err := client.NewClient(serverAddr)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer c.Close()

	// Wait for connection
	for i := 0; i < 10; i++ {
		err = c.CheckConnectivity(ctx)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		log.Fatalf("Server not reachable: %v", err)
	}

	// 2. Compile
	fmt.Println("   Compiling contract...")
	compileResp, err := c.CompileContract(ctx, sourceCode)
	if err != nil {
		log.Fatalf("Compilation failed: %v", err)
	}
	if len(compileResp.Contract.Bytecode) == 0 {
		log.Fatal("Bytecode should not be empty")
	}
	fmt.Printf("   ✅ Compiled: %s (len: %d) ABI len: %d\n", compileResp.Contract.Name, len(compileResp.Contract.Bytecode), len(compileResp.Contract.Abi))

	// 3. Deploy
	fmt.Println("   Deploying contract...")
	randomBytes := make([]byte, 20)
	copy(randomBytes, []byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	caller := common.BytesToAddress(randomBytes)

	deployOpts := &client.DeployOptions{
		GasLimit: 3000000,
		ABI:      compileResp.Contract.Abi,
	}

	deployResp, err := c.DeployContract(
		ctx,
		caller.Bytes(),
		[]byte(compileResp.Contract.Bytecode),
		deployOpts,
	)
	if err != nil {
		log.Fatalf("Deployment RPC failed: %v", err)
	}
	if !deployResp.Result.Success {
		log.Fatalf("Deployment execution failed: %s", deployResp.Result.Error)
	}

	contractAddr := deployResp.Result.ContractAddress
	if len(contractAddr) == 0 {
		log.Fatal("Contract address should be returned")
	}
	fmt.Printf("   ✅ Deployed at: 0x%x\n", contractAddr)

	// 4. Get Code
	fmt.Println("   Verifying code...")
	codeResp, err := c.GetContractCode(ctx, contractAddr)
	if err != nil {
		log.Fatalf("GetContractCode failed: %v", err)
	}
	if len(codeResp.Code) == 0 {
		log.Fatal("Contract code should exist on chain")
	}
	fmt.Printf("   ✅ Code found (len: %d)\n", len(codeResp.Code))

	// 5. Call (Read) - GetStorage
	fmt.Println("   Verifying storage...")
	storageResp, err := c.GetStorage(ctx, contractAddr, common.Hash{}.Bytes()) // Slot 0
	if err != nil {
		log.Fatalf("GetStorage failed: %v", err)
	}

	val := new(big.Int).SetBytes(storageResp.Value)
	if val.Int64() != 100 {
		log.Fatalf("Initial storage value mismatch: got %d, want 100", val.Int64())
	}
	fmt.Printf("   ✅ Initial storedData: %d\n", val.Int64())

	// 6. List Contracts
	fmt.Println("   Listing contracts...")
	listResp, err := c.ListContracts(ctx, 10)
	if err != nil {
		log.Fatalf("ListContracts failed: %v", err)
	}
	fmt.Printf("   ✅ Found %d contracts in registry\n", len(listResp.Contracts))

	// Check ABI
	if len(listResp.Contracts) > 0 {
		c0 := listResp.Contracts[len(listResp.Contracts)-1] // Last deployed
		if c0.Abi == "" {
			log.Fatal("❌ ABI is empty in registry!")
		}
		fmt.Printf("   ✅ ABI present for latest contract (len: %d)\n", len(c0.Abi))
	} else {
		log.Fatal("❌ No contracts found to verify ABI")
	}

	fmt.Println("\n✅ CLIENT DEMO PASSED")
}
