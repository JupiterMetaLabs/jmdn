// MODULE: DB_OPs/tx_markers
// PURPOSE: Value-aware processed-marker layer + the per-tx atomic apply used by
//          the live executor (F4, RCA_account_sync.md §6e, design D-B).
//
// MARKER SEMANTICS (value-aware, NOT existence-based):
//   value > 0  → applied (Unix timestamp of application)
//   value = -1 → REVOKED: the tx's balance effects were rolled back after the
//                marker committed. Introduced by F4-A1: per-tx atomic commits
//                make the prefix's markers durable before a later tx can fail
//                the block; rollback restores balances but immudb cannot delete
//                inside a transaction — so rollback overwrites the prefix
//                markers with -1 and every consumer treats -1 as not-processed.
//   unparseable → applied (legacy markers hold timestamps; fail toward skip
//                would be I1-unsafe only if a legacy value were corrupt AND its
//                effects absent — treated as the lower-risk direction).
//
// DB PRECEDENCE (dual-read): accountsdb is authoritative when the key exists
// there (F4 writes markers + revocations to accountsdb, atomically with the
// balances they describe); defaultdb is consulted only for keys absent from
// accountsdb (legacy populations: current-era defaultdb markers + pre-F4
// history). Precedence is load-bearing: a -1 revocation in accountsdb must
// never be overridden by a stale legacy marker for the same tx.
//
// WHY MARKERS LIVE IN accountsdb FROM F4 ON: immudb ExecAll executes on the
// session's selected database (ExecAllRequest carries no DB field) — the ONLY
// way a marker can commit atomically with the account balances it describes is
// to live in the same database.

package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gossipnode/config"

	"github.com/codenotary/immudb/pkg/api/schema"
)

// MarkerRevoked is the value-aware "rolled back" sentinel. Matches the
// convention already used for tx_processing cleanup (Processing.go).
const MarkerRevoked = int64(-1)

// maxExecAllEntries mirrors immudb embedded/store DefaultMaxTxEntries (1024).
// Any ExecAll larger than this fails the commit outright (C2: the pre-F4
// block-end marker commit wrote 2×txs+1 entries and halted on blocks >511 txs).
const maxExecAllEntries = 1024

// TxProcessedKey / BlockProcessedKey build the marker keys. Formats are frozen —
// they must match the legacy populations already on disk.
func TxProcessedKey(txHash string) string    { return "tx_processed:" + txHash }
func TxProcessingKey(txHash string) string   { return "tx_processing:" + txHash }
func BlockProcessedKey(blockHash string) string { return "block_processed:" + blockHash }

// markerValueApplied is the single value-aware decision: does this raw marker
// value mean "applied"? Pure; unit-tested in tx_markers_test.go.
func markerValueApplied(raw []byte) bool {
	v, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		// Legacy/corrupt value — see MARKER SEMANTICS in the module header.
		return true
	}
	return v != MarkerRevoked
}

// IsMarkerApplied dual-reads one marker key: accountsdb (authoritative,
// value-aware) first, defaultdb (legacy) only when accountsdb has no key.
// accountsConn may be the caller's accounts connection (it is used as-is);
// the defaultdb read acquires its own pooled connection.
// Fail direction: (false, err) — callers decide; the live guard sites keep
// their historical fail-open shape (err → process) and log.
func IsMarkerApplied(accountsConn *config.PooledConnection, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// accountsdb — authoritative.
	conn, release, err := withAccountsConn(ctx, accountsConn)
	if err != nil {
		return false, fmt.Errorf("marker %s: %w", key, err)
	}
	entry, aerr := conn.Client.Client.Get(ctx, []byte(key))
	release()
	if aerr == nil && entry != nil {
		return markerValueApplied(entry.Value), nil
	}
	if aerr != nil && !isNotFoundError(aerr) {
		return false, fmt.Errorf("marker %s (accountsdb): %w", key, aerr)
	}

	// defaultdb — legacy population.
	mainConn, err := GetMainDBConnection(ctx)
	if err != nil {
		return false, fmt.Errorf("marker %s: main connection: %w", key, err)
	}
	defer PutMainDBConnection(mainConn)
	if err := ensureMainDBSelected(mainConn); err != nil {
		return false, fmt.Errorf("marker %s: select defaultdb: %w", key, err)
	}
	entry, merr := mainConn.Client.Client.Get(ctx, []byte(key))
	if merr != nil {
		if isNotFoundError(merr) {
			return false, nil
		}
		return false, fmt.Errorf("marker %s (defaultdb): %w", key, merr)
	}
	return markerValueApplied(entry.Value), nil
}

// ApplyTxAtomic commits one transaction's complete effect in ONE accountsdb
// ExecAll (F4 D-B): every touched account document + the tx_processed marker.
// All-or-nothing: a crash leaves either no trace of the tx or a fully-applied,
// fully-marked tx — the partially-applied state the pre-F4 code could produce
// (≤6 independent commits per tx) is no longer expressible.
//
// The transient tx_processing: advisory lock is deliberately NOT included — it
// lives (and is read) in defaultdb and is not correctness-bearing for replay;
// pulling its cleanup into this accountsdb ExecAll would create a new
// split-brain marker population (the H0 lesson).
//
// Entry count is len(docs)+1 — far under the 1024 ExecAll cap (C2) by
// construction.
func ApplyTxAtomic(accountsConn *config.PooledConnection, docs []*Account, txHash string, appliedAt int64) error {
	if len(docs) == 0 {
		return fmt.Errorf("ApplyTxAtomic %s: no account docs staged", txHash)
	}
	if len(docs)+1 > maxExecAllEntries {
		return fmt.Errorf("ApplyTxAtomic %s: %d entries exceeds ExecAll cap %d", txHash, len(docs)+1, maxExecAllEntries)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, accountsConn)
	if err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: %w", txHash, err)
	}
	defer release()

	ops := make([]*schema.Op, 0, len(docs)+1)
	for _, doc := range docs {
		val, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("ApplyTxAtomic %s: marshal account %s: %w", txHash, doc.Address.Hex(), err)
		}
		key := fmt.Sprintf("%s%s", Prefix, doc.Address)
		ops = append(ops, &schema.Op{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{Key: []byte(key), Value: val}}})
	}
	ops = append(ops, markerOp(TxProcessedKey(txHash), appliedAt))

	if _, err := conn.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops}); err != nil {
		return fmt.Errorf("ApplyTxAtomic %s: ExecAll (%d ops): %w", txHash, len(ops), err)
	}
	return nil
}

// WriteTxProcessedMarkers writes applied markers for a set of txs to
// accountsdb, chunked under the ExecAll cap. Used by the drain worker for
// RECON-applied txs (F4 3a): reconciliation's balance effects commit through
// BatchRestoreAccounts, and these markers make those txs visible to the live
// guards and to future delta exclusion — without them, a recon re-run after a
// failed anchor advance re-applies the same deltas.
//
// A2 ORDERING (enforced by the caller): markers commit strictly AFTER all
// account chunks of the drain batch. Markers-first + a later account-chunk
// failure would let a recon rerun exclude never-applied txs (permanent skip);
// markers-last fails toward bounded double-apply on retry instead.
//
// Idempotent: re-writing the same marker value is a no-op semantically.
func WriteTxProcessedMarkers(accountsConn *config.PooledConnection, markers map[string]int64) error {
	if len(markers) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, accountsConn)
	if err != nil {
		return fmt.Errorf("write tx markers: %w", err)
	}
	defer release()

	const chunk = 500 // < maxExecAllEntries
	ops := make([]*schema.Op, 0, chunk)
	flush := func() error {
		if len(ops) == 0 {
			return nil
		}
		if _, err := conn.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops}); err != nil {
			return fmt.Errorf("write tx markers (%d ops): %w", len(ops), err)
		}
		ops = ops[:0]
		return nil
	}
	for h, appliedAt := range markers {
		ops = append(ops, markerOp(TxProcessedKey(h), appliedAt))
		if len(ops) >= chunk {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// RevokeTxProcessedMarkers overwrites the given txs' markers with -1 in
// accountsdb (F4-A1, rollback path). Chunked under the ExecAll cap. Runs
// BEFORE balance restoration: a crash between revocation and restore leaves
// revoked markers over still-applied balances → replay re-applies → bounded
// double-apply, the repairable direction. The reverse order would leave
// applied markers over restored balances → permanent skip (I1 violation).
func RevokeTxProcessedMarkers(accountsConn *config.PooledConnection, txHashes []string) error {
	if len(txHashes) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, accountsConn)
	if err != nil {
		return fmt.Errorf("revoke markers: %w", err)
	}
	defer release()

	const chunk = 500 // < maxExecAllEntries
	for i := 0; i < len(txHashes); i += chunk {
		end := i + chunk
		if end > len(txHashes) {
			end = len(txHashes)
		}
		ops := make([]*schema.Op, 0, end-i)
		for _, h := range txHashes[i:end] {
			ops = append(ops, markerOp(TxProcessedKey(h), MarkerRevoked))
		}
		if _, err := conn.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops}); err != nil {
			return fmt.Errorf("revoke markers [%d:%d]: %w", i, end, err)
		}
	}
	return nil
}

// WriteBlockProcessedMarker writes the block-level marker to accountsdb.
// Post-F4 this is a fast-path hint — the per-tx markers carry the actual
// exactly-once guarantee — but it short-circuits whole-block replays cheaply.
func WriteBlockProcessedMarker(accountsConn *config.PooledConnection, blockHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, release, err := withAccountsConn(ctx, accountsConn)
	if err != nil {
		return fmt.Errorf("block marker %s: %w", blockHash, err)
	}
	defer release()

	ops := []*schema.Op{markerOp(BlockProcessedKey(blockHash), time.Now().UTC().Unix())}
	if _, err := conn.Client.Client.ExecAll(ctx, &schema.ExecAllRequest{Operations: ops}); err != nil {
		return fmt.Errorf("block marker %s: ExecAll: %w", blockHash, err)
	}
	return nil
}

// markerOp builds a KV op holding an int64 in the frozen marker encoding
// (JSON int64 = decimal string bytes — matches toBytes and the legacy
// populations on disk).
func markerOp(key string, value int64) *schema.Op {
	return &schema.Op{Operation: &schema.Op_Kv{Kv: &schema.KeyValue{
		Key:   []byte(key),
		Value: []byte(strconv.FormatInt(value, 10)),
	}}}
}
