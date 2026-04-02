package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"gossipnode/internal/repository/thebe_repo"
	log "gossipnode/logging"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/ethereum/go-ethereum/common"
)

func TestIntegration_BackfillToRealThebeDB(t *testing.T) {
	// 0. Disable OpenTelemetry for this test to avoid Configuration panics
	log.Once.Do(func() {
		// Mock out the Once block entirely to skip otelsetup
	})
	log.NewAsyncLogger() // This will now no-op securely behind sync.Once

	// 1. Setup real ThebeDB instance connected to pgAdmin
	tempDir, err := os.MkdirTemp("", "thebedb_integration_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir) // clean up

	sqlPath := os.Getenv("THEBEDB_TEST_URL")
	if sqlPath == "" {
		sqlPath = "postgres://postgres:postgres@localhost:5432/thebedbtest?sslmode=disable"
	}
	cfg := thebedb.Config{
		KVPath:  tempDir,
		SQLPath: sqlPath,
	}
	db, err := thebedb.New(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to init ThebeDB: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.Start(ctx); err != nil {
		t.Fatalf("Failed to start ThebeDB Projector daemon: %v", err)
	}
	defer db.Close()

	// 2. Initialize Repositories
	thebeRepoInstance := thebe_repo.NewThebeRepository(db)
	immuMock := NewMockImmuRepoMassive(100, 10) // 100 blocks, 10 transactions each

	// 2.5 Clean slate: Drop tables if they exist in the test DB
	db.SQL.GetDB().ExecContext(ctx, "DROP TABLE IF EXISTS blocks CASCADE; DROP TABLE IF EXISTS transactions CASCADE; DROP TABLE IF EXISTS accounts CASCADE; DROP TABLE IF EXISTS zk_proofs CASCADE;")

	// 3. Ensure the schema exists in thebedbtest database
	tracker := &StateTracker{db: db}
	err = tracker.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("Failed to create tables in Postgres: %v", err)
	}
	t.Log("Successfully created/verified 'blocks', 'transactions', 'accounts', and 'zk_proofs' tables in Postgres.")

	// 4. Setup the Backfill Worker
	workerCfg := Config{
		Enabled:             true,
		MaxBlocksPerBatch:   10,
		MaxAccountsPerBatch: 10,
		ThrottleDuration:    1 * time.Millisecond,
		MigrateBlocks:       true,
		MigrateAccounts:     false,
	}

	worker := NewBackfillWorker(
		immuMock,
		thebeRepoInstance,
		db,
		workerCfg,
	)

	// 5. Run the Backfill synchronously
	worker.Run(ctx)
	t.Log("BackfillWorker finished pushing data to the Builder")

	// 6. Give the ThebeDB async projector enough time to flush the massive SQL batch
	time.Sleep(15 * time.Second)

	// 7. Verify the data actually hit Postgres via the Verifier
	verifier := NewVerifier(immuMock, db)
	// We mocked 100 blocks in the ImmuDB instance. Verify them all.
	report, err := verifier.VerifyBlocks(ctx, 1, 100)
	if err != nil {
		t.Fatalf("Verifier failed to run against SQL table: %v", err)
	}

	if len(report.Mismatches) > 0 {
		for _, m := range report.Mismatches {
			t.Errorf("Mismatch: %s", m)
		}
		t.Fatalf("SQL mismatch detected by verifier! Total errors: %d", len(report.Mismatches))
	}
	if report.TotalChecked != 100 {
		t.Fatalf("Verifier did not find all 100 blocks in SQL. Found: %d", report.TotalChecked)
	}

	// 8. Double check SQL directly
	var hash string
	err = db.SQL.GetDB().QueryRow("SELECT block_hash FROM blocks WHERE block_number = 100").Scan(&hash)
	if err != nil {
		t.Fatalf("Failed to query block 100 from Postgres directly: %v", err)
	}

	expectedHash := common.HexToHash(fmt.Sprintf("0x%x", 100)).Hex()
	if hash != expectedHash {
		t.Fatalf("Direct SQL query hash mismatch. Expected %s, got %s", expectedHash, hash)
	}

	var txCount int
	err = db.SQL.GetDB().QueryRow("SELECT count(*) FROM transactions").Scan(&txCount)
	if err != nil {
		t.Fatalf("Failed to query transaction count: %v", err)
	}

	var proofCount int
	err = db.SQL.GetDB().QueryRow("SELECT count(*) FROM zk_proofs").Scan(&proofCount)
	if err != nil {
		t.Fatalf("Failed to query proof count: %v", err)
	}

	t.Logf("======================================================================")
	t.Logf("✅ MIGRATION SUCCESS: %d Blocks correctly verified by Verifier tool.", report.TotalChecked)
	t.Logf("✅ SQL RECORD COUNT: %d Blocks found in Postgres `blocks` table.", report.TotalChecked)
	t.Logf("✅ SQL RECORD COUNT: %d Transactions found in Postgres `transactions` table.", txCount)
	t.Logf("✅ SQL RECORD COUNT: %d ZK Proofs found in Postgres `zk_proofs` table.", proofCount)
	t.Logf("======================================================================")
}
