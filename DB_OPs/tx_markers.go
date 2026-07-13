// MODULE: DB_OPs/tx_markers
// PURPOSE: Value-aware processed-marker layer + the per-tx apply used by
//          the live executor. ThebeDB retarget of the F4 module.
//
// MARKER SEMANTICS (value-aware, NOT existence-based — unchanged from F4):
//   value > 0  → applied (Unix timestamp of application)
//   value = -1 → REVOKED: the tx's balance effects were rolled back after the
//                marker committed. Rollback overwrites the prefix markers with
//                -1 and every consumer treats -1 as not-processed.
//   unparseable → applied (the lower-risk skip direction; see markerValueApplied).
//
// STORAGE (ThebeDB): markers live in the BadgerDB sync-state KV — the same
// store as the canonical log, so a volume restore rolls markers back together
// with the log that produced the balances (the projection is rebuilt from the
// log). Mode (a) fresh-bootstrap migration: the legacy dual-population
// (defaultdb + accountsdb) dual-read is retired — there is exactly ONE marker
// population.
//
// ORDERING (fail-direction guarantee, preserved from F4): account writes
// commit BEFORE their marker (ApplyTxAtomic writes docs first, marker last;
// the drain writes markers strictly after all account chunks). A crash between
// the two fails toward bounded re-apply — NEVER toward silent skip.
// TODO(STEP 3): move markers + accounts into one projector SQL transaction to
// upgrade the ordering guarantee to true atomicity.

package DB_OPs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gossipnode/config"
)

// MarkerRevoked is the value-aware "rolled back" sentinel. Matches the
// convention already used for tx_processing cleanup (Processing.go).
const MarkerRevoked = int64(-1)

// maxMarkerBatch chunks marker writes. ThebeDB's BadgerDB store has no
// ExecAll-style 1024-entry commit cap (the C2 chain-halt class) — writes are
// per-key — but chunking keeps failure windows small and bounded.
const maxMarkerBatch = 500

// TxProcessedKey / BlockProcessedKey build the marker keys. Formats are frozen —
// they must match the legacy populations already on disk.
func TxProcessedKey(txHash string) string       { return "tx_processed:" + txHash }
func TxProcessingKey(txHash string) string      { return "tx_processing:" + txHash }
func BlockProcessedKey(blockHash string) string { return "block_processed:" + blockHash }

// markerValueApplied is the single value-aware decision: does this raw marker
// value mean "applied"? Pure; unit-tested in tx_markers_test.go.
func markerValueApplied(raw []byte) bool {
	v, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		// Unparseable = APPLIED. All known legacy populations hold JSON int64s
		// (Nov-2025/current defaultdb, Dec-2025 accountsdb), so this branch
		// should be unreachable; if a corrupt value ever lands here,
		// "applied" is the skip direction and the affected tx becomes a case
		// for the historical repair job, which recomputes from the tx index
		// rather than trusting markers.
		return true
	}
	return v != MarkerRevoked
}

// markerKV is the frozen marker wire form: key bytes + decimal-string value
// bytes (identical to toBytes(int64) / the legacy markers on disk). It
// replaces the retired immudb schema.Op with the same GetKv() surface so the
// encoding test pins the exact same bytes.
type markerKV struct {
	Key   []byte
	Value []byte
}

// GetKv preserves the schema.Op accessor shape used by TestMarkerOpEncoding.
func (m *markerKV) GetKv() *markerKV { return m }

// markerOp builds a KV op holding an int64 in the frozen marker encoding
// (JSON int64 = decimal string bytes — matches toBytes and the legacy
// populations on disk).
func markerOp(key string, value int64) *markerKV {
	return &markerKV{
		Key:   []byte(key),
		Value: []byte(strconv.FormatInt(value, 10)),
	}
}

// IsMarkerApplied reads one marker key from the ThebeDB sync-state KV
// (single authoritative population — the legacy defaultdb dual-read retired
// with mode (a) bootstrap).
// Fail direction: (false, err) — callers decide; the live guard sites keep
// their historical fail-open shape (err → process) and log.
func IsMarkerApplied(_ *config.PooledConnection, key string) (bool, error) {
	h, err := getHandle(nil)
	if err != nil {
		return false, fmt.Errorf("marker %s: %w", key, err)
	}
	raw, err := h.GetSyncKV(key)
	if err != nil {
		return false, fmt.Errorf("marker %s: %w", key, err)
	}
	if raw == nil {
		return false, nil
	}
	return markerValueApplied(raw), nil
}

