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
// ORDERING: account writes commit BEFORE their marker (ApplyTxAtomic writes
// docs first, marker last; the drain writes markers strictly after all account
// chunks), so a crash never produces a marker over missing effects.
//
// CRASH SAFETY (live apply): the account writes and the marker are separate
// store commits, so ApplyTxAtomic first persists a redo record holding the
// tx's complete staged post-state (absolute documents, not deltas). If the
// process dies part-way, RecoverPendingTxApply rewrites those same documents
// and the marker at boot — an idempotent roll-forward — instead of letting the
// tx be re-staged from a partially updated base (which would apply it twice).

package DB_OPs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gossipnode/config"
)

// MarkerRevoked is the value-aware "rolled back" sentinel. Matches the
// convention already used for tx_processing cleanup (Processing.go).
const MarkerRevoked = int64(-1)

// TxProcessedKey / BlockProcessedKey build the marker keys. Formats are frozen —
// they must match the legacy populations already on disk.
func TxProcessedKey(txHash string) string { return "tx_processed:" + txHash }

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

// txApplyRedoKey holds the in-flight live apply. Live applies are serialized by
// LockStateApply, so at most one is ever in flight and a single key suffices.
// An empty value means "nothing pending".
const txApplyRedoKey = "tx_apply_redo"

// txApplyRedo is the persisted form of one in-flight apply. A single-tx apply
// sets TxHash only, in the same format older binaries wrote. A group apply
// (ApplyTxGroupAtomic with more than one tx) also sets TxHashes, the full list
// in block order; TxHash then holds its first entry.
type txApplyRedo struct {
	TxHash    string     `json:"tx_hash"`
	TxHashes  []string   `json:"tx_hashes,omitempty"`
	AppliedAt int64      `json:"applied_at"`
	Docs      []*Account `json:"docs"`
}

// hashes returns every tx the record covers.
func (r *txApplyRedo) hashes() []string {
	if len(r.TxHashes) > 0 {
		return r.TxHashes
	}
	return []string{r.TxHash}
}

// applyLabel names an apply in errors: the tx hash, or the first hash and a
// count for a group.
func applyLabel(txHashes []string) string {
	switch len(txHashes) {
	case 0:
		return "(no txs)"
	case 1:
		return txHashes[0]
	default:
		return fmt.Sprintf("%s (+%d txs)", txHashes[0], len(txHashes)-1)
	}
}

// redoStore is the subset of the handle the redo record needs.
type redoStore interface {
	GetSyncKV(key string) ([]byte, error)
	PutSyncKV(key string, value []byte) error
}

func readTxApplyRedo(h redoStore) (*txApplyRedo, error) {
	raw, err := h.GetSyncKV(txApplyRedoKey)
	if err != nil {
		return nil, fmt.Errorf("read apply redo: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var r txApplyRedo
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode apply redo: %w", err)
	}
	if r.TxHash == "" || len(r.Docs) == 0 || (len(r.TxHashes) > 0 && r.TxHashes[0] != r.TxHash) {
		return nil, fmt.Errorf("apply redo record is malformed (tx=%q docs=%d)", r.TxHash, len(r.Docs))
	}
	return &r, nil
}

func clearTxApplyRedo(h redoStore) error {
	if err := h.PutSyncKV(txApplyRedoKey, []byte{}); err != nil {
		return fmt.Errorf("clear apply redo: %w", err)
	}
	return nil
}

// writeTxEffects performs the commits that make up an apply's effect: the
// absolute account documents, then one tx_processed marker per tx, in order.
// Idempotent: re-running it with the same inputs converges to the same state.
func writeTxEffects(h redoStore, docs []*Account, txHashes []string, appliedAt int64) error {
	// RAW authoritative write (merge-bypassing). The stage was built from the
	// stored base under LockStateApply, so these absolute docs must win
	// unconditionally; the LWW merge gate is for uncoordinated sync writers
	// only (see DB_OPs/authoritative_write.go).
	entries := make([]struct {
		Key   string
		Value []byte
	}, 0, len(docs))
	for _, doc := range docs {
		val, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("marshal account %s: %w", doc.Address.Hex(), err)
		}
		entries = append(entries, struct {
			Key   string
			Value []byte
		}{Key: Prefix + doc.Address.Hex(), Value: val})
	}
	if err := BatchPutAccountsAuthoritative(entries); err != nil {
		return fmt.Errorf("accounts: %w", err)
	}
	for i, txHash := range txHashes {
		op := markerOp(TxProcessedKey(txHash), appliedAt)
		if err := h.PutSyncKV(string(op.Key), op.Value); err != nil {
			return fmt.Errorf("marker %s (%d/%d written): %w", txHash, i, len(txHashes), err)
		}
	}
	return nil
}

// ApplyTxAtomic commits one transaction's complete effect: every touched
// account document, then the tx_processed marker LAST, bracketed by a redo
// record so a crash between the commits is rolled forward at boot
// (RecoverPendingTxApply) rather than re-applied on a mutated base.
//
// Caller must hold LockStateApply.
//
// On a returned error the redo record is cleared: the caller rolls the block
// back (rollbackState), and a leftover record would otherwise re-apply the
// failed tx after that rollback.
func ApplyTxAtomic(c *config.PooledConnection, docs []*Account, txHash string, appliedAt int64) error {
	return ApplyTxGroupAtomic(c, docs, []string{txHash}, appliedAt)
}

