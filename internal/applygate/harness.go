//go:build applygate

// Package applygate is the shared apply-path test harness: a real ThebeDB store
// (Badger KV + a fresh Postgres database) installed as the process-wide handle,
// with the EVM executor and contract fold registered exactly as main.go does.
// Used by the applygate-tagged tests in messaging/BlockProcessing and Block.
//
//	APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  CGO_ENABLED=1 go test -tags applygate ./messaging/BlockProcessing/ ./Block/
package applygate

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/builder"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"
	thebeSql "github.com/JupiterMetaLabs/ThebeDB/pkg/sql"

	_ "github.com/lib/pq"
	"go.uber.org/zap"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/backend"
	"gossipnode/DB_OPs/cassata"
	"gossipnode/DB_OPs/contractDB"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/SmartContract/evmexec"
	"gossipnode/consensushash"
)

// ChainID is the apply-gate chain ID.
const ChainID = 8000800

// AcctA, AcctB are the two funded genesis accounts (deterministic addresses; balances in wei)
var (
	AcctA = common.HexToAddress("0x00000000000000000000000000000000000000A1")
	AcctB = common.HexToAddress("0x00000000000000000000000000000000000000B2")
	OneK  = new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1e18)) // 1e6 ETH
)

// BuildHandle stands up a real ThebeDB handle in `dir`, installs it as the
// process-wide handle, and registers the EVM executor + contract fold hook against
// it. Returns a cleanup. Mirrors main.go's ThebeDB init (contracts-enabled path).
func BuildHandle(t testing.TB, dir string) func() {
	t.Helper()

	reg := profile.NewRegistry()
	reg.Register(thebeprofile.NewJMDNProfile())

	kvStore, err := kv.NewStore(kv.Config{Backend: kv.BackendBadger, Path: filepath.Join(dir, "kv")})
	if err != nil {
		t.Fatalf("kv.NewStore: %v", err)
	}

	// ThebeDB's SQL engine is Postgres-only (lib/pq). Each store gets its OWN fresh
	// database so the two "validators" in a test share nothing but the blocks.
	//   export APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable"
	sqlEngine, err := thebeSql.NewSQLEngine(FreshPGDatabase(t))
	if err != nil {
		t.Fatalf("thebeSql.NewSQLEngine: %v", err)
	}

	db, err := thebedb.New(kvStore, sqlEngine, thebedb.WithProfileRegistry(reg))
	if err != nil {
		t.Fatalf("thebedb.New: %v", err)
	}

	cas := cassata.New(db, zap.NewNop())

	// EVM execution against this store's local ledger (EVM-A16) + P4 contract fold.
	// hasCode mirrors main.go: read code from the SAME repo the executor commits
	// to. (contractDB.HasCode reads an unset singleton and always returns false,
	// which silently routed every CALL onto the value-transfer path.)
	contractRepo := contractDB.NewKVStateRepository(cas.KV(), cas)
	evmexec.Register(
		ChainID,
		DB_OPs.ContractAccountSource{},
		contractRepo,
		func(addr common.Address) bool {
			code, err := contractRepo.GetCode(context.Background(), addr)
			if err != nil {
				return true
			}
			return len(code) > 0
		},
	)
	contractDB.SetSharedAccountSource(DB_OPs.ContractAccountSource{})
	kvForFold := cas.KV()
	DB_OPs.SetContractFoldHook(func(f *consensushash.StateFingerprinterV1) error {
		return contractDB.FoldAllContracts(kvForFold, f)
	})

	outbox, err := thebegateway.NewOutboxStore(filepath.Join(dir, "kv", "outbox.db"))
	if err != nil {
		t.Fatalf("NewOutboxStore: %v", err)
	}
	gw := thebegateway.NewThebeGateway(builder.New(db), db.KV, nil, outbox)
	reader := thebegateway.NewThebeReader(db.SQL.GetDB(), db.KV, nil)
	handle := backend.NewComposite(backend.New(gw, reader, nil), nil)

	DB_OPs.SetGlobalHandle(handle)

	// Allow out-of-band account creation for genesis seeding in-test.
	t.Setenv("JMDN_ALLOW_LOCAL_ACCOUNT_CREATE", "1")

	return func() {
		DB_OPs.SetGlobalHandle(nil)
		_ = db.Close()
	}
}

// FreshPGDatabase creates a uniquely-named, empty Postgres database and returns
// its DSN. Skips the test when APPLYGATE_PG_DSN is unset.
func FreshPGDatabase(t testing.TB) string {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("APPLYGATE_PG_DSN"))
	if base == "" {
		t.Skip("set APPLYGATE_PG_DSN (e.g. \"host=127.0.0.1 user=postgres sslmode=disable\") to run the apply gate")
	}
	admin, err := sql.Open("postgres", base+" dbname=postgres")
	if err != nil {
		t.Fatalf("open admin pg: %v", err)
	}
	defer admin.Close()
	name := fmt.Sprintf("applygate_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if a, err := sql.Open("postgres", base+" dbname=postgres"); err == nil {
			_, _ = a.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
			_ = a.Close()
		}
	})
	return base + " dbname=" + name
}

// SeedGenesis funds the two accounts on the currently-installed handle.
func SeedGenesis(t testing.TB) {
	t.Helper()
	for _, a := range []common.Address{AcctA, AcctB} {
		if err := DB_OPs.CreateAccount(nil, "did:jmdn:"+strings.ToLower(a.Hex()), a, nil); err != nil {
			t.Fatalf("CreateAccount(%s): %v", a.Hex(), err)
		}
		if err := DB_OPs.UpdateAccountBalance(nil, a, OneK.String(), 0); err != nil {
			t.Fatalf("UpdateAccountBalance(%s): %v", a.Hex(), err)
		}
	}
}
