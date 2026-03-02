package DB_OPs

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	hashmap "jmdn/crdt/HashMap"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/linkedin/goavro/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Config holds the configuration for the backup process.
type Config struct {
	Address    string
	Username   string
	Password   string
	Database   string
	OutputPath string
}

const (
	compression = "snappy"
)

func BackupFromHashMap(cfg Config, MAP *hashmap.HashMap) error {
	// ———————————————————————————————————————————————
	// 1. Dial & login
	// ———————————————————————————————————————————————
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, cfg.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()

	client := schema.NewImmuServiceClient(conn)
	apiCtx := context.Background()
	login, err := client.Login(apiCtx, &schema.LoginRequest{
		User:     []byte(cfg.Username),
		Password: []byte(cfg.Password),
	})
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	apiCtx = metadata.NewOutgoingContext(apiCtx,
		metadata.Pairs("authorization", "Bearer "+login.GetToken()),
	)
	useDb, err := client.UseDatabase(apiCtx, &schema.Database{DatabaseName: cfg.Database})
	if err != nil {
		return fmt.Errorf("use database failed: %w", err)
	}
	apiCtx = metadata.NewOutgoingContext(apiCtx,
		metadata.Pairs("authorization", "Bearer "+useDb.GetToken()),
	)
	log.Printf("Connected to %s/%s", cfg.Address, cfg.Database)

	// ———————————————————————————————————————————————
	// 2. Prepare Avro OCF writer
	// ———————————————————————————————————————————————
	// Schema: simple Key, Value (both strings)
	schemaJSON := `{
      "namespace": "fastsync",
      "type": "record",
      "name": "KeyValue",
      "fields": [
        {"name":"Key",   "type":"string"},
        {"name":"Value", "type":"string"},
        {"name":"Database", "type":"string"}
      ]
    }`
	codec, err := goavro.NewCodec(schemaJSON)
	if err != nil {
		return fmt.Errorf("avro codec init: %w", err)
	}

	// Detect if we need to append or write fresh
	exists := false
	if _, err := os.Stat(cfg.OutputPath); err == nil {
		exists = true
	}

	var avroFile *os.File
	if exists {
		avroFile, err = os.OpenFile(cfg.OutputPath, os.O_APPEND|os.O_RDWR, 0600)
	} else {
		avroFile, err = os.Create(cfg.OutputPath)
	}
	if err != nil {
		return fmt.Errorf("open avro file: %w", err)
	}
	defer avroFile.Close()

	ocfWriter, err := goavro.NewOCFWriter(goavro.OCFConfig{
		W:               avroFile,
		Codec:           codec,
		CompressionName: compression,
	})

	if err != nil {
		return fmt.Errorf("avro writer init: %w", err)
	}

	// ———————————————————————————————————————————————
	// 3. Peel the HashMap & stream into Avro
	// ———————————————————————————————————————————————
	keys := MAP.Keys()
	sort.Strings(keys)
	totalKeys := len(keys)
	log.Printf("Exporting %d keys into Avro → %s", totalKeys, cfg.OutputPath)

	// Batch size for processing keys and AVRO writes
	const (
		readBatchSize  = 100  // Process 100 keys at a time
		writeBatchSize = 1000 // Write 1000 records to AVRO at once
	)

	var recordsBatch []interface{}
	processed := 0
	blockKeysInHashMap := 0
	blockKeysBackedUp := 0
	latestBlockInHashMap := false
	latestBlockBackedUp := false
	failedBlockKeys := 0
	failedLatestBlock := false

	// Count block keys in HashMap first
	for _, key := range keys {
		if strings.HasPrefix(key, "block:") {
			blockKeysInHashMap++
		} else if key == "latest_block" {
			latestBlockInHashMap = true
		}
	}

	for i := 0; i < totalKeys; i += readBatchSize {
		end := i + readBatchSize
		if end > totalKeys {
			end = totalKeys
		}

		batchKeys := keys[i:end]
		log.Printf("Processing batch %d-%d of %d keys", i+1, end, totalKeys)

		// Process batch of keys
		for _, key := range batchKeys {
			isBlockKey := strings.HasPrefix(key, "block:")
			isLatestBlock := (key == "latest_block")

			resp, err := client.Get(apiCtx, &schema.KeyRequest{Key: []byte(key)})
			if err != nil {
				if isBlockKey {
					failedBlockKeys++
					log.Printf("[ERROR] Failed to get block key '%s': %v - BLOCK KEY SKIPPED!", key, err)
				} else if isLatestBlock {
					failedLatestBlock = true
					log.Printf("[ERROR] Failed to get latest_block key: %v - latest_block SKIPPED!", err)
				} else {
					log.Printf("[WARN] Get(%s): %v", key, err)
				}
				continue
			}

			if isBlockKey {
				blockKeysBackedUp++
				if blockKeysBackedUp <= 20 || blockKeysBackedUp%100 == 0 {
					log.Printf("[INFO] Successfully retrieved block key '%s' (value length: %d) [%d/%d blocks]",
						key, len(resp.Value), blockKeysBackedUp, blockKeysInHashMap)
				}
			} else if isLatestBlock {
				latestBlockBackedUp = true
				log.Printf("[INFO] Successfully retrieved latest_block key (value length: %d)", len(resp.Value))
			}

			record := map[string]interface{}{
				"Key":      key,
				"Value":    string(resp.Value),
				"Database": cfg.Database,
			}
			recordsBatch = append(recordsBatch, record)
			processed++

			// Write to AVRO in batches to improve I/O efficiency
			if len(recordsBatch) >= writeBatchSize {
				if err := ocfWriter.Append(recordsBatch); err != nil {
					log.Printf("[WARN] Avro batch append failed: %v", err)
					// Continue with next batch even if this one failed
				}
				recordsBatch = recordsBatch[:0] // Clear batch
			}
		}

		// Progress update every 1000 keys
		if processed%1000 == 0 {
			log.Printf("Progress: %d/%d keys processed (%.1f%%)", processed, totalKeys, float64(processed)/float64(totalKeys)*100)
		}
	}

	// Write remaining records
	if len(recordsBatch) > 0 {
		if err := ocfWriter.Append(recordsBatch); err != nil {
			log.Printf("[WARN] Avro final batch append failed: %v", err)
		}
	}

	log.Printf("Avro export complete. Processed %d/%d keys", processed, totalKeys)

	// Block keys summary
	if blockKeysInHashMap > 0 || latestBlockInHashMap {
		log.Printf("=== BLOCK KEYS SUMMARY ===")
		log.Printf("Block keys in HashMap: %d", blockKeysInHashMap)
		log.Printf("Block keys successfully backed up: %d", blockKeysBackedUp)
		log.Printf("Block keys failed/skipped: %d", failedBlockKeys)
		log.Printf("latest_block in HashMap: %v", latestBlockInHashMap)
		log.Printf("latest_block successfully backed up: %v", latestBlockBackedUp)
		log.Printf("latest_block failed/skipped: %v", failedLatestBlock)

		if blockKeysBackedUp < blockKeysInHashMap || (latestBlockInHashMap && !latestBlockBackedUp) {
			log.Printf("WARNING: Some block keys were not backed up! This will cause blocks to not sync.")
			log.Printf("Missing: %d block keys, latest_block: %v",
				blockKeysInHashMap-blockKeysBackedUp, latestBlockInHashMap && !latestBlockBackedUp)
		}
	}

	return nil
}
