// MODULE: DB_OPs/thebeprofile/apply_account.go
// PURPOSE: Apply a single "account" namespace CanonicalRecord to the `accounts` SQL table.
//          Invariant: upsert — accounts table is mutable (balance/nonce change per tx).
//
// CORE DATA STRUCTURES:
//   - thebegateway.AccountRecord: plain DTO; JSON-unmarshalled from record.Value.
//     Fields: Address (PK), DIDAddress, BalanceWei, Nonce, AccountType, Metadata, CreatedAt, UpdatedAt.
//
// TO MODIFY BEHAVIOR:
//   - Add field to upsert: add to sqlUpsertAccount const + pass in tx.Exec args
//   - Change conflict resolution: edit ON CONFLICT clause in sqlUpsertAccount
//
// DO NOT:
//   - Use fmt.Sprintf to build SQL (use const query only)
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Share the *sql.Tx across goroutines
//
// EXTENSION POINT: additional metadata fields → extend sqlUpsertAccount + AccountRecord

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"

	"gossipnode/DB_OPs/thebegateway"
)

const sqlUpsertAccount = `
INSERT INTO accounts (address, did_address, balance_wei, nonce, account_type, metadata, created_at, updated_at)
-- created_at intentionally excluded from DO UPDATE (preserved from first insert)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (address) DO UPDATE SET
    balance_wei  = EXCLUDED.balance_wei,
    nonce        = EXCLUDED.nonce,
    account_type = EXCLUDED.account_type,
    metadata     = EXCLUDED.metadata,
    -- did_address intentionally excluded: immutable after account creation
    updated_at   = EXCLUDED.updated_at
WHERE accounts.updated_at < EXCLUDED.updated_at`

func applyAccount(_ context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r thebegateway.AccountRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applyAccount: unmarshal: %w", err)
	}
	metaJSON, err := json.Marshal(r.Metadata)
	if err != nil {
		return fmt.Errorf("applyAccount: marshal metadata: %w", err)
	}
	_, err = tx.Exec(sqlUpsertAccount,
		r.Address, r.DIDAddress, r.BalanceWei, r.Nonce,
		r.AccountType, metaJSON, r.CreatedAt, r.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("applyAccount: exec: %w", err)
	}
	return nil
}
