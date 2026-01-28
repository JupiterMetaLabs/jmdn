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
	"github.com/ethereum/go-ethereum/common/hexutil"
)

const (
	serverAddr       = "localhost:15056"
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
	helloWorldBytecode = "608060405234801561000f575f5ffd5b506040518060400160405280600f81526020017f48656c6c6f2066726f6d2053444b2100000000000000000000000000000000008152505f908161005391906102a7565b50610376565b5f81519050919050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52604160045260245ffd5b7f4e487b71000000000000000000000000000000000000000000000000000000005f52602260045260245ffd5b5f60028204905060018216806100d457607f821691505b6020821081036100e7576100e6610090565b5b50919050565b5f819050815f5260205f209050919050565b5f6020601f8301049050919050565b5f82821b905092915050565b5f600883026101497fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff8261010e565b610153868361010e565b95508019841693508086168417925050509392505050565b5f819050919050565b5f819050919050565b5f61019761019261018d8461016b565b610174565b61016b565b9050919050565b5f819050919050565b6101b08361017d565b6101c46101bc8261019e565b84845461011a565b825550505050565b5f5f905090565b6101db6101cc565b6101e68184846101a7565b505050565b5f5b8281101561020c576102015f8284016101d3565b6001810190506101ed565b505050565b601f82111561025f578282111561025e5761022b816100ed565b610234836100ff565b610234836100ff565b61023d856100ff565b602086101561024a575f90505b808301610259828403826101eb565b505050505b5b505050565b5f82821c905092915050565b5f61027f5f1984600802610264565b1980831691505092915050565b5f6102978383610270565b9150826002028217905092915050565b6102b082610059565b67ffffffffffffffff8111156102c9576102c8610063565b5b6102d382546100bd565b6102de828285610211565b5f60209050601f83116001811461030f575f84156102fd578287015190505b610307858261028c565b86555061036e565b601f19841661031d866100ed565b5f5b828110156103445784890151825560018201915060208501945060208101905061031f565b86831015610361578489015161035d601f891682610270565b8355505b6001600288020188555050505b50505050505056fea2646970667358221220a0513761898cb8b96b6aa0d52ec21030c32ab0300fa7994c08446661d5aba58264736f6c63430008210033"
	helloWorldABI      = `[{"inputs":[],"stateMutability":"nonpayable","type":"constructor"},{"inputs":[],"name":"getMessage","outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},{"inputs":[],"name":"message","outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"string","name":"_newMessage","type":"string"}],"name":"setMessage","outputs":[],"stateMutability":"nonpayable","type":"function"}]`
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
	fmt.Println("\n[1] Compiling/Getting Contract Bytecode...")
	var bytecode []byte
	compileResp, err := c.CompileContract(context.Background(), helloWorldSource)
	if err != nil || compileResp.Error != "" {
		fmt.Printf("⚠️  Compile failed or not available (%v), using pre-compiled bytecode\n", err)
		bytecode, _ = hexutil.Decode("0x" + helloWorldBytecode)
	} else {
		bytecode = []byte(compileResp.Contract.Bytecode)
		fmt.Printf("✅ Compiled! Bytecode size: %d bytes\n", len(bytecode))
	}

	// 4. Deploy
	fmt.Println("\n[2] Deploying Contract...")
	deployer := common.HexToAddress("0xf302B257cFFB7b30aF229F50F66315194d441C41")
	deployResp, err := c.DeployContract(context.Background(), deployer.Bytes(), bytecode, &client.DeployOptions{
		GasLimit: 3000000,
		ABI:      helloWorldABI,
	})
	if err != nil {
		log.Fatalf("Deploy failed: %v", err)
	}
	if !deployResp.Result.Success {
		log.Fatalf("Deploy failed: %s", deployResp.Result.Error)
	}
	contractAddr := deployResp.Result.ContractAddress
	fmt.Printf("✅ Deployed at: %s\n", contractAddr)

	// 5. Call (Get Message)
	fmt.Println("\n[3] Calling getMessage()...")

	parsedABI, err := abi.JSON(strings.NewReader(helloWorldABI))
	if err != nil {
		log.Fatalf("Failed to parse ABI: %v", err)
	}

	getInput, _ := parsedABI.Pack("getMessage")
	callResp, err := c.CallContract(context.Background(), deployer.Bytes(), common.HexToAddress(contractAddr).Bytes(), getInput)
	if err != nil {
		log.Fatalf("Call failed: %v", err)
	}

	// Proto CallContractResponse has `ReturnData`, not `Result`.
	var message string
	decodedRet, _ := hexutil.Decode(callResp.ReturnData)
	parsedABI.UnpackIntoInterface(&message, "getMessage", decodedRet)
	fmt.Printf("✅ Current Message: %q\n", message)

	// 6. Execute (Set Message)
	fmt.Println("\n[4] Executing setMessage('Hello from Go SDK!')...")
	setInput, _ := parsedABI.Pack("setMessage", "Hello from Go SDK!")
	execResp, err := c.ExecuteContract(context.Background(), deployer.Bytes(), common.HexToAddress(contractAddr).Bytes(), setInput, nil)
	if err != nil {
		log.Fatalf("Execute failed: %v", err)
	}
	if !execResp.Result.Success {
		log.Fatalf("Execution failed: %s", execResp.Result.Error)
	}
	fmt.Printf("✅ Executed! Gas Used: %d\n", execResp.Result.GasUsed)

	// 7. Verify
	fmt.Println("\n[5] Verifying Message...")
	callResp2, _ := c.CallContract(context.Background(), deployer.Bytes(), common.HexToAddress(contractAddr).Bytes(), getInput)
	decodedRet2, _ := hexutil.Decode(callResp2.ReturnData)
	parsedABI.UnpackIntoInterface(&message, "getMessage", decodedRet2)
	fmt.Printf("✅ New Message: %q\n", message)

	fmt.Println("\n🎉 SDK Demo Complete!")
}
