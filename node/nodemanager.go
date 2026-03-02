package node

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gossipnode/DB_OPs/sqlops"
	"gossipnode/config"
	"gossipnode/config/GRO"
	log "gossipnode/logging"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"
	"go.opentelemetry.io/otel/attribute"
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
	Logger          *log.Logging
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
var (
	NodeManagerInterface *NodeManager
	nodeManagerMutex     sync.RWMutex
	nodeManagerOnce      sync.Once
)

func setNodeManagerInterface(nodeManager *NodeManager) {
	nodeManagerMutex.Lock()
	defer nodeManagerMutex.Unlock()
	NodeManagerInterface = nodeManager
}

func GetNodeManagerInterface() *NodeManager {
	nodeManagerMutex.RLock()
	defer nodeManagerMutex.RUnlock()
	return NodeManagerInterface
}

func ClearNodeManagerInterface() {
	nodeManagerMutex.Lock()
	defer nodeManagerMutex.Unlock()
	NodeManagerInterface = nil
}

// Zero allocation logger - its already allocated in the asynclogger
func logger() *log.Logging {
	logger, err := log.NewAsyncLogger().Get().NamedLogger(log.MessagePassing_StructService, "")
	if err != nil {
		return nil
	}
	return logger
}

// NewNodeManager creates a new node manager for the host
func NewNodeManager(node *config.Node) (*NodeManager, error) {
	return NewNodeManagerWithLogger(node)
}

func NewNodeManagerWithLogger(node *config.Node) (*NodeManager, error) {
	// Singleton check: if NodeManager already exists, return it
	nodeManagerMutex.RLock()
	if NodeManagerInterface != nil {
		existing := NodeManagerInterface
		nodeManagerMutex.RUnlock()
		return existing, nil
	}
	nodeManagerMutex.RUnlock()

	// Use sync.Once to ensure only one NodeManager is created
	var manager *NodeManager
	var initErr error
	nodeManagerOnce.Do(func() {
		manager, initErr = createNodeManager(node)
		if initErr == nil && manager != nil {
			setNodeManagerInterface(manager)
		}
	})

	if initErr != nil {
		// Reset once on error so it can be retried
		nodeManagerOnce = sync.Once{}
		return nil, initErr
	}

	// If once didn't execute (shouldn't happen), check if interface was set
	nodeManagerMutex.RLock()
	defer nodeManagerMutex.RUnlock()
	if NodeManagerInterface != nil {
		return NodeManagerInterface, nil
	}

	return nil, errors.New("failed to create node manager")
}

func createNodeManager(node *config.Node) (*NodeManager, error) {
	if node == nil || node.Host == nil {
		return nil, errors.New("invalid node configuration")
	}

	// Create a DB connection if not provided
	var db *sql.DB
	if node.DB != nil && node.DB.Instance != nil {
		// Try to use the existing DB connection
		var ok bool
		db, ok = node.DB.Instance.(*sql.DB)
		if !ok {
			return nil, errors.New("invalid database instance type")
		}
	} else {
		// Create a new connection
		var err error
		db, err = sql.Open("sqlite3", config.DBPath)
		if err != nil {
			return nil, errors.New("failed to open database: " + err.Error())
		}

		// Ping to ensure connection is valid
		if err := db.Ping(); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				fmt.Printf("Failed to close node manager DB on ping error: %v\n", closeErr)
			}
			return nil, errors.New("database ping failed: " + err.Error())
		}

		// Set the DB in the node config
		if node.DB == nil {
			node.DB = &config.UnifiedDB{}
		}
		node.DB.Instance = db
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Set initial metrics logger using new Ion-based logger
	metricsLogger := logger()

	// Create logger context for tracing
	metricsLogger.NamedLogger.Info(ctx, "Initializing Node Manager",
		ion.String("connection_database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.NewNodeManager"),
	)

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
	if err := manager.initConnectedPeersTable(ctx); err != nil {
		cancel()
		return nil, errors.New("failed to initialize connected peers table: " + err.Error())
	}

	// Load any existing peers
	if err := manager.loadManagedPeers(ctx); err != nil {
		cancel()
		return nil, errors.New("failed to load managed peers: " + err.Error())
	}

	// manager.DisplayDBPeers()

	// Set up heartbeat handler
	metricsLogger.NamedLogger.Info(ctx, "Node Manager initialized",
		ion.Int("managed_peers", len(manager.trackedPeers)),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.NewNodeManager"),
	)
	node.Host.SetStreamHandler(config.HeartbeatProtocol, manager.handleHeartbeat)

	// NodeManagerInterface is set in NewNodeManagerWithLogger via sync.Once
	return manager, nil
}

