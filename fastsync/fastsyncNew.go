// Package fastsync - Core FastSync Implementation with CRDT Integration
package fastsync

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

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/crdt"
	hashmap "gossipnode/crdt/HashMap"

	"github.com/JupiterMetaLabs/ion"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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
	Type               string                      `json:"type"`
	SenderID           string                      `json:"sender_id"`
	TxID               uint64                      `json:"tx_id,omitempty"`
	StartTxID          uint64                      `json:"start_tx_id,omitempty"`
	EndTxID            uint64                      `json:"end_tx_id,omitempty"`
	BatchNumber        int                         `json:"batch_number,omitempty"`
	TotalBatches       int                         `json:"total_batches,omitempty"`
	ChunkNumber        int                         `json:"chunk_number,omitempty"` // New: Chunk number for HashMap transfer
	TotalChunks        int                         `json:"total_chunks,omitempty"` // New: Total chunks for HashMap transfer
	ChunkKeys          []string                    `json:"chunk_keys,omitempty"`   // New: Keys in this chunk
	MerkleRoot         MerkleRoot                  `json:"merkle_root,omitempty"`
	KeysCount          int                         `json:"keys_count,omitempty"`
	Data               json.RawMessage             `json:"data,omitempty"`
	Success            bool                        `json:"success,omitempty"`
	ErrorMessage       string                      `json:"error_message,omitempty"`
	Timestamp          int64                       `json:"timestamp"`
	DBType             DatabaseType                `json:"db_type,omitempty"`
	HashMap            *TypeHashMapExchange_Struct `json:"hashmap_sync,omitempty"`
	HashMap_MetaData   *HashMap_MetaData           `json:"hashmap_meta_data,omitempty"`
	ReconciliationType string                      `json:"reconciliation_type,omitempty"` // MERKLE or HASHMAP
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
	Logger     *ion.Ion
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
	RequestFiletransfer       = "REQUEST_FILE_TRANSFER" // Request for file transfer
	TypeFirstSyncRequest      = "FIRST_SYNC_REQ"        // Request for first sync (all data)
	TypeFirstSyncResponse     = "FIRST_SYNC_RESP"       // Response to first sync request

	// Reconciliation types
	TypeReconciliationRequest  = "RECONCILIATION_REQ"
	TypeReconciliationResponse = "RECONCILIATION_RESP"
	ReconTypeMerkle            = "MERKLE"
	ReconTypeHashMap           = "HASHMAP"

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

	ctx := context.Background()
	defer ctx.Done()

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
			logger().Debug(context.Background(), "Stopping scan after key outside prefix range",
				ion.String("key", key),
				ion.String("prefix", prefix))
			break
		}
	}

	return keys, nil
}

