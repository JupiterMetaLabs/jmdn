// Package fastsync - Core FastSync Implementation with CRDT Integration
//
// This file contains the main FastSync implementation with integrated CRDT support.
// It provides the core synchronization functionality for distributed systems.
//
// CRDT INTEGRATION CHANGES:
// =========================
// 1. FastSync struct now includes a CRDT engine for conflict-free operations
// 2. NewFastSync constructor initializes CRDT engine with 50MB memory limit
// 3. Added GetCRDTEngine() method for external CRDT operations
// 4. Added ExportCRDTs() and ImportCRDTs() methods for network sync
// 5. Enhanced error handling and logging for CRDT operations
//
// FUNCTIONALITY ADDED:
// ===================
// - Conflict-free synchronization of LWW-Sets and Counters
// - Operation-based sync instead of state-based (preserves operation history)
// - Deterministic convergence across distributed nodes
// - Memory-bounded operation history with automatic eviction
// - Comprehensive logging and error handling for CRDT operations
//
// USAGE EXAMPLE:
// ==============
//
//	fs := NewFastSync(host, mainDB, accountsDB)
//	crdtEngine := fs.GetCRDTEngine()
//	crdtEngine.LWWAdd("user:123", "preferences", "theme:dark", VectorClock{})
//	crdtEngine.CounterInc("node:abc", "requests", 1, VectorClock{})
package fastsync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/crdt"
	hashmap "gossipnode/crdt/HashMap"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
)

const (
	AVRO_FILE_PATH = "fastsync/.temp/"
)

type MerkleRoot struct {
	MainMerkleRoot     []byte `json:"main_merkle_root"`
	AccountsMerkleRoot []byte `json:"accounts_merkle_root"`
}

// Need to change from IBLT to HashMap

type SyncMessage struct {
	Type             string                      `json:"type"`
	SenderID         string                      `json:"sender_id"`
	TxID             uint64                      `json:"tx_id,omitempty"`
	StartTxID        uint64                      `json:"start_tx_id,omitempty"`
	EndTxID          uint64                      `json:"end_tx_id,omitempty"`
	BatchNumber      int                         `json:"batch_number,omitempty"`
	TotalBatches     int                         `json:"total_batches,omitempty"`
	ChunkNumber      int                         `json:"chunk_number,omitempty"` // New: Chunk number for HashMap transfer
	TotalChunks      int                         `json:"total_chunks,omitempty"` // New: Total chunks for HashMap transfer
	ChunkKeys        []string                    `json:"chunk_keys,omitempty"`   // New: Keys in this chunk
	MerkleRoot       MerkleRoot                  `json:"merkle_root,omitempty"`
	KeysCount        int                         `json:"keys_count,omitempty"`
	Data             json.RawMessage             `json:"data,omitempty"`
	Success          bool                        `json:"success,omitempty"`
	ErrorMessage     string                      `json:"error_message,omitempty"`
	Timestamp        int64                       `json:"timestamp"`
	DBType           DatabaseType                `json:"db_type,omitempty"`
	HashMap          *TypeHashMapExchange_Struct `json:"hashmap_sync,omitempty"`
	HashMap_MetaData *HashMap_MetaData           `json:"hashmap_meta_data,omitempty"`
}

type FastSync struct {
	host             host.Host
	mainDB           *config.PooledConnection
	accountsDB       *config.PooledConnection
	active           map[peer.ID]*syncState
	mutex            sync.RWMutex
	HashMap_MetaData *HashMap_MetaData
	// NEW: CRDT engine for conflict-free replicated data types
	// This enables proper handling of concurrent operations during synchronization
	// without data loss or conflicts. Supports LWW-Sets and Counters.
	crdtEngine *crdt.Engine
}

type HashMap_MetaData struct {
	Main_HashMap_MetaData     *MetaData
	Accounts_HashMap_MetaData *MetaData
}

type MetaData struct {
	KeysCount int
	Checksum  string
}

type TypeHashMapExchange_Struct struct {
	MAIN_HashMap     *hashmap.HashMap
	Accounts_HashMap *hashmap.HashMap
}

const (
	TypeSyncRequest           = "SYNC_REQ"
	TypeSyncResponse          = "SYNC_RESP"
	TypeHashMapExchangeClient = "HASHMAP_EXCHANGE_CLIENT"
	TypeHashMapExchangeSYNC   = "HASHMAP_EXCHANGE_SYNC"
	TypeHashMapChunk          = "HASHMAP_CHUNK"          // New: Chunk of HashMap data
	TypeHashMapChunkAck       = "HASHMAP_CHUNK_ACK"      // New: Acknowledgment for chunk
	TypeHashMapChunkRequest   = "HASHMAP_CHUNK_REQ"      // New: Request for specific chunk
	TypeHashMapChunkComplete  = "HASHMAP_CHUNK_COMPLETE" // New: All chunks sent
	TypeBatchRequest          = "BATCH_REQ"
	TypeBatchData             = "BATCH_DATA"
	TypeSyncComplete          = "SYNC_COMPLETE"
	TypeSyncAbort             = "SYNC_ABORT"
	TypeVerificationRequest   = "VERIFY_REQ"
	TypeVerificationResult    = "VERIFY_RESP"
	TypeBatchAck              = "BATCH_ACK"
	TypeTransferFile          = "TRANSFER_FILE"
	RequestFiletransfer       = "REQUEST_FILE_TRANSFER"

	// Chunk size for HashMap transfer (100 keys per chunk)
	HashMapChunkSize = 100
)

func (fs *FastSync) getDB(dbType DatabaseType) *config.PooledConnection {
	if dbType == AccountsDB {
		return fs.accountsDB
	}
	return fs.mainDB
}

// getKeysBatchIncremental gets a batch of keys from database using direct scan
// This is a helper function to avoid loading all keys into memory
func (fs *FastSync) getKeysBatchIncremental(db *config.PooledConnection, prefix string, limit int, seekKey []byte) ([]string, error) {
	if db == nil || db.Client == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	ic := db.Client
	scanReq := &schema.ScanRequest{
		Prefix:  []byte(prefix),
		Limit:   uint64(limit),
		SeekKey: seekKey,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	scanResult, err := ic.Client.Scan(ctx, scanReq)
	if err != nil {
		return nil, fmt.Errorf("scan failed for prefix %s: %w", prefix, err)
	}

	// Validate that all keys match the prefix (SeekKey might cause issues with prefix filtering)
	keys := make([]string, 0, len(scanResult.Entries))
	for _, entry := range scanResult.Entries {
		key := string(entry.Key)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		} else {
			// If we get a key that doesn't match the prefix, we've gone past the prefix range
			fmt.Printf(">>> [SERVER] Key '%s' doesn't match prefix '%s' - stopping scan\n", key, prefix)
			break
		}
	}

	return keys, nil
}

// getAllUniquePrefixes scans the database and extracts all unique prefixes from keys
// A prefix is defined as everything before the first colon (:) or the entire key if no colon exists
// Uses a more efficient approach: sample keys from different prefixes instead of scanning all keys
func (fs *FastSync) getAllUniquePrefixes(db *config.PooledConnection, dbType DatabaseType) ([]string, error) {
	fmt.Printf(">>> [SERVER] Discovering prefixes by sampling keys from database...\n")

	prefixSet := make(map[string]bool)
	batchSize := 1000
	maxKeysToScan := 50000 // Limit to 50k keys max to avoid infinite loops
	totalKeysScanned := 0

	// Instead of scanning all keys with empty prefix (which can loop), sample from known common prefixes
	// This is more efficient and avoids the infinite loop issue
	commonPrefixes := []string{"block:", "tx:", "tx_processed:", "tx_processing:", "address:", "did:", "latest_block"}

	// Sample a small batch from each common prefix to discover all unique prefixes
	for _, prefix := range commonPrefixes {
		keys, err := fs.getKeysBatchIncremental(db, prefix, 100, nil) // Sample 100 keys per prefix
		if err != nil {
			// Prefix might not exist, continue
			continue
		}

		totalKeysScanned += len(keys)

		// Extract prefixes from keys
		for _, key := range keys {
			// Extract prefix: everything before the first colon, or entire key if no colon
			extractedPrefix := key
			if idx := strings.Index(key, ":"); idx != -1 {
				extractedPrefix = key[:idx+1] // Include the colon in the prefix
			}
			prefixSet[extractedPrefix] = true
		}
	}

	// If we still haven't found many prefixes, do a limited full scan as fallback
	// But with strict limits to prevent infinite loops
	if len(prefixSet) < 3 && totalKeysScanned < maxKeysToScan {
		fmt.Printf(">>> [SERVER] Sampling from common prefixes found %d prefixes, doing limited full scan...\n", len(prefixSet))

		var lastKey []byte
		batchNum := 0
		seenKeys := make(map[string]bool)

		for totalKeysScanned < maxKeysToScan {
			batchNum++
			keys, err := fs.getKeysBatchIncremental(db, "", batchSize, lastKey)
			if err != nil {
				fmt.Printf(">>> [SERVER] ERROR: Failed to scan batch %d: %v\n", batchNum, err)
				break
			}

			if len(keys) == 0 {
				break
			}

			// Check for duplicate keys (loop detection)
			allDuplicates := true
			for _, key := range keys {
				if !seenKeys[key] {
					seenKeys[key] = true
					allDuplicates = false

					// Extract prefix
					extractedPrefix := key
					if idx := strings.Index(key, ":"); idx != -1 {
						extractedPrefix = key[:idx+1]
					}
					prefixSet[extractedPrefix] = true
				}
			}

			// If all keys in this batch were duplicates, we're in a loop - stop
			if allDuplicates && batchNum > 1 {
				fmt.Printf(">>> [SERVER] Detected duplicate keys (loop), stopping scan at batch %d\n", batchNum)
				break
			}

			totalKeysScanned += len(keys)

			if batchNum%10 == 0 {
				fmt.Printf(">>> [SERVER] Prefix discovery progress: batch %d, scanned %d keys, found %d unique prefixes...\n",
					batchNum, totalKeysScanned, len(prefixSet))
			}

			// If we got fewer than batch size, we're done
			if len(keys) < batchSize {
				break
			}

			// Set last key for next iteration
			newLastKey := []byte(keys[len(keys)-1])

			// Check if lastKey is the same as before (loop detection)
			if lastKey != nil && string(newLastKey) == string(lastKey) {
				fmt.Printf(">>> [SERVER] Detected same last key (loop), stopping scan\n")
				break
			}

			lastKey = newLastKey
		}
	}

	// Convert set to sorted slice
	prefixes := make([]string, 0, len(prefixSet))
	for prefix := range prefixSet {
		prefixes = append(prefixes, prefix)
	}

	// Sort prefixes for consistent output
	sort.Strings(prefixes)

	fmt.Printf(">>> [SERVER] ✓ Prefix discovery complete: scanned %d keys, found %d unique prefixes: %v\n",
		totalKeysScanned, len(prefixes), prefixes)

	return prefixes, nil
}

