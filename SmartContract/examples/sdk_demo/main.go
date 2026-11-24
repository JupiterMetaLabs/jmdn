package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"gossipnode/SmartContract/pkg/client"
	"gossipnode/SmartContract/proto"

	"github.com/ethereum/go-ethereum/common"
)

const (
	serverAddr       = "localhost:15056"
	helloWorldSource = `
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract Empty {}
`
	// ABI for Empty contract
	helloWorldABI = `[{"inputs":[],"stateMutability":"nonpayable","type":"constructor"}]`
)

func main() {
	fmt.Println("🚀 Starting SDK Demo - Testing Empty Contract...")

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
		fmt.Printf(" (Not ready yet: %v) ", err)
	}
	fmt.Println(" Connected!")

	// 3. Compile
	fmt.Println("\n[1] Compiling Empty Contract...")
	var bytecode []byte
	compileResp, err := c.CompileContract(context.Background(), &proto.CompileRequest{
		SourceCode: helloWorldSource,
	})
	if err != nil {
		log.Fatalf("❌ Compilation failed (gRPC error): %v", err)
	}
	if compileResp == nil || compileResp.Error != "" {
		errMsg := "unknown error"
		if compileResp != nil {
			errMsg = compileResp.Error
		}
		log.Fatalf("❌ Compilation failed (Server Error: %s)", errMsg)
	}
	bytecode = common.FromHex(compileResp.Contract.Bytecode)
	fmt.Printf("✅ Compiled! Bytecode size: %d bytes\n", len(bytecode))

	// 4. Deploy
	fmt.Println("\n[2] Deploying Empty Contract...")
	deployer := common.HexToAddress("0xf302B257cFFB7b30aF229F50F66315194d441C41")
	deployResp, err := c.DeployContract(context.Background(), deployer.Bytes(), bytecode, &client.DeployOptions{
		GasLimit: 1000000,
		ABI:      helloWorldABI,
		Value:    big.NewInt(0).Bytes(),
	})
	if err != nil {
		log.Fatalf("Deploy failed: %v", err)
	}
	if !deployResp.Result.Success {
		log.Fatalf("Deploy failed: %s", deployResp.Result.Error)
	}
	contractAddr := deployResp.Result.ContractAddress
	fmt.Printf("✅ Deployed at: %s\n", contractAddr)
	fmt.Printf("   Gas Used: %d\n", deployResp.Result.GasUsed)

	fmt.Println("\n✅ Empty contract deployed successfully!")
	fmt.Println("(No functions to test - contract is empty)")
	fmt.Println("\n🎉 SDK Demo Complete!")
}
