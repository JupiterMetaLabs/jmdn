package node

import (
    "context"
    "database/sql"
    "fmt"
    "gossipnode/config"
	"gossipnode/metrics"
    "strings"
    "sync"
    "time"

    "github.com/libp2p/go-libp2p/core/host"
    "github.com/libp2p/go-libp2p/core/network"
    "github.com/libp2p/go-libp2p/core/peer"
    "github.com/multiformats/go-multiaddr"
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
}

// ManagedPeer represents a peer being manually managed
type ManagedPeer struct {
    ID            peer.ID
    Multiaddr     string
    LastSeen      int64
    HeartbeatFail int
    IsAlive       bool
}

// NewNodeManager creates a new node manager for the host
func NewNodeManager(node *config.Node) (*NodeManager, error) {
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

    // Set up heartbeat handler
    node.Host.SetStreamHandler(config.HeartbeatProtocol, manager.handleHeartbeat)

    return manager, nil
}

// initConnectedPeersTable creates the connected_peers table if it doesn't exist
func (nm *NodeManager) initConnectedPeersTable() error {
	startTime := time.Now()
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
    startTime := time.Now()
    nm.mutex.Lock()
    defer nm.mutex.Unlock()

    query := fmt.Sprintf("SELECT peer_id, multiaddr, last_seen, heartbeat_fail, is_alive FROM %s", config.ConnectedPeers)
    rows, err := nm.db.Query(query)
    
    // Record database metrics
    duration := time.Since(startTime).Seconds()
    metrics.DatabaseLatency.WithLabelValues("select_peers").Observe(duration)
    if err != nil {
        metrics.DatabaseOperations.WithLabelValues("select_peers", "failure").Inc()
        return err
    }
    metrics.DatabaseOperations.WithLabelValues("select_peers", "success").Inc()
    
    defer rows.Close()

    loadedCount := 0
    activeCount := 0
    for rows.Next() {
        var peerIDStr string
        var managedPeer ManagedPeer
        var isAlive int

        if err := rows.Scan(&peerIDStr, &managedPeer.Multiaddr, &managedPeer.LastSeen, &managedPeer.HeartbeatFail, &isAlive); err != nil {
            fmt.Printf("Error scanning peer row: %v\n", err)
            continue
        }

        // Use the peer package's Decode function
        peerID, err := peer.Decode(peerIDStr)
        if err != nil {
            fmt.Printf("Warning: Invalid peer ID in database: %s\n", peerIDStr)
            continue
        }

        managedPeer.ID = peerID
        managedPeer.IsAlive = isAlive == 1
        nm.trackedPeers[peerID] = &managedPeer
        loadedCount++
        
        if managedPeer.IsAlive {
            activeCount++
        }
    }

    // Update metrics
    metrics.ManagedPeersGauge.Set(float64(loadedCount))
    metrics.ActivePeersGauge.Set(float64(activeCount))

    fmt.Printf("Loaded %d managed peers from database\n", loadedCount)
    return nil
}

// StartHeartbeat starts the heartbeat process for managed peers
func (nm *NodeManager) StartHeartbeat(intervalSeconds int) {
    if intervalSeconds <= 0 {
        intervalSeconds = 300 // Default to 5 minutes
    }

    interval := time.Duration(intervalSeconds) * time.Second
    nm.heartbeatTicker = time.NewTicker(interval)

    go func() {
        fmt.Printf("Starting heartbeat process with interval of %v\n", interval)
        for {
            select {
            case <-nm.ctx.Done():
                return
            case <-nm.heartbeatTicker.C:
                nm.performHeartbeat()
            }
        }
    }()
}

// StopHeartbeat stops the heartbeat process
func (nm *NodeManager) StopHeartbeat() {
    if nm.heartbeatTicker != nil {
        nm.heartbeatTicker.Stop()
    }
}

// AddPeer adds a peer to be managed
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

    nm.mutex.Lock()
    defer nm.mutex.Unlock()

    // Check if peer already exists
    if _, exists := nm.trackedPeers[peerInfo.ID]; exists {
        return fmt.Errorf("peer %s already exists in managed peers", peerInfo.ID)
    }

    // Add peer to memory
    now := time.Now().Unix()
    managedPeer := &ManagedPeer{
        ID:        peerInfo.ID,
        Multiaddr: multiAddr,
        LastSeen:  now,
        IsAlive:   true,
    }
    nm.trackedPeers[peerInfo.ID] = managedPeer

    // Add peer to database
	startTime := time.Now()

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
	startTime := time.Now()
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
    now := time.Now().Unix()
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
    startTime := time.Now()
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

