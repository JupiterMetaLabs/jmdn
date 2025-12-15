package node

import (
	"context"
	"database/sql"
	"fmt"
	"gossipnode/DB_OPs/sqlops"
	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/logging"
	"gossipnode/metrics"

	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
)

const (
	LOG_FILE = "node.log"
	TOPIC    = "node"
)

// NodeManager manages connections to manually specified nodes
type NodeManager struct {
	host            host.Host
	db              *sql.DB
	trackedPeers    map[peer.ID]*ManagedPeer
	heartbeatTicker *time.Ticker
	ctx             context.Context
	cancel          context.CancelFunc
	mutex           sync.RWMutex
	Logger          *logging.AsyncLogger
}

// ManagedPeer represents a peer being manually managed
type ManagedPeer struct {
	ID            peer.ID
	Multiaddr     string
	LastSeen      int64
	HeartbeatFail int
	IsAlive       bool
}

// NodeManager Global Variable to be immported by other packages
var NodeManagerInterface *NodeManager

func setNodeManagerInterface(nodeManager *NodeManager) {
	NodeManagerInterface = nodeManager
}

func GetNodeManagerInterface() *NodeManager {
	return NodeManagerInterface
}

func ClearNodeManagerInterface() {
	NodeManagerInterface = nil
}

// NewNodeManager creates a new node manager for the host
func NewNodeManager(node *config.Node) (*NodeManager, error) {
	return NewNodeManagerWithLoki(node, true)
}

func NewNodeManagerWithLoki(node *config.Node, enableLoki bool) (*NodeManager, error) {
	if node == nil || node.Host == nil {
		return nil, fmt.Errorf("invalid node configuration")
	}

	// Create a DB connection if not provided
	var db *sql.DB
	if node.DB != nil && node.DB.Instance != nil {
		// Try to use the existing DB connection
		var ok bool
		db, ok = node.DB.Instance.(*sql.DB)
		if !ok {
			return nil, fmt.Errorf("invalid database instance type")
		}
	} else {
		// Create a new connection
		var err error
		db, err = sql.Open("sqlite3", config.DBPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}

		// Ping to ensure connection is valid
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, fmt.Errorf("database ping failed: %w", err)
		}

		// Set the DB in the node config
		if node.DB == nil {
			node.DB = &config.UnifiedDB{}
		}
		node.DB.Instance = db
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Set initial metrics
	metricsLogger, err := logging.ReturnDefaultLoggerWithLoki("metrics.log", "metrics", enableLoki)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	metricsLogger.Logger.Info("Initializing Node Manager",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.NewNodeManager"),
	)

	metrics.ManagedPeersGauge.Set(0)
	metrics.ActivePeersGauge.Set(0)
	metrics.ConnectedPeersGauge.Set(float64(len(node.Host.Network().Peers())))

	manager := &NodeManager{
		host:         node.Host,
		db:           db,
		trackedPeers: make(map[peer.ID]*ManagedPeer),
		ctx:          ctx,
		cancel:       cancel,
		mutex:        sync.RWMutex{},
		Logger:       metricsLogger,
	}

	// Set up the connected_peers table
	if err := manager.initConnectedPeersTable(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize connected peers table: %w", err)
	}

	// Load any existing peers
	if err := manager.loadManagedPeers(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load managed peers: %w", err)
	}

	// manager.DisplayDBPeers()

	// Set up heartbeat handler
	metricsLogger.Logger.Info("Node Manager initialized",
		zap.Int("managed_peers", len(manager.trackedPeers)),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.NewNodeManager"),
	)
	node.Host.SetStreamHandler(config.HeartbeatProtocol, manager.handleHeartbeat)

	// Set the NodeManagerInterface
	setNodeManagerInterface(manager)

	return manager, nil
}

