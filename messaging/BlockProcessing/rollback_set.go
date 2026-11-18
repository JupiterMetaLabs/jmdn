package BlockProcessing

// Rollback bookkeeping for one block apply.
//
// A failed block is rolled back to the snapshot taken before its first
// transaction. The snapshot starts with the accounts the block names (senders,
// recipients, coinbase, zkVM, fee recipients, see affectedAccountsForBlock), but
// a contract transaction also moves value to accounts only its execution knows
// (internal CALL{value} targets, SELFDESTRUCT beneficiaries, deployed
// contracts). Each of those is added here, with its state as stored, before the
// contract transaction that touches it commits. The first capture of an address
// wins, so every snapshot is the address's state before the block.
//
// An account that did not exist before the block is recorded as Absent, and the
// rollback deletes it instead of writing a zero balance: a zero-balance row is
// still an account, and the state fingerprint folds every account.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

type rollbackSet struct {
	mu    sync.Mutex
	snaps map[common.Address]AccountSnapshot
}

type rollbackSetKey struct{}

func withRollbackSet(ctx context.Context, rs *rollbackSet) context.Context {
	return context.WithValue(ctx, rollbackSetKey{}, rs)
}

func rollbackSetFrom(ctx context.Context) *rollbackSet {
	rs, _ := ctx.Value(rollbackSetKey{}).(*rollbackSet)
	return rs
}

// snapshotAccount reads addr's stored state as a rollback snapshot. A missing
// account is Absent. Any other read error is returned: it is not proof the
// account is absent, and a wrong Absent would delete a real account.
func snapshotAccount(conn *config.PooledConnection, addr common.Address) (AccountSnapshot, error) {
	doc, err := DB_OPs.GetAccount(conn, addr)
	switch {
	case err == nil && doc != nil:
		return accountSnapshotOf(doc), nil
	case err == nil || DB_OPs.IsNotFound(err):
		return AccountSnapshot{Balance: "0", Absent: true}, nil
	default:
		return AccountSnapshot{}, err
	}
}

// recordRollbackTargets adds every account a contract transaction is about to
// write and the block's snapshot does not cover yet. It must run before the
// transaction commits anything (contract storage or accounts), so the stored
// state it reads is the state before the block. Outside a block apply (no set
// in ctx) it does nothing.
func recordRollbackTargets(ctx context.Context, conn *config.PooledConnection, addrs []common.Address) error {
	rs := rollbackSetFrom(ctx)
	if rs == nil {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, a := range addrs {
		if _, ok := rs.snaps[a]; ok {
			continue
		}
		snap, err := snapshotAccount(conn, a)
		if err != nil {
			return fmt.Errorf("rollback snapshot of %s: %w", a.Hex(), err)
		}
		rs.snaps[a] = snap
	}
	return nil
}

// ErrApplyHalted is returned for every block once this node has refused an
// incomplete rollback: its state holds a block's partial effects and must be
// resynced, so nothing may be applied on top of it.
var ErrApplyHalted = errors.New("block apply halted: state holds a partially applied block and must be resynced")

var applyHaltReason atomic.Pointer[string]

// haltApply latches block apply off for the life of the process.
func haltApply(reason string) {
	applyHaltReason.CompareAndSwap(nil, &reason)
}

// ApplyHaltReason reports why block apply is halted, or "" when it is not.
func ApplyHaltReason() string {
	if r := applyHaltReason.Load(); r != nil {
		return *r
	}
	return ""
}

// refuseIncompleteRollback decides a rollback for a block that may have
// committed contract state. Contract storage has no undo, so once a contract
// transaction of this block succeeded a rollback would restore the accounts and
// leave the storage changed: a state no other node has. In that case the
// rollback is refused, block apply is halted on this node, and the returned
// error says so. Otherwise it returns nil and the caller rolls back.
func refuseIncompleteRollback(blockHash common.Hash, blockNumber uint64, cause error) error {
	if !didCommitContractState(blockHash) {
		return nil
	}
	reason := fmt.Sprintf("block %d (%s) failed after committing contract state: %v", blockNumber, blockHash.Hex(), cause)
	haltApply(reason)
	return fmt.Errorf("%w: %s — rollback refused, resync required", ErrApplyHalted, reason)
}
