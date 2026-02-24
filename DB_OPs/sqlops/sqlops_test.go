package sqlops

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gossipnode/config"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
)

var logger *log.Logger
var logFile *os.File

func init() {
	// Set up logging to file
	var err error
	logFile, err = os.Create("sqlops_test.log")
	if err != nil {
		panic(fmt.Sprintf("Failed to create log file: %v", err))
	}

	// Create logger that writes to both file and console
	logger = log.New(logFile, "", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile)
	logger.Println("===== DB Test Log Started =====")
	logger.Printf("Test started at: %v", time.Now().UTC().Format(time.RFC3339))
}

func cleanup() {
	if logFile != nil {
		logger.Println("===== DB Test Log Ended =====")
		logFile.Close()
	}
}

func setupTestDB(t *testing.T) *UnifiedDB {
	logger.Printf("Setting up test DB for %s", t.Name())
	testDBPath := filepath.Join("test.db")
	logger.Printf("Test DB path: %s", testDBPath)

	config.DBPath = testDBPath
	config.PeersTable = "peers"
	config.KeyValueTable = "key_value"
	config.MerkleTable = "merkle"

	logger.Printf("Using tables: peers=%s, kv=%s, merkle=%s",
		config.PeersTable, config.KeyValueTable, config.MerkleTable)

	db, err := NewUnifiedDB()
	if err != nil {
		logger.Printf("ERROR: Failed to create DB: %v", err)
		t.Fatalf("Failed to setup test DB: %v", err)
	}
	logger.Printf("Test DB setup successfully")

	// Log that schema was created
	tables := []string{config.PeersTable, config.KeyValueTable, config.MerkleTable}
	for _, table := range tables {
		var count int
		// Check if table exists by counting rows
		err := db.DB.QueryRow(fmt.Sprintf("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='%s'", table)).Scan(&count)
		if err != nil {
			logger.Printf("ERROR checking if table %s exists: %v", table, err)
		} else if count > 0 {
			logger.Printf("Table %s exists", table)
		} else {
			logger.Printf("WARNING: Table %s does not exist!", table)
		}
	}

	return db
}

