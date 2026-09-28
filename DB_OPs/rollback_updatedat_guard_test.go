package DB_OPs

// LANDING GUARD (ticket: docs/audit/STO-ACCOUNT-UPDATEDAT-CLOBBER.md)
//
// storeAccount and StorePropagatedAccount currently clobber the caller's
// updated_at with wall-clock now(). That is a rollback-safety trap: restoring an
// older account snapshot through storeAccount would stamp the restored row with
// a NEWER updated_at than the state it represents, so any "higher updated_at
// wins" read path would silently prefer the rolled-back-over value.
//
// This test is the guard the reviewer asked to land BEFORE anyone changes
// storeAccount to honor the caller's timestamp. It is intentionally skipped
// today because (a) there is no account snapshot-restore primitive in the tree
// yet and (b) it needs a live ThebeDB handle. When the fix is implemented, an
// engineer MUST flesh this out (removing the Skip) so it FAILS against the
// current clobber and PASSES only once updated_at is preserved.
//
// Do NOT delete or weaken this test to make a storeAccount change go green —
// implement the assertion instead. Do NOT run the §10.1 recovery UPDATE in the
// ticket until the RCA is re-derived; run the read-only SELECT check first.

import (
	"testing"
	"time"
)

// storeAccountGuard keeps this guard compile-coupled to storeAccount's signature:
// a change to storeAccount that this file does not follow will not build, forcing
// whoever edits storeAccount to see this ticket.
var storeAccountGuard = storeAccount

func TestRollbackState_RestoresOlderSnapshot(t *testing.T) {
	_ = storeAccountGuard // reference the pinned symbol

	t.Skip("LANDING GUARD (docs/audit/STO-ACCOUNT-UPDATEDAT-CLOBBER.md): " +
		"implement before changing storeAccount to honor caller updated_at. " +
		"Required assertion contract below.")

	// ---- Assertion contract the implementer must realize ----
	//
	//  t1 := <timestamp of an OLDER snapshot state>
	//  t2 := <timestamp of a NEWER state>, with t1 < t2
	//
	//  1. storeAccount(conn, acct@state2 with UpdatedAt=t2)   // apply newer state
	//  2. rollback/restore acct to state1 (UpdatedAt=t1)      // restore older snapshot
	//  3. got := GetAccount(conn, acct.Address)
	//
	//  require: got.UpdatedAt == t1          // NOT time.Now(), NOT t2
	//  require: got.Balance   == state1.Balance
	//
	// Against today's storeAccount, step 2 stamps UpdatedAt=now() (>= t2 > t1),
	// so the require on got.UpdatedAt FAILS — which is exactly what this guard
	// must catch. Preserving the caller's timestamp makes it PASS.
	_ = time.Now
}
