// MODULE: DB_OPs/sync_anchor
// PURPOSE: The accounts-applied anchor — the single honest watermark for
//          account state. ThebeDB retarget of the F3/F5 module.
//
// SEMANTICS (unchanged): anchor = N means "the account effects of every block
// ≤ N have been applied exactly once". Two writers advance it:
//   - live block processing: contiguously only (block == anchor+1), after the
//     block's marker commit succeeds;
//   - reconciliation: to the verified range end, only after post-sync
//     verification passes with zero failed accounts.
//
// SAFETY DIRECTION (unchanged): an anchor that is TOO LOW is safe — recon
// re-covers the range and per-tx markers exclude already-applied txs. An
// anchor that is TOO HIGH permanently and silently skips account effects.
// Every writer rounds DOWN under uncertainty; every target is capped at the
// locally verified tip (CapAnchorTarget — MaxUint64 poison guard).
//
// STORAGE (ThebeDB): the anchor lives in the BadgerDB sync-state KV — the same
// store as the canonical log AND the tx markers, so a volume restore rolls
// anchor + markers + log back together (I3); the SQL projection (balances) is
// rebuilt from that same log.
//
// CONCURRENCY: appliedAnchorMu serializes the read-modify-write between the
// two writers (both run in this process). This is NOT optional — see the F3
// module history: an unlocked interleaving regresses the anchor and re-opens
// a range containing recon-applied txs, which carry no tx_processed markers.
//
// DO NOT:
//   - Write this key through BatchRestoreAccounts — it is not an account
//     object and must never pass through LWW/merge machinery.
//   - Advance the anchor from any path that has not proven full application.
//   - Seed or advance past the local tip (CapAnchorTarget).

package DB_OPs

import (
	"encoding/json"
	"fmt"
	"sync"

	"gossipnode/config"
)

// AppliedAnchorKey is the sync-state key holding the anchor (uint64 as JSON).
// The "sync:" prefix keeps it outside the address:/did: namespaces that
// BatchRestoreAccounts routes through account-merge logic.
const AppliedAnchorKey = "sync:accounts_last_applied_block"

// appliedAnchorMu serializes all anchor read-modify-write cycles — see the
// CONCURRENCY section of the module header for why this is load-bearing.
var appliedAnchorMu sync.Mutex

// ─── Pure decision rules (unit-tested in sync_anchor_test.go) ────────────────

// NextLiveAnchor is the live path's advancement rule: strictly contiguous.
// Returns (newAnchor, true) only when block is exactly anchor+1. A gap means
// blocks below are missing (downtime) — advancing would silently skip their
// effects; reconciliation owns gap filling.
func NextLiveAnchor(current, block uint64) (uint64, bool) {
	if block == current+1 {
		return block, true
	}
	return current, false
}

// NextReconAnchor is reconciliation's advancement rule: monotonic max.
// Never moves the anchor backwards.
func NextReconAnchor(current, target uint64) (uint64, bool) {
	if target > current {
		return target, true
	}
	return current, false
}

// ShouldAdvanceReconAnchor gates reconciliation's anchor writes. ALL must hold:
// no reconciliation error, zero failed accounts, and the post-sync verification
// (buildDataMissingTag empty) passed. Anything less and the range is not proven
// applied — advancing would claim effects were applied when they may not have
// been (failed accounts, or skeleton blocks passing as complete).
func ShouldAdvanceReconAnchor(reconErr error, failedAccounts int, verifyPassed bool) bool {
	return reconErr == nil && failedAccounts == 0 && verifyPassed
}

// CapAnchorTarget bounds any anchor target (advance OR legacy seed) at the
// locally verified tip. Two real-world poison sources this neutralizes:
//   - HandleSync substitutes math.MaxUint64 when a legacy peer reports
//     BlockHeight 0 — the legacy SQLite code persisted it, permanently
//     disabling reconciliation;
//   - nodes carrying that poisoned SQLite value would otherwise import it
//     into the new anchor via the migration seed.
func CapAnchorTarget(target, tip uint64) uint64 {
	if target > tip {
		return tip
	}
	return target
}