// ApplyTxGroupAtomic commits the combined effect of consecutive txs of one
// block: the final document of every account they touched (each written
// once), then one tx_processed marker per tx in block order. The docs must be
// the post-state of applying the txs in that order on the stored base, under
// one continuous hold of LockStateApply.
//
// The same redo record as ApplyTxAtomic brackets the writes, so a crash
// part-way is rolled forward at boot. On a returned error the redo record is
// cleared and some markers of the group may already be written: the caller
// must revoke the markers of every tx in txHashes before restoring balances.
//
// Caller must hold LockStateApply.
func ApplyTxGroupAtomic(_ *config.PooledConnection, docs []*Account, txHashes []string, appliedAt int64) error {
	label := applyLabel(txHashes)
	if len(txHashes) == 0 {
		return fmt.Errorf("ApplyTxGroupAtomic: no txs")
	}
	for _, hash := range txHashes {
		if hash == "" {
			return fmt.Errorf("ApplyTxGroupAtomic %s: empty tx hash", label)
		}
	}
	if len(docs) == 0 {
		return fmt.Errorf("ApplyTxAtomic %s: no account docs staged", label)
	}
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: %w", label, err)
	}

	// A record left over here means boot recovery did not run or failed.
	// Never stack a second apply on top of an unresolved one.
	pending, err := readTxApplyRedo(h)
	if err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: %w", label, err)
	}
	if pending != nil {
		return fmt.Errorf("ApplyTxAtomic %s: unresolved apply of tx %s is pending; run RecoverPendingTxApply first", label, applyLabel(pending.hashes()))
	}

	rec := &txApplyRedo{TxHash: txHashes[0], AppliedAt: appliedAt, Docs: docs}
	if len(txHashes) > 1 {
		rec.TxHashes = txHashes
	}
	redo, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: encode redo: %w", label, err)
	}
	if err := h.PutSyncKV(txApplyRedoKey, redo); err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: write redo: %w", label, err)
	}

	if err := writeTxEffects(h, docs, txHashes, appliedAt); err != nil {
		if cerr := clearTxApplyRedo(h); cerr != nil {
			return fmt.Errorf("ApplyTxAtomic %s: %w (and %v)", label, err, cerr)
		}
		return fmt.Errorf("ApplyTxAtomic %s: %w", label, err)
	}

	if err := clearTxApplyRedo(h); err != nil {
		// Effects and markers are committed. A stale record is harmless to
		// recovery (it rewrites identical docs) but blocks the next apply, so
		// surface it.
		return fmt.Errorf("ApplyTxAtomic %s: applied, but %w", label, err)
	}
	return nil
}

// RecoverPendingTxApply rolls forward a live apply that was interrupted
// between its account writes and its marker. Call once at startup, after the
// ThebeDB handle is wired and before any block processing.
//
// Returns the recovered tx hash, or the comma-separated hashes of a group
// ("" when nothing was pending). An error means state could not be made
// consistent; the node must not process blocks.
func RecoverPendingTxApply() (string, error) {
	h, err := getHandle(nil)
	if err != nil {
		return "", fmt.Errorf("recover apply: %w", err)
	}
	LockStateApply()
	defer UnlockStateApply()
	return recoverPendingTxApply(h)
}

func recoverPendingTxApply(h redoStore) (string, error) {
	pending, err := readTxApplyRedo(h)
	if err != nil {
		return "", fmt.Errorf("recover apply: %w", err)
	}
	if pending == nil {
		return "", nil
	}
	hashes := pending.hashes()
	label := applyLabel(hashes)
	// A revoked marker means the block was rolled back after this record was
	// written; rolling forward would undo that rollback. The rollback revokes
	// every tx of a group, so one revoked marker is enough to discard it.
	for _, hash := range hashes {
		raw, err := h.GetSyncKV(TxProcessedKey(hash))
		if err != nil {
			return "", fmt.Errorf("recover apply %s: marker read %s: %w", label, hash, err)
		}
		if raw != nil && !markerValueApplied(raw) {
			if err := clearTxApplyRedo(h); err != nil {
				return "", fmt.Errorf("recover apply %s: %w", label, err)
			}
			return "", nil
		}
	}
	if err := writeTxEffects(h, pending.Docs, hashes, pending.AppliedAt); err != nil {
		return "", fmt.Errorf("recover apply %s: %w", label, err)
	}
	if err := clearTxApplyRedo(h); err != nil {
		return "", fmt.Errorf("recover apply %s: %w", label, err)
	}
	return strings.Join(hashes, ","), nil
}

// DiscardPendingTxApply drops an in-flight apply record without applying it.
// Called by the block rollback path before balances are restored, so a record
// that survived a failed ApplyTxAtomic cannot re-apply the failed tx later.
// Caller must hold LockStateApply.
func DiscardPendingTxApply() error {
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("discard apply: %w", err)
	}
	raw, err := h.GetSyncKV(txApplyRedoKey)
	if err != nil {
		return fmt.Errorf("discard apply: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}
	return clearTxApplyRedo(h)
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
