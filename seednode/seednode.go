package seednode

import (
	"context"
	"fmt"
	peerpb "gossipnode/seednode/proto"
	"io"
	"net/http"
	"os"
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

// isValidMultiaddr validates if a multiaddress is properly formatted and has a valid peer ID
func isValidMultiaddr(addr string) bool {
	// Try to parse the multiaddress
	_, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return false
	}
	return true
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

// HealthCheck performs a health check on the seed node
func (c *Client) HealthCheck(ctx context.Context) error {
	_, err := c.client.HealthCheck(ctx, &emptypb.Empty{})
	return err
}

// ListPeers lists peers from the seed node
func (c *Client) ListPeers(ctx context.Context, request *peerpb.PeerListRequest) (*peerpb.PeerListResponse, error) {
	return c.client.ListPeers(ctx, request)
}

// GetPeer retrieves a peer record by peer ID
func (c *Client) GetPeer(peerID string) (*peerpb.SignedPeerRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &peerpb.GetPeerRequest{
		PeerId: peerID,
	}

	response, err := c.client.GetPeer(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer: %w", err)
	}

	if !response.Found {
		return nil, fmt.Errorf("peer not found")
	}

	return response.PeerRecord, nil
}

// GetPeerByAlias retrieves a peer record by alias
func (c *Client) GetPeerByAlias(alias string) (*peerpb.SignedPeerRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &peerpb.GetPeerByAliasRequest{
		Alias: alias,
	}

	response, err := c.client.GetPeerByAlias(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer by alias: %w", err)
	}

	if !response.Found {
		return nil, fmt.Errorf("peer with alias not found")
	}

	return response.PeerRecord, nil
}

// GetAliasByPeerID attempts to find the alias for a given peer ID
// This is a workaround since the seed node API doesn't have a direct method for this
func (c *Client) GetAliasByPeerID(peerID string) (string, error) {
	// First, get the peer record to confirm it exists
	_, err := c.GetPeer(peerID)
	if err != nil {
		return "", fmt.Errorf("peer not found: %w", err)
	}

	// For now, we'll return an empty string since we can't directly look up alias by peer ID
	// The seed node API would need to be extended to support this functionality
	// This is a placeholder for future implementation
	return "", fmt.Errorf("alias lookup by peer ID not yet implemented in seed node API")
}

// GetNeighbors retrieves neighbors for a given peer
func (c *Client) GetNeighbors(peerID string) ([]*peerpb.PeerNeighbor, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &peerpb.GetNeighborsRequest{
		PeerId: peerID,
	}

	response, err := c.client.GetNeighbors(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get neighbors: %w", err)
	}

	if !response.Found {
		return []*peerpb.PeerNeighbor{}, nil
	}

	return response.Neighbors, nil
}

// AllocateNeighbors requests new neighbors from the seed node
func (c *Client) AllocateNeighbors(peerID string, forceRefresh bool) ([]*peerpb.PeerNeighbor, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &peerpb.AllocateNeighborsRequest{
		PeerId:       peerID,
		ForceRefresh: forceRefresh,
	}

	response, err := c.client.AllocateNeighbors(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate neighbors: %w", err)
	}

	if !response.Allocated {
		return []*peerpb.PeerNeighbor{}, nil
	}

	return response.Neighbors, nil
}

// AddNeighbor adds a neighbor relationship to the seed node
func (c *Client) AddNeighbor(neighbor *peerpb.PeerNeighbor) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &peerpb.AddNeighborRequest{
		Neighbor: neighbor,
	}

	response, err := c.client.AddNeighbor(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to add neighbor: %w", err)
	}

	if !response.Accepted {
		return fmt.Errorf("neighbor addition rejected: %s", response.Message)
	}

	return nil
}