// ApplyTxAtomic commits one transaction's complete effect: every touched
// account document, then the tx_processed marker LAST. On ThebeDB the account
// writes route through mergeAccountForWrite/storeAccount (canonical log) and
// the marker through the sync-state KV; the marker-last ordering guarantees a
// crash mid-apply is re-applied on replay — never silently skipped.
// TODO(STEP 3): single projector SQL transaction for docs + marker.
func ApplyTxAtomic(_ *config.PooledConnection, docs []*Account, txHash string, appliedAt int64) error {
	if len(docs) == 0 {
		return fmt.Errorf("ApplyTxAtomic %s: no account docs staged", txHash)
	}

	// Accounts first — through the single merge decision point.
	entries := make([]struct {
		Key   string
		Value []byte
	}, 0, len(docs))
	for _, doc := range docs {
		val, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("ApplyTxAtomic %s: marshal account %s: %w", txHash, doc.Address.Hex(), err)
		}
		entries = append(entries, struct {
			Key   string
			Value []byte
		}{Key: Prefix + doc.Address.Hex(), Value: val})
	}
	if err := BatchRestoreAccounts(nil, nil, entries); err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: accounts: %w", txHash, err)
	}

	// Marker LAST — the "done" claim only lands after the effects it describes.
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: %w", txHash, err)
	}
	op := markerOp(TxProcessedKey(txHash), appliedAt)
	if err := h.PutSyncKV(string(op.Key), op.Value); err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: marker: %w", txHash, err)
	}
	return nil
}

// WriteTxProcessedMarkers writes applied markers for a set of txs.
// Used by the drain worker for RECON-applied txs: reconciliation's balance
// effects commit through BatchRestoreAccounts, and these markers make those
// txs visible to the live guards and to future delta exclusion — without
// them, a recon re-run after a failed anchor advance re-applies the same
// deltas.
//
// ORDERING (enforced by the caller): markers commit strictly AFTER all
// account chunks of the drain batch. Markers-first + a later account-chunk
// failure would let a recon rerun exclude never-applied txs (permanent skip);
// markers-last fails toward bounded double-apply on retry instead.
//
// Idempotent: re-writing the same marker value is a no-op semantically.
func WriteTxProcessedMarkers(_ *config.PooledConnection, markers map[string]int64) error {
	if len(markers) == 0 {
		return nil
	}
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("write tx markers: %w", err)
	}
	n := 0
	for hash, appliedAt := range markers {
		op := markerOp(TxProcessedKey(hash), appliedAt)
		if err := h.PutSyncKV(string(op.Key), op.Value); err != nil {
			return fmt.Errorf("write tx markers (%d/%d written): %w", n, len(markers), err)
		}
		n++
	}
	return nil
}

// RevokeTxProcessedMarkers overwrites the given txs' markers with -1
// (rollback path). Runs BEFORE balance restoration: a crash between
// revocation and restore leaves revoked markers over still-applied balances →
// replay re-applies → bounded double-apply, the repairable direction. The
// reverse order would leave applied markers over restored balances → effects
// silently skipped forever.
func RevokeTxProcessedMarkers(_ *config.PooledConnection, txHashes []string) error {
	if len(txHashes) == 0 {
		return nil
	}
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("revoke markers: %w", err)
	}
	for i, hash := range txHashes {
		op := markerOp(TxProcessedKey(hash), MarkerRevoked)
		if err := h.PutSyncKV(string(op.Key), op.Value); err != nil {
			return fmt.Errorf("revoke markers (%d/%d revoked): %w", i, len(txHashes), err)
		}
	}
	return nil
}

// WriteBlockProcessedMarker writes the block-level marker.
// This is only a fast-path hint — the per-tx markers carry the actual
// exactly-once guarantee — but it short-circuits whole-block replays cheaply.
func WriteBlockProcessedMarker(_ *config.PooledConnection, blockHash string) error {
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("block marker %s: %w", blockHash, err)
	}
	op := markerOp(BlockProcessedKey(blockHash), time.Now().UTC().Unix())
	if err := h.PutSyncKV(string(op.Key), op.Value); err != nil {
		return fmt.Errorf("block marker %s: %w", blockHash, err)
	}
	return nil
}

// FilterProcessedTxMarkers returns which of txHashes carry an APPLIED
// tx_processed marker (value-aware: -1 revoked = not processed). Used by
// reconciliation's delta exclusion (I2: never re-apply live-applied txs).
//
// FAIL-CLOSED: any storage error returns (nil, err) — the caller must abort
// delta computation rather than proceed with a partial view, because a
// partial "not processed" answer re-applies live-applied txs (double-count).
//
// ThebeDB: exactly ONE marker population (sync-state KV) — the legacy
// defaultdb dual-read died with mode (a) fresh bootstrap.
func FilterProcessedTxMarkers(txHashes []string) (map[string]bool, error) {
	processed := make(map[string]bool, len(txHashes))
	if len(txHashes) == 0 {
		return processed, nil
	}
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("marker filter: %w", err)
	}
	for _, hash := range txHashes {
		raw, err := h.GetSyncKV(TxProcessedKey(hash))
		if err != nil {
			return nil, fmt.Errorf("marker filter %s: %w", hash, err) // fail-closed
		}
		if raw == nil {
			continue
		}
		if markerValueApplied(raw) {
			processed[hash] = true
		}
	}
	return processed, nil
}
