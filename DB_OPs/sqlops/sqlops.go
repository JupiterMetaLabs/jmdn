package sqlops

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gossipnode/config"
)

// UnifiedDB is a wrapper for SQLite database
type UnifiedDB struct {
	DB    *sql.DB
	mutex sync.RWMutex
}

// NewUnifiedDB creates a new database connection
func NewUnifiedDB() (*UnifiedDB, error) {
	// Ensure the database directory exists
	dbDir := filepath.Dir(config.DBPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database connection
	db, err := sql.Open("sqlite3", config.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Check connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("database ping failed: %w", err)
	}

	// Initialize the database schema
	if err := initializeSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return &UnifiedDB{
		DB:    db,
		mutex: sync.RWMutex{},
	}, nil
}

// initializeSchema creates the necessary tables
func initializeSchema(db *sql.DB) error {
	// Create the peers table
	createPeersTable := fmt.Sprintf(`
    CREATE TABLE IF NOT EXISTS %s (
        peerID TEXT PRIMARY KEY,
        publicaddr TEXT NOT NULL,
        connections INTEGER DEFAULT 0,
        last_seen INTEGER DEFAULT 0,
        capabilities TEXT
    );`, config.PeersTable)

	if _, err := db.Exec(createPeersTable); err != nil {
		return fmt.Errorf("failed to create peers table: %w", err)
	}

	// Create the key-value table
	createKeyValueTable := fmt.Sprintf(`
    CREATE TABLE IF NOT EXISTS %s (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL,
        timestamp INTEGER DEFAULT 0
    );`, config.KeyValueTable)

	if _, err := db.Exec(createKeyValueTable); err != nil {
		return fmt.Errorf("failed to create key-value table: %w", err)
	}

	// Create the merkle hashes table
	createMerkleTable := fmt.Sprintf(`
    CREATE TABLE IF NOT EXISTS %s (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        key TEXT NOT NULL,
        value_hash TEXT NOT NULL,
        timestamp INTEGER DEFAULT 0
    );`, config.MerkleTable)

	if _, err := db.Exec(createMerkleTable); err != nil {
		return fmt.Errorf("failed to create merkle table: %w", err)
	}

	return nil
}

// Close closes the database connection
func (u *UnifiedDB) Close() error {
	if u.DB != nil {
		return u.DB.Close()
	}
	return nil
}

// AddPeer adds or updates a peer in the database
func (u *UnifiedDB) AddPeer(peerID, publicAddr string, connections int, capabilities []string) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	// Convert capabilities slice to comma-separated string
	capsStr := ""
	if len(capabilities) > 0 {
		for i, cap := range capabilities {
			if i > 0 {
				capsStr += ","
			}
			capsStr += cap
		}
	}

	query := fmt.Sprintf(`
        INSERT INTO %s (peerID, publicaddr, connections, last_seen, capabilities)
        VALUES (?, ?, ?, strftime('%%s','now'), ?)
        ON CONFLICT(peerID) DO UPDATE SET
            publicaddr = ?,
            connections = ?,
            last_seen = strftime('%%s','now'),
            capabilities = ?
    `, config.PeersTable)

	_, err := u.DB.Exec(query, peerID, publicAddr, connections, capsStr,
		publicAddr, connections, capsStr)
	return err
}

// GetPeer retrieves a peer from the database
func (u *UnifiedDB) GetPeer(peerID string) (string, int, []string, int64, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`SELECT publicaddr, connections, capabilities, last_seen FROM %s WHERE peerID = ?`,
		config.PeersTable)

	var publicAddr string
	var connections int
	var capsStr string
	var lastSeen int64

	err := u.DB.QueryRow(query, peerID).Scan(&publicAddr, &connections, &capsStr, &lastSeen)
	if err != nil {
		return "", 0, nil, 0, err
	}

	// Convert comma-separated string back to slice
	var capabilities []string
	if capsStr != "" {
		capabilities = strings.Split(capsStr, ",")
	}

	return publicAddr, connections, capabilities, lastSeen, nil
}

