// MODULE: DB_OPs/latest_block
// PURPOSE: Single monotonic choke point for the latest_block tip marker.
//          ThebeDB retarget of the F6 module.
//
// ON THEBEDB the authoritative tip READ is SQL MAX(block_number)
// (GetLatestBlockNumber) — monotonic by construction, and skeleton header
// rows cannot regress it. The explicit marker is still maintained through
// this choke point because (a) callers gate on the "did it move" result and
// (b) the marker records the DATA-COMPLETE tip (writers call it only after
// full-block writes — headers-only writers never do, preserving F6's
// "skeletons never advance it").
//
// CONCURRENCY: live block processing, DataSync workers, and catchup all
// write from this process; the mutex serializes the read-decide-write cycle
// (same pattern and rationale as the applied-anchor mutex in sync_anchor.go).

package DB_OPs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gossipnode/DB_OPs/thebegateway"

	"github.com/ethereum/go-ethereum/common"
)

var latestBlockMu sync.Mutex

// cachedTip mirrors the data-complete tip marker in memory (0 = not yet known
// in this process). Updated on every read and advance through
// UpdateLatestBlockMonotonic. Read lock-free by CachedCommittedTip for hot
// paths (committee pool resolution) that must not touch the DB per call.
var cachedTip atomic.Uint64

// CachedCommittedTip returns this process's last known data-complete tip and
// whether one is known yet. It never touches the DB; callers that get
// known=false may fall back to GetLatestBlockNumber once.
func CachedCommittedTip() (tip uint64, known bool) {
	v := cachedTip.Load()
	return v, v != 0
}

// noteTip raises cachedTip monotonically.
func noteTip(v uint64) {
	for {
		cur := cachedTip.Load()
		if v <= cur || cachedTip.CompareAndSwap(cur, v) {
			return
		}
	}
}

// onAdvance, guarded by latestBlockMu, is fired (under the lock) whenever the
// marker actually advances — i.e. this node just committed a new block's state
// (balances). It is injected from main to push an immediate seednode
// block-state report right after apply, instead of waiting for the periodic
// sync-monitor tick. Injection (rather than importing syncmonitor/seednode here)
// avoids an import cycle.
//
// CONTRACT: the hook MUST be non-blocking and MUST NOT call back into DB_OPs —
// it runs while latestBlockMu is held, so any blocking or re-entrancy stalls all
// block application. Production wraps it in a debounced, fire-and-forget pusher
// (startSeedBlockHeadPusher) that only sets a flag and returns.
var onAdvance func(uint64)

// SetLatestBlockAdvanceHook installs the marker-advance hook (nil clears it).
// Call once at startup, before block application begins. Read and write of the
// hook are both serialized by latestBlockMu, so this is race-safe against a
// concurrent UpdateLatestBlockMonotonic.
func SetLatestBlockAdvanceHook(fn func(uint64)) {
	latestBlockMu.Lock()
	defer latestBlockMu.Unlock()
	onAdvance = fn
}

// LatestBlockMarkerKey is the sync-state key holding the data-complete tip.
const LatestBlockMarkerKey = "latest_block"

// nextLatestBlock is the pure monotonic decision (unit-tested).
func nextLatestBlock(current, candidate uint64) (uint64, bool) {
	if candidate > current {
		return candidate, true
	}
	return current, false
}

// UpdateLatestBlockMonotonic advances the latest_block marker to blockNumber
// iff it is greater than the stored value. Returns the resulting marker value
// and whether it moved. All latest_block writes MUST go through here.
func UpdateLatestBlockMonotonic(blockNumber uint64) (uint64, bool, error) {
	latestBlockMu.Lock()
	defer latestBlockMu.Unlock()

	h, err := getHandle(nil)
	if err != nil {
		return 0, false, fmt.Errorf("latest_block: %w", err)
	}

	var current uint64
	if raw, err := h.GetSyncKV(LatestBlockMarkerKey); err != nil {
		return 0, false, fmt.Errorf("latest_block read: %w", err)
	} else if raw != nil {
		if v, perr := strconv.ParseUint(string(raw), 10, 64); perr == nil {
			current = v
		}
	}

	noteTip(current)
	next, moved := nextLatestBlock(current, blockNumber)
	if !moved {
		return current, false, nil
	}
	if err := h.PutSyncKV(LatestBlockMarkerKey, []byte(strconv.FormatUint(next, 10))); err != nil {
		return current, false, fmt.Errorf("latest_block write: %w", err)
	}
	noteTip(next)
	// Marker advanced: a new block's state is committed. Fire the (non-blocking)
	// advance hook so the node can push its fresh head to the seednode now rather
	// than at the next periodic tick. Held under latestBlockMu by contract.
	if onAdvance != nil {
		onAdvance(next)
	}
	return next, true, nil
}

