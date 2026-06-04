package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gossipnode/DB_OPs"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
)

var (
	dataDir     = flag.String("dir", "/opt/jmdn/data-old-live", "Path to pre-migrated ImmuDB data directory")
	replayStats = flag.String("replay-stats", "replayed_accounts.json", "Optional JSON file with actual TxCounts (Address -> {Nonce, TxCount})")
	batchSize   = flag.Int("batch", 500, "Number of accounts per commit")
)

type AccountStats struct {
	TxCount uint64 `json:"tx_count"`
	MaxNonce uint64 `json:"max_nonce"`
}

func main() {
	flag.Parse()

	log.Printf("Starting V3 Migration...")
	log.Printf("Data Directory: %s", *dataDir)

	// 1. Load Replay Stats (if available)
	statsMap := make(map[string]AccountStats)
	if data, err := os.ReadFile(*replayStats); err == nil {
		if err := json.Unmarshal(data, &statsMap); err != nil {
			log.Fatalf("Failed to parse %s: %v", *replayStats, err)
		}
		log.Printf("Loaded true transaction stats for %d active accounts", len(statsMap))
	} else {
		log.Printf("No replay stats found at %s. Proceeding with TxNonce=0 for all accounts.", *replayStats)
	}

	// 2. Initialize ImmuDB
	opts := client.DefaultOptions().
		WithDir(*dataDir).
		WithAddress("127.0.0.1").
		WithPort(3323)

	c := client.NewClient().WithOptions(opts)
	err := c.OpenSession(context.Background(), []byte("immudb"), []byte("immudb"), "defaultdb")
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer c.CloseSession(context.Background())

	// 3. Scan all accounts
	req := &schema.ScanRequest{
		Prefix: []byte(DB_OPs.Prefix),
		Desc:   false,
	}

	entries, err := c.Scan(context.Background(), req)
	if err != nil {
		log.Fatalf("Failed to scan: %v", err)
	}

	log.Printf("Found %d total account entries to migrate", len(entries.Entries))

	var currentBatch []*schema.KeyValue
	migratedCount := 0

	for _, entry := range entries.Entries {
		var acc DB_OPs.Account
		if err := json.Unmarshal(entry.Value, &acc); err != nil {
			log.Printf("Warning: skipped unparseable account %s", string(entry.Key))
			continue
		}

		// THE MIGRATION LOGIC
		// 1. The old bad nonce is automatically loaded into acc.Nonce thanks to the `json:"nonce"` tag.
		// We just need to ensure it's not zero (though it shouldn't be for old accounts).
		if acc.Nonce == 0 {
			log.Printf("Warning: Account %s had 0 for old nonce!", acc.Address.Hex())
		}

		// 2. Assign the true Ethereum Nonce & TxCount (if available)
		if stats, ok := statsMap[acc.Address.Hex()]; ok {
			acc.TxNonce = stats.MaxNonce
			acc.TxCountSent = stats.TxCount
		} else {
			acc.TxNonce = 0
			acc.TxCountSent = 0
		}

		// Re-serialize
		newBytes, err := json.Marshal(acc)
		if err != nil {
			log.Fatalf("Marshal failed: %v", err)
		}

		currentBatch = append(currentBatch, &schema.KeyValue{
			Key:   entry.Key,
			Value: newBytes,
		})

		if len(currentBatch) >= *batchSize {
			commitBatch(c, currentBatch)
			migratedCount += len(currentBatch)
			currentBatch = nil
			log.Printf("Migrated %d accounts...", migratedCount)
		}
	}

	if len(currentBatch) > 0 {
		commitBatch(c, currentBatch)
		migratedCount += len(currentBatch)
	}

	log.Printf("SUCCESS: Migrated %d accounts.", migratedCount)
	log.Printf("Old bad nonces successfully preserved in ART Nonce, and TxNonce initialized.")
}

func commitBatch(c client.ImmuClient, batch []*schema.KeyValue) {
	req := &schema.SetRequest{KVs: batch}
	if _, err := c.SetAll(context.Background(), req); err != nil {
		log.Fatalf("Failed to commit batch: %v", err)
	}
}
