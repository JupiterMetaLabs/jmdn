package txindex

import (
	"path/filepath"
	"strings"
	"testing"
)

// The stats total-transaction count is SELECT COUNT(DISTINCT tx_hash) FROM
// address_txns. Without a tx_hash index that's a full-table scan (multi-second
// on a large index). This asserts the planner uses idx_address_txns_txhash so
// the query stays an index-only distinct scan — a guard against the index being
// dropped from createSchema.
func TestCountDistinctTxHashUsesIndex(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "plan.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.writeDB.Exec(
		`INSERT OR IGNORE INTO address_txns(address, block_number, tx_hash) VALUES
			('a',1,'h1'),('b',1,'h1'),('c',2,'h2'),('d',3,'h3')`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	rows, err := db.readDB.Query(`EXPLAIN QUERY PLAN SELECT COUNT(DISTINCT tx_hash) FROM address_txns`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString(" | ")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	got := plan.String()
	t.Logf("query plan: %s", got)
	if !strings.Contains(got, "idx_address_txns_txhash") {
		t.Fatalf("COUNT(DISTINCT tx_hash) did not use idx_address_txns_txhash (full scan?); plan=%q", got)
	}

	// Sanity: the count is correct (3 distinct hashes).
	var n int
	if err := db.readDB.QueryRow(`SELECT COUNT(DISTINCT tx_hash) FROM address_txns`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Fatalf("distinct tx count: want 3, got %d", n)
	}
}
