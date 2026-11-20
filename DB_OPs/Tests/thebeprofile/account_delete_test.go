//go:build applygate

package thebeprofile_test

// A Deleted account record removes the row; an ordinary record still upserts.
//
//	APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  go test -tags applygate ./DB_OPs/Tests/thebeprofile/ -run TestAccountDelete

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/internal/applygate"
)

func TestAccountDelete(t *testing.T) {
	db, err := sql.Open("postgres", applygate.FreshPGDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	p := thebeprofile.NewJMDNProfile()
	if _, err := db.ExecContext(ctx, p.GetMigration()); err != nil {
		t.Fatalf("migration: %v", err)
	}
	apply := func(r *thebegateway.AccountRecord) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Apply(ctx, 0, record(t, "account", r), tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %+v: %v", r, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	count := func(addr string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE address = $1`, addr).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	now := time.Now().UTC()
	z := "0x00000000000000000000000000000000000000F9"
	keep := "0x00000000000000000000000000000000000000A1"
	for _, a := range []string{z, keep} {
		apply(&thebegateway.AccountRecord{Address: a, DIDAddress: "did:jmdt:metamask:" + a, BalanceWei: "10", Nonce: "7", Metadata: map[string]any{}, CreatedAt: now, UpdatedAt: now})
	}
	apply(&thebegateway.AccountRecord{Address: z, Deleted: true})
	if count(z) != 0 {
		t.Fatal("a Deleted record left the account row in place")
	}
	if count(keep) != 1 {
		t.Fatal("deleting one account removed another")
	}
	apply(&thebegateway.AccountRecord{Address: z, Deleted: true}) // absent: no error
}