// computeSyncKeysIncremental computes SYNC keys by streaming through database keys
// without building the full server HashMap. This is much more memory-efficient for large datasets.
func (fs *FastSync) computeSyncKeysIncremental(db *config.PooledConnection, clientHashMap *hashmap.HashMap, dbType DatabaseType) ([]string, error) {
	var syncKeys []string
	var prefixes []string

	// Dynamically discover all prefixes in the database instead of hardcoding
	fmt.Printf(">>> [SERVER] Discovering prefixes dynamically for %s...\n", func() string {
		if dbType == MainDB {
			return "MainDB"
		}
		return "AccountsDB"
	}())

	discoveredPrefixes, err := fs.getAllUniquePrefixes(db, dbType)
	if err != nil {
		fmt.Printf(">>> [SERVER] WARNING: Failed to discover prefixes dynamically: %v, falling back to hardcoded prefixes\n", err)
		// Fallback to hardcoded prefixes if discovery fails
		if dbType == MainDB {
			prefixes = []string{"block:", "tx:", "tx_processed:", "tx_processing:"}
		} else {
			prefixes = []string{"address:", "did:"}
		}
	} else {
		fmt.Printf(">>> [SERVER] Discovered %d prefixes: %v\n", len(discoveredPrefixes), discoveredPrefixes)
		prefixes = discoveredPrefixes
		// Filter out latest_block from prefixes (it's handled separately)
		filteredPrefixes := make([]string, 0, len(prefixes))
		for _, prefix := range prefixes {
			if prefix != "latest_block" {
				filteredPrefixes = append(filteredPrefixes, prefix)
			}
		}
		prefixes = filteredPrefixes

		// Log which prefixes will be processed
		fmt.Printf(">>> [SERVER] Prefixes to process (after filtering latest_block): %v\n", prefixes)

		// Check if tx_processing is in the list
		hasTxProcessing := false
		for _, prefix := range prefixes {
			if prefix == "tx_processing:" {
				hasTxProcessing = true
				break
			}
		}
		if !hasTxProcessing && dbType == MainDB {
			fmt.Printf(">>> [SERVER] WARNING: tx_processing: prefix not found in discovered prefixes! Adding it manually...\n")
			prefixes = append(prefixes, "tx_processing:")
			fmt.Printf(">>> [SERVER] Updated prefixes: %v\n", prefixes)
		}
	}

	// CRITICAL: Handle latest_block for MainDB (compare VALUES to prevent downgrading client's newer blocks)
	// Only include latest_block if server's value is newer than client's (or client doesn't have it)
	if dbType == MainDB {
		exists, err := DB_OPs.Exists(db, "latest_block")
		if err == nil && exists {
			// Get server's latest_block value
			serverLatestBytes, err := DB_OPs.Read(db, "latest_block")
			if err == nil {
				var serverLatestBlock uint64
				if err := json.Unmarshal(serverLatestBytes, &serverLatestBlock); err == nil {
					// Check if client has latest_block in HashMap and compare values
					clientHasLatestBlock := clientHashMap.Exists("latest_block")

					if !clientHasLatestBlock {
						// Client doesn't have latest_block, include it
						syncKeys = append(syncKeys, "latest_block")
						fmt.Printf(">>> [SERVER] ✓ latest_block will be included (client doesn't have it, server: %d)\n", serverLatestBlock)
					} else {
						// Client has latest_block, need to check if we can get its value from HashMap
						// Note: HashMap only stores keys, not values, so we can't directly compare
						// But we can check if client has blocks up to server's latest_block
						// If client has block:serverLatestBlock, then client likely has same or newer latest_block
						clientBlockKey := fmt.Sprintf("block:%d", serverLatestBlock)
						if !clientHashMap.Exists(clientBlockKey) {
							// Client doesn't have the block corresponding to server's latest_block
							// This means server's latest_block is newer (or client has different blocks)
							// Include latest_block to sync it
							syncKeys = append(syncKeys, "latest_block")
							fmt.Printf(">>> [SERVER] ✓ latest_block will be included (server: %d, client missing block:%d)\n", serverLatestBlock, serverLatestBlock)
						} else {
							// Client has the block corresponding to server's latest_block
							// Check if client might have newer blocks by checking for higher block numbers
							clientHasNewerBlock := false
							for testBlock := serverLatestBlock + 1; testBlock <= serverLatestBlock+10; testBlock++ {
								testBlockKey := fmt.Sprintf("block:%d", testBlock)
								if clientHashMap.Exists(testBlockKey) {
									clientHasNewerBlock = true
									break
								}
							}

							if clientHasNewerBlock {
								// Client has newer blocks than server, DON'T include latest_block to avoid downgrading
								fmt.Printf(">>> [SERVER] ⚠ SKIPPING latest_block (server: %d, client has newer blocks - would downgrade)\n", serverLatestBlock)
							} else {
								// Client has same or older blocks, include latest_block for safety
								syncKeys = append(syncKeys, "latest_block")
								fmt.Printf(">>> [SERVER] ✓ latest_block will be included (server: %d, client likely same or older)\n", serverLatestBlock)
							}
						}
					}
				} else {
					fmt.Printf(">>> [SERVER] WARNING: Failed to parse server's latest_block value: %v\n", err)
				}
			} else {
				fmt.Printf(">>> [SERVER] WARNING: Failed to read server's latest_block: %v\n", err)
			}
		} else {
			fmt.Printf(">>> [SERVER] WARNING: Server does not have latest_block key (exists: %v, err: %v)\n", exists, err)
		}
	}

	fmt.Printf(">>> [SERVER] Checking %d prefixes for SYNC keys (incremental approach)...\n", len(prefixes))
	fmt.Printf(">>> [SERVER] Client HashMap size: %d\n", func() int {
		if clientHashMap != nil {
			return clientHashMap.Size()
		}
		return 0
	}())

	// Track block key statistics across all prefixes
	totalBlockKeysChecked := 0
	totalBlockKeysInSync := 0
	totalBlockKeysSkipped := 0

	// Process each prefix incrementally
	for prefixIdx, prefix := range prefixes {
		fmt.Printf(">>> [SERVER] Processing prefix %d/%d: '%s'...\n", prefixIdx+1, len(prefixes), prefix)

		batchSize := 1000
		var lastKey []byte
		batchNum := 0
		totalChecked := 0
		keysInClientHashMap := 0
		prefixBlockKeysChecked := 0
		prefixBlockKeysInSync := 0
		prefixBlockKeysSkipped := 0

		for {
			batchNum++
			if batchNum%100 == 0 {
				fmt.Printf(">>> [SERVER] Progress for '%s': batch %d (checked %d keys, found %d SYNC keys, %d already in client)...\n",
					prefix, batchNum, totalChecked, len(syncKeys), keysInClientHashMap)
			}

			// Get batch of keys from database
			keys, err := fs.getKeysBatchIncremental(db, prefix, batchSize, lastKey)
			if err != nil {
				fmt.Printf(">>> [SERVER] ERROR: Failed to get batch for '%s': %v\n", prefix, err)
				return nil, fmt.Errorf("failed to get keys batch for prefix %s: %w", prefix, err)
			}

			if len(keys) == 0 {
				fmt.Printf(">>> [SERVER] ✓ Finished processing prefix '%s' (total batches: %d, checked %d, found %d SYNC, %d in client)\n",
					prefix, batchNum, totalChecked, len(syncKeys), keysInClientHashMap)
				break
			}

			totalChecked += len(keys)

			// Check each key against client HashMap - only keep keys NOT in client HashMap
			// CRITICAL: For block keys, we skip latest_block check here since it's already handled above
			blockKeysChecked := 0
			blockKeysInSync := 0
			blockKeysSkipped := 0

			for _, key := range keys {
				// Skip latest_block - it's already handled above
				if key == "latest_block" {
					continue
				}

				isBlockKey := strings.HasPrefix(key, "block:")
				if isBlockKey {
					blockKeysChecked++
				}

				if !clientHashMap.Exists(key) {
					syncKeys = append(syncKeys, key)
					if isBlockKey {
						blockKeysInSync++
					}
				} else {
					keysInClientHashMap++
					if isBlockKey {
						blockKeysSkipped++
					}
				}
			}

			// Accumulate block key statistics for this prefix
			prefixBlockKeysChecked += blockKeysChecked
			prefixBlockKeysInSync += blockKeysInSync
			prefixBlockKeysSkipped += blockKeysSkipped

			// Log block key statistics for debugging
			if blockKeysChecked > 0 && batchNum%10 == 0 {
				fmt.Printf(">>> [SERVER] Block keys in batch %d: checked %d, SYNC %d, skipped %d (already in client HashMap)\n",
					batchNum, blockKeysChecked, blockKeysInSync, blockKeysSkipped)
			}

			// If we got fewer than batch size, we're done with this prefix
			if len(keys) < batchSize {
				fmt.Printf(">>> [SERVER] ✓ Finished processing prefix '%s' (total batches: %d, checked %d keys, found %d SYNC keys, %d already in client)\n",
					prefix, batchNum, totalChecked, len(syncKeys), keysInClientHashMap)
				break
			}

			// Set last key for next iteration
			lastKey = []byte(keys[len(keys)-1])
		}

		// Accumulate block key statistics across all prefixes
		if prefix == "block:" {
			totalBlockKeysChecked += prefixBlockKeysChecked
			totalBlockKeysInSync += prefixBlockKeysInSync
			totalBlockKeysSkipped += prefixBlockKeysSkipped
			fmt.Printf(">>> [SERVER] ✓ Prefix 'block:' complete - checked %d keys, found %d SYNC keys, %d already in client HashMap\n",
				totalChecked, len(syncKeys), keysInClientHashMap)
			fmt.Printf(">>> [SERVER]   Block keys: checked %d, SYNC %d, skipped %d (in client HashMap)\n",
				prefixBlockKeysChecked, prefixBlockKeysInSync, prefixBlockKeysSkipped)
		} else {
			fmt.Printf(">>> [SERVER] ✓ Prefix '%s' complete - checked %d keys, found %d SYNC keys, %d already in client HashMap\n",
				prefix, totalChecked, len(syncKeys), keysInClientHashMap)
		}
	}

	// Count block keys and latest_block in final SYNC keys
	blockKeyCount := 0
	hasLatestBlock := false
	for _, key := range syncKeys {
		if strings.HasPrefix(key, "block:") {
			blockKeyCount++
		} else if key == "latest_block" {
			hasLatestBlock = true
		}
	}

	fmt.Printf(">>> [SERVER] ✓ Incremental SYNC computation complete\n")
	fmt.Printf(">>> [SERVER]   Total SYNC keys: %d\n", len(syncKeys))
	fmt.Printf(">>> [SERVER]   Block keys in SYNC: %d\n", blockKeyCount)
	fmt.Printf(">>> [SERVER]   latest_block in SYNC: %v\n", hasLatestBlock)

	if dbType == MainDB {
		fmt.Printf(">>> [SERVER]   Block key statistics:\n")
		fmt.Printf(">>> [SERVER]     Total block keys checked: %d\n", totalBlockKeysChecked)
		fmt.Printf(">>> [SERVER]     Block keys in SYNC: %d\n", totalBlockKeysInSync)
		fmt.Printf(">>> [SERVER]     Block keys skipped (in client HashMap): %d\n", totalBlockKeysSkipped)

		if totalBlockKeysChecked > 0 && totalBlockKeysInSync == 0 && totalBlockKeysSkipped > 0 {
			fmt.Printf(">>> [SERVER] ⚠ WARNING: Client HashMap contains ALL %d block keys, but 0 were added to SYNC!\n", totalBlockKeysSkipped)
			fmt.Printf(">>> [SERVER] ⚠ This suggests client's HashMap is stale (contains keys not in client's actual database)\n")
			fmt.Printf(">>> [SERVER] ⚠ Client should validate HashMap keys before sending to server\n")
		}

		if blockKeyCount == 0 && !hasLatestBlock {
			fmt.Printf(">>> [SERVER] WARNING: No block keys or latest_block in SYNC keys! This may indicate client HashMap is stale.\n")
		}
	}

	return syncKeys, nil
}

