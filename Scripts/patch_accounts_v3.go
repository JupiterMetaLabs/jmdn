package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

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
	TxCount     uint64 `json:"tx_count"`
	MaxNonce    uint64 `json:"max_nonce"`
	MaxNonceSet bool   `json:"max_nonce_set"`
}

func main() {
	flag.Parse()

	log.Printf("Starting V3 Migration with Pagination...")
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

	ctx := context.Background()
	c := client.NewClient().WithOptions(opts)
	err := c.OpenSession(ctx, []byte("immudb"), []byte("immudb"), "accountsdb")
	if err != nil {
		log.Fatalf("Failed to connect to accountsdb: %v", err)
	}
	defer c.CloseSession(ctx)

	log.Printf("Connected to accountsdb. Starting paginated patch process...")

	processedAccounts := 0
	patchedAccounts := 0
	parseErrors := 0

	prefix := []byte(DB_OPs.Prefix)
	var seekKey []byte

	for {
		req := &schema.ScanRequest{
			Prefix:  prefix,
			SeekKey: seekKey,
			Limit:   uint64(*batchSize),
			Desc:    false,
		}

		resp, err := c.Scan(ctx, req)
		if err != nil {
			log.Fatalf("FATAL: scan failed (seekKey=%q): %v", string(seekKey), err)
		}
		if len(resp.Entries) == 0 {
			break
		}

		// ImmuDB Scan with SeekKey is INCLUSIVE — skip the first entry if it
		// matches our cursor to avoid re-processing and infinite loops.
		startIndex := 0
		if seekKey != nil && len(resp.Entries) > 0 && bytes.Equal(resp.Entries[0].Key, seekKey) {
			startIndex = 1
		}

		// Build a batch update for all accounts that need patching in this page.
		ops := make([]*schema.Op, 0, *batchSize)

		for i := startIndex; i < len(resp.Entries); i++ {
			e := resp.Entries[i]

			var acc DB_OPs.Account
			if err := json.Unmarshal(e.Value, &acc); err != nil {
				log.Printf("WARN: skipping key %q — unmarshal error: %v", string(e.Key), err)
				parseErrors++
				continue
			}
			processedAccounts++

			// ── V3 Migration Logic ──────────────────────────────────────────────
			// 1. The old bad nonce is automatically loaded into acc.Nonce thanks to the `json:"nonce"` tag.
			if acc.Nonce == 0 {
				// Just a warning, not an error.
				log.Printf("Warning: Account %s had 0 for old nonce!", acc.Address.Hex())
			}

			// 2. Assign the true Ethereum Nonce & TxCount
			if stats, ok := statsMap[acc.Address.Hex()]; ok {
				acc.TxCountSent = stats.TxCount
				if stats.MaxNonceSet {
					acc.TxNonce = stats.MaxNonce + 1
				} else {
					acc.TxNonce = 1 // Fallback if max_nonce_set is somehow false but tx_count > 0
				}
			} else {
				acc.TxNonce = 0
				acc.TxCountSent = 0
			}

			// Re-marshal and prepare the operation
			valBytes, err := json.Marshal(acc)
			if err != nil {
				log.Printf("WARN: skipping key %q — re-marshal error: %v", string(e.Key), err)
				continue
			}

			ops = append(ops, &schema.Op{
				Operation: &schema.Op_Kv{
					Kv: &schema.KeyValue{
						Key:   e.Key,
						Value: valBytes,
					},
				},
			})
			patchedAccounts++
		}

		// Flush the batch for this page
		if len(ops) > 0 {
			_, err := c.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops})
			if err != nil {
				log.Fatalf("FATAL: batch write failed: %v", err)
			}
		}

		// Advance the cursor past the last key in this batch
		seekKey = resp.Entries[len(resp.Entries)-1].Key

		// Progress logging
		if processedAccounts%100_000 == 0 && processedAccounts > 0 {
			fmt.Printf("  ... processed %d accounts, patched %d so far\n", processedAccounts, patchedAccounts)
		}

		// If this was a partial batch we've reached the end.
		if len(resp.Entries) < *batchSize {
			break
		}
	}

	log.Printf("\n[Phase V3] Migration Complete!")
	log.Printf("  Accounts scanned : %d", processedAccounts)
	log.Printf("  Accounts patched : %d", patchedAccounts)
	log.Printf("  Parse errors     : %d", parseErrors)
	log.Printf("Old bad nonces successfully preserved in ART Nonce, and TxNonce initialized.")
}