// initConnectedPeersTable creates the connected_peers table if it doesn't exist
func (nm *NodeManager) initConnectedPeersTable() error {
	startTime := time.Now().UTC()
	query := fmt.Sprintf(`
    CREATE TABLE IF NOT EXISTS %s (
        peer_id TEXT PRIMARY KEY,
        multiaddr TEXT NOT NULL,
        last_seen INTEGER NOT NULL,
        heartbeat_fail INTEGER DEFAULT 0,
        is_alive BOOLEAN DEFAULT 1
    )`, config.ConnectedPeers)

	_, err := nm.db.Exec(query)
	// Record database operation metrics
	duration := time.Since(startTime).Seconds()
	metrics.DatabaseLatency.WithLabelValues("create_table").Observe(duration)
	if err != nil {
		metrics.DatabaseOperations.WithLabelValues("create_table", "failure").Inc()
		return err
	}
	metrics.DatabaseOperations.WithLabelValues("create_table", "success").Inc()
	return nil
}

// loadManagedPeers loads connected peers from the database
func (nm *NodeManager) loadManagedPeers() error {
	startTime := time.Now().UTC()

	// Create UnifiedDB instance to access the function
	udb := &sqlops.UnifiedDB{DB: nm.db}

	// Call the function to get peers
	peers, err := udb.GetConnectedPeers()
	if err != nil {
		nm.Logger.Logger.Error("Failed to get connected peers",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.loadManagedPeers"),
		)
		metrics.DatabaseOperations.WithLabelValues("select_peers", "failure").Inc()
		return fmt.Errorf("failed to get connected peers: %w", err)
	}
	metrics.DatabaseOperations.WithLabelValues("select_peers", "success").Inc()

	// Now update the in-memory tracked peers
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	loadedCount := 0
	activeCount := 0

	for _, dbPeer := range peers {
		// Use the peer package's Decode function to get a peer.ID
		peerID, err := peer.Decode(dbPeer.PeerID)
		if err != nil {
			nm.Logger.Logger.Warn("Invalid peer ID in database",
				zap.Error(err),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "node.loadManagedPeers"),
			)
			continue
		}

		// Create a managed peer object
		managedPeer := &ManagedPeer{
			ID:            peerID,
			Multiaddr:     dbPeer.Multiaddr,
			LastSeen:      dbPeer.LastSeen,
			HeartbeatFail: dbPeer.HeartbeatFail,
			IsAlive:       dbPeer.IsAlive,
		}

		// Store in tracked peers map
		nm.trackedPeers[peerID] = managedPeer
		loadedCount++

		if dbPeer.IsAlive {
			activeCount++
		}
	}

	// Update metrics
	metrics.ManagedPeersGauge.Set(float64(loadedCount))
	metrics.ActivePeersGauge.Set(float64(activeCount))

	duration := time.Since(startTime).Seconds()
	nm.Logger.Logger.Info("Loaded managed peers from database",
		zap.Int("loaded", loadedCount),
		zap.Int("active", activeCount),
		zap.Float64("duration_seconds", duration),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.loadManagedPeers"),
	)

	fmt.Printf("Loaded %d managed peers from database in %.2f seconds\n", loadedCount, duration)

	return nil
}

func (nm *NodeManager) DisplayDBPeers() {
	// Create UnifiedDB instance to access the function
	udb := &sqlops.UnifiedDB{DB: nm.db}

	// Call the function
	peers, err := udb.GetConnectedPeers()
	if err != nil {
		fmt.Printf("Error getting peers: %v\n", err)
		return
	}

	fmt.Println("Connected Peers:")
	for i, peer := range peers {
		fmt.Printf("  %d. ID: %s, Multiaddr: %s, Last Seen: %s, Alive: %v\n", i+1, peer.PeerID, peer.Multiaddr, time.Unix(peer.LastSeen, 0), peer.IsAlive)
	}
}

// StartHeartbeat starts the heartbeat process for managed peers
func (nm *NodeManager) StartHeartbeat(intervalSeconds int) {
	if intervalSeconds <= 0 {
		intervalSeconds = 300
	}

	interval := time.Duration(intervalSeconds) * time.Second
	nm.heartbeatTicker = time.NewTicker(interval)

	LocalGRO.Go(GRO.NodeThread, func(ctx context.Context) error {
		fmt.Printf("Starting heartbeat process with interval of %v\n", interval)
		for {
			select {
			case <-nm.ctx.Done():
				return nil
			case <-nm.heartbeatTicker.C:
				nm.performHeartbeat()
			}
		}
	})
}

// StopHeartbeat stops the heartbeat process
func (nm *NodeManager) StopHeartbeat() {
	if nm.heartbeatTicker != nil {
		nm.heartbeatTicker.Stop()
	}
}