func GetDBData_Default(db *config.PooledConnection, prefix string) ([]string, error) {
	keys, err := DB_OPs.GetAllKeys(db, prefix)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func GetDBData_Accounts(db *config.PooledConnection, prefix string) ([]string, error) {
	DIDs, err := DB_OPs.GetAllKeys(db, prefix)
	if err != nil {
		return nil, err
	}
	return DIDs, nil
}

func (fs *FastSync) MakeHashMap_Default() (*hashmap.HashMap, error) {
	if fs.mainDB == nil {
		return nil, fmt.Errorf("mainDB is nil - FastSync not properly initialized")
	}

	fmt.Println(">>> [SERVER] Making Default HashMap...")
	MAP := hashmap.New()

	// Get block: keys
	fmt.Println(">>> [SERVER] Getting block: keys...")
	blockKeys, err := GetDBData_Default(fs.mainDB, "block:")
	if err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to get block keys: %v\n", err)
		return nil, err
	}
	fmt.Printf(">>> [SERVER] ✓ Got %d block keys\n", len(blockKeys))
	for _, key := range blockKeys {
		MAP.Insert(key)
	}

	// Get tx: keys
	fmt.Println(">>> [SERVER] Getting tx: keys...")
	txKeys, err := GetDBData_Default(fs.mainDB, "tx:")
	if err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to get tx keys: %v\n", err)
		return nil, err
	}
	fmt.Printf(">>> [SERVER] ✓ Got %d tx keys\n", len(txKeys))
	for _, key := range txKeys {
		MAP.Insert(key)
	}

	// Get tx_processed: keys
	fmt.Println(">>> [SERVER] Getting tx_processed: keys...")
	txProcessedKeys, err := GetDBData_Default(fs.mainDB, "tx_processed:")
	if err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to get tx_processed keys: %v\n", err)
		return nil, err
	}
	fmt.Printf(">>> [SERVER] ✓ Got %d tx_processed keys\n", len(txProcessedKeys))
	for _, key := range txProcessedKeys {
		MAP.Insert(key)
	}

	// Check for latest_block key explicitly
	fmt.Println(">>> [SERVER] Checking for latest_block key...")
	exists, err := DB_OPs.Exists(fs.mainDB, "latest_block")
	if err == nil && exists {
		MAP.Insert("latest_block")
		fmt.Println(">>> [SERVER] ✓ Added latest_block key")
	}

	fmt.Printf(">>> [SERVER] ✓ Default HashMap complete: %d total keys\n", MAP.Size())

	// CRITICAL: Validate HashMap keys exist in DB to remove stale keys
	// This ensures the HashMap accurately reflects the current DB state
	// This is especially important for subsequent syncs where the HashMap might contain stale keys
	fmt.Println(">>> [SERVER] Validating Main HashMap keys against DB (removing stale keys)...")
	validatedHashMap := hashmap.New()
	allKeys := MAP.Keys()
	validatedCount := 0
	removedCount := 0

	for _, key := range allKeys {
		// Explicitly handle latest_block for clarity
		isLatestBlock := (key == "latest_block")

		exists, err := DB_OPs.Exists(fs.mainDB, key)
		if err != nil {
			if isLatestBlock {
				fmt.Printf(">>> [SERVER] WARNING: Error checking latest_block key: %v, skipping\n", err)
			} else {
				fmt.Printf(">>> [SERVER] WARNING: Error checking key '%s': %v, skipping\n", key, err)
			}
			removedCount++
			continue
		}
		if exists {
			validatedHashMap.Insert(key)
			validatedCount++
			if isLatestBlock {
				fmt.Printf(">>> [SERVER] ✓ latest_block validated and included in HashMap\n")
			}
		} else {
			removedCount++
			if isLatestBlock {
				fmt.Printf(">>> [SERVER] WARNING: Removed stale latest_block from HashMap (not in DB)\n")
			} else {
				fmt.Printf(">>> [SERVER] Removed stale key from HashMap: '%s' (not in DB)\n", key)
			}
		}
	}

	if removedCount > 0 {
		fmt.Printf(">>> [SERVER] WARNING: Removed %d stale keys from Main HashMap (original: %d, validated: %d)\n",
			removedCount, MAP.Size(), validatedHashMap.Size())
		fmt.Printf(">>> [SERVER] This suggests keys were deleted or the HashMap was out of sync with DB\n")
	} else {
		fmt.Printf(">>> [SERVER] ✓ All %d keys validated - HashMap matches DB state\n", validatedHashMap.Size())
	}

	return validatedHashMap, nil
}

