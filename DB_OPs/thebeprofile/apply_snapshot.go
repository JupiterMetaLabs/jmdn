// MODULE: DB_OPs/thebeprofile/apply_snapshot.go
// PURPOSE: Apply a single "snapshot" namespace CanonicalRecord to the `snapshots` SQL table.
//          Invariant: append-only — one snapshot per block; ON CONFLICT DO NOTHING for idempotency.
//
// CORE DATA STRUCTURES:
//   - thebegateway.SnapshotRecord: plain DTO; JSON-unmarshalled from record.Value.
//     Fields: BlockNumber (PK/FK→blocks), BlockHash (UNIQUE), CreatedAt.
//
// TO MODIFY BEHAVIOR:
//   - Add snapshot field: add to sqlInsertSnapshot const + pass in tx.Exec args
//
// DO NOT:
//   - Use fmt.Sprintf for SQL construction
//   - Insert the same block_number twice (FK to blocks enforces referential integrity)
//
// EXTENSION POINT: snapshot chain tracking → add prev_snapshot_id field in future phase

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"

	"gossipnode/DB_OPs/thebegateway"
)

const sqlInsertSnapshot = `
INSERT INTO snapshots (block_number, block_hash, created_at)
VALUES ($1,$2,$3)
ON CONFLICT (block_number) DO NOTHING`

func applySnapshot(_ context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r thebegateway.SnapshotRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applySnapshot: unmarshal: %w", err)
	}
	_, err := tx.Exec(sqlInsertSnapshot, r.BlockNumber, r.BlockHash, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("applySnapshot: exec: %w", err)
	}
	return nil
}
