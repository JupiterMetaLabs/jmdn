// MODULE: DB_OPs/thebeprofile/apply_contract_registry.go
// PURPOSE: Apply CanonicalRecord with namespace "contract_registry" to contracts SQL table.
//          Upserts deployed-contract metadata so the registry survives node restarts.
//          Contract code/storage/nonce/meta live in BadgerDB KV — not projected here.
//
// CORE DATA STRUCTURES:
//   - contractRegistryRecord: plain DTO; JSON-unmarshalled from record.Value.
//
// TO MODIFY BEHAVIOR:
//   - Add a column: extend contractRegistryRecord + sqlUpsertContractRegistry + ExecContext args
//
// DO NOT:
//   - Use fmt.Sprintf to build SQL (use const query only)
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Share the *sql.Tx across goroutines

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
)

// contractRegistryRecord is the DTO carried in the CanonicalRecord value bytes.
// Must match cassata.ContractRegistryResult field names exactly.
type contractRegistryRecord struct {
	Address      string          `json:"address"`
	Deployer     string          `json:"deployer"`
	Name         string          `json:"name"`
	ABI          string          `json:"abi"`
	BytecodeHash string          `json:"bytecode_hash"`
	DeployBlock  uint64          `json:"deploy_block"`
	DeployTime   uint64          `json:"deploy_time"`
	DeployTxHash string          `json:"deploy_tx_hash"`
	CodeSize     uint64          `json:"code_size"`
	ContractType string          `json:"contract_type"`
	State        string          `json:"state"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
}

const sqlUpsertContractRegistry = `
    INSERT INTO contracts
        (address, deployer, name, abi, bytecode_hash,
         deploy_block, deploy_time, deploy_tx_hash,
         code_size, contract_type, state, metadata, created_at, updated_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW())
    ON CONFLICT (address) DO UPDATE SET
        state        = EXCLUDED.state,
        metadata     = EXCLUDED.metadata,
        updated_at   = NOW()`

func applyContractRegistry(ctx context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r contractRegistryRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applyContractRegistry: unmarshal: %w", err)
	}
	if r.State == "" {
		r.State = "active"
	}
	if r.ContractType == "" {
		r.ContractType = "custom"
	}
	if len(r.Metadata) == 0 {
		r.Metadata = json.RawMessage("{}")
	}
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, sqlUpsertContractRegistry,
		r.Address, r.Deployer, r.Name, r.ABI, r.BytecodeHash,
		r.DeployBlock, r.DeployTime, r.DeployTxHash,
		r.CodeSize, r.ContractType, r.State, r.Metadata, createdAt,
	)
	if err != nil {
		return fmt.Errorf("applyContractRegistry: exec: %w", err)
	}
	return nil
}