func (fs *FastSync) MakeHashMap_Accounts() (*hashmap.HashMap, error) {
	if fs.accountsDB == nil {
		return nil, fmt.Errorf("accountsDB is nil - FastSync not properly initialized")
	}

	fmt.Println(">>> [SERVER] Making Accounts HashMap...")
	MAP := hashmap.New()

	// Get address: keys (actual account data)
	fmt.Println(">>> [SERVER] Getting address: keys...")
	addressKeys, err := GetDBData_Accounts(fs.accountsDB, "address:")
	if err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to get address keys: %v\n", err)
		return nil, err
	}
	fmt.Printf(">>> [SERVER] ✓ Got %d address keys\n", len(addressKeys))
	for _, key := range addressKeys {
		MAP.Insert(key)
	}

	// Get did: keys (DID references to accounts)
	fmt.Println(">>> [SERVER] Getting did: keys...")
	didKeys, err := GetDBData_Accounts(fs.accountsDB, "did:")
	if err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to get did keys: %v\n", err)
		return nil, err
	}
	fmt.Printf(">>> [SERVER] ✓ Got %d did keys\n", len(didKeys))
	for _, key := range didKeys {
		MAP.Insert(key)
	}

	fmt.Printf(">>> [SERVER] ✓ Accounts HashMap complete: %d total keys\n", MAP.Size())

	// CRITICAL: Validate HashMap keys exist in DB to remove stale keys
	// This ensures the HashMap accurately reflects the current DB state
	fmt.Println(">>> [SERVER] Validating Accounts HashMap keys against DB (removing stale keys)...")
	validatedHashMap := hashmap.New()
	allKeys := MAP.Keys()
	validatedCount := 0
	removedCount := 0

	for _, key := range allKeys {
		exists, err := DB_OPs.Exists(fs.accountsDB, key)
		if err != nil {
			fmt.Printf(">>> [SERVER] WARNING: Error checking key '%s': %v, skipping\n", key, err)
			removedCount++
			continue
		}
		if exists {
			validatedHashMap.Insert(key)
			validatedCount++
		} else {
			removedCount++
			fmt.Printf(">>> [SERVER] Removed stale key from HashMap: '%s' (not in DB)\n", key)
		}
	}

	if removedCount > 0 {
		fmt.Printf(">>> [SERVER] WARNING: Removed %d stale keys from Accounts HashMap (original: %d, validated: %d)\n",
			removedCount, MAP.Size(), validatedHashMap.Size())
		fmt.Printf(">>> [SERVER] This suggests keys were deleted or the HashMap was out of sync with DB\n")
	} else {
		fmt.Printf(">>> [SERVER] ✓ All %d keys validated - HashMap matches DB state\n", validatedHashMap.Size())
	}

	return validatedHashMap, nil
}

// UPDATED: NewFastSync now initializes CRDT engine for conflict-free synchronization
// This enables proper handling of concurrent operations during sync without data loss
func NewFastSync(h host.Host, mainDB, accountsDB *config.PooledConnection) *FastSync {
	// NEW: Initialize CRDT engine with 50MB memory limit for operation history
	// This allows the system to maintain a bounded operation log for synchronization
	crdtEngine := crdt.NewEngineMemOnly(50 * 1024 * 1024)

	fs := &FastSync{
		host:       h,
		mainDB:     mainDB,
		accountsDB: accountsDB,
		active:     make(map[peer.ID]*syncState),
		crdtEngine: crdtEngine, // NEW: CRDT engine for conflict-free operations
	}

	h.SetStreamHandler(SyncProtocolID, fs.handleStream)
	log.Info().Msg("FastSync initialized with multi-database support and CRDT engine")

	return fs
}

// NEW: GetCRDTEngine provides access to the CRDT engine for external operations
// This allows other parts of the system to perform CRDT operations like:
// - Adding/removing elements from LWW-Sets
// - Incrementing counters
// - Querying current CRDT state
func (fs *FastSync) GetCRDTEngine() *crdt.Engine {
	return fs.crdtEngine
}

// IMPLEMENTED: ExportCRDTs exports all CRDTs for synchronization with other nodes
// This is used during sync to send CRDT state to remote nodes
// Returns serialized CRDT data that can be transmitted over the network
func (fs *FastSync) ExportCRDTs() ([]json.RawMessage, error) {
	if fs.crdtEngine == nil {
		return nil, fmt.Errorf("CRDT engine not initialized")
	}

	log.Info().Msg("Starting CRDT export for synchronization")

	// 1. Get all CRDT objects from the memory store
	allCRDTs := fs.crdtEngine.GetAllCRDTs()

	if len(allCRDTs) == 0 {
		log.Info().Msg("No CRDTs to export")
		return []json.RawMessage{}, nil
	}

	log.Info().Int("count", len(allCRDTs)).Msg("Exporting CRDTs")

	// 2. Serialize each CRDT to JSON format with metadata
	var exportedCRDTs []json.RawMessage

	for key, crdtObj := range allCRDTs {
		// Determine CRDT type using proper type assertion
		var crdtType string
		switch crdtObj.(type) {
		case *crdt.LWWSet:
			crdtType = "lww-set"
		case *crdt.Counter:
			crdtType = "counter"
		default:
			log.Warn().Str("key", key).Msg("Unknown CRDT type, skipping export")
			continue
		}

		// Serialize CRDT data to JSON
		crdtData, err := json.Marshal(crdtObj)
		if err != nil {
			log.Error().Err(err).Str("key", key).Str("type", crdtType).Msg("Failed to marshal CRDT data")
			continue
		}

		// 3. Wrap with metadata (type, key, timestamp)
		wrapper := map[string]interface{}{
			"type":      crdtType,
			"key":       key,
			"data":      json.RawMessage(crdtData),
			"timestamp": time.Now().UTC().Unix(),
			"version":   "1.0", // For future compatibility
		}

		// Serialize wrapper to JSON
		wrapperData, err := json.Marshal(wrapper)
		if err != nil {
			log.Error().Err(err).Str("key", key).Str("type", crdtType).Msg("Failed to marshal CRDT wrapper")
			continue
		}

		exportedCRDTs = append(exportedCRDTs, json.RawMessage(wrapperData))

		log.Debug().
			Str("key", key).
			Str("type", crdtType).
			Int("size", len(wrapperData)).
			Msg("Exported CRDT")
	}

	log.Info().
		Int("total", len(allCRDTs)).
		Int("exported", len(exportedCRDTs)).
		Msg("CRDT export completed")

	// 4. Return serialized data for network transmission
	return exportedCRDTs, nil
}

// IMPLEMENTED: ImportCRDTs imports CRDTs received from other nodes during sync
// This processes incoming CRDT data and applies it to the local CRDT engine
func (fs *FastSync) ImportCRDTs(crdtData []json.RawMessage) error {
	if fs.crdtEngine == nil {
		return fmt.Errorf("CRDT engine not initialized")
	}

	if len(crdtData) == 0 {
		log.Info().Msg("No CRDTs to import")
		return nil
	}

	log.Info().Int("count", len(crdtData)).Msg("Starting CRDT import")

	// Use the existing storeCRDTs function which already handles the import logic
	err := fs.storeCRDTs(fs.crdtEngine, crdtData)
	if err != nil {
		log.Error().Err(err).Msg("Failed to import CRDTs")
		return fmt.Errorf("failed to import CRDTs: %w", err)
	}

	log.Info().Int("imported", len(crdtData)).Msg("CRDT import completed successfully")
	return nil
}

func readMessage(reader *bufio.Reader, stream network.Stream) (*SyncMessage, error) {
	// Set initial deadline - use extended timeout for debugging (30 minutes)
	// This allows for very long HashMap computation times
	extendedTimeout := 30 * time.Minute
	deadline := time.Now().UTC().Add(extendedTimeout)
	if err := stream.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("failed to set read deadline: %w", err)
	}

	// Use a larger buffer for reading message data
	var msgData []byte
	chunkCount := 0
	lastDeadlineUpdate := time.Now().UTC()

	// Read until newline to get complete message
	for {
		// Extend deadline periodically during long reads (every 30 seconds)
		// This prevents timeout for very large messages like HashMap exchanges
		if time.Since(lastDeadlineUpdate) > 30*time.Second {
			deadline = time.Now().UTC().Add(extendedTimeout)
			if err := stream.SetReadDeadline(deadline); err != nil {
				log.Warn().Err(err).Msg("Failed to extend read deadline, continuing")
			} else {
				lastDeadlineUpdate = time.Now().UTC()
				log.Debug().
					Int("chunks_read", chunkCount).
					Int("bytes_read", len(msgData)).
					Msg("Extended read deadline for large message")
			}
		}

		chunk, isPrefix, err := reader.ReadLine()
		if err != nil {
			log.Error().Err(err).Msg("Failed to read message chunk")
			return nil, fmt.Errorf("failed to read message: %w", err)
		}

		msgData = append(msgData, chunk...)
		chunkCount++

		if !isPrefix {
			break
		}
	}

	// Add debugging to show message size and content
	log.Info().
		Int("bytes", len(msgData)).
		Int("chunks", chunkCount).
		Msg("Read message data")

	if len(msgData) == 0 {
		return nil, fmt.Errorf("received empty message")
	}

	var msg SyncMessage
	if err := json.Unmarshal(msgData, &msg); err != nil {
		log.Error().
			Err(err).
			Int("bytes", len(msgData)).
			Str("raw_data", string(msgData)).
			Msg("Failed to unmarshal message")
		return nil, fmt.Errorf("failed to unmarshal message (%d bytes): %w", len(msgData), err)
	}

	return &msg, nil
}

