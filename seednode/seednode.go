package seednode

import (
	"context"
	"fmt"
	peerpb "gossipnode/seednode/proto"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/multiformats/go-multiaddr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

// getPublicIP fetches the public IP address using ifconfig.me
func getPublicIP() (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get("https://ifconfig.me/ip")
	if err != nil {
		return "", fmt.Errorf("failed to get public IP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	publicIP := strings.TrimSpace(string(body))
	if publicIP == "" {
		return "", fmt.Errorf("empty public IP response")
	}

	return publicIP, nil
}

// isPublicAddress checks if an address is public (not localhost or private)
func isPublicAddress(addr string) bool {
	// Skip localhost addresses
	if strings.Contains(addr, "/127.0.0.1/") || strings.Contains(addr, "/::1/") || strings.Contains(addr, "/localhost/") {
		return false
	}
	// Skip private IP ranges
	if strings.Contains(addr, "/10.") || strings.Contains(addr, "/192.168.") || strings.Contains(addr, "/172.") {
		return false
	}
	return true
}

// Client represents a seed node gRPC client
type Client struct {
	conn   *grpc.ClientConn
	client peerpb.PeerDirectoryClient
}

// NewClient creates a new seed node client
func NewClient(seedNodeURL string) (*Client, error) {
	// Create gRPC connection
	conn, err := grpc.Dial(seedNodeURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to seed node: %w", err)
	}

	// Create client
	client := peerpb.NewPeerDirectoryClient(conn)

	return &Client{
		conn:   conn,
		client: client,
	}, nil
}

// Close closes the gRPC connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// RegisterPeer registers this peer with the seed node
func (c *Client) RegisterPeer(h host.Host) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get peer addresses - try to use public IP first
	var multiaddrs []string

	// Try to get public IP and use it with the detected port
	publicIP, err := getPublicIP()
	if err == nil {
		// Find the first TCP port from the host's listening addresses
		var tcpPort string
		for _, addr := range h.Addrs() {
			addrStr := addr.String()
			if strings.Contains(addrStr, "/tcp/") {
				// Extract port from multiaddress like /ip4/0.0.0.0/tcp/15000
				parts := strings.Split(addrStr, "/tcp/")
				if len(parts) > 1 {
					portParts := strings.Split(parts[1], "/")
					if len(portParts) > 0 {
						tcpPort = portParts[0]
						break
					}
				}
			}
		}

		// Use default port if none found
		if tcpPort == "" {
			tcpPort = "15000"
		}

		// Use the public IP with the detected port
		publicAddr := fmt.Sprintf("/ip4/%s/tcp/%s/p2p/%s", publicIP, tcpPort, h.ID().String())
		fullAddr, err := multiaddr.NewMultiaddr(publicAddr)
		if err == nil {
			multiaddrs = append(multiaddrs, fullAddr.String())
		}
	}

	// Also include local addresses as fallback
	for _, addr := range h.Addrs() {
		// Skip localhost and private addresses for external registration
		if isPublicAddress(addr.String()) {
			// Use proper multiaddress construction
			fullAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String()))
			if err == nil {
				multiaddrs = append(multiaddrs, fullAddr.String())
			}
		}
	}

	// If still no addresses found, include all addresses as final fallback
	if len(multiaddrs) == 0 {
		for _, addr := range h.Addrs() {
			fullAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String()))
			if err == nil {
				multiaddrs = append(multiaddrs, fullAddr.String())
			}
		}
	}

	// Create peer record
	peerRecord := &peerpb.SignedPeerRecord{
		PeerId:        h.ID().String(),
		Multiaddrs:    multiaddrs,
		Seq:           1,
		CurrentStatus: peerpb.PeerStatus_PEER_STATUS_ACTIVE,
		Region:        "", // No region specified
		// Signature fields will be populated by signPeerRecord
		V: "",
		R: "",
		S: "",
	}

	// Sign the peer record
	err = SignPeerRecord(peerRecord, h)
	if err != nil {
		return fmt.Errorf("failed to sign peer record: %w", err)
	}

	// Create registration request
	request := &peerpb.RegisterPeerRequest{
		PeerRecord: peerRecord,
		Neighbors:  []string{}, // No initial neighbors for now
	}

	// Call the seed node
	response, err := c.client.RegisterPeer(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to register with seed node: %w", err)
	}

	if !response.Accepted {
		return fmt.Errorf("seed node rejected registration: %s", response.Message)
	}

	fmt.Printf("Successfully registered with seed node: %s\n", response.Message)
	return nil
}

// SendHeartbeat sends a heartbeat to the seed node
func (c *Client) SendHeartbeat(h host.Host) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get peer addresses
	var multiaddrs []string
	for _, addr := range h.Addrs() {
		multiaddrs = append(multiaddrs, fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String()))
	}

	// Create heartbeat message
	heartbeat := &peerpb.HeartbeatMessage{
		PeerId:     h.ID().String(),
		Status:     peerpb.PeerStatus_PEER_STATUS_ACTIVE,
		Multiaddrs: multiaddrs,
		// Signature fields will be populated by signHeartbeat
		V: "",
		R: "",
		S: "",
	}

	// Sign the heartbeat message
	err := SignHeartbeat(heartbeat, h)
	if err != nil {
		return fmt.Errorf("failed to sign heartbeat: %w", err)
	}

	// Create heartbeat request
	request := &peerpb.SendHeartbeatRequest{
		Heartbeat: heartbeat,
	}

	// Send heartbeat
	response, err := c.client.SendHeartbeat(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}

	if !response.Accepted {
		return fmt.Errorf("seed node rejected heartbeat: %s", response.Message)
	}

	return nil
}

// GetPeers retrieves a list of peers from the seed node
func (c *Client) GetPeers(limit int32, status peerpb.PeerStatus) ([]*peerpb.SignedPeerRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create peer list request
	request := &peerpb.PeerListRequest{
		Limit:  limit,
		Status: status,
	}

	// Get peers
	response, err := c.client.ListPeers(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get peers: %w", err)
	}

	return response.Peers, nil
}

// HealthCheck performs a health check on the seed node
func (c *Client) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.client.HealthCheck(ctx, &emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("seed node health check failed: %w", err)
	}

	return nil
}
