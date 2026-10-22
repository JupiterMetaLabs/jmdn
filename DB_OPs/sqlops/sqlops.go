package sqlops

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"gossipnode/config"
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
)

// Pre-built SQL query strings using constant table names.
// Constructing queries at package initialisation (rather than inside each
// database function) removes dynamic fmt.Sprintf calls from SQL execution
// paths and eliminates the CWE-89 / SonarQube S3649 pattern while keeping
// the config-driven table-name abstraction intact.
var (

	// ConnectedPeers DDL + DML (also referenced from sqlops_test.go)
	// sqlCreateConnectedPeersTableStmt = fmt.Sprintf(`
	// CREATE TABLE IF NOT EXISTS %s (
	//     peer_id TEXT PRIMARY KEY,
	//     multiaddr TEXT NOT NULL,
	//     last_seen INTEGER NOT NULL,
	//     heartbeat_fail INTEGER DEFAULT 0,
	//     is_alive BOOLEAN DEFAULT 1
	// )`, config.ConnectedPeers)

	// sqlInsertConnectedPeerStmt = fmt.Sprintf(
	// 	`INSERT INTO %s (peer_id, multiaddr, last_seen, heartbeat_fail, is_alive) VALUES (?, ?, ?, ?, ?)`,
	// 	config.ConnectedPeers)

	sqlPragmaTableInfoConnectedPeers = fmt.Sprintf("PRAGMA table_info(%s)", config.ConnectedPeers)
	sqlSelectConnectedPeersStmt      = fmt.Sprintf(
		`SELECT peer_id, multiaddr, last_seen, heartbeat_fail, is_alive FROM %s`,
		config.ConnectedPeers)
)

// UnifiedDB is a wrapper for SQLite database
type UnifiedDB struct {
	DB    *sql.DB
	mutex sync.RWMutex
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
	rows, err := u.DB.Query(sqlPragmaTableInfoConnectedPeers)
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
	rows, err = u.DB.Query(sqlSelectConnectedPeersStmt)
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
			sqllogger(log.DB_OPs_SqlOps).Warn(context.Background(), "Unexpected type for isAlive", ion.String("type", fmt.Sprintf("%T", v)))
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

// logger returns the ion logger instance for sqlops package
func sqllogger(namedLogger string) *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(namedLogger, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
