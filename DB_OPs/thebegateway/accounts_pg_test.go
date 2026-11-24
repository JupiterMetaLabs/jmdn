//go:build pgtest

// Postgres-backed tests for the account listing / lookup queries and their
// indexes (accountsdb-pool-exhaustion follow-up). Each test gets a fresh
// database with the real startup DDL (thebeprofile.GetMigration).
//
//	JMDN_TEST_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  go test -tags pgtest ./DB_OPs/thebegateway/ -run TestAccounts -v

package thebegateway_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	_ "github.com/lib/pq"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
)

func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("JMDN_TEST_PG_DSN"))
	if base == "" {
		t.Skip("set JMDN_TEST_PG_DSN to run Postgres-backed tests")
	}
	admin, err := sql.Open("postgres", base+" dbname=postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("acctpg_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a, err := sql.Open("postgres", base+" dbname=postgres"); err == nil {
			_, _ = a.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
			_ = a.Close()
		}
	})
	db, err := sql.Open("postgres", base+" dbname="+name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(thebeprofile.NewJMDNProfile().GetMigration()); err != nil {
		t.Fatalf("apply startup DDL: %v", err)
	}
	return db
}

// seedAccounts inserts n accounts with checksummed addresses (the production
// write form, backend.toAccountRecord) and nonce = i+1. Returns the addresses.
func seedAccounts(t *testing.T, db *sql.DB, n int) []common.Address {
	t.Helper()
	addrs := make([]common.Address, n)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO accounts (address, did_address, balance_wei, nonce, account_type) VALUES ($1,$2,'0',$3,1)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		a := common.BytesToAddress(crypto.Keccak256([]byte(fmt.Sprintf("acct-%d", i)))[12:])
		addrs[i] = a
		if _, err := stmt.Exec(a.Hex(), "did:jmdt:test:"+a.Hex(), fmt.Sprint(i+1)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ANALYZE accounts"); err != nil {
		t.Fatal(err)
	}
	return addrs
}

// The keyset listing returns every account exactly once, in exactly the order
// the OFFSET listing (the P2.5 fingerprint's canonical order) returns them.
func TestAccounts_KeysetEqualsOffsetOrder(t *testing.T) {
	db := freshDB(t)
	r := thebegateway.NewThebeReader(db, nil, nil)
	ctx := context.Background()
	const n, page = 2503, 100 // not a multiple of page: exercises the short last page
	seedAccounts(t, db, n)

	var offsetOrder []string
	for off := 0; ; off += page {
		recs, err := r.ListAccountsPaginated(ctx, page, off)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 0 {
			break
		}
		for _, rec := range recs {
			offsetOrder = append(offsetOrder, rec.Address)
		}
	}

	var keysetOrder []string
	after := ""
	for {
		recs, err := r.ListAccountsAfter(ctx, after, page)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 0 {
			break
		}
		for _, rec := range recs {
			keysetOrder = append(keysetOrder, rec.Address)
		}
		// The cursor production uses: common.Address.Hex() of the last row.
		after = common.HexToAddress(recs[len(recs)-1].Address).Hex()
	}

	if len(keysetOrder) != n || len(offsetOrder) != n {
		t.Fatalf("row counts: keyset=%d offset=%d want %d", len(keysetOrder), len(offsetOrder), n)
	}
	for i := range offsetOrder {
		if offsetOrder[i] != keysetOrder[i] {
			t.Fatalf("order differs at %d: offset=%s keyset=%s", i, offsetOrder[i], keysetOrder[i])
		}
	}
	if !sort.SliceIsSorted(keysetOrder, func(i, j int) bool {
		return strings.ToLower(keysetOrder[i]) < strings.ToLower(keysetOrder[j])
	}) {
		t.Fatal("keyset listing is not in ascending LOWER(address) order")
	}
}

