package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gossipnode/SmartContract/pkg/client"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const (
	serverAddr       = "localhost:15055"
	helloWorldSource = `
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract HelloWorld {
    string public message;
    
    constructor() {
        message = "Hello from SDK!";
    }

    function setMessage(string memory _newMessage) public {
        message = _newMessage;
    }

    function getMessage() public view returns (string memory) {
        return message;
    }
}
`
)

func main() {
	fmt.Println("🚀 Starting SDK Demo...")

	// 1. Create Client
	c, err := client.NewClient(serverAddr)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer c.Close()

	// 2. Check Connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fmt.Print("Connecting...")
	time.Sleep(1 * time.Second)
	if err := c.CheckConnectivity(ctx); err != nil {
		// Just log, don't fatal, maybe it takes a bit
		fmt.Printf(" (Not ready yet: %v) ", err)
	}
	fmt.Println(" Connected!")

	// 3. Compile
	fmt.Println("\n[1] Compiling Contract...")
	compileResp, err := c.CompileContract(context.Background(), helloWorldSource)
	if err != nil {
		log.Fatalf("Compile failed: %v", err)
	}
	if compileResp.Error != "" {
		log.Fatalf("Compile error: %s", compileResp.Error)
	}
	fmt.Printf("✅ Compiled! Bytecode size: %d bytes\n", len(compileResp.Contract.Bytecode))

	// 4. Deploy
	fmt.Println("\n[2] Deploying Contract...")
	deployer := common.HexToAddress("0x9999999999999999999999999999999999999999")
	deployResp, err := c.DeployContract(context.Background(), deployer.Bytes(), []byte(compileResp.Contract.Bytecode), nil)
	if err != nil {
		log.Fatalf("Deploy failed: %v", err)
	}
	if !deployResp.Result.Success {
		log.Fatalf("Deploy failed: %s", deployResp.Result.Error)
	}
	contractAddr := deployResp.Result.ContractAddress
	fmt.Printf("✅ Deployed at: 0x%x\n", contractAddr)

	// 5. Call (Get Message)
	fmt.Println("\n[3] Calling getMessage()...")

	contractABI := `[{"inputs":[],"stateMutability":"nonpayable","type":"constructor"},{"inputs":[],"name":"getMessage","outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"string","name":"_newMessage","type":"string"}],"name":"setMessage","outputs":[],"stateMutability":"nonpayable","type":"function"}]`
	parsedABI, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		log.Fatalf("Failed to parse ABI: %v", err)
	}

	getInput, _ := parsedABI.Pack("getMessage")
	callResp, err := c.CallContract(context.Background(), deployer.Bytes(), contractAddr, getInput)
	if err != nil {
		log.Fatalf("Call failed: %v", err)
	}

	// Proto CallContractResponse has `ReturnData`, not `Result`.
	var message string
	parsedABI.UnpackIntoInterface(&message, "getMessage", callResp.ReturnData)
	fmt.Printf("✅ Current Message: %q\n", message)

	// 6. Execute (Set Message)
	fmt.Println("\n[4] Executing setMessage('Hello from Go SDK!')...")
	setInput, _ := parsedABI.Pack("setMessage", "Hello from Go SDK!")
	execResp, err := c.ExecuteContract(context.Background(), deployer.Bytes(), contractAddr, setInput, nil)
	if err != nil {
		log.Fatalf("Execute failed: %v", err)
	}
	if !execResp.Result.Success {
		log.Fatalf("Execution failed: %s", execResp.Result.Error)
	}
	fmt.Printf("✅ Executed! Gas Used: %d\n", execResp.Result.GasUsed)

	// 7. Verify
	fmt.Println("\n[5] Verifying Message...")
	callResp2, _ := c.CallContract(context.Background(), deployer.Bytes(), contractAddr, getInput)
	parsedABI.UnpackIntoInterface(&message, "getMessage", callResp2.ReturnData)
	fmt.Printf("✅ New Message: %q\n", message)

	fmt.Println("\n🎉 SDK Demo Complete!")
}