// getAllUniquePrefixes scans the database and extracts all unique prefixes from keys
// A prefix is defined as everything before the first colon (:) or the entire key if no colon exists
// Uses a more efficient approach: sample keys from different prefixes instead of scanning all keys
func (fs *FastSync) getAllUniquePrefixes(db *config.PooledConnection, dbType DatabaseType) ([]string, error) {
	logger().Debug(context.Background(), "Discovering prefixes by sampling keys from database")

	prefixSet := make(map[string]bool)
	batchSize := 20
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
		logger().Debug(context.Background(), "Sampling from common prefixes, doing limited full scan",
			ion.Int("prefixes_found", len(prefixSet)))

		var lastKey []byte
		batchNum := 0
		seenKeys := make(map[string]bool)

		for totalKeysScanned < maxKeysToScan {
			batchNum++
			keys, err := fs.getKeysBatchIncremental(db, "", batchSize, lastKey)
			if err != nil {
				logger().Debug(context.Background(), "Failed to scan batch",
					ion.Int("batch_num", batchNum),
					ion.Err(err))
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
				logger().Debug(context.Background(), "Detected duplicate keys, stopping scan",
					ion.Int("batch_num", batchNum))
				break
			}

			totalKeysScanned += len(keys)

			if batchNum%10 == 0 {
				logger().Debug(context.Background(), "Prefix discovery progress",
					ion.Int("batch", batchNum),
					ion.Int("keys_scanned", totalKeysScanned),
					ion.Int("unique_prefixes", len(prefixSet)))
			}

			// If we got fewer than batch size, we're done
			if len(keys) < batchSize {
				break
			}

			// Set last key for next iteration
			newLastKey := []byte(keys[len(keys)-1])

			// Check if lastKey is the same as before (loop detection)
			if lastKey != nil && string(newLastKey) == string(lastKey) {
				logger().Debug(context.Background(), "Detected same last key, stopping scan")
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

	logger().Debug(context.Background(), "Prefix discovery complete",
		ion.Int("keys_scanned", totalKeysScanned),
		ion.Int("unique_prefixes", len(prefixes)))

	return prefixes, nil
}

// computeSyncKeysIncremental computes SYNC keys by streaming through database keys
// without building the full server HashMap. This is much more memory-efficient for large datasets.
func (fs *FastSync) computeSyncKeysIncremental(db *config.PooledConnection, clientHashMap *hashmap.HashMap, dbType DatabaseType) ([]string, string, error) {
	var syncKeys []string
	var prefixes []string
	serverFullState := hashmap.New()

	// Dynamically discover all prefixes in the database instead of hardcoding
	dbTypeStr := "AccountsDB"
	if dbType == MainDB {
		dbTypeStr = "MainDB"
	}
	logger().Debug(context.Background(), "Discovering prefixes dynamically",
		ion.String("db_type", dbTypeStr))

	discoveredPrefixes, err := fs.getAllUniquePrefixes(db, dbType)
	if err != nil {
		logger().Debug(context.Background(), "Failed to discover prefixes dynamically, falling back to hardcoded",
			ion.Err(err))
		// Fallback to hardcoded prefixes if discovery fails
		if dbType == MainDB {
			prefixes = []string{"block:", "tx:", "tx_processed:", "tx_processing:"}
		} else {
			prefixes = []string{"address:", "did:"}
		}
	} else {
		logger().Debug(context.Background(), "Discovered prefixes",
			ion.Int("count", len(discoveredPrefixes)))
		prefixes = discoveredPrefixes
		// Filter out latest_block from prefixes (it's handled separately)
		filteredPrefixes := make([]string, 0, len(prefixes))
		for _, prefix := range prefixes {
			if prefix != "latest_block" {
				filteredPrefixes = append(filteredPrefixes, prefix)
			}
		}
		prefixes = filteredPrefixes

		// Check if tx_processing is in the list
		hasTxProcessing := false
		for _, prefix := range prefixes {
			if prefix == "tx_processing:" {
				hasTxProcessing = true
				break
			}
		}
		if !hasTxProcessing && dbType == MainDB {
			logger().Debug(context.Background(), "tx_processing prefix not found, adding manually")
			prefixes = append(prefixes, "tx_processing:")
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
						serverFullState.Insert("latest_block")
						logger().Debug(context.Background(), "latest_block will be included",
							ion.Uint64("server_block", serverLatestBlock))
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
							serverFullState.Insert("latest_block")
							logger().Debug(context.Background(), "latest_block will be included",
								ion.Uint64("server_block", serverLatestBlock),
								ion.String("missing_client_block", clientBlockKey))
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
								logger().Debug(context.Background(), "Skipping latest_block - client has newer blocks",
									ion.Uint64("server_block", serverLatestBlock))
							} else {
								// Client has same or older blocks, include latest_block for safety
								syncKeys = append(syncKeys, "latest_block")
								serverFullState.Insert("latest_block")
								logger().Debug(context.Background(), "latest_block will be included",
									ion.Uint64("server_block", serverLatestBlock))
							}
						}
					}
				} else {
					logger().Debug(context.Background(), "Failed to parse server's latest_block value",
						ion.Err(err))
				}
			} else {
				logger().Debug(context.Background(), "Failed to read server's latest_block",
					ion.Err(err))
			}
		} else {
			logger().Debug(context.Background(), "Server does not have latest_block key")
		}
	}

	clientHashMapSize := 0
	if clientHashMap != nil {
		clientHashMapSize = clientHashMap.Size()
	}
	logger().Debug(context.Background(), "Checking prefixes for SYNC keys",
		ion.Int("prefixes", len(prefixes)),
		ion.Int("client_hashmap_size", clientHashMapSize))

	// Track block key statistics across all prefixes
	totalBlockKeysChecked := 0
	totalBlockKeysInSync := 0
	totalBlockKeysSkipped := 0

	// Process each prefix incrementally
	for prefixIdx, prefix := range prefixes {
		logger().Debug(context.Background(), "Processing prefix",
			ion.Int("prefix_index", prefixIdx+1),
			ion.Int("total_prefixes", len(prefixes)),
			ion.String("prefix", prefix))

		batchSize := 100
		var lastKey []byte
		batchNum := 0
		totalChecked := 0
		keysInClientHashMap := 0
		prefixBlockKeysChecked := 0
		prefixBlockKeysInSync := 0
		prefixBlockKeysSkipped := 0

		// Use smaller batch size for large prefixes to avoid gRPC message size limits
		// block: keys can be very large (each block contains full transaction data)
		// gRPC has a 20MB message size limit, so we use much smaller batches for block: prefix
		// Note: Scan returns entries (key+value), so even small batches can be large for blocks
		// Error showed 97MB for 1000 blocks, so ~97KB per block. Using 20 blocks = ~2MB (safe margin)
		actualBatchSize := batchSize
		switch prefix {
		case "block:":
			actualBatchSize = 20 // Very small batch for blocks - each block ~97KB, so 20 blocks = ~2MB (well under 20MB limit)
			logger().Debug(context.Background(), "Using reduced batch size for block prefix",
				ion.Int("batch_size", actualBatchSize))
		case "tx:", "tx_processed:":
			actualBatchSize = 20 // Medium batch for transactions
		case "tx_processing:":
			actualBatchSize = 20 // Small prefix, can use larger batches
		case "address:", "did:":
			actualBatchSize = 20 // AccountsDB entries - using smaller batch to ensure all DIDs/addresses are synced
			logger().Debug(context.Background(), "Using batch size for AccountsDB prefix",
				ion.Int("batch_size", actualBatchSize),
				ion.String("prefix", prefix))
		}

		for {
			batchNum++
			if batchNum%100 == 0 {
				logger().Debug(context.Background(), "Progress scanning prefix",
					ion.String("prefix", prefix),
					ion.Int("batch", batchNum),
					ion.Int("keys_checked", totalChecked),
					ion.Int("sync_keys_found", len(syncKeys)),
					ion.Int("keys_in_client", keysInClientHashMap))
			}

			// Get batch of keys from database
			keys, err := fs.getKeysBatchIncremental(db, prefix, actualBatchSize, lastKey)
			if err != nil {
				logger().Debug(context.Background(), "Failed to get batch",
					ion.String("prefix", prefix),
					ion.Err(err))
				return nil, "", fmt.Errorf("failed to get keys batch for prefix %s: %w", prefix, err)
			}
			rawKeysCount := len(keys)

			if len(keys) == 0 {
				logger().Debug(context.Background(), "Finished processing prefix",
					ion.String("prefix", prefix),
					ion.Int("total_batches", batchNum),
					ion.Int("keys_checked", totalChecked),
					ion.Int("sync_keys_found", len(syncKeys)),
					ion.Int("keys_in_client", keysInClientHashMap))
				break
			}

			// CRITICAL: Skip the first key if it matches lastKey (SeekKey can include the key itself)
			// This prevents duplicates when using SeekKey with lexicographically ordered keys
			// Immudb returns keys lexicographically: block:1, block:10, block:100, block:1000, etc.
			// When using SeekKey, it may include the SeekKey itself in the next batch
			if lastKey != nil && len(keys) > 0 && string(keys[0]) == string(lastKey) {
				logger().Debug(context.Background(), "Skipping duplicate key from previous batch",
					ion.String("key", keys[0]))
				keys = keys[1:]
				if len(keys) == 0 {
					// If we skipped the only key, get next batch
					continue
				}
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
				serverFullState.Insert(key)

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
				logger().Debug(context.Background(), "Block keys statistics in batch",
					ion.Int("batch", batchNum),
					ion.Int("checked", blockKeysChecked),
					ion.Int("sync", blockKeysInSync),
					ion.Int("skipped", blockKeysSkipped))
			}

			// If we got fewer than batch size, we're done with this prefix
			// CRITICAL: Must use rawKeysCount and actualBatchSize
			if rawKeysCount < actualBatchSize {
				logger().Debug(context.Background(), "Finished processing prefix",
					ion.String("prefix", prefix),
					ion.Int("total_batches", batchNum),
					ion.Int("keys_checked", totalChecked),
					ion.Int("sync_keys_found", len(syncKeys)),
					ion.Int("keys_in_client", keysInClientHashMap))
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
			logger().Debug(context.Background(), "Prefix block: complete",
				ion.Int("keys_checked", totalChecked),
				ion.Int("sync_keys_found", len(syncKeys)),
				ion.Int("keys_in_client", keysInClientHashMap))
			logger().Debug(context.Background(), "Block keys statistics",
				ion.Int("checked", prefixBlockKeysChecked),
				ion.Int("sync", prefixBlockKeysInSync),
				ion.Int("skipped", prefixBlockKeysSkipped))
		} else {
			logger().Debug(context.Background(), "Prefix complete",
				ion.String("prefix", prefix),
				ion.Int("keys_checked", totalChecked),
				ion.Int("sync_keys_found", len(syncKeys)),
				ion.Int("keys_in_client", keysInClientHashMap))
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

	logger().Debug(context.Background(), "Incremental SYNC computation complete",
		ion.Int("total_sync_keys", len(syncKeys)),
		ion.Int("block_keys_in_sync", blockKeyCount),
		ion.Bool("has_latest_block", hasLatestBlock))

	if dbType == MainDB {
		logger().Debug(context.Background(), "Block key statistics",
			ion.Int("total_checked", totalBlockKeysChecked),
			ion.Int("in_sync", totalBlockKeysInSync),
			ion.Int("skipped", totalBlockKeysSkipped))

		if totalBlockKeysChecked > 0 && totalBlockKeysInSync == 0 && totalBlockKeysSkipped > 0 {
			logger().Debug(context.Background(), "Client HashMap contains all block keys but none added to SYNC",
				ion.Int("block_keys_skipped", totalBlockKeysSkipped))
		}

		if blockKeyCount == 0 && !hasLatestBlock {
			logger().Debug(context.Background(), "No block keys or latest_block in SYNC keys - may indicate stale HashMap")
		}
	}

	dbTypeForLog := "AccountsDB"
	if dbType == MainDB {
		dbTypeForLog = "MainDB"
	}
	fingerprint := serverFullState.Fingerprint()
	logger().Debug(context.Background(), "Full state fingerprint",
		ion.String("db_type", dbTypeForLog),
		ion.String("fingerprint", fingerprint))

	return syncKeys, fingerprint, nil
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
	logger().Info(context.Background(), "Making Default HashMap")
	MAP := hashmap.New()

	// Get block: keys
	logger().Info(context.Background(), "Getting block keys")
	blockKeys, err := GetDBData_Default(fs.mainDB, "block:")
	if err != nil {
		logger().Debug(context.Background(), "Failed to get block keys",
			ion.Err(err))
		return nil, err
	}
	logger().Debug(context.Background(), "Got block keys",
		ion.Int("count", len(blockKeys)))
	for _, key := range blockKeys {
		MAP.Insert(key)
	}

	// Get tx: keys
	logger().Info(context.Background(), "Getting tx keys")
	txKeys, err := GetDBData_Default(fs.mainDB, "tx:")
	if err != nil {
		logger().Debug(context.Background(), "Failed to get tx keys",
			ion.Err(err))
		return nil, err
	}
	logger().Debug(context.Background(), "Got tx keys",
		ion.Int("count", len(txKeys)))
	for _, key := range txKeys {
		MAP.Insert(key)
	}

	// Get tx_processed: keys
	logger().Info(context.Background(), "Getting tx_processed keys")
	txProcessedKeys, err := GetDBData_Default(fs.mainDB, "tx_processed:")
	if err != nil {
		logger().Debug(context.Background(), "Failed to get tx_processed keys",
			ion.Err(err))
		return nil, err
	}
	logger().Debug(context.Background(), "Got tx_processed keys",
		ion.Int("count", len(txProcessedKeys)))
	for _, key := range txProcessedKeys {
		MAP.Insert(key)
	}

	// Check for latest_block key explicitly
	logger().Info(context.Background(), "Checking for latest_block key")
	exists, err := DB_OPs.Exists(fs.mainDB, "latest_block")
	if err == nil && exists {
		MAP.Insert("latest_block")
		logger().Info(context.Background(), "Added latest_block key")
	}

	logger().Debug(context.Background(), "Default HashMap complete",
		ion.Int("total_keys", MAP.Size()))

	// CRITICAL: Validate HashMap keys exist in DB to remove stale keys
	// This ensures the HashMap accurately reflects the current DB state
	// This is especially important for subsequent syncs where the HashMap might contain stale keys
	// Validation removed as it was redundant (keys just fetched from DB) and caused timeouts
	return MAP, nil
}

func (fs *FastSync) MakeHashMap_Accounts() (*hashmap.HashMap, error) {
	logger().Info(context.Background(), "Making Accounts HashMap")
	MAP := hashmap.New()

	// Get address: keys (actual account data)
	logger().Info(context.Background(), "Getting address keys")
	addressKeys, err := GetDBData_Accounts(fs.accountsDB, "address:")
	if err != nil {
		logger().Debug(context.Background(), "Failed to get address keys",
			ion.Err(err))
		return nil, err
	}
	logger().Debug(context.Background(), "Got address keys",
		ion.Int("count", len(addressKeys)))
	for _, key := range addressKeys {
		MAP.Insert(key)
	}

	// Get did: keys (DID references to accounts)
	logger().Info(context.Background(), "Getting did keys")
	didKeys, err := GetDBData_Accounts(fs.accountsDB, "did:")
	if err != nil {
		logger().Debug(context.Background(), "Failed to get did keys",
			ion.Err(err))
		return nil, err
	}
	logger().Debug(context.Background(), "Got did keys",
		ion.Int("count", len(didKeys)))
	for _, key := range didKeys {
		MAP.Insert(key)
	}

	logger().Debug(context.Background(), "Accounts HashMap complete",
		ion.Int("total_keys", MAP.Size()))

	// CRITICAL: Validate HashMap keys exist in DB to remove stale keys
	// This ensures the HashMap accurately reflects the current DB state
	// Validation removed as it was redundant (keys just fetched from DB) and caused timeouts
	return MAP, nil
}

// UPDATED: NewFastSync now initializes CRDT engine for conflict-free synchronization
// This enables proper handling of// NewFastSync creates a new FastSync instance with CRDT support
func NewFastSync(h host.Host, mainDB, accountsDB *config.PooledConnection, logger *ion.Ion) *FastSync {
	// Initialize CRDT engine with memory limit (e.g., 50MB)
	// This ensures we don't consume too much memory with operation history
	// The engine handles cleanup automatically when limit is reached
	engine := crdt.NewEngineMemOnly(50 * 1024 * 1024)

	fs := &FastSync{
		host:       h,
		mainDB:     mainDB,
		accountsDB: accountsDB,
		active:     make(map[peer.ID]*syncState),
		crdtEngine: engine,
		Logger:     logger,
	}

	h.SetStreamHandler(SyncProtocolID, fs.handleStream)

	// Log initialization
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "FastSync service initialized with CRDT engine",
			ion.String("protocol_id", string(SyncProtocolID)),
			ion.Int("crdt_memory_limit_mb", 50))
	}

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

	logger().Info(context.Background(), "Starting CRDT export for synchronization")

	// 1. Get all CRDT objects from the memory store
	allCRDTs := fs.crdtEngine.GetAllCRDTs()

	if len(allCRDTs) == 0 {
		logger().Info(context.Background(), "No CRDTs to export")
		return []json.RawMessage{}, nil
	}

	logger().Info(context.Background(), "Exporting CRDTs",
		ion.Int("count", len(allCRDTs)))

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
			logger().Warn(context.Background(), "Unknown CRDT type, skipping export",
				ion.String("key", key))
			continue
		}

		// Serialize CRDT data to JSON
		crdtData, err := json.Marshal(crdtObj)
		if err != nil {
			logger().Error(context.Background(), "Failed to marshal CRDT data", err,
				ion.String("key", key),
				ion.String("type", crdtType))
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
			logger().Error(context.Background(), "Failed to marshal CRDT wrapper", err,
				ion.String("key", key),
				ion.String("type", crdtType))
			continue
		}

		exportedCRDTs = append(exportedCRDTs, json.RawMessage(wrapperData))

		logger().Debug(context.Background(), "Exported CRDT",
			ion.String("key", key),
			ion.String("type", crdtType),
			ion.Int("size", len(wrapperData)))
	}

	logger().Info(context.Background(), "CRDT export completed",
		ion.Int("total", len(allCRDTs)),
		ion.Int("exported", len(exportedCRDTs)))

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
		logger().Info(context.Background(), "No CRDTs to import")
		return nil
	}

	logger().Info(context.Background(), "Starting CRDT import",
		ion.Int("count", len(crdtData)))

	// Use the existing storeCRDTs function which already handles the import logic
	err := fs.storeCRDTs(fs.crdtEngine, crdtData)
	if err != nil {
		logger().Error(context.Background(), "Failed to import CRDTs", err)
		return fmt.Errorf("failed to import CRDTs: %w", err)
	}

	logger().Info(context.Background(), "CRDT import completed successfully",
		ion.Int("imported", len(crdtData)))
	return nil
}