// initConnectedPeersTable creates the connected_peers table if it doesn't exist
func (nm *NodeManager) initConnectedPeersTable(logger_ctx context.Context) error {

	// record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(logger_ctx, "NodeManager.initConnectedPeersTable")
	defer span.End()

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

	span.SetAttributes(attribute.Float64("duration", duration))

	if err != nil {
		span.RecordError(err)
		return err
	}
	span.SetAttributes(attribute.String("status", "success"))
	logger().NamedLogger.Info(span_ctx, "Connected peers table created",
		ion.Float64("duration", duration),
		ion.String("status", "success"),
	)
	return nil
}

// loadManagedPeers loads connected peers from the database
func (nm *NodeManager) loadManagedPeers(logger_ctx context.Context) error {

	// record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(logger_ctx, "NodeManager.loadManagedPeers")
	defer span.End()

	startTime := time.Now().UTC()

	// Create UnifiedDB instance to access the function
	udb := &sqlops.UnifiedDB{DB: nm.db}

	// Call the function to get peers
	peers, err := udb.GetConnectedPeers()
	if err != nil {
		logger().NamedLogger.Error(span_ctx, "Failed to get connected peers",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.loadManagedPeers"),
		)
		return errors.New("failed to get connected peers: " + err.Error())
	}

	// Now update the in-memory tracked peers
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	loadedCount := 0
	activeCount := 0

	for _, dbPeer := range peers {
		// Use the peer package's Decode function to get a peer.ID
		peerID, err := peer.Decode(dbPeer.PeerID)
		if err != nil {
			logger().NamedLogger.Warn(span_ctx, "Invalid peer ID in database",
				ion.String("error", err.Error()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.loadManagedPeers"),
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

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))
	logger().NamedLogger.Info(span_ctx, "Loaded managed peers from database",
		ion.Int("loaded", loadedCount),
		ion.Int("active", activeCount),
		ion.Float64("duration_seconds", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("function", "node.loadManagedPeers"),
	)

	return nil
}

// StartHeartbeat starts the heartbeat process for managed peers
func (nm *NodeManager) StartHeartbeat(intervalSeconds int) {
	if intervalSeconds <= 0 {
		intervalSeconds = 300
	}

	// record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(nm.ctx, "NodeManager.StartHeartbeat")
	defer span.End()

	interval := time.Duration(intervalSeconds) * time.Second
	nm.heartbeatTicker = time.NewTicker(interval)

	LocalGRO.Go(GRO.NodeThread, func(ctx context.Context) error {
		logger().NamedLogger.Debug(span_ctx, "Starting heartbeat process with interval of %v",
			ion.Int("interval", intervalSeconds),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("function", "node.StartHeartbeat"),
		)
		for {
			select {
			case <-nm.ctx.Done():
				return nil
			case <-nm.heartbeatTicker.C:
				nm.performHeartbeat(span_ctx)
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
	// Record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(nm.ctx, "NodeManager.AddPeer")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("multiaddr", multiAddr))

	// Parse multiaddress
	addr, err := multiaddr.NewMultiaddr(multiAddr)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		return errors.New("invalid multiaddress: " + err.Error())
	}

	// Extract peer info from multiaddress
	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		return errors.New("invalid peer address: " + err.Error())
	}

	span.SetAttributes(attribute.String("peer_id", peerInfo.ID.String()))

	// Check if this is a self-connection attempt
	if peerInfo.ID == nm.host.ID() {
		span.SetAttributes(attribute.String("status", "error"), attribute.String("error_type", "self_connection"))
		logger().NamedLogger.Warn(span_ctx, "Attempted to add self as peer",
			ion.String("peer_id", peerInfo.ID.String()),
			ion.String("multiaddr", multiAddr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.AddPeer"),
		)
		return errors.New("cannot add self as peer: " + peerInfo.ID.String())
	}

	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Check if peer already exists
	existingPeer, exists := nm.trackedPeers[peerInfo.ID]

	// If peer exists
	if exists {
		span.SetAttributes(attribute.String("operation", "reconnect"), attribute.Bool("peer_exists", true))
		// Check if already connected
		isConnected := nm.host.Network().Connectedness(peerInfo.ID) == network.Connected

		if isConnected && existingPeer.IsAlive {
			// Already connected and marked as alive - nothing to do
			span.SetAttributes(attribute.String("status", "already_connected"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return fmt.Errorf("peer %s is already connected and managed", peerInfo.ID)
		}

		// Peer exists but is not connected or marked offline - attempt to reconnect
		span.SetAttributes(attribute.Bool("is_connected", isConnected), attribute.Bool("is_alive", existingPeer.IsAlive))

		// Try to connect immediately
		connectCtx, cancel := context.WithTimeout(span_ctx, 10*time.Second)
		defer cancel()
		connectStart := time.Now().UTC()

		if err := nm.host.Connect(connectCtx, *peerInfo); err != nil {
			// Connection attempt failed
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "reconnection_failed"))
			span.SetAttributes(attribute.Float64("connection_duration", time.Since(connectStart).Seconds()))
			logger().NamedLogger.Warn(span_ctx, "Reconnection attempt to peer failed",
				ion.String("peer_id", peerInfo.ID.String()),
				ion.String("error", err.Error()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.AddPeer"),
			)
			return errors.New("reconnection failed: " + err.Error())
		}

		// Successfully reconnected - update peer status
		now := time.Now().UTC().Unix()
		existingPeer.LastSeen = now
		existingPeer.IsAlive = true
		existingPeer.HeartbeatFail = 0

		span.SetAttributes(attribute.Float64("connection_duration", time.Since(connectStart).Seconds()))

		// Update in database
		query := fmt.Sprintf(`
        UPDATE %s 
        SET last_seen = ?, heartbeat_fail = ?, is_alive = ?
        WHERE peer_id = ?`, config.ConnectedPeers)

		_, err = nm.db.Exec(query, now, 0, 1, peerInfo.ID.String())
		if err != nil {
			// Database update failed, but connection was successful
			span.RecordError(err)
			logger().NamedLogger.Error(span_ctx, "Failed to update reconnected peer status",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.AddPeer"),
			)
		}

		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "reconnected"))
		logger().NamedLogger.Info(span_ctx, "Successfully reconnected to peer",
			ion.String("peer_id", peerInfo.ID.String()),
			ion.String("multiaddr", multiAddr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.AddPeer"),
		)
		return nil
	}

	// This is a new peer - add to memory
	span.SetAttributes(attribute.String("operation", "add_new_peer"), attribute.Bool("peer_exists", false))
	now := time.Now().UTC().Unix()
	managedPeer := &ManagedPeer{
		ID:        peerInfo.ID,
		Multiaddr: multiAddr,
		LastSeen:  now,
		IsAlive:   true,
	}
	nm.trackedPeers[peerInfo.ID] = managedPeer

	query := fmt.Sprintf(`
    INSERT INTO %s (peer_id, multiaddr, last_seen, heartbeat_fail, is_alive)
    VALUES (?, ?, ?, ?, ?)`, config.ConnectedPeers)

	_, err = nm.db.Exec(query, peerInfo.ID.String(), multiAddr, now, 0, 1)

	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "database_error"))
		logger().NamedLogger.Error(span_ctx, "Failed to store peer in database",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.AddPeer"),
		)
		delete(nm.trackedPeers, peerInfo.ID) // Remove from memory if DB insertion failed
		return fmt.Errorf("failed to store peer in database: %w", err)
	}

	// Try to connect immediately
	connectCtx, cancel := context.WithTimeout(span_ctx, 10*time.Second)
	defer cancel()
	connectStart := time.Now().UTC()
	if err := nm.host.Connect(connectCtx, *peerInfo); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"), attribute.Float64("connection_duration", time.Since(connectStart).Seconds()))
		logger().NamedLogger.Warn(span_ctx, "Initial connection to peer failed",
			ion.String("peer_id", peerInfo.ID.String()),
			ion.Float64("duration", time.Since(connectStart).Seconds()),
			ion.String("multiaddr", multiAddr),
			ion.String("error", err.Error()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.AddPeer"),
		)
		// We still keep the peer in our list for future connection attempts
	} else {
		span.SetAttributes(attribute.Float64("connection_duration", time.Since(connectStart).Seconds()))
		logger().NamedLogger.Info(span_ctx, "Successfully connected to peer",
			ion.String("peer_id", peerInfo.ID.String()),
			ion.Float64("duration", time.Since(connectStart).Seconds()),
			ion.String("multiaddr", multiAddr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.AddPeer"),
		)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	return nil
}

// GetHost returns the libp2p host instance for connection verification
func (nm *NodeManager) GetHost() host.Host {
	return nm.host
}

// RemovePeer removes a peer from management
func (nm *NodeManager) RemovePeer(peerIDStr string) error {
	// Record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(nm.ctx, "NodeManager.RemovePeer")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("peer_id_str", peerIDStr))

	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		return errors.New("invalid peer ID: " + err.Error())
	}

	span.SetAttributes(attribute.String("peer_id", peerID.String()))

	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Check if peer exists
	existingPeer, exists := nm.trackedPeers[peerID]
	if !exists {
		span.SetAttributes(attribute.String("status", "not_found"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return errors.New("peer " + peerID.String() + " not found in managed peers")
	}

	span.SetAttributes(attribute.Bool("peer_exists", true), attribute.Bool("was_alive", existingPeer.IsAlive))

	// Remove from memory
	delete(nm.trackedPeers, peerID)

	// Remove from database
	query := fmt.Sprintf("DELETE FROM %s WHERE peer_id = ?", config.ConnectedPeers)
	_, err = nm.db.Exec(query, peerIDStr)

	// Record database metrics
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "database_error"))
		logger().NamedLogger.Error(span_ctx, "Failed to remove peer from database",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.RemovePeer"),
		)
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return errors.New("failed to remove peer from database: " + err.Error())
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Info(span_ctx, "Peer removed from management",
		ion.String("peer_id", peerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.Float64("duration", duration),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.RemovePeer"),
	)

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
		return errors.New("peer " + peerID.String() + " not found")
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
		return errors.New("failed to update peer status: " + err.Error())
	}
	metrics.DatabaseOperations.WithLabelValues("update_peer", "success").Inc()

	return nil
}

func (nm *NodeManager) handleHeartbeat(stream network.Stream) {
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			logger().NamedLogger.Error(context.Background(), "Failed to close heartbeat stream", closeErr)
		}
	}()

	// Record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(nm.ctx, "NodeManager.handleHeartbeat")
	defer span.End()

	startTime := time.Now().UTC()
	remotePeer := stream.Conn().RemotePeer()
	span.SetAttributes(attribute.String("remote_peer_id", remotePeer.String()))

	logger().NamedLogger.Debug(span_ctx, "Received heartbeat",
		ion.String("remote_peer_id", remotePeer.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.handleHeartbeat"),
	)
	metrics.HeartbeatReceivedCounter.Inc()

	// Read the heartbeat message (optional)
	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "read_error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Error reading heartbeat",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.handleHeartbeat"),
		)
		return
	}
	message := string(buf[:n])
	span.SetAttributes(attribute.String("heartbeat_message", message))

	if message != "HEARTBEAT" {
		span.SetAttributes(attribute.String("message_status", "invalid"))
		logger().NamedLogger.Debug(span_ctx, "Heartbeat message received",
			ion.String("message", message),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.handleHeartbeat"),
		)

		// Send heartbeat response
		response := "OK\n"
		_, err = stream.Write([]byte(response))
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "write_error"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			logger().NamedLogger.Error(span_ctx, "Error sending heartbeat response",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.handleHeartbeat"),
			)
			return
		}

		span.SetAttributes(attribute.String("response_sent", response))
		logger().NamedLogger.Debug(span_ctx, "Sent heartbeat response",
			ion.String("response", response),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.handleHeartbeat"),
		)

		// Update the peer's status if it's one of our managed peers
		nm.mutex.RLock()
		existingPeer, exists := nm.trackedPeers[remotePeer]
		nm.mutex.RUnlock()

		span.SetAttributes(attribute.Bool("is_managed_peer", exists))

		if exists {
			now := time.Now().UTC().Unix()
			existingPeer.LastSeen = now
			existingPeer.IsAlive = true
			existingPeer.HeartbeatFail = 0

			span.SetAttributes(attribute.Bool("was_alive", existingPeer.IsAlive))

			// Update in DB
			query := fmt.Sprintf(`
        UPDATE %s 
        SET last_seen = ?, heartbeat_fail = ?, is_alive = ?
        WHERE peer_id = ?`, config.ConnectedPeers)

			_, err = nm.db.Exec(query, now, 0, 1, remotePeer.String())
			if err != nil {
				span.RecordError(err)
				span.SetAttributes(attribute.String("db_update_status", "failed"))
				logger().NamedLogger.Error(span_ctx, "Failed to update peer status in database",
					err,
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "node.handleHeartbeat"),
				)
			} else {
				span.SetAttributes(attribute.String("db_update_status", "success"))
				logger().NamedLogger.Debug(span_ctx, "Updated peer status in database",
					ion.String("peer_id", remotePeer.String()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "node.handleHeartbeat"),
				)
			}
		} else {
			logger().NamedLogger.Debug(span_ctx, "Heartbeat from non-managed peer",
				ion.String("peer_id", remotePeer.String()),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.handleHeartbeat"),
			)
		}
	} else {
		span.SetAttributes(attribute.String("message_status", "valid"))
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// sendHeartbeat sends a heartbeat message to a peer

func (nm *NodeManager) sendHeartbeat(peerID peer.ID) (bool, error) {
	// Record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(nm.ctx, "NodeManager.sendHeartbeat")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("peer_id", peerID.String()))
	metrics.HeartbeatSentCounter.Inc()

	nm.mutex.RLock()
	peers, exists := nm.trackedPeers[peerID]
	nm.mutex.RUnlock()

	span.SetAttributes(attribute.Bool("peer_exists", exists))

	if !exists {
		span.RecordError(fmt.Errorf("peer %s not found in managed peers", peerID))
		span.SetAttributes(attribute.String("status", "peer_not_found"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Peer not found in managed peers",
			fmt.Errorf("peer %s not found in managed peers", peerID),
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, errors.New("peer " + peerID.String() + " not found in managed peers")
	}

	span.SetAttributes(attribute.String("multiaddr", peers.Multiaddr))

	// Parse multiaddress
	addr, err := multiaddr.NewMultiaddr(peers.Multiaddr)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "invalid_multiaddr"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Invalid stored multiaddress",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("multiaddr", peers.Multiaddr),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, errors.New("invalid stored multiaddress: " + err.Error())
	}

	// Extract peer info
	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "invalid_peer_info"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Invalid peer info",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, errors.New("invalid peer info: " + err.Error())
	}

	// Try to connect if not connected
	connectCtx, cancel := context.WithTimeout(span_ctx, 5*time.Second)
	defer cancel()
	connectStart := time.Now().UTC()

	logger().NamedLogger.Debug(span_ctx, "Attempting to connect",
		ion.String("peer_id", peerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.sendHeartbeat"),
	)

	if err := nm.host.Connect(connectCtx, *peerInfo); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "connection_failed"), attribute.Float64("connection_duration", time.Since(connectStart).Seconds()))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Debug(span_ctx, "Connection failed",
			ion.String("peer_id", peerID.String()),
			ion.String("error", err.Error()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, err
	}

	span.SetAttributes(attribute.Float64("connection_duration", time.Since(connectStart).Seconds()))

	// Open a heartbeat stream
	stream, err := nm.host.NewStream(connectCtx, peerID, config.HeartbeatProtocol)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "stream_open_failed"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Failed to open heartbeat stream",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, errors.New("failed to open heartbeat stream: " + err.Error())
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			logger().NamedLogger.Error(span_ctx, "Failed to close send heartbeat stream", closeErr)
		}
	}()

	span.SetAttributes(attribute.String("stream_opened", "true"))

	// Write a simple heartbeat message
	writeStart := time.Now().UTC()
	logger().NamedLogger.Debug(span_ctx, "Sending heartbeat message",
		ion.String("peer_id", peerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.sendHeartbeat"),
	)
	_, err = stream.Write([]byte("HEARTBEAT\n"))
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "write_failed"), attribute.Float64("write_duration", time.Since(writeStart).Seconds()))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Failed to send heartbeat",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, errors.New("failed to send heartbeat: " + err.Error())
	}

	span.SetAttributes(attribute.Float64("write_duration", time.Since(writeStart).Seconds()))

	// Wait for response with a timeout
	responseBytes := make([]byte, 16)
	stream.SetReadDeadline(time.Now().UTC().Add(5 * time.Second))
	readStart := time.Now().UTC()

	logger().NamedLogger.Debug(span_ctx, "Waiting for heartbeat response",
		ion.String("peer_id", peerID.String()),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.sendHeartbeat"),
	)
	n, err := stream.Read(responseBytes)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "read_failed"), attribute.Float64("read_duration", time.Since(readStart).Seconds()))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Failed to read heartbeat response",
			err,
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, errors.New("failed to read heartbeat response: " + err.Error())
	}

	span.SetAttributes(attribute.Float64("read_duration", time.Since(readStart).Seconds()))

	response := string(responseBytes[:n])
	span.SetAttributes(attribute.String("response", response))
	if !strings.Contains(response, "OK") {
		span.RecordError(errors.New("invalid heartbeat response: " + response))
		span.SetAttributes(attribute.String("status", "invalid_response"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		logger().NamedLogger.Error(span_ctx, "Invalid heartbeat response",
			errors.New("invalid heartbeat response: "+response),
			ion.String("response", response),
			ion.String("peer_id", peerID.String()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.sendHeartbeat"),
		)
		return false, errors.New("invalid heartbeat response: " + response)
	}

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))
	logger().NamedLogger.Debug(span_ctx, "Valid heartbeat response received",
		ion.String("peer_id", peerID.String()),
		ion.String("response", response),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.sendHeartbeat"),
	)
	return true, nil
}

