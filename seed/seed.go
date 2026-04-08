package seed

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	database "gossipnode/DB_OPs/sqlops"
	"gossipnode/config"
	"gossipnode/config/GRO"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerRequest represents a request for peer information
type PeerRequest struct {
	MaxPeers int    `json:"max_peers"`
	Type     string `json:"type,omitempty"` // Filter by peer type/capability
}

// PeerResponse contains information about peers
type PeerResponse struct {
	Peers []config.PeerInfo `json:"peers"`
}

// NewPeerRegistry creates a new peer registry
func NewPeerRegistry() *config.PeerRegistry {
	return &config.PeerRegistry{
		Peers:       make(map[peer.ID]*config.PeerInfo),
		LastCleanup: time.Now().UTC().Unix(),
	}
}

// RegisterAsSeed configures the node as a seed node
func RegisterAsSeed(node *config.Node) error {
	// Initialize peer store if not already initialized
	if node.PeerStore == nil {
		node.PeerStore = NewPeerRegistry()
	}

	// Ensure we have a database connection
	if node.DB == nil {
		db, err := database.NewUnifiedDB()
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		node.DB = &config.UnifiedDB{Instance: db}
	}

	node.IsSeed = true

	// Set up handlers for seed node protocols
	node.Host.SetStreamHandler(config.SeedProtocol,
		func(s network.Stream) {
			handleSeedRequest(s, node)
		})

	node.Host.SetStreamHandler(config.PeerDiscoveryProtocol,
		func(s network.Stream) {
			handlePeerDiscoveryRequest(s, node)
		})

	node.Host.SetStreamHandler(config.RegisterProtocol,
		func(s network.Stream) {
			handleRegisterRequest(s, node)
		})

	// Start routine to cleanup stale peers
	LocalGRO.Go(GRO.SeedLocal, func(ctx context.Context) error {
		runPeerCleanup(node)
		return nil
	})

	fmt.Println("Node registered as seed node")
	return nil
}

// handleSeedRequest handles general seed node requests
func handleSeedRequest(stream network.Stream, node *config.Node) {
	// Basic handler for seed protocol
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			fmt.Printf("Error closing stream: %v\n", closeErr)
		}
	}()

	// Read the request
	reader := bufio.NewReader(stream)
	message, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("Error reading seed request: %v\n", err)
		return
	}

	fmt.Println("Received seed request:", message)

	// For now, just acknowledge receipt
	_, err = stream.Write([]byte("Seed node received request\n"))
	if err != nil {
		fmt.Printf("Error sending response: %v\n", err)
	}
}

// handlePeerDiscoveryRequest processes requests for peer discovery
func handlePeerDiscoveryRequest(stream network.Stream, node *config.Node) {
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			fmt.Printf("Error closing stream: %v\n", closeErr)
		}
	}()

	if !node.IsSeed {
		stream.Write([]byte("{\"error\": \"Not a seed node\"}\n"))
		return
	}

	// Track this peer in our registry
	remotePeer := stream.Conn().RemotePeer()
	addrs := stream.Conn().RemoteMultiaddr().String()

	// Update peer registry in memory
	node.PeerStore.Mutex.Lock()
	node.PeerStore.Peers[remotePeer] = &config.PeerInfo{
		ID:       remotePeer,
		Addrs:    []string{addrs},
		LastSeen: time.Now().UTC().Unix(),
	}
	node.PeerStore.Mutex.Unlock()

	// Update peer in database
	db := node.DB.Instance.(*database.UnifiedDB)
	err := db.AddPeer(remotePeer.String(), addrs, 0, nil)
	if err != nil {
		fmt.Printf("Error updating peer in database: %v\n", err)
	}

	// Read the peer request
	var request PeerRequest
	decoder := json.NewDecoder(stream)
	if err := decoder.Decode(&request); err != nil {
		fmt.Printf("Error decoding peer request: %v\n", err)
		stream.Write([]byte("{\"error\": \"Invalid request format\"}\n"))
		return
	}

	// Prepare response with peer information
	maxPeers := request.MaxPeers
	if maxPeers <= 0 || maxPeers > config.MaxTrackedPeers {
		maxPeers = 20 // Default to 20 peers
	}

	// Get peers from database
	peerAddrs, err := db.GetPeers(6, maxPeers)
	if err != nil {
		fmt.Printf("Error retrieving peers: %v\n", err)
		stream.Write([]byte("{\"error\": \"Failed to retrieve peers\"}\n"))
		return
	}

	// Convert to PeerInfo objects
	peers := make([]config.PeerInfo, 0, len(peerAddrs))
	for _, addr := range peerAddrs {
		// Skip the requesting peer
		if strings.Contains(addr, remotePeer.String()) {
			continue
		}

		// Parse multiaddr to extract peer ID
		parts := strings.Split(addr, "/p2p/")
		if len(parts) < 2 {
			continue
		}

		peerIDStr := parts[1]
		peerID, err := peer.Decode(peerIDStr)
		if err != nil {
			continue
		}

		// Apply type filter if specified
		if request.Type != "" {
			// Get the peer's capabilities from the database
			_, _, capabilities, _, err := db.GetPeer(peerID.String())
			if err != nil || !containsCapability(capabilities, request.Type) {
				continue
			}
		}

		peers = append(peers, config.PeerInfo{
			ID:    peerID,
			Addrs: []string{addr},
		})

		if len(peers) >= maxPeers {
			break
		}
	}

	// Send the response
	response := PeerResponse{Peers: peers}
	encoder := json.NewEncoder(stream)
	if err := encoder.Encode(response); err != nil {
		fmt.Printf("Error encoding peer response: %v\n", err)
	}
}

