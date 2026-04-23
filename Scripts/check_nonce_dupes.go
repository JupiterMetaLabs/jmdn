//go:build ignore

// check_nonce_dupes.go — scan the accounts DB and report duplicate nonces.
//
// Usage:
//
//	go run Scripts/check_nonce_dupes.go [flags]
//
// Flags:
//
//	-host     ImmuDB host          (default: 127.0.0.1)
//	-port     ImmuDB port          (default: 3322)
//	-user     ImmuDB username      (default: immudb)
//	-pass     ImmuDB password      (default: immudb)
//	-db       accounts DB name     (default: accountsdb)
//	-batch    scan batch size      (default: 100)
//	-prefix   account key prefix   (default: address:)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	immudb "github.com/codenotary/immudb/pkg/client"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/metadata"
)

// Account mirrors DB_OPs.Account — keep in sync if fields change.
type Account struct {
	DIDAddress  string         `json:"did,omitempty"`
	Address     common.Address `json:"address"`
	Balance     string         `json:"balance,omitempty"`
	Nonce       uint64         `json:"nonce"`
	AccountType string         `json:"account_type"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
}

func main() {
	host := flag.String("host", "127.0.0.1", "ImmuDB host")
	port := flag.Int("port", 3322, "ImmuDB port")
	user := flag.String("user", "immudb", "ImmuDB username")
	pass := flag.String("pass", "immudb", "ImmuDB password")
	dbName := flag.String("db", "accountsdb", "Accounts database name")
	batch := flag.Int("batch", 100, "Scan batch size")
	prefix := flag.String("prefix", "address:", "Account key prefix")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// --- Connect ---
	opts := immudb.DefaultOptions().WithAddress(*host).WithPort(*port)
	client := immudb.NewClient().WithOptions(opts)

	if err := client.OpenSession(ctx, []byte(*user), []byte(*pass), *dbName); err != nil {
		fmt.Fprintf(os.Stderr, "failed to open session: %v\n", err)
		os.Exit(1)
	}
	defer client.CloseSession(ctx)

	md := metadata.Pairs("setname", *dbName)
	ctx = metadata.NewOutgoingContext(ctx, md)

	fmt.Printf("Connected to immudb %s:%d, database: %s\n\n", *host, *port, *dbName)

	// --- Scan all address: keys ---
	accounts, err := scanAllAccounts(ctx, client, []byte(*prefix), *batch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Scanned %d accounts\n\n", len(accounts))

	// --- Group by nonce ---
	// nonceMap[nonce] = list of accounts with that nonce
	nonceMap := make(map[uint64][]*Account)
	for _, acc := range accounts {
		nonceMap[acc.Nonce] = append(nonceMap[acc.Nonce], acc)
	}

	// --- Find duplicates ---
	type dupeGroup struct {
		nonce    uint64
		accounts []*Account
	}
	var dupes []dupeGroup
	for nonce, accs := range nonceMap {
		if len(accs) > 1 {
			dupes = append(dupes, dupeGroup{nonce, accs})
		}
	}

	// Sort by nonce for deterministic output
	sort.Slice(dupes, func(i, j int) bool { return dupes[i].nonce < dupes[j].nonce })

	// --- Print all accounts ---
	fmt.Println("=== All accounts ===")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ADDRESS\tNONCE\tCREATED_AT\tNOTE")
	fmt.Fprintln(tw, "-------\t-----\t----------\t----")

	// Sort accounts by nonce for readability
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Nonce < accounts[j].Nonce })

	for _, acc := range accounts {
		createdAt := time.Unix(0, acc.CreatedAt).UTC().Format(time.RFC3339)
		note := ""
		if len(nonceMap[acc.Nonce]) > 1 {
			note = fmt.Sprintf("DUPLICATE NONCE (shared by %d accounts)", len(nonceMap[acc.Nonce]))
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", acc.Address.Hex(), acc.Nonce, createdAt, note)
	}
	tw.Flush()

	// --- Duplicate summary ---
	fmt.Println()
	if len(dupes) == 0 {
		fmt.Println("No duplicate nonces found.")
	} else {
		fmt.Printf("=== Duplicate nonces (%d) ===\n", len(dupes))
		tw2 := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw2, "NONCE\tADDRESS\tCREATED_AT")
		fmt.Fprintln(tw2, "-----\t-------\t----------")
		for _, d := range dupes {
			for i, acc := range d.accounts {
				nStr := fmt.Sprintf("%d", d.nonce)
				if i > 0 {
					nStr = "  (same)"
				}
				createdAt := time.Unix(0, acc.CreatedAt).UTC().Format(time.RFC3339)
				fmt.Fprintf(tw2, "%s\t%s\t%s\n", nStr, acc.Address.Hex(), createdAt)
			}
			fmt.Fprintln(tw2, "")
		}
		tw2.Flush()
	}
}

// scanAllAccounts pages through all keys with the given prefix and returns parsed accounts.
func scanAllAccounts(ctx context.Context, c immudb.ImmuClient, prefix []byte, batchSize int) ([]*Account, error) {
	var accounts []*Account
	var seekKey []byte

	for {
		req := &schema.ScanRequest{
			Prefix:  prefix,
			Limit:   uint64(batchSize),
			SeekKey: seekKey,
			Desc:    false,
		}

		result, err := c.Scan(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		if len(result.Entries) == 0 {
			break
		}

		startIdx := 0
		if seekKey != nil && len(result.Entries) > 0 &&
			string(result.Entries[0].Key) == string(seekKey) {
			startIdx = 1 // skip the seek key (inclusive pagination)
		}

		for i := startIdx; i < len(result.Entries); i++ {
			entry := result.Entries[i]
			var acc Account
			if err := json.Unmarshal(entry.Value, &acc); err != nil {
				fmt.Fprintf(os.Stderr, "warn: skip key %s — unmarshal error: %v\n", entry.Key, err)
				continue
			}
			accounts = append(accounts, &acc)
		}

		if len(result.Entries) < batchSize {
			break
		}
		seekKey = result.Entries[len(result.Entries)-1].Key
	}

	return accounts, nil
}