func readMessage(reader *bufio.Reader, stream network.Stream) (*SyncMessage, error) {
	// Set initial deadline - use extended timeout for debugging (120 minutes)
	// This allows for very long HashMap computation times (Phase 1)
	extendedTimeout := 120 * time.Minute
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
				logger().Warn(context.Background(), "Failed to extend read deadline, continuing", ion.Err(err))
			} else {
				lastDeadlineUpdate = time.Now().UTC()
				logger().Debug(context.Background(), "Extended read deadline for large message",
					ion.Int("chunks_read", chunkCount),
					ion.Int("bytes_read", len(msgData)))
			}
		}

		chunk, isPrefix, err := reader.ReadLine()
		if err != nil {
			logger().Error(context.Background(), "Failed to read message chunk", err)
			return nil, fmt.Errorf("failed to read message: %w", err)
		}

		msgData = append(msgData, chunk...)
		chunkCount++

		if !isPrefix {
			break
		}
	}

	// Add debugging to show message size and content
	logger().Info(context.Background(), "Read message data",
		ion.Int("bytes", len(msgData)),
		ion.Int("chunks", chunkCount))

	if len(msgData) == 0 {
		return nil, fmt.Errorf("received empty message")
	}

	var msg SyncMessage
	if err := json.Unmarshal(msgData, &msg); err != nil {
		logger().Error(context.Background(), "Failed to unmarshal message", err,
			ion.Int("bytes", len(msgData)))
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
		logger().Error(context.Background(), "Failed to marshal message", err)
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	actualSize := len(msgBytes)
	logger().Info(context.Background(), "Writing message data",
		ion.Int("bytes", actualSize),
		ion.Int("estimated_keys", estimatedSize/100))

	// Set write deadline - extend for large messages
	deadline := time.Now().UTC().Add(ResponseTimeout)
	if estimatedSize > 1000000 || actualSize > 1000000 {
		// For very large messages (>1MB), give extra time
		deadline = time.Now().UTC().Add(ResponseTimeout * 2)
		logger().Info(context.Background(), "Large message detected, using extended timeout",
			ion.Int("size_bytes", actualSize))
	}

	if err := stream.SetWriteDeadline(deadline); err != nil {
		logger().Error(context.Background(), "Failed to set write deadline", err)
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
				logger().Error(context.Background(), "Failed to write message chunk", err,
					ion.Int("offset", i))
				return fmt.Errorf("failed to write message chunk: %w", err)
			}

			// Periodically flush and extend deadline during long writes
			currentChunk := i / chunkSize
			if currentChunk%10 == 0 {
				if err := writer.Flush(); err != nil {
					logger().Error(context.Background(), "Failed to flush during chunk write", err)
				}
				deadline = time.Now().UTC().Add(ResponseTimeout)
				stream.SetWriteDeadline(deadline)

				// Log progress every 50 chunks (approx 3.2MB)
				if currentChunk%50 == 0 {
					progress := float64(i) / float64(len(msgBytes)) * 100
					logger().Debug(context.Background(), "Sending large message",
						ion.Float64("progress_percent", progress),
						ion.Int("bytes_sent", i),
						ion.Int("total_bytes", len(msgBytes)))
				}
			}
		}
	} else {
		if _, err := writer.Write(msgBytes); err != nil {
			logger().Error(context.Background(), "Failed to write message bytes", err)
			return fmt.Errorf("failed to write message: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		logger().Error(context.Background(), "Failed to flush message", err)
		return fmt.Errorf("failed to flush message: %w", err)
	}

	logger().Info(context.Background(), "Message written and flushed successfully")
	return nil
}

func retry(operation func() error) error {
	var lastErr error
	backoff := RetryDelay

	for attempt := 0; attempt < MaxRetries; attempt++ {
		if attempt > 0 {
			logger().Debug(context.Background(), "Retrying operation",
				ion.Int("attempt", attempt+1),
				ion.Int64("delay_ms", backoff.Milliseconds()))
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
			logger().Debug(context.Background(), "PANIC in handleStream",
				ion.String("panic", fmt.Sprintf("%v", r)))
		}
		stream.Close()
	}()

	peerID := stream.Conn().RemotePeer()
	remote := stream.Conn().RemoteMultiaddr().String()

	logger().Info(context.Background(), "Received sync stream",
		ion.String("peer", peerID.String()),
		ion.String("remote", remote))

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

	// State for chunked HashMap assembly
	var clientHashMapBuilder *TypeHashMapExchange_Struct

	for {
		msg, err := readMessage(reader, stream)
		if err != nil {
			// Check for various forms of EOF/Reset
			errMsg := err.Error()
			if err == io.EOF || strings.Contains(errMsg, "EOF") || strings.Contains(errMsg, "stream reset") {
				// EOF is expected when client closes stream after sync completion
				logger().Debug(context.Background(), "Stream closed by client - sync completed")
			} else {
				logger().Debug(context.Background(), "Error reading from stream",
					ion.Err(err))
			}
			break
		}

		var response *SyncMessage
		var handleErr error

		// Wrap handler in panic recovery
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger().Debug(context.Background(), "PANIC in message handler",
						ion.String("message_type", msg.Type),
						ion.String("panic", fmt.Sprintf("%v", r)))
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
				// Start of valid HashMap exchange
				// If msg chunks are involved, we prepare the builder
				if msg.TotalChunks > 0 {
					logger().Debug(context.Background(), "Starting chunked HashMap exchange",
						ion.Int("expected_chunks", msg.TotalChunks),
						ion.String("peer", peerID.String()))

					// Initialize builder
					clientHashMapBuilder = &TypeHashMapExchange_Struct{
						MAIN_HashMap:     hashmap.New(),
						Accounts_HashMap: hashmap.New(),
					}

					// No immediate response needed, client will start sending chunks
					response = nil
				} else {
					// Legacy/Standard flow (single message)
					logger().Debug(context.Background(), "Received HashMap exchange request",
						ion.String("peer", peerID.String()))
					handleErr = fs.handleHashMapExchangeSYNCChunked(peerID, msg, writer, reader, stream)
					response = nil // Chunks/Response sent directly in handler
				}

			case TypeHashMapChunk:
				// Accumulate chunks
				if clientHashMapBuilder == nil {
					// Auto-initialize if missed start (resilience)
					clientHashMapBuilder = &TypeHashMapExchange_Struct{
						MAIN_HashMap:     hashmap.New(),
						Accounts_HashMap: hashmap.New(),
					}
				}

				// Decode keys from chunk
				// Assume msg.Data contains JSON []string
				var keys []string
				if err := json.Unmarshal(msg.Data, &keys); err != nil {
					logger().Error(context.Background(), "Failed to unmarshal keys from chunk", err)
				} else {
					// Identify DB type for keys
					if msg.DBType == MainDB {
						for _, k := range keys {
							clientHashMapBuilder.MAIN_HashMap.Insert(k)
						}
					} else {
						for _, k := range keys {
							clientHashMapBuilder.Accounts_HashMap.Insert(k)
						}
					}
				}

				// Chunk received, send ACK
				ackMsg := &SyncMessage{
					Type:        TypeHashMapChunkAck,
					SenderID:    fs.host.ID().String(),
					Timestamp:   time.Now().UTC().Unix(),
					ChunkNumber: msg.ChunkNumber,
					Success:     true,
				}
				if err := writeMessage(writer, stream, ackMsg); err != nil {
					logger().Error(context.Background(), "Failed to write ACK", err)
				}
				response = nil

			case TypeReconciliationRequest:
				// Handle reconciliation request (Pre-Sync Check)
				if err := fs.handleReconciliation(peerID, msg, stream, writer); err != nil {
					logger().Error(context.Background(), "Failed to handle reconciliation request", err)
				}
				response = nil

			case TypeHashMapChunkComplete:
				logger().Debug(context.Background(), "Chunk assembly complete, processing full HashMap exchange")

				// Ensure builder is not nil
				if clientHashMapBuilder == nil {
					clientHashMapBuilder = &TypeHashMapExchange_Struct{
						MAIN_HashMap:     hashmap.New(),
						Accounts_HashMap: hashmap.New(),
					}
				}

				// Construct synthetic exchange message
				syntheticMsg := &SyncMessage{
					Type:    TypeHashMapExchangeSYNC,
					HashMap: clientHashMapBuilder,
					HashMap_MetaData: &HashMap_MetaData{
						Main_HashMap_MetaData: &MetaData{
							KeysCount: clientHashMapBuilder.MAIN_HashMap.Size(),
							Checksum:  clientHashMapBuilder.MAIN_HashMap.Fingerprint(),
						},
						Accounts_HashMap_MetaData: &MetaData{
							KeysCount: clientHashMapBuilder.Accounts_HashMap.Size(),
							Checksum:  clientHashMapBuilder.Accounts_HashMap.Fingerprint(),
						},
					},
				}
				handleErr = fs.handleHashMapExchangeSYNCChunked(peerID, syntheticMsg, writer, reader, stream)
				response = nil
				// Reset builder
				clientHashMapBuilder = nil

			case TypeHashMapChunkAck:
				// Acknowledgment received (should be client side, but good to have)
				response = nil
			case RequestFiletransfer: // This will trigger for the file transfer
				response, handleErr = fs.MakeAVROFile_Transfer(peerID, msg)
			default:
				logger().Warn(context.Background(), "Unknown message type",
					ion.String("type", msg.Type))
				handleErr = fmt.Errorf("unknown message type: %s", msg.Type)
			}
		}() // End panic recovery wrapper

		// Skip unknown message types
		if handleErr != nil && strings.Contains(handleErr.Error(), "unknown message type") {
			continue
		}

		if handleErr != nil {
			logger().Error(context.Background(), "Error handling message", handleErr,
				ion.String("msg_type", msg.Type),
				ion.String("peer", peerID.String()))

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
				logger().Error(context.Background(), "Failed to send response", err)
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
	logger().Info(context.Background(), "Received HashMap Exchange SYNC Request - starting chunked transfer",
		ion.String("peer", peerID.String()))

	// Checksum validation
	logger().Info(context.Background(), "Validating client HashMap checksums")
	if msg.HashMap_MetaData.Main_HashMap_MetaData.KeysCount > 0 {
		if !CheckChecksum(msg.HashMap.MAIN_HashMap, msg.HashMap_MetaData.Main_HashMap_MetaData.Checksum) {
			logger().Info(context.Background(), "Invalid main HashMap checksum")
			return fmt.Errorf("invalid main HashMap checksum")
		}
		logger().Info(context.Background(), "Main HashMap checksum valid")
	}

	if msg.HashMap_MetaData.Accounts_HashMap_MetaData.KeysCount > 0 {
		if !CheckChecksum(msg.HashMap.Accounts_HashMap, msg.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum) {
			logger().Info(context.Background(), "Invalid accounts HashMap checksum")
			return fmt.Errorf("invalid accounts HashMap checksum")
		}
		logger().Info(context.Background(), "Accounts HashMap checksum valid")
	}

	// OPTIMIZATION: For very large datasets, compute SYNC keys incrementally without building full HashMaps
	// This avoids loading millions of keys into memory
	logger().Info(context.Background(), "Computing SYNC keys incrementally")

	// Compute SYNC keys incrementally for Main DB
	logger().Debug(context.Background(), "Computing Main DB SYNC keys incrementally")
	mainHashMapSize := 0
	if msg.HashMap.MAIN_HashMap != nil {
		mainHashMapSize = msg.HashMap.MAIN_HashMap.Size()
	}
	logger().Debug(context.Background(), "Client Main HashMap size",
		ion.Int("size", mainHashMapSize))

	SYNC_Keys_Main, mainFingerprint, err := fs.computeSyncKeysIncremental(fs.mainDB, msg.HashMap.MAIN_HashMap, MainDB)
	if err != nil {
		logger().Debug(context.Background(), "Failed to compute Main SYNC keys",
			ion.Err(err))
		return err
	}
	logger().Debug(context.Background(), "Main SYNC keys computed",
		ion.Int("count", len(SYNC_Keys_Main)))

	// Compute SYNC keys incrementally for Accounts DB
	logger().Debug(context.Background(), "Computing Accounts DB SYNC keys incrementally")
	acctsHashMapSize := 0
	if msg.HashMap.Accounts_HashMap != nil {
		acctsHashMapSize = msg.HashMap.Accounts_HashMap.Size()
	}
	logger().Debug(context.Background(), "Client Accounts HashMap size",
		ion.Int("size", acctsHashMapSize))

	SYNC_Keys_Accounts, acctsFingerprint, err := fs.computeSyncKeysIncremental(fs.accountsDB, msg.HashMap.Accounts_HashMap, AccountsDB)
	if err != nil {
		logger().Debug(context.Background(), "Failed to compute Accounts SYNC keys",
			ion.Err(err))
		return err
	}
	logger().Debug(context.Background(), "Accounts SYNC keys computed",
		ion.Int("count", len(SYNC_Keys_Accounts)))

	// Calculate total chunks needed
	totalKeys := len(SYNC_Keys_Main) + len(SYNC_Keys_Accounts)
	totalChunks := (totalKeys + HashMapChunkSize - 1) / HashMapChunkSize

	logger().Debug(context.Background(), "Preparing to send chunks",
		ion.Int("total_chunks", totalChunks),
		ion.Int("total_keys", totalKeys),
		ion.Int("keys_per_chunk", HashMapChunkSize))
	logger().Info(context.Background(), "Sending HashMap in chunks",
		ion.Int("main_keys", len(SYNC_Keys_Main)),
		ion.Int("accounts_keys", len(SYNC_Keys_Accounts)),
		ion.Int("total_chunks", totalChunks))

	// Send metadata first
	logger().Info(context.Background(), "Sending HashMap metadata")
	metadataResponse := &SyncMessage{
		Type:        TypeHashMapExchangeSYNC,
		SenderID:    fs.host.ID().String(),
		Timestamp:   time.Now().UTC().Unix(),
		Data:        json.RawMessage([]byte(`"Message From Server"`)),
		TotalChunks: totalChunks,
		HashMap_MetaData: &HashMap_MetaData{
			Main_HashMap_MetaData: &MetaData{
				KeysCount: len(SYNC_Keys_Main),
				Checksum:  mainFingerprint,
			},
			Accounts_HashMap_MetaData: &MetaData{
				KeysCount: len(SYNC_Keys_Accounts),
				Checksum:  acctsFingerprint,
			},
		},
	}

	if err := writeMessage(writer, stream, metadataResponse); err != nil {
		logger().Debug(context.Background(), "Failed to send HashMap metadata",
			ion.Err(err))
		return fmt.Errorf("failed to send HashMap metadata: %w", err)
	}
	logger().Info(context.Background(), "Metadata sent successfully")

	// Send chunks: Main HashMap first, then Accounts
	allKeys := append(SYNC_Keys_Main, SYNC_Keys_Accounts...)
	chunkNum := 0
	lastDeadlineUpdate := time.Now().UTC()

	logger().Debug(context.Background(), "Starting to send chunks",
		ion.Int("total_chunks", totalChunks))
	for i := 0; i < len(allKeys); i += HashMapChunkSize {
		end := i + HashMapChunkSize
		if end > len(allKeys) {
			end = len(allKeys)
		}

		chunkKeys := allKeys[i:end]
		chunkNum++

		// Extend write/read deadlines periodically during chunk sending to prevent timeout
		// This is important when sending many chunks or waiting for ACKs
		if time.Since(lastDeadlineUpdate) > 30*time.Second {
			newDeadline := time.Now().UTC().Add(30 * time.Minute)
			if err := stream.SetWriteDeadline(newDeadline); err != nil {
				logger().Debug(context.Background(), "Failed to extend write deadline",
					ion.Err(err))
			}
			if err := stream.SetReadDeadline(newDeadline); err != nil {
				logger().Debug(context.Background(), "Failed to extend read deadline",
					ion.Err(err))
			} else {
				lastDeadlineUpdate = time.Now().UTC()
				logger().Debug(context.Background(), "Extended deadlines",
					ion.Int("chunk", chunkNum),
					ion.Int("total", totalChunks))
			}
		}

		logger().Debug(context.Background(), "Sending chunk",
			ion.Int("chunk", chunkNum),
			ion.Int("total", totalChunks),
			ion.Int("keys", len(chunkKeys)))
		chunkMsg := &SyncMessage{
			Type:        TypeHashMapChunk,
			SenderID:    fs.host.ID().String(),
			Timestamp:   time.Now().UTC().Unix(),
			ChunkNumber: chunkNum,
			TotalChunks: totalChunks,
			ChunkKeys:   chunkKeys,
		}

		if err := writeMessage(writer, stream, chunkMsg); err != nil {
			logger().Debug(context.Background(), "Failed to send chunk",
				ion.Int("chunk", chunkNum),
				ion.Err(err))
			return fmt.Errorf("failed to send chunk %d: %w", chunkNum, err)
		}
		logger().Debug(context.Background(), "Chunk sent, waiting for ACK",
			ion.Int("chunk", chunkNum))

		// Wait for ACK before sending next chunk
		logger().Debug(context.Background(), "Waiting for ACK for chunk",
			ion.Int("chunk", chunkNum))
		ackMsg, err := readMessage(reader, stream)
		if err != nil {
			logger().Debug(context.Background(), "Failed to read chunk ACK",
				ion.Err(err))
			return fmt.Errorf("failed to read chunk ACK: %w", err)
		}

		if ackMsg.Type != TypeHashMapChunkAck || ackMsg.ChunkNumber != chunkNum {
			logger().Debug(context.Background(), "Invalid ACK for chunk",
				ion.Int("chunk", chunkNum),
				ion.String("ack_type", ackMsg.Type),
				ion.Int("ack_chunk_num", ackMsg.ChunkNumber))
			return fmt.Errorf("invalid ACK for chunk %d", chunkNum)
		}

		progress := float64(i+len(chunkKeys)) / float64(len(allKeys)) * 100
		logger().Debug(context.Background(), "Chunk acknowledged",
			ion.Int("chunk", chunkNum),
			ion.Int("total", totalChunks),
			ion.Float64("progress_percent", progress))
	}

	// Send completion message
	logger().Info(context.Background(), "All chunks sent, sending completion message")
	completeMsg := &SyncMessage{
		Type:      TypeHashMapChunkComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Success:   true,
	}

	if err := writeMessage(writer, stream, completeMsg); err != nil {
		logger().Debug(context.Background(), "Failed to send chunk complete",
			ion.Err(err))
		return fmt.Errorf("failed to send chunk complete: %w", err)
	}

	logger().Debug(context.Background(), "All HashMap chunks sent successfully",
		ion.Int("total_chunks", totalChunks))
	logger().Info(context.Background(), "All HashMap chunks sent successfully",
		ion.Int("total_chunks", totalChunks))

	return nil
}

/* UNUSED
// handleHashMapChunkRequest handles a request for a specific chunk (for future use)
func (fs *FastSync) handleHashMapChunkRequest(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	return nil, fmt.Errorf("not implemented")
}
*/

// requestReconciliation handles the client-side reconciliation flow
func (fs *FastSync) requestReconciliation(peerID peer.ID, stream network.Stream, reader *bufio.Reader, writer *bufio.Writer, reconType string, localMainChecksum, localAcctsChecksum string) (bool, error) {
	fs.Logger.Info(context.Background(), "Initiating reconciliation",
		ion.String("type", reconType),
		ion.String("peer", peerID.String()))

	reqMsg := &SyncMessage{
		Type:               TypeReconciliationRequest,
		SenderID:           fs.host.ID().String(),
		Timestamp:          time.Now().UTC().Unix(),
		ReconciliationType: reconType,
	}

	if err := writeMessage(writer, stream, reqMsg); err != nil {
		return false, fmt.Errorf("failed to send reconciliation request: %w", err)
	}

	respMsg, err := readMessage(reader, stream)
	if err != nil {
		return false, fmt.Errorf("failed to read reconciliation response: %w", err)
	}

	if respMsg.Type != TypeReconciliationResponse {
		return false, fmt.Errorf("unexpected response type: %s", respMsg.Type)
	}

	if reconType == ReconTypeMerkle {
		// Verify Merkle Roots (TxHash)
		mainConn, err := DB_OPs.GetMainDBConnection(context.Background())
		if err != nil {
			return false, fmt.Errorf("failed to get local main db connection: %w", err)
		}
		defer DB_OPs.PutMainDBConnection(mainConn)

		mainRoot, err := DB_OPs.GetMerkleRoot(mainConn)
		if err != nil {
			return false, fmt.Errorf("failed to get local main merkle root: %w", err)
		}

		acctsConn, err := DB_OPs.GetAccountsConnections(context.Background())
		if err != nil {
			return false, fmt.Errorf("failed to get local accounts db connection: %w", err)
		}
		defer DB_OPs.PutAccountsConnection(acctsConn)

		acctsRoot, err := DB_OPs.GetMerkleRoot(acctsConn)
		if err != nil {
			return false, fmt.Errorf("failed to get local accounts merkle root: %w", err)
		}

		// Compare bytes
		serverMainRoot := respMsg.MerkleRoot.MainMerkleRoot
		serverAcctsRoot := respMsg.MerkleRoot.AccountsMerkleRoot

		// Debug: Print reconciliation data
		logger().Debug(context.Background(), "Reconciliation data",
			ion.String("local_main_root", fmt.Sprintf("%x", mainRoot)),
			ion.String("server_main_root", fmt.Sprintf("%x", serverMainRoot)),
			ion.String("local_accts_root", fmt.Sprintf("%x", acctsRoot)),
			ion.String("server_accts_root", fmt.Sprintf("%x", serverAcctsRoot)))

		mainMatch := string(mainRoot) == string(serverMainRoot)
		acctsMatch := string(acctsRoot) == string(serverAcctsRoot)

		fs.Logger.Info(context.Background(), "Merkle Reconciliation Result",
			ion.Bool("main_match", mainMatch),
			ion.Bool("accounts_match", acctsMatch))

		return mainMatch && acctsMatch, nil

	} else if reconType == ReconTypeHashMap {
		// content-based verification using Fingerprints
		serverMainChecksum := respMsg.HashMap_MetaData.Main_HashMap_MetaData.Checksum
		serverAcctsChecksum := respMsg.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum

		// Debug: Print reconciliation data
		logger().Debug(context.Background(), "HashMap reconciliation data",
			ion.String("local_main_checksum", localMainChecksum),
			ion.String("server_main_checksum", serverMainChecksum),
			ion.String("local_accts_checksum", localAcctsChecksum),
			ion.String("server_accts_checksum", serverAcctsChecksum))

		mainMatch := localMainChecksum == serverMainChecksum
		acctsMatch := localAcctsChecksum == serverAcctsChecksum

		fs.Logger.Info(context.Background(), "HashMap Reconciliation Result",
			ion.String("local_main", localMainChecksum),
			ion.String("server_main", serverMainChecksum),
			ion.String("local_accts", localAcctsChecksum),
			ion.String("server_accts", serverAcctsChecksum),
			ion.Bool("main_match", mainMatch),
			ion.Bool("accounts_match", acctsMatch))

		return mainMatch && acctsMatch, nil
	}

	return false, fmt.Errorf("unknown reconciliation type: %s", reconType)
}

// handleReconciliation handles the server-side reconciliation request
func (fs *FastSync) handleReconciliation(peerID peer.ID, msg *SyncMessage, stream network.Stream, writer *bufio.Writer) error {
	fs.Logger.Info(context.Background(), "Handling reconciliation request",
		ion.String("type", msg.ReconciliationType),
		ion.String("peer", peerID.String()))

	respMsg := &SyncMessage{
		Type:               TypeReconciliationResponse,
		SenderID:           fs.host.ID().String(),
		Timestamp:          time.Now().UTC().Unix(),
		ReconciliationType: msg.ReconciliationType,
		Success:            true,
	}

	if msg.ReconciliationType == ReconTypeMerkle {
		// Fetch current MainDB Merkle Root
		mainConn, err := DB_OPs.GetMainDBConnection(context.Background())
		if err != nil {
			respMsg.Success = false
			respMsg.ErrorMessage = fmt.Sprintf("failed to get main db conn: %v", err)
		} else {
			mainRoot, err := DB_OPs.GetMerkleRoot(mainConn)
			DB_OPs.PutMainDBConnection(mainConn) // Return immediately

			if err != nil {
				respMsg.Success = false
				respMsg.ErrorMessage = fmt.Sprintf("failed to get main merkle root: %v", err)
			} else {
				// Fetch current AccountsDB Merkle Root
				acctsConn, err := DB_OPs.GetAccountsConnections(context.Background())
				if err != nil {
					respMsg.Success = false
					respMsg.ErrorMessage = fmt.Sprintf("failed to get accounts db conn: %v", err)
				} else {
					acctsRoot, err := DB_OPs.GetMerkleRoot(acctsConn)
					DB_OPs.PutAccountsConnection(acctsConn) // Return immediately

					if err != nil {
						respMsg.Success = false
						respMsg.ErrorMessage = fmt.Sprintf("failed to get accounts merkle root: %v", err)
					} else {
						respMsg.MerkleRoot = MerkleRoot{
							MainMerkleRoot:     mainRoot,
							AccountsMerkleRoot: acctsRoot,
						}
					}
				}
			}
		}

	} else if msg.ReconciliationType == ReconTypeHashMap {
		// Compute fingerprints on-demand (expensive but thorough)
		logger().Info(context.Background(), ">>> [SERVER] Computing on-demand HashMap fingerprints for reconciliation...")

		emptyHM := hashmap.New()
		_, mainFingerprint, err := fs.computeSyncKeysIncremental(fs.mainDB, emptyHM, MainDB)
		if err != nil {
			respMsg.Success = false
			respMsg.ErrorMessage = fmt.Sprintf("failed to compute main fingerprint: %v", err)
		} else {
			_, acctsFingerprint, err := fs.computeSyncKeysIncremental(fs.accountsDB, emptyHM, AccountsDB)
			if err != nil {
				respMsg.Success = false
				respMsg.ErrorMessage = fmt.Sprintf("failed to compute accounts fingerprint: %v", err)
			} else {
				respMsg.HashMap_MetaData = &HashMap_MetaData{
					Main_HashMap_MetaData: &MetaData{
						Checksum: mainFingerprint,
					},
					Accounts_HashMap_MetaData: &MetaData{
						Checksum: acctsFingerprint,
					},
				}
			}
		}
	}

	return writeMessage(writer, stream, respMsg)
}

/* UNUSED
// Legacy handler kept for backward compatibility
func (fs *FastSync) handleHashMapExchangeSYNC(peerID peer.ID, msg *SyncMessage) (*SyncMessage, error) {
	// This is now handled by handleHashMapExchangeSYNCChunked
	return nil, fmt.Errorf("use handleHashMapExchangeSYNCChunked instead")
}
*/

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
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Phase1: Making Main HashMap", ion.String("role", "CLIENT"))
	} else {
		logger().Info(context.Background(), "Phase1: Making Main HashMap")
	}
	MAIN_HashMap, err := fs.MakeHashMap_Default()
	if err != nil {
		return nil, err
	}
	logger().Debug(context.Background(), "Phase1: Main HashMap created",
		ion.Int("keys", MAIN_HashMap.Size()))

	// Second make hashmap of Accounts DB
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Phase1: Making Accounts HashMap", ion.String("role", "CLIENT"))
	} else {
		logger().Info(context.Background(), "Phase1: Making Accounts HashMap")
	}
	ACCOUNTS_HashMap, err := fs.MakeHashMap_Accounts()
	if err != nil {
		return nil, err
	}
	logger().Debug(context.Background(), "Phase1: Accounts HashMap created",
		ion.Int("keys", ACCOUNTS_HashMap.Size()))

	// Compute the Metadata for the both Maps

	ComputeCHECKSUM_MAIN_Value := MAIN_HashMap.Fingerprint()
	ComputeCHECKSUM_ACCOUNTS_Value := ACCOUNTS_HashMap.Fingerprint()

	logger().Debug(context.Background(), "Phase1: Sending HashMaps to server",
		ion.Int("main_keys", MAIN_HashMap.Size()),
		ion.Int("accounts_keys", ACCOUNTS_HashMap.Size()))

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

	// 1. Pre-Sync Optimization: Check Merkle Roots
	// We open a temporary stream to check if data is already identical.
	// If it matches, we ABORT the expensive Phase 1 & 2.
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Performing Pre-Sync Merkle Check...", ion.String("peer", peerID.String()))
	} else {
		logger().Info(context.Background(), ">>> [CLIENT] Performing Pre-Sync Merkle Check...")
	}

	preStream, err := returnStream(fs, peerID)
	if err == nil {
		shouldAbort := false
		// Use a closure to ensure stream is closed BEFORE Phase 1 (avoid idle timeout)
		func() {
			defer preStream.Close()
			preReader := bufio.NewReader(preStream)
			preWriter := bufio.NewWriter(preStream)

			// Request Merkle Reconciliation
			// We pass empty strings for local checksums because Merkle check fetches them internally
			match, reconErr := fs.requestReconciliation(peerID, preStream, preReader, preWriter, ReconTypeMerkle, "", "")
			if reconErr == nil && match {
				shouldAbort = true
			} else if reconErr != nil {
				if fs.Logger != nil {
					fs.Logger.Warn(context.Background(), "Pre-Sync Check failed (mismatch or error)", ion.String("error", reconErr.Error()))
				}
			}
		}()

		if shouldAbort {
			msg := "Pre-Sync: Merkle Roots Match. Data is identical. Aborting Sync."
			if fs.Logger != nil {
				fs.Logger.Info(context.Background(), msg)
			} else {
				logger().Info(context.Background(), ">>> [CLIENT] " + msg)
			}
			return &SyncMessage{Type: TypeSyncComplete, Success: true, Data: json.RawMessage([]byte(`"Pre-Sync Match"`))}, nil
		}
	} else {
		if fs.Logger != nil {
			fs.Logger.Warn(context.Background(), "Pre-Sync failed to open stream, proceeding", ion.String("error", err.Error()))
		}
	}

	//phase1: Should make the HashMap of accounts and main DB
	// Note: We run Phase 1 (local computation) BEFORE opening the stream to avoid
	// idle timeouts while building the HashMap (which can take 20+ minutes).
	Phase1, err := fs.Phase1_SYNC(peerID)
	if err != nil {
		return nil, fmt.Errorf("failed in Phase1_SYNC: %w", err)
	}

	// Only open stream after Phase 1 is done
	stream, err := returnStream(fs, peerID)
	if err != nil {
		return nil, err
	}
	defer (stream).Close()

	reader := bufio.NewReader(stream)
	writer := bufio.NewWriter(stream)

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
		if fs.Logger != nil {
			fs.Logger.Warn(context.Background(), "HashMap metadata checksum mismatch, retrying HashMap exchange",
				ion.String("peer", peerID.String()))
		}
		for i := range 3 {
			if fs.Logger != nil {
				fs.Logger.Debug(context.Background(), "Retrying HashMap exchange",
					ion.String("peer", peerID.String()),
					ion.Int("attempt", i+1))
			}
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
		if fs.Logger != nil {
			fs.Logger.Warn(context.Background(), "Failed to transfer AVRO file, retrying file transfer",
				ion.String("peer", peerID.String()))
		} else {
			logger().Warn(context.Background(), "Failed to transfer AVRO file, retrying file transfer",
				ion.String("peer", peerID.String()))
		}

		for i := range 3 {
			logger().Debug(context.Background(), "Retrying file transfer",
				ion.String("peer", peerID.String()),
				ion.Int("attempt", i+1))

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
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Starting Phase4 - Pushing data from AVRO files to database", ion.String("role", "CLIENT"))
	} else {
		logger().Info(context.Background(), "Starting Phase4 - Pushing data from AVRO files to database")
	}

	// Debug: Check Phase2 state
	mainMapExists := Phase2.HashMap != nil && Phase2.HashMap.MAIN_HashMap != nil
	acctsMapExists := Phase2.HashMap != nil && Phase2.HashMap.Accounts_HashMap != nil
	logger().Debug(context.Background(), "Phase2 state",
		ion.Bool("has_main_hashmap", mainMapExists),
		ion.Bool("has_accounts_hashmap", acctsMapExists))

	if Phase2.HashMap != nil {
		mainSize := 0
		if Phase2.HashMap.MAIN_HashMap != nil {
			mainSize = Phase2.HashMap.MAIN_HashMap.Size()
		}
		acctsSize := 0
		if Phase2.HashMap.Accounts_HashMap != nil {
			acctsSize = Phase2.HashMap.Accounts_HashMap.Size()
		}
		logger().Debug(context.Background(), "Phase2 HashMaps size",
			ion.Int("main_size", mainSize),
			ion.Int("accounts_size", acctsSize))
	}

	// 1. Push the Main DB Transactions from AVRO file to the DB - if no maindb then skip
	mainHashMapSize := 0
	if Phase2.HashMap != nil && Phase2.HashMap.MAIN_HashMap != nil {
		mainHashMapSize = Phase2.HashMap.MAIN_HashMap.Size()
	}
	logger().Debug(context.Background(), "Checking Main DB",
		ion.Int("hashmap_size", mainHashMapSize))

	if Phase2.HashMap != nil && Phase2.HashMap.MAIN_HashMap != nil && Phase2.HashMap.MAIN_HashMap.Size() > 0 {
		logger().Debug(context.Background(), "Pushing Main DB data from AVRO file",
			ion.Int("keys", Phase2.HashMap.MAIN_HashMap.Size()))
		if err := fs.PushDataToDB(Phase2, MainDB, "fastsync/.temp/defaultdb.avro"); err != nil {
			logger().Debug(context.Background(), "Failed to push Main DB transactions", ion.Err(err))
			return nil, fmt.Errorf("failed to push Main DB transactions: %w", err)
		}
		logger().Info(context.Background(), "Successfully pushed Main DB transactions")
	} else {
		logger().Info(context.Background(), "Skipping Main DB push - no data to push")
	}

	// 2. Push the Accounts DB Transactions from AVRO file to the DB - if no accountsdb then skip
	acctSize := 0
	if Phase2.HashMap != nil && Phase2.HashMap.Accounts_HashMap != nil {
		acctSize = Phase2.HashMap.Accounts_HashMap.Size()
	}
	logger().Debug(context.Background(), "Checking Accounts DB",
		ion.Int("hashmap_size", acctSize))

	if Phase2.HashMap != nil && Phase2.HashMap.Accounts_HashMap != nil && Phase2.HashMap.Accounts_HashMap.Size() > 0 {
		logger().Debug(context.Background(), "Pushing Accounts DB data from AVRO file",
			ion.Int("keys", Phase2.HashMap.Accounts_HashMap.Size()))

		// Check if accounts database client is properly initialized
		if fs.accountsDB == nil {
			logger().Error(context.Background(), "Accounts database client is nil - cannot restore accounts data", nil)
			return nil, fmt.Errorf("accounts database client is not initialized")
		}

		if err := fs.PushDataToDB(Phase2, AccountsDB, "fastsync/.temp/accountsdb.avro"); err != nil {
			logger().Error(context.Background(), "Failed to push Accounts DB transactions", err)
			return nil, fmt.Errorf("failed to push Accounts DB transactions: %w", err)
		}
		logger().Info(context.Background(), "Successfully pushed Accounts DB transactions")
	} else {
		acctMapNil := Phase2.HashMap == nil || Phase2.HashMap.Accounts_HashMap == nil
		acctSize := 0
		if !acctMapNil && Phase2.HashMap.Accounts_HashMap != nil {
			acctSize = Phase2.HashMap.Accounts_HashMap.Size()
		}
		logger().Info(context.Background(), "Skipping Accounts DB push - no data to push",
			ion.Bool("hashmap_nil", acctMapNil),
			ion.Int("hashmap_size", acctSize))
	}

	logger().Info(context.Background(), "Phase4 completed successfully")

	// 5. Post-Sync Verification: Dual-Check (Merkle + Content)
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Performing Post-Sync Verification", ion.String("peer", peerID.String()))
	} else {
		logger().Info(context.Background(), "Performing Post-Sync Verification")
	}

	// 5a. Merkle Check (Fast, but history-dependent)
	postStream, err := returnStream(fs, peerID)
	if err == nil {
		func() {
			defer postStream.Close()
			postReader := bufio.NewReader(postStream)
			postWriter := bufio.NewWriter(postStream)

			match, reconErr := fs.requestReconciliation(peerID, postStream, postReader, postWriter, ReconTypeMerkle, "", "")
			if reconErr != nil {
				if fs.Logger != nil {
					fs.Logger.Warn(context.Background(), "Post-Sync Merkle Check failed", ion.String("error", reconErr.Error()))
				}
			} else if match {
				msg := "Post-Sync: Merkle Roots Match. Transaction history is identical!"
				if fs.Logger != nil {
					fs.Logger.Info(context.Background(), msg)
				} else {
					logger().Info(context.Background(), msg)
				}
			} else {
				msg := "Post-Sync: Merkle Roots Mismatch (Expected due to different transaction history)."
				if fs.Logger != nil {
					fs.Logger.Warn(context.Background(), msg)
				} else {
					logger().Info(context.Background(), msg)
				}
			}
		}()
	}

	// 5b. Content Verification (Thorough, history-independent)
	logger().Info(context.Background(), "Performing POST-SYNC CONTENT VERIFICATION - Computing fresh local State Fingerprints")
	localMainHM, mainHMErr := fs.MakeHashMap_Default()
	localAcctsHM, acctsHMErr := fs.MakeHashMap_Accounts()

	if mainHMErr == nil && acctsHMErr == nil && localAcctsHM != nil {
		localMainFingerprint := localMainHM.Fingerprint()
		localAcctsFingerprint := localAcctsHM.Fingerprint()

		serverMainFingerprint := Phase2.HashMap_MetaData.Main_HashMap_MetaData.Checksum
		serverAcctsFingerprint := Phase2.HashMap_MetaData.Accounts_HashMap_MetaData.Checksum

		mainMatch := localMainFingerprint == serverMainFingerprint
		acctsMatch := localAcctsFingerprint == serverAcctsFingerprint

		logger().Debug(context.Background(), "Content Verification Results",
			ion.Bool("main_match", mainMatch),
			ion.String("local_main", localMainFingerprint),
			ion.String("server_main", serverMainFingerprint),
			ion.Bool("accounts_match", acctsMatch),
			ion.String("local_accounts", localAcctsFingerprint),
			ion.String("server_accounts", serverAcctsFingerprint))

		if mainMatch && acctsMatch {
			msg := "POST-SYNC CONTENT VERIFICATION SUCCESSFUL! Total state matches server."
			if fs.Logger != nil {
				fs.Logger.Info(context.Background(), msg)
			} else {
				logger().Info(context.Background(), msg)
			}
		} else {
			msg := "POST-SYNC CONTENT VERIFICATION FAILED! State divergence detected."
			if fs.Logger != nil {
				fs.Logger.Error(context.Background(), msg, fmt.Errorf("state mismatch"),
					ion.Bool("main_match", mainMatch),
					ion.Bool("accounts_match", acctsMatch))
			} else {
				logger().Error(context.Background(), msg, fmt.Errorf("state mismatch"),
					ion.Bool("main_match", mainMatch),
					ion.Bool("accounts_match", acctsMatch))
			}
		}
	} else {
		logger().Debug(context.Background(), "Could not perform content verification",
			ion.Err(mainHMErr),
			ion.Err(acctsHMErr))
	}

	return &SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Success:   true,
	}, nil
}

