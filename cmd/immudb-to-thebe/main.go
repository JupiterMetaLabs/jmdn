// cmd/immudb-to-thebe: one-shot migration tool.
// Reads all blocks and accounts from ImmuDB and commits them to ThebeDB
// using the ThebeGateway interfaces defined in DB_OPs/thebegateway.
//
// Usage:
//
//	immudb-to-thebe \
//	  --thebe-kv-path ./data/thebe-kv \
//	  --thebe-sql-dsn "postgres://..." \
//	  [--start-block 0] \
//	  [--batch-size 500] \
//	  [--skip-blocks] \
//	  [--skip-accounts]
//
// Config is loaded from jmdn.yaml / JMDN_* env vars first; CLI flags override.
// ImmuDB address/port come from config defaults (localhost:3322).
// Run after settings.Load() so InitAccountsPool() can read credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"

	DB_OPs "gossipnode/DB_OPs"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/builder"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"
	thebeSql "github.com/JupiterMetaLabs/ThebeDB/pkg/sql"
)

func main() {
	thebeKVPath := flag.String("thebe-kv-path", "/opt/jmdn/thebe-kv", "BadgerDB path for ThebeDB KV store")
	thebeSQLDSN := flag.String("thebe-sql-dsn", "postgres://jmdn@0.0.0.0:5430/jmdn?sslmode=disable", "PostgreSQL DSN for ThebeDB SQL projection")
	_ = flag.String("thebe-redis-url", "redis://127.0.0.1:6379", "Redis URL (informational — cache is disabled in migration mode)")
	startBlock := flag.Uint64("start-block", 0, "Block number to start migration from (for resume)")
	batchSize := flag.Int("batch-size", 500, "Number of blocks per ImmuDB GetAll batch")
	skipBlocks := flag.Bool("skip-blocks", false, "Skip block/transaction migration")
	skipAccounts := flag.Bool("skip-accounts", false, "Skip account migration")
	flag.Parse()

	if *thebeKVPath == "" || *thebeSQLDSN == "" {
		fmt.Fprintln(os.Stderr, "error: --thebe-kv-path and --thebe-sql-dsn are required")
		flag.Usage()
		os.Exit(1)
	}

	// 1. Load node config (reads jmdn.yaml + JMDN_* env vars).
	//    InitAccountsPool() reads credentials from settings.Get() internally.
	if _, err := settings.Load(); err != nil {
		log.Fatalf("settings load: %v", err)
	}

	// 2. Init ImmuDB connection pools.
	poolCfg := config.DefaultConnectionPoolConfig()
	cfg := settings.Get()
	if err := DB_OPs.InitMainDBPoolWithLoki(poolCfg, false, cfg.Database.Username, cfg.Database.Password); err != nil {
		log.Fatalf("main DB pool init: %v", err)
	}
	if err := DB_OPs.InitAccountsPool(); err != nil {
		log.Fatalf("accounts DB pool init: %v", err)
	}
	log.Println("ImmuDB pools ready")

	// 3. Init ThebeDB (kv + sql + JMDN profile).
	reg := profile.NewRegistry()
	reg.Register(thebeprofile.NewJMDNProfile())

	kvStore, err := kv.NewStore(kv.Config{Backend: kv.BackendBadger, Path: *thebeKVPath})
	if err != nil {
		log.Fatalf("thebe kv init: %v", err)
	}
	sqlEngine, err := thebeSql.NewSQLEngine(*thebeSQLDSN)
	if err != nil {
		log.Fatalf("thebe sql init: %v", err)
	}
	db, err := thebedb.New(kvStore, sqlEngine, thebedb.WithProfileRegistry(reg))
	if err != nil {
		log.Fatalf("thebedb init: %v", err)
	}
	outbox, err := thebegateway.NewOutboxStore(*thebeKVPath + "/outbox.db")
	if err != nil {
		log.Fatalf("outbox init: %v", err)
	}
	gw := thebegateway.NewThebeGateway(builder.New(db), db.KV, nil, outbox)
	adapter := DB_OPs.NewGatewayAdapter(gw)
	log.Println("ThebeDB gateway ready")

	// 4. Migrate blocks.
	if !*skipBlocks {
		if err := migrateBlocks(adapter, *startBlock, *batchSize); err != nil {
			log.Fatalf("block migration: %v", err)
		}
	}

	// 5. Migrate accounts.
	if !*skipAccounts {
		if err := migrateAccounts(gw); err != nil {
			log.Fatalf("account migration: %v", err)
		}
	}

	log.Println("migration complete")
}

