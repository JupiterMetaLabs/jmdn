#!/bin/bash

# Compile and Deploy Script
# 1. Sends Solidity source to SmartContract/CompileContract
# 2. Extracts Bytecode and ABI
# 3. Sends DeployContract request

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}=== SmartContract Compiler & Deployer ===${NC}\n"

SOURCE_FILE="hello_world.sol"
if [ ! -f "$SOURCE_FILE" ]; then
    echo -e "${RED}Error: $SOURCE_FILE not found!${NC}"
    exit 1
fi

echo -e "Reading source code from ${GREEN}$SOURCE_FILE${NC}..."
SOURCE_CODE=$(cat "$SOURCE_FILE")

# 1. COMPILE
echo -e "\n${BLUE}Step 1: Compiling...${NC}"
# Use jq to safely construct JSON payload
PAYLOAD=$(jq -n --arg src "$SOURCE_CODE" '{source_code: $src, optimize: true}')

COMPILE_RESPONSE=$(grpcurl -plaintext -d "$PAYLOAD" localhost:15055 smartcontract.SmartContractService/CompileContract 2>&1)

if [[ "$COMPILE_RESPONSE" == *"Error"* || "$COMPILE_RESPONSE" == *"code ="* ]]; then
    echo -e "${RED}Compilation/RPC Failed:${NC}"
    echo "$COMPILE_RESPONSE"
    exit 1
fi

# Extract Bytecode and ABI using jq (assuming response format)
# structure: { "contract": { "bytecode": "...", "abi": "..." } }
# Removing newlines/formatting for cleaner extraction? No, jq handles logs if we pipe clean JSON.
# grpcurl output might not be pure JSON if we don't watch out, but plaintext mode usually outputs formatted JSON.
# I'll rely on text processing to isolate the JSON part if grpcurl adds chatter, but usually it outputs just the response.

# Just in case, I will try to parse using jq.
BYTECODE=$(echo "$COMPILE_RESPONSE" | jq -r '.contract.bytecode')
ABI=$(echo "$COMPILE_RESPONSE" | jq -r '.contract.abi')
ERROR=$(echo "$COMPILE_RESPONSE" | jq -r '.error // empty')

if [ "$ERROR" != "empty" ] && [ -n "$ERROR" ]; then
     echo -e "${RED}Compiler Error:${NC} $ERROR"
     exit 1
fi

if [ "$BYTECODE" == "null" ] || [ -z "$BYTECODE" ]; then
    echo -e "${RED}Failed to extract bytecode.${NC} Raw response:"
    echo "$COMPILE_RESPONSE"
    exit 1
fi

echo -e "${GREEN}Compilation Successful!${NC}"
echo "Bytecode Length: ${#BYTECODE}"
# echo "ABI: $ABI"

# 2. DEPLOY
echo -e "\n${BLUE}Step 2: Deploying...${NC}"

CALLER="0xf302B257cFFB7b30aF229F50F66315194d441C41"
VALUE="0x1"
GAS_LIMIT=3000000

# ABI needs to be escaped for the JSON string in Deploy request??
# Wait, `abi` field in DeployContractRequest is string. The Compiler returns it as string (JSON encoded).
# So `jq` output `.contract.abi` is a string like `[{"inputs":...}]`.
# We need to insert this STRING into the JSON payload for Deploy.
# We can use `jq` to build the deploy payload too!

DEPLOY_PAYLOAD=$(jq -n \
    --arg caller "$CALLER" \
    --arg bytecode "$BYTECODE" \
    --arg value "$VALUE" \
    --arg gas_limit "$GAS_LIMIT" \
    --arg abi "$ABI" \
    '{caller: $caller, bytecode: $bytecode, value: $value, gas_limit: ($gas_limit|tonumber), abi: $abi}')

DEPLOY_RESPONSE=$(grpcurl -plaintext -d "$DEPLOY_PAYLOAD" localhost:15055 smartcontract.SmartContractService/DeployContract 2>&1)

echo "$DEPLOY_RESPONSE"

# Check success
SUCCESS=$(echo "$DEPLOY_RESPONSE" | jq -r '.result.success // false')

