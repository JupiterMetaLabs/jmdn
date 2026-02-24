package CLI

import (
	"context"
	"fmt"
	"time"

	pb "gossipnode/CLI/proto"
	"gossipnode/config/settings"
	"gossipnode/pkg/gatekeeper"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Client wraps the gRPC CLI client
type Client struct {
	conn     pb.CLIServiceClient
	grpcConn *grpc.ClientConn
}

// NewClient creates a new CLI client connecting to the specified address
func NewClient(addr string) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Setup minimal logger for CLI client
	// We use the helper from logger.go which gets it from global AsyncLogger
	logger := clientLogger()

	// 2. Load Security Config
	// We assume settings have been loaded by the CLI entrypoint (cobra/viper)
	secCfg := &settings.Get().Security

	// 3. Configure Transport Credentials (Standardized Helper)
	tlsLoader := gatekeeper.NewTLSLoader(secCfg, logger)
	// We are the client connecting to "cli_admin" service, identifying as "cli_client"
	creds, err := tlsLoader.LoadClientCredentials(settings.ServiceCLI, "cli_client")
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials for admin connection: %w", err)
	}

	grpcConn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to gRPC server: %w", err)
	}

	return &Client{
		conn:     pb.NewCLIServiceClient(grpcConn),
		grpcConn: grpcConn,
	}, nil
}

// Close closes the gRPC connection
func (c *Client) Close() error {
	return c.grpcConn.Close()
}

// ListPeers returns a list of managed peers
func (c *Client) ListPeers() (*pb.PeerList, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.ListPeers(ctx, &emptypb.Empty{})
}

// AddPeer adds a peer to the managed peer list
func (c *Client) AddPeer(peerAddr string) (*pb.OperationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.AddPeer(ctx, &pb.PeerRequest{Peer: peerAddr})
}

// RemovePeer removes a peer from the managed peer list
func (c *Client) RemovePeer(peerID string) (*pb.OperationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.RemovePeer(ctx, &pb.PeerRequest{Peer: peerID})
}

// CleanPeers removes offline peers from the managed peer list
func (c *Client) CleanPeers() (*pb.CleanPeersResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.CleanPeers(ctx, &emptypb.Empty{})
}

// SendMessage sends a message to a specific peer
func (c *Client) SendMessage(target, message string) (*pb.OperationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.conn.SendMessage(ctx, &pb.MessageRequest{
		Target:  target,
		Message: message,
	})
}

// SendYggdrasilMessage sends a message via Yggdrasil
func (c *Client) SendYggdrasilMessage(target, message string) (*pb.OperationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.conn.SendYggdrasilMessage(ctx, &pb.MessageRequest{
		Target:  target,
		Message: message,
	})
}

// SendFile sends a file to a peer
func (c *Client) SendFile(peer, filepath, remoteFilename string) (*pb.OperationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return c.conn.SendFile(ctx, &pb.FileRequest{
		Peer:           peer,
		Filepath:       filepath,
		RemoteFilename: remoteFilename,
	})
}

// BroadcastMessage broadcasts a message to all connected peers
func (c *Client) BroadcastMessage(message string) (*pb.OperationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.conn.BroadcastMessage(ctx, &pb.MessageRequest{Message: message})
}

// GetMessageStats returns messaging statistics
func (c *Client) GetMessageStats() (*pb.MessageStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.GetMessageStats(ctx, &emptypb.Empty{})
}

// GetDID retrieves a DID document
func (c *Client) GetDID(did string) (*pb.DIDDocument, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.GetDID(ctx, &pb.DIDRequest{Did: did})
}

// PropagateDID propagates a DID to the network
func (c *Client) PropagateDID(did, publicKey, balance string) (*pb.OperationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.conn.PropagateDID(ctx, &pb.DIDPropagationRequest{
		Did:       did,
		PublicKey: publicKey,
		Balance:   balance,
	})
}

// FastSync performs fast synchronization with a peer
func (c *Client) FastSync(peerAddr string) (*pb.SyncStats, error) {
	ctx := context.Background()
	defer ctx.Done()
	return c.conn.FastSync(ctx, &pb.PeerRequest{Peer: peerAddr})
}

// FirstSync performs first synchronization with a peer (server or client mode)
func (c *Client) FirstSync(peerAddr string, mode string) (*pb.SyncStats, error) {
	// ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	// defer cancel()
	ctx := context.Background()
	defer ctx.Done()
	return c.conn.FirstSync(ctx, &pb.FirstSyncRequest{
		Peer: peerAddr,
		Mode: mode,
	})
}

// GetDatabaseState returns the current database state
func (c *Client) GetDatabaseState() (*pb.DatabaseStates, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.GetDatabaseState(ctx, &emptypb.Empty{})
}

// ReturnAddrs returns all addresses for this node
func (c *Client) ReturnAddrs() (*pb.Addrs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.ReturnAddrs(ctx, &emptypb.Empty{})
}

// GetNodeVersion returns the version information of the remote node
func (c *Client) GetNodeVersion() (*pb.VersionInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.conn.GetNodeVersion(ctx, &emptypb.Empty{})
}
