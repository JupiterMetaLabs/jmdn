package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"time"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
)

// ErrThebeCloseSkipped is returned when CloseThebeDBGated left ThebeDB open
// because a writer was still running.
var ErrThebeCloseSkipped = errors.New("thebedb close skipped: a write is still in flight; leaving the database open")

// CloseThebeDBGated closes ThebeDB only when no state write can be in flight,
// and holds that guarantee until the process exits.
//
// Why: ThebeDB commits a write as KV prepared -> SQL committed -> KV committed
// (builder.Append), and Close closes Badger before the SQL pool. A write still
// running when Close starts can commit to SQL and then fail on KV, leaving SQL
// ahead of the canonical log. Closing under a block apply or rollback also
// runs its error paths against a closing store (the apply-halt record is then
// lost). Before graceful shutdown existed the process was killed instead, and
// in-flight work finished with the database open.
//
// It first takes, within wait and in this order:
//  1. the block-apply gate (EnterBlockApply), which every block apply holds
//     for its whole duration, including its rollback and any apply-halt
//     record it writes;
//  2. the state-apply lock (LockStateApply), which reconciliation and each
//     block-apply commit hold; and
//  3. ThebeDB's append slot (KVPool, width 1), which every 2PC append holds.
//
// All three are kept for the rest of the process, so no block apply, rollback,
// reconciliation or append can start after the close. If either cannot be taken in time, it does NOT
// close and returns ErrThebeCloseSkipped: the database is left open, which is
// how every stop ended before (SIGKILL), and Badger recovers on the next start.
func CloseThebeDBGated(db *thebedb.ThebeDB, wait time.Duration) error {
	if db == nil {
		return nil
	}
	var acquireAppend func(context.Context) error
	if db.KVPool != nil {
		acquireAppend = func(ctx context.Context) error {
			_, _, err := db.KVPool.Acquire(ctx) // never released: held until exit
			return err
		}
	}
	return closeGated(tryCloseBlockApplyWithin, TryLockStateApplyWithin, acquireAppend, db.Close, wait)
}

// closeGated is CloseThebeDBGated's logic with its dependencies injected, for
// tests. All waits share one budget, wait. acquireAppend may be nil (no append
// slot to take).
func closeGated(lockBlockApply, lockApply func(time.Duration) bool, acquireAppend func(context.Context) error, closeFn func() error, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	if !lockBlockApply(wait) {
		return fmt.Errorf("%w (a block apply is still running after %s)", ErrThebeCloseSkipped, wait)
	}
	// The block-apply gate is now held and is not released: no block apply
	// may start after this point.
	if !lockApply(time.Until(deadline)) {
		return fmt.Errorf("%w (state-apply lock busy)", ErrThebeCloseSkipped)
	}
	// The state-apply lock is now held and is not released: no apply or
	// rollback may start after this point.
	if acquireAppend != nil {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		if err := acquireAppend(ctx); err != nil {
			return fmt.Errorf("%w (append slot busy: %v)", ErrThebeCloseSkipped, err)
		}
	}
	return closeFn()
}