func writeMessage(writer *bufio.Writer, stream network.Stream, msg *SyncMessage) error {
	// For large HashMap messages, serialization can take time
	// Estimate message size based on HashMap metadata if available
	var estimatedSize int
	if msg.HashMap_MetaData != nil {
		// Rough estimate: ~100 bytes per key for HashMap serialization
		mainKeys := 0
		accountsKeys := 0
		if msg.HashMap_MetaData.Main_HashMap_MetaData != nil {
			mainKeys = msg.HashMap_MetaData.Main_HashMap_MetaData.KeysCount
		}
		if msg.HashMap_MetaData.Accounts_HashMap_MetaData != nil {
			accountsKeys = msg.HashMap_MetaData.Accounts_HashMap_MetaData.KeysCount
		}
		estimatedSize = (mainKeys + accountsKeys) * 100
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal message")
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	actualSize := len(msgBytes)
	log.Info().
		Int("bytes", actualSize).
		Int("estimated_keys", estimatedSize/100).
		Msg("Writing message data")

	// Set write deadline - extend for large messages
	deadline := time.Now().UTC().Add(ResponseTimeout)
	if estimatedSize > 1000000 || actualSize > 1000000 {
		// For very large messages (>1MB), give extra time
		deadline = time.Now().UTC().Add(ResponseTimeout * 2)
		log.Info().
			Int("size_bytes", actualSize).
			Msg("Large message detected, using extended timeout")
	}

	if err := stream.SetWriteDeadline(deadline); err != nil {
		log.Error().Err(err).Msg("Failed to set write deadline")
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	// Write in chunks for very large messages to avoid blocking
	if actualSize > 1024*1024 { // > 1MB
		chunkSize := 64 * 1024 // 64KB chunks
		for i := 0; i < len(msgBytes); i += chunkSize {
			end := i + chunkSize
			if end > len(msgBytes) {
				end = len(msgBytes)
			}
			if _, err := writer.Write(msgBytes[i:end]); err != nil {
				log.Error().Err(err).Int("offset", i).Msg("Failed to write message chunk")
				return fmt.Errorf("failed to write message chunk: %w", err)
			}
			// Periodically flush and extend deadline during long writes
			if (i/chunkSize)%10 == 0 {
				if err := writer.Flush(); err != nil {
					log.Error().Err(err).Msg("Failed to flush during chunk write")
				}
				deadline = time.Now().UTC().Add(ResponseTimeout)
				stream.SetWriteDeadline(deadline)
			}
		}
	} else {
		if _, err := writer.Write(msgBytes); err != nil {
			log.Error().Err(err).Msg("Failed to write message bytes")
			return fmt.Errorf("failed to write message: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		log.Error().Err(err).Msg("Failed to flush message")
		return fmt.Errorf("failed to flush message: %w", err)
	}

	log.Info().Msg("Message written and flushed successfully")
	return nil
}

func retry(operation func() error) error {
	var lastErr error
	backoff := RetryDelay

	for attempt := 0; attempt < MaxRetries; attempt++ {
		if attempt > 0 {
			log.Debug().Int("attempt", attempt+1).Dur("delay", backoff).Msg("Retrying operation")
			time.Sleep(backoff)
			backoff *= 2 // Exponential backoff
		}

		if err := operation(); err == nil {
			return nil // Success
		} else {
			lastErr = err
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", MaxRetries, lastErr)
}

// handleStream processes incoming sync protocol messages
func (fs *FastSync) handleStream(stream network.Stream) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Msg("PANIC in handleStream - recovering and closing stream")
			fmt.Printf(">>> [SERVER] PANIC in handleStream: %v\n", r)
		}
		stream.Close()
	}()

	peerID := stream.Conn().RemotePeer()
	remote := stream.Conn().RemoteMultiaddr().String()

	log.Info().
		Str("peer", peerID.String()).
		Str("remote", remote).
		Msg("Received sync stream")

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

	for {
		msg, err := readMessage(reader, stream)
		if err != nil {
			if err != io.EOF {
				log.Error().Err(err).Msg("Error reading from stream")
				fmt.Printf(">>> [SERVER] ERROR reading message: %v\n", err)
			}
			break
		}

		var response *SyncMessage
		var handleErr error

		// Wrap handler in panic recovery
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error().
						Interface("panic", r).
						Str("message_type", msg.Type).
						Msg("PANIC in message handler - recovering")
					fmt.Printf(">>> [SERVER] PANIC handling message type %s: %v\n", msg.Type, r)
					handleErr = fmt.Errorf("panic in handler: %v", r)

					// Try to send error response to client
					errorMsg := &SyncMessage{
						Type:      "ERROR",
						SenderID:  fs.host.ID().String(),
						Timestamp: time.Now().UTC().Unix(),
						Success:   false,
						Data:      []byte(fmt.Sprintf(`{"error":"panic in handler: %v"}`, r)),
					}
					writeMessage(writer, stream, errorMsg)
				}
			}()

			switch msg.Type {
			case TypeBatchRequest:
				response, handleErr = fs.handleBatchRequest(peerID, msg)
			case TypeBatchData:
				response, handleErr = fs.handleBatchData(peerID, msg)
			case TypeVerificationRequest:
				response, handleErr = fs.handleVerification(peerID)
			case TypeSyncComplete:
				response, handleErr = fs.handleSyncComplete(peerID, msg)
			case TypeHashMapExchangeSYNC:
				// Send chunks directly instead of returning response
				fmt.Printf(">>> [SERVER] Received HashMap exchange request from %s\n", peerID.String())
				fmt.Printf(">>> [SERVER] Client HashMap sizes - Main: %d, Accounts: %d\n",
					func() int {
						if msg.HashMap != nil && msg.HashMap.MAIN_HashMap != nil {
							return msg.HashMap.MAIN_HashMap.Size()
						}
						return 0
					}(),
					func() int {
						if msg.HashMap != nil && msg.HashMap.Accounts_HashMap != nil {
							return msg.HashMap.Accounts_HashMap.Size()
						}
						return 0
					}())
				handleErr = fs.handleHashMapExchangeSYNCChunked(peerID, msg, writer, reader, stream)
				response = nil // Chunks are sent directly
			case TypeHashMapChunkRequest:
				// Client requesting a specific chunk
				response, handleErr = fs.handleHashMapChunkRequest(peerID, msg)
			case TypeHashMapChunkAck:
				// Acknowledgment received, continue sending chunks
				response = nil // No response needed for ACK
			case TypeHashMapChunk:
				// Chunk received, send ACK
				ackMsg := &SyncMessage{
					Type:        TypeHashMapChunkAck,
					SenderID:    fs.host.ID().String(),
					Timestamp:   time.Now().UTC().Unix(),
					ChunkNumber: msg.ChunkNumber,
					Success:     true,
				}
				response = ackMsg
			case TypeHashMapChunkComplete:
				// All chunks received, no response needed
				response = nil
			case RequestFiletransfer: // This will trigger for the file transfer
				response, handleErr = fs.MakeAVROFile_Transfer(peerID, msg)
			default:
				log.Warn().Str("type", msg.Type).Msg("Unknown message type")
				handleErr = fmt.Errorf("unknown message type: %s", msg.Type)
			}
		}() // End panic recovery wrapper

		// Skip unknown message types
		if handleErr != nil && strings.Contains(handleErr.Error(), "unknown message type") {
			continue
		}

		if handleErr != nil {
			log.Error().Err(handleErr).
				Str("msg_type", msg.Type).
				Str("peer", peerID.String()).
				Msg("Error handling message")

			// Send abort message
			abortMsg := &SyncMessage{
				Type:         TypeSyncAbort,
				SenderID:     fs.host.ID().String(),
				ErrorMessage: handleErr.Error(),
				Timestamp:    time.Now().UTC().Unix(),
			}

			writeMessage(writer, stream, abortMsg)
			break
		}

		if response != nil {
			if err := writeMessage(writer, stream, response); err != nil {
				log.Error().Err(err).Msg("Failed to send response")
				break
			}
		}
	}
}

func CheckChecksum(temp *hashmap.HashMap, checksum string) bool {
	computedChecksum := temp.Fingerprint()
	return computedChecksum == checksum
}