// DeletePeer removes a peer from the database
func (u *UnifiedDB) DeletePeer(peerID string) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	query := fmt.Sprintf(`DELETE FROM %s WHERE peerID = ?`, config.PeersTable)
	result, err := u.DB.Exec(query, peerID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return fmt.Errorf("peer %s not found", peerID)
	}

	return nil
}

// GetPeers returns a list of peers with fewer than maxConnections
func (u *UnifiedDB) GetPeers(maxConnections int, limit int) ([]string, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`
        SELECT publicaddr FROM %s
        WHERE connections < ?
        ORDER BY last_seen DESC
        LIMIT ?
    `, config.PeersTable)

	rows, err := u.DB.Query(query, maxConnections, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		peers = append(peers, addr)
	}

	return peers, nil
}

// GetAllPeers returns all peers in the database
func (u *UnifiedDB) GetAllPeers() ([]string, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`SELECT publicaddr FROM %s`, config.PeersTable)

	rows, err := u.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		peers = append(peers, addr)
	}

	return peers, nil
}

// UpdatePeerConnections updates the connections count for a peer
func (u *UnifiedDB) UpdatePeerConnections(peerID string, connections int) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	query := fmt.Sprintf(`
        UPDATE %s
        SET connections = ?,
            last_seen = strftime('%%s','now')
        WHERE peerID = ?
    `, config.PeersTable)

	result, err := u.DB.Exec(query, connections, peerID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return fmt.Errorf("peer %s not found", peerID)
	}

	return nil
}

// StoreKeyValue stores a key-value pair
func (u *UnifiedDB) StoreKeyValue(key, value string) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	query := fmt.Sprintf(`
        INSERT INTO %s (key, value, timestamp)
        VALUES (?, ?, strftime('%%s','now'))
        ON CONFLICT(key) DO UPDATE SET
            value = ?,
            timestamp = strftime('%%s','now')
    `, config.KeyValueTable)

	_, err := u.DB.Exec(query, key, value, value)
	return err
}

// GetKeyValue retrieves a value by key
func (u *UnifiedDB) GetKeyValue(key string) (string, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`SELECT value FROM %s WHERE key = ?`, config.KeyValueTable)

	var value string
	err := u.DB.QueryRow(query, key).Scan(&value)
	if err != nil {
		return "", err
	}

	return value, nil
}

// DeleteKeyValue deletes a key-value pair
func (u *UnifiedDB) DeleteKeyValue(key string) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	query := fmt.Sprintf(`DELETE FROM %s WHERE key = ?`, config.KeyValueTable)

	result, err := u.DB.Exec(query, key)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return fmt.Errorf("key %s not found", key)
	}

	return nil
}

// GetAllKeyValues retrieves all key-value pairs
func (u *UnifiedDB) GetAllKeyValues() (map[string]string, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`SELECT key, value FROM %s`, config.KeyValueTable)

	rows, err := u.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	data := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		data[key] = value
	}

	return data, nil
}

// StoreMerkleHash stores a key with its value hash for Merkle tree computation
func (u *UnifiedDB) StoreMerkleHash(key string, valueHash string) error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	query := fmt.Sprintf(`
        INSERT INTO %s (key, value_hash, timestamp)
        VALUES (?, ?, strftime('%%s','now'))
    `, config.MerkleTable)

	_, err := u.DB.Exec(query, key, valueHash)
	return err
}

// GetAllMerkleHashes retrieves all stored hashes for Merkle tree computation
func (u *UnifiedDB) GetAllMerkleHashes() (map[string]string, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`SELECT key, value_hash FROM %s`, config.MerkleTable)

	rows, err := u.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	data := make(map[string]string)
	for rows.Next() {
		var key, hash string
		if err := rows.Scan(&key, &hash); err != nil {
			return nil, err
		}
		data[key] = hash
	}

	return data, nil
}