// GetLatestDataCompleteBlock returns the DATA-COMPLETE tip: the latest_block
// marker, which is advanced ONLY after a block's full apply+store succeeds
// (UpdateLatestBlockMonotonic). It returns 0 when the marker is unset (fresh node).
//
// D-858 (review B1): this is DELIBERATELY different from GetLatestBlockNumber, which
// reads SQL MAX(block_number). StoreZKBlock is a non-atomic chain of gateway writes
// (block → snapshot → [zkproof] → transactions); a WriteTransaction projection
// failure (the uq_txn_block_index 858 trigger) leaves the `blocks` row already
// committed, so MAX(block_number) advances to a height whose state was rolled back
// and never applied. The duplicate-height ingress gates and the vote chain-position
// gate MUST read the data-complete marker, not MAX — otherwise a skeleton row from a
// failed store makes them treat a never-applied height as committed: the ingress
// gates then reject re-proposal of that height PERMANENTLY (no TTL), and the vote
// gate links N+1 against a block nobody applied.
func GetLatestDataCompleteBlock() (uint64, error) {
	h, err := getHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("GetLatestDataCompleteBlock: %w", err)
	}
	raw, err := h.GetSyncKV(LatestBlockMarkerKey)
	if err != nil {
		return 0, fmt.Errorf("GetLatestDataCompleteBlock: %w", err)
	}
	var marker uint64
	if raw != nil {
		v, perr := strconv.ParseUint(string(raw), 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("GetLatestDataCompleteBlock: parse marker %q: %w", string(raw), perr)
		}
		marker = v
	}

	// D-858 review B4: the latest_block marker write is NON-FATAL
	// (UpdateLatestBlockMonotonic failures are logged and swallowed on all apply
	// paths), so the marker can transiently LAG the truly-applied state. With the
	// zero-tolerance vote gate that would falsely reject the next height as
	// non-contiguous until reconcile heals it. The applied-anchor
	// (AdvanceAppliedAnchorContiguous) is a second, independent "highest contiguously
	// applied block" signal; take the MAX of the two so a lagging marker cannot cause
	// a false reject. This is a tighter lower bound, NOT a tolerance window — it never
	// admits a height ABOVE the applied tip, so the gap guard (reject N != tip+1 /
	// N <= tip) is preserved and the 844-without-843 class stays closed. A missing
	// anchor (never seeded) contributes 0.
	tip := marker
	if anchor, ok, aerr := GetAppliedAnchor(nil); aerr == nil && ok && anchor > tip {
		tip = anchor
	}
	// Both writes can fail after a block was stored and applied; count such
	// blocks too (repairTipForward, below).
	return repairTipForward(tip), nil
}

// Tip repair: the latest_block marker and the applied anchor are written after
// a block is stored, and both writes only log on failure. When both fail, the
// block is stored and applied but the data-complete tip still reads the height
// below it. A sequencer then proposes that height again, every validator
// refuses it as already committed, and the chain stops until the marker is
// set by hand.
//
// GetLatestDataCompleteBlock therefore looks one block past the recorded tip.
// It counts block tip+1 as committed only when all of these hold:
//
//   - its parent hash is the recorded tip's block hash;
//   - it has at least one transaction, and its stored transactions hash to its
//     TxnsRoot, so it is not a header-only row or a partly written store;
//   - every one of those transactions carries an applied tx_processed marker.
//     Markers commit together with the balances, and a failed store revokes
//     them, so once no apply of the block is running they are present only
//     when the block was applied and kept;
//   - no apply of that height is running, or started while it was checked
//     (BeginBlockApply).
//
// It then records the repaired height (best effort) and repeats, up to
// tipRepairMaxSteps blocks. A block with no transactions is never repaired:
// nothing on it proves it was applied rather than written by header sync.

const tipRepairMaxSteps = 8

// blockApplies tracks block applies by height. A transaction marker is written
// as each transaction applies, before the block's store, and a failed store
// revokes it again. While an apply of a height is running, or if one started
// while the repair was reading, its markers prove nothing, so the repair does
// not count that height.
var blockApplies = struct {
	sync.Mutex
	running map[uint64]int
	started map[uint64]uint64
}{running: map[uint64]int{}, started: map[uint64]uint64{}}

// BeginBlockApply registers an apply of the block at height n, before any of
// its transactions are applied. Call the returned function when the apply
// returns, whatever the outcome.
func BeginBlockApply(n uint64) (end func()) {
	blockApplies.Lock()
	blockApplies.running[n]++
	blockApplies.started[n]++
	blockApplies.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			blockApplies.Lock()
			if blockApplies.running[n]--; blockApplies.running[n] <= 0 {
				delete(blockApplies.running, n)
			}
			blockApplies.Unlock()
		})
	}
}