// sendHeartbeat sends a heartbeat to a specific peer
func (nm *NodeManager) sendHeartbeat(peerID peer.ID) (bool, error) {
    nm.mutex.RLock()
    peers, exists := nm.trackedPeers[peerID]
    nm.mutex.RUnlock()

    if !exists {
        return false, fmt.Errorf("peer %s not found in managed peers", peerID)
    }

	// Increase sent counter
	metrics.HeartbeatSentCounter.Inc()
	startTime := time.Now()

    // Parse multiaddress
    addr, err := multiaddr.NewMultiaddr(peers.Multiaddr)
    if err != nil {
        return false, fmt.Errorf("invalid stored multiaddress: %w", err)
    }

    // Extract peer info
    peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
    if err != nil {
        return false, fmt.Errorf("invalid peer info: %w", err)
    }

    // Try to connect if not connected
    ctx, cancel := context.WithTimeout(nm.ctx, 5*time.Second)
    defer cancel()

    if err := nm.host.Connect(ctx, *peerInfo); err != nil {
        fmt.Printf("Failed to connect to peer %s: %v\n", peerID, err)
        return false, err
    }

    // Open a heartbeat stream
    stream, err := nm.host.NewStream(ctx, peerID, config.HeartbeatProtocol)
    if err != nil {
        metrics.HeartbeatFailedCounter.Inc()
        return false, fmt.Errorf("failed to open heartbeat stream: %w", err)
    }
    defer stream.Close()

    // Write a simple heartbeat message
    _, err = stream.Write([]byte("HEARTBEAT\n"))
    if err != nil {
        metrics.HeartbeatFailedCounter.Inc()
        return false, fmt.Errorf("failed to send heartbeat: %w", err)
    }

    // Wait for response with a timeout
    responseBytes := make([]byte, 16)
    stream.SetReadDeadline(time.Now().Add(5 * time.Second))

    n, err := stream.Read(responseBytes)
    if err != nil {
        metrics.HeartbeatFailedCounter.Inc()
        return false, fmt.Errorf("failed to read heartbeat response: %w", err)
    }

    // Record heartbeat latency
    duration := time.Since(startTime).Seconds()
    metrics.HeartbeatLatency.WithLabelValues(peerID.String()).Observe(duration)

    response := string(responseBytes[:n])
    if !strings.Contains(response, "OK") {
        metrics.HeartbeatFailedCounter.Inc()
        return false, fmt.Errorf("invalid heartbeat response: %s", response)
    }

    return true, nil
}

// handleHeartbeat processes incoming heartbeat requests
func (nm *NodeManager) handleHeartbeat(stream network.Stream) {
    defer stream.Close()

    // Get the peer's info
    remotePeer := stream.Conn().RemotePeer()

    // Increment received counter
    metrics.HeartbeatReceivedCounter.Inc()

    // Read the heartbeat message (optional)
    buf := make([]byte, 64)
    _, err := stream.Read(buf)
    if err != nil {
        fmt.Printf("Error reading heartbeat from %s: %v\n", remotePeer, err)
        return
    }

    // Send heartbeat response
    _, err = stream.Write([]byte("OK\n"))
    if err != nil {
        fmt.Printf("Error sending heartbeat response to %s: %v\n", remotePeer, err)
        return
    }

    // Update the peer's status if it's one of our managed peers
    nm.mutex.RLock()
    if peer, exists := nm.trackedPeers[remotePeer]; exists {
        nm.mutex.RUnlock()
        peer.LastSeen = time.Now().Unix()
        peer.IsAlive = true
        peer.HeartbeatFail = 0

        // Update in DB
        query := fmt.Sprintf(`
        UPDATE %s 
        SET last_seen = ?, heartbeat_fail = ?, is_alive = ?
        WHERE peer_id = ?`, config.ConnectedPeers)

        _, err = nm.db.Exec(query, peer.LastSeen, 0, 1, remotePeer.String())
        if err != nil {
            fmt.Printf("Failed to update peer %s status: %v\n", remotePeer, err)
        }
    } else {
        nm.mutex.RUnlock()
    }

    fmt.Printf("Received heartbeat from peer: %s\n", remotePeer)
}

// performHeartbeat sends heartbeats to all managed peers
func (nm *NodeManager) performHeartbeat() {
    fmt.Println("Starting heartbeat cycle to all managed peers...")

    metrics.ConnectedPeersGauge.Set(float64(len(nm.host.Network().Peers())))

    nm.mutex.RLock()
    peers := make([]peer.ID, 0, len(nm.trackedPeers))
    for id := range nm.trackedPeers {
        peers = append(peers, id)
    }
    nm.mutex.RUnlock()

    var wg sync.WaitGroup
    for _, peerID := range peers {
        wg.Add(1)
        go func(id peer.ID) {
            defer wg.Done()

            nm.mutex.RLock()
            peer := nm.trackedPeers[id]
            failCount := peer.HeartbeatFail
            nm.mutex.RUnlock()

            fmt.Printf("Sending heartbeat to %s...\n", id)
            success, err := nm.sendHeartbeat(id)

            if err != nil {
                failCount++
                fmt.Printf("Heartbeat to %s failed (%d consecutive failures): %v\n", id, failCount, err)
            } else {
                fmt.Printf("Heartbeat to %s successful\n", id)
                failCount = 0
            }

            // Update peer status
            err = nm.UpdatePeerStatus(id, success, failCount)
            if err != nil {
                fmt.Printf("Failed to update status for peer %s: %v\n", id, err)
            }

            // If too many consecutive failures, mark as offline
            if failCount >= 3 {
                fmt.Printf("Peer %s has failed %d consecutive heartbeats, marking as offline\n", id, failCount)
            }
        }(peerID)
    }

    wg.Wait()
    fmt.Println("Heartbeat cycle completed")
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