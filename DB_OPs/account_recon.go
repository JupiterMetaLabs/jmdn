// MODULE: DB_OPs/account_recon
// PURPOSE: Commutative, exactly-once application of one block's balance
//          effects for reconciliation (FastSync catch-up).
//
// DESIGN:
//   - Reconciliation does not ship computed balances. It ships BLOCK
//     REFERENCES ({number, hash}); this module recomputes the block's deltas
//     from the locally stored block AT APPLY TIME, so the write can never be
//     built on a stale base.
//   - The tx_processed marker filter runs AT APPLY TIME under the global
//     state-apply lock, so a tx applied by the live path a microsecond ago is
//     excluded here — each tx's effect lands exactly once, with no timestamp
//     arbitration between writers.
//   - Balances mutate as base+delta read-modify-write (commutative with live
//     execution), and the block's tx markers commit IN THE SAME ExecAll as
//     the balances. Marker ⟺ effect applied, atomically. A replay of the same
//     block message finds every tx marked and no-ops.
//   - Fee arithmetic is config.GasFee / config.SplitFee — the same single
//     source of truth the live path uses (config/gasfee.go). FeeRecipients is
//     threaded from the stored block, matching live execution exactly.
//
// FAIL DIRECTION: any error leaves the remaining tx groups unapplied and
// unmarked; callers leave the queue entry unACKed / the anchor lagging and
// retry. Already-committed groups are excluded on retry by their own markers,
// so a partial block converges instead of repeating.

package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"
	"time"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// BlockAccountDelta is one account's aggregate effect within a single block.
// TxNonce is an absolute candidate (highest sender nonce + 1); the rest are
// additive deltas.
type BlockAccountDelta struct {
	BalanceDelta *big.Int
	TxNonce      uint64 // 0 = not a sender in this block
	TxCountSent  uint64
	IsSender     bool
}

// txTouchedAccounts returns a conservative superset of the lowercase-hex
// accounts one transaction mutates (sender, receiver, coinbase side, zkvm).
// Used only to budget ExecAll group sizes — overcounting is harmless.
func txTouchedAccounts(blk *config.ZKBlock, tx *config.Transaction) []string {
	out := make([]string, 0, 4+len(blk.FeeRecipients))
	if tx.From != nil {
		out = append(out, strings.ToLower(tx.From.Hex()))
	}
	if tx.To != nil {
		out = append(out, strings.ToLower(tx.To.Hex()))
	}
	if blk.CoinbaseAddr != nil {
		out = append(out, strings.ToLower(blk.CoinbaseAddr.Hex()))
	}
	for _, r := range blk.FeeRecipients {
		out = append(out, strings.ToLower(r.Addr.Hex()))
	}
	if blk.ZKVMAddr != nil {
		out = append(out, strings.ToLower(blk.ZKVMAddr.Hex()))
	}
	return out
}

// ComputeBlockDeltas aggregates the balance/nonce effects of every tx in blk
// that is NOT in skipTxs, keyed by lowercase hex address. Mirrors the live
// path arithmetic (messaging/BlockProcessing processTransaction):
//
//	sender   -= value + config.GasFee(...); TxNonce = nonce+1; TxCountSent++
//	receiver += value
//	coinbase side += per config.SplitFee(gasFee, coinbase, blk.FeeRecipients)
//	zkvm     += zkvm share
//
// First-touch sender tracking (IsSender) fixes the historical nonce-0 edge:
// a sender whose only tx has nonce 0 must still yield TxNonce 1.
func ComputeBlockDeltas(blk *config.ZKBlock, skipTxs map[string]bool) map[string]*BlockAccountDelta {
	deltas := make(map[string]*BlockAccountDelta)
	if blk == nil {
		return deltas
	}
	get := func(addr string) *BlockAccountDelta {
		d, ok := deltas[addr]
		if !ok {
			d = &BlockAccountDelta{BalanceDelta: new(big.Int)}
			deltas[addr] = d
		}
		return d
	}

	var coinbase common.Address
	var coinbaseSet bool
	if blk.CoinbaseAddr != nil {
		coinbase = *blk.CoinbaseAddr
		coinbaseSet = true
	}
	zkvmSet := blk.ZKVMAddr != nil

	for i := range blk.Transactions {
		tx := &blk.Transactions[i]
		if skipTxs[tx.Hash.String()] {
			continue
		}

		gasFee := config.GasFee(tx.Type, tx.GasLimit, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee)
		zkvmShare, coinbaseCredits := config.SplitFee(gasFee, coinbase, blk.FeeRecipients)

		if tx.From != nil {
			d := get(strings.ToLower(tx.From.Hex()))
			d.BalanceDelta.Sub(d.BalanceDelta, gasFee)
			if tx.Value != nil && tx.Value.Sign() > 0 {
				d.BalanceDelta.Sub(d.BalanceDelta, tx.Value)
			}
			// First-touch OR ascending: covers the single-tx nonce-0 sender
			// (0 > 0 is false, but !IsSender is true on first touch).
			if !d.IsSender || tx.Nonce+1 > d.TxNonce {
				d.TxNonce = tx.Nonce + 1
			}
			d.TxCountSent++
			d.IsSender = true
		}
		if tx.To != nil && tx.Value != nil && tx.Value.Sign() > 0 {
			d := get(strings.ToLower(tx.To.Hex()))
			d.BalanceDelta.Add(d.BalanceDelta, tx.Value)
		}
		if coinbaseSet {
			for _, c := range coinbaseCredits {
				d := get(strings.ToLower(c.Addr.Hex()))
				d.BalanceDelta.Add(d.BalanceDelta, c.Amount)
			}
		}
		if zkvmSet {
			d := get(strings.ToLower(blk.ZKVMAddr.Hex()))
			d.BalanceDelta.Add(d.BalanceDelta, zkvmShare)
		}
	}
	return deltas
}