if [ "$SUCCESS" == "true" ]; then
    ADDRESS=$(echo "$DEPLOY_RESPONSE" | jq -r '.result.contractAddress')
    echo -e "\n${GREEN}=== Deployment Successful ===${NC}"
    echo -e "Contract Address: ${GREEN}$ADDRESS${NC}"

    # 3. INTERACT - READ (Call)
    echo -e "\n${BLUE}Step 3: Reading State (message)...${NC}"
    
    # Encode 'message()' call
    ENCODE_PAYLOAD=$(jq -n --arg abi "$ABI" '{abi_json: $abi, function_name: "message", args: []}')
    ENCODE_RESP=$(grpcurl -plaintext -d "$ENCODE_PAYLOAD" localhost:15055 smartcontract.SmartContractService/EncodeFunctionCall 2>&1)
    CALL_DATA=$(echo "$ENCODE_RESP" | jq -r '.encodedData')

    # CallContract
    CALL_PAYLOAD=$(jq -n --arg addr "$ADDRESS" --arg data "$CALL_DATA" '{contract_address: $addr, input: $data}')
    CALL_RESP=$(grpcurl -plaintext -d "$CALL_PAYLOAD" localhost:15055 smartcontract.SmartContractService/CallContract 2>&1)
    RETURN_DATA=$(echo "$CALL_RESP" | jq -r '.returnData')
    
    # Decode Output
    DECODE_PAYLOAD=$(jq -n --arg abi "$ABI" --arg data "$RETURN_DATA" '{abi_json: $abi, function_name: "message", output_data: $data}')
    DECODE_RESP=$(grpcurl -plaintext -d "$DECODE_PAYLOAD" localhost:15055 smartcontract.SmartContractService/DecodeFunctionOutput 2>&1)
    MESSAGE=$(echo "$DECODE_RESP" | jq -r '.decodedValues[0]')
    echo -e "Current Message: ${GREEN}$MESSAGE${NC}"

    # 4. INTERACT - WRITE (Execute)
    echo -e "\n${BLUE}Step 4: Writing State (setMessage)...${NC}"
    NEW_MSG="Hello Jupiter"
    
    # Encode 'setMessage' call
    # JSON-encode the args array correctly for EncodeFunctionCall (expects repeated string of JSON values? No, repeated string args)
    # The proto says `repeated string args`. 
    # Logic in `handlers.go` likely parses each string as JSON value if complex, or primitive string?
    # Usually `EncodeFunctionCall` expects JSON-encoded values for arguments. e.g. "\"Hello Jupiter\""
    ENCODE_WRITE_PAYLOAD=$(jq -n --arg abi "$ABI" --arg msg "\"$NEW_MSG\"" '{abi_json: $abi, function_name: "setMessage", args: [$msg]}')
    ENCODE_WRITE_RESP=$(grpcurl -plaintext -d "$ENCODE_WRITE_PAYLOAD" localhost:15055 smartcontract.SmartContractService/EncodeFunctionCall 2>&1)
    WRITE_DATA=$(echo "$ENCODE_WRITE_RESP" | jq -r '.encodedData')
    
    # ExecuteContract
    EXEC_PAYLOAD=$(jq -n --arg caller "$CALLER" --arg addr "$ADDRESS" --arg data "$WRITE_DATA" '{caller: $caller, contract_address: $addr, input: $data, gas_limit: 3000000}')
    EXEC_RESP=$(grpcurl -plaintext -d "$EXEC_PAYLOAD" localhost:15055 smartcontract.SmartContractService/ExecuteContract 2>&1)
    EXEC_SUCCESS=$(echo "$EXEC_RESP" | jq -r '.result.success // false')

    if [ "$EXEC_SUCCESS" == "true" ]; then
        echo -e "${GREEN}Transaction Successful!${NC}"
        
        # 5. VERIFY WRITE
        echo -e "\n${BLUE}Step 5: Verifying New State...${NC}"
        # Reuse CALL_DATA for 'message()' since it's same function
        CALL_RESP_2=$(grpcurl -plaintext -d "$CALL_PAYLOAD" localhost:15055 smartcontract.SmartContractService/CallContract 2>&1)
        RETURN_DATA_2=$(echo "$CALL_RESP_2" | jq -r '.returnData')
        
        DECODE_PAYLOAD_2=$(jq -n --arg abi "$ABI" --arg data "$RETURN_DATA_2" '{abi_json: $abi, function_name: "message", output_data: $data}')
        DECODE_RESP_2=$(grpcurl -plaintext -d "$DECODE_PAYLOAD_2" localhost:15055 smartcontract.SmartContractService/DecodeFunctionOutput 2>&1)
        MESSAGE_2=$(echo "$DECODE_RESP_2" | jq -r '.decodedValues[0]')
        
        echo -e "New Message: ${GREEN}$MESSAGE_2${NC}"
    else
        echo -e "${RED}Transaction Failed!${NC}"
        echo "$EXEC_RESP"
    fi

else
    echo -e "\n${RED}Deployment Failed!${NC}"
fi
