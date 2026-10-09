package thebegateway_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"gossipnode/DB_OPs/thebegateway"
)

// The address-paginated queries replaced the SQLite tx-address index
// (DB_OPs/txindex). They are exercised here against an in-memory SQLite with
// the projection's transactions columns — the SQL uses only $N placeholders,
// COUNT, ORDER BY and LIMIT/OFFSET, which SQLite and PostgreSQL share.
func newTxTable(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared&_fk=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE transactions (
		tx_hash TEXT PRIMARY KEY, block_number INTEGER NOT NULL, tx_index INTEGER NOT NULL,
		from_addr TEXT NOT NULL, to_addr TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTxByAddress_CountAndPage(t *testing.T) {
	db := newTxTable(t)
	const a, b, c = "0xAaaa000000000000000000000000000000000001", "0xBbbb000000000000000000000000000000000002", "0xCccc000000000000000000000000000000000003"
	// a: sender in blocks 1,3,5 ; receiver in block 4 ; c→b in block 2 (not a's)
	rows := []struct {
		hash string
		blk  int
		idx  int
		from string
		to   any
	}{
		{"0xh1", 1, 0, a, b},
		{"0xh2", 2, 0, c, b},
		{"0xh3", 3, 0, a, nil}, // contract creation, to_addr NULL
		{"0xh4", 4, 0, b, a},
		{"0xh5a", 5, 0, a, c},
		{"0xh5b", 5, 1, a, c},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO transactions VALUES ($1,$2,$3,$4,$5)`, r.hash, r.blk, r.idx, r.from, r.to); err != nil {
			t.Fatal(err)
		}
	}
	rd := thebegateway.NewThebeReader(db, nil, nil)
	ctx := context.Background()

	n, err := rd.CountTransactionsByAddress(ctx, a)
	if err != nil || n != 5 {
		t.Fatalf("count(a) = %d, %v; want 5", n, err)
	}
	if n, _ := rd.CountTransactionsByAddress(ctx, c); n != 3 {
		t.Fatalf("count(c) = %d, want 3", n)
	}
	if n, _ := rd.CountTransactionsByAddress(ctx, "0xdead"); n != 0 {
		t.Fatalf("count(unknown) = %d, want 0", n)
	}

	// Newest first: block 5 idx 1, block 5 idx 0, block 4, block 3, block 1.
	page1, err := rd.GetTransactionRefsByAddress(ctx, a, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(page1); got != "[{0xh5b 5} {0xh5a 5}]" {
		t.Fatalf("page1 = %s", got)
	}
	page2, _ := rd.GetTransactionRefsByAddress(ctx, a, 2, 2)
	if got := fmt.Sprint(page2); got != "[{0xh4 4} {0xh3 3}]" {
		t.Fatalf("page2 = %s", got)
	}
	page3, _ := rd.GetTransactionRefsByAddress(ctx, a, 2, 4)
	if got := fmt.Sprint(page3); got != "[{0xh1 1}]" {
		t.Fatalf("page3 = %s", got)
	}
	empty, _ := rd.GetTransactionRefsByAddress(ctx, a, 2, 10)
	if len(empty) != 0 {
		t.Fatalf("past the end must be empty, got %v", empty)
	}
	// Exact-match semantics: the projection stores checksummed addresses and
	// callers must pass the same form (DB_OPs.*ByAddress does .Hex()).
	if n, _ := rd.CountTransactionsByAddress(ctx, "0xaaaa000000000000000000000000000000000001"); n != 0 {
		t.Fatalf("lowercase form must not match the checksummed row (got %d) — callers normalise via common.Address.Hex()", n)
	}
}
