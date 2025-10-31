# Block gRPC Server

## Overview
The Block gRPC server provides a gRPC interface for block processing operations, complementing the existing REST API.

## Starting the Server

The Block gRPC server can be started using the `--blockgrpc` flag:

```bash
./jmdn --blockgen 8080 --blockgrpc 15055
```

This starts:
- REST API on port 8080
- gRPC server on port 15055

## API Endpoint

### ProcessBlock

Processes a ZK block through consensus and stores it.

**gRPC Method:** `BlockService.ProcessBlock`

**Request:** `ProcessBlockRequest`
```protobuf
message ProcessBlockRequest {
    ZKBlock block = 1;
}
```

**Response:** `ProcessBlockResponse`
```protobuf
message ProcessBlockResponse {
    bool success = 1;
    string message = 2;
    string block_hash = 3;
    uint64 block_number = 4;
    string error = 5;
}
```

## Usage Example

### Using gRPCurl (for testing)

```bash
# Convert your JSON block to base64 (for the stark_proof)
cat block.json | grpcurl -plaintext \
  -d @ \
  localhost:15055 \
  Block.BlockService/ProcessBlock
```

### Using Go Client

```go
package main

import (
    "context"
    "fmt"
    
    pb "gossipnode/Block/proto"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func main() {
    conn, err := grpc.NewClient("localhost:15055", 
        grpc.WithTransportCredentials(insecure.NewCredentials()))
    if err != nil {
        panic(err)
    }
    defer conn.Close()
    
    client := pb.NewBlockServiceClient(conn)
    
    // Create your block request
    req := &pb.ProcessBlockRequest{
        Block: &pb.ZKBlock{
            BlockNumber: 1,
            Status: "verified",
            // ... other fields
        },
    }
    
    resp, err := client.ProcessBlock(context.Background(), req)
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("Success: %v\n", resp.Success)
    fmt.Printf("Block Hash: %s\n", resp.BlockHash)
}
```

## Architecture

The gRPC server mirrors the REST API's `processZKBlock` handler but uses:
- Protocol Buffers for serialization
- gRPC for transport
- Support for larger message sizes (50MB)

## Key Features

1. **Consensus Integration**: Blocks go through the same consensus process as REST API
2. **Health Checks**: Includes gRPC health checking service
3. **Reflection**: gRPC reflection enabled for debugging
4. **Structured Logging**: All requests are logged to `logs/block-grpc.log`
5. **Error Handling**: Proper gRPC status codes for different error conditions

## Comparison: REST vs gRPC

| Feature | REST API | gRPC |
|---------|----------|------|
| Protocol | HTTP/JSON | gRPC/Protobuf |
| Port | Configurable | Configurable (separate) |
| Message Size | Standard HTTP limits | 50MB configured |
| Serialization | JSON | Protobuf (more efficient) |
| Use Case | Web clients, browsers | Internal services, mobile apps |

## Status Codes

- `codes.InvalidArgument`: Block validation failed (no transactions, not verified)
- `codes.Internal`: Consensus or processing errors
- `codes.OK`: Success

