package node

import (
	"context"
	"database/sql"
	"fmt"
	"gossipnode/DB_OPs/sqlops"
	"gossipnode/config"
	"gossipnode/logging"
	"gossipnode/metrics"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var logger = logging.GetSubLogger("nodemanager")

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
	logger = logging.GetSubLogger("node_manager")
    logger.Info().Msg("Initializing Node Manager")
    
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

    // manager.DisplayDBPeers()

    // Set up heartbeat handler
	logger.Info().Int("managed_peers", len(manager.trackedPeers)).Msg("Node Manager initialized")
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
    
    // Create UnifiedDB instance to access the function
    udb := &sqlops.UnifiedDB{DB: nm.db}
    
    // Call the function to get peers
    peers, err := udb.GetConnectedPeers()
    if err != nil {
        logger.Error().
            Err(err).
            Msg("Failed to get connected peers")
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
            logger.Warn().
                Err(err).
                Str("peer_id", dbPeer.PeerID).
                Msg("Invalid peer ID in database")
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
    logger.Info().
        Int("loaded", loadedCount).
        Int("active", activeCount).
        Float64("duration_seconds", duration).
        Msg("Loaded managed peers from database")
    
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
        now := time.Now().Unix()
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
            logger.Error().Err(err).Str("peer_id", peerInfo.ID.String()).Msg("Failed to update reconnected peer status")
        }
        
        metrics.ActivePeersGauge.Inc() // Increment if it was previously marked as inactive
        
        fmt.Printf("Successfully reconnected to peer: %s\n", peerInfo.ID)
        return nil
    }

    // This is a new peer - add to memory
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

func (nm *NodeManager) handleHeartbeat(stream network.Stream) {
    defer stream.Close()
    
    remotePeer := stream.Conn().RemotePeer()
    peerLogger := logger.With().Str("remote_peer", remotePeer.String()).Logger()
    
    peerLogger.Debug().Msg("Received heartbeat")
    metrics.HeartbeatReceivedCounter.Inc()

    // Read the heartbeat message (optional)
    buf := make([]byte, 64)
    n, err := stream.Read(buf)
    if err != nil {
        peerLogger.Error().Err(err).Msg("Error reading heartbeat")
        return
    }
    message := string(buf[:n])
    
    peerLogger.Debug().Str("message", message).Msg("Heartbeat message received")

    // Send heartbeat response
    response := "OK\n"
    _, err = stream.Write([]byte(response))
    if err != nil {
        peerLogger.Error().Err(err).Msg("Error sending heartbeat response")
        return
    }
    
    peerLogger.Debug().Str("response", response).Msg("Sent heartbeat response")

    // Update the peer's status if it's one of our managed peers
    nm.mutex.RLock()
    if peer, exists := nm.trackedPeers[remotePeer]; exists {
        nm.mutex.RUnlock()
        
        now := time.Now().Unix()
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
            peerLogger.Error().Err(err).Msg("Failed to update peer status in database")
        } else {
            peerLogger.Debug().Int64("last_seen", now).Msg("Updated peer status in database")
        }
    } else {
        nm.mutex.RUnlock()
        peerLogger.Debug().Msg("Heartbeat from non-managed peer")
    }
}