// handleHashMapExchangeSYNCChunked sends HashMap data in chunks of 100 keys
func (fs *FastSync) handleHashMapExchangeSYNCChunked(peerID peer.ID, msg *SyncMessage, writer *bufio.Writer, reader *bufio.Reader, stream network.Stream) error {
	if fs.mainDB == nil {
		return fmt.Errorf("mainDB is nil - FastSync not properly initialized")
	}
	if fs.accountsDB == nil {
		return fmt.Errorf("accountsDB is nil - FastSync not properly initialized")
	}

	fmt.Println(">>> [SERVER] Received HashMap Exchange SYNC Request - starting chunked transfer")
	log.Info().
		Str("peer", peerID.String()).
		Msg("Received HashMap Exchange SYNC Request - sending chunks")

	// Checksum validation
	fmt.Println(">>> [SERVER] Validating client HashMap checksums...")
	if msg.HashMap_MetaData.Main_HashMap_MetaData.KeysCount > 0 {
		if !CheckChecksum(msg.HashMap.MAIN_HashMap, msg.HashMap_MetaData.Main_HashMap_MetaData.Checksum) {
			fmt.Println(">>> [SERVER] ERROR: Invalid main HashMap checksum")
			return fmt.Errorf("invalid main HashMap checksum")
		}
		fmt.Println(">>> [SERVER] ✓ Main HashMap checksum valid")
	}

	if msg.HashMap_MetaData.Accounts_HashMap_MetaData.KeysCount > 0 {
		if !CheckChecksum(msg.HashMap.Accounts_HashMap, msg.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum) {
			fmt.Println(">>> [SERVER] ERROR: Invalid accounts HashMap checksum")
			return fmt.Errorf("invalid accounts HashMap checksum")
		}
		fmt.Println(">>> [SERVER] ✓ Accounts HashMap checksum valid")
	}

	// OPTIMIZATION: For very large datasets, compute SYNC keys incrementally without building full HashMaps
	// This avoids loading millions of keys into memory
	fmt.Println(">>> [SERVER] Computing SYNC keys incrementally (streaming approach for large datasets)...")

	// Compute SYNC keys incrementally for Main DB
	fmt.Printf(">>> [SERVER] Computing Main DB SYNC keys incrementally...\n")
	fmt.Printf(">>> [SERVER] Client Main HashMap size: %d\n", func() int {
		if msg.HashMap.MAIN_HashMap != nil {
			return msg.HashMap.MAIN_HashMap.Size()
		}
		return 0
	}())
	SYNC_Keys_Main, err := fs.computeSyncKeysIncremental(fs.mainDB, msg.HashMap.MAIN_HashMap, MainDB)
	if err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to compute Main SYNC keys: %v\n", err)
		return err
	}
	fmt.Printf(">>> [SERVER] ✓ Main SYNC keys computed: %d keys (server has more, client needs %d)\n", len(SYNC_Keys_Main), len(SYNC_Keys_Main))

	// Compute SYNC keys incrementally for Accounts DB
	fmt.Printf(">>> [SERVER] Computing Accounts DB SYNC keys incrementally...\n")
	fmt.Printf(">>> [SERVER] Client Accounts HashMap size: %d\n", func() int {
		if msg.HashMap.Accounts_HashMap != nil {
			return msg.HashMap.Accounts_HashMap.Size()
		}
		return 0
	}())
	SYNC_Keys_Accounts, err := fs.computeSyncKeysIncremental(fs.accountsDB, msg.HashMap.Accounts_HashMap, AccountsDB)
	if err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to compute Accounts SYNC keys: %v\n", err)
		return err
	}
	fmt.Printf(">>> [SERVER] ✓ Accounts SYNC keys computed: %d keys (server has more, client needs %d)\n", len(SYNC_Keys_Accounts), len(SYNC_Keys_Accounts))

	// Calculate total chunks needed
	totalKeys := len(SYNC_Keys_Main) + len(SYNC_Keys_Accounts)
	totalChunks := (totalKeys + HashMapChunkSize - 1) / HashMapChunkSize

	fmt.Printf(">>> [SERVER] Preparing to send %d chunks (total %d keys, %d keys per chunk)\n", totalChunks, totalKeys, HashMapChunkSize)
	log.Info().
		Int("main_keys", len(SYNC_Keys_Main)).
		Int("accounts_keys", len(SYNC_Keys_Accounts)).
		Int("total_chunks", totalChunks).
		Msg("Sending HashMap in chunks")

	// Send metadata first
	fmt.Println(">>> [SERVER] Sending HashMap metadata...")
	metadataResponse := &SyncMessage{
		Type:        TypeHashMapExchangeSYNC,
		SenderID:    fs.host.ID().String(),
		Timestamp:   time.Now().UTC().Unix(),
		Data:        json.RawMessage([]byte(`"Message From Server"`)),
		TotalChunks: totalChunks,
		HashMap_MetaData: &HashMap_MetaData{
			Main_HashMap_MetaData: &MetaData{
				KeysCount: len(SYNC_Keys_Main),
			},
			Accounts_HashMap_MetaData: &MetaData{
				KeysCount: len(SYNC_Keys_Accounts),
			},
		},
	}

	if err := writeMessage(writer, stream, metadataResponse); err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to send HashMap metadata: %v\n", err)
		return fmt.Errorf("failed to send HashMap metadata: %w", err)
	}
	fmt.Println(">>> [SERVER] ✓ Metadata sent successfully")

	// Send chunks: Main HashMap first, then Accounts
	allKeys := append(SYNC_Keys_Main, SYNC_Keys_Accounts...)
	chunkNum := 0

	fmt.Printf(">>> [SERVER] Starting to send chunks (total: %d chunks)...\n", totalChunks)
	for i := 0; i < len(allKeys); i += HashMapChunkSize {
		end := i + HashMapChunkSize
		if end > len(allKeys) {
			end = len(allKeys)
		}

		chunkKeys := allKeys[i:end]
		chunkNum++

		fmt.Printf(">>> [SERVER] Sending chunk %d/%d (%d keys)...\n", chunkNum, totalChunks, len(chunkKeys))
		chunkMsg := &SyncMessage{
			Type:        TypeHashMapChunk,
			SenderID:    fs.host.ID().String(),
			Timestamp:   time.Now().UTC().Unix(),
			ChunkNumber: chunkNum,
			TotalChunks: totalChunks,
			ChunkKeys:   chunkKeys,
		}

		if err := writeMessage(writer, stream, chunkMsg); err != nil {
			fmt.Printf(">>> [SERVER] ERROR: Failed to send chunk %d: %v\n", chunkNum, err)
			return fmt.Errorf("failed to send chunk %d: %w", chunkNum, err)
		}
		fmt.Printf(">>> [SERVER] ✓ Chunk %d sent, waiting for ACK...\n", chunkNum)

		// Wait for ACK before sending next chunk
		fmt.Printf(">>> [SERVER] Waiting for ACK for chunk %d...\n", chunkNum)
		ackMsg, err := readMessage(reader, stream)
		if err != nil {
			fmt.Printf(">>> [SERVER] ERROR: Failed to read chunk ACK: %v\n", err)
			return fmt.Errorf("failed to read chunk ACK: %w", err)
		}

		if ackMsg.Type != TypeHashMapChunkAck || ackMsg.ChunkNumber != chunkNum {
			fmt.Printf(">>> [SERVER] ERROR: Invalid ACK for chunk %d (type: %s, chunkNum: %d)\n", chunkNum, ackMsg.Type, ackMsg.ChunkNumber)
			return fmt.Errorf("invalid ACK for chunk %d", chunkNum)
		}

		fmt.Printf(">>> [SERVER] ✓ Chunk %d/%d acknowledged (progress: %.1f%%)\n", chunkNum, totalChunks, float64(chunkNum)/float64(totalChunks)*100)
		log.Debug().
			Int("chunk", chunkNum).
			Int("total", totalChunks).
			Int("keys", len(chunkKeys)).
			Msg("Chunk sent and acknowledged")
	}

	// Send completion message
	fmt.Println(">>> [SERVER] All chunks sent, sending completion message...")
	completeMsg := &SyncMessage{
		Type:      TypeHashMapChunkComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Success:   true,
	}

	if err := writeMessage(writer, stream, completeMsg); err != nil {
		fmt.Printf(">>> [SERVER] ERROR: Failed to send chunk complete: %v\n", err)
		return fmt.Errorf("failed to send chunk complete: %w", err)
	}

	fmt.Printf(">>> [SERVER] ✓ All HashMap chunks sent successfully (%d chunks)\n", totalChunks)
	log.Info().
		Int("total_chunks", totalChunks).
		Msg("All HashMap chunks sent successfully")

	return nil
}

// handleHashMapChunkRequest handles a request for a specific chunk (for future use)
func (fs *FastSync) handleHashMapChunkRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// This can be used for retry logic if needed
	return nil, fmt.Errorf("not implemented")
}

// Legacy handler kept for backward compatibility
func (fs *FastSync) handleHashMapExchangeSYNC(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// This is now handled by handleHashMapExchangeSYNCChunked
	return nil, fmt.Errorf("use handleHashMapExchangeSYNCChunked instead")
}

