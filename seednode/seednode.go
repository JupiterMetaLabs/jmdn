package seednode

import (
	"context"
	AppContext "gossipnode/config/Context"
	"crypto/tls"
	"fmt"
	peerpb "gossipnode/seednode/proto"
	seednodetypes "gossipnode/seednode/types"
	selection "gossipnode/shared/selection"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

const(
	SeedNodeAppContext = "seednode"
)

// ManagedPeer represents a peer being manually managed (local copy to avoid circular imports)
type ManagedPeer struct {
	ID            peer.ID
	Multiaddr     string
	LastSeen      int64
	HeartbeatFail int
	IsAlive       bool
}

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
func isValidMultiaddr(addr string, currentPeerID peer.ID) bool {
	// Try to parse the multiaddress
	maddr, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return false
	}

	// Extract peer ID from multiaddress
	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return false
	}

	// Check if this is a self-connection attempt
	if peerInfo.ID == currentPeerID {
		fmt.Printf("🚫 Skipping self-connection attempt: %s\n", addr)
		return false
	}

	return peerInfo.ID != ""
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
	// Determine if we need secure or insecure credentials based on the URL
	var creds credentials.TransportCredentials
	if strings.HasPrefix(seedNodeURL, "https://") || strings.Contains(seedNodeURL, ":443") {
		// Use secure credentials for SSL/TLS connections with proper ALPN configuration
		tlsConfig := &tls.Config{
			NextProtos: []string{"h2"}, // Specify HTTP/2 protocol for gRPC
		}
		creds = credentials.NewTLS(tlsConfig)
	} else {
		// Use insecure credentials for non-SSL connections
		creds = insecure.NewCredentials()
	}

	// Create gRPC connection with appropriate credentials
	conn, err := grpc.Dial(seedNodeURL, grpc.WithTransportCredentials(creds))
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

