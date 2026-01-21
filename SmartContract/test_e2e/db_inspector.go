package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"

	"gossipnode/SmartContract/proto"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const serverAddr = "localhost:15055"

func main() {
	ctx := context.Background()

	// Connect to gRPC server
	conn, err := grpc.Dial(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := proto.NewSmartContractServiceClient(conn)

	printSeparator("DATABASE STORAGE INSPECTION TOOL")

	// The contract address from the previous deployment
	contractAddrHex := "0x01c75B79BEf232123BeD7925D4712874Ce1c05e2"
	contractAddr := common.HexToAddress(contractAddrHex)

	fmt.Printf("Inspecting contract: %s\n\n", contractAddrHex)

	// ===================================================================
	// 1. WHICH DATABASE IS BEING USED?
	// ===================================================================
	printSubSection("1. DATABASE TYPE")

	fmt.Println("The SmartContract server supports TWO storage modes:")
	fmt.Println("")
	fmt.Println("  🔹 In-Memory StateDB (CURRENT MODE)")
	fmt.Println("     - Volatile storage, data lost on restart")
	fmt.Println("     - Fast, no external dependencies")
	fmt.Println("     - Used for: testing, development")
	fmt.Println("     - Implementation: statedb.NewInMemoryStateDB()")
	fmt.Println("")
	fmt.Println("  🔹 ImmuDB StateDB (PERSISTENT MODE)")
	fmt.Println("     - Persistent storage, data survives restarts")
	fmt.Println("     - Tamper-proof, cryptographically verifiable")
	fmt.Println("     - Used for: production, mainnet")
	fmt.Println("     - Implementation: statedb.NewImmuStateDB(dbClient)")
	fmt.Println("")
	fmt.Printf("Current server mode: IN-MEMORY (check SmartContract/cmd/main.go line 22)\n")

	// ===================================================================
	// 2. HOW IS DATA STORED?
	// ===================================================================
	printSubSection("2. DATABASE STORAGE STRUCTURE")

	fmt.Println("Data is stored using KEY-VALUE pairs with prefixes:")
	fmt.Println("")

	fmt.Printf("  Key Pattern                    | Value Type           | Example\n")
	fmt.Printf("  -------------------------------|----------------------|----------------------------------\n")
	fmt.Printf("  balance:<address>              | big.Int bytes        | balance:%s\n", contractAddr.Hex())
	fmt.Printf("  nonce:<address>                | uint64 JSON          | nonce:%s\n", contractAddr.Hex())
	fmt.Printf("  code:<address>                 | bytecode []byte      | code:%s\n", contractAddr.Hex())
	fmt.Printf("  codehash:<address>             | keccak256 hash       | codehash:%s\n", contractAddr.Hex())
	fmt.Printf("  storage:<address>:<slot_hash>  | 32-byte value        | storage:%s:0x0001\n", contractAddr.Hex())
	fmt.Printf("  stateroot:latest               | merkle root hash     | stateroot:latest\n")
	fmt.Println("")

	// ===================================================================
	// 3. INSPECT CONTRACT CODE
	// ===================================================================
	printSubSection("3. INSPECTING CONTRACT CODE")

	codeResp, err := client.GetContractCode(ctx, &proto.GetContractCodeRequest{
		ContractAddress: contractAddr.Bytes(),
	})
	if err != nil {
		log.Printf("⚠️  Failed to get contract code: %v", err)
		log.Printf("⚠️  This likely means the contract doesn't exist or server restarted\n")
		return
	}

	fmt.Printf("✅ Contract found!\n\n")
	fmt.Printf("  Code Length:  %d bytes\n", len(codeResp.Code))
	fmt.Printf("  Code Preview: 0x%s...\n", hex.EncodeToString(codeResp.Code[:min(32, len(codeResp.Code))]))
	fmt.Printf("  Storage Key:  code:%s\n", contractAddr.Hex())
	fmt.Println("")

	// ===================================================================
	// 4. INSPECT CONTRACT STORAGE
	// ===================================================================
	printSubSection("4. INSPECTING CONTRACT STORAGE")

	fmt.Println("Reading storage slots directly from database:")
	fmt.Println("")

	// Slot 0: message (string - stored as length + data)
	slot0 := common.BigToHash(big.NewInt(0))
	storageResp0, err := client.GetStorage(ctx, &proto.GetStorageRequest{
		ContractAddress: contractAddr.Bytes(),
		StorageKey:      slot0.Bytes(),
	})
	if err == nil {
		fmt.Printf("  Slot 0 (message length): 0x%s\n", hex.EncodeToString(storageResp0.Value))
		fmt.Printf("    Database Key: storage:%s:%s\n", contractAddr.Hex(), slot0.Hex())
	}

	// Slot 1: owner
	slot1 := common.BigToHash(big.NewInt(1))
	storageResp1, err := client.GetStorage(ctx, &proto.GetStorageRequest{
		ContractAddress: contractAddr.Bytes(),
		StorageKey:      slot1.Bytes(),
	})
	if err == nil {
		owner := common.BytesToAddress(storageResp1.Value)
		fmt.Printf("  Slot 1 (owner):          %s\n", owner.Hex())
		fmt.Printf("    Database Key: storage:%s:%s\n", contractAddr.Hex(), slot1.Hex())
	}

	// Slot 2: updateCount
	slot2 := common.BigToHash(big.NewInt(2))
	storageResp2, err := client.GetStorage(ctx, &proto.GetStorageRequest{
		ContractAddress: contractAddr.Bytes(),
		StorageKey:      slot2.Bytes(),
	})
	if err == nil {
		updateCount := new(big.Int).SetBytes(storageResp2.Value)
		fmt.Printf("  Slot 2 (updateCount):    %s\n", updateCount.String())
		fmt.Printf("    Database Key: storage:%s:%s\n", contractAddr.Hex(), slot2.Hex())
	}
	fmt.Println("")

	// ===================================================================
	// 5. HOW TO LIST CONTRACTS
	// ===================================================================
	printSubSection("5. HOW TO LIST DEPLOYED CONTRACTS")

	fmt.Println("Method 1: Using gRPC API (currently returns empty - needs registry)")

	listResp, err := client.ListContracts(ctx, &proto.ListContractsRequest{
		FromBlock: 0,
		ToBlock:   0,
		Limit:     100,
	})
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		fmt.Printf("  Contracts found: %d\n", len(listResp.Contracts))
		fmt.Printf("  Note: ListContracts is not fully implemented yet (see Router.go line 233)\n")
	}

	fmt.Println("")
	fmt.Println("Method 2: Keep a registry (recommended for production)")
	fmt.Println("  - Store contract deployments in a separate contracts table")
	fmt.Println("  - Track: address, deployer, deploy_block, timestamp, name, ABI")
	fmt.Println("  - Implement in Router.go by updating ListContracts()")
	fmt.Println("")

	// ===================================================================
	// 6. HOW TO CALL/TRIGGER A CONTRACT
	// ===================================================================
	printSubSection("6. HOW TO CALL/TRIGGER A CONTRACT")

	fmt.Println("There are TWO ways to interact with a contract:")
	fmt.Println("")

	fmt.Println("📖 READ-ONLY CALL (view/pure functions, no gas cost, no state change)")
	fmt.Println("   - Use: CallContract RPC")
	fmt.Println("   - Example: getMessage()")
	fmt.Println("   - Code snippet:")
	fmt.Println(`
   input := abi.Pack("getMessage")  // Encode function call
   result, err := client.CallContract(ctx, &proto.CallContractRequest{
       Caller:          callerAddress.Bytes(),
       ContractAddress: contractAddress.Bytes(),
       Input:           input,
   })
   // Decode result to get return value
`)

	fmt.Println("")
	fmt.Println("✍️  STATE-CHANGING CALL (modifies storage, costs gas, creates transaction)")
	fmt.Println("   - Use: ExecuteContract RPC")
	fmt.Println("   - Example: setMessage()")
	fmt.Println("   - Code snippet:")
	fmt.Println(`
   input := abi.Pack("setMessage", "New message")
   result, err := client.ExecuteContract(ctx, &proto.ExecuteContractRequest{
       Caller:          callerAddress.Bytes(),
       ContractAddress: contractAddress.Bytes(),
       Input:           input,
       Value:           big.NewInt(0).Bytes(),
       GasLimit:        100000,
   })
   // Check result.Success and result.GasUsed
`)

	fmt.Println("")
	fmt.Println("📋 STEP-BY-STEP GUIDE TO CALL A CONTRACT:")
	fmt.Println("")
	fmt.Println("  1. Get contract ABI (from compilation or storage)")
	fmt.Println("  2. Parse ABI: abi.JSON(strings.NewReader(abiJSON))")
	fmt.Println("  3. Encode function call: parsedABI.Pack(\"functionName\", args...)")
	fmt.Println("  4. Choose call type:")
	fmt.Println("     - Read-only → CallContract")
	fmt.Println("     - State-changing → ExecuteContract (with EstimateGas first)")
	fmt.Println("  5. Decode return data: parsedABI.Unpack(\"functionName\", returnData)")
	fmt.Println("")

	// ===================================================================
	// 7. PRACTICAL EXAMPLE
	// ===================================================================
	printSubSection("7. PRACTICAL EXAMPLE - CALLING getMessage()")

	// We need the ABI - let's check if we have it saved
	contractInfoFile := findLatestContractInfo()
	if contractInfoFile != "" {
		fmt.Printf("Loading ABI from: %s\n\n", contractInfoFile)

		// Demo the actual call
		fmt.Println("Executing the call...")
		abiJSON := loadABIFromFile(contractInfoFile)
		if abiJSON != "" {
			demonstrateContractCall(ctx, client, contractAddr, abiJSON)
		}
	} else {
		fmt.Println("⚠️  No contract info file found. Run hello_world_demo.go first.")
		fmt.Println("   The demo saves contract ABI to contract_info_*.json")
	}

	printSeparator("SUMMARY")
	fmt.Println("✅ Database structure explained")
	fmt.Println("✅ Storage keys documented")
	fmt.Println("✅ Contract code inspected")
	fmt.Println("✅ Storage slots read")
	fmt.Println("✅ Call methods demonstrated")
	fmt.Println("")
	fmt.Println("💡 Key Takeaways:")
	fmt.Println("   - Current mode: In-Memory (data lost on restart)")
	fmt.Println("   - For persistence: Switch to ImmuDB in cmd/main.go")
	fmt.Println("   - Use CallContract for reads, ExecuteContract for writes")
	fmt.Println("   - Always encode function calls with ABI.Pack()")
	fmt.Println("")
}

