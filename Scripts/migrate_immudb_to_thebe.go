package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	DB_OPs "gossipnode/DB_OPs"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/config"
	"gossipnode/config/settings"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/builder"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"
	thebeSql "github.com/JupiterMetaLabs/ThebeDB/pkg/sql"
	"github.com/ethereum/go-ethereum/common"
)

func main() {
	start := time.Now()

	var (
		thebeDSN  string
		thebeKV   string
		immuUser  string
		immuPass  string
		fromBlock uint64
		toBlock   uint64
	)

	flag.StringVar(&thebeDSN, "thebe-dsn", "", "Thebe PostgreSQL DSN (fallback: THEBE_SQL_DSN or settings)")
	flag.StringVar(&thebeKV, "thebe-kv-path", "", "Thebe KV path (fallback: settings)")
	flag.StringVar(&immuUser, "immudb-user", "", "ImmuDB username (fallback: settings)")
	flag.StringVar(&immuPass, "immudb-pass", "", "ImmuDB password (fallback: settings)")
	flag.Uint64Var(&fromBlock, "from-block", 0, "Start block number (inclusive)")
	flag.Uint64Var(&toBlock, "to-block", 0, "End block number (inclusive, 0 means latest)")
	flag.Parse()

	cfg, err := settings.Load()
	if err != nil {
		log.Fatalf("load settings: %v", err)
	}

	if immuUser == "" {
		immuUser = cfg.Database.Username
	}
	if immuPass == "" {
		immuPass = cfg.Database.Password
	}
	if thebeDSN == "" {
		thebeDSN = os.Getenv("THEBE_SQL_DSN")
	}
	if thebeDSN == "" {
		thebeDSN = cfg.Thebe.SQLDSN
	}
	if thebeKV == "" {
		thebeKV = cfg.Thebe.KVPath
	}

	if immuUser == "" || immuPass == "" {
		log.Fatal("missing immudb credentials: set --immudb-user/--immudb-pass or configure settings")
	}
	if thebeDSN == "" {
		log.Fatal("missing thebe SQL DSN: set --thebe-dsn or THEBE_SQL_DSN")
	}
	if thebeKV == "" {
		thebeKV = "./data/thebe-kv-migration"
	}

	// Keep settings-backed APIs in sync with explicit flags.
	settings.Get().Database.Username = immuUser
	settings.Get().Database.Password = immuPass

	log.Printf("starting migration: thebe_kv=%s from_block=%d to_block=%d", thebeKV, fromBlock, toBlock)

	if err := DB_OPs.InitMainDBPoolWithLoki(config.DefaultConnectionPoolConfig(), false, immuUser, immuPass); err != nil {
		log.Fatalf("init main immudb pool: %v", err)
	}
	if err := DB_OPs.InitAccountsPool(); err != nil {
		log.Fatalf("init accounts immudb pool: %v", err)
	}
	defer DB_OPs.CloseMainDBPool()

	reg := profile.NewRegistry()
	reg.Register(thebeprofile.NewJMDNProfile())

	kvStore, err := kv.NewStore(kv.Config{Backend: kv.BackendBadger, Path: thebeKV})
	if err != nil {
		log.Fatalf("kv store init: %v", err)
	}
	sqlEngine, err := thebeSql.NewSQLEngine(thebeDSN)
	if err != nil {
		log.Fatalf("sql engine init: %v", err)
	}
	thebe, err := thebedb.New(kvStore, sqlEngine, thebedb.WithProfileRegistry(reg))
	if err != nil {
		log.Fatalf("init thebedb: %v", err)
	}
	defer thebe.Close()

	outbox, err := thebegateway.NewOutboxStore(thebeKV + "/outbox.db")
	if err != nil {
		log.Fatalf("outbox store init: %v", err)
	}
	gw := thebegateway.NewThebeGateway(builder.New(thebe), nil, outbox)
	adapter := DB_OPs.NewGatewayAdapter(gw)

	ctx := context.Background()

	if err := migrateAccounts(ctx, gw); err != nil {
		log.Fatalf("migrate accounts: %v", err)
	}
	if err := ensureTxParticipantAccounts(ctx, gw, fromBlock, toBlock); err != nil {
		log.Fatalf("ensure tx participant accounts: %v", err)
	}
	if err := migrateBlocks(ctx, adapter, fromBlock, toBlock); err != nil {
		log.Fatalf("migrate blocks: %v", err)
	}

	log.Printf("migration completed in %s", time.Since(start).Round(time.Millisecond))
}

func migrateAccounts(ctx context.Context, gw thebegateway.ThebeGateway) error {
	accounts, err := DB_OPs.ListAllAccounts(nil, 0)
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	var migrated int
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		rec := accountToRecord(acc)
		if err := gw.WriteAccount(ctx, rec); err != nil {
			return fmt.Errorf("ingest account %s: %w", acc.Address.Hex(), err)
		}
		migrated++
		if migrated%100 == 0 {
			log.Printf("accounts migrated: %d", migrated)
		}
	}
	log.Printf("accounts migrated total: %d", migrated)
	return nil
}