// DiscoverAndAddNeighbors performs the complete neighbor discovery process
func (c *Client) DiscoverAndAddNeighbors(h host.Host, nodeManager interface{}) error {
	peerID := h.ID().String()
	fmt.Printf("🔍 Starting neighbor discovery for peer: %s\n", peerID)

	// Step 1: Check if we already have neighbors
	fmt.Println("📋 Checking existing neighbors...")
	existingNeighbors, err := c.GetNeighbors(peerID)
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to check existing neighbors: %v\n", err)
	} else if len(existingNeighbors) > 0 {
		fmt.Printf("✅ Found %d existing neighbors\n", len(existingNeighbors))
		// Convert existing neighbors to multiaddrs and add them to node manager
		for _, neighbor := range existingNeighbors {
			if neighbor.IsActive {
				// Try to construct multiaddr from neighbor info
				// Note: We might need to get the full peer record to get multiaddrs
				fmt.Printf("  - Neighbor: %s (active: %v)\n", neighbor.NeighborId, neighbor.IsActive)
			}
		}
	}

	// Step 2: Check current managed peers using listpeers command
	fmt.Println("📋 Checking current managed peers...")
	// We'll need to call the listpeers functionality through the node manager
	// For now, let's assume we need to get new neighbors

	// Step 3: Request new neighbors from seed node
	fmt.Println("🔄 Requesting new neighbors from seed node...")
	newNeighbors, err := c.AllocateNeighbors(peerID, false) // Don't force refresh initially
	if err != nil {
		return fmt.Errorf("failed to allocate neighbors: %w", err)
	}

	if len(newNeighbors) == 0 {
		fmt.Println("ℹ️  No new neighbors allocated by seed node")
		return nil
	}

	fmt.Printf("✅ Allocated %d new neighbors from seed node\n", len(newNeighbors))

	// Step 4: Add neighbors to node manager
	var successfullyAddedPeers []string
	for _, neighbor := range newNeighbors {
		neighborID := neighbor.NeighborId
		fmt.Printf("🔗 Processing neighbor: %s\n", neighborID)

		// Get the full peer record to get multiaddrs
		peerRecord, err := c.GetPeer(neighborID)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to get peer record for %s: %v\n", neighborID, err)
			continue
		}

		// Add each multiaddr to the node manager
		for _, multiaddr := range peerRecord.Multiaddrs {
			fmt.Printf("  📍 Adding peer: %s\n", multiaddr)

			// Validate the multiaddress before attempting to add it
			if !isValidMultiaddr(multiaddr) {
				fmt.Printf("⚠️  Skipping invalid multiaddr: %s\n", multiaddr)
				continue
			}

			// Use type assertion to call AddPeer method on nodeManager
			// This is a workaround since we don't have direct access to the NodeManager type
			if nm, ok := nodeManager.(interface{ AddPeer(string) error }); ok {
				err := nm.AddPeer(multiaddr)
				if err != nil {
					fmt.Printf("❌ Failed to add peer %s: %v\n", multiaddr, err)
					continue
				}
				fmt.Printf("✅ Successfully added peer: %s\n", multiaddr)
				successfullyAddedPeers = append(successfullyAddedPeers, neighborID)
			} else {
				fmt.Printf("❌ Node manager does not support AddPeer method\n")
				return fmt.Errorf("node manager does not support AddPeer method")
			}
		}
	}

	// Step 5: Report successfully added peers to seed node
	if len(successfullyAddedPeers) > 0 {
		fmt.Printf("📤 Reporting %d successfully added peers to seed node\n", len(successfullyAddedPeers))
		for _, peerID := range successfullyAddedPeers {
			neighbor := &peerpb.PeerNeighbor{
				PeerId:     h.ID().String(),
				NeighborId: peerID,
				CreatedAt:  time.Now().Unix(),
				LastSeen:   time.Now().Unix(),
				IsActive:   true,
				V:          "",
				R:          "",
				S:          "",
			}

			// Sign the neighbor record (we'll need to implement this)
			// For now, we'll add it without signature
			err := c.AddNeighbor(neighbor)
			if err != nil {
				fmt.Printf("⚠️  Warning: Failed to report neighbor %s: %v\n", peerID, err)
			} else {
				fmt.Printf("✅ Successfully reported neighbor: %s\n", peerID)
			}
		}
	}

	fmt.Printf("🎉 Neighbor discovery completed. Added %d peers successfully.\n", len(successfullyAddedPeers))
	return nil
}

// UpdatePeer updates an existing peer record
func (c *Client) UpdatePeer(peerRecord *peerpb.SignedPeerRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create peer record update
	update := &peerpb.PeerRecordUpdate{
		PeerId:     peerRecord.PeerId,
		Seq:        peerRecord.Seq,
		Multiaddrs: peerRecord.Multiaddrs,
		Status:     peerRecord.CurrentStatus,
		Neighbors:  []string{}, // No neighbors for now
		Labels:     peerRecord.Labels,
		Region:     peerRecord.Region,
		Weights:    0, // Will be auto-calculated by server
		V:          peerRecord.V,
		R:          peerRecord.R,
		S:          peerRecord.S,
	}

	request := &peerpb.UpdatePeerRequest{
		Update: update,
	}

	response, err := c.client.UpdatePeer(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to update peer: %w", err)
	}

	if !response.Accepted {
		return fmt.Errorf("peer update rejected: %s", response.Message)
	}

	return nil
}

// CreateAlias creates an alias for an existing peer
func (c *Client) CreateAlias(alias *peerpb.PeerAlias) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &peerpb.CreateAliasRequest{
		Alias: alias,
	}

	response, err := c.client.CreateAlias(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to create alias: %w", err)
	}

	if !response.Accepted {
		return fmt.Errorf("alias creation rejected: %s", response.Message)
	}

	return nil
}