// AddPeer adds a peer to be managed or reconnects if already managed
func (nm *NodeManager) AddPeer(multiAddr string) error {
	// Parse multiaddress
	addr, err := multiaddr.NewMultiaddr(multiAddr)
	if err != nil {
		return fmt.Errorf("invalid multiaddress: %w", err)
	}

	// Extract peer info from multiaddress
	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return fmt.Errorf("invalid peer address: %w", err)
	}

	// Check if this is a self-connection attempt
	if peerInfo.ID == nm.host.ID() {
		nm.Logger.Logger.Warn("Attempted to add self as peer",
			zap.String("peer_id", peerInfo.ID.String()),
			zap.String("multiaddr", multiAddr),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.AddPeer"),
		)
		return fmt.Errorf("cannot add self as peer: %s", peerInfo.ID)
	}

	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Check if peer already exists
	existingPeer, exists := nm.trackedPeers[peerInfo.ID]

	// If peer exists
	if exists {
		// Check if already connected
		isConnected := nm.host.Network().Connectedness(peerInfo.ID) == network.Connected

		if isConnected && existingPeer.IsAlive {
			// Already connected and marked as alive - nothing to do
			return fmt.Errorf("peer %s is already connected and managed", peerInfo.ID)
		}

		// Peer exists but is not connected or marked offline - attempt to reconnect
		fmt.Printf("Peer %s exists but appears offline. Attempting reconnection...\n", peerInfo.ID)

		// Try to connect immediately
		ctx, cancel := context.WithTimeout(nm.ctx, 10*time.Second)
		defer cancel()

		if err := nm.host.Connect(ctx, *peerInfo); err != nil {
			// Connection attempt failed
			fmt.Printf("Warning: Reconnection attempt to peer %s failed: %v\n", peerInfo.ID, err)
			return fmt.Errorf("reconnection failed: %w", err)
		}

		// Successfully reconnected - update peer status
		now := time.Now().UTC().Unix()
		existingPeer.LastSeen = now
		existingPeer.IsAlive = true
		existingPeer.HeartbeatFail = 0

		// Update in database
		query := fmt.Sprintf(`
        UPDATE %s 
        SET last_seen = ?, heartbeat_fail = ?, is_alive = ?
        WHERE peer_id = ?`, config.ConnectedPeers)

		_, err = nm.db.Exec(query, now, 0, 1, peerInfo.ID.String())
		if err != nil {
			// Database update failed, but connection was successful
			nm.Logger.Logger.Error("Failed to update reconnected peer status",
				zap.Error(err),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "node.AddPeer"),
			)
		}

		metrics.ActivePeersGauge.Inc() // Increment if it was previously marked as inactive

		fmt.Printf("Successfully reconnected to peer: %s\n", peerInfo.ID)
		return nil
	}

	// This is a new peer - add to memory
	now := time.Now().UTC().Unix()
	managedPeer := &ManagedPeer{
		ID:        peerInfo.ID,
		Multiaddr: multiAddr,
		LastSeen:  now,
		IsAlive:   true,
	}
	nm.trackedPeers[peerInfo.ID] = managedPeer

	// Add peer to database
	startTime := time.Now().UTC()

	query := fmt.Sprintf(`
    INSERT INTO %s (peer_id, multiaddr, last_seen, heartbeat_fail, is_alive)
    VALUES (?, ?, ?, ?, ?)`, config.ConnectedPeers)

	_, err = nm.db.Exec(query, peerInfo.ID.String(), multiAddr, now, 0, 1)

	duration := time.Since(startTime).Seconds()
	metrics.DatabaseLatency.WithLabelValues("insert_peer").Observe(duration)

	if err != nil {
		metrics.DatabaseOperations.WithLabelValues("insert_peer", "failure").Inc()
		delete(nm.trackedPeers, peerInfo.ID) // Remove from memory if DB insertion failed
		return fmt.Errorf("failed to store peer in database: %w", err)
	}

	metrics.DatabaseOperations.WithLabelValues("insert_peer", "success").Inc()

	// Update metrics
	metrics.ManagedPeersGauge.Set(float64(len(nm.trackedPeers)))
	metrics.ActivePeersGauge.Inc() // New peer starts as active

	// Try to connect immediately
	ctx, cancel := context.WithTimeout(nm.ctx, 10*time.Second)
	defer cancel()

	if err := nm.host.Connect(ctx, *peerInfo); err != nil {
		fmt.Printf("Warning: Initial connection to peer %s failed: %v\n", peerInfo.ID, err)
		// We still keep the peer in our list for future connection attempts
	} else {
		fmt.Printf("Successfully connected to peer: %s\n", peerInfo.ID)
	}

	return nil
}

