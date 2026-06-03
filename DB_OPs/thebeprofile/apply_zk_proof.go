// MODULE: DB_OPs/thebeprofile/apply_zk_proof.go
// PURPOSE: Apply a single "zk" namespace CanonicalRecord to the `zk_proofs` SQL table.
//          Invariant: append-only — ZK proofs are cryptographic commitments; never updated.
//
// CORE DATA STRUCTURES:
//   - thebegateway.ZKProofRecord: plain DTO; JSON-unmarshalled from record.Value.
//     StarkProof and Commitment are []byte — stored as BYTEA in PostgreSQL.
//     BlockNumber is the PRIMARY KEY (one proof per block).
//
// TO MODIFY BEHAVIOR:
//   - Add proof field: add to sqlInsertZKProof const + pass in tx.Exec args
//   - Change conflict: ON CONFLICT DO NOTHING preserves first-write-wins for immutable proofs
//
// DO NOT:
//   - Use fmt.Sprintf for SQL construction
//   - Allow duplicate block_number insertions (PK enforces 1:1 block→proof)
//
// EXTENSION POINT: Groth16/Plonk fields → extend sqlInsertZKProof + ZKProofRecord

package thebeprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"

	"gossipnode/DB_OPs/thebegateway"
)

const sqlInsertZKProof = `
INSERT INTO zk_proofs (block_number, proof_hash, stark_proof, commitment)
VALUES ($1,$2,$3,$4)
ON CONFLICT (block_number) DO NOTHING`

func applyZKProof(_ context.Context, _ uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	var r thebegateway.ZKProofRecord
	if err := json.Unmarshal(record.Value, &r); err != nil {
		return fmt.Errorf("applyZKProof: unmarshal: %w", err)
	}
	_, err := tx.Exec(sqlInsertZKProof, r.BlockNumber, r.ProofHash, r.StarkProof, r.Commitment)
	if err != nil {
		return fmt.Errorf("applyZKProof: exec: %w", err)
	}
	return nil
}
