// MODULE: DB_OPs/thebeprofile/apply_contract_receipt.go
// PURPOSE: Apply CanonicalRecord with namespace "contract_receipt" to contract_receipts SQL table.
//          Contract receipts are written once (INSERT ... ON CONFLICT DO NOTHING) — immutable after insertion.
//          Code/nonce/storage/meta live in BadgerDB KV — not projected here.
//
// CORE DATA STRUCTURES:
//   - thebegateway.ContractReceiptRecord: plain DTO; JSON-unmarshalled from record.Value.
//     Fields: TxHash (PK), BlockNumber, TxIndex, Status, GasUsed, ContractAddress, Logs, RevertReason, CreatedAt.
//
// TO MODIFY BEHAVIOR:
//   - Add field to insert: add to sqlInsertContractReceipt const + pass in tx.ExecContext args
//   - Change conflict resolution: edit ON CONFLICT clause in sqlInsertContractReceipt
//
// DO NOT:
//   - Use fmt.Sprintf to build SQL (use const query only)
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Share the *sql.Tx across goroutines
//
// EXTENSION POINT: additional receipt fields → extend sqlInsertContractReceipt + ContractReceiptRecord

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"

	"gossipnode/DB_OPs/thebegateway"
)

const sqlInsertContractReceipt = `
    INSERT INTO contract_receipts
        (tx_hash, block_number, tx_index, status, gas_used, contract_address, logs, revert_reason, created_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
    ON CONFLICT (tx_hash) DO NOTHING`

func applyContractReceipt(ctx context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r thebegateway.ContractReceiptRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applyContractReceipt: unmarshal: %w", err)
	}
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	contractAddr := sql.NullString{String: "", Valid: r.ContractAddress != nil}
	if r.ContractAddress != nil {
		contractAddr.String = *r.ContractAddress
	}
	_, err := tx.ExecContext(ctx, sqlInsertContractReceipt,
		r.TxHash, r.BlockNumber, r.TxIndex, r.Status,
		r.GasUsed, contractAddr, r.Logs, r.RevertReason, createdAt,
	)
	if err != nil {
		return fmt.Errorf("applyContractReceipt: exec: %w", err)
	}
	return nil
}
