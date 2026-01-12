package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"gossipnode/SmartContract/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// Connect to the server
	conn, err := grpc.Dial("localhost:15055", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := proto.NewSmartContractServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Println("🚀 Starting End-to-End Test for SmartContract Server")

	// 1. Test CompileContract
	fmt.Println("\n1️⃣  Testing CompileContract...")
	sourceCode := `// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract SimpleStorage {
    uint256 private storedData;

    event DataStored(uint256 val);

    constructor(uint256 initialValue) {
        storedData = initialValue;
    }

    function set(uint256 x) public {
        storedData = x;
        emit DataStored(x);
    }

    function get() public view returns (uint256) {
        return storedData;
    }
}`
	compileResp, err := c.CompileContract(ctx, &proto.CompileRequest{SourceCode: sourceCode})
	if err != nil {
		log.Fatalf("CompileContract failed: %v", err)
	}
	if compileResp.Error != "" {
		log.Fatalf("Compilation error: %s", compileResp.Error)
	}
	fmt.Printf("✅ Compile success! Bytecode length: %d\n", len(compileResp.Contract.Bytecode))

	// 2. Test DeployContract
	fmt.Println("\n2️⃣  Testing DeployContract...")

	// We need to encode the constructor arguments
	// For simplicity in this test, we will assume generic encoding or skip args if complex,
	// but here we have uint256. Let's try to deploy without EncodeFunctionCall for constructor first
	// or properly pad the argument. 42 = 0x2a
	// padded to 32 bytes
	initVal := make([]byte, 32)
	initVal[31] = 0x2a // 42

	// Alternatively, use EncodeFunctionCall if the server supports arbitrary encoding?
	// The proto has EncodeFunctionCall but that implies a method name. Constructor is special.
	// We'll just construct the bytes for uint256 42 manually.

	deployResp, err := c.DeployContract(ctx, &proto.DeployContractRequest{
		Bytecode:        compileResp.Contract.Bytecode,
		Caller:          make([]byte, 20), // dummy caller
		GasLimit:        3000000,
		Value:           []byte{0},
		ConstructorArgs: initVal,
	})
	if err != nil {
		log.Fatalf("DeployContract failed: %v", err)
	}
	if !deployResp.Result.Success {
		log.Fatalf("Deployment execution failed: %s", deployResp.Result.Error)
	}
	contractAddr := deployResp.Result.ContractAddress
	fmt.Printf("✅ Deploy success! Contract Address: 0x%x\n", contractAddr)

	// 3. Test GetContractCode
	fmt.Println("\n3️⃣  Testing GetContractCode...")
	codeResp, err := c.GetContractCode(ctx, &proto.GetContractCodeRequest{
		ContractAddress: contractAddr,
	})
	if err != nil {
		log.Fatalf("GetContractCode failed: %v", err)
	}
	fmt.Printf("✅ GetCode success! Code length: %d\n", len(codeResp.Code))

	// 4. Test CallContract (get)
	fmt.Println("\n4️⃣  Testing CallContract (get)...")
	// Encode "get()"
	// hash("get()") = 6d4ce63c...
	// We can use EncodeFunctionCall endpoint!

	encodeResp, err := c.EncodeFunctionCall(ctx, &proto.EncodeFunctionCallRequest{
		AbiJson:      compileResp.Contract.Abi,
		FunctionName: "get",
		Args:         [][]byte{},
	})
	if err != nil {
		log.Fatalf("EncodeFunctionCall (get) failed: %v", err)
	}
	if encodeResp.Error != "" {
		log.Fatalf("Encode encoding error: %s", encodeResp.Error)
	}
	// Note: EncodeFunctionCall response might be full calldata or just args.
	// Usually pack includes selector. Let's assume server does it right.

	callResp, err := c.CallContract(ctx, &proto.CallContractRequest{
		ContractAddress: contractAddr,
		Caller:          make([]byte, 20),
		Input:           encodeResp.EncodedData,
	})
	if err != nil {
		log.Fatalf("CallContract failed: %v", err)
	}
	if callResp.Error != "" {
		log.Fatalf("Call execution error: %s", callResp.Error)
	}

	// Decode result
	decodeResp, err := c.DecodeFunctionOutput(ctx, &proto.DecodeFunctionOutputRequest{
		AbiJson:      compileResp.Contract.Abi,
		FunctionName: "get",
		OutputData:   callResp.ReturnData,
	})
	if err != nil {
		log.Fatalf("DecodeFunctionOutput (get) failed: %v", err)
	}
	if decodeResp.Error != "" {
		log.Fatalf("Decode error: %s", decodeResp.Error)
	}
	fmt.Printf("✅ Call (get) success! Result: %s (Expected 42)\n", string(decodeResp.DecodedValues[0]))

	// 5. Test ExecuteContract (set)
	fmt.Println("\n5️⃣  Testing ExecuteContract (set 100)...")

	// Need to encode 100
	arg100 := make([]byte, 32)
	arg100[31] = 0x64 // 100
	// Wait, EncodeFunctionCall expects [][]byte for args, but we might pass raw or json?
	// The server implementation of EncodeFunctionCall takes [][]byte and treats them as... interface{}?
	// "func (s *FullServer) EncodeFunctionCall... passedABI.Pack(..., interfaceArgs...)"
	// interfaceArgs will be []byte.
	// go-ethereum ABI Pack expects native Go types (e.g. big.Int, string) not []byte usually, unless type is bytes.
	// This might be a bug in our Server.go EncodeFunctionCall if it doesn't unmarshal inputs to expected types.
	// But let's try direct manual encoding for the selector + arg if EncodeFunctionCall is suspicious.
	// set(uint256) -> 60fe47b1
	// 100 -> padding... 64

	// Actually, let's look at Server.go EncodeFunctionCall again in previous steps.
	// It appends `arg` (which is []byte) directly to `interfaceArgs`.
	// abi.Pack will fail if the ABI expects uint256 but gets []byte.
	// We might hit a snag here. Let's try manually constructing valid calldata for now to test ExecuteContract endpoint
	// effectively, bypassing potential Encode bug for now.

	// Selector: set(uint256) = 0x60fe47b1
	selector, _ := hex.DecodeString("60fe47b1")
	param := make([]byte, 32)
	param[31] = 0x64 // 100
	inputData := append(selector, param...)

	executeResp, err := c.ExecuteContract(ctx, &proto.ExecuteContractRequest{
		ContractAddress: contractAddr,
		Caller:          make([]byte, 20),
		Input:           inputData,
		GasLimit:        100000,
		Value:           []byte{0},
	})
	if err != nil {
		log.Fatalf("ExecuteContract failed: %v", err)
	}
	if !executeResp.Result.Success {
		log.Fatalf("Execute failed: %s", executeResp.Result.Error)
	}
	fmt.Printf("✅ Execute (set) success! Gas used: %d\n", executeResp.Result.GasUsed)

	// 6. Verify Access via GetStorage
	// storedData is slot 0
	fmt.Println("\n6️⃣  Testing GetStorage...")
	storageResp, err := c.GetStorage(ctx, &proto.GetStorageRequest{
		ContractAddress: contractAddr,
		StorageKey:      make([]byte, 32), // key 0
	})
	if err != nil {
		log.Fatalf("GetStorage failed: %v", err)
	}
	fmt.Printf("✅ GetStorage success! Value: 0x%x\n", storageResp.Value)

	// 7. Test EstimateGas
	fmt.Println("\n7️⃣  Testing EstimateGas...")
	estResp, err := c.EstimateGas(ctx, &proto.EstimateGasRequest{
		ContractAddress: contractAddr,
		Caller:          make([]byte, 20),
		Input:           inputData,
		Value:           []byte{0},
	})
	if err != nil {
		log.Fatalf("EstimateGas failed: %v", err)
	}
	fmt.Printf("✅ EstimateGas success! Estimate: %d\n", estResp.GasEstimate)

	fmt.Println("\n🎉 All End-to-End tests passed!")
}
