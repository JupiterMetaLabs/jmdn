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
// SCOPE — READ THIS BEFORE RELYING ON THE INVARIANT ABOVE.
// This mutex serializes the two AUTHORITATIVE writers against each other. It
// does NOT serialize every writer of an `address:` record. Four paths write
// those records; two take this lock and two do not:
//
//	TAKES THE LOCK
//	  live path   messaging/BlockProcessing processTransaction (marker check →
//	              stage reads → ApplyTxAtomic commit), and the revoke+rollback pair
//	  recon path  ApplyBlockRecon (marker filter → base reads → delta commit)
//
//	DOES NOT TAKE THE LOCK
//	  sync path   BatchRestoreAccounts, driven by the Redis drain worker and by
//	              writeAccountsDirect when Redis is unavailable
//	  DID path    StorePropagatedAccount and the DID-propagation writes
//
// What actually stops the unlocked writers from destroying an authoritative
// balance is NOT this mutex — it is mergeAccountForWrite's last-writer-wins
// comparison on UpdatedAt, plus the zero-balance guard (isZeroBalanceString).
// Every unlocked write goes through that merge; the two locked writers bypass
// it and commit raw KV.
//
// The practical consequence: balance safety against the sync path depends on
// UpdatedAt ordering being correct. That is a weaker guarantee than mutual
// exclusion, and it is the same mechanism that failed once before (UpdatedAt
// stored in seconds while sync wrote nanoseconds, so every sync write won).
// Do not read the INVARIANT paragraph as "no concurrent writer is possible".
//
// Making BatchRestoreAccounts take this lock has been checked and is
// deadlock-free — no current holder reaches it. It is deliberately NOT done
// yet: BatchRestoreAccounts performs a GetAll plus a chunked ExecAll over a
// whole batch, so putting it under this mutex would extend hold time
// significantly, and the contention cost of the CURRENT holders has not been
// measured under load. Measure first, then decide.
//
// RULES:
//   - Acquire ONLY around a complete read→decide→commit cycle. Never hold
//     across network calls unrelated to the commit.
//   - Never nest: none of the holders call each other. This is a plain
//     sync.Mutex and is NOT reentrant — a second acquire on the same goroutine
//     deadlocks. Verify the call graph before adding a holder.
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