// blockApplyState reports whether an apply of height n is running and how
// many have started.
func blockApplyState(n uint64) (running bool, started uint64) {
	blockApplies.Lock()
	defer blockApplies.Unlock()
	return blockApplies.running[n] > 0, blockApplies.started[n]
}

var (
	tipRepairHookMu sync.Mutex
	tipRepairHook   func(from, to uint64, recordErr error)
)

// SetTipRepairHook installs a callback that runs when the data-complete tip
// was found behind a stored, applied block. recordErr is non-nil when the
// repaired height could not be written back (the repair still applies to the
// returned tip). The hook must not block or call back into DB_OPs.
func SetTipRepairHook(fn func(from, to uint64, recordErr error)) {
	tipRepairHookMu.Lock()
	defer tipRepairHookMu.Unlock()
	tipRepairHook = fn
}

func fireTipRepairHook(from, to uint64, recordErr error) {
	tipRepairHookMu.Lock()
	fn := tipRepairHook
	tipRepairHookMu.Unlock()
	if fn != nil {
		fn(from, to, recordErr)
	}
}

// TxnsRootOfHashes is the block TxnsRoot: a SHA-256 binary Merkle tree over
// the transaction hashes in block order, an odd level duplicating its last
// node, "0x"-prefixed hex. It matches the block generator.
func TxnsRootOfHashes(hashes []common.Hash) string {
	if len(hashes) == 0 {
		return "0x" + strings.Repeat("0", 64)
	}
	level := make([][]byte, len(hashes))
	for i := range hashes {
		level[i] = hashes[i].Bytes()
	}
	if len(level) == 1 {
		combined := make([]byte, 0, 64)
		combined = append(combined, level[0]...)
		combined = append(combined, level[0]...)
		s := sha256.Sum256(combined)
		return "0x" + hex.EncodeToString(s[:])
	}
	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		next := make([][]byte, 0, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			combined := make([]byte, 0, 64)
			combined = append(combined, level[i]...)
			combined = append(combined, level[i+1]...)
			s := sha256.Sum256(combined)
			next = append(next, s[:])
		}
		level = next
	}
	return "0x" + hex.EncodeToString(level[0])
}

// repairTipForward returns the highest block at or above tip that is stored
// and applied, walking at most tipRepairMaxSteps blocks past tip. Any read
// error or failed check stops the walk at the last proven height.
func repairTipForward(tip uint64) uint64 {
	if tip == 0 {
		return 0
	}
	h, err := getHandle(nil)
	if err != nil {
		return tip
	}
	start := tip
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var parent *thebegateway.BlockRecord
	for i := 0; i < tipRepairMaxSteps; i++ {
		next, err := h.GetBlock(ctx, tip+1)
		if err != nil || next == nil {
			break // the usual case: nothing stored above the tip
		}
		if parent == nil {
			if parent, err = h.GetBlock(ctx, tip); err != nil || parent == nil {
				break
			}
		}
		if !strings.EqualFold(next.ParentHash, parent.BlockHash) || next.TxsRoot == "" {
			break
		}
		running, startedBefore := blockApplyState(tip + 1)
		if running {
			break
		}
		recs, err := h.GetTransactionsByBlock(ctx, tip+1)
		if err != nil || len(recs) == 0 {
			break
		}
		sort.SliceStable(recs, func(a, b int) bool { return recs[a].TxIndex < recs[b].TxIndex })
		hashes := make([]common.Hash, 0, len(recs))
		hexes := make([]string, 0, len(recs))
		for _, r := range recs {
			hh := common.HexToHash(r.TxHash)
			hashes = append(hashes, hh)
			hexes = append(hexes, hh.Hex())
		}
		if !strings.EqualFold(TxnsRootOfHashes(hashes), next.TxsRoot) {
			break
		}
		applied, err := FilterProcessedTxMarkers(hexes)
		if err != nil {
			break
		}
		all := true
		for _, x := range hexes {
			if !applied[x] {
				all = false
				break
			}
		}
		if !all {
			break
		}
		// An apply that started after the first check may have written these
		// markers and be about to revoke them.
		if running, startedAfter := blockApplyState(tip + 1); running || startedAfter != startedBefore {
			break
		}
		tip++
		parent = next
	}
	if tip > start {
		_, _, werr := UpdateLatestBlockMonotonic(tip)
		fireTipRepairHook(start, tip, werr)
	}
	return tip
}
