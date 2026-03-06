package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
)

// Verifier provides methods to mathematically prove the ImmuDB and ThebeDB states match.
type Verifier struct {
	source CoordinatorRepository
	db     *thebedb.ThebeDB
}

// NewVerifier creates a new parity verifier.
func NewVerifier(source CoordinatorRepository, db *thebedb.ThebeDB) *Verifier {
	return &Verifier{
		source: source,
		db:     db,
	}
}

// Report holds the verification results.
type Report struct {
	TotalChecked uint64
	Mismatches   []string
	Duration     time.Duration
}

// VerifyBlocks compares all blocks between ImmuDB and ThebeDB's SQL layer.
func (v *Verifier) VerifyBlocks(ctx context.Context, startBlock, endBlock uint64) (Report, error) {
	start := time.Now()
	report := Report{}

	if v.db == nil || v.db.SQL == nil || v.db.SQL.GetDB() == nil {
		return report, fmt.Errorf("thebedb sql engine not initialized")
	}
	db := v.db.SQL.GetDB()

	for current := startBlock; current <= endBlock; current++ {
		immuBlock, err := v.source.GetZKBlockByNumber(ctx, current)
		if err != nil {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("Block %d missing in ImmuDB: %v", current, err))
			continue
		}

		if immuBlock == nil {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("Block %d returned nil from ImmuDB", current))
			continue
		}

		var sqlHash, sqlStateRoot, sqlTxnsRoot string
		err = db.QueryRowContext(ctx, "SELECT block_hash, state_root, txns_root FROM blocks WHERE block_number = $1", current).
			Scan(&sqlHash, &sqlStateRoot, &sqlTxnsRoot)

		if err != nil {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("Block %d missing in ThebeDB SQL: %v", current, err))
			continue
		}

		if immuBlock.BlockHash.Hex() != strings.TrimSpace(sqlHash) {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("Block %d Hash Mismatch: Immu=%s SQL=%s", current, immuBlock.BlockHash.Hex(), sqlHash))
		}

		if immuBlock.StateRoot.Hex() != strings.TrimSpace(sqlStateRoot) {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("Block %d StateRoot Mismatch: Immu=%s SQL=%s", current, immuBlock.StateRoot.Hex(), sqlStateRoot))
		}

		if immuBlock.TxnsRoot != strings.TrimSpace(sqlTxnsRoot) {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("Block %d TxnsRoot Mismatch: Immu=%s SQL=%s", current, immuBlock.TxnsRoot, sqlTxnsRoot))
		}

		// Verify transaction count for this block
		var txCount int
		err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions WHERE block_number = $1", current).Scan(&txCount)
		if err == nil && len(immuBlock.Transactions) != txCount {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("Block %d TxCount Mismatch: Immu=%d SQL=%d", current, len(immuBlock.Transactions), txCount))
		}

		report.TotalChecked++
	}

	report.Duration = time.Since(start)
	return report, nil
}
