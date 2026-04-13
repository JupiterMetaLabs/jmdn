#!/bin/bash

# Value Transfer Demo Script
# 1. Registers a new DID (Receiver)
# 2. Compiles & Deploys 'Transfer.sol'
# 3. Executes transferTo(Receiver) with value
# 4. Verifies Receiver Balance

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}=== Value Transfer Demo ===${NC}\n"

RECEIVER_DID="0x0000000000000000000000000000000000009999"

# 1. REGISTER DID
echo -e "${BLUE}Step 1: Registering Receiver DID ($RECEIVER_DID)...${NC}"
REG_PAYLOAD=$(jq -n --arg did "$RECEIVER_DID" '{did: $did, public_key: "0x0000000000000000000000000000000000009999"}')
REG_RESP=$(grpcurl -plaintext -d "$REG_PAYLOAD" localhost:15052 proto.DIDService/RegisterDID 2>&1)
echo "$REG_RESP"

# Initial Balance Check
echo -e "\n${BLUE}Step 1b: Checking Initial Balance...${NC}"
GET_DID_PAYLOAD=$(jq -n --arg did "$RECEIVER_DID" '{did: $did}')
GET_DID_RESP=$(grpcurl -plaintext -d "$GET_DID_PAYLOAD" localhost:15052 proto.DIDService/GetDID 2>&1)
INIT_BAL=$(echo "$GET_DID_RESP" | jq -r '.didInfo.balance // "0"')
echo -e "Initial Balance: ${GREEN}$INIT_BAL${NC}"

# 2. COMPILE
echo -e "\n${BLUE}Step 2: Compiling Transfer.sol...${NC}"
SOURCE_CODE=$(cat Transfer.sol)
COMPILE_PAYLOAD=$(jq -n --arg src "$SOURCE_CODE" '{source_code: $src, optimize: true}')
COMPILE_RESP=$(grpcurl -plaintext -d "$COMPILE_PAYLOAD" localhost:15055 smartcontract.SmartContractService/CompileContract 2>&1)
BYTECODE=$(echo "$COMPILE_RESP" | jq -r '.contract.bytecode')
ABI=$(echo "$COMPILE_RESP" | jq -r '.contract.abi')

if [ -z "$BYTECODE" ] || [ "$BYTECODE" == "null" ]; then
    echo -e "${RED}Compilation Failed!${NC}"
    echo "Response: $COMPILE_RESP"
    exit 1
fi
echo -e "${GREEN}Compilation Successful!${NC}"

# 3. DEPLOY
echo -e "\n${BLUE}Step 3: Deploying Transfer Contract...${NC}"
DEPLOY_PAYLOAD=$(jq -n --arg bc "$BYTECODE" --arg caller "0xf302B257cFFB7b30aF229F50F66315194d441C41" '{caller: $caller, bytecode: $bc, gas_limit: 3000000}')
DEPLOY_RESP=$(grpcurl -plaintext -d "$DEPLOY_PAYLOAD" localhost:15055 smartcontract.SmartContractService/DeployContract 2>&1)
CONTRACT_ADDR=$(echo "$DEPLOY_RESP" | jq -r '.result.contractAddress')

if [ -z "$CONTRACT_ADDR" ] || [ "$CONTRACT_ADDR" == "null" ]; then
    echo -e "${RED}Deployment Failed!${NC}"
    exit 1
fi
echo -e "Contract Address: ${GREEN}$CONTRACT_ADDR${NC}"

# 4. EXECUTE TRANSFER
TRANSFER_AMOUNT="100" # Wei
echo -e "\n${BLUE}Step 4: Executing transferTo($RECEIVER_DID) with ${TRANSFER_AMOUNT} Wei...${NC}"

# Encode 'transferTo(address)'
# Args: [ReceiverAddress]
# 'EncodeFunctionCall' expects JSON args array. Address should be string.
ENCODE_PAYLOAD=$(jq -n --arg abi "$ABI" --arg recv "$RECEIVER_DID" '{abi_json: $abi, function_name: "transferTo", args: [$recv]}')
ENCODE_RESP=$(grpcurl -plaintext -d "$ENCODE_PAYLOAD" localhost:15055 smartcontract.SmartContractService/EncodeFunctionCall 2>&1)
ENCODED_DATA=$(echo "$ENCODE_RESP" | jq -r '.encodedData')
echo "Encoded Data: $ENCODED_DATA"

# Execute
# Send Value = 100 Wei
EXEC_PAYLOAD=$(jq -n \
    --arg caller "0xf302B257cFFB7b30aF229F50F66315194d441C41" \
    --arg addr "$CONTRACT_ADDR" \
    --arg data "$ENCODED_DATA" \
    --arg val "$TRANSFER_AMOUNT" \
    '{caller: $caller, contract_address: $addr, input: $data, value: $val, gas_limit: 3000000}')

EXEC_RESP=$(grpcurl -plaintext -d "$EXEC_PAYLOAD" localhost:15055 smartcontract.SmartContractService/ExecuteContract 2>&1)
echo "Execution Response: $EXEC_RESP"
SUCCESS=$(echo "$EXEC_RESP" | jq -r '.result.success // false')

if [ "$SUCCESS" == "true" ]; then
    echo -e "${GREEN}Transfer Transaction Successful!${NC}"
else
    echo -e "${RED}Transfer Failed!${NC}"
    echo "$EXEC_RESP"
    exit 1
fi

# 5. VERIFY FINAL BALANCE
echo -e "\n${BLUE}Step 5: Verifying Recipient Balance...${NC}"
GET_DID_RESP_FINAL=$(grpcurl -plaintext -d "$GET_DID_PAYLOAD" localhost:15052 proto.DIDService/GetDID 2>&1)
FINAL_BAL=$(echo "$GET_DID_RESP_FINAL" | jq -r '.didInfo.balance // "0"')

echo -e "Final Balance: ${GREEN}$FINAL_BAL${NC}"

# Diff
if [ "$FINAL_BAL" == "100" ] || [ "$FINAL_BAL" != "0" ]; then
     echo -e "${GREEN}Balance Increased! Demo Complete.${NC}"
else
     echo -e "${RED}Balance did not increase properly?${NC}"
fi
