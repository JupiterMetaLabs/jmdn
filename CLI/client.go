package CLI

import (
	"context"
	"fmt"
	"log"
	"time"

	pb "gossipnode/CLI/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

	grpcConn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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

// FastSync performs fast synchronization with a peer
func (c *Client) FastSync(peerAddr string) (*pb.SyncStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	return c.conn.FastSync(ctx, &pb.PeerRequest{Peer: peerAddr})
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

// Example usage:
func exampleUsage() {
	// Connect to the gRPC server
	client, err := NewClient("localhost:15053")
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// List peers
	peers, err := client.ListPeers()
	if err != nil {
		log.Fatalf("Failed to list peers: %v", err)
	}
	fmt.Printf("Found %d peers\n", len(peers.Peers))

	// Get node addresses
	addrs, err := client.ReturnAddrs()
	if err != nil {
		log.Fatalf("Failed to get addresses: %v", err)
	}
	fmt.Printf("Node addresses: %v\n", addrs.Peers)

	// Get database state
	dbState, err := client.GetDatabaseState()
	if err != nil {
		log.Fatalf("Failed to get database state: %v", err)
	}
	fmt.Printf("Main DB TxID: %d\n", dbState.MainDb.TxId)
}