// GetHost returns the libp2p host instance for connection verification
func (nm *NodeManager) GetHost() host.Host {
	return nm.host
}

// RemovePeer removes a peer from management
func (nm *NodeManager) RemovePeer(peerIDStr string) error {
	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("invalid peer ID: %w", err)
	}

	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Check if peer exists
	if _, exists := nm.trackedPeers[peerID]; !exists {
		return fmt.Errorf("peer %s not found in managed peers", peerID)
	}

	// Remove from memory
	delete(nm.trackedPeers, peerID)

	// Remove from database
	startTime := time.Now().UTC()
	query := fmt.Sprintf("DELETE FROM %s WHERE peer_id = ?", config.ConnectedPeers)
	_, err = nm.db.Exec(query, peerIDStr)

	// Record database metrics
	duration := time.Since(startTime).Seconds()
	metrics.DatabaseLatency.WithLabelValues("delete_peer").Observe(duration)
	if err != nil {
		metrics.DatabaseOperations.WithLabelValues("delete_peer", "failure").Inc()
		return fmt.Errorf("failed to remove peer from database: %w", err)
	}
	metrics.DatabaseOperations.WithLabelValues("delete_peer", "success").Inc()

	// Update metrics
	metrics.ManagedPeersGauge.Set(float64(len(nm.trackedPeers)))

	// If the peer was active, reduce active count
	if peer, exists := nm.trackedPeers[peerID]; exists && peer.IsAlive {
		metrics.ActivePeersGauge.Dec()
	}

	fmt.Printf("Peer %s removed from management\n", peerID)
	return nil
}

// ListManagedPeers returns the list of managed peers
func (nm *NodeManager) ListManagedPeers() []*ManagedPeer {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	peers := make([]*ManagedPeer, 0, len(nm.trackedPeers))
	for _, peer := range nm.trackedPeers {
		peers = append(peers, peer)
	}
	return peers
}

// UpdatePeerStatus updates the status of a peer in both memory and database
func (nm *NodeManager) UpdatePeerStatus(peerID peer.ID, isAlive bool, failCount int) error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Update in memory
	peer, exists := nm.trackedPeers[peerID]
	if !exists {
		return fmt.Errorf("peer %s not found", peerID)
	}

	wasAlive := peer.IsAlive

	// Update peer status
	now := time.Now().UTC().Unix()
	peer.LastSeen = now
	peer.IsAlive = isAlive
	peer.HeartbeatFail = failCount

	// Update metrics if status changed
	if wasAlive != isAlive {
		if isAlive {
			metrics.ActivePeersGauge.Inc()
		} else {
			metrics.ActivePeersGauge.Dec()
		}
	}

	// Update in database
	startTime := time.Now().UTC()
	query := fmt.Sprintf(`
    UPDATE %s 
    SET last_seen = ?, heartbeat_fail = ?, is_alive = ?
    WHERE peer_id = ?`, config.ConnectedPeers)

	_, err := nm.db.Exec(query, now, failCount, boolToInt(isAlive), peerID.String())

	// Record database metrics
	duration := time.Since(startTime).Seconds()
	metrics.DatabaseLatency.WithLabelValues("update_peer").Observe(duration)
	if err != nil {
		metrics.DatabaseOperations.WithLabelValues("update_peer", "failure").Inc()
		return fmt.Errorf("failed to update peer status: %w", err)
	}
	metrics.DatabaseOperations.WithLabelValues("update_peer", "success").Inc()

	return nil
}