// Update the performHeartbeat function to include auto-removal logic
func (nm *NodeManager) performHeartbeat(logger_ctx context.Context) {
	// Record trace span and close it
	span_ctx, span := logger().NamedLogger.Tracer("NodeManager").Start(logger_ctx, "NodeManager.performHeartbeat")
	defer span.End()

	startTime := time.Now().UTC()

	if LocalGRO == nil {
		var err error
		LocalGRO, err = InitializeNode()
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(attribute.String("status", "initialization_error"))
			logger().NamedLogger.Error(span_ctx, "Error initializing LocalGRO",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.performHeartbeat"),
			)
			return
		}
	}
	// Update metrics
	connectedPeersCount := len(nm.host.Network().Peers())
	managedPeersCount := len(nm.trackedPeers)
	metrics.ConnectedPeersGauge.Set(float64(connectedPeersCount))
	metrics.ManagedPeersGauge.Set(float64(managedPeersCount))

	span.SetAttributes(attribute.Int("connected_peers", connectedPeersCount), attribute.Int("managed_peers", managedPeersCount))

	nm.mutex.RLock()
	peers := make([]peer.ID, 0, len(nm.trackedPeers))
	for id := range nm.trackedPeers {
		peers = append(peers, id)
	}
	nm.mutex.RUnlock()

	span.SetAttributes(attribute.Int("peers_to_check", len(peers)))

	wg, err := LocalGRO.NewFunctionWaitGroup(context.Background(), GRO.NodeManagerWG)
	if err != nil {
		logger().NamedLogger.Error(logger_ctx, "Failed to create waitgroup",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.performHeartbeat"),
		)
		return
	}

	for _, peerID := range peers {
		// Capture peerID in closure to avoid race condition
		peerID := peerID
		LocalGRO.Go(GRO.NodeThread, func(ctx context.Context) error {

			logger().NamedLogger.Debug(logger_ctx, "Sending heartbeat",
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.performHeartbeat"),
			)

			nm.mutex.RLock()
			peer := nm.trackedPeers[peerID]
			failCount := peer.HeartbeatFail
			nm.mutex.RUnlock()

			logger().NamedLogger.Debug(logger_ctx, "Sending heartbeat",
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "node.performHeartbeat"),
			)

			// Record start time for latency measurement
			startTime := time.Now().UTC()
			success, err := nm.sendHeartbeat(peerID)
			latency := time.Since(startTime).Seconds()

			if err != nil {
				failCount++
				// logger().NamedLogger.Warn(logger_ctx, "Heartbeat failed",
				// 	ion.String("error", err.Error()),
				// 	ion.Int("failures", failCount),
				// 	ion.Float64("latency_seconds", latency),
				// 	ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				// 	ion.String("log_file", LOG_FILE),
				// 	ion.String("topic", TOPIC),
				// 	ion.String("function", "node.performHeartbeat"),
				// )

			} else {
				// logger().NamedLogger.Info(span_ctx, "Heartbeat successful",
				// 	ion.String("peer_id", peerID.String()),
				// 	ion.Float64("latency_seconds", latency),
				// 	ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				// 	ion.String("log_file", LOG_FILE),
				// 	ion.String("topic", TOPIC),
				// 	ion.String("function", "node.performHeartbeat"),
				// )

				failCount = 0
				metrics.HeartbeatLatency.WithLabelValues(peerID.String()).Observe(latency)
			}

			// Update peer status
			err = nm.UpdatePeerStatus(peerID, success, failCount)
			if err != nil {
				logger().NamedLogger.Error(span_ctx, "Failed to update peer status",
					err,
					ion.String("peer_id", peerID.String()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "node.performHeartbeat"),
				)
			}

			// If too many consecutive failures, mark as offline
			if failCount >= config.HeartbeatFailureThreshold {
				logger().NamedLogger.Warn(span_ctx, "Peer marked as offline due to consecutive heartbeat failures",
					ion.String("peer_id", peerID.String()),
					ion.Int("failures", failCount),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "node.performHeartbeat"),
				)
			}

			// NEW CODE: Auto-remove peers with excessive failures (9+)
			if failCount >= config.HeartbeatRemovalThreshold {
				logger().NamedLogger.Warn(span_ctx, "Removing peer due to excessive consecutive failures",
					ion.String("peer_id", peerID.String()),
					ion.Int("failures", failCount),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "node.performHeartbeat"),
				)

				// Remove the peer from management
				if err := nm.RemovePeer(peerID.String()); err != nil {
					logger().NamedLogger.Error(span_ctx, "Failed to remove unreachable peer",
						err,
						ion.String("peer_id", peerID.String()),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "node.performHeartbeat"),
					)
				} else {
					logger().NamedLogger.Info(span_ctx, "Peer removed from management after 9 consecutive failures",
						ion.String("peer_id", peerID.String()),
						ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
						ion.String("log_file", LOG_FILE),
						ion.String("topic", TOPIC),
						ion.String("function", "node.performHeartbeat"),
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

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Int("active_peers", activeCount), attribute.Float64("duration", duration), attribute.String("status", "success"))
}