/* UNUSED
func calculateBatchCount(startTxID, endTxID uint64) int {
	if startTxID >= endTxID {
		return 1 // At least one batch even if no diff
	}

	txDiff := endTxID - startTxID
	return int((txDiff + SyncBatchSize - 1) / SyncBatchSize)
}
*/

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

	logger().Debug(context.Background(), "Sending batch data",
		ion.String("peer", peerID.String()),
		ion.Int("batch", int(msg.BatchNumber)),
		ion.Int("entries", len(entries)),
		ion.Int("crdts", len(crdts)),
		ion.String("db", dbTypeToString(msg.DBType)))

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
	if err := os.MkdirAll(AVRO_FILE_PATH, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	mainAVROpath := AVRO_FILE_PATH + "main.avro"
	accountsAVROpath := AVRO_FILE_PATH + "accounts.avro"

	// 2. Use defer to ensure backup files are cleaned up even if errors occur.
	defer func() {
		if err := os.Remove(mainAVROpath); err != nil && !os.IsNotExist(err) {
			logger().Error(context.Background(), "Failed to remove temporary backup file", err,
				ion.String("path", mainAVROpath))
		}
		if err := os.Remove(accountsAVROpath); err != nil && !os.IsNotExist(err) {
			logger().Error(context.Background(), "Failed to remove temporary backup file", err,
				ion.String("path", accountsAVROpath))
		}
	}()

	// 3. Create targeted backups using the client's HashMap.
	// Only create MainDB AVRO file if there are keys to sync
	mainHashMapSize := 0
	if msg.HashMap != nil && msg.HashMap.MAIN_HashMap != nil {
		mainHashMapSize = msg.HashMap.MAIN_HashMap.Size()
	}
	logger().Debug(context.Background(), "MakeAVROFile_Transfer: MainDB HashMap size",
		ion.Int("size", mainHashMapSize))
	if msg.HashMap != nil && msg.HashMap.MAIN_HashMap != nil && msg.HashMap.MAIN_HashMap.Size() > 0 {
		mainCfg := DB_OPs.Config{
			Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
			Username:   settings.Get().Database.Username,
			Password:   settings.Get().Database.Password,
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
		logger().Debug(context.Background(), "MainDB HashMap breakdown for AVRO creation",
			ion.Int("total_keys", totalKeysInHashMap),
			ion.Int("block_keys", blockKeyCount),
			ion.Int("tx_keys", txKeyCount),
			ion.Int("tx_processed_keys", txProcessedKeyCount),
			ion.Bool("has_latest_block", latestBlockInHashMap))

		logger().Info(context.Background(), "Creating targeted backup from MAIN HashMap",
			ion.String("peer", peerID.String()),
			ion.Int("keys", msg.HashMap.MAIN_HashMap.Size()),
			ion.Int("block_keys", blockKeyCount),
			ion.Bool("latest_block", latestBlockInHashMap))

		err := DB_OPs.BackupFromHashMap(mainCfg, msg.HashMap.MAIN_HashMap)
		if err != nil {
			return nil, fmt.Errorf("failed to backup main database: %w", err)
		}

		// Transfer the main DB backup file
		logger().Info(context.Background(), "Transferring main DB backup file",
			ion.String("peer", peerID.String()),
			ion.String("file", mainAVROpath))
		err = TransferAVROFile(fs.host, peerID, mainAVROpath, "fastsync/.temp/defaultdb.avro")
		if err != nil {
			return nil, fmt.Errorf("failed to transfer main database: %w", err)
		}
		logger().Info(context.Background(), "Successfully transferred main DB backup file",
			ion.Int("keys", msg.HashMap.MAIN_HashMap.Size()))
	} else {
		emptySize := 0
		if msg.HashMap.MAIN_HashMap != nil {
			emptySize = msg.HashMap.MAIN_HashMap.Size()
		}
		logger().Info(context.Background(), "Skipping main DB AVRO file creation and transfer - HashMap is empty")
		logger().Debug(context.Background(), "MainDB HashMap is empty, skipping AVRO file creation",
			ion.Int("size", emptySize))
	}

	// Process accounts DB if it has entries
	if msg.HashMap.Accounts_HashMap != nil && msg.HashMap.Accounts_HashMap.Size() > 0 {
		// Count address and did keys in HashMap for debugging
		addressKeyCount := 0
		didKeyCount := 0
		for _, key := range msg.HashMap.Accounts_HashMap.Keys() {
			if strings.HasPrefix(key, "address:") {
				addressKeyCount++
			} else if strings.HasPrefix(key, "did:") {
				didKeyCount++
			}
		}
		logger().Debug(context.Background(), "AccountsDB HashMap breakdown for AVRO creation",
			ion.Int("total_keys", msg.HashMap.Accounts_HashMap.Size()),
			ion.Int("address_keys", addressKeyCount),
			ion.Int("did_keys", didKeyCount))

		accountsCfg := DB_OPs.Config{
			Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
			Username:   settings.Get().Database.Username,
			Password:   settings.Get().Database.Password,
			Database:   config.AccountsDBName,
			OutputPath: accountsAVROpath,
		}

		logger().Info(context.Background(), "Creating targeted backup from Accounts HashMap",
			ion.String("peer", peerID.String()),
			ion.Int("keys", msg.HashMap.Accounts_HashMap.Size()),
			ion.Int("address_keys", addressKeyCount),
			ion.Int("did_keys", didKeyCount))

		err := DB_OPs.BackupFromHashMap(accountsCfg, msg.HashMap.Accounts_HashMap)
		if err != nil {
			return nil, fmt.Errorf("failed to backup accounts database: %w", err)
		}

		// Transfer the accounts DB backup file
		logger().Info(context.Background(), "Transferring accounts DB backup file",
			ion.String("peer", peerID.String()),
			ion.String("file", accountsAVROpath))
		err = TransferAVROFile(fs.host, peerID, accountsAVROpath, "fastsync/.temp/accountsdb.avro")
		if err != nil {
			return nil, fmt.Errorf("failed to transfer accounts database: %w", err)
		}
	}

	// 6. Send a completion message back on the control stream.
	logger().Info(context.Background(), "File transfers complete, sending SyncComplete",
		ion.String("peer", peerID.String()))
	return &SyncMessage{
		Type:      TypeSyncComplete,
		SenderID:  fs.host.ID().String(),
		Timestamp: time.Now().UTC().Unix(),
		Success:   true,
	}, nil
}

// FirstSyncServer exports all data from both databases (defaultdb and accountsdb)
// and sends it to the receiver over port 15000 using FileProtocol (same as fastsync).
// This is used for initial synchronization when a node needs to get all data from another node.
func (fs *FastSync) FirstSyncServer(peerID peer.ID) error {
	// First sync server
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Starting first sync server - exporting all data",
			ion.String("peer", peerID.String()))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_SERVER] Starting first sync - exporting all data from both databases")
		logger().Info(context.Background(), "Starting first sync server - exporting all data", ion.String("peer", peerID.String()))
	}

	// Ensure the temporary directory exists
	if err := os.MkdirAll(AVRO_FILE_PATH, 0o750); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	mainAVROpath := AVRO_FILE_PATH + "defaultdb.avro"
	accountsAVROpath := AVRO_FILE_PATH + "accountsdb.avro"

	// Clean up any existing files
	defer func() {
		if err := os.Remove(mainAVROpath); err != nil && !os.IsNotExist(err) {
			logger().Error(context.Background(), "Failed to remove temp file", err,
				ion.String("path", mainAVROpath))
		}
		if err := os.Remove(accountsAVROpath); err != nil && !os.IsNotExist(err) {
			logger().Error(context.Background(), "Failed to remove temp file", err,
				ion.String("path", accountsAVROpath))
		}
	}()

	// 1. Get ALL keys from MainDB (defaultdb)
	logger().Info(context.Background(), "Getting all keys from MainDB (defaultdb)")
	mainHashMap := hashmap.New()
	mainKeys, err := DB_OPs.GetAllKeys(fs.mainDB, "")
	if err != nil {
		return fmt.Errorf("failed to get all keys from MainDB: %w", err)
	}
	logger().Debug(context.Background(), "Found keys in MainDB",
		ion.Int("count", len(mainKeys)))
	for _, key := range mainKeys {
		mainHashMap.Insert(key)
	}
	logger().Debug(context.Background(), "MainDB HashMap created",
		ion.Int("keys", mainHashMap.Size()))

	// 2. Get ALL keys from AccountsDB
	logger().Info(context.Background(), "Getting all keys from AccountsDB")
	accountsHashMap := hashmap.New()
	accountsKeys, err := DB_OPs.GetAllKeys(fs.accountsDB, "")
	if err != nil {
		return fmt.Errorf("failed to get all keys from AccountsDB: %w", err)
	}
	logger().Debug(context.Background(), "Found keys in AccountsDB",
		ion.Int("count", len(accountsKeys)))
	for _, key := range accountsKeys {
		accountsHashMap.Insert(key)
	}
	logger().Debug(context.Background(), "AccountsDB HashMap created",
		ion.Int("keys", accountsHashMap.Size()))

	// 3. Create AVRO file for MainDB if it has data
	if mainHashMap.Size() > 0 {
		logger().Debug(context.Background(), "Creating AVRO file for MainDB",
			ion.Int("keys", mainHashMap.Size()))
		mainCfg := DB_OPs.Config{
			Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
			Username:   settings.Get().Database.Username,
			Password:   settings.Get().Database.Password,
			Database:   config.DBName, // defaultdb
			OutputPath: mainAVROpath,
		}

		if err := DB_OPs.BackupFromHashMap(mainCfg, mainHashMap); err != nil {
			return fmt.Errorf("failed to backup MainDB: %w", err)
		}
		logger().Info(context.Background(), "MainDB AVRO file created")

		// Transfer the MainDB file
		logger().Info(context.Background(), "Transferring MainDB AVRO file to receiver")
		if err := TransferAVROFile(fs.host, peerID, mainAVROpath, "fastsync/.temp/defaultdb.avro"); err != nil {
			return fmt.Errorf("failed to transfer MainDB file: %w", err)
		}
		logger().Info(context.Background(), "MainDB file transferred successfully",
			ion.String("peer", peerID.String()),
			ion.Int("keys", mainHashMap.Size()))
	} else {
		logger().Info(context.Background(), "MainDB is empty, skipping AVRO file creation")
	}

	// 4. Create AVRO file for AccountsDB if it has data
	if accountsHashMap.Size() > 0 {
		logger().Debug(context.Background(), "Creating AVRO file for AccountsDB",
			ion.Int("keys", accountsHashMap.Size()))
		accountsCfg := DB_OPs.Config{
			Address:    config.DBAddress + ":" + strconv.Itoa(config.DBPort),
			Username:   settings.Get().Database.Username,
			Password:   settings.Get().Database.Password,
			Database:   config.AccountsDBName,
			OutputPath: accountsAVROpath,
		}

		if err := DB_OPs.BackupFromHashMap(accountsCfg, accountsHashMap); err != nil {
			return fmt.Errorf("failed to backup AccountsDB: %w", err)
		}
		logger().Info(context.Background(), ">>> [FIRST_SYNC_SERVER] ✓ AccountsDB AVRO file created")

		// Transfer the AccountsDB file
		logger().Info(context.Background(), ">>> [FIRST_SYNC_SERVER] Transferring AccountsDB AVRO file to receiver...")
		if err := TransferAVROFile(fs.host, peerID, accountsAVROpath, "fastsync/.temp/accountsdb.avro"); err != nil {
			return fmt.Errorf("failed to transfer AccountsDB file: %w", err)
		}
		logger().Info(context.Background(), ">>> [FIRST_SYNC_SERVER] ✓ AccountsDB file transferred successfully")
		logger().Info(context.Background(), "AccountsDB file transferred successfully", ion.String("peer", peerID.String()), ion.Int("keys", accountsHashMap.Size()))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_SERVER] AccountsDB is empty, skipping AVRO file creation")
	}

	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "First sync server completed successfully",
			ion.String("peer", peerID.String()),
			ion.Int("main_keys", mainHashMap.Size()),
			ion.Int("accounts_keys", accountsHashMap.Size()))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_SERVER] ✓ First sync server completed successfully")
		logger().Info(context.Background(), "First sync server completed successfully", ion.String("peer", peerID.String()), ion.Int("main_keys", mainHashMap.Size()), ion.Int("accounts_keys", accountsHashMap.Size()))
	}

	return nil
}