// applyDeltaToAccount merges one block delta into a stored account (nil =
// account does not exist yet → created from zero). Identity fields are
// preserved from the stored object; the ART identity Nonce is never touched.
// UpdatedAt is block-timestamp derived (nanoseconds), matching the live
// executor, so both paths produce identical documents for identical effects.
//
// A negative resulting balance is WRITTEN (with the caller logging loudly):
// out-of-order catch-up can transiently dip below zero mid-range; the sum is
// correct once the range completes. Clamping would change the range total.
func applyDeltaToAccount(existing *Account, addr common.Address, d *BlockAccountDelta, updatedAtNanos int64) (*Account, error) {
	doc := &Account{Address: addr, Balance: "0", AccountType: "user", CreatedAt: updatedAtNanos}
	if existing != nil {
		cp := *existing
		doc = &cp
	}
	base := new(big.Int)
	if doc.Balance != "" {
		if _, ok := base.SetString(doc.Balance, 10); !ok {
			return nil, fmt.Errorf("invalid stored balance %q for %s", doc.Balance, addr.Hex())
		}
	}
	doc.Balance = new(big.Int).Add(base, d.BalanceDelta).String()
	if d.TxNonce > doc.TxNonce {
		doc.TxNonce = d.TxNonce
	}
	doc.TxCountSent += d.TxCountSent
	doc.UpdatedAt = updatedAtNanos
	return doc, nil
}

