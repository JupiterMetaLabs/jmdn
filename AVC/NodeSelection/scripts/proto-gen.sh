#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${YELLOW}🔧 Generating Protocol Buffer files...${NC}\n"

# Set the proto file path
PROTO_DIR="api/proto/peer"
PROTO_FILE="peer.proto"
OUTPUT_DIR="api/proto/peer"

# Check if proto file exists
if [ ! -f "$PROTO_DIR/$PROTO_FILE" ]; then
    echo -e "${RED}❌ Error: $PROTO_DIR/$PROTO_FILE not found${NC}"
    exit 1
fi

# Create output directory if it doesn't exist
mkdir -p "$OUTPUT_DIR"

# Generate protobuf files
echo -e "${YELLOW}📦 Generating Go protobuf code...${NC}"
protoc \
    --go_out="$OUTPUT_DIR" \
    --go_opt=paths=source_relative \
    --go-grpc_out="$OUTPUT_DIR" \
    --go-grpc_opt=paths=source_relative \
    --proto_path="$PROTO_DIR" \
    "$PROTO_DIR/$PROTO_FILE"

# Check if generation was successful
if [ $? -eq 0 ]; then
    echo -e "\n${GREEN}✅ Successfully generated:${NC}"
    echo -e "   ${GREEN}→${NC} $OUTPUT_DIR/peer.pb.go"
    echo -e "   ${GREEN}→${NC} $OUTPUT_DIR/peer_grpc.pb.go"
else
    echo -e "\n${RED}❌ Failed to generate protobuf files${NC}"
    exit 1
fi

echo -e "\n${GREEN}🎉 Done!${NC}"