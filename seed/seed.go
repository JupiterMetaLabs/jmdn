package seed

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	database "gossipnode/DB_OPs/sqlops"
	"gossipnode/config"
	"gossipnode/config/GRO"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/time/rate"
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

// ── Per-peer registration rate limiter ───────────────────────────────────────
// Prevents Sybil attacks: each peer ID is allowed at most 5 registrations per
// hour. The limiter map is cleaned up lazily on access (entries older than 2h
// are evicted). This is an in-memory guard; it resets on process restart.
//
// Rate: 5 tokens/hour = ~1 token per 720 seconds, burst of 5.
const (
	registrationRate  = rate.Every(720 * time.Second) // 5/hour
	registrationBurst = 5
	limiterTTL        = 2 * time.Hour
)

type limiterEntry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

var (
	peerLimitersMu sync.Mutex
	peerLimiters   = make(map[string]*limiterEntry)
)

// getOrCreateLimiter returns a rate.Limiter for the given peer ID, evicting
// stale entries older than limiterTTL to bound memory usage.
func getOrCreateLimiter(peerID string) *rate.Limiter {
	peerLimitersMu.Lock()
	defer peerLimitersMu.Unlock()

	now := time.Now()

	// Lazy eviction: remove entries not seen in > limiterTTL.
	for id, entry := range peerLimiters {
		if now.Sub(entry.lastSeen) > limiterTTL {
			delete(peerLimiters, id)
		}
	}

	entry, ok := peerLimiters[peerID]
	if !ok {
		entry = &limiterEntry{lim: rate.NewLimiter(registrationRate, registrationBurst)}
		peerLimiters[peerID] = entry
	}
	entry.lastSeen = now
	return entry.lim
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

	log.Printf("[seed] node registered as seed node")
	return nil
}

// handleSeedRequest handles general seed node requests
func handleSeedRequest(stream network.Stream, node *config.Node) {
	defer stream.Close()

	reader := bufio.NewReader(stream)
	message, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("[seed] error reading seed request from %s: %v", stream.Conn().RemotePeer(), err)
		return
	}

	// Log at Debug level only — message is peer-supplied and must not be logged at Info
	// to avoid inadvertently recording attacker-controlled data in structured logs.
	log.Printf("[seed] seed request received (peer=%s, len=%d)", stream.Conn().RemotePeer(), len(message))

	_, err = stream.Write([]byte("Seed node received request\n"))
	if err != nil {
		log.Printf("[seed] error sending seed response to %s: %v", stream.Conn().RemotePeer(), err)
	}
}