// handleRegisterRequest handles peer registration
func handleRegisterRequest(stream network.Stream, node *config.Node) {
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			fmt.Printf("Error closing stream: %v\n", closeErr)
		}
	}()

	if !node.IsSeed {
		stream.Write([]byte("{\"error\": \"Not a seed node\"}\n"))
		return
	}

	// Get the peer's info
	remotePeer := stream.Conn().RemotePeer()
	remoteAddr := stream.Conn().RemoteMultiaddr()

	// Create a full multiaddress for this peer
	fullAddr := fmt.Sprintf("%s/p2p/%s", remoteAddr.String(), remotePeer.String())

	// Add the peer to the database
	db := node.DB.Instance.(*database.UnifiedDB)
	err := db.AddPeer(remotePeer.String(), fullAddr, 0, nil)
	if err != nil {
		fmt.Printf("Error registering peer %s: %v\n", remotePeer, err)
		stream.Write([]byte("{\"error\": \"Registration failed\"}\n"))
		return
	}

	// Send back acknowledgment with 4 random peers
	peers, err := db.GetPeers(6, 4)
	if err != nil {
		fmt.Printf("Error getting peers for %s: %v\n", remotePeer, err)
		stream.Write([]byte("{\"error\": \"Failed to retrieve peers\"}\n"))
		return
	}

	// Format response
	response := "REGISTERED|"
	if len(peers) > 0 {
		response += strings.Join(peers, ",")
	}

	_, err = stream.Write([]byte(response + "\n"))
	if err != nil {
		fmt.Printf("Error sending registration response to %s: %v\n", remotePeer, err)
	}
}

// containsCapability checks if a capability is in the capabilities slice
func containsCapability(capabilities []string, capability string) bool {
	for _, c := range capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// runPeerCleanup periodically removes stale peers
func runPeerCleanup(node *config.Node) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		<-ticker.C
		if !node.IsSeed || node.DB == nil {
			return // Stop if no longer a seed node
		}

		now := time.Now().UTC().Unix()

		// Clean up the in-memory store
		node.PeerStore.Mutex.Lock()
		for id, info := range node.PeerStore.Peers {
			if now-info.LastSeen > config.PeerTTL {
				delete(node.PeerStore.Peers, id)
			}
		}
		node.PeerStore.LastCleanup = now
		node.PeerStore.Mutex.Unlock()

		// Also clean up peers in the database (could ping them first)
		// This would require more implementation but shows how you would use it
		fmt.Println("Peer cleanup completed")
	}
}

// RequestPeers asks a seed node for available peers
func RequestPeers(h host.Host, seedAddr string, maxPeers int, peerType string) ([]config.PeerInfo, error) {
	// Parse the seed node address
	addr, err := peer.AddrInfoFromString(seedAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid seed address: %v", err)
	}

	// Connect to the seed node
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Connect(ctx, *addr); err != nil {
		return nil, fmt.Errorf("failed to connect to seed node: %v", err)
	}

	// Open a stream for peer discovery
	stream, err := h.NewStream(ctx, addr.ID, config.PeerDiscoveryProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open peer discovery stream: %v", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			fmt.Printf("Error closing stream: %v\n", closeErr)
		}
	}()

	// Send request
	request := PeerRequest{
		MaxPeers: maxPeers,
		Type:     peerType,
	}

	encoder := json.NewEncoder(stream)
	if err := encoder.Encode(request); err != nil {
		return nil, fmt.Errorf("failed to send peer request: %v", err)
	}

	// Read response
	var response PeerResponse
	decoder := json.NewDecoder(stream)
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to read peer response: %v", err)
	}

	return response.Peers, nil
}
