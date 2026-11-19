package BlockProcessing

// Rollback of a failed block, and the block-apply halt.
//
// A failed block is rolled back to the snapshot taken before its first
// transaction (affectedAccountsForBlock: senders, recipients, coinbase, zkVM,
// fee recipients). An account that did not exist before the block is recorded
// as Absent and the rollback deletes it instead of writing a zero balance: a
// zero-balance row is still an account, and the state fingerprint folds every
// account.
//
// A rollback runs in two steps under the state-apply lock:
//
//  1. planRollback reads every snapshotted account and decides the writes. It
//     writes nothing. If a read fails, the caller abandons the rollback before
//     revoking any tx marker, so the block's applied prefix stays applied AND
//     marked: a consistent state that a replay continues from.
//  2. After the markers are revoked, restoreAfterRevoke performs the writes. If
//     one fails, the state is neither the old one nor the new one, and nothing
//     on this node may build on it: block apply halts (see below).
//
// Contract storage has no undo. Once a contract transaction of a block has
// succeeded, no rollback of that block can be complete, so it is refused and
// block apply halts (refuseIncompleteRollback). That refusal is also why the
// snapshot does not need the accounts only a contract's execution touches
// (internal CALL{value} targets, SELFDESTRUCT beneficiaries, deployed
// contracts): they are written only by a successful contract transaction. If
// contract storage ever gets an undo and the refusal is relaxed, those
// accounts must be added to the snapshot first.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

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

// rollbackPlan is the writes that return the snapshotted accounts to their
// state before the block, in address order.
type rollbackPlan struct {
	deletes  []common.Address
	restores []*DB_OPs.Account
}

// planRollback reads every snapshotted account and computes the rollback's
// writes. It writes nothing, so an error leaves the state untouched.
func planRollback(snapshots map[common.Address]AccountSnapshot, conn *config.PooledConnection) (*rollbackPlan, error) {
	addrs := make([]common.Address, 0, len(snapshots))
	for a := range snapshots {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i][:], addrs[j][:]) < 0 })

	plan := &rollbackPlan{}
	for _, addr := range addrs {
		snap := snapshots[addr]
		doc, err := DB_OPs.GetAccount(conn, addr)
		missing := doc == nil || (err != nil && DB_OPs.IsNotFound(err))
		if err != nil && !DB_OPs.IsNotFound(err) {
			return nil, fmt.Errorf("rollback: read %s: %w", addr.Hex(), err)
		}
		if snap.Absent {
			// The block created this account: remove it.
			if !missing {
				plan.deletes = append(plan.deletes, addr)
			}
			continue
		}
		if missing {
			// Existed before the block but is gone now: rebuild it with the
			// identity it had.
			doc = &DB_OPs.Account{
				Address:     addr,
				DIDAddress:  snap.DIDAddress,
				AccountType: snap.AccountType,
				CreatedAt:   snap.CreatedAt,
				Metadata:    snap.Metadata,
			}
		}
		doc.Nonce = snap.Nonce
		doc.Balance = snap.Balance
		doc.TxNonce = snap.TxNonce
		doc.TxCountSent = snap.TxCountSent
		doc.UpdatedAt = snap.UpdatedAt
		plan.restores = append(plan.restores, doc)
	}
	return plan, nil
}

// executeRollback performs the plan's writes. None of them goes through the
// outbox: a write that fails is reported (and halts apply), never replayed
// later over whatever the node has done since.
func executeRollback(plan *rollbackPlan, _ *config.PooledConnection) error {
	for _, addr := range plan.deletes {
		if err := DB_OPs.DeleteAccount(addr); err != nil {
			return fmt.Errorf("rollback: delete %s created by the failed block: %w", addr.Hex(), err)
		}
	}
	for _, doc := range plan.restores {
		if err := DB_OPs.RestoreAccount(doc); err != nil {
			return fmt.Errorf("rollback: restore %s: %w", doc.Address.Hex(), err)
		}
	}
	return nil
}

