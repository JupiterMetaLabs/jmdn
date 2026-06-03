// MODULE: DB_OPs/thebeprofile/apply_block.go
// PURPOSE: Apply a single "block" namespace CanonicalRecord to the `blocks` SQL table.
//          Invariant: append-only — blocks are never updated or deleted after write.
//
// CORE DATA STRUCTURES:
//   - thebegateway.BlockRecord: plain DTO; JSON-unmarshalled from record.Value.
//     GasLimit/GasUsed are uint64 — stored as NUMERIC(78,0); passed as fmt.Sprintf decimal strings.
//     ExtraData is map[string]any — marshalled to JSON bytes for JSONB column.
//
// TO MODIFY BEHAVIOR:
//   - Add block field: add to sqlInsertBlock const + pass in tx.Exec args
//   - Change idempotency: ON CONFLICT DO NOTHING is the invariant for append-only tables
//
// DO NOT:
//   - Use fmt.Sprintf for SQL construction (only for uint64→string numeric conversion)
//   - Update or delete from blocks (append-only enforced by DB RULEs)
//
// EXTENSION POINT: new block metadata field → extend sqlInsertBlock + BlockRecord in thebegateway

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"

	"gossipnode/DB_OPs/thebegateway"
)

const sqlInsertBlock = `
INSERT INTO blocks (block_number, block_hash, parent_hash, timestamp, txs_root, state_root, logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (block_number) DO NOTHING`

func applyBlock(_ context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r thebegateway.BlockRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applyBlock: unmarshal: %w", err)
	}
	extraJSON, err := json.Marshal(r.ExtraData)
	if err != nil {
		return fmt.Errorf("applyBlock: marshal extra_data: %w", err)
	}
	if len(extraJSON) == 0 || string(extraJSON) == "null" {
		extraJSON = []byte(`{}`)
	}
	_, err = tx.Exec(sqlInsertBlock,
		r.BlockNumber, r.BlockHash, r.ParentHash, r.Timestamp,
		r.TxsRoot, r.StateRoot, r.LogsBloom,
		r.CoinbaseAddr, r.ZKVMAddr,
		fmt.Sprintf("%d", r.GasLimit), fmt.Sprintf("%d", r.GasUsed),
		r.Status, extraJSON,
	)
	if err != nil {
		return fmt.Errorf("applyBlock: exec: %w", err)
	}
	return nil
}