func TestUnifiedDB(t *testing.T) {
	defer cleanup() // Make sure we close the log file when tests are done

	logger.Printf("Starting TestUnifiedDB")

	t.Run("Test Add and Retrieve Peer", func(t *testing.T) {
		logger.Printf("=== Starting Test: Add and Retrieve Peer ===")
		db := setupTestDB(t)
		defer db.Close()

		peerID := "peer1"
		addr := "192.168.1.1"
		connections := 3
		capabilities := []string{"relay", "validator"}

		logger.Printf("Adding peer: id=%s, addr=%s, conn=%d, cap=%v",
			peerID, addr, connections, capabilities)

		err := db.AddPeer(peerID, addr, connections, capabilities)
		if err != nil {
			logger.Printf("ERROR adding peer: %v", err)
		} else {
			logger.Printf("Peer added successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Retrieving peer: %s", peerID)
		retAddr, retConn, retCap, lastSeen, err := db.GetPeer(peerID)

		if err != nil {
			logger.Printf("ERROR retrieving peer: %v", err)
		} else {
			logger.Printf("Peer retrieved: addr=%s, conn=%d, cap=%v, lastSeen=%d",
				retAddr, retConn, retCap, lastSeen)
		}

		assert.NoError(t, err)
		assert.Equal(t, addr, retAddr)
		assert.Equal(t, connections, retConn)
		assert.ElementsMatch(t, capabilities, retCap)
		assert.NotZero(t, lastSeen)

		logger.Printf("=== Test Completed: Add and Retrieve Peer ===")
	})

	t.Run("Test Update Peer Connections", func(t *testing.T) {
		logger.Printf("=== Starting Test: Update Peer Connections ===")
		db := setupTestDB(t)
		defer db.Close()

		peerID := "peer1"
		addr := "192.168.1.1"
		initialConn := 3
		newConn := 5
		capabilities := []string{"relay"}

		logger.Printf("Adding peer: id=%s, addr=%s, conn=%d, cap=%v",
			peerID, addr, initialConn, capabilities)

		err := db.AddPeer(peerID, addr, initialConn, capabilities)
		if err != nil {
			logger.Printf("ERROR adding peer: %v", err)
		} else {
			logger.Printf("Peer added successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Updating peer connections: id=%s, new conn=%d", peerID, newConn)
		err = db.UpdatePeerConnections(peerID, newConn)
		if err != nil {
			logger.Printf("ERROR updating connections: %v", err)
		} else {
			logger.Printf("Connections updated successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Retrieving updated peer: %s", peerID)
		_, retConn, _, _, err := db.GetPeer(peerID)
		if err != nil {
			logger.Printf("ERROR retrieving peer: %v", err)
		} else {
			logger.Printf("Retrieved connections: %d", retConn)
		}

		assert.NoError(t, err)
		assert.Equal(t, newConn, retConn)

		logger.Printf("=== Test Completed: Update Peer Connections ===")
	})

	t.Run("Test Delete Peer", func(t *testing.T) {
		logger.Printf("=== Starting Test: Delete Peer ===")
		db := setupTestDB(t)
		defer db.Close()

		peerID := "peer1"
		addr := "192.168.1.1"
		connections := 3
		capabilities := []string{"relay"}

		logger.Printf("Adding peer: id=%s, addr=%s, conn=%d, cap=%v",
			peerID, addr, connections, capabilities)

		err := db.AddPeer(peerID, addr, connections, capabilities)
		if err != nil {
			logger.Printf("ERROR adding peer: %v", err)
		} else {
			logger.Printf("Peer added successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Deleting peer: %s", peerID)
		err = db.DeletePeer(peerID)
		if err != nil {
			logger.Printf("ERROR deleting peer: %v", err)
		} else {
			logger.Printf("Peer deleted successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Attempting to retrieve deleted peer: %s", peerID)
		_, _, _, _, err = db.GetPeer(peerID)
		if err != nil {
			logger.Printf("Expected error confirmed: %v", err)
		} else {
			logger.Printf("ERROR: Deleted peer was unexpectedly found")
		}
		assert.Error(t, err)

		logger.Printf("=== Test Completed: Delete Peer ===")
	})

	t.Run("Test Store and Retrieve KeyValue", func(t *testing.T) {
		logger.Printf("=== Starting Test: Store and Retrieve KeyValue ===")
		db := setupTestDB(t)
		defer db.Close()

		key := "key1"
		value := "value1"

		logger.Printf("Storing key-value: key=%s, value=%s", key, value)
		err := db.StoreKeyValue(key, value)
		if err != nil {
			logger.Printf("ERROR storing key-value: %v", err)
		} else {
			logger.Printf("Key-value stored successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Retrieving value for key: %s", key)
		val, err := db.GetKeyValue(key)
		if err != nil {
			logger.Printf("ERROR retrieving value: %v", err)
		} else {
			logger.Printf("Retrieved value: %s", val)
		}

		assert.NoError(t, err)
		assert.Equal(t, value, val)

		logger.Printf("=== Test Completed: Store and Retrieve KeyValue ===")
	})

	t.Run("Test Delete KeyValue", func(t *testing.T) {
		logger.Printf("=== Starting Test: Delete KeyValue ===")
		db := setupTestDB(t)
		defer db.Close()

		key := "key1"
		value := "value1"

		logger.Printf("Storing key-value: key=%s, value=%s", key, value)
		err := db.StoreKeyValue(key, value)
		if err != nil {
			logger.Printf("ERROR storing key-value: %v", err)
		} else {
			logger.Printf("Key-value stored successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Deleting key-value: %s", key)
		err = db.DeleteKeyValue(key)
		if err != nil {
			logger.Printf("ERROR deleting key-value: %v", err)
		} else {
			logger.Printf("Key-value deleted successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Attempting to retrieve deleted key: %s", key)
		_, err = db.GetKeyValue(key)
		if err != nil {
			logger.Printf("Expected error confirmed: %v", err)
		} else {
			logger.Printf("ERROR: Deleted key was unexpectedly found")
		}
		assert.Error(t, err)

		logger.Printf("=== Test Completed: Delete KeyValue ===")
	})

	t.Run("Test Store and Retrieve Merkle Hashes", func(t *testing.T) {
		logger.Printf("=== Starting Test: Store and Retrieve Merkle Hashes ===")
		db := setupTestDB(t)
		defer db.Close()

		key := "key2"
		hash := "hash123"

		logger.Printf("Storing merkle hash: key=%s, hash=%s", key, hash)
		err := db.StoreMerkleHash(key, hash)
		if err != nil {
			logger.Printf("ERROR storing merkle hash: %v", err)
		} else {
			logger.Printf("Merkle hash stored successfully")
		}
		assert.NoError(t, err)

		logger.Printf("Retrieving all merkle hashes")
		merkleHashes, err := db.GetAllMerkleHashes()
		if err != nil {
			logger.Printf("ERROR retrieving merkle hashes: %v", err)
		} else {
			logger.Printf("Retrieved %d merkle hashes", len(merkleHashes))
			for k, v := range merkleHashes {
				logger.Printf("  %s: %s", k, v)
			}
		}

		assert.NoError(t, err)
		assert.Equal(t, hash, merkleHashes[key])

		logger.Printf("=== Test Completed: Store and Retrieve Merkle Hashes ===")
	})

	// Add a concurrent test with detailed logging
	t.Run("Test Concurrent Operations", func(t *testing.T) {
		logger.Printf("=== Starting Test: Concurrent Operations ===")
		db := setupTestDB(t)
		defer db.Close()

		// Add some initial test data
		logger.Printf("Adding initial test data")
		for i := 0; i < 5; i++ {
			peerID := fmt.Sprintf("concurrent_peer%d", i)
			addr := fmt.Sprintf("192.168.0.%d", i+1)
			err := db.AddPeer(peerID, addr, i, []string{"test"})
			if err != nil {
				logger.Printf("ERROR adding initial peer %s: %v", peerID, err)
			} else {
				logger.Printf("Added initial peer: %s", peerID)
			}
		}

		// Run concurrent reads and writes
		logger.Printf("Starting concurrent operations")
		const numGoroutines = 5
		const numOperations = 20

		done := make(chan bool, numGoroutines*2)

		// Concurrent peer reading
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				logger.Printf("Reader goroutine %d started", id)
				for j := 0; j < numOperations; j++ {
					peers, err := db.GetAllPeers()
					if err != nil {
						logger.Printf("ERROR in reader %d, op %d: GetAllPeers failed: %v", id, j, err)
					} else {
						logger.Printf("Reader %d, op %d: Got %d peers", id, j, len(peers))
					}
					time.Sleep(time.Millisecond * 10)
				}
				logger.Printf("Reader goroutine %d completed", id)
				done <- true
			}(i)
		}

		// Concurrent peer writing
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				logger.Printf("Writer goroutine %d started", id)
				for j := 0; j < numOperations; j++ {
					peerID := fmt.Sprintf("concurrent_write_peer%d_%d", id, j)
					addr := fmt.Sprintf("10.0.%d.%d", id, j)

					err := db.AddPeer(peerID, addr, j%10, []string{"test", "writer"})
					if err != nil {
						logger.Printf("ERROR in writer %d, op %d: AddPeer failed: %v", id, j, err)
					} else {
						logger.Printf("Writer %d, op %d: Added peer %s", id, j, peerID)
					}

					// Occasionally update a peer
					if j%5 == 0 {
						targetID := fmt.Sprintf("concurrent_peer%d", j%5)
						err = db.UpdatePeerConnections(targetID, j)
						if err != nil {
							logger.Printf("ERROR in writer %d, op %d: UpdatePeerConnections failed: %v", id, j, err)
						} else {
							logger.Printf("Writer %d, op %d: Updated peer %s connections to %d", id, j, targetID, j)
						}
					}

					time.Sleep(time.Millisecond * 5)
				}
				logger.Printf("Writer goroutine %d completed", id)
				done <- true
			}(i)
		}

		// Wait for all goroutines to finish
		for i := 0; i < numGoroutines*2; i++ {
			<-done
		}

		// Verify the database is still functional
		finalPeers, err := db.GetAllPeers()
		if err != nil {
			logger.Printf("ERROR in final check: GetAllPeers failed: %v", err)
			t.Errorf("Database not functional after concurrent access: %v", err)
		} else {
			logger.Printf("Final check: Found %d peers after concurrent operations", len(finalPeers))
		}

		logger.Printf("=== Test Completed: Concurrent Operations ===")
	})

	logger.Printf("All tests completed")
}

// package sqlops

// import (
//     "fmt"
//     "os"
//     "path/filepath"
//     "testing"
//     "gossipnode/config"
//     _ "github.com/mattn/go-sqlite3"
//     "github.com/stretchr/testify/assert"
// )

// const testDBPath = "./testdata/test.db"

// func setupTestDB() (*UnifiedDB, error) {
//     config.DBPath = testDBPath
//     config.PeersTable = "peers"
//     config.KeyValueTable = "key_value"
//     config.MerkleTable = "merkle"

//     fmt.Println("Setting up test database...")
//     return NewUnifiedDB()
// }

// func teardownTestDB() {
//     fmt.Println("Tearing down test database...")
//     os.RemoveAll(filepath.Dir(testDBPath))
// }

// func TestUnifiedDB(t *testing.T) {
//     db, err := setupTestDB()
//     assert.NoError(t, err)
//     defer db.Close()
//     defer teardownTestDB()

//     fmt.Println("Testing AddPeer...")
//     err = db.AddPeer("peer1", "192.168.1.1", 3, []string{"relay", "validator"})
//     assert.NoError(t, err)

//     fmt.Println("Testing GetPeer...")
//     addr, connections, capabilities, lastSeen, err := db.GetPeer("peer1")
//     assert.NoError(t, err)
//     assert.Equal(t, "192.168.1.1", addr)
//     assert.Equal(t, 3, connections)
//     assert.ElementsMatch(t, []string{"relay", "validator"}, capabilities)
//     assert.NotZero(t, lastSeen)

//     fmt.Println("Testing UpdatePeerConnections...")
//     err = db.UpdatePeerConnections("peer1", 5)
//     assert.NoError(t, err)
//     addr, connections, _, _, err = db.GetPeer("peer1")
//     assert.NoError(t, err)
//     assert.Equal(t, 5, connections)

//     fmt.Println("Testing DeletePeer...")
//     err = db.DeletePeer("peer1")
//     assert.NoError(t, err)
//     _, _, _, _, err = db.GetPeer("peer1")
//     assert.Error(t, err)

//     fmt.Println("Testing StoreKeyValue & GetKeyValue...")
//     err = db.StoreKeyValue("key1", "value1")
//     assert.NoError(t, err)
//     val, err := db.GetKeyValue("key1")
//     assert.NoError(t, err)
//     assert.Equal(t, "value1", val)

//     fmt.Println("Testing DeleteKeyValue...")
//     err = db.DeleteKeyValue("key1")
//     assert.NoError(t, err)
//     _, err = db.GetKeyValue("key1")
//     assert.Error(t, err)

//	    fmt.Println("Testing StoreMerkleHash & GetAllMerkleHashes...")
//	    err = db.StoreMerkleHash("key2", "hash123")
//	    assert.NoError(t, err)
//	    merkleHashes, err := db.GetAllMerkleHashes()
//	    assert.NoError(t, err)
//	    assert.Equal(t, "hash123", merkleHashes["key2"])
//	}
func TestGetConnectedPeers(t *testing.T) {
	// Path to the existing database
	dbPath := "../../DB/gossipnode.db"

	// Check if the database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("Database file %s does not exist", dbPath)
	}

	// Open the database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Check connection
	if err = db.Ping(); err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}

	// Initialize UnifiedDB
	unifiedDB := &UnifiedDB{
		DB:    db,
		mutex: sync.RWMutex{},
	}

	// First, check if the table exists
	var tableExists bool
	err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", config.ConnectedPeers).Scan(&tableExists)
	if err != nil {
		t.Errorf("Failed to check if table exists: %v", err)
	}

	if !tableExists {
		t.Logf("Table %s does not exist in the database", config.ConnectedPeers)
		// Create the table for testing
		_, err = db.Exec(fmt.Sprintf(`
            CREATE TABLE IF NOT EXISTS %s (
                peer_id TEXT PRIMARY KEY,
                multiaddr TEXT NOT NULL,
                last_seen INTEGER NOT NULL,
                heartbeat_fail INTEGER DEFAULT 0,
                is_alive BOOLEAN DEFAULT 1
            )`, config.ConnectedPeers))
		if err != nil {
			t.Fatalf("Failed to create table: %v", err)
		}
		t.Logf("Created table %s", config.ConnectedPeers)

		// Add a test peer
		now := time.Now().UTC().Unix()
		_, err = db.Exec(fmt.Sprintf(
			"INSERT INTO %s (peer_id, multiaddr, last_seen, heartbeat_fail, is_alive) VALUES (?, ?, ?, ?, ?)",
			config.ConnectedPeers),
			"12D3KooWTestPeerID123456789ABCDEF",
			"/ip6/2001:db8::1/tcp/15000/p2p/12D3KooWTestPeerID123456789ABCDEF",
			now,
			0,
			1,
		)
		if err != nil {
			t.Fatalf("Failed to insert test peer: %v", err)
		}
		t.Log("Added test peer to database")
	}

	// Get row count
	var count int
	err = db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", config.ConnectedPeers)).Scan(&count)
	if err != nil {
		t.Errorf("Failed to count rows: %v", err)
	}
	t.Logf("Table %s contains %d rows", config.ConnectedPeers, count)

	// Call the function being tested
	peers, err := unifiedDB.GetConnectedPeers()
	if err != nil {
		t.Fatalf("GetConnectedPeers failed: %v", err)
	}

	fmt.Println(peers)

	// Print and verify results
	t.Logf("Found %d peers in database", len(peers))
	for i, peer := range peers {
		t.Logf("Peer %d:", i+1)
		t.Logf("  ID: %s", peer.PeerID)
		t.Logf("  Multiaddr: %s", peer.Multiaddr)
		t.Logf("  Last seen: %s", time.Unix(peer.LastSeen, 0).Format(time.RFC3339))
		t.Logf("  Heartbeat fail: %d", peer.HeartbeatFail)
		t.Logf("  Is alive: %v", peer.IsAlive)
	}

	// Basic validation
	if count > 0 && len(peers) == 0 {
		t.Errorf("Database reports %d rows but GetConnectedPeers returned empty result", count)
	}

	if count == 0 && len(peers) > 0 {
		t.Errorf("Database reports 0 rows but GetConnectedPeers returned %d peers", len(peers))
	}
}