// PeerInfo represents a connected peer in the database
type PeerInfo struct {
	PeerID        string `json:"peer_id"`
	Multiaddr     string `json:"multiaddr"`
	LastSeen      int64  `json:"last_seen"`
	HeartbeatFail int    `json:"heartbeat_fail"`
	IsAlive       bool   `json:"is_alive"`
}

// GetConnectedPeers retrieves all connected peers from the database
func (u *UnifiedDB) GetConnectedPeers() ([]PeerInfo, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	// First, check the table schema
	rows, err := u.DB.Query(fmt.Sprintf("PRAGMA table_info(%s)", config.ConnectedPeers))
	if err != nil {
		return nil, fmt.Errorf("failed to query table schema: %w", err)
	}

	for rows.Next() {
		var cid int
		var name, typeName string
		var notNull, pk int
		var dfltValue interface{}

		if err := rows.Scan(&cid, &name, &typeName, &notNull, &dfltValue, &pk); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan column info: %w", err)
		}

	}
	rows.Close()

	// Now query the actual data
	query := fmt.Sprintf(`SELECT peer_id, multiaddr, last_seen, heartbeat_fail, is_alive FROM %s`, config.ConnectedPeers)

	rows, err = u.DB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query connected peers: %w", err)
	}
	defer rows.Close()

	var peers []PeerInfo
	for rows.Next() {
		var peer PeerInfo
		var isAlive interface{}

		// Scan everything as interfaces to debug the types
		dest := []interface{}{
			&peer.PeerID,
			&peer.Multiaddr,
			&peer.LastSeen,
			&peer.HeartbeatFail,
			&isAlive,
		}

		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("failed to scan peer row: %w", err)
		}

		// Convert isAlive to bool based on its actual type
		switch v := isAlive.(type) {
		case bool:
			peer.IsAlive = v
		case int64:
			peer.IsAlive = v != 0
		case int:
			peer.IsAlive = v != 0
		case string:
			peer.IsAlive = v == "true" || v == "1"
		case []byte:
			s := string(v)
			peer.IsAlive = s == "true" || s == "1"
		default:
			fmt.Printf("Unexpected type for isAlive: %T\n", v)
			// Default to false
			peer.IsAlive = false
		}

		peers = append(peers, peer)
	}

	// Check for errors after iterating through rows
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating peer rows: %w", err)
	}

	return peers, nil
}

// GetConnectedPeersAsMap retrieves all connected peers as a list of map[string]interface{}
func (u *UnifiedDB) GetConnectedPeersAsMap() ([]map[string]interface{}, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`SELECT peer_id, multiaddr, last_seen, heartbeat_fail, is_alive FROM %s`, config.ConnectedPeers)

	rows, err := u.DB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query connected peers: %w", err)
	}
	defer rows.Close()

	var result []map[string]interface{}

	for rows.Next() {
		var peerID, multiaddr string
		var lastSeen int64
		var heartbeatFail, isAlive int

		if err := rows.Scan(&peerID, &multiaddr, &lastSeen, &heartbeatFail, &isAlive); err != nil {
			return nil, fmt.Errorf("failed to scan peer row: %w", err)
		}

		// Format timestamp for readability
		lastSeenTime := time.Unix(lastSeen, 0)

		// Create a map for this peer
		peerMap := map[string]interface{}{
			"peer_id":        peerID,
			"multiaddr":      multiaddr,
			"last_seen":      lastSeen,
			"last_seen_fmt":  lastSeenTime.Format(time.RFC3339),
			"heartbeat_fail": heartbeatFail,
			"is_alive":       isAlive != 0,
			"status": func() string {
				if isAlive != 0 {
					return "ONLINE"
				}
				return "OFFLINE"
			}(),
		}

		result = append(result, peerMap)
	}

	// Check for errors from iterating over rows
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating peer rows: %w", err)
	}

	return result, nil
}

// CountConnectedPeers returns the number of peers in the connected_peers table
func (u *UnifiedDB) CountConnectedPeers() (int, error) {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, config.ConnectedPeers)

	var count int
	err := u.DB.QueryRow(query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count connected peers: %w", err)
	}

	return count, nil
}