// ─── Storage (ThebeDB sync-state KV) ─────────────────────────────────────────

// readAnchorLocked reads the anchor. Caller holds appliedAnchorMu.
func readAnchorLocked() (uint64, bool, error) {
	h, err := getHandle(nil)
	if err != nil {
		return 0, false, fmt.Errorf("applied anchor read: %w", err)
	}
	raw, err := h.GetSyncKV(AppliedAnchorKey)
	if err != nil {
		return 0, false, fmt.Errorf("applied anchor read: %w", err)
	}
	if raw == nil {
		return 0, false, nil
	}
	var v uint64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, fmt.Errorf("applied anchor decode: %w", err)
	}
	return v, true, nil
}

// writeAnchorLocked writes the anchor. Caller holds appliedAnchorMu.
func writeAnchorLocked(value uint64) error {
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("applied anchor write: %w", err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("applied anchor encode: %w", err)
	}
	if err := h.PutSyncKV(AppliedAnchorKey, raw); err != nil {
		return fmt.Errorf("applied anchor write: %w", err)
	}
	return nil
}

// GetAppliedAnchor reads the current anchor. found=false means never seeded.
func GetAppliedAnchor(_ *config.PooledConnection) (uint64, bool, error) {
	appliedAnchorMu.Lock()
	defer appliedAnchorMu.Unlock()
	return readAnchorLocked()
}

// AdvanceAppliedAnchorContiguous applies the live path rule: advance iff
// block == anchor+1. Returns the anchor after the call and whether it moved.
// Never returns an error that should fail block processing — the anchor
// lagging is safe (see SAFETY DIRECTION); callers log and continue.
func AdvanceAppliedAnchorContiguous(_ *config.PooledConnection, block uint64) (uint64, bool, error) {
	return advanceAnchor(block, true)
}

// AdvanceAppliedAnchorTo applies reconciliation's rule: monotonic max to
// target. Returns the anchor after the call and whether it moved.
func AdvanceAppliedAnchorTo(_ *config.PooledConnection, target uint64) (uint64, bool, error) {
	return advanceAnchor(target, false)
}

// advanceAnchor is the single locked read-decide-write cycle for both writers.
func advanceAnchor(target uint64, contiguous bool) (uint64, bool, error) {
	appliedAnchorMu.Lock()
	defer appliedAnchorMu.Unlock()

	current, _, err := readAnchorLocked()
	if err != nil {
		return 0, false, err
	}

	var next uint64
	var advance bool
	if contiguous {
		next, advance = NextLiveAnchor(current, target)
	} else {
		next, advance = NextReconAnchor(current, target)
	}
	if !advance {
		return current, false, nil
	}
	if err := writeAnchorLocked(next); err != nil {
		return current, false, err
	}
	return next, true, nil
}

// SeedAppliedAnchor writes the anchor ONLY if the key does not exist yet.
// Migration path: older nodes tracked reconciliation in SQLite
// (fastsync:last_reconciled_block); reconciliation-applied txs carry NO
// tx_processed markers, so re-reconciling that range would double-apply.
// Seeding from the legacy value trades a bounded, repairable risk (the legacy
// value may be dishonestly HIGH → some effects stay missing until the repair
// job runs) against guaranteed corruption (systematic double-apply).
//
// CALLERS MUST CAP legacy via CapAnchorTarget(legacy, localTip) first — the
// legacy SQLite value can carry the MaxUint64 poison. Returns whether a seed
// write happened.
func SeedAppliedAnchor(_ *config.PooledConnection, legacy uint64) (bool, error) {
	if legacy == 0 {
		return false, nil
	}

	appliedAnchorMu.Lock()
	defer appliedAnchorMu.Unlock()

	_, found, err := readAnchorLocked()
	if err != nil {
		return false, err
	}
	if found {
		return false, nil
	}
	if err := writeAnchorLocked(legacy); err != nil {
		return false, err
	}
	return true, nil
}