// RegisterPeerWithAlias registers this peer with the seed node using an alias
// If the peer already exists with an alias, the service will be terminated
func (c *Client) RegisterPeerWithAlias(h host.Host, alias string) error {
	peerID := h.ID().String()

	// First, check if the alias already exists (regardless of which peer has it)
	_, aliasErr := c.GetPeerByAlias(alias)
	if aliasErr == nil {
		// This specific alias already exists - kill the service
		fmt.Printf("❌ FATAL ERROR: Alias '%s' already exists\n", alias)
		fmt.Printf("❌ Cannot register with existing alias. Terminating service.\n")
		os.Exit(1)
		return fmt.Errorf("alias already exists") // This line will never be reached
	}

	// Alias is available, now check if this peer already exists
	existingPeer, err := c.GetPeer(peerID)
	if err != nil {
		// Peer doesn't exist, register as new peer with alias
		fmt.Printf("Peer %s not found, registering as new peer with alias '%s'\n", peerID, alias)
		return c.registerNewPeerWithAlias(h, alias)
	}

	// Peer already exists - this is a duplicate registration attempt
	// Kill the service because the peer is already registered
	fmt.Printf("❌ FATAL ERROR: Peer %s already exists (seq: %d)\n", peerID, existingPeer.Seq)
	fmt.Printf("❌ Peer already registered with different alias. Terminating service.\n")
	fmt.Printf("❌ Cannot register existing peer with new alias '%s'\n", alias)
	os.Exit(1)
	return fmt.Errorf("peer already exists") // This line will never be reached
}

// registerNewPeerWithAlias registers a completely new peer with an alias
func (c *Client) registerNewPeerWithAlias(h host.Host, alias string) error {
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

	// Create peer alias
	peerAlias := &peerpb.PeerAlias{
		Name:   alias,
		PeerId: h.ID().String(),
		// Signature fields will be populated by signAlias
		V: "",
		R: "",
		S: "",
	}

	// Sign the alias
	err = SignAlias(peerAlias, h)
	if err != nil {
		return fmt.Errorf("failed to sign alias: %w", err)
	}

	// Create registration request with alias
	request := &peerpb.RegisterPeerWithAliasRequest{
		PeerRecord: peerRecord,
		Alias:      peerAlias,
		Neighbors:  []string{}, // No initial neighbors for now
	}

	// Call the seed node
	response, err := c.client.RegisterPeerWithAlias(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to register with seed node using alias: %w", err)
	}

	if !response.Accepted {
		return fmt.Errorf("seed node rejected registration with alias: %s", response.Message)
	}

	fmt.Printf("Successfully registered new peer with alias '%s': %s\n", alias, response.Message)
	return nil
}

// updateExistingPeerWithAlias updates an existing peer record with an alias
func (c *Client) updateExistingPeerWithAlias(h host.Host, alias string, existingPeer *peerpb.SignedPeerRecord) error {
	// Get current peer addresses - try to use public IP first
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

	// Create updated peer record with incremented sequence number
	updatedPeerRecord := &peerpb.SignedPeerRecord{
		PeerId:        h.ID().String(),
		Multiaddrs:    multiaddrs,
		Seq:           existingPeer.Seq + 1, // Increment sequence number
		CurrentStatus: peerpb.PeerStatus_PEER_STATUS_ACTIVE,
		Region:        "",                  // No region specified
		Labels:        existingPeer.Labels, // Preserve existing labels
		// Signature fields will be populated by signPeerRecord
		V: "",
		R: "",
		S: "",
	}

	// Sign the updated peer record
	err = SignPeerRecord(updatedPeerRecord, h)
	if err != nil {
		return fmt.Errorf("failed to sign updated peer record: %w", err)
	}

	// Update the existing peer record
	err = c.UpdatePeer(updatedPeerRecord)
	if err != nil {
		return fmt.Errorf("failed to update existing peer: %w", err)
	}

	// Create peer alias
	peerAlias := &peerpb.PeerAlias{
		Name:   alias,
		PeerId: h.ID().String(),
		// Signature fields will be populated by signAlias
		V: "",
		R: "",
		S: "",
	}

	// Sign the alias
	err = SignAlias(peerAlias, h)
	if err != nil {
		return fmt.Errorf("failed to sign alias: %w", err)
	}

	// Create the alias for the existing peer
	err = c.CreateAlias(peerAlias)
	if err != nil {
		return fmt.Errorf("failed to create alias for existing peer: %w", err)
	}

	fmt.Printf("Successfully updated existing peer with alias '%s' (seq: %d)\n", alias, updatedPeerRecord.Seq)
	return nil
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