func (nm *NodeManager) handleHeartbeat(stream network.Stream) {
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer()

	nm.Logger.Logger.Debug("Received heartbeat",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.handleHeartbeat"),
	)
	metrics.HeartbeatReceivedCounter.Inc()

	// Read the heartbeat message (optional)
	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	if err != nil {
		nm.Logger.Logger.Error("Error reading heartbeat",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.handleHeartbeat"),
		)
		return
	}
	message := string(buf[:n])
	if message != "HEARTBEAT" {
		nm.Logger.Logger.Debug("Heartbeat message received",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.handleHeartbeat"),
		)

		// Send heartbeat response
		response := "OK\n"
		_, err = stream.Write([]byte(response))
		if err != nil {
			nm.Logger.Logger.Error("Error sending heartbeat response",
				zap.Error(err),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "node.handleHeartbeat"),
			)
			return
		}

		nm.Logger.Logger.Debug("Sent heartbeat response",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.handleHeartbeat"),
		)

		// Update the peer's status if it's one of our managed peers
		nm.mutex.RLock()
		if peer, exists := nm.trackedPeers[remotePeer]; exists {
			nm.mutex.RUnlock()

			now := time.Now().UTC().Unix()
			peer.LastSeen = now
			peer.IsAlive = true
			peer.HeartbeatFail = 0

			// Update in DB
			query := fmt.Sprintf(`
        UPDATE %s 
        SET last_seen = ?, heartbeat_fail = ?, is_alive = ?
        WHERE peer_id = ?`, config.ConnectedPeers)

			_, err = nm.db.Exec(query, now, 0, 1, remotePeer.String())
			if err != nil {
				nm.Logger.Logger.Error("Failed to update peer status in database",
					zap.Error(err),
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Function, "node.handleHeartbeat"),
				)
			} else {
				nm.Logger.Logger.Debug("Updated peer status in database",
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Function, "node.handleHeartbeat"),
				)
			}
		} else {
			nm.mutex.RUnlock()
			nm.Logger.Logger.Debug("Heartbeat from non-managed peer",
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "node.handleHeartbeat"),
			)
		}
	}
}

// sendHeartbeat sends a heartbeat message to a peer

