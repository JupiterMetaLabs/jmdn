package DB_OPs

import (
	"context"
	"database/sql"
	"fmt"
)

// LogTxCountReconciliation compares each account's stored tx_count_sent (a
// state-fingerprint field — see consensushash AccountLeaf, D-66) against
// COUNT(*) over its outgoing transactions in the SQL projection, and logs every
// discrepancy (capped at 100).
//
// This is the early-warning for the exact drift the D-64/D-66 bugs produced: a
// low tx_count_sent silently flips an AccountLeaf and triggers STATE DIVERGENCE
// on the very next block. With D-66 (RefreshAccountTxStats removed) the apply
// path is now the single writer, so going forward stored == projection wherever
// the projection is complete; a mismatch here means either a leftover projection
// gap (repair with ReprojectRange / JMDN_REPROJECT_RANGE) or a real accounting
// bug — either way, catch it before it halts consensus.
//
// Read-only and opt-in (it scans accounts ⋈ transactions, so it is deliberately
// NOT on the default boot path). Returns the number of mismatched accounts (0 =
// clean). Portable across the Postgres and SQLite projections (lower/COALESCE/
// LIMIT only).
func LogTxCountReconciliation(ctx context.Context, sqlDB *sql.DB) (mismatches int, err error) {
	if sqlDB == nil {
		return 0, fmt.Errorf("LogTxCountReconciliation: nil sql db")
	}
	const q = `
SELECT a.address, a.tx_count_sent, COALESCE(c.cnt, 0) AS projected
FROM accounts a
LEFT JOIN (
    SELECT lower(from_addr) AS fa, COUNT(*) AS cnt
    FROM transactions
    GROUP BY lower(from_addr)
) c ON c.fa = lower(a.address)
WHERE a.tx_count_sent <> COALESCE(c.cnt, 0)
ORDER BY a.address
LIMIT 100`
	rows, qerr := sqlDB.QueryContext(ctx, q)
	if qerr != nil {
		return 0, fmt.Errorf("LogTxCountReconciliation: query: %w", qerr)
	}
	defer rows.Close()
	for rows.Next() {
		var addr string
		var stored, projected uint64
		if scanErr := rows.Scan(&addr, &stored, &projected); scanErr != nil {
			return mismatches, fmt.Errorf("LogTxCountReconciliation: scan: %w", scanErr)
		}
		mismatches++
		fmt.Printf("reconcile(tx_count_sent): MISMATCH addr=%s stored=%d projected=%d\n", addr, stored, projected)
	}
	if rerr := rows.Err(); rerr != nil {
		return mismatches, fmt.Errorf("LogTxCountReconciliation: rows: %w", rerr)
	}
	if mismatches == 0 {
		fmt.Printf("reconcile(tx_count_sent): OK — stored counters match the transactions projection\n")
	} else {
		fmt.Printf("reconcile(tx_count_sent): %d account(s) DISAGREE with the projection (capped at 100) — "+
			"repair with JMDN_REPROJECT_RANGE before it becomes STATE DIVERGENCE\n", mismatches)
	}
	return mismatches, nil
}