// Shutdown closes all resources
func (nm *NodeManager) Shutdown() {
	nm.cancel()
	if nm.heartbeatTicker != nil {
		nm.heartbeatTicker.Stop()
	}
	logger().NamedLogger.Info(nm.ctx, "Node manager shutdown complete",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.Shutdown"),
	)
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
		return false, 0, errors.New("invalid multiaddress: " + err.Error())
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return false, 0, errors.New("multiaddr missing peer ID: " + err.Error())
	}

	nm.host.Peerstore().AddAddrs(peerInfo.ID, peerInfo.Addrs, peerstore.TempAddrTTL)

	// Connect first
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := nm.host.Connect(ctx, *peerInfo); err != nil {
		logger().NamedLogger.Debug(nm.ctx, "Connection failed",
			ion.String("multiaddr", multiAddr),
			ion.String("error", err.Error()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.PingMultiaddrWithRetries"),
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
		logger().NamedLogger.Info(nm.ctx, "Ping successful",
			ion.String("multiaddr", multiAddr),
			ion.Int("successful_pings", successCount),
			ion.Int("total_attempts", attempts),
			ion.Float64("avg_rtt", avgRTT.Seconds()),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "node.PingMultiaddrWithRetries"),
		)
		return true, avgRTT, nil
	}

	logger().NamedLogger.Debug(nm.ctx, "All ping attempts failed",
		ion.String("multiaddr", multiAddr),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "node.PingMultiaddrWithRetries"),
	)
	return false, 0, nil
}