// restoreAfterRevoke performs the plan once the block's tx markers are revoked.
// A failed write leaves state that is neither before nor after the block, under
// revoked markers, so block apply halts: a replay would apply the block again on
// top of whatever was not restored.
func restoreAfterRevoke(ctx context.Context, plan *rollbackPlan, conn *config.PooledConnection, blockHash common.Hash, blockNumber uint64) error {
	if err := executeRollback(plan, conn); err != nil {
		reason := fmt.Sprintf("block %d (%s): rollback could not be completed after its tx markers were revoked: %v", blockNumber, blockHash.Hex(), err)
		haltApply(ctx, reason)
		return fmt.Errorf("%w: %s", ErrApplyHalted, reason)
	}
	logger().Info(ctx, "Rolled back the failed block's accounts",
		ion.Uint64("block_number", blockNumber),
		ion.Int("restored", len(plan.restores)),
		ion.Int("deleted", len(plan.deletes)),
		ion.String("function", "BlockProcessing.restoreAfterRevoke"))
	return nil
}

// rollbackAbandoned decides what an abandoned rollback leaves: one whose reads,
// whose pending-apply-record drop, or whose marker revocation failed, so nothing
// was restored. Unless the block
// had a commit that failed part-way, every tx it applied is applied AND marked:
// consistent, and a replay continues from it (nil is returned). After a partial
// commit (DB_OPs.ErrPartialApply) some accounts hold effects that no marker
// records, which a replay would apply again: block apply halts.
func rollbackAbandoned(ctx context.Context, blockHash common.Hash, blockNumber uint64, cause, abandoned error) error {
	if !errors.Is(cause, DB_OPs.ErrPartialApply) && !errors.Is(abandoned, DB_OPs.ErrPartialRevoke) {
		return nil
	}
	reason := fmt.Sprintf("block %d (%s): a commit failed part-way (%v) and the rollback could not run (%v)", blockNumber, blockHash.Hex(), cause, abandoned)
	haltApply(ctx, reason)
	return fmt.Errorf("%w: %s", ErrApplyHalted, reason)
}

// ErrApplyHalted is returned for every block once this node holds state it
// could not roll back (a partially applied block). Nothing may be applied on top
// of it; the node must be resynced.
var ErrApplyHalted = errors.New("block apply halted: state holds a partially applied block and must be resynced")

// haltState is the block-apply halt. Every transition happens under the
// mutex, so a halt and a clear cannot interleave.
var haltState struct {
	sync.Mutex
	reason   string // "" = not halted
	loaded   bool   // the record of an earlier run has been read
	recorded bool   // reason is in the record, so a restart keeps it
}

// The halt is recorded in the node's store so a restart does not lift it.
// Seams for tests.
var (
	recordApplyHalt   = DB_OPs.RecordApplyHalt
	recordedApplyHalt = DB_OPs.RecordedApplyHalt
	eraseApplyHalt    = DB_OPs.ClearRecordedApplyHalt
)

// haltApply halts block apply on this node and records why. A record write that
// fails is retried by every later ApplyHaltStatus.
func haltApply(ctx context.Context, reason string) {
	haltState.Lock()
	defer haltState.Unlock()
	if haltState.reason != "" {
		return
	}
	haltState.reason, haltState.recorded = reason, false
	logger().Error(ctx, "BLOCK APPLY HALTED — this node holds a partially applied block and must be resynced",
		ErrApplyHalted, ion.String("reason", reason),
		ion.String("function", "BlockProcessing.haltApply"))
	if err := recordApplyHalt(reason); err != nil {
		logger().Error(ctx, "Block apply halt could not be recorded yet; retried on every block, a restart before it succeeds would lift it",
			err, ion.String("reason", reason),
			ion.String("function", "BlockProcessing.haltApply"))
		return
	}
	haltState.recorded = true
}

