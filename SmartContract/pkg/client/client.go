package client

import (
	"context"
	"fmt"
	"gossipnode/SmartContract/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a high-level wrapper for the SmartContract service
type Client struct {
	conn   *grpc.ClientConn
	remote proto.SmartContractServiceClient
}

// NewClient creates a new SmartContract client connection
func NewClient(address string) (*Client, error) {
	conn, err := grpc.Dial(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to smart contract server: %w", err)
	}

	return &Client{
		conn:   conn,
		remote: proto.NewSmartContractServiceClient(conn),
	}, nil
}

// Close closes the underlying connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// CheckConnectivity verifies connection to the server
func (c *Client) CheckConnectivity(ctx context.Context) error {
	state := c.conn.GetState()
	if state == connectivity.Ready || state == connectivity.Idle {
		return nil
	}
	return fmt.Errorf("connection not ready: %s", state)
}

// CompileContract compiles Solidity source code
func (c *Client) CompileContract(ctx context.Context, sourceCode string) (*proto.CompileResponse, error) {
	return c.remote.CompileContract(ctx, &proto.CompileRequest{
		SourceCode: sourceCode,
	})
}

// DeployOptions handles optional parameters for deployment
type DeployOptions struct {
	GasLimit uint64
	Value    []byte // BigInt bytes
	ABI      string // Contract ABI JSON
}

// DeployContract deploys a compiled contract
func (c *Client) DeployContract(ctx context.Context, caller []byte, bytecode []byte, opts *DeployOptions) (*proto.DeployContractResponse, error) {
	// Ensure bytecode is string (hex encoded) as per proto definition
	// If the SDK user passes raw bytes, we should encode it, but current usage suggests
	// they pass what they got from CompileResponse which is string.
	// But the function signature accepted []byte.
	// Let's assume the user passes raw bytes and we hex encode?
	// OR, let's look at CompileResponse. It returns CombinedContract.Bytecode which is string.
	// So we should probably change signature to string or just cast if it's already bytes of string.
	// For simplicity and matching proto, let's cast string(bytecode) assuming it holds the hex string bytes.

	req := &proto.DeployContractRequest{
		Caller:   caller,
		Bytecode: string(bytecode), // Proto expects string (hex)
		GasLimit: 3000000,          // Default
	}

	if opts != nil {
		if opts.GasLimit > 0 {
			req.GasLimit = opts.GasLimit
		}
		if len(opts.Value) > 0 {
			req.Value = opts.Value
		}
		if opts.ABI != "" {
			req.Abi = opts.ABI
		}
	}

	return c.remote.DeployContract(ctx, req)
}

// ExecuteOptions handles optional parameters for execution
type ExecuteOptions struct {
	GasLimit uint64
	Value    []byte
}

// ExecuteContract executes a state-changing function
func (c *Client) ExecuteContract(ctx context.Context, caller []byte, contractAddr []byte, input []byte, opts *ExecuteOptions) (*proto.ExecuteContractResponse, error) {
	req := &proto.ExecuteContractRequest{
		Caller:          caller,
		ContractAddress: contractAddr,
		Input:           input,
		GasLimit:        100000, // Default
	}

	if opts != nil {
		if opts.GasLimit > 0 {
			req.GasLimit = opts.GasLimit
		}
		if len(opts.Value) > 0 {
			req.Value = opts.Value
		}
	}

	return c.remote.ExecuteContract(ctx, req)
}

// CallContract calls a read-only function
func (c *Client) CallContract(ctx context.Context, caller []byte, contractAddr []byte, input []byte) (*proto.CallContractResponse, error) {
	return c.remote.CallContract(ctx, &proto.CallContractRequest{
		Caller:          caller,
		ContractAddress: contractAddr,
		Input:           input,
	})
}

// GetContractCode retrieves the bytecode of a contract
func (c *Client) GetContractCode(ctx context.Context, contractAddr []byte) (*proto.GetContractCodeResponse, error) {
	return c.remote.GetContractCode(ctx, &proto.GetContractCodeRequest{
		ContractAddress: contractAddr,
	})
}

// GetStorage reads a storage slot
func (c *Client) GetStorage(ctx context.Context, contractAddr []byte, key []byte) (*proto.GetStorageResponse, error) {
	req := &proto.GetStorageRequest{
		ContractAddress: contractAddr,
		// Proto field is named 'storage_key', not 'Key'
		StorageKey: key,
	}
	return c.remote.GetStorage(ctx, req)
}

// ListContracts retrieves deployed contracts
func (c *Client) ListContracts(ctx context.Context, limit uint32) (*proto.ListContractsResponse, error) {
	return c.remote.ListContracts(ctx, &proto.ListContractsRequest{
		Limit: limit,
	})
}