func returnStream(fs *FastSync, peerID peer.ID) (network.Stream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	stream, err := fs.host.NewStream(ctx, peerID, SyncProtocolID)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (fs *FastSync) Phase1_SYNC(peerID peer.ID) (*SyncMessage, error) {

	// First make hashmap of Main DB
	fmt.Println(">>> [CLIENT] Phase1: Making Main HashMap...")
	MAIN_HashMap, err := fs.MakeHashMap_Default()
	if err != nil {
		return nil, err
	}
	fmt.Printf(">>> [CLIENT] Phase1: Main HashMap created with %d keys\n", MAIN_HashMap.Size())

	// Second make hashmap of Accounts DB
	fmt.Println(">>> [CLIENT] Phase1: Making Accounts HashMap...")
	ACCOUNTS_HashMap, err := fs.MakeHashMap_Accounts()
	if err != nil {
		return nil, err
	}
	fmt.Printf(">>> [CLIENT] Phase1: Accounts HashMap created with %d keys\n", ACCOUNTS_HashMap.Size())

	// Compute the Metadata for the both Maps

	ComputeCHECKSUM_MAIN_Value := MAIN_HashMap.Fingerprint()
	ComputeCHECKSUM_ACCOUNTS_Value := ACCOUNTS_HashMap.Fingerprint()

	fmt.Printf(">>> [CLIENT] Phase1: Sending HashMaps to server - Main: %d keys, Accounts: %d keys\n",
		MAIN_HashMap.Size(), ACCOUNTS_HashMap.Size())

	MAIN_HashMap_Metadata := &MetaData{
		KeysCount: MAIN_HashMap.Size(),
		Checksum:  ComputeCHECKSUM_MAIN_Value,
	}
	ACCOUNTS_HashMap_Metadata := &MetaData{
		KeysCount: ACCOUNTS_HashMap.Size(),
		Checksum:  ComputeCHECKSUM_ACCOUNTS_Value,
	}

	return &SyncMessage{
		Type:      TypeSyncRequest,
		SenderID:  fs.host.ID().String(),
		TxID:      0,
		Timestamp: time.Now().UTC().Unix(),
		Data:      json.RawMessage([]byte(`"Message From Client"`)),
		HashMap: &TypeHashMapExchange_Struct{
			MAIN_HashMap:     MAIN_HashMap,
			Accounts_HashMap: ACCOUNTS_HashMap,
		},
		HashMap_MetaData: &HashMap_MetaData{
			Main_HashMap_MetaData:     MAIN_HashMap_Metadata,
			Accounts_HashMap_MetaData: ACCOUNTS_HashMap_Metadata,
		},
	}, nil
}

func (fs *FastSync) HandleSync(peerID peer.ID) (*SyncMessage, error) {

	stream, err := returnStream(fs, peerID)
	if err != nil {
		return nil, err
	}
	defer (stream).Close()

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

	//phase1: Should make the HashMap of accounts and main DB
	Phase1, err := fs.Phase1_SYNC(peerID)
	if err != nil {
		return nil, fmt.Errorf("failed in Phase1_SYNC: %w", err)
	}

	//phase2: Should compute the Client IBLT and send it to the server
	// -> Client will send the IBLT to the server to get the SYNC_IBLT
	Phase2, MainChecksum, AccountChecksum, err := fs.Phase2_Sync(Phase1, peerID, stream, writer, reader)
	if err != nil {
		return nil, fmt.Errorf("failed in Phase2_Sync: %w", err)
	}

	// Check if the metadata checksums match
	// else retry to get the HashMap from the server
	// Only validate checksum if the diff HashMap is not empty.
	mainDiffInvalid := Phase2.HashMap.MAIN_HashMap.Size() > 0 && Phase2.HashMap.MAIN_HashMap.Fingerprint() != MainChecksum
	accountsDiffInvalid := Phase2.HashMap.Accounts_HashMap.Size() > 0 && Phase2.HashMap.Accounts_HashMap.Fingerprint() != AccountChecksum

	if mainDiffInvalid || accountsDiffInvalid {
		// retry 3 times to get the valid HashMap from the server
		log.Warn().
			Str("peer", peerID.String()).
			Msg("HashMap metadata checksum mismatch, retrying HashMap exchange")
		for i := range 3 {
			log.Debug().
				Str("peer", peerID.String()).
				Int("attempt", i+1).
				Msg("Retrying HashMap exchange")
			Phase2, MainChecksum, AccountChecksum, err = fs.Phase2_Sync(Phase1, peerID, stream, writer, reader)
			if err == nil &&
				(Phase2.HashMap_MetaData.Main_HashMap_MetaData.Checksum == MainChecksum &&
					Phase2.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum == AccountChecksum) {
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get valid HashMap from server: %w", err)
	}

	// Phase3: Request the AVRO file from the server
	// Server will send the AVRO file to the client

	err = fs.Phase3_FileRequest(Phase2, peerID, stream, writer, reader)
	if err != nil {
		// Retry initiating the file transfer again
		log.Warn().
			Str("peer", peerID.String()).
			Msg("Failed to transfer AVRO file, retrying file transfer")

		for i := range 3 {
			log.Debug().
				Str("peer", peerID.String()).
				Int("attempt", i+1).
				Msg("Retrying file transfer")

			err = fs.Phase3_FileRequest(Phase1, peerID, stream, writer, reader)
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("failed to transfer AVRO file after retries: %w", err)
		}
	}

	// Phase4: Push the Transactions from AVRO file to the DB
	fmt.Println(">>> [CLIENT] Starting Phase4 - Pushing data from AVRO files to database...")

	// Debug: Check Phase2 state
	fmt.Printf(">>> [CLIENT] DEBUG: Phase2 state - Main HashMap: %v, Accounts HashMap: %v\n",
		Phase2.HashMap != nil,
		Phase2.HashMap != nil)
	if Phase2.HashMap != nil {
		fmt.Printf(">>> [CLIENT] DEBUG: Phase2.MAIN_HashMap: %v, size: %d\n",
			Phase2.HashMap.MAIN_HashMap != nil,
			func() int {
				if Phase2.HashMap.MAIN_HashMap != nil {
					return Phase2.HashMap.MAIN_HashMap.Size()
				}
				return 0
			}())
		fmt.Printf(">>> [CLIENT] DEBUG: Phase2.Accounts_HashMap: %v, size: %d\n",
			Phase2.HashMap.Accounts_HashMap != nil,
			func() int {
				if Phase2.HashMap.Accounts_HashMap != nil {
					return Phase2.HashMap.Accounts_HashMap.Size()
				}
				return 0
			}())
	}

	// 1. Push the Main DB Transactions from AVRO file to the DB - if no maindb then skip
	mainHashMapSize := 0
	if Phase2.HashMap != nil && Phase2.HashMap.MAIN_HashMap != nil {
		mainHashMapSize = Phase2.HashMap.MAIN_HashMap.Size()
	}
	fmt.Printf(">>> [CLIENT] Checking Main DB - HashMap size: %d\n", mainHashMapSize)

	if Phase2.HashMap != nil && Phase2.HashMap.MAIN_HashMap != nil && Phase2.HashMap.MAIN_HashMap.Size() > 0 {
		fmt.Printf(">>> [CLIENT] Pushing Main DB data (%d keys) from AVRO file...\n", Phase2.HashMap.MAIN_HashMap.Size())
		if err := fs.PushDataToDB(Phase2, MainDB, "fastsync/.temp/defaultdb.avro"); err != nil {
			fmt.Printf(">>> [CLIENT] ERROR: Failed to push Main DB transactions: %v\n", err)
			log.Error().Err(err).Msg("Failed to push Main DB transactions")
			return nil, fmt.Errorf("failed to push Main DB transactions: %w", err)
		}
		fmt.Println(">>> [CLIENT] ✓ Successfully pushed Main DB transactions to immudb")
		log.Info().Msg("Successfully pushed Main DB transactions")
	} else {
		fmt.Println(">>> [CLIENT] Skipping Main DB push - no data to push")
		log.Info().Msg("Skipping Main DB push - no data to push")
	}

	// 2. Push the Accounts DB Transactions from AVRO file to the DB - if no accountsdb then skip
	fmt.Printf(">>> [CLIENT] Checking Accounts DB - HashMap size: %d\n", func() int {
		if Phase2.HashMap.Accounts_HashMap != nil {
			return Phase2.HashMap.Accounts_HashMap.Size()
		}
		return 0
	}())

	if Phase2.HashMap.Accounts_HashMap != nil && Phase2.HashMap.Accounts_HashMap.Size() > 0 {
		fmt.Printf(">>> [CLIENT] Pushing Accounts DB data (%d keys) from AVRO file...\n", Phase2.HashMap.Accounts_HashMap.Size())
		log.Info().
			Int("accounts_hashmap_size", Phase2.HashMap.Accounts_HashMap.Size()).
			Msg("Processing Accounts DB transactions")

		// Check if accounts database client is properly initialized
		if fs.accountsDB == nil {
			fmt.Println(">>> [CLIENT] ERROR: Accounts database client is nil - cannot restore accounts data")
			log.Error().Msg("Accounts database client is nil - cannot restore accounts data")
			return nil, fmt.Errorf("accounts database client is not initialized")
		}

		if err := fs.PushDataToDB(Phase2, AccountsDB, "fastsync/.temp/accountsdb.avro"); err != nil {
			fmt.Printf(">>> [CLIENT] ERROR: Failed to push Accounts DB transactions: %v\n", err)
			log.Error().Err(err).Msg("Failed to push Accounts DB transactions")
			return nil, fmt.Errorf("failed to push Accounts DB transactions: %w", err)
		}
		fmt.Println(">>> [CLIENT] ✓ Successfully pushed Accounts DB transactions to immudb")
		log.Info().Msg("Successfully pushed Accounts DB transactions")
	} else {
		fmt.Println(">>> [CLIENT] Skipping Accounts DB push - no data to push")
		log.Info().
			Bool("hashmap_nil", Phase2.HashMap.Accounts_HashMap == nil).
			Int("hashmap_size", func() int {
				if Phase2.HashMap.Accounts_HashMap != nil {
					return Phase2.HashMap.Accounts_HashMap.Size()
				}
				return 0
			}()).
			Msg("Skipping Accounts DB push - no data to push")
	}

	fmt.Println(">>> [CLIENT] ✓ Phase4 completed successfully")

	return &SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Success:   true,
	}, nil
}

func calculateBatchCount(startTxID, endTxID uint64) int {
	if startTxID >= endTxID {
		return 1 // At least one batch even if no diff
	}

	txDiff := endTxID - startTxID
	return int((txDiff + SyncBatchSize - 1) / SyncBatchSize)
}

// handleBatchRequest processes an initial sync request
func (fs *FastSync) handleBatchRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	fs.mutex.RLock()
	_, exists := fs.active[peerID]
	fs.mutex.RUnlock()
	if !exists {
		return nil, fmt.Errorf("no active sync for peer %s", peerID)
	}
	db := fs.getDB(msg.DBType)

	// Fetch only the desired prefixes
	entries, crdts, err := fs.getBatchData(db, msg.DBType)
	if err != nil {
		return nil, fmt.Errorf("failed to get batch data: %w", err)
	}

	// Cap entries & CRDTs
	const maxEntriesPerBatch = 100
	if len(entries) > maxEntriesPerBatch {
		entries = entries[:maxEntriesPerBatch]
	}
	const maxCRDTsPerBatch = 50
	if len(crdts) > maxCRDTsPerBatch {
		crdts = crdts[:maxCRDTsPerBatch]
	}

	// Serialize batch payload
	batchData := BatchData{Entries: entries, CRDTs: crdts, DBType: msg.DBType}
	dataBytes, err := json.Marshal(batchData)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize batch data: %w", err)
	}

	log.Info().
		Str("peer", peerID.String()).
		Int("batch", msg.BatchNumber).
		Int("entries", len(entries)).
		Int("crdts", len(crdts)).
		Str("db", dbTypeToString(msg.DBType)).
		Msg("Sending batch data")

	fmt.Printf(
		"Sending batch data → peer=%s batch=%d entries=%d crdts=%d db=%s\n",
		peerID.String(),
		msg.BatchNumber,
		len(entries),
		len(crdts),
		dbTypeToString(msg.DBType),
	)

	return &SyncMessage{
		Type:        TypeBatchData,
		SenderID:    fs.host.ID().String(),
		BatchNumber: msg.BatchNumber,
		Data:        dataBytes,
		DBType:      msg.DBType,
		Timestamp:   time.Now().UTC().Unix(),
	}, nil
}

