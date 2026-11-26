package DB_OPs

import (
	"sync"
	"time"
)

// blockApplyGate spans one WHOLE block apply (BlockProcessing's
// processBlockTransactions): every transaction, the rollback, the
// post-apply checks and any apply-halt record written on the way. Block
// applies hold it shared (EnterBlockApply / ExitBlockApply), so they run
// concurrently with each other exactly as before. CloseThebeDBGated takes it
// exclusively, so ThebeDB is never closed in the middle of a block apply, and
// no block apply can start after the close.
//
// The state-apply lock (LockStateApply) is held per commit, not per block,
// and several halt paths (contract-state halts, abandoned rollbacks) run
// outside it; this gate covers them.
//
// Lock order: blockApplyGate, then the state-apply lock. Block apply takes
// them in that order, and so does CloseThebeDBGated.
var blockApplyGate sync.RWMutex

// EnterBlockApply marks the start of a block apply. Pair with ExitBlockApply.
// After CloseThebeDBGated has taken the gate it blocks until the process
// exits, so no apply starts against a closed database.
func EnterBlockApply() { blockApplyGate.RLock() }

// ExitBlockApply marks the end of a block apply.
func ExitBlockApply() { blockApplyGate.RUnlock() }

// tryCloseBlockApplyWithin tries to take the gate exclusively for up to wait.
// TryLock, not Lock: a waiting writer would block new readers, and a block
// apply that nests a second EnterBlockApply would then deadlock. Polling
// never registers as a waiting writer.
func tryCloseBlockApplyWithin(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if blockApplyGate.TryLock() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}