func (nm *NodeManager) sendHeartbeat(peerID peer.ID) (bool, error) {
	metrics.HeartbeatSentCounter.Inc()

	nm.mutex.RLock()
	peers, exists := nm.trackedPeers[peerID]
	nm.mutex.RUnlock()

	if !exists {
		nm.Logger.Logger.Error("Peer not found in managed peers",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, fmt.Errorf("peer %s not found in managed peers", peerID)
	}

	// Parse multiaddress
	addr, err := multiaddr.NewMultiaddr(peers.Multiaddr)
	if err != nil {
		nm.Logger.Logger.Error("Invalid stored multiaddress",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, fmt.Errorf("invalid stored multiaddress: %w", err)
	}

	// Extract peer info
	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		nm.Logger.Logger.Error("Invalid peer info",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, fmt.Errorf("invalid peer info: %w", err)
	}

	// Try to connect if not connected
	ctx, cancel := context.WithTimeout(nm.ctx, 5*time.Second)
	defer cancel()

	nm.Logger.Logger.Debug("Attempting to connect",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.sendHeartbeat"),
	)

	if err := nm.host.Connect(ctx, *peerInfo); err != nil {
		nm.Logger.Logger.Debug("Connection failed",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, err
	}

	// Open a heartbeat stream
	stream, err := nm.host.NewStream(ctx, peerID, config.HeartbeatProtocol)
	if err != nil {
		nm.Logger.Logger.Error("Failed to open heartbeat stream",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, fmt.Errorf("failed to open heartbeat stream: %w", err)
	}
	defer stream.Close()

	// Write a simple heartbeat message
	nm.Logger.Logger.Debug("Sending heartbeat message",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.sendHeartbeat"),
	)
	_, err = stream.Write([]byte("HEARTBEAT\n"))
	if err != nil {
		nm.Logger.Logger.Error("Failed to send heartbeat",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, fmt.Errorf("failed to send heartbeat: %w", err)
	}

	// Wait for response with a timeout
	responseBytes := make([]byte, 16)
	stream.SetReadDeadline(time.Now().UTC().Add(5 * time.Second))

	nm.Logger.Logger.Debug("Waiting for heartbeat response",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.sendHeartbeat"),
	)
	n, err := stream.Read(responseBytes)
	if err != nil {
		nm.Logger.Logger.Error("Failed to read heartbeat response",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, fmt.Errorf("failed to read heartbeat response: %w", err)
	}

	response := string(responseBytes[:n])
	if !strings.Contains(response, "OK") {
		nm.Logger.Logger.Error("Invalid heartbeat response",
			zap.String("response", response),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.sendHeartbeat"),
		)
		return false, fmt.Errorf("invalid heartbeat response: %s", response)
	}

	nm.Logger.Logger.Debug("Valid heartbeat response received",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Function, "node.sendHeartbeat"),
	)
	return true, nil
}

// Update the performHeartbeat function to include auto-removal logic
func (nm *NodeManager) performHeartbeat() {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = InitializeNode()
		if err != nil {
			fmt.Println("Error initializing LocalGRO:", err)
			return
		}
	}
	// Update metrics
	metrics.ConnectedPeersGauge.Set(float64(len(nm.host.Network().Peers())))
	metrics.ManagedPeersGauge.Set(float64(len(nm.trackedPeers)))

	nm.mutex.RLock()
	peers := make([]peer.ID, 0, len(nm.trackedPeers))
	for id := range nm.trackedPeers {
		peers = append(peers, id)
	}
	nm.mutex.RUnlock()

	wg, err := LocalGRO.NewFunctionWaitGroup(context.Background(), GRO.NodeManagerWG)
	if err != nil {
		nm.Logger.Logger.Error("Failed to create waitgroup",
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "node.performHeartbeat"),
		)
		return
	}

	for _, peerID := range peers {
		// Capture peerID in closure to avoid race condition
		peerID := peerID
		LocalGRO.Go(GRO.NodeThread, func(ctx context.Context) error {

			nm.Logger.Logger.Debug("Sending heartbeat",
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "node.performHeartbeat"),
			)

			nm.mutex.RLock()
			peer := nm.trackedPeers[peerID]
			failCount := peer.HeartbeatFail
			nm.mutex.RUnlock()

			nm.Logger.Logger.Debug("Sending heartbeat",
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Function, "node.performHeartbeat"),
			)

			// Record start time for latency measurement
			startTime := time.Now().UTC()
			success, err := nm.sendHeartbeat(peerID)
			latency := time.Since(startTime).Seconds()

			if err != nil {
				failCount++
				nm.Logger.Logger.Warn("Heartbeat failed",
					zap.Int("failures", failCount),
					zap.Error(err),
					zap.Float64("latency_seconds", latency),
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Function, "node.performHeartbeat"),
				)

				metrics.HeartbeatFailedCounter.Inc()
			} else {
				nm.Logger.Logger.Info("Heartbeat successful",
					zap.Float64("latency_seconds", latency),
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Function, "node.performHeartbeat"),
				)

				failCount = 0
				metrics.HeartbeatLatency.WithLabelValues(peerID.String()).Observe(latency)
			}

			// Update peer status
			err = nm.UpdatePeerStatus(peerID, success, failCount)
			if err != nil {
				nm.Logger.Logger.Error("Failed to update peer status",
					zap.Error(err),
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Function, "node.performHeartbeat"),
				)
			}

			// If too many consecutive failures, mark as offline
			if failCount >= config.HeartbeatFailureThreshold {
				nm.Logger.Logger.Warn("Peer marked as offline due to consecutive heartbeat failures",
					zap.Int("failures", failCount),
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Function, "node.performHeartbeat"),
					zap.String("peer", peerID.String()),
				)
			}

			// NEW CODE: Auto-remove peers with excessive failures (9+)
			if failCount >= config.HeartbeatRemovalThreshold {
				nm.Logger.Logger.Warn("Removing peer due to excessive consecutive failures",
					zap.Int("failures", failCount),
					zap.Time(logging.Created_at, time.Now().UTC()),
					zap.String(logging.Log_file, LOG_FILE),
					zap.String(logging.Topic, TOPIC),
					zap.String(logging.Function, "node.performHeartbeat"),
					zap.String("peer", peerID.String()),
				)

				// Remove the peer from management
				if err := nm.RemovePeer(peerID.String()); err != nil {
					nm.Logger.Logger.Error("Failed to remove unreachable peer",
						zap.Error(err),
						zap.Time(logging.Created_at, time.Now().UTC()),
						zap.String(logging.Log_file, LOG_FILE),
						zap.String(logging.Topic, TOPIC),
						zap.String(logging.Function, "node.performHeartbeat"),
					)
				} else {
					nm.Logger.Logger.Info("Peer removed from management after 9 consecutive failures",
						zap.Time(logging.Created_at, time.Now().UTC()),
						zap.String(logging.Log_file, LOG_FILE),
						zap.String(logging.Topic, TOPIC),
						zap.String(logging.Function, "node.performHeartbeat"),
					)

					// Send custom metric for peer removal
					metrics.PeerRemovedCounter.WithLabelValues("excessive_failures").Inc()
				}
			}
			return nil
		}, local.AddToWaitGroup(GRO.NodeManagerWG))
	}

	wg.Wait()

	// Count active peers
	activeCount := 0
	nm.mutex.RLock()
	for _, peer := range nm.trackedPeers {
		if peer.IsAlive {
			activeCount++
		}
	}
	nm.mutex.RUnlock()

	metrics.ActivePeersGauge.Set(float64(activeCount))

}

