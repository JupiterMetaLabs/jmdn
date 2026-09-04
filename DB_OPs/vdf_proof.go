// MODULE: DB_OPs/vdf_proof
// PURPOSE: Durable per-epoch VDF proof, so a node can answer a peer's
//          "give me the proof for epoch E" request without scanning the chain,
//          and so a proof survives restart.
//
// WHY A DEDICATED STORE RATHER THAN READING IT BACK OFF THE BLOCK. The proof
// is already persisted inside the epoch-boundary block's extra_data
// ("vdf_proof", see backend/block.go), but that copy is reachable only by
// BLOCK HEIGHT. Answering "which block was epoch E's boundary?" needs a
// slot->height index, and none exists — GetAllKeys is a removed-ImmuDB stub
// that always errors, so a prefix scan is not available either. The responder
// would have to walk the chain backwards on every request, which is exactly
// the unbounded work a request handler must never do.
//
// Keying the proof by epoch turns that walk into one direct lookup.
//
// SAFETY DIRECTION: only a proof this node has already VERIFIED is written —
// either one it sealed itself, or one that passed Pipeline.Accept. An
// unverified proof never reaches this file.
//
// STORAGE: ThebeDB sync-state KV via the pooled handle, same population and
// same shape as the beacon entropy records (see beacon_entropy.go). Key format
// is frozen; the value is JSON-encoded to match every other record in this KV.

package DB_OPs

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gossipnode/DB_OPs/store"
	"gossipnode/config"
)

// vdfProofPrefix namespaces the per-epoch records. Frozen.
const vdfProofPrefix = "vdf_proof:"

// vdfProofNewestKey holds the highest epoch ever written, so a restore or an
// audit can find records without a prefix scan. Same pattern, and same
// reason, as beaconEntropyNewestKey.
const vdfProofNewestKey = "vdf_proof_newest"

// MaxVDFProofBytes bounds a single stored/transmitted proof.
//
// A vdf.Proof is JSON-encoded {Y, Pi, T, Group}. Y and Pi are each at most one
// modulus wide — 2048 bits, 617 decimal digits as encoded by big.Int's JSON
// form — so ~1.3KB of digits plus a small envelope. 8KiB is a generous
// multiple of that: large enough that a legitimate 2048-bit proof can never
// hit it, small enough that a malicious peer cannot use the proof RPC as a
// bulk-transfer channel.
const MaxVDFProofBytes = 8 << 10

// VDFProofKey builds the durable key for an epoch's proof.
func VDFProofKey(epoch uint64) string {
	return fmt.Sprintf("%s%d", vdfProofPrefix, epoch)
}

// GetVDFProof returns the encoded proof recorded for epoch.
//
// The bytes are exactly what vdf.Proof.MarshalBinary produced — the SAME
// encoding the block carries — so a caller can hand them to the existing
// verification path unchanged. found=false (nil error) when nothing is stored.
func GetVDFProof(conn *config.PooledConnection, epoch uint64) ([]byte, bool, error) {
	h, err := getHandle(conn)
	if err != nil {
		return nil, false, fmt.Errorf("vdf proof read: %w", err)
	}
	raw, err := h.GetSyncKV(VDFProofKey(epoch))
	if err != nil {
		if isNotFoundError(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("vdf proof read epoch %d: %w", epoch, err)
	}
	if raw == nil {
		return nil, false, nil
	}
	var proofHex string
	if err := json.Unmarshal(raw, &proofHex); err != nil {
		return nil, false, fmt.Errorf("vdf proof parse epoch %d: %w", epoch, err)
	}
	out, err := hex.DecodeString(proofHex)
	if err != nil {
		return nil, false, fmt.Errorf("vdf proof hex epoch %d: %w", epoch, err)
	}
	return out, true, nil
}

// RecordVDFProof durably stores a VERIFIED proof for an epoch.
//
// IDEMPOTENT for an identical encoding. A CONFLICTING encoding is REFUSED
// with an error rather than overwritten — and that refusal is more
// interesting than it looks. The VDF is deterministic: the same mix, group and
// T yield the same Y and Pi. Two different byte encodings for one epoch
// therefore cannot both be valid proofs over the same mix, so a conflict here
// means either a re-encoding difference or a genuine mix divergence. Refusing
// keeps the first (already-verified) record and surfaces the disagreement.
//
// Fails CLOSED on a read error, for the reason equivocation.go documents: a
// read error that fell through would overwrite the durable record.
func RecordVDFProof(conn *config.PooledConnection, epoch uint64, encodedProof []byte) error {
	if len(encodedProof) == 0 {
		return fmt.Errorf("vdf proof write epoch %d: refusing to store an empty proof", epoch)
	}
	if len(encodedProof) > MaxVDFProofBytes {
		return fmt.Errorf("vdf proof write epoch %d: %d bytes exceeds the %d-byte maximum",
			epoch, len(encodedProof), MaxVDFProofBytes)
	}
	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("vdf proof write: %w", err)
	}
	key := VDFProofKey(epoch)

	raw, gerr := h.GetSyncKV(key)
	if gerr != nil && !isNotFoundError(gerr) {
		return fmt.Errorf("vdf proof pre-read (fail closed) epoch %d: %w", epoch, gerr)
	}
	if raw != nil {
		var existingHex string
		if uerr := json.Unmarshal(raw, &existingHex); uerr != nil {
			return fmt.Errorf("vdf proof parse existing epoch %d: %w", epoch, uerr)
		}
		if strings.EqualFold(existingHex, hex.EncodeToString(encodedProof)) {
			return nil // identical — idempotent
		}
		return fmt.Errorf("vdf proof epoch %d already has a DIFFERENT proof stored; the VDF is "+
			"deterministic, so two distinct valid proofs for one epoch imply a mix divergence — "+
			"keeping the first and refusing the second", epoch)
	}

	val, err := json.Marshal(hex.EncodeToString(encodedProof))
	if err != nil {
		return fmt.Errorf("vdf proof encode epoch %d: %w", epoch, err)
	}
	if err := h.PutSyncKV(key, val); err != nil {
		return fmt.Errorf("vdf proof write epoch %d: %w", epoch, err)
	}

	// Advance the newest pointer monotonically. Non-fatal: the proof itself is
	// already durable and the pointer self-heals on the next higher write.
	if newest, found, rerr := readVDFProofNewest(h); rerr == nil && (!found || epoch > newest) {
		if nv, merr := json.Marshal(epoch); merr == nil {
			_ = h.PutSyncKV(vdfProofNewestKey, nv)
		}
	}
	return nil
}

func readVDFProofNewest(h store.ThebeHandle) (uint64, bool, error) {
	raw, err := h.GetSyncKV(vdfProofNewestKey)
	if err != nil {
		if isNotFoundError(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if raw == nil {
		return 0, false, nil
	}
	var newest uint64
	if err := json.Unmarshal(raw, &newest); err != nil {
		return 0, false, fmt.Errorf("vdf proof newest parse: %w", err)
	}
	return newest, true, nil
}

// NewestVDFProofEpoch reports the highest epoch with a stored proof.
func NewestVDFProofEpoch(conn *config.PooledConnection) (uint64, bool, error) {
	h, err := getHandle(conn)
	if err != nil {
		return 0, false, fmt.Errorf("vdf proof newest: %w", err)
	}
	return readVDFProofNewest(h)
}
