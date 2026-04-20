//go:build archive
// +build archive

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	DB_OPs "gossipnode/DB_OPs"
	"gossipnode/DB_OPs/cassata"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/config"
	"gossipnode/config/settings"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	thebecfg "github.com/JupiterMetaLabs/ThebeDB/pkg/config"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"
	_ "github.com/lib/pq"
)

const (
	defaultSample = 1000
	defaultDrift  = 0
)

type report struct {
	blockCountImmu    int
	blockCountThebe   int
	blockCountDelta   int
	blockSampleMism   []string
	accountCountImmu  int
	accountCountThebe int
	accountCountDelta int
	accountFieldMism  []string
}

type blockRow struct {
	BlockHash string
}

type accountRow struct {
	Address    string
	DIDAddress sql.NullString
	BalanceWei string
	Nonce      string
}

func main() {
	start := time.Now().UTC()
	var sample int
	var drift int
	flag.IntVar(&sample, "sample", defaultSample, "sample size for blocks and accounts")
	flag.IntVar(&drift, "drift", defaultDrift, "allowed absolute account count drift")
	flag.Parse()
	if sample <= 0 {
		log.Fatal("--sample must be > 0")
	}
	if drift < 0 {
		log.Fatal("--drift must be >= 0")
	}

	cfg, err := settings.Load()
	if err != nil {
		log.Fatalf("load settings: %v", err)
	}

	sqlDSN := os.Getenv("THEBE_SQL_DSN")
	if sqlDSN == "" {
		log.Fatal("missing THEBE_SQL_DSN")
	}
	kvPath := os.Getenv("THEBE_KV_PATH")
	if kvPath == "" {
		log.Fatal("missing THEBE_KV_PATH")
	}

	immuUser := os.Getenv("IMMUDB_USER")
	immuPass := os.Getenv("IMMUDB_PASS")
	if immuUser == "" {
		immuUser = cfg.Database.Username
	}
	if immuPass == "" {
		immuPass = cfg.Database.Password
	}
	if immuUser == "" || immuPass == "" {
		log.Fatal("missing immudb credentials: IMMUDB_USER / IMMUDB_PASS")
	}
	if addr := os.Getenv("IMMUDB_ADDR"); addr != "" && addr != "localhost:3322" {
		log.Fatalf("IMMUDB_ADDR=%q is not supported by current pool wiring (expected localhost:3322)", addr)
	}

	if err := DB_OPs.InitMainDBPoolWithLoki(config.DefaultConnectionPoolConfig(), false, immuUser, immuPass); err != nil {
		log.Fatalf("init main immudb pool: %v", err)
	}
	if err := DB_OPs.InitAccountsPool(); err != nil {
		log.Fatalf("init accounts immudb pool: %v", err)
	}
	defer DB_OPs.CloseMainDBPool()

	sqlDB, err := sql.Open("postgres", sqlDSN)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		log.Fatalf("ping postgres: %v", err)
	}

	reg := profile.NewRegistry()
	reg.Register(thebeprofile.New())
	thebe, err := thebedb.NewFromConfig(thebedb.Config{
		KV:       kv.Config{Backend: kv.BackendBadger, Path: kvPath},
		SQL:      thebecfg.SQL{DSN: sqlDSN},
		Profiles: reg,
	})
	if err != nil {
		log.Fatalf("init thebedb: %v", err)
	}
	defer thebe.Close()
	cas := cassata.New(thebe, nil)

	rng := rand.New(rand.NewSource(time.Now().UTC().UnixNano()))
	rep, err := runChecks(ctx, sqlDB, cas, rng, sample)
	if err != nil {
		log.Fatalf("run checks: %v", err)
	}

	printReport(rep, drift)

	fmt.Printf("completed in %s\n", time.Since(start).Round(time.Millisecond))
	if !accept(rep, drift) {
		os.Exit(1)
	}
}