// The cursor is case-insensitive: a lowercase cursor and a checksummed cursor
// yield the same next page, and the listing never revisits the cursor row.
func TestAccounts_KeysetCursorCaseInsensitive(t *testing.T) {
	db := freshDB(t)
	r := thebegateway.NewThebeReader(db, nil, nil)
	ctx := context.Background()
	seedAccounts(t, db, 50)
	first, err := r.ListAccountsAfter(ctx, "", 10)
	if err != nil || len(first) != 10 {
		t.Fatalf("first page: %v len=%d", err, len(first))
	}
	cur := first[9].Address
	a, err := r.ListAccountsAfter(ctx, strings.ToLower(cur), 10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.ListAccountsAfter(ctx, strings.ToUpper(cur[:2])+strings.ToUpper(cur[2:]), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 10 || len(b) != 10 || a[0].Address != b[0].Address {
		t.Fatalf("case-dependent cursor: lower→%v upper→%v", a[0].Address, b[0].Address)
	}
	if strings.EqualFold(a[0].Address, cur) {
		t.Fatal("keyset revisited the cursor row")
	}
}

// Accounts inserted mid-listing never cause a repeat (the OFFSET cursor could
// repeat a row when an insert landed before the current offset).
func TestAccounts_KeysetNoRepeatUnderConcurrentInsert(t *testing.T) {
	db := freshDB(t)
	r := thebegateway.NewThebeReader(db, nil, nil)
	ctx := context.Background()
	seedAccounts(t, db, 500)
	seen := map[string]bool{}
	after := ""
	for page := 0; ; page++ {
		recs, err := r.ListAccountsAfter(ctx, after, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 0 {
			break
		}
		for _, rec := range recs {
			if seen[rec.Address] {
				t.Fatalf("account %s returned twice", rec.Address)
			}
			seen[rec.Address] = true
		}
		after = recs[len(recs)-1].Address
		if page == 2 { // insert accounts that sort BEFORE the cursor
			if _, err := db.Exec(`INSERT INTO accounts (address, did_address, balance_wei, nonce, account_type)
				VALUES ('0x0000000000000000000000000000000000000001','did:x:1','0','999999',1)`); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// GetAccountsByNonces returns exactly the requested accounts.
func TestAccounts_GetAccountsByNonces(t *testing.T) {
	db := freshDB(t)
	r := thebegateway.NewThebeReader(db, nil, nil)
	addrs := seedAccounts(t, db, 1000)
	recs, err := r.GetAccountsByNonces(context.Background(), []uint64{1, 500, 1000, 5000})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, rec := range recs {
		got[rec.Address] = true
	}
	for _, i := range []int{0, 499, 999} {
		if !got[addrs[i].Hex()] {
			t.Fatalf("nonce %d (%s) missing", i+1, addrs[i].Hex())
		}
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 accounts (nonce 5000 absent), got %d", len(recs))
	}
}

// The three hot account queries are served by an index, not a sequential scan.
// Before this change GetAccount (LOWER(address) = LOWER($1)), the listing
// (ORDER BY LOWER(address)) and GetAccountsByNonces (nonce = ANY) all scanned
// the whole table.
func TestAccounts_QueriesUseIndexes(t *testing.T) {
	db := freshDB(t)
	addrs := seedAccounts(t, db, 20000)
	cases := []struct {
		name, sql string
		args      []any
		index     string
	}{
		{"GetAccount", `SELECT address FROM accounts WHERE LOWER(address) = LOWER($1)`, []any{addrs[7].Hex()}, "idx_accounts_address_lower"},
		{"ListAccountsAfter", `SELECT address FROM accounts WHERE LOWER(address) > LOWER($1) ORDER BY LOWER(address) ASC, address ASC LIMIT $2`, []any{addrs[7].Hex(), 3000}, "idx_accounts_address_lower"},
		{"GetAccountsByNonces", `SELECT address FROM accounts WHERE nonce = ANY($1)`, []any{"{1,2,3,4000,19999}"}, "idx_accounts_nonce"},
	}
	for _, c := range cases {
		rows, err := db.Query("EXPLAIN "+c.sql, c.args...)
		if err != nil {
			t.Fatalf("%s explain: %v", c.name, err)
		}
		var plan []string
		for rows.Next() {
			var line string
			_ = rows.Scan(&line)
			plan = append(plan, line)
		}
		rows.Close()
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, c.index) || strings.Contains(joined, "Seq Scan on accounts") {
			t.Fatalf("%s is not served by %s:\n%s", c.name, c.index, joined)
		}
		t.Logf("%s → %s", c.name, plan[0])
	}
}