// Shutdown closes all resources
func (nm *NodeManager) Shutdown() {
	nm.cancel()
	if nm.heartbeatTicker != nil {
		nm.heartbeatTicker.Stop()
	}
	fmt.Println("Node manager shutdown complete")
}

// Utility function to convert bool to int for SQLite
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (nm *NodeManager) CleanupOfflinePeers(minFailures int) (int, error) {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	var removedCount int
	var peersToRemove []peer.ID

	for id, peer := range nm.trackedPeers {
		if peer.HeartbeatFail >= minFailures {
			peersToRemove = append(peersToRemove, id)
		}
	}

	for _, id := range peersToRemove {
		if err := nm.RemovePeer(id.String()); err == nil {
			removedCount++
		}
	}

	return removedCount, nil
}

// CheckMultiaddrReachability verifies whether a multiaddr is reachable
// without performing a libp2p-level connection. It uses a raw TCP dial.
// PingMultiaddrWithRetries performs multiple ping attempts for better reliability
func (nm *NodeManager) PingMultiaddrWithRetries(multiAddr string, attempts int) (bool, time.Duration, error) {
	addr, err := multiaddr.NewMultiaddr(multiAddr)
	if err != nil {
		return false, 0, fmt.Errorf("invalid multiaddress: %w", err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return false, 0, fmt.Errorf("multiaddr missing peer ID: %w", err)
	}

	nm.host.Peerstore().AddAddrs(peerInfo.ID, peerInfo.Addrs, peerstore.TempAddrTTL)

	// Connect first
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := nm.host.Connect(ctx, *peerInfo); err != nil {
		nm.Logger.Logger.Debug("Connection failed",
			zap.String("multiaddr", multiAddr),
			zap.Error(err),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Function, "node.PingMultiaddrWithRetries"),
			zap.String(logging.Topic, TOPIC),
		)
		return false, 0, nil
	}

	// Perform multiple pings
	pingService := ping.NewPingService(nm.host)
	var totalRTT time.Duration
	successCount := 0

	for i := 0; i < attempts; i++ {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
		responseChan := pingService.Ping(pingCtx, peerInfo.ID)

		select {
		case res := <-responseChan:
			if res.Error == nil {
				totalRTT += res.RTT
				successCount++
			}
		case <-pingCtx.Done():
			// Timeout for this attempt
		}
		pingCancel()

		// Small delay between attempts
		if i < attempts-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	if successCount > 0 {
		avgRTT := totalRTT / time.Duration(successCount)
		nm.Logger.Logger.Info("Ping successful",
			zap.String("multiaddr", multiAddr),
			zap.Int("successful_pings", successCount),
			zap.Int("total_attempts", attempts),
			zap.Duration("avg_rtt", avgRTT),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Function, "node.PingMultiaddrWithRetries"),
			zap.String(logging.Topic, TOPIC),
		)
		return true, avgRTT, nil
	}

	nm.Logger.Logger.Debug("All ping attempts failed",
		zap.String("multiaddr", multiAddr),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Function, "node.PingMultiaddrWithRetries"),
		zap.String(logging.Topic, TOPIC),
	)
	return false, 0, nil
}