// accountToRecord maps a DB_OPs.Account to an AccountRecord DTO.
func accountToRecord(acc *DB_OPs.Account) *thebegateway.AccountRecord {
	did := acc.DIDAddress
	if did == "" {
		did = "did:jmdn:" + acc.Address.Hex()
	}
	accountType := int16(0) // "did"
	if acc.AccountType == "publickey" {
		accountType = 1
	}
	return &thebegateway.AccountRecord{
		Address:     acc.Address.Hex(),
		DIDAddress:  did,
		BalanceWei:  acc.Balance,
		Nonce:       strconv.FormatUint(acc.Nonce, 10),
		AccountType: accountType,
		Metadata:    acc.Metadata,
		CreatedAt:   time.Unix(0, acc.CreatedAt).UTC(),
		UpdatedAt:   time.Unix(0, acc.UpdatedAt).UTC(),
	}
}

// accountAddressesFromImmu returns lower-hex keys (no 0x) for addresses already in immudb accounts.
func accountAddressesFromImmu() (map[string]struct{}, error) {
	accounts, err := DB_OPs.ListAllAccounts(nil, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		if a == nil {
			continue
		}
		out[strings.TrimPrefix(strings.ToLower(a.Address.Hex()), "0x")] = struct{}{}
	}
	return out, nil
}

func ensureTxParticipantAccounts(ctx context.Context, gw thebegateway.ThebeGateway, fromBlock, toBlock uint64) error {
	latest, err := DB_OPs.GetLatestBlockNumber(nil)
	if err != nil {
		return fmt.Errorf("get latest block number: %w", err)
	}
	if toBlock == 0 || toBlock > latest {
		toBlock = latest
	}
	if fromBlock > toBlock {
		return nil
	}

	known, err := accountAddressesFromImmu()
	if err != nil {
		return err
	}

	addrs := make(map[string]struct{})
	// Burn / placeholder address used when ZK txs omit `from`.
	addrs[strings.TrimPrefix(strings.ToLower(common.Address{}.Hex()), "0x")] = struct{}{}
	for n := fromBlock; n <= toBlock; n++ {
		block, err := DB_OPs.GetZKBlockByNumber(nil, n)
		if err != nil {
			continue
		}
		for _, tx := range block.Transactions {
			if tx.From != nil {
				addrs[strings.TrimPrefix(strings.ToLower(tx.From.Hex()), "0x")] = struct{}{}
			}
			if tx.To != nil {
				addrs[strings.TrimPrefix(strings.ToLower(tx.To.Hex()), "0x")] = struct{}{}
			}
		}
	}

	var stubs int
	for hexKey := range addrs {
		if _, ok := known[hexKey]; ok {
			continue
		}
		addr := common.HexToAddress("0x" + hexKey)
		stub := &thebegateway.AccountRecord{
			Address:     addr.Hex(),
			DIDAddress:  "did:jmdn:" + addr.Hex(),
			BalanceWei:  "0",
			Nonce:       "0",
			AccountType: 0,
			Metadata:    map[string]any{"migrated_stub": true},
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}
		if err := gw.WriteAccount(ctx, stub); err != nil {
			return fmt.Errorf("stub account %s: %w", addr.Hex(), err)
		}
		known[hexKey] = struct{}{}
		stubs++
	}
	if stubs > 0 {
		log.Printf("ingested stub accounts for tx participants: %d", stubs)
	}
	return nil
}

func migrateBlocks(_ context.Context, adapter *DB_OPs.GatewayAdapter, fromBlock, toBlock uint64) error {
	latest, err := DB_OPs.GetLatestBlockNumber(nil)
	if err != nil {
		return fmt.Errorf("get latest block number: %w", err)
	}
	if toBlock == 0 || toBlock > latest {
		toBlock = latest
	}
	if fromBlock > toBlock {
		return fmt.Errorf("invalid range: from-block %d > to-block %d", fromBlock, toBlock)
	}

	var migrated int
	for n := fromBlock; n <= toBlock; n++ {
		block, err := DB_OPs.GetZKBlockByNumber(nil, n)
		if err != nil {
			log.Printf("skip block %d: %v", n, err)
			continue
		}
		if err := adapter.StoreZKBlock(nil, block); err != nil {
			return fmt.Errorf("ingest block %d: %w", n, err)
		}
		migrated++
		if migrated%100 == 0 {
			log.Printf("blocks migrated: %d (last=%d)", migrated, n)
		}
	}
	log.Printf("blocks migrated total: %d (range=%d..%d latest=%d)", migrated, fromBlock, toBlock, latest)
	return nil
}
