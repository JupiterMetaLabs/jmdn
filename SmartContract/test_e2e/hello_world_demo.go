package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"time"

	"gossipnode/SmartContract/proto"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	serverAddr       = "localhost:15055"
	helloWorldSource = `
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

/**
 * @title HelloWorld
 * @dev Simple contract for testing compilation and execution
 */
contract HelloWorld {
    string private message;
    address public owner;
    uint256 public updateCount;

    event MessageUpdated(string newMessage, address updatedBy);

    constructor() {
        message = "Hello, World!";
        owner = msg.sender;
        updateCount = 0;
    }

    function getMessage() public view returns (string memory) {
        return message;
    }

    function setMessage(string memory _newMessage) public {
        message = _newMessage;
        updateCount++;
        emit MessageUpdated(_newMessage, msg.sender);
    }

    function getOwner() public view returns (address) {
        return owner;
    }

    function getUpdateCount() public view returns (uint256) {
        return updateCount;
    }
}
`
)

// printSeparator prints a formatted section separator
func printSeparator(title string) {
	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	fmt.Printf("  %s\n", title)
	fmt.Printf("%s\n\n", strings.Repeat("=", 80))
}

// printSubSection prints a subsection header
func printSubSection(title string) {
	fmt.Printf("\n--- %s ---\n\n", title)
}

// printField prints a labeled value
func printField(label, value string) {
	fmt.Printf("%-25s: %s\n", label, value)
}

func main() {
	ctx := context.Background()

	// Connect to the gRPC server
	printSeparator("CONNECTING TO SMART CONTRACT SERVER")
	fmt.Printf("Server Address: %s\n", serverAddr)

	conn, err := grpc.Dial(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := proto.NewSmartContractServiceClient(conn)
	fmt.Println("✅ Connected successfully!")

	// Step 1: Compile the HelloWorld contract
	printSeparator("STEP 1: COMPILING HELLOWORLD CONTRACT")

	compileResp, err := client.CompileContract(ctx, &proto.CompileRequest{
		SourceCode:   helloWorldSource,
		Optimize:     true,
		OptimizeRuns: 200,
	})
	if err != nil {
		log.Fatalf("Compilation failed: %v", err)
	}

	if compileResp.Error != "" {
		log.Fatalf("Compilation error: %s", compileResp.Error)
	}

	contract := compileResp.Contract
	printField("Contract Name", contract.Name)
	printField("Bytecode Length", fmt.Sprintf("%d bytes", len(contract.Bytecode)/2))
	printField("Compiler Version", compileResp.CompilerVersion)
	fmt.Printf("\n✅ Compilation successful!\n")

	// Parse ABI for later use
	parsedABI, err := abi.JSON(strings.NewReader(contract.Abi))
	if err != nil {
		log.Fatalf("Failed to parse ABI: %v", err)
	}

	// Step 2: Estimate gas for deployment
	printSeparator("STEP 2: ESTIMATING GAS FOR DEPLOYMENT")

	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")

	estimateResp, err := client.EstimateGas(ctx, &proto.EstimateGasRequest{
		Caller:          caller.Bytes(),
		ContractAddress: nil, // nil means deployment
		Input:           []byte(contract.Bytecode),
		Value:           big.NewInt(0).Bytes(),
	})
	if err != nil {
		log.Fatalf("Gas estimation failed: %v", err)
	}

	printField("Estimated Gas", fmt.Sprintf("%d", estimateResp.GasEstimate))
	printField("Gas with Buffer", fmt.Sprintf("%d (+20%%)", estimateResp.GasEstimate))

	// Assuming gas price of 1 gwei (for demonstration)
	gasPrice := big.NewInt(1_000_000_000) // 1 gwei
	estimatedCost := new(big.Int).Mul(big.NewInt(int64(estimateResp.GasEstimate)), gasPrice)
	printField("Estimated Cost (1 gwei)", fmt.Sprintf("%s wei (%s ETH)", estimatedCost.String(), weiToEth(estimatedCost)))

	// Step 3: Deploy the contract
	printSeparator("STEP 3: DEPLOYING CONTRACT")

	deployResp, err := client.DeployContract(ctx, &proto.DeployContractRequest{
		Caller:   caller.Bytes(),
		Bytecode: contract.Bytecode,
		Value:    big.NewInt(0).Bytes(),
		GasLimit: estimateResp.GasEstimate,
	})
	if err != nil {
		log.Fatalf("Deployment failed: %v", err)
	}

	result := deployResp.Result
	if !result.Success {
		log.Fatalf("Deployment error: %s", result.Error)
	}

	contractAddress := common.BytesToAddress(result.ContractAddress)
	printField("Contract Address", contractAddress.Hex())
	printField("Gas Used", fmt.Sprintf("%d", result.GasUsed))

	actualCost := new(big.Int).Mul(big.NewInt(int64(result.GasUsed)), gasPrice)
	printField("Actual Cost (1 gwei)", fmt.Sprintf("%s wei (%s ETH)", actualCost.String(), weiToEth(actualCost)))
	printField("Deployment Success", "✅ YES")

	// Step 4: Inspect database storage
	printSeparator("STEP 4: INSPECTING DATABASE STORAGE")

	// Get contract code
	codeResp, err := client.GetContractCode(ctx, &proto.GetContractCodeRequest{
		ContractAddress: contractAddress.Bytes(),
	})
	if err != nil {
		log.Fatalf("Failed to get contract code: %v", err)
	}

	printSubSection("Contract Code")
	printField("Runtime Code Length", fmt.Sprintf("%d bytes", len(codeResp.Code)))
	printField("Code Hash", fmt.Sprintf("0x%x...", codeResp.Code[:min(32, len(codeResp.Code))]))

	printSubSection("Storage Layout (Solidity)")
	fmt.Println("Slot 0: message (string - length + data)")
	fmt.Println("Slot 1: owner (address)")
	fmt.Println("Slot 2: updateCount (uint256)")

	// Get storage slot 1 (owner)
	slot1 := common.BigToHash(big.NewInt(1))
	storageResp, err := client.GetStorage(ctx, &proto.GetStorageRequest{
		ContractAddress: contractAddress.Bytes(),
		StorageKey:      slot1.Bytes(),
	})
	if err != nil {
		log.Fatalf("Failed to get storage: %v", err)
	}

	ownerFromStorage := common.BytesToAddress(storageResp.Value)
	printSubSection("Storage Slot 1 (owner)")
	printField("Value", ownerFromStorage.Hex())
	printField("Expected", caller.Hex())
	if ownerFromStorage == caller {
		fmt.Println("✅ Owner correctly stored!")
	}

	// Get storage slot 2 (updateCount)
	slot2 := common.BigToHash(big.NewInt(2))
	storageResp2, err := client.GetStorage(ctx, &proto.GetStorageRequest{
		ContractAddress: contractAddress.Bytes(),
		StorageKey:      slot2.Bytes(),
	})
	if err != nil {
		log.Fatalf("Failed to get storage: %v", err)
	}

	updateCount := new(big.Int).SetBytes(storageResp2.Value)
	printSubSection("Storage Slot 2 (updateCount)")
	printField("Value", updateCount.String())
	printField("Expected", "0")

	// Step 5: Call getMessage (read-only)
	printSeparator("STEP 5: CALLING getMessage() - READ-ONLY")

	getMessageData, err := parsedABI.Pack("getMessage")
	if err != nil {
		log.Fatalf("Failed to pack getMessage: %v", err)
	}

	callResp, err := client.CallContract(ctx, &proto.CallContractRequest{
		Caller:          caller.Bytes(),
		ContractAddress: contractAddress.Bytes(),
		Input:           getMessageData,
	})
	if err != nil {
		log.Fatalf("Call failed: %v", err)
	}

	// Decode the return data
	var message string
	if err := parsedABI.UnpackIntoInterface(&message, "getMessage", callResp.ReturnData); err != nil {
		log.Fatalf("Failed to decode message: %v", err)
	}

	printField("Current Message", fmt.Sprintf(`"%s"`, message))
	printField("Gas Cost", "0 (read-only call)")

	// Step 6: Execute setMessage (state-changing)
	printSeparator("STEP 6: EXECUTING setMessage() - STATE CHANGE")

	newMessage := "Hello from Jupiter Meta!"
	setMessageData, err := parsedABI.Pack("setMessage", newMessage)
	if err != nil {
		log.Fatalf("Failed to pack setMessage: %v", err)
	}

	// Estimate gas for execution
	execEstimateResp, err := client.EstimateGas(ctx, &proto.EstimateGasRequest{
		Caller:          caller.Bytes(),
		ContractAddress: contractAddress.Bytes(),
		Input:           setMessageData,
		Value:           big.NewInt(0).Bytes(),
	})
	if err != nil {
		log.Fatalf("Gas estimation failed: %v", err)
	}

	printField("Estimated Gas", fmt.Sprintf("%d", execEstimateResp.GasEstimate))

	// Execute the transaction
	execResp, err := client.ExecuteContract(ctx, &proto.ExecuteContractRequest{
		Caller:          caller.Bytes(),
		ContractAddress: contractAddress.Bytes(),
		Input:           setMessageData,
		Value:           big.NewInt(0).Bytes(),
		GasLimit:        execEstimateResp.GasEstimate,
	})
	if err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	if !execResp.Result.Success {
		log.Fatalf("Execution error: %s", execResp.Result.Error)
	}

	printField("Gas Used", fmt.Sprintf("%d", execResp.Result.GasUsed))

	execCost := new(big.Int).Mul(big.NewInt(int64(execResp.Result.GasUsed)), gasPrice)
	printField("Execution Cost (1 gwei)", fmt.Sprintf("%s wei (%s ETH)", execCost.String(), weiToEth(execCost)))
	printField("Execution Success", "✅ YES")

	// Step 7: Verify the message was updated
	printSeparator("STEP 7: VERIFYING STATE CHANGE")

	// Call getMessage again
	callResp2, err := client.CallContract(ctx, &proto.CallContractRequest{
		Caller:          caller.Bytes(),
		ContractAddress: contractAddress.Bytes(),
		Input:           getMessageData,
	})
	if err != nil {
		log.Fatalf("Call failed: %v", err)
	}

	var updatedMessage string
	if err := parsedABI.UnpackIntoInterface(&updatedMessage, "getMessage", callResp2.ReturnData); err != nil {
		log.Fatalf("Failed to decode message: %v", err)
	}

	printField("Previous Message", fmt.Sprintf(`"%s"`, message))
	printField("New Message", fmt.Sprintf(`"%s"`, updatedMessage))
	printField("Match Expected", fmt.Sprintf("%v", updatedMessage == newMessage))

	// Get updated updateCount from storage
	storageResp3, err := client.GetStorage(ctx, &proto.GetStorageRequest{
		ContractAddress: contractAddress.Bytes(),
		StorageKey:      slot2.Bytes(),
	})
	if err != nil {
		log.Fatalf("Failed to get storage: %v", err)
	}

	updatedCount := new(big.Int).SetBytes(storageResp3.Value)
	printField("Update Count", updatedCount.String())
	printField("Expected", "1")

	if updatedMessage == newMessage && updatedCount.Cmp(big.NewInt(1)) == 0 {
		fmt.Println("\n✅ State change verified successfully!")
	}

	// Step 8: Summary
	printSeparator("SUMMARY - GAS FEES AND COSTS")

	totalGas := result.GasUsed + execResp.Result.GasUsed
	totalCost := new(big.Int).Add(actualCost, execCost)

	fmt.Printf("\n%-30s | %15s | %15s | %20s\n", "Operation", "Gas Used", "Cost (wei)", "Cost (ETH)")
	fmt.Println(strings.Repeat("-", 90))
	fmt.Printf("%-30s | %15d | %15s | %20s\n", "Contract Deployment", result.GasUsed, actualCost.String(), weiToEth(actualCost))
	fmt.Printf("%-30s | %15d | %15s | %20s\n", "setMessage() Execution", execResp.Result.GasUsed, execCost.String(), weiToEth(execCost))
	fmt.Println(strings.Repeat("-", 90))
	fmt.Printf("%-30s | %15d | %15s | %20s\n", "TOTAL", totalGas, totalCost.String(), weiToEth(totalCost))

	printSeparator("DATABASE STORAGE KEYS")

	fmt.Printf("\nThe contract data is stored in the database using these key prefixes:\n\n")
	fmt.Printf("  balance:%s     -> Account balance (wei)\n", contractAddress.Hex())
	fmt.Printf("  nonce:%s       -> Account nonce (transaction count)\n", contractAddress.Hex())
	fmt.Printf("  code:%s        -> Contract bytecode (%d bytes)\n", contractAddress.Hex(), len(codeResp.Code))
	fmt.Printf("  codehash:%s    -> Keccak256 hash of code\n", contractAddress.Hex())
	fmt.Printf("  storage:%s:%s -> Storage slot values\n", contractAddress.Hex(), "{slot_hash}")

	fmt.Printf("\nExample storage keys:\n")
	fmt.Printf("  storage:%s:%s -> owner address\n", contractAddress.Hex(), slot1.Hex())
	fmt.Printf("  storage:%s:%s -> updateCount\n", contractAddress.Hex(), slot2.Hex())

	printSeparator("DEMO COMPLETE")
	fmt.Println("✅ All operations completed successfully!")
	fmt.Println("✅ Contract deployed, executed, and verified")
	fmt.Println("✅ Gas fees calculated and displayed")
	fmt.Println("✅ Database storage inspected")

	// Save contract info to file
	saveContractInfo(contractAddress, contract.Abi, result.GasUsed, execResp.Result.GasUsed)
}

// weiToEth converts wei to ETH for display
func weiToEth(wei *big.Int) string {
	eth := new(big.Float).Quo(
		new(big.Float).SetInt(wei),
		new(big.Float).SetInt(big.NewInt(1_000_000_000_000_000_000)),
	)
	return eth.Text('f', 18) + " ETH"
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// saveContractInfo saves contract information to a JSON file
func saveContractInfo(addr common.Address, abiJSON string, deployGas, execGas uint64) {
	info := map[string]interface{}{
		"contract_address": addr.Hex(),
		"abi":              json.RawMessage(abiJSON),
		"deployed_at":      time.Now().UTC().Format(time.RFC3339),
		"gas": map[string]uint64{
			"deployment": deployGas,
			"execution":  execGas,
			"total":      deployGas + execGas,
		},
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal contract info: %v", err)
		return
	}

	filename := fmt.Sprintf("contract_info_%s.json", time.Now().Format("20060102_150405"))
	if err := os.WriteFile(filename, data, 0644); err != nil {
		log.Printf("Failed to save contract info: %v", err)
		return
	}

	fmt.Printf("\n📄 Contract info saved to: %s\n", filename)
}
