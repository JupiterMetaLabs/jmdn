package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"

	DB_OPs "gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"

	_ "github.com/lib/pq"
)

const (
	sampleSize          = 10
	maxAllowedDivergPct = 0.001 // 0.1%
)

type checkResult struct {
	name        string
	immuCount   int
	thebeCount  int
	countMatch  bool
	samplesOK   bool
	missingKeys []string
}

func main() {
	start := time.Now().UTC()

	cfg, err := settings.Load()
	if err != nil {
		log.Fatalf("load settings: %v", err)
	}

	dsn := os.Getenv("TEST_THEBE_SQL_DSN")
	if dsn == "" {
		log.Fatal("missing TEST_THEBE_SQL_DSN")
	}

	if cfg.Database.Username == "" || cfg.Database.Password == "" {
		log.Fatal("missing immudb credentials in settings")
	}

	if err := DB_OPs.InitMainDBPoolWithLoki(config.DefaultConnectionPoolConfig(), false, cfg.Database.Username, cfg.Database.Password); err != nil {
		log.Fatalf("init main immudb pool: %v", err)
	}
	if err := DB_OPs.InitAccountsPool(); err != nil {
		log.Fatalf("init accounts immudb pool: %v", err)
	}
	defer DB_OPs.CloseMainDBPool()

	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		log.Fatalf("ping postgres: %v", err)
	}

	rng := rand.New(rand.NewSource(time.Now().UTC().UnixNano()))

	blocks, err := checkBlocks(ctx, sqlDB, rng)
	if err != nil {
		log.Fatalf("check blocks: %v", err)
	}
	transactions, err := checkTransactions(ctx, sqlDB, rng)
	if err != nil {
		log.Fatalf("check transactions: %v", err)
	}
	accounts, err := checkAccounts(ctx, sqlDB, rng)
	if err != nil {
		log.Fatalf("check accounts: %v", err)
	}

	results := []checkResult{blocks, transactions, accounts}
	for _, r := range results {
		match := "no"
		if r.countMatch && r.samplesOK {
			match = "yes"
		}
		fmt.Printf("%-13s immudb=%d  thebe=%d  match=%s\n", r.name+":", r.immuCount, r.thebeCount, match)
	}

	var failed bool
	for _, r := range results {
		if !r.countMatch || !r.samplesOK {
			failed = true
			if len(r.missingKeys) > 0 {
				fmt.Printf("missing %s keys (%d): %s\n", r.name, len(r.missingKeys), strings.Join(r.missingKeys, ", "))
			}
		}
	}

	fmt.Printf("completed in %s\n", time.Since(start).Round(time.Millisecond))
	if failed {
		os.Exit(1)
	}
}

func checkBlocks(ctx context.Context, db *sql.DB, rng *rand.Rand) (checkResult, error) {
	immuCount, err := DB_OPs.CountBuilder{}.GetMainDBCount(DB_OPs.PREFIX_BLOCK_HASH)
	if err != nil {
		return checkResult{}, err
	}
	thebeCount, err := sqlCount(ctx, db, "SELECT COUNT(*) FROM blocks")
	if err != nil {
		return checkResult{}, err
	}

	keys, err := DB_OPs.GetAllKeys(nil, DB_OPs.PREFIX_BLOCK_HASH)
	if err != nil {
		return checkResult{}, err
	}
	samples := pickRandom(keys, sampleSize, rng)
	missing, err := missingSamples(ctx, db, samples, DB_OPs.PREFIX_BLOCK_HASH, "SELECT EXISTS(SELECT 1 FROM blocks WHERE lower(block_hash)=lower($1))")
	if err != nil {
		return checkResult{}, err
	}

	return checkResult{
		name:        "blocks",
		immuCount:   immuCount,
		thebeCount:  thebeCount,
		countMatch:  withinThreshold(immuCount, thebeCount),
		samplesOK:   len(missing) == 0,
		missingKeys: missing,
	}, nil
}

func checkTransactions(ctx context.Context, db *sql.DB, rng *rand.Rand) (checkResult, error) {
	immuCount, err := DB_OPs.CountBuilder{}.GetMainDBCount(DB_OPs.DEFAULT_PREFIX_TX)
	if err != nil {
		return checkResult{}, err
	}
	thebeCount, err := sqlCount(ctx, db, "SELECT COUNT(*) FROM transactions")
	if err != nil {
		return checkResult{}, err
	}

	keys, err := DB_OPs.GetAllKeys(nil, DB_OPs.DEFAULT_PREFIX_TX)
	if err != nil {
		return checkResult{}, err
	}
	samples := pickRandom(keys, sampleSize, rng)
	missing, err := missingSamples(ctx, db, samples, DB_OPs.DEFAULT_PREFIX_TX, "SELECT EXISTS(SELECT 1 FROM transactions WHERE lower(tx_hash)=lower($1))")
	if err != nil {
		return checkResult{}, err
	}

	return checkResult{
		name:        "transactions",
		immuCount:   immuCount,
		thebeCount:  thebeCount,
		countMatch:  withinThreshold(immuCount, thebeCount),
		samplesOK:   len(missing) == 0,
		missingKeys: missing,
	}, nil
}

func checkAccounts(ctx context.Context, db *sql.DB, rng *rand.Rand) (checkResult, error) {
	immuCount, err := DB_OPs.CountBuilder{}.GetAccountsDBCount(DB_OPs.Prefix)
	if err != nil {
		return checkResult{}, err
	}
	thebeCount, err := sqlCount(ctx, db, "SELECT COUNT(*) FROM accounts")
	if err != nil {
		return checkResult{}, err
	}

	accConn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return checkResult{}, err
	}
	defer DB_OPs.PutAccountsConnection(accConn)

	keys, err := DB_OPs.GetAllKeys(accConn, DB_OPs.Prefix)
	if err != nil {
		return checkResult{}, err
	}
	samples := pickRandom(keys, sampleSize, rng)
	missing, err := missingSamples(ctx, db, samples, DB_OPs.Prefix, "SELECT EXISTS(SELECT 1 FROM accounts WHERE lower(address)=lower($1))")
	if err != nil {
		return checkResult{}, err
	}

	return checkResult{
		name:        "accounts",
		immuCount:   immuCount,
		thebeCount:  thebeCount,
		countMatch:  withinThreshold(immuCount, thebeCount),
		samplesOK:   len(missing) == 0,
		missingKeys: missing,
	}, nil
}

func sqlCount(ctx context.Context, db *sql.DB, q string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func pickRandom(keys []string, n int, rng *rand.Rand) []string {
	if len(keys) <= n {
		return append([]string{}, keys...)
	}
	out := append([]string{}, keys...)
	rng.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out[:n]
}

func missingSamples(ctx context.Context, db *sql.DB, sampled []string, prefix, existsQuery string) ([]string, error) {
	missing := make([]string, 0)
	for _, k := range sampled {
		v := strings.TrimPrefix(k, prefix)
		var exists bool
		if err := db.QueryRowContext(ctx, existsQuery, v).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			missing = append(missing, v)
		}
	}
	return missing, nil
}

func withinThreshold(immuCount, thebeCount int) bool {
	if immuCount == 0 {
		return thebeCount == 0
	}
	diff := math.Abs(float64(immuCount - thebeCount))
	return (diff / float64(immuCount)) <= maxAllowedDivergPct
}
