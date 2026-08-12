// MODULE: DB_OPs/thebeprofile/apply_l1_finality.go
// PURPOSE: Apply a single "l1_finality" namespace CanonicalRecord to the `l1_finality` SQL table.
//          Invariant: append-only — L1 finality confirmations are immutable once written.
//
// CORE DATA STRUCTURES:
//   - thebegateway.L1FinalityRecord: plain DTO; JSON-unmarshalled from record.Value.
//     BlockNumbers is []uint64 — stored as BIGINT[] using pq.Array for PostgreSQL array binding.
//     Metadata is map[string]any — marshalled to JSON bytes for JSONB column (nil → SQL NULL).
//
// TO MODIFY BEHAVIOR:
//   - Add field: add to sqlInsertL1Finality const + pass in tx.Exec args
//   - Change array driver: pq.Array wraps []uint64 for lib/pq (go.mod: github.com/lib/pq v1.10.9)
//
// DO NOT:
//   - Use fmt.Sprintf for SQL construction
//   - Import gossipnode/DB_OPs (cycle risk)
//
// EXTENSION POINT: L1 block hash → add confirmation_block_hash field in future phase

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	"github.com/lib/pq"

	"gossipnode/DB_OPs/thebegateway"
)

const sqlInsertL1Finality = `
INSERT INTO l1_finality (confirmation, l1_block_number, block_numbers, metadata)
VALUES ($1,$2,$3,$4)
ON CONFLICT (confirmation) DO NOTHING`

func applyL1Finality(_ context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r thebegateway.L1FinalityRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applyL1Finality: unmarshal: %w", err)
	}
	var metaJSON []byte
	var err error
	if len(r.Metadata) > 0 {
		metaJSON, err = json.Marshal(r.Metadata)
		if err != nil {
			return fmt.Errorf("applyL1Finality: marshal metadata: %w", err)
		}
	}
	if len(metaJSON) == 0 || string(metaJSON) == "null" {
		metaJSON = []byte("{}")
	}
	_, err = tx.Exec(sqlInsertL1Finality,
		r.Confirmation,
		r.L1BlockNumber,
		pq.Array(r.BlockNumbers),
		metaJSON,
	)
	if err != nil {
		return fmt.Errorf("applyL1Finality: exec: %w", err)
	}
	return nil
}