// migrateBlocks iterates all blocks from startBlock to the latest block and
// fans each one out to ThebeDB via the GatewayAdapter (block + snapshot + zk_proof + txs).
func migrateBlocks(adapter *DB_OPs.GatewayAdapter, startBlock uint64, batchSize int) error {
	ctx := context.Background()
	mainConn, err := DB_OPs.GetMainDBConnection(ctx)
	if err != nil {
		return fmt.Errorf("get main DB conn: %w", err)
	}
	defer DB_OPs.PutMainDBConnection(mainConn)

	latest, err := DB_OPs.GetLatestBlockNumber(mainConn)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}
	if latest == 0 {
		log.Println("blocks: no blocks in ImmuDB, skipping")
		return nil
	}
	log.Printf("blocks: migrating %d → %d (%d total)", startBlock, latest, latest-startBlock+1)

	iter := DB_OPs.NewBlockIterator(mainConn, startBlock, latest, batchSize)
	var migrated, failed int
	for {
		blocks, err := iter.Next()
		if err != nil {
			return fmt.Errorf("block iterator: %w", err)
		}
		if len(blocks) == 0 {
			break
		}
		for _, block := range blocks {
			if err := adapter.StoreZKBlock(nil, block); err != nil {
				log.Printf("blocks: WARN block %d failed: %v", block.BlockNumber, err)
				failed++
				continue
			}
			migrated++
		}
		log.Printf("blocks: %d migrated, %d failed (last batch end ~%d)",
			migrated, failed, blocks[len(blocks)-1].BlockNumber)
	}
	log.Printf("blocks: done — %d migrated, %d failed", migrated, failed)
	return nil
}

// migrateAccounts scans all "address:" keys from the accounts DB and writes
// each account to ThebeDB via gw.WriteAccount().
func migrateAccounts(gw thebegateway.ThebeGateway) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	accConn, err := DB_OPs.GetAccountsConnections(ctx)
	if err != nil {
		return fmt.Errorf("get accounts conn: %w", err)
	}
	defer DB_OPs.PutAccountsConnection(accConn)

	// GetAllKeys with a non-nil accounts-DB connection scans that DB.
	keys, err := DB_OPs.GetAllKeys(accConn, DB_OPs.Prefix)
	if err != nil {
		return fmt.Errorf("scan account keys: %w", err)
	}
	log.Printf("accounts: found %d keys", len(keys))

	var migrated, failed int
	for _, key := range keys {
		hexAddr := strings.TrimPrefix(key, DB_OPs.Prefix)
		if hexAddr == "" {
			continue
		}
		addr := common.HexToAddress(hexAddr)

		acc, err := DB_OPs.GetAccount(accConn, addr)
		if err != nil {
			log.Printf("accounts: WARN read %s failed: %v", hexAddr, err)
			failed++
			continue
		}

		rec := accountToRecord(acc)
		if err := gw.WriteAccount(ctx, rec); err != nil {
			log.Printf("accounts: WARN write %s failed: %v", hexAddr, err)
			failed++
			continue
		}
		migrated++
		if migrated%1000 == 0 {
			log.Printf("accounts: %d migrated so far…", migrated)
		}
	}
	log.Printf("accounts: done — %d migrated, %d failed", migrated, failed)
	return nil
}

// accountToRecord maps DB_OPs.Account → thebegateway.AccountRecord.
func accountToRecord(acc *DB_OPs.Account) *thebegateway.AccountRecord {
	var accType int16 // 0 = legacy/did, 1 = publickey
	if acc.AccountType == "publickey" {
		accType = 1
	}

	createdAt := time.Unix(0, acc.CreatedAt).UTC()
	updatedAt := time.Unix(0, acc.UpdatedAt).UTC()
	if acc.CreatedAt == 0 {
		createdAt = time.Now().UTC()
	}
	if acc.UpdatedAt == 0 {
		updatedAt = createdAt
	}

	var meta map[string]any
	if acc.Metadata != nil {
		meta = make(map[string]any, len(acc.Metadata))
		maps.Copy(meta, acc.Metadata)
	}

	return &thebegateway.AccountRecord{
		Address:     acc.Address.Hex(),
		DIDAddress:  acc.DIDAddress,
		BalanceWei:  balanceOrZero(acc.Balance),
		Nonce:       strconv.FormatUint(acc.Nonce, 10),
		AccountType: accType,
		Metadata:    meta,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

func balanceOrZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}