// FirstSyncClient receives all data from the server and loads it into the respective databases.
// This is used for initial synchronization when a node needs to get all data from another node.
// The files are received over port 15000 using FileProtocol (same as fastsync).
func (fs *FastSync) FirstSyncClient(peerID peer.ID) error {
	// 1. Pre-Sync Check: Merkle Roots
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Performing Pre-Sync Merkle Check...", ion.String("peer", peerID.String()))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] Performing Pre-Sync Merkle Check...")
	}

	preStream, err := returnStream(fs, peerID)
	if err == nil {
		shouldAbort := false
		func() {
			defer preStream.Close()
			preReader := bufio.NewReader(preStream)
			preWriter := bufio.NewWriter(preStream)

			// Request Merkle Reconciliation
			match, reconErr := fs.requestReconciliation(peerID, preStream, preReader, preWriter, ReconTypeMerkle, "", "")
			if reconErr == nil && match {
				shouldAbort = true
			} else if reconErr != nil && fs.Logger != nil {
				fs.Logger.Warn(context.Background(), "Pre-Sync Check failed", ion.String("error", reconErr.Error()))
			}
		}()

		if shouldAbort {
			msg := "Pre-Sync: Merkle Roots Match. Data is identical. Aborting FirstSync."
			if fs.Logger != nil {
				fs.Logger.Info(context.Background(), msg)
			} else {
				logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] " + msg)
			}
			return nil // Abort successfully
		}
	}

	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Starting first sync client - waiting for data",
			ion.String("peer", peerID.String()))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] Starting first sync client - waiting for data from server")
		logger().Info(context.Background(), "Starting first sync client - waiting for data", ion.String("peer", peerID.String()))
	}

	// Ensure the temp directory exists
	if err := os.MkdirAll(AVRO_FILE_PATH, 0o750); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	// The files will be received by HandleFileStream which saves them to fastsync/.temp/
	// We need to wait for the files to be received, then load them
	mainDBPath := "fastsync/.temp/defaultdb.avro"
	accountsDBPath := "fastsync/.temp/accountsdb.avro"

	// Wait for files to be received (with timeout)
	logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] Waiting for files from server...")
	maxWaitTime := 30 * time.Minute // Allow up to 30 minutes for large transfers
	startTime := time.Now()

	var mainFileExists, accountsFileExists bool
	for time.Since(startTime) < maxWaitTime {
		// Check if MainDB file exists
		if _, err := os.Stat(mainDBPath); err == nil {
			mainFileExists = true
		}

		// Check if AccountsDB file exists
		if _, err := os.Stat(accountsDBPath); err == nil {
			accountsFileExists = true
		}

		// If both files exist, break
		if mainFileExists && accountsFileExists {
			logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] ✓ Both files received")
			break
		}

		// Check at intervals
		time.Sleep(5 * time.Second)

		// Log progress every minute
		if int(time.Since(startTime).Seconds())%60 == 0 {
			if fs.Logger != nil {
				fs.Logger.Info(context.Background(), "Waiting for AVRO files...",
					ion.Bool("main_received", mainFileExists),
					ion.Bool("accounts_received", accountsFileExists))
			}
		}
	}

	if !mainFileExists && !accountsFileExists {
		return fmt.Errorf("timeout waiting for files from server (waited %v)", time.Since(startTime))
	}

	// 1. Load MainDB data if file exists
	if mainFileExists {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] Loading MainDB data from AVRO file...")
		// Create a dummy SyncMessage for PushDataToDB
		mainSyncMsg := &SyncMessage{
			Type:      TypeSyncComplete,
			SenderID:  fs.host.ID().String(),
			Timestamp: time.Now().UTC().Unix(),
			Success:   true,
		}

		if err := fs.PushDataToDB(mainSyncMsg, MainDB, mainDBPath); err != nil {
			return fmt.Errorf("failed to load MainDB data: %w", err)
		}
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] ✓ MainDB data loaded successfully")
		logger().Info(context.Background(), "MainDB data loaded successfully", ion.String("peer", peerID.String()), ion.String("file", mainDBPath))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] MainDB file not received, skipping")
	}

	// 2. Load AccountsDB data if file exists
	if accountsFileExists {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] Loading AccountsDB data from AVRO file...")
		// Create a dummy SyncMessage for PushDataToDB
		accountsSyncMsg := &SyncMessage{
			Type:      TypeSyncComplete,
			SenderID:  fs.host.ID().String(),
			Timestamp: time.Now().UTC().Unix(),
			Success:   true,
		}

		if err := fs.PushDataToDB(accountsSyncMsg, AccountsDB, accountsDBPath); err != nil {
			return fmt.Errorf("failed to load AccountsDB data: %w", err)
		}
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] ✓ AccountsDB data loaded successfully")
		logger().Info(context.Background(), "AccountsDB data loaded successfully", ion.String("peer", peerID.String()), ion.String("file", accountsDBPath))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] AccountsDB file not received, skipping")
	}

	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "First sync client completed successfully",
			ion.String("peer", peerID.String()),
			ion.Bool("main_loaded", mainFileExists),
			ion.Bool("accounts_loaded", accountsFileExists))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] ✓ First sync client completed successfully")
		logger().Info(context.Background(), "First sync client completed successfully", ion.String("peer", peerID.String()), ion.Bool("main_loaded", mainFileExists), ion.Bool("accounts_loaded", accountsFileExists))
	}

	// Post-Sync Verification: Dual-Check (Merkle + Content)
	if fs.Logger != nil {
		fs.Logger.Info(context.Background(), "Performing Post-Sync Verification...", ion.String("peer", peerID.String()))
	} else {
		logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] Performing Post-Sync Verification...")
	}

	// 5a. Merkle Check (Fast, but history-dependent)
	postStream, err := returnStream(fs, peerID)
	if err == nil {
		func() {
			defer postStream.Close()
			postReader := bufio.NewReader(postStream)
			postWriter := bufio.NewWriter(postStream)

			match, reconErr := fs.requestReconciliation(peerID, postStream, postReader, postWriter, ReconTypeMerkle, "", "")
			if reconErr != nil {
				if fs.Logger != nil {
					fs.Logger.Warn(context.Background(), "Post-Sync Merkle Check failed (error)", ion.String("error", reconErr.Error()))
				}
			} else if match {
				msg := "Post-Sync: Merkle Roots Match. Transaction history is identical!"
				if fs.Logger != nil {
					fs.Logger.Info(context.Background(), msg)
				} else {
					logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] " + msg)
				}
			} else {
				msg := "Post-Sync: Merkle Roots Mismatch (Expected due to different transaction history)."
				if fs.Logger != nil {
					fs.Logger.Warn(context.Background(), msg)
				} else {
					logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] " + msg)
				}
			}
		}()
	}

	// 5b. Content Verification (Thorough, history-independent)
	logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] Performing POST-SYNC CONTENT VERIFICATION - Computing fresh local State Fingerprints...")
	localMainHM, mainHMErr := fs.MakeHashMap_Default()
	localAcctsHM, acctsHMErr := fs.MakeHashMap_Accounts()

	if mainHMErr == nil && acctsHMErr == nil {
		localMainFingerprint := localMainHM.Fingerprint()
		localAcctsFingerprint := localAcctsHM.Fingerprint()

		contentStream, err := returnStream(fs, peerID)
		if err == nil {
			func() {
				defer contentStream.Close()
				cw := bufio.NewWriter(contentStream)
				cr := bufio.NewReader(contentStream)

				match, reconErr := fs.requestReconciliation(peerID, contentStream, cr, cw, ReconTypeHashMap, localMainFingerprint, localAcctsFingerprint)
				if reconErr != nil {
					if fs.Logger != nil {
						fs.Logger.Error(context.Background(), "Post-Sync Content Verification failed", reconErr)
					}
				} else if match {
					msg := "POST-SYNC CONTENT VERIFICATION SUCCESSFUL! Total state matches server."
					if fs.Logger != nil {
						fs.Logger.Info(context.Background(), msg)
					} else {
						logger().Info(context.Background(), ">>> [FIRST_SYNC_CLIENT] " + msg)
					}
				} else {
					msg := "POST-SYNC CONTENT VERIFICATION FAILED! State divergence detected."
					if fs.Logger != nil {
						fs.Logger.Error(context.Background(), msg, fmt.Errorf("state mismatch"))
					} else {
						logger().Error(context.Background(), ">>> [FIRST_SYNC_CLIENT] ERROR: " + msg, nil)
					}
				}
			}()
		}
	} else {
		logger().Debug(context.Background(), "Could not perform content verification")
	}

	return nil
}