func (fs *FastSync) getBatchData(
	db *config.PooledConnection,
	dbType DatabaseType,
) ([]KeyValueEntry, []json.RawMessage, error) {
	var entries []KeyValueEntry
	var crdts []json.RawMessage

	keys, err := DB_OPs.GetKeys(db, "", 0) // Get all keys for IBLT
	if err != nil {
		return nil, nil, err
	}

	for _, key := range keys {
		// pick the correct prefix
		switch dbType {
		case MainDB:
			// Include block: keys, latest_block, tx: keys, and tx_processed: keys
			if !strings.HasPrefix(key, "block:") &&
				key != "latest_block" &&
				!strings.HasPrefix(key, "tx:") &&
				!strings.HasPrefix(key, "tx_processed:") {
				continue
			}
		case AccountsDB:
			// Include address: keys (actual account data) and did: keys (DID references)
			if !strings.HasPrefix(key, "address:") && !strings.HasPrefix(key, "did:") {
				continue
			}
		}

		data, err := DB_OPs.Read(db, key)
		if err != nil {
			continue
		}

		entries = append(entries, KeyValueEntry{
			Key:       []byte(key),
			Value:     data,
			Timestamp: time.Now().UTC(),
			TxID:      0,
		})
	}

	return entries, crdts, nil
}

func (fs *FastSync) MakeAVROFile_Transfer(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// 1. Check if the message contains valid HashMap data
	if msg.HashMap == nil {
		return nil, fmt.Errorf("message is missing HashMap data")
	}

	if msg.HashMap_MetaData == nil || msg.HashMap_MetaData.Main_HashMap_MetaData == nil || msg.HashMap_MetaData.Accounts_HashMap_MetaData == nil {
		return nil, fmt.Errorf("message is missing HashMap metadata")
	}

	// 2. Ensure the temporary backup directory exists.
	if err := os.MkdirAll(AVRO_FILE_PATH, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	mainAVROpath := AVRO_FILE_PATH + "main.avro"
	accountsAVROpath := AVRO_FILE_PATH + "accounts.avro"

	// 2. Use defer to ensure backup files are cleaned up even if errors occur.
	defer func() {
		if err := os.Remove(mainAVROpath); err != nil && !os.IsNotExist(err) {
			log.Error().Err(err).Str("path", mainAVROpath).Msg("Failed to remove temporary backup file")
		}
		if err := os.Remove(accountsAVROpath); err != nil && !os.IsNotExist(err) {
			log.Error().Err(err).Str("path", accountsAVROpath).Msg("Failed to remove temporary backup file")
		}
	}()

	// 3. Create targeted backups using the client's HashMap.
	// Only create MainDB AVRO file if there are keys to sync
	fmt.Printf(">>> [SERVER] MakeAVROFile_Transfer: MainDB HashMap size: %d\n",
		func() int {
			if msg.HashMap != nil && msg.HashMap.MAIN_HashMap != nil {
				return msg.HashMap.MAIN_HashMap.Size()
			}
			return 0
		}())
	if msg.HashMap != nil && msg.HashMap.MAIN_HashMap != nil && msg.HashMap.MAIN_HashMap.Size() > 0 {
		mainCfg := DB_OPs.Config{
			Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
			Username:   config.DBUsername,
			Password:   config.DBPassword,
			Database:   config.DBName, // This is correct for main DB (defaultdb)
			OutputPath: mainAVROpath,
		}

		// Count block keys in HashMap for debugging
		blockKeyCount := 0
		txKeyCount := 0
		txProcessedKeyCount := 0
		latestBlockInHashMap := false
		totalKeysInHashMap := 0
		if msg.HashMap.MAIN_HashMap != nil {
			totalKeysInHashMap = msg.HashMap.MAIN_HashMap.Size()
			for _, key := range msg.HashMap.MAIN_HashMap.Keys() {
				if strings.HasPrefix(key, "block:") {
					blockKeyCount++
				} else if strings.HasPrefix(key, "tx:") {
					txKeyCount++
				} else if strings.HasPrefix(key, "tx_processed:") {
					txProcessedKeyCount++
				} else if key == "latest_block" {
					latestBlockInHashMap = true
				}
			}
		}
		fmt.Printf(">>> [SERVER] MainDB HashMap breakdown for AVRO creation:\n")
		fmt.Printf(">>> [SERVER]   Total keys: %d\n", totalKeysInHashMap)
		fmt.Printf(">>> [SERVER]   Block keys: %d\n", blockKeyCount)
		fmt.Printf(">>> [SERVER]   TX keys: %d\n", txKeyCount)
		fmt.Printf(">>> [SERVER]   TX_processed keys: %d\n", txProcessedKeyCount)
		fmt.Printf(">>> [SERVER]   latest_block: %v\n", latestBlockInHashMap)

		log.Info().
			Str("peer", peerID.String()).
			Int("keys", msg.HashMap.MAIN_HashMap.Size()).
			Int("block_keys", blockKeyCount).
			Bool("latest_block", latestBlockInHashMap).
			Msg("Creating targeted backup from MAIN HashMap")

		err := DB_OPs.BackupFromHashMap(mainCfg, msg.HashMap.MAIN_HashMap)
		if err != nil {
			return nil, fmt.Errorf("failed to backup main database: %w", err)
		}

		// Transfer the main DB backup file
		log.Info().Str("peer", peerID.String()).Str("file", mainAVROpath).Msg("Transferring main DB backup file")
		err = TransferAVROFile(fs.host, peerID, mainAVROpath, "fastsync/.temp/defaultdb.avro")
		if err != nil {
			return nil, fmt.Errorf("failed to transfer main database: %w", err)
		}
		log.Info().Int("keys", msg.HashMap.MAIN_HashMap.Size()).Msg("Successfully transferred main DB backup file")
	} else {
		log.Info().Msg("Skipping main DB AVRO file creation and transfer (no keys to sync - HashMap is empty)")
		fmt.Printf(">>> [SERVER] MainDB HashMap is empty (size: %d), skipping AVRO file creation\n",
			func() int {
				if msg.HashMap.MAIN_HashMap != nil {
					return msg.HashMap.MAIN_HashMap.Size()
				}
				return 0
			}())
	}

	// Process accounts DB if it has entries
	if msg.HashMap.Accounts_HashMap != nil && msg.HashMap.Accounts_HashMap.Size() > 0 {
		accountsCfg := DB_OPs.Config{
			Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
			Username:   config.DBUsername,
			Password:   config.DBPassword,
			Database:   config.AccountsDBName,
			OutputPath: accountsAVROpath,
		}

		log.Info().
			Str("peer", peerID.String()).
			Int("keys", msg.HashMap.Accounts_HashMap.Size()).
			Msg("Creating targeted backup from Accounts HashMap")

		err := DB_OPs.BackupFromHashMap(accountsCfg, msg.HashMap.Accounts_HashMap)
		if err != nil {
			return nil, fmt.Errorf("failed to backup accounts database: %w", err)
		}

		// Transfer the accounts DB backup file
		log.Info().Str("peer", peerID.String()).Str("file", accountsAVROpath).Msg("Transferring accounts DB backup file")
		err = TransferAVROFile(fs.host, peerID, accountsAVROpath, "fastsync/.temp/accountsdb.avro")
		if err != nil {
			return nil, fmt.Errorf("failed to transfer accounts database: %w", err)
		}
	}

	// 6. Send a completion message back on the control stream.
	log.Info().Str("peer", peerID.String()).Msg("File transfers complete. Sending SyncComplete.")
	return &SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Success:   true,
	}, nil
}
