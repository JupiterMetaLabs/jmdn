// Phase 1: Replay all blocks from defaultdb and export per-address stats to JSON.
//
// For every confirmed transaction it records:
//   - tx_count  : true number of transactions sent by this address
//   - max_nonce : highest nonce value recorded in any of those transactions
//
// Output: replayed_accounts.json  (safe to review before running patch_accounts.go)
//
// Usage:
//   go run scripts/replay_blocks_and_export.go -host 127.0.0.1 -port 3322 -user immudb -pass immudb

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/codenotary/immudb/pkg/client"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// AccountStats holds the ground-truth values computed from block replay.
type AccountStats struct {
	TxCount      uint64 `json:"tx_count"`
	MaxNonce     uint64 `json:"max_nonce"`
	MaxNonceSet  bool   `json:"max_nonce_set"` // true once we have seen at least one tx
}

func main() {
	immudbHost := flag.String("host", "127.0.0.1", "ImmuDB Host")
	immudbPort := flag.Int("port", 3322, "ImmuDB Port")
	immudbUser := flag.String("user", "immudb", "ImmuDB Username")
	immudbPass := flag.String("pass", "immudb", "ImmuDB Password")
	outputFile := flag.String("out", "replayed_accounts.json", "Output JSON file path")
	flag.Parse()

	ctx := context.Background()

	// 1. Connect directly to defaultdb (read-only intent — we only call Get)
	opts := client.DefaultOptions().WithAddress(*immudbHost).WithPort(*immudbPort)
	c := client.NewClient().WithOptions(opts)
	if err := c.OpenSession(ctx, []byte(*immudbUser), []byte(*immudbPass), "defaultdb"); err != nil {
		log.Fatalf("FATAL: failed to open session on defaultdb: %v", err)
	}
	defer c.CloseSession(ctx)

	fmt.Println("[Phase 1] Connected to defaultdb. Fetching chain tip...")

	// 2. Read latest_block — stored as JSON-encoded uint64
	entry, err := c.Get(ctx, []byte("latest_block"))
	if err != nil {
		log.Fatalf("FATAL: failed to get latest_block key: %v", err)
	}

	var latestBlock uint64
	if err := json.Unmarshal(entry.Value, &latestBlock); err != nil {
		log.Fatalf("FATAL: failed to parse latest_block as uint64: %v", err)
	}

	fmt.Printf("[Phase 1] Chain tip = block %d. Replaying 0 → %d...\n", latestBlock, latestBlock)

	// address (checksummed hex) → stats
	stats := make(map[string]*AccountStats, 512)
	totalTxSeen := 0
	blocksWithErrors := 0

	// 3. Sequential block replay — O(N blocks)
	for i := uint64(0); i <= latestBlock; i++ {
		blockKey := fmt.Sprintf("%s%d", DB_OPs.PREFIX_BLOCK, i)
		bEntry, err := c.Get(ctx, []byte(blockKey))
		if err != nil {
			// Don't fatal — log and continue so we get partial data on sparse chains.
			log.Printf("WARN: block %d not found (key=%s): %v — skipping", i, blockKey, err)
			blocksWithErrors++
			continue
		}

		var block config.ZKBlock
		if err := json.Unmarshal(bEntry.Value, &block); err != nil {
			log.Printf("WARN: failed to unmarshal block %d: %v — skipping", i, err)
			blocksWithErrors++
			continue
		}

		for _, tx := range block.Transactions {
			if tx.From == nil {
				// Contract-creation submitted without a sender — skip.
				continue
			}

			// common.Address.Hex() → "0xAbCd..." (EIP-55 checksum).
			// This is the same format used by storeAccount via fmt.Sprintf("%s%s", Prefix, addr).
			sender := tx.From.Hex()

			s, exists := stats[sender]
			if !exists {
				s = &AccountStats{}
				stats[sender] = s
			}

			s.TxCount++
			totalTxSeen++

			// Track the highest nonce seen across all transactions for this address.
			// We initialise MaxNonce to 0 and MaxNonceSet to false so that nonce=0
			// is correctly handled on the very first tx.
			if !s.MaxNonceSet || tx.Nonce > s.MaxNonce {
				s.MaxNonce = tx.Nonce
				s.MaxNonceSet = true
			}
		}

		if i > 0 && i%1000 == 0 {
			fmt.Printf("  ... replayed through block %d / %d (%d txs so far)\n", i, latestBlock, totalTxSeen)
		}
	}

	fmt.Printf("[Phase 1] Replay complete. Blocks processed: %d, skipped: %d\n", latestBlock+1-uint64(blocksWithErrors), blocksWithErrors)
	fmt.Printf("[Phase 1] Total transactions: %d | Active addresses: %d\n", totalTxSeen, len(stats))

	// 4. Write JSON
	fileBytes, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		log.Fatalf("FATAL: failed to marshal stats to JSON: %v", err)
	}
	if err := os.WriteFile(*outputFile, fileBytes, 0644); err != nil {
		log.Fatalf("FATAL: failed to write %s: %v", *outputFile, err)
	}

	fmt.Printf("[Phase 1] Exported → %s\n", *outputFile)
	fmt.Println("[Phase 1] Please REVIEW the file before running patch_accounts.go (Phase 2).")
}