func (c *Client) ListWeightsofPeers() (map[string]float64, error) {
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(15*time.Second)
	defer cancel()

	// Get all peers from the seed node
	peers, err := c.ListPeers(ctx, &peerpb.PeerListRequest{
		Limit:  1000,
		Status: peerpb.PeerStatus_PEER_STATUS_ACTIVE,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list peers: %w", err)
	}

	// Create a map of peer IDs to weights
	weights := make(map[string]float64)
	for _, peer := range peers.Peers {
		weights[peer.PeerId] = float64(peer.Weights)
	}

	return weights, nil
}

// GetPeer retrieves a peer record by peer ID
func (c *Client) GetPeer(peerID string) (*peerpb.SignedPeerRecord, error) {
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	var currentPeerIDs map[string]bool = make(map[string]bool)

	// Get current managed peers from node manager using reflection
	peerCount := 0
	if nodeManager != nil {
		// Use reflection to call ListManagedPeers method
		nodeManagerValue := reflect.ValueOf(nodeManager)
		listMethod := nodeManagerValue.MethodByName("ListManagedPeers")

		if listMethod.IsValid() {
			results := listMethod.Call(nil)
			if len(results) > 0 {
				peersValue := results[0]
				if peersValue.Kind() == reflect.Slice {
					peerCount = peersValue.Len()
					fmt.Printf("✅ Found %d currently managed peers\n", peerCount)

					// Create a map of current peer IDs for quick lookup
					for i := 0; i < peerCount; i++ {
						peerValue := peersValue.Index(i)
						if peerValue.Kind() == reflect.Ptr {
							peerValue = peerValue.Elem()
						}

						// Try to get the ID field
						idField := peerValue.FieldByName("ID")
						if idField.IsValid() {
							// Convert peer.ID to string
							if idField.CanInterface() {
								if peerID, ok := idField.Interface().(peer.ID); ok {
									currentPeerIDs[peerID.String()] = true
								}
							}
						}
					}
				}
			}
		} else {
			fmt.Printf("⚠️  Warning: Node manager does not support ListManagedPeers method\n")
		}
	}

	// Check if we're already at the peer limit (7)
	const MAX_PEERS = 7
	if peerCount >= MAX_PEERS {
		fmt.Printf("⚠️  Already at maximum peer limit (%d). Skipping neighbor discovery.\n", MAX_PEERS)
		return nil
	}

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

	// Step 4: Add neighbors to node manager (only if not already managed and under limit)
	var successfullyAddedPeers []string
	peersAdded := 0

	for _, neighbor := range newNeighbors {
		neighborID := neighbor.NeighborId

		// Check if we've reached the peer limit
		if peerCount+peersAdded >= MAX_PEERS {
			fmt.Printf("⚠️  Reached maximum peer limit (%d). Stopping peer addition.\n", MAX_PEERS)
			break
		}

		// Check if this peer is already managed
		if currentPeerIDs[neighborID] {
			fmt.Printf("⏭️  Skipping peer %s - already managed\n", neighborID)
			continue
		}

		fmt.Printf("🔗 Processing neighbor: %s\n", neighborID)

		// Get the full peer record to get multiaddrs
		peerRecord, err := c.GetPeer(neighborID)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to get peer record for %s: %v\n", neighborID, err)
			continue
		}

		// Add each multiaddr to the node manager
		peerAdded := false
		for _, multiaddr := range peerRecord.Multiaddrs {
			fmt.Printf("  📍 Adding peer: %s\n", multiaddr)

			// Validate the multiaddress before attempting to add it
			if !isValidMultiaddr(multiaddr, h.ID()) {
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
				peerAdded = true
				break // Only add one multiaddr per peer
			} else {
				fmt.Printf("❌ Node manager does not support AddPeer method\n")
				return fmt.Errorf("node manager does not support AddPeer method")
			}
		}

		if peerAdded {
			successfullyAddedPeers = append(successfullyAddedPeers, neighborID)
			peersAdded++
			// Update the current peer IDs map to avoid duplicates in the same run
			currentPeerIDs[neighborID] = true
		}
	}

	// Step 5: Report successfully added peers to seed node
	if len(successfullyAddedPeers) > 0 {
		fmt.Printf("📤 Reporting %d successfully added peers to seed node\n", len(successfullyAddedPeers))
		for _, peerID := range successfullyAddedPeers {
			neighbor := &peerpb.PeerNeighbor{
				PeerId:     h.ID().String(),
				NeighborId: peerID,
				CreatedAt:  time.Now().UTC().Unix(),
				LastSeen:   time.Now().UTC().Unix(),
				IsActive:   true,
				V:          "",
				R:          "",
				S:          "",
			}

			// Sign the neighbor record using the host's private key
			err := SignNeighbor(neighbor, h)
			if err != nil {
				fmt.Printf("⚠️  Warning: Failed to sign neighbor record for %s: %v\n", peerID, err)
				continue
			}

			err = c.AddNeighbor(neighbor)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(5*time.Second)
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
	ctx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)
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
func convertProtoToNode(peer *peerpb.SignedPeerRecord) selection.Node {
	fmt.Println("ℹ️ Converting peer:", peer.PeerId)

	var lastSeen time.Time
	if peer.LastUpdated > 0 {
		lastSeen = time.Unix(peer.LastUpdated, 0)
	} else {
		lastSeen = time.Unix(peer.FirstCreated, 0)
	}

	var address string
	if len(peer.Multiaddrs) > 0 {
		address = peer.Multiaddrs[0]
	}

	// Extract ASN from labels (ASN is stored as string in labels map)
	var asn int
	if peer.Labels != nil && peer.Labels["asn"] != "" {
		if parsedASN, err := strconv.Atoi(peer.Labels["asn"]); err == nil {
			asn = parsedASN
		}
	}

	// Extract additional fields from labels
	var ipPrefix, reachability string
	if peer.Labels != nil {
		ipPrefix = peer.Labels["ip_prefix"]
		reachability = peer.Labels["reachability"]
	}

	// Determine RTT bucket and RTT based on labels
	var rttBucket string
	var rttMs int
	if peer.Labels != nil {
		if peer.Labels["rtt_failure_reason"] != "" {
			rttBucket = "failed"
			rttMs = -1 // Indicate failure
		} else {
			rttBucket = "unknown" // Default when no RTT data available
			rttMs = 0
		}
	}

	isActive := peer.CurrentStatus == peerpb.PeerStatus_PEER_STATUS_ACTIVE &&
		time.Since(lastSeen) <= 10*time.Minute

	capacity := int(peer.Weights * 100)
	if capacity == 0 {
		capacity = 100 // Default capacity
	}

	// Selection score logic with proper defaults
	selectionScore := 0.5 // Default starting score for new nodes
	if peer.Labels != nil && peer.Labels["selection_score"] != "" {
		fmt.Sscanf(peer.Labels["selection_score"], "%f", &selectionScore)
	} else if peer.Weights > 0 {
		selectionScore = float64(peer.Weights)
		// Clamp to valid range [0.5, 1.0)
		if selectionScore < 0.5 {
			selectionScore = 0.5
		}
		if selectionScore >= 1.0 {
			selectionScore = 0.99
		}
	}

	return selection.Node{
		Node: seednodetypes.Node{
			// New structured fields
			PeerId:       peer.PeerId,
			Alias:        "", // Not available in SignedPeerRecord
			Region:       peer.Region,
			ASN:          asn,
			IPPrefix:     ipPrefix,
			Reachability: reachability,
			RTTBucket:    rttBucket,
			RTTMs:        rttMs,
			LastSeen:     lastSeen,
			Multiaddrs:   peer.Multiaddrs,
			// Legacy fields for backward compatibility
			ID:              peer.PeerId,
			Address:         address,
			ReputationScore: float64(peer.Weights),
			IsActive:        isActive,
			Capacity:        capacity,
		},
		SelectionScore: selectionScore, // 0.0 to 1.0 range for selection logic
	}
}

// convertBuddyPeerRecordToNode converts BuddyPeerRecord to selection.Node
func convertBuddyPeerRecordToNode(peer *peerpb.BuddyPeerRecord) selection.Node {
	var lastSeen time.Time
	if peer.LastUpdated > 0 {
		lastSeen = time.Unix(peer.LastUpdated, 0)
	} else {
		lastSeen = time.Unix(peer.FirstCreated, 0)
	}

	var address string
	if len(peer.Multiaddrs) > 0 {
		address = peer.Multiaddrs[0]
	}

	// BuddyPeerRecord doesn't have ASN field, so we'll use a hash-based approach
	// or set to 0 to indicate unknown ASN
	var asn int
	if peer.Region != "" {
		// Convert region to a numeric ASN (simple hash-based approach)
		asnStr := "AS-" + peer.Region
		// Simple hash to convert string to number
		hash := 0
		for _, c := range asnStr {
			hash = hash*31 + int(c)
		}
		asn = hash % 1000000 // Keep it reasonable
	} else {
		asn = 0 // Unknown ASN
	}

	capacity := int(peer.Weights * 100)
	if capacity == 0 {
		capacity = 100
	}

	// Buddy peers get default selection score of 0.8 (high priority)
	selectionScore := 0.8
	if peer.Weights > 0 && peer.Weights < 1.0 {
		selectionScore = float64(peer.Weights)
		// Clamp to valid range [0.5, 1.0)
		if selectionScore < 0.5 {
			selectionScore = 0.5
		}
		if selectionScore >= 1.0 {
			selectionScore = 0.99
		}
	}

	return selection.Node{
		Node: seednodetypes.Node{
			// New structured fields
			PeerId:       peer.PeerId,
			Alias:        peer.Alias, // Available in BuddyPeerRecord
			Region:       peer.Region,
			ASN:          asn,
			IPPrefix:     "",        // Not available in BuddyPeerRecord
			Reachability: "",        // Not available in BuddyPeerRecord
			RTTBucket:    "unknown", // Not available in BuddyPeerRecord
			RTTMs:        0,         // Not available in BuddyPeerRecord
			LastSeen:     lastSeen,
			Multiaddrs:   peer.Multiaddrs,
			// Legacy fields for backward compatibility
			ID:              peer.PeerId,
			Address:         address,
			ReputationScore: float64(peer.Weights),
			IsActive:        true, // Buddy peers are always active
			Capacity:        capacity,
		},
		SelectionScore: selectionScore, // 0.0 to 1.0 range for selection logic
	}
}

func (c *Client) ListBuddy(ctx context.Context) (*peerpb.ListBuddyResponse, error) {
	return c.client.ListBuddy(ctx, &peerpb.ListBuddyRequest{})
}

func (c *Client) UpdateBuddy(ctx context.Context, peerIDs []string) (*peerpb.UpdateBuddyResponse, error) {
	return c.client.UpdateBuddy(ctx, &peerpb.UpdateBuddyRequest{
		PeerIds: peerIDs,
	})
}

// UpdateBuddies updates the buddy list on the peer directory
func (c *Client) UpdateBuddies(ctx context.Context, peerIDs []string) error {
	resp, err := c.UpdateBuddy(ctx, peerIDs)
	if err != nil {
		return fmt.Errorf("failed to update buddies: %w", err)
	}

	if !resp.Accepted {
		return fmt.Errorf("buddy update rejected: %s", resp.Message)
	}

	fmt.Printf("✅ Updated buddy list: added %d, removed %d\n", resp.AddedCount, resp.RemovedCount)
	return nil
}

// Add conversion functions
func (c *Client) ListBuddyPeers(ctx context.Context) ([]selection.Node, error) {
	resp, err := c.ListBuddy(ctx)
	if err != nil {
		return nil, err
	}

	nodes := make([]selection.Node, 0, len(resp.Peers))
	for _, peer := range resp.Peers {
		node := convertBuddyPeerRecordToNode(peer)
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (c *Client) ListAllPeers(ctx context.Context) ([]selection.Node, error) {
	allNodes := make([]selection.Node, 0, 100)
	var afterPeerID string
	limit := int32(100)
	totalFetched := 0

	for {
		reqCtx, cancel := AppContext.GetAppContext(SeedNodeAppContext).NewChildContextWithTimeout(10*time.Second)

		req := &peerpb.PeerListRequest{
			AfterPeerId: afterPeerID,
			Limit:       limit,
			Status:      peerpb.PeerStatus_PEER_STATUS_ACTIVE,
		}

		fmt.Printf("🔍 Fetching peers (after: %s, limit: %d)...\n", afterPeerID, limit)

		resp, err := c.client.ListPeers(reqCtx, req)
		cancel()

		if err != nil {
			return nil, fmt.Errorf("failed to list peers: %w", err)
		}

		batchSize := len(resp.Peers)
		totalFetched += batchSize

		// Convert proto peers to Node type
		for _, peer := range resp.Peers {
			node := convertProtoToNode(peer)
			allNodes = append(allNodes, node)
		}

		// Check if there are more peers to fetch
		if !resp.HasMore {
			fmt.Println("✅ No more peers to fetch")
			break
		}

		afterPeerID = resp.Last
	}

	return allNodes, nil
}

func (c *Client) RemoveAllBuddies(ctx context.Context) error {
	resp, err := c.client.RemoveAllBuddies(ctx, &peerpb.RemoveAllBuddiesRequest{})
	if err != nil {
		return fmt.Errorf("failed to remove all buddies: %w", err)
	}

	if !resp.Accepted {
		return fmt.Errorf("buddy removal rejected: %s", resp.Message)
	}

	return nil
}
