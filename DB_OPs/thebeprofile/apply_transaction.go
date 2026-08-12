// MODULE: DB_OPs/thebeprofile/apply_transaction.go
// PURPOSE: Apply a single "tx" namespace CanonicalRecord to the `transactions` SQL table.
//          Invariant: append-only — transactions are never updated or deleted after write.
//
// CORE DATA STRUCTURES:
//   - thebegateway.TransactionRecord: plain DTO; JSON-unmarshalled from record.Value.
//     ToAddr is *string — nil maps to SQL NULL (contract creation).
//     AccessList is map[string]any — marshalled to JSON bytes for JSONB column.
//     SigV is uint64 stored as BIGINT (covers EIP-155 for any chain ID).
//
// TO MODIFY BEHAVIOR:
//   - Add tx field: add to sqlInsertTransaction const + pass in tx.Exec args
//   - Change idempotency: ON CONFLICT DO NOTHING is the invariant for append-only tables
//
// DO NOT:
//   - Use fmt.Sprintf for SQL construction
//   - Modify transactions after write (append-only enforced by DB RULEs)
//
// EXTENSION POINT: EIP-4844 blob fields → extend sqlInsertTransaction + TransactionRecord

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"

	"gossipnode/DB_OPs/thebegateway"
)

const sqlInsertTransaction = `
INSERT INTO transactions (tx_hash, block_number, tx_index, from_addr, to_addr, value_wei, nonce, type, gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei, gas_fee_wei, data, access_list, sig_v, sig_r, sig_s)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (tx_hash) DO NOTHING`

func applyTransaction(_ context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r thebegateway.TransactionRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applyTransaction: unmarshal: %w", err)
	}
	alJSON, err := json.Marshal(r.AccessList)
	if err != nil {
		return fmt.Errorf("applyTransaction: marshal access_list: %w", err)
	}
	if len(alJSON) == 0 || string(alJSON) == "null" {
		alJSON = []byte(`[]`)
	}
	_, err = tx.Exec(sqlInsertTransaction,
		r.TxHash, r.BlockNumber, r.TxIndex, r.FromAddr, r.ToAddr,
		r.ValueWei, r.Nonce, r.Type,
		r.GasLimit, r.GasPriceWei, r.MaxFeeWei, r.MaxPriorityFeeWei,
		nullIfEmptyNumeric(r.GasFeeWei),
		r.Data, alJSON, r.SigV, r.SigR, r.SigS,
	)
	if err != nil {
		return fmt.Errorf("applyTransaction: exec: %w", err)
	}
	return nil
}

// nullIfEmptyNumeric maps "" to "0" so NUMERIC NOT NULL DEFAULT 0 columns
// accept records written before the field existed.
func nullIfEmptyNumeric(v string) string {
	if v == "" {
		return "0"
	}
	return v
}