// handlePeerDiscoveryRequest processes requests for peer discovery
func handlePeerDiscoveryRequest(stream network.Stream, node *config.Node) {
	defer stream.Close()

	if !node.IsSeed {
		stream.Write([]byte("{\"error\": \"Not a seed node\"}\n"))
		return
	}

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
		log.Printf("[seed] error updating peer %s in database: %v", remotePeer, err)
	}

	var request PeerRequest
	decoder := json.NewDecoder(stream)
	if err := decoder.Decode(&request); err != nil {
		log.Printf("[seed] error decoding peer request from %s: %v", remotePeer, err)
		stream.Write([]byte("{\"error\": \"Invalid request format\"}\n"))
		return
	}

	maxPeers := request.MaxPeers
	if maxPeers <= 0 || maxPeers > config.MaxTrackedPeers {
		maxPeers = 20
	}

	peerAddrs, err := db.GetPeers(6, maxPeers)
	if err != nil {
		log.Printf("[seed] error retrieving peers for %s: %v", remotePeer, err)
		stream.Write([]byte("{\"error\": \"Failed to retrieve peers\"}\n"))
		return
	}

	peers := make([]config.PeerInfo, 0, len(peerAddrs))
	for _, addr := range peerAddrs {
		if strings.Contains(addr, remotePeer.String()) {
			continue
		}

		parts := strings.Split(addr, "/p2p/")
		if len(parts) < 2 {
			continue
		}

		peerIDStr := parts[1]
		peerID, err := peer.Decode(peerIDStr)
		if err != nil {
			continue
		}

		if request.Type != "" {
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

	response := PeerResponse{Peers: peers}
	encoder := json.NewEncoder(stream)
	if err := encoder.Encode(response); err != nil {
		log.Printf("[seed] error encoding peer response for %s: %v", remotePeer, err)
	}
}

// handleRegisterRequest handles peer registration with per-peer rate limiting
// and a registry size cap to prevent Sybil attacks.
func handleRegisterRequest(stream network.Stream, node *config.Node) {
	defer stream.Close()

	if !node.IsSeed {
		stream.Write([]byte("{\"error\": \"Not a seed node\"}\n"))
		return
	}

	remotePeer := stream.Conn().RemotePeer()
	remoteAddr := stream.Conn().RemoteMultiaddr()

	// ── Rate limit: max 5 registrations per hour per peer ID ─────────────────
	lim := getOrCreateLimiter(remotePeer.String())
	if !lim.Allow() {
		log.Printf("[seed] registration rate limit exceeded for peer %s — rejecting", remotePeer)
		stream.Write([]byte("{\"error\": \"Rate limit exceeded — try again later\"}\n"))
		return
	}

	// ── Registry size cap: reject when at capacity ────────────────────────────
	node.PeerStore.Mutex.RLock()
	peerCount := len(node.PeerStore.Peers)
	node.PeerStore.Mutex.RUnlock()
	if peerCount >= config.MaxTrackedPeers {
		log.Printf("[seed] registry full (%d/%d) — rejecting registration for %s",
			peerCount, config.MaxTrackedPeers, remotePeer)
		stream.Write([]byte("{\"error\": \"Seed registry full\"}\n"))
		return
	}

	fullAddr := fmt.Sprintf("%s/p2p/%s", remoteAddr.String(), remotePeer.String())

	db := node.DB.Instance.(*database.UnifiedDB)
	err := db.AddPeer(remotePeer.String(), fullAddr, 0, nil)
	if err != nil {
		log.Printf("[seed] error registering peer %s: %v", remotePeer, err)
		stream.Write([]byte("{\"error\": \"Registration failed\"}\n"))
		return
	}

	peers, err := db.GetPeers(6, 4)
	if err != nil {
		log.Printf("[seed] error getting bootstrap peers for %s: %v", remotePeer, err)
		stream.Write([]byte("{\"error\": \"Failed to retrieve peers\"}\n"))
		return
	}

	response := "REGISTERED|"
	if len(peers) > 0 {
		response += strings.Join(peers, ",")
	}

	_, err = stream.Write([]byte(response + "\n"))
	if err != nil {
		log.Printf("[seed] error sending registration response to %s: %v", remotePeer, err)
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
			return
		}

		now := time.Now().UTC().Unix()

		node.PeerStore.Mutex.Lock()
		for id, info := range node.PeerStore.Peers {
			if now-info.LastSeen > config.PeerTTL {
				delete(node.PeerStore.Peers, id)
			}
		}
		node.PeerStore.LastCleanup = now
		node.PeerStore.Mutex.Unlock()

		log.Printf("[seed] peer cleanup completed")
	}
}

// RequestPeers asks a seed node for available peers
func RequestPeers(h host.Host, seedAddr string, maxPeers int, peerType string) ([]config.PeerInfo, error) {
	addr, err := peer.AddrInfoFromString(seedAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid seed address: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Connect(ctx, *addr); err != nil {
		return nil, fmt.Errorf("failed to connect to seed node: %v", err)
	}

	stream, err := h.NewStream(ctx, addr.ID, config.PeerDiscoveryProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open peer discovery stream: %v", err)
	}
	defer stream.Close()

	request := PeerRequest{
		MaxPeers: maxPeers,
		Type:     peerType,
	}

	encoder := json.NewEncoder(stream)
	if err := encoder.Encode(request); err != nil {
		return nil, fmt.Errorf("failed to send peer request: %v", err)
	}

	var response PeerResponse
	decoder := json.NewDecoder(stream)
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to read peer response: %v", err)
	}

	return response.Peers, nil
}