// ApplyBlockRecon applies one block's outstanding balance effects exactly
// once. It is the reconciliation counterpart of the live path's
// ProcessBlockTransactions, sharing its arithmetic and its marker namespace.
//
// Steps (under the global state-apply lock):
//  1. Load the stored block by number; verify the stored hash matches
//     expectHash (mismatch = stored block replaced → error).
//  2. Filter the block's txs through the tx_processed markers (fresh read).
//     All marked → already fully applied → no-op success.
//  3. Partition the pending txs into groups whose account documents plus tx
//     markers fit ONE ExecAll, and commit each group atomically: the group's
//     deltas and the group's markers land together. Marker ⟺ effect applied
//     therefore holds at EVERY boundary — a replay, or a live apply arriving
//     between groups, observes exactly the committed txs as marked and
//     processes only the remainder.
//
// Returns applied=false only with a non-nil error. Idempotent: replaying a
// fully-committed block finds all markers set and returns (true, nil).
func ApplyBlockRecon(accountsConn *config.PooledConnection, blockNumber uint64, expectHash string) (bool, error) {
	mainCtx, mainCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer mainCancel()
	mainConn, err := GetMainDBConnectionandPutBack(mainCtx)
	if err != nil {
		return false, fmt.Errorf("recon block %d: main connection: %w", blockNumber, err)
	}
	defer PutMainDBConnection(mainConn)

	blk, err := GetZKBlockByNumber(mainConn, blockNumber)
	if err != nil {
		return false, fmt.Errorf("recon block %d: load: %w", blockNumber, err)
	}
	if expectHash != "" && !strings.EqualFold(blk.BlockHash.Hex(), expectHash) {
		return false, fmt.Errorf("recon block %d: stored hash %s != enqueued hash %s (block replaced?)",
			blockNumber, blk.BlockHash.Hex(), expectHash)
	}
	if len(blk.Transactions) == 0 {
		return true, nil // nothing to apply
	}

	// Serialize with the live executor: marker filter + base reads + commit
	// form one critical section, so a tx cannot be applied by both paths.
	LockStateApply()
	defer UnlockStateApply()

	// CRASH-WINDOW GUARD: commitReconGroup writes accounts first, markers
	// last (no cross-store atomicity until the ThebeDB builder-2PC task
	// lands). A crash between the two would make a naive replay recompute
	// deltas on the ALREADY-MUTATED base — a silent double-credit. The intent
	// record turns that into a LOUD stop requiring operator resolution.
	ih, ihErr := getHandle(accountsConn)
	if ihErr != nil {
		return false, fmt.Errorf("recon block %d: handle: %w", blockNumber, ihErr)
	}
	intentKey := fmt.Sprintf("recon_intent:%d", blockNumber)
	raw, gerr := ih.GetSyncKV(intentKey)
	if gerr != nil {
		// Fail closed (audit SYN-08): a read error here is exactly the
		// store-unhealthy condition that produces the crash this guard exists
		// for. Do not skip the guard on error.
		return false, fmt.Errorf("recon block %d: intent read (fail closed): %w", blockNumber, gerr)
	}
	if string(raw) == "pending" {
		return false, fmt.Errorf(
			"recon block %d: crash-window detected — a previous reconciliation of this block stopped between account writes and markers (sync-state key %q is 'pending'); balances may be partially applied. Manual resolution required: verify the block's account balances, then overwrite the key with 'done' to re-enable recon for this block",
			blockNumber, intentKey)
	}

	hashes := make([]string, 0, len(blk.Transactions))
	for i := range blk.Transactions {
		hashes = append(hashes, blk.Transactions[i].Hash.String())
	}
	applied, err := FilterProcessedTxMarkers(hashes)
	if err != nil {
		return false, fmt.Errorf("recon block %d: marker filter (fail closed): %w", blockNumber, err)
	}
	pendingTx := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if !applied[h] {
			pendingTx = append(pendingTx, h)
		}
	}
	if len(pendingTx) == 0 {
		return true, nil // fully live-applied or replayed message — no-op
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pendingSet := make(map[string]bool, len(pendingTx))
	for _, h := range pendingTx {
		pendingSet[h] = true
	}

	updatedAt := blk.Timestamp * int64(time.Second) // nanos, block-derived (matches live)
	appliedAt := time.Now().UTC().Unix()

	if err := ih.PutSyncKV(intentKey, []byte("pending")); err != nil {
		return false, fmt.Errorf("recon block %d: intent write (fail closed): %w", blockNumber, err)
	}

	for _, groupTxs := range partitionReconGroups(blk, pendingSet, reconGroupOpBudget) {
		// Deltas for EXACTLY this group: skip everything outside it.
		skip := make(map[string]bool, len(blk.Transactions))
		inGroup := make(map[string]bool, len(groupTxs))
		for _, h := range groupTxs {
			inGroup[h] = true
		}
		for i := range blk.Transactions {
			if h := blk.Transactions[i].Hash.String(); !inGroup[h] {
				skip[h] = true
			}
		}
		deltas := ComputeBlockDeltas(blk, skip)

		if err := commitReconGroup(ctx, accountsConn, blockNumber, deltas, groupTxs, updatedAt, appliedAt); err != nil {
			return false, err
		}
	}

	if err := ih.PutSyncKV(intentKey, []byte("done")); err != nil {
		// Accounts + markers are fully committed; only the intent record is
		// stale. Fail loud anyway so the operator clears it — a 'pending'
		// leftover blocks the next recon of this block by design.
		return false, fmt.Errorf("recon block %d: applied fully but intent-clear failed (key %q stuck 'pending'): %w", blockNumber, intentKey, err)
	}
	return true, nil
}

// reconGroupOpBudget bounds one group's commit batch: distinct account
// documents + tx markers. Nearly every block forms a single group; only
// blocks touching hundreds of accounts split.
const reconGroupOpBudget = 1000

// partitionReconGroups splits a block's PENDING txs (block order preserved)
// into consecutive groups whose projected ExecAll size — distinct touched
// accounts + one marker per tx — stays within budget. Pure function.
//
// Invariants: every pending tx appears in exactly one group; a group is never
// empty; the first tx of a group is always admitted regardless of budget (a
// single tx must be committable on its own).
func partitionReconGroups(blk *config.ZKBlock, pendingSet map[string]bool, budget int) [][]string {
	var groups [][]string
	i := 0
	for i < len(blk.Transactions) {
		groupAccounts := make(map[string]bool)
		var groupTxs []string
		for i < len(blk.Transactions) {
			tx := &blk.Transactions[i]
			h := tx.Hash.String()
			if !pendingSet[h] {
				i++
				continue // already applied — excluded from every group
			}
			cand := txTouchedAccounts(blk, tx)
			newAccounts := 0
			for _, a := range cand {
				if !groupAccounts[a] {
					newAccounts++
				}
			}
			if len(groupTxs) > 0 && len(groupAccounts)+newAccounts+len(groupTxs)+1 > budget {
				break // group full — commit it, continue with the rest
			}
			for _, a := range cand {
				groupAccounts[a] = true
			}
			groupTxs = append(groupTxs, h)
			i++
		}
		if len(groupTxs) > 0 {
			groups = append(groups, groupTxs)
		}
	}
	return groups
}

