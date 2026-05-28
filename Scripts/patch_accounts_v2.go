// Phase 2: Read replayed_accounts.json (produced by replay_blocks_and_export.go) and patch
// every account in accountsdb with the ground-truth Nonce, TxCountSent, and standardised timestamps.
//
// What gets patched per account:
//   - Nonce       : set to MaxNonce + 1 to prevent replay attacks (since gapless nonces weren't strictly enforced before)
//   - TxCountSent : set to TxCount to decouple analytical transaction count from cryptographic Nonce
//   - CreatedAt   : if stored in seconds (< 1e15), multiply by 1e9 → nanoseconds
//   - UpdatedAt   : same normalisation as CreatedAt
//
// IMPORTANT: run replay_blocks_and_export.go first and review its output before running this.
//
// Usage:
//   go run scripts/patch_accounts_v2.go -host 127.0.0.1 -port 3323 -user immudb -pass immudb

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"

	"gossipnode/DB_OPs"
)

// AccountStats mirrors the struct from replay_blocks_and_export.go.
type AccountStats struct {
	TxCount     uint64 `json:"tx_count"`
	MaxNonce    uint64 `json:"max_nonce"`
	MaxNonceSet bool   `json:"max_nonce_set"`
}

const scanBatchSize = 500 // keep batches small for accountsdb health

func main() {
	immudbHost := flag.String("host", "127.0.0.1", "ImmuDB Host")
	immudbPort := flag.Int("port", 3322, "ImmuDB Port")
	immudbUser := flag.String("user", "immudb", "ImmuDB Username")
	immudbPass := flag.String("pass", "immudb", "ImmuDB Password")
	inputFile := flag.String("in", "replayed_accounts.json", "Input JSON file produced by Phase 1")
	flag.Parse()

	// 1. Load the ground-truth snapshot produced by Phase 1
	fileBytes, err := os.ReadFile(*inputFile)
	if err != nil {
		log.Fatalf("FATAL: cannot read %s: %v\n  → Did you run replay_blocks_and_export.go first?", *inputFile, err)
	}

	stats := make(map[string]*AccountStats)
	if err := json.Unmarshal(fileBytes, &stats); err != nil {
		log.Fatalf("FATAL: failed to parse %s: %v", *inputFile, err)
	}
	fmt.Printf("[Phase 2] Loaded %d active addresses from %s\n", len(stats), *inputFile)

	ctx := context.Background()

	// 2. Connect to accountsdb
	opts := client.DefaultOptions().WithAddress(*immudbHost).WithPort(*immudbPort)
	c := client.NewClient().WithOptions(opts)
	if err := c.OpenSession(ctx, []byte(*immudbUser), []byte(*immudbPass), "accountsdb"); err != nil {
		log.Fatalf("FATAL: failed to open session on accountsdb: %v", err)
	}
	defer c.CloseSession(ctx)

	fmt.Println("[Phase 2] Connected to accountsdb. Starting patch process...")

	// Counters
	processedAccounts := 0
	patchedAccounts := 0
	fixedNonces := 0
	fixedTimestamps := 0
	parseErrors := 0

	prefix := []byte(DB_OPs.Prefix) // "address:"
	var seekKey []byte               // nil = start from first key

	for {
		req := &schema.ScanRequest{
			Prefix:  prefix,
			SeekKey: seekKey,
			Limit:   scanBatchSize,
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
		ops := make([]*schema.Op, 0, scanBatchSize)

		for i := startIndex; i < len(resp.Entries); i++ {
			e := resp.Entries[i]

			var acc DB_OPs.Account
			if err := json.Unmarshal(e.Value, &acc); err != nil {
				log.Printf("WARN: skipping key %q — unmarshal error: %v", string(e.Key), err)
				parseErrors++
				continue
			}
			processedAccounts++

			needsPatch := false

			// ── Timestamp normalisation ──────────────────────────────────────────────
			// Timestamps stored as Unix seconds are < 1_000_000_000_000_000 (1e15).
			// Multiply by 1e9 to convert to nanoseconds.
			const nsThreshold = int64(1_000_000_000_000_000) // 1e15

			if acc.CreatedAt > 0 && acc.CreatedAt < nsThreshold {
				acc.CreatedAt *= 1_000_000_000
				fixedTimestamps++
				needsPatch = true
			}
			if acc.UpdatedAt > 0 && acc.UpdatedAt < nsThreshold {
				acc.UpdatedAt *= 1_000_000_000
				fixedTimestamps++
				needsPatch = true
			}

			// ── Nonce and TxCountSent correction ─────────────────────────────────────
			var trueNonce uint64 = 0
			var trueTxCountSent uint64 = 0
			
			if s, exists := stats[acc.Address.Hex()]; exists {
				trueTxCountSent = s.TxCount
				if s.MaxNonceSet {
					trueNonce = s.MaxNonce + 1
				} else {
					trueNonce = 1 // Fallback if max_nonce_set is somehow false but tx_count > 0
				}
			}

			if acc.Nonce != trueNonce || acc.TxCountSent != trueTxCountSent {
				acc.Nonce = trueNonce
				acc.TxCountSent = trueTxCountSent
				fixedNonces++
				needsPatch = true
			}

			if needsPatch {
				patchedAccounts++
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
			}
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
		if len(resp.Entries) < scanBatchSize {
			break
		}
	}

	fmt.Println("\n[Phase 2] Migration Complete!")
	fmt.Printf("  Accounts scanned : %d\n", processedAccounts)
	fmt.Printf("  Accounts patched : %d\n", patchedAccounts)
	fmt.Printf("    Nonces fixed   : %d\n", fixedNonces)
	fmt.Printf("    Timestamps fixed: %d\n", fixedTimestamps)
	fmt.Printf("  Parse errors     : %d\n", parseErrors)
}