func (nm *NodeManager) sendHeartbeat(peerID peer.ID) (bool, error) {
    peerLogger := logger.With().Str("peer_id", peerID.String()).Logger()
    metrics.HeartbeatSentCounter.Inc()

    nm.mutex.RLock()
    peers, exists := nm.trackedPeers[peerID]
    nm.mutex.RUnlock()

    if !exists {
        peerLogger.Error().Msg("Peer not found in managed peers")
        return false, fmt.Errorf("peer %s not found in managed peers", peerID)
    }

    // Parse multiaddress
    addr, err := multiaddr.NewMultiaddr(peers.Multiaddr)
    if err != nil {
        peerLogger.Error().Err(err).Str("multiaddr", peers.Multiaddr).Msg("Invalid stored multiaddress")
        return false, fmt.Errorf("invalid stored multiaddress: %w", err)
    }

    // Extract peer info
    peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
    if err != nil {
        peerLogger.Error().Err(err).Str("multiaddr", peers.Multiaddr).Msg("Invalid peer info")
        return false, fmt.Errorf("invalid peer info: %w", err)
    }

    // Try to connect if not connected
    ctx, cancel := context.WithTimeout(nm.ctx, 5*time.Second)
    defer cancel()

    peerLogger.Debug().Msg("Attempting to connect")

    if err := nm.host.Connect(ctx, *peerInfo); err != nil {
        peerLogger.Debug().Err(err).Msg("Connection failed")
        return false, err
    }

    // Open a heartbeat stream
    stream, err := nm.host.NewStream(ctx, peerID, config.HeartbeatProtocol)
    if err != nil {
        peerLogger.Error().Err(err).Str("protocol", string(config.HeartbeatProtocol)).Msg("Failed to open heartbeat stream")
        return false, fmt.Errorf("failed to open heartbeat stream: %w", err)
    }
    defer stream.Close()

    // Write a simple heartbeat message
    peerLogger.Debug().Msg("Sending heartbeat message")
    _, err = stream.Write([]byte("HEARTBEAT\n"))
    if err != nil {
        peerLogger.Error().Err(err).Msg("Failed to send heartbeat")
        return false, fmt.Errorf("failed to send heartbeat: %w", err)
    }

    // Wait for response with a timeout
    responseBytes := make([]byte, 16)
    stream.SetReadDeadline(time.Now().Add(5 * time.Second))

    peerLogger.Debug().Msg("Waiting for heartbeat response")
    n, err := stream.Read(responseBytes)
    if err != nil {
        peerLogger.Error().Err(err).Msg("Failed to read heartbeat response")
        return false, fmt.Errorf("failed to read heartbeat response: %w", err)
    }

    response := string(responseBytes[:n])
    if !strings.Contains(response, "OK") {
        peerLogger.Error().Str("response", response).Msg("Invalid heartbeat response")
        return false, fmt.Errorf("invalid heartbeat response: %s", response)
    }

    peerLogger.Debug().Str("response", response).Msg("Valid heartbeat response received")
    return true, nil
}

// Update the performHeartbeat function to include auto-removal logic
func (nm *NodeManager) performHeartbeat() {
    // logger.Info().
    //     Int("managed_peers", len(nm.trackedPeers)).
    //     Int("connected_peers", len(nm.host.Network().Peers())).
    //     Msg("Starting heartbeat cycle")

    // Update metrics
    metrics.ConnectedPeersGauge.Set(float64(len(nm.host.Network().Peers())))
    metrics.ManagedPeersGauge.Set(float64(len(nm.trackedPeers)))

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

            peerLogger := logger.With().Str("peer_id", id.String()).Logger()

            nm.mutex.RLock()
            peer := nm.trackedPeers[id]
            failCount := peer.HeartbeatFail
            nm.mutex.RUnlock()

            peerLogger.Debug().Msg("Sending heartbeat")

            // Record start time for latency measurement
            startTime := time.Now()
            success, err := nm.sendHeartbeat(id)
            latency := time.Since(startTime).Seconds()

            if err != nil {
                failCount++
                peerLogger.Warn().
                    Int("failures", failCount).
                    Err(err).
                    Float64("latency_seconds", latency).
                    Msg("Heartbeat failed")

                metrics.HeartbeatFailedCounter.Inc()
            } else {
                peerLogger.Info().
                    Float64("latency_seconds", latency).
                    Msg("Heartbeat successful")
                
                failCount = 0
                metrics.HeartbeatLatency.WithLabelValues(id.String()).Observe(latency)
            }

            // Update peer status
            err = nm.UpdatePeerStatus(id, success, failCount)
            if err != nil {
                peerLogger.Error().
                    Err(err).
                    Msg("Failed to update peer status")
            }

            // If too many consecutive failures, mark as offline
			if failCount >= config.HeartbeatFailureThreshold {
				peerLogger.Warn().
					Int("failures", failCount).
					Msg("Peer marked as offline due to consecutive heartbeat failures")
			}
			

            // NEW CODE: Auto-remove peers with excessive failures (9+)
			if failCount >= config.HeartbeatRemovalThreshold {
				peerLogger.Warn().
					Int("failures", failCount).
					Msg("Removing peer due to excessive consecutive failures")
				
                // Remove the peer from management
                if err := nm.RemovePeer(id.String()); err != nil {
                    peerLogger.Error().
                        Err(err).
                        Msg("Failed to remove unreachable peer")
                } else {
                    peerLogger.Info().
                        Msg("Peer removed from management after 9 consecutive failures")
                    
                    // Send custom metric for peer removal
                    metrics.PeerRemovedCounter.WithLabelValues("excessive_failures").Inc()
                }
            }
        }(peerID)
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
    
    // logger.Info().
    //     Int("active_peers", activeCount).
    //     Int("total_peers", len(nm.trackedPeers)).
    //     Msg("Heartbeat cycle completed")
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