// commitReconGroup writes one tx group's account documents, then its tx
// markers LAST — the same crash-consistency contract as the live path
// (ApplyTxAtomic): the "done" claim only lands after the effects it
// describes.
//
// ATOMICITY NOTE (vs the ImmuDB original): the original committed documents
// and markers in one ExecAll. The ThebeDB path has no mixed account+sync-KV
// batch primitive, so a crash between the account writes and the marker
// writes re-applies the group on replay — a BOUNDED double-apply through the
// mergeAccountForWrite LWW decision point, which is the failure direction
// this codebase explicitly prefers over a silent skip (see the marker-revoke
// commentary in messaging/BlockProcessing). If stricter atomicity is needed,
// route the group through ThebeDB's builder 2PC (tracked in
// docs/RECONCILE-thebe-sc.md).
func commitReconGroup(ctx context.Context, conn *config.PooledConnection, blockNumber uint64,
	deltas map[string]*BlockAccountDelta, groupTxs []string, updatedAtNanos, appliedAt int64) error {

	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("recon block %d: handle: %w", blockNumber, err)
	}

	// Deterministic account order (stable commit layout across retries).
	addrs := make([]string, 0, len(deltas))
	for a := range deltas {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)

	// Prefetch stored bases. Key format must match GetAccount/ApplyTxAtomic
	// exactly: fmt.Sprintf("%s%s", Prefix, address) with the checksummed
	// stringer. Absent accounts are nil bases (applyDeltaToAccount creates
	// them); a real read error must fail closed — treating accounts as absent
	// would zero their bases.
	existing := make(map[string]*Account, len(addrs))
	for _, a := range addrs {
		addr := common.HexToAddress(a)
		key := fmt.Sprintf("%s%s", Prefix, addr)
		sa, gerr := h.GetAccount(ctx, addr.Hex())
		if gerr != nil {
			return fmt.Errorf("recon block %d: prefetch %s: %w", blockNumber, addr.Hex(), gerr)
		}
		if sa != nil {
			existing[key] = storeAccountFromStore(sa)
		}
	}

	// Materialize the absolute post-group documents.
	entries := make([]struct {
		Key   string
		Value []byte
	}, 0, len(addrs))
	for _, a := range addrs {
		addr := common.HexToAddress(a)
		key := fmt.Sprintf("%s%s", Prefix, addr)
		doc, derr := applyDeltaToAccount(existing[key], addr, deltas[a], updatedAtNanos)
		if derr != nil {
			return fmt.Errorf("recon block %d: %w", blockNumber, derr)
		}
		if strings.HasPrefix(doc.Balance, "-") {
			// A transient dip below zero is possible while a range is applied
			// behind already-executed newer blocks; the range total converges.
			// Loud so a PERSISTENT negative gets investigated.
			log.Printf("[recon] WARNING: transient negative balance %s for %s applying block %d (converges when the range completes)",
				doc.Balance, addr.Hex(), blockNumber)
		}
		val, merr := json.Marshal(doc)
		if merr != nil {
			return fmt.Errorf("recon block %d: marshal %s: %w", blockNumber, addr.Hex(), merr)
		}
		entries = append(entries, struct {
			Key   string
			Value []byte
		}{Key: key, Value: val})
	}

	// Accounts first — RAW authoritative write (merge-bypassing): these are
	// absolute post-group docs computed from the stored base under
	// LockStateApply and must win unconditionally (see authoritative_write.go;
	// KB-review findings 1-2).
	if len(entries) > 0 {
		if err := BatchPutAccountsAuthoritative(entries); err != nil {
			return fmt.Errorf("recon block %d: accounts (%d docs): %w", blockNumber, len(entries), err)
		}
	}

	// Markers LAST.
	for _, txh := range groupTxs {
		op := markerOp(TxProcessedKey(txh), appliedAt)
		if err := h.PutSyncKV(string(op.Key), op.Value); err != nil {
			return fmt.Errorf("recon block %d: marker %s: %w", blockNumber, txh, err)
		}
	}
	return nil
}