func demonstrateContractCall(ctx context.Context, client proto.SmartContractServiceClient, contractAddr common.Address, abiJSON string) {
	// This demonstrates how to actually call a contract
	fmt.Println("Calling getMessage() on the contract...")

	// Parse ABI
	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		fmt.Printf("Failed to parse ABI: %v\n", err)
		return
	}

	// Pack the function call
	callData, err := parsedABI.Pack("getMessage")
	if err != nil {
		fmt.Printf("Failed to pack function call: %v\n", err)
		return
	}

	fmt.Printf("  Encoded call data: 0x%s\n", hex.EncodeToString(callData))

	// Make the call
	caller := common.HexToAddress("0x0000000000000000000000000000000000000000")
	result, err := client.CallContract(ctx, &proto.CallContractRequest{
		Caller:          caller.Bytes(),
		ContractAddress: contractAddr.Bytes(),
		Input:           callData,
	})

	if err != nil {
		fmt.Printf("  ❌ Call failed: %v\n", err)
		return
	}

	// Decode the result
	var message string
	if err := parsedABI.UnpackIntoInterface(&message, "getMessage", result.ReturnData); err != nil {
		fmt.Printf("  Failed to decode result: %v\n", err)
		return
	}

	fmt.Printf("  ✅ Message retrieved: \"%s\"\n", message)
	fmt.Printf("  Gas cost: 0 (read-only)\n")
}

func loadABIFromFile(filename string) string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return ""
	}

	var info map[string]interface{}
	if err := json.Unmarshal(data, &info); err != nil {
		return ""
	}

	if abiData, ok := info["abi"]; ok {
		abiBytes, _ := json.Marshal(abiData)
		return string(abiBytes)
	}

	return ""
}

func findLatestContractInfo() string {
	// Look for contract_info_*.json files
	files, err := os.ReadDir(".")
	if err != nil {
		return ""
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), "contract_info_") && strings.HasSuffix(file.Name(), ".json") {
			return file.Name()
		}
	}

	return ""
}

func printSeparator(title string) {
	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	fmt.Printf("  %s\n", title)
	fmt.Printf("%s\n", strings.Repeat("=", 80))
}

func printSubSection(title string) {
	fmt.Printf("\n%s\n", title)
	fmt.Printf("%s\n", strings.Repeat("-", len(title)))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