func runChecks(ctx context.Context, sqlDB *sql.DB, cas *cassata.Cassata, rng *rand.Rand, sample int) (*report, error) {
	rep := &report{}
	var err error

	rep.blockCountImmu, err = DB_OPs.CountBuilder{}.GetMainDBCount(DB_OPs.PREFIX_BLOCK_HASH)
	if err != nil {
		return nil, fmt.Errorf("immudb block count: %w", err)
	}
	rep.blockCountThebe, err = sqlCount(ctx, sqlDB, "SELECT COUNT(*) FROM blocks")
	if err != nil {
		return nil, fmt.Errorf("thebe block count: %w", err)
	}
	rep.blockCountDelta = rep.blockCountThebe - rep.blockCountImmu

	latest, err := DB_OPs.GetLatestBlockNumber(nil)
	if err != nil {
		return nil, fmt.Errorf("latest block: %w", err)
	}
	for _, n := range randomBlockNumbers(latest, sample, rng) {
		immuBlock, getErr := DB_OPs.GetZKBlockByNumber(nil, n)
		if getErr != nil || immuBlock == nil {
			rep.blockSampleMism = append(rep.blockSampleMism, fmt.Sprintf("block %d missing in immudb read", n))
			continue
		}
		tb, getErr := cas.GetBlock(ctx, n)
		if getErr != nil || tb == nil {
			rep.blockSampleMism = append(rep.blockSampleMism, fmt.Sprintf("block %d missing in thebe", n))
			continue
		}
		immuHash := strings.ToLower(immuBlock.BlockHash.Hex())
		thebeHash := strings.ToLower(tb.BlockHash)
		if immuHash != thebeHash {
			rep.blockSampleMism = append(rep.blockSampleMism, fmt.Sprintf("block %d hash immudb=%s thebe=%s", n, immuHash, thebeHash))
		}
		txs, txErr := cas.ListTransactionsByBlock(ctx, n)
		if txErr != nil {
			rep.blockSampleMism = append(rep.blockSampleMism, fmt.Sprintf("block %d thebe tx fetch error: %v", n, txErr))
			continue
		}
		if len(immuBlock.Transactions) != len(txs) {
			rep.blockSampleMism = append(rep.blockSampleMism, fmt.Sprintf("block %d tx_count immudb=%d thebe=%d", n, len(immuBlock.Transactions), len(txs)))
		}
	}

	rep.accountCountImmu, err = DB_OPs.CountBuilder{}.GetAccountsDBCount(DB_OPs.Prefix)
	if err != nil {
		return nil, fmt.Errorf("immudb account count: %w", err)
	}
	rep.accountCountThebe, err = sqlCount(ctx, sqlDB, "SELECT COUNT(*) FROM accounts")
	if err != nil {
		return nil, fmt.Errorf("thebe account count: %w", err)
	}
	rep.accountCountDelta = rep.accountCountThebe - rep.accountCountImmu

	accounts, err := DB_OPs.ListAllAccounts(nil, 0)
	if err != nil {
		return nil, fmt.Errorf("list immudb accounts: %w", err)
	}
	for _, i := range randomIndexes(len(accounts), sample, rng) {
		ia := accounts[i]
		if ia == nil {
			rep.accountFieldMism = append(rep.accountFieldMism, fmt.Sprintf("account[%d] nil in immudb list", i))
			continue
		}
		ta, getErr := cas.GetAccount(ctx, ia.Address.Hex())
		if getErr != nil || ta == nil {
			rep.accountFieldMism = append(rep.accountFieldMism, fmt.Sprintf("missing account %s in thebe", ia.Address.Hex()))
			continue
		}
		immuAddr := strings.ToLower(ia.Address.Hex())
		if immuAddr != strings.ToLower(ta.Address) {
			rep.accountFieldMism = append(rep.accountFieldMism, fmt.Sprintf("address mismatch %s vs %s", ia.Address.Hex(), ta.Address))
		}

		immuDID := ia.DIDAddress
		thebeDID := ""
		if ta.DIDAddress != nil {
			thebeDID = *ta.DIDAddress
		}
		if immuDID != thebeDID {
			rep.accountFieldMism = append(rep.accountFieldMism, fmt.Sprintf("did mismatch %s immudb=%q thebe=%q", ia.Address.Hex(), immuDID, thebeDID))
		}

		if !numericEqual(ia.Balance, ta.BalanceWei) {
			rep.accountFieldMism = append(rep.accountFieldMism, fmt.Sprintf("balance mismatch %s immudb=%s thebe=%s", ia.Address.Hex(), ia.Balance, ta.BalanceWei))
		}
		if !numericEqual(strconv.FormatUint(ia.Nonce, 10), ta.Nonce) {
			rep.accountFieldMism = append(rep.accountFieldMism, fmt.Sprintf("nonce mismatch %s immudb=%d thebe=%s", ia.Address.Hex(), ia.Nonce, ta.Nonce))
		}
	}

	return rep, nil
}

func sqlCount(ctx context.Context, db *sql.DB, q string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func randomBlockNumbers(max uint64, sample int, rng *rand.Rand) []uint64 {
	if max == 0 {
		return []uint64{0}
	}
	out := make([]uint64, int(max)+1)
	for i := uint64(0); i <= max; i++ {
		out[i] = i
	}
	rng.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	if sample > len(out) {
		sample = len(out)
	}
	return out[:sample]
}

func randomIndexes(total, sample int, rng *rand.Rand) []int {
	if total == 0 {
		return nil
	}
	idx := make([]int, total)
	for i := range idx {
		idx[i] = i
	}
	rng.Shuffle(len(idx), func(i, j int) {
		idx[i], idx[j] = idx[j], idx[i]
	})
	if sample > len(idx) {
		sample = len(idx)
	}
	return idx[:sample]
}

func numericEqual(a, b string) bool {
	ai, okA := new(big.Int).SetString(strings.TrimSpace(a), 10)
	bi, okB := new(big.Int).SetString(strings.TrimSpace(b), 10)
	if !okA || !okB {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return ai.Cmp(bi) == 0
}

func printReport(rep *report, drift int) {
	fmt.Printf("blocks:   immudb=%d  thebe=%d  delta=%d  required=0\n", rep.blockCountImmu, rep.blockCountThebe, rep.blockCountDelta)
	fmt.Printf("accounts: immudb=%d  thebe=%d  delta=%d  allowed_drift=%d\n", rep.accountCountImmu, rep.accountCountThebe, rep.accountCountDelta, drift)
	fmt.Printf("block hash/tx_count sample mismatches: %d\n", len(rep.blockSampleMism))
	fmt.Printf("account field mismatches: %d\n", len(rep.accountFieldMism))

	if len(rep.blockSampleMism) > 0 {
		fmt.Println("block sample mismatch details:")
		for _, m := range rep.blockSampleMism {
			fmt.Printf("  - %s\n", m)
		}
	}
	if len(rep.accountFieldMism) > 0 {
		fmt.Println("account mismatch details:")
		for _, m := range rep.accountFieldMism {
			fmt.Printf("  - %s\n", m)
		}
	}
}

func accept(rep *report, drift int) bool {
	if rep.blockCountDelta != 0 {
		return false
	}
	if len(rep.blockSampleMism) != 0 {
		return false
	}
	if abs(rep.accountCountDelta) > drift {
		return false
	}
	if len(rep.accountFieldMism) != 0 {
		return false
	}
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