// ApplyHaltStatus reports why block apply is halted on this node ("" when it is
// not) and whether that halt is recorded, so that a restart keeps it. A halt
// recorded before a restart is loaded on first use, and an unrecorded halt is
// recorded again here. An error reading the record is returned; callers fail
// closed on it.
func ApplyHaltStatus() (reason string, recorded bool, err error) {
	haltState.Lock()
	defer haltState.Unlock()
	if !haltState.loaded {
		r, rerr := recordedApplyHalt()
		if rerr != nil {
			return "", false, fmt.Errorf("read the block-apply halt record: %w", rerr)
		}
		haltState.loaded = true
		if r != "" && haltState.reason == "" {
			haltState.reason, haltState.recorded = r, true
			logger().Error(context.Background(), "BLOCK APPLY HALTED — loaded the halt recorded before this start; this node must be resynced",
				ErrApplyHalted, ion.String("reason", r),
				ion.String("function", "BlockProcessing.ApplyHaltStatus"))
		}
	}
	if haltState.reason != "" && !haltState.recorded {
		if recordApplyHalt(haltState.reason) == nil {
			haltState.recorded = true
			logger().Warn(context.Background(), "Block apply halt recorded on retry; a restart now keeps it",
				ion.String("reason", haltState.reason),
				ion.String("function", "BlockProcessing.ApplyHaltStatus"))
		}
	}
	return haltState.reason, haltState.recorded, nil
}

// ApplyHaltReason reports why block apply is halted on this node, or "" when
// it is not (see ApplyHaltStatus).
func ApplyHaltReason() (string, error) {
	reason, _, err := ApplyHaltStatus()
	return reason, err
}

// ClearApplyHalt lifts the halt, erases its record and returns the reason it
// cleared ("" when nothing was halted). Only for an operator whose node's
// state is again the canonical chain's (after a resync). The record is read
// again on the next status check.
func ClearApplyHalt() (string, error) {
	haltState.Lock()
	defer haltState.Unlock()
	prev := haltState.reason
	if prev == "" && !haltState.loaded {
		if r, err := recordedApplyHalt(); err == nil {
			prev = r
		}
	}
	if err := eraseApplyHalt(); err != nil {
		return "", err
	}
	haltState.reason, haltState.recorded, haltState.loaded = "", false, false
	return prev, nil
}

// markersToRevoke lists the tx markers a rollback of an in-loop failure must
// revoke: every tx the block applied before the failing one, plus the failing
// tx itself when its OWN commit (ownCommit: the error came from its own
// processTransaction, not from flushing an earlier group) wrote its marker
// before failing (DB_OPs.ErrApplyRedoNotCleared). Any other failing tx wrote no
// marker of its own, and its hash is left alone: a marker under it may belong
// to another block's apply. A failed group flush lists its own txs in applied.
func markersToRevoke(applied []string, failingTx string, cause error, ownCommit bool) []string {
	if !ownCommit || !errors.Is(cause, DB_OPs.ErrApplyRedoNotCleared) {
		return applied
	}
	return append(append(make([]string, 0, len(applied)+1), applied...), failingTx)
}

// refuseIncompleteRollback decides a rollback for a block that may have
// committed contract state. Contract storage has no undo, so once a contract
// transaction of this block succeeded a rollback would restore the accounts and
// leave the storage changed: a state no other node has. In that case the
// rollback is refused, block apply halts on this node, and the returned error
// says so. Otherwise it returns nil and the caller rolls back.
func refuseIncompleteRollback(ctx context.Context, blockHash common.Hash, blockNumber uint64, cause error) error {
	if !didCommitContractState(blockHash) {
		return nil
	}
	reason := fmt.Sprintf("block %d (%s) failed after committing contract state: %v", blockNumber, blockHash.Hex(), cause)
	haltApply(ctx, reason)
	return fmt.Errorf("%w: %s — rollback refused, resync required", ErrApplyHalted, reason)
}
