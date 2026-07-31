// MODULE: DB_OPs/state_apply_lock
// PURPOSE: Process-wide mutual exclusion between the two account-state
//          writers: live block execution and reconciliation apply.
//
// INVARIANT: a balance write is correct only if no other writer commits to
// the same account between this writer's base read and its commit. Live
// execution and reconciliation both run read→modify→commit cycles on account
// documents; this mutex makes each cycle atomic with respect to the other
// writer. Both writers run in this process, so in-process locking is
// sufficient — no distributed coordination needed.
//
// Holders:
//   - live path: messaging/BlockProcessing processTransaction (marker check →
//     stage reads → ApplyTxAtomic commit) and the revoke+rollback pair;
//   - recon path: ApplyBlockRecon (marker filter → base reads → delta commit).
//
// RULES:
//   - Acquire ONLY around a complete read→decide→commit cycle. Never hold
//     across network calls unrelated to the commit.
//   - Never nest: none of the holders call each other.
//   - Lock hold time is bounded by one ImmuDB ExecAll (worst case ~15 s under
//     load). Both paths were already DB-serialized in practice; consistency
//     is worth the serialization.

package DB_OPs

import "sync"

var stateApplyMu sync.Mutex

// LockStateApply acquires the global state-application lock.
func LockStateApply() { stateApplyMu.Lock() }

// UnlockStateApply releases the global state-application lock.
func UnlockStateApply() { stateApplyMu.Unlock() }
