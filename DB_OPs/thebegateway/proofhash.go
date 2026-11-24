// MODULE: DB_OPs/thebegateway/proofhash.go
// PURPOSE: The canonical proof_hash for a stored ZK proof.
//
// WHY THIS EXISTS (Auspex proof-drop, storage layer):
// zk_proofs declares proof_hash CHAR(66) NOT NULL UNIQUE. Auspex blocks arrived with
// a real StarkProof but an EMPTY proof_hash (proof_hash was Espresso-derived; the
// orchestrator stopped setting it when Espresso was retired). So the first such block
// took proof_hash='' and every later one collided on the UNIQUE constraint — and the
// projection insert (ON CONFLICT DO NOTHING, no target) swallowed that violation, so
// each later proof silently had no SQL row and read back proofless on every node.
//
// CanonicalProofHash gives every proof a real, unique hash: the one the orchestrator
// now sends (JMDT-Sequencer-Orchestrator 01e6341, asserted by its prover test as
// crypto.Keccak256Hash(envelope).Hex()), where the envelope IS the block's StarkProof.
// Old and new blocks therefore converge on the same value.
//
// DO NOT change the format: it must stay byte-identical to the orchestrator
// ("0x" + lowercase hex of keccak256(StarkProof), 66 chars — fits CHAR(66)).

package thebegateway

import (
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

// CanonicalProofHash returns the proof_hash to store/serve for a ZK proof.
//   - A non-blank proofHash is kept (trimmed — CHAR(66) blank-pads, so a stored empty
//     string can read back as 66 spaces, which must not count as a real hash).
//   - Otherwise, if stark is non-empty, it derives "0x"+hex(keccak256(stark)).
//   - Otherwise it returns "" (no proof data — nothing to store).
//
// Deterministic, so it is safe on the write path, in the SQL projection (a derived,
// rebuildable view), and on read.
func CanonicalProofHash(proofHash string, stark []byte) string {
	if h := strings.TrimSpace(proofHash); h != "" {
		return h
	}
	if len(stark) == 0 {
		return ""
	}
	return crypto.Keccak256Hash(stark).Hex()
}
