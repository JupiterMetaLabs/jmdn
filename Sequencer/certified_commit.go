package Sequencer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/DB_OPs"
	"gossipnode/Sequencer/Alerts"
	"gossipnode/config"
	"gossipnode/internal/pendingcommit"
	"gossipnode/messaging"
	"gossipnode/messaging/BlockProcessing"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
)

// A certified block is committed or the height halts; it is never abandoned.
//
// Before this, a failed local apply of a certified block advanced the period
// (advancePeriodAfterFailedRound), and the next round could certify a
// different block at the same height while the first certificate still
// existed. Now the sequencer records the block and its certificate, retries
// the same block with the same certificate at the same period, and halts the
// height (alert, record kept) when the failure keeps repeating. A halted
// height keeps being retried in the background (RunPendingCommitWatcher), so a
// store outage that heals commits the block without a restart. A state that
// cannot heal on its own (a different or partly applied block stored, or block
// apply halted on this node) needs an operator; the operator may, as a last
// resort, drop the record (consensus admin pending-commit drop).

// ErrCertifiedBlockHalted reports that a certified block could not be stored
// and the height is halted until an operator repairs the cause. The pending
// record is kept, so new proposals at the height stay refused and a restart
// resumes the retry.
var ErrCertifiedBlockHalted = errors.New("certified block could not be committed; height halted")

// Hooks, replaced in tests.
var (
	applyCertifiedBlock   = messaging.ProcessBlockLocally
	recordCommitCert      = messaging.RecordCommitCertificate
	storedBlockStateAt    = loadStoredBlockState
	committedTip          = DB_OPs.GetLatestDataCompleteBlock
	broadcastCommitted    = broadcastCommittedBlock
	pendingCommits        = pendingcommit.Default
	certifiedCommitDelays = []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	certifiedCommitSleep = func(ctx context.Context, d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
)

// storedState is what the node's store holds at a certified block's height.
type storedState int

const (
	storedNone          storedState = iota // nothing stored at the height
	storedSameApplied                      // this block, applied
	storedSameUnapplied                    // this block stored, none of its transactions applied
	storedSamePartial                      // this block stored, some of its transactions applied
	storedDifferent                        // a different block
)

func (s storedState) String() string {
	switch s {
	case storedNone:
		return "none"
	case storedSameApplied:
		return "same block, applied"
	case storedSameUnapplied:
		return "same block stored, transactions not applied"
	case storedSamePartial:
		return "same block stored, transactions partly applied"
	case storedDifferent:
		return "different block"
	}
	return "unknown"
}

// Store reads used by loadStoredBlockState, replaced in tests.
var (
	readStoredBlock = func(height uint64) (*config.ZKBlock, error) {
		return DB_OPs.GetZKBlockByNumber(nil, height)
	}
	blockMarkerApplied = func(blockHash string) (bool, error) {
		return DB_OPs.IsMarkerApplied(nil, DB_OPs.BlockProcessedKey(blockHash))
	}
	txMarkersApplied = DB_OPs.FilterProcessedTxMarkers
)

// loadStoredBlockState reports what the store holds at block's height.
//
// A stored block is not proof that its transactions were applied. The block
// write is a chain of separate ThebeDB appends, and an append that fails is
// queued in the gateway outbox and written later, so a block whose apply was
// rolled back (store error) can still appear in the store afterwards. Whether
// it was applied is decided by the per-transaction markers, which commit
// together with each transaction's balances and are revoked by the rollback.
// The block-processed marker is only a hint: it is not written for a block
// without transactions, and a failure to write it is only logged.
func loadStoredBlockState(block *config.ZKBlock) (storedState, error) {
	stored, err := readStoredBlock(block.BlockNumber)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			return storedNone, nil
		}
		return storedNone, err
	}
	if stored == nil {
		return storedNone, nil
	}
	if stored.BlockHash != block.BlockHash {
		return storedDifferent, nil
	}
	marked, err := blockMarkerApplied(block.BlockHash.Hex())
	if err != nil {
		return storedNone, err
	}
	if marked {
		return storedSameApplied, nil
	}
	hashes := blockTxHashes(block)
	applied, err := txMarkersApplied(hashes)
	if err != nil {
		return storedNone, err
	}
	return classifyStoredBlock(hashes, applied), nil
}

// blockTxHashes lists block's transaction hashes once each, in the form the
// apply path keys its per-transaction markers by.
func blockTxHashes(block *config.ZKBlock) []string {
	seen := make(map[string]bool, len(block.Transactions))
	out := make([]string, 0, len(block.Transactions))
	for i := range block.Transactions {
		h := block.Transactions[i].Hash.String()
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// classifyStoredBlock decides, for a stored block without its block-processed
// marker, whether its transactions were applied: all of them (a block without
// transactions counts as applied), none, or only some.
func classifyStoredBlock(hashes []string, applied map[string]bool) storedState {
	n := 0
	for _, h := range hashes {
		if applied[h] {
			n++
		}
	}
	switch {
	case n == len(hashes):
		return storedSameApplied
	case n == 0:
		return storedSameUnapplied
	default:
		return storedSamePartial
	}
}

// pendingRecordFor encodes block and its certificate responses.
func pendingRecordFor(block *config.ZKBlock, bls []BLS_Signer.BLSresponse) (pendingcommit.Record, error) {
	rawBlock, err := json.Marshal(block)
	if err != nil {
		return pendingcommit.Record{}, fmt.Errorf("encode block: %w", err)
	}
	rawResults, err := json.Marshal(bls)
	if err != nil {
		return pendingcommit.Record{}, fmt.Errorf("encode certificate: %w", err)
	}
	return pendingcommit.Record{
		Height:    block.BlockNumber,
		BlockHash: block.BlockHash.Hex(),
		RoundID:   certifiedRoundID(block),
		Block:     rawBlock,
		Results:   rawResults,
	}, nil
}

// certifiedRoundID identifies the certified round of block: its
// ConsensusHash, which covers the period and the consensus fields. BlockHash
// alone covers only the transactions, so the same transactions certified
// again at a later period would look like the same block. Without a
// ConsensusHash (a block built without the consensus binding) it falls back to
// block hash and period.
func certifiedRoundID(block *config.ZKBlock) string {
	if ch := block.ConsensusHashHex(); ch != "" {
		return ch
	}
	return fmt.Sprintf("%s/p%d", block.BlockHash.Hex(), block.Period)
}

// errCertifiedBlockAlreadyCommitted reports that the block was already
// committed and broadcast by the background watcher; the live path does
// nothing more.
var errCertifiedBlockAlreadyCommitted = errors.New("certified block already committed")

// certifiedBlockAlreadyCommitted reports whether block is stored at its
// height and applied, with the committed tip at or above it. A read error
// counts as not committed, so the normal path runs.
func certifiedBlockAlreadyCommitted(block *config.ZKBlock) bool {
	tip, err := committedTip()
	if err != nil || tip < block.BlockNumber {
		return false
	}
	state, serr := storedBlockStateAt(block)
	return serr == nil && state == storedSameApplied
}

// settleRecordBelowTip handles a pending record whose height the committed
// tip has reached, and reports whether the record was removed:
//
//   - the stored block at that height is this block, applied: it was
//     committed before its record was cleared; remove the record.
//   - a different block is stored at that height: the chain moved on without
//     this block, so its transactions did not land. Report them to the
//     orchestrator, then remove the record.
//   - anything else (unreadable, not yet applied, partly applied): keep the
//     record; proposals stay refused and the operator is alerted elsewhere.
func settleRecordBelowTip(ctx context.Context, rec *pendingcommit.Record) bool {
	var block config.ZKBlock
	if err := json.Unmarshal(rec.Block, &block); err != nil {
		return false
	}
	state, serr := storedBlockStateAt(&block)
	if serr != nil {
		return false
	}
	if state != storedSameApplied && state != storedDifferent {
		return false
	}
	// Remove the record first and report only once that succeeded, so a
	// record that cannot be cleared (retried every minute) is not reported to
	// the orchestrator again on every try. The report is best effort, as all
	// rejection reports are.
	if err := pendingCommits.Clear(rec.Height, rec.BlockHash, rec.RoundID); err != nil {
		logger().Warn(ctx, "Could not remove a settled pending record",
			ion.String("error", err.Error()),
			ion.Uint64("block_number", rec.Height),
			ion.String("function", "settleRecordBelowTip"))
		return false
	}
	if state == storedDifferent {
		logger().Warn(ctx, "A different block is stored at the pending height; its certified block did not land and is reported to the orchestrator",
			ion.Uint64("block_number", rec.Height),
			ion.String("block_hash", rec.BlockHash),
			ion.String("function", "settleRecordBelowTip"))
		sendRejectionReport(&block, supersededPendingReason, "")
	}
	return true
}

// supersededPendingReason is reported for the transactions of a certified
// block whose height the chain filled with a different block.
const supersededPendingReason = "certified, but a different block is stored at this height; its transactions did not land"

// recordPendingCertified saves block and its certificate as the pending
// commit, first dropping a stale record left by an already-stored block. Call
// it as soon as the certificate is known (ProcessVoteCollection, right after
// quorum is verified), so a crash before the commit attempt still leaves the
// record on disk: the block servers then refuse new proposals at the height and
// startup resumes this block. Saving the same block again is allowed, so
// commitCertifiedBlock calls it too. A failure is logged and alerted; the
// returned error is informational (the record may still be held in memory).
func recordPendingCertified(ctx context.Context, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) error {
	// A record at or below the committed tip would make Save refuse this
	// height. Settle it first (see settleRecordBelowTip). If the tip cannot be
	// read the record is kept: it may belong to a block that is not stored.
	var tip uint64
	tipKnown := false
	if t, terr := committedTip(); terr != nil {
		logger().Warn(ctx, "Could not read the committed tip before recording a certified block",
			ion.String("error", terr.Error()),
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("function", "recordPendingCertified"))
	} else {
		tip, tipKnown = t, true
		if old, cerr := pendingCommits.Current(); cerr == nil && old != nil && old.Height <= tip && !settleRecordBelowTip(ctx, old) {
			// The old record could not be settled, but it no longer blocks
			// proposals (the chain reached its height). This block's height
			// must still be protected, so its record replaces the old one
			// below (SaveOver); the old one is logged here for the operator.
			logger().Error(ctx, "A pending record at or below the committed tip could not be settled; replacing it so the new certified height stays protected",
				errors.New("unsettled pending record"),
				ion.Uint64("old_block_number", old.Height),
				ion.String("old_block_hash", old.BlockHash),
				ion.Uint64("committed_tip", tip),
				ion.Uint64("block_number", block.BlockNumber),
				ion.String("function", "recordPendingCertified"))
		}
	}

	rec, err := pendingRecordFor(block, bls)
	if err == nil {
		if tipKnown && block.BlockNumber > tip {
			err = pendingCommits.SaveOver(rec, tip)
		} else {
			err = pendingCommits.Save(rec)
		}
	}
	if err != nil {
		// Either the store write failed (the record still blocks proposals in
		// this process but will not survive a restart), or Save refused because
		// another height is still pending (this height is not recorded). Both
		// need an operator to look, so alert rather than only log.
		logger().Error(ctx, "Could not record the pending certified block", err,
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.String("function", "recordPendingCertified"))
		Alerts.NewAlertBuilder(ctx).
			AlertName("Consensus: Pending Commit Record Not Saved").
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(fmt.Sprintf("The pending-commit record for certified block %d could not be saved; a restart or a later failure may leave the height unprotected. Check the store and any other pending height.", block.BlockNumber)).
			Label("block_number", fmt.Sprintf("%d", block.BlockNumber)).
			Label("block_hash", block.BlockHash.Hex()).
			Label("cause", err.Error()).
			Send()
	}
	return err
}

// commitCertifiedBlock stores a block that holds a valid certificate. It
// returns nil once the block is stored, or an error wrapping
// ErrCertifiedBlockHalted when the height must halt. It never advances the
// period.
func commitCertifiedBlock(ctx context.Context, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) error {
	// One commit series at a time: the live path and the background watcher
	// must not apply the same block concurrently.
	certifiedCommitMu.Lock()
	defer certifiedCommitMu.Unlock()
	// The background watcher may have committed this block (from the record
	// saved at quorum) before the live path got here. Then there is nothing
	// to do: no record, no apply, no certificate, no broadcast. This check is
	// only at entry; inside the retry series a stored, applied block may be
	// this series' own late-landing write, which still needs its apply.
	if certifiedBlockAlreadyCommitted(block) {
		return errCertifiedBlockAlreadyCommitted
	}
	// Normally already saved by ProcessVoteCollection when quorum was
	// verified; saving the same block again just rewrites the record. Only
	// the live path saves it, and never for a block an operator dropped (the
	// check and the save run under certifiedAttemptMu, which the drop takes),
	// so a drop cannot be undone.
	if !recordPendingUnlessDropped(ctx, block, bls) {
		certifiedAttemptMu.Lock()
		err := droppedError(ctx, block)
		certifiedAttemptMu.Unlock()
		return err
	}
	return commitCertifiedBlockLocked(ctx, block, bls)
}

// commitCertifiedBlockLocked runs the retry series; the caller holds
// certifiedCommitMu. Each attempt runs under certifiedAttemptMu and first
// checks that an operator has not dropped the block, so a drop waits for at
// most one attempt and no attempt starts after it.
func commitCertifiedBlockLocked(ctx context.Context, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		done, err := commitAttempt(ctx, block, bls, attempt)
		if done {
			return err
		}
		lastErr = err
		if errors.Is(err, BlockProcessing.ErrApplyHalted) {
			// Block apply on this node is halted until it is resynced; every
			// retry would fail the same way. Halt now and keep the record.
			return haltCertifiedHeight(ctx, block, attempt+1, err)
		}
		if attempt >= len(certifiedCommitDelays) {
			return haltCertifiedHeight(ctx, block, attempt+1, lastErr)
		}
		delay := certifiedCommitDelays[attempt]
		logger().Warn(ctx, "Certified block failed to apply; retrying the same block at the same period",
			ion.String("error", err.Error()),
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Uint64("period", block.Period),
			ion.Int("attempt", attempt+1),
			ion.String("retry_in", delay.String()),
			ion.String("function", "commitCertifiedBlock"))
		if !certifiedCommitSleep(ctx, delay) {
			return fmt.Errorf("%w: stopped retrying block %d: %v (last error: %v)",
				ErrCertifiedBlockHalted, block.BlockNumber, ctx.Err(), lastErr)
		}
	}
}

// commitAttempt makes one attempt under certifiedAttemptMu. done reports
// that the series is over (committed, dropped or halted); otherwise err is
// the apply error to retry on.
func commitAttempt(ctx context.Context, block *config.ZKBlock, bls []BLS_Signer.BLSresponse, attempt int) (done bool, err error) {
	certifiedAttemptMu.Lock()
	defer certifiedAttemptMu.Unlock()
	if pendingDropped(block) {
		return true, droppedError(ctx, block)
	}
	state, serr := storedBlockStateAt(block)
	switch {
	case serr != nil:
		// Cannot tell what is stored. The apply below is still safe: it
		// re-checks the block-processed marker and fails closed on a read error.
		logger().Warn(ctx, "Stored-block check failed before applying a certified block",
			ion.String("error", serr.Error()),
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("function", "commitCertifiedBlock"))
	case state == storedDifferent:
		return true, haltCertifiedHeight(ctx, block, attempt+1,
			errors.New("a different block is already stored at this height"))
	case state == storedSamePartial:
		// Some transactions are applied and some are not: applying again
		// could not tell which, and skipping would leave state behind the
		// chain. An operator must repair it.
		return true, haltCertifiedHeight(ctx, block, attempt+1,
			errors.New("the block is stored but only some of its transactions are applied"))
	case state == storedSameUnapplied:
		// Typically a store that reported an error was rolled back and the
		// queued write landed later. Nothing of the block is applied, and
		// storing it again is an idempotent upsert, so apply it again.
		logger().Warn(ctx, "Certified block is stored but none of its transactions are applied; applying it again",
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int("attempt", attempt+1),
			ion.String("function", "commitCertifiedBlock"))
	}

	if aerr := applyCertifiedBlock(block, bls); aerr != nil {
		return false, aerr
	}

	// Stored. Only now may the certificate feed the next block (it used to be
	// recorded before the commit attempt).
	recordCommitCert(block.BlockNumber, bls)
	if err := pendingCommits.Clear(block.BlockNumber, block.BlockHash.Hex(), certifiedRoundID(block)); err != nil {
		// The record is now at or below the committed tip, so it no longer
		// blocks proposals. It would make Save refuse the next height, so the
		// next recordPendingCertified settles it first (settleRecordBelowTip).
		logger().Warn(ctx, "Committed a certified block but could not clear its pending record",
			ion.String("error", err.Error()),
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("function", "commitCertifiedBlock"))
	}
	return true, nil
}

// recordPendingUnlessDropped saves the pending record for block unless an
// operator dropped that block, and reports whether it was saved (or at least
// attempted). The check and the save run under certifiedAttemptMu, which the
// drop takes, so a drop is never undone by a later save.
func recordPendingUnlessDropped(ctx context.Context, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) bool {
	certifiedAttemptMu.Lock()
	defer certifiedAttemptMu.Unlock()
	if pendingDropped(block) {
		return false
	}
	_ = recordPendingCertified(ctx, block, bls)
	return true
}

// droppedError logs that block was dropped by an operator, removes any
// record still held for it (one a race may have re-created must not keep
// refusing proposals), and returns ErrCertifiedBlockDropped. The caller holds
// certifiedAttemptMu.
func droppedError(ctx context.Context, block *config.ZKBlock) error {
	logger().Warn(ctx, "Certified block was dropped by an operator; not applying it",
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.String("function", "commitCertifiedBlock"))
	if err := pendingCommits.Clear(block.BlockNumber, block.BlockHash.Hex(), certifiedRoundID(block)); err != nil {
		logger().Warn(ctx, "Could not remove the pending record of a dropped block",
			ion.String("error", err.Error()),
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("function", "commitCertifiedBlock"))
	}
	return fmt.Errorf("%w: block %d (%s)", ErrCertifiedBlockDropped, block.BlockNumber, block.BlockHash.Hex())
}

func haltCertifiedHeight(ctx context.Context, block *config.ZKBlock, attempts int, cause error) error {
	logger().Error(ctx, "Certified block could not be committed; height halted, retried in the background until the cause is repaired", cause,
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("period", block.Period),
		ion.Int("attempts", attempts),
		ion.String("function", "haltCertifiedHeight"))
	if haltAlerts.due(block.BlockNumber, certifiedNow()) {
		Alerts.NewAlertBuilder(ctx).
			AlertName("Consensus: Certified Block Halted").
			Status(Alerts.AlertStatusError).
			Severity(Alerts.SeverityError).
			Description(fmt.Sprintf("Block %d has a valid certificate but could not be stored; the height is halted and the same block is retried in the background, never skipped. Repair the cause (see the cause label). If the block can never be stored, see the runbook for dropping the pending record.", block.BlockNumber)).
			Label("block_number", fmt.Sprintf("%d", block.BlockNumber)).
			Label("block_hash", block.BlockHash.Hex()).
			Label("attempts", fmt.Sprintf("%d", attempts)).
			Label("cause", cause.Error()).
			Send()
	}
	return fmt.Errorf("%w: block %d after %d attempt(s): %v", ErrCertifiedBlockHalted, block.BlockNumber, attempts, cause)
}

func broadcastCommittedBlock(h host.Host, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) error {
	return messaging.BroadcastBlockToEveryNodeWithExtraData(h, block, true, map[string]string{"status": "accepted"}, bls)
}

// ResumePendingCommit commits the pending certified block, if any: one left
// by the last run, or one whose height halted. RunPendingCommitWatcher calls
// it at startup and then periodically. It returns nil when nothing is pending
// or the block is committed; an error leaves the record in place, so new
// proposals at the height stay refused.
func ResumePendingCommit(ctx context.Context, h host.Host) error {
	block, bls, err := resumeUnderLock(ctx)
	if err != nil || block == nil {
		return err
	}
	if h != nil {
		if err := broadcastCommitted(h, block, bls); err != nil {
			logger().Warn(ctx, "Resumed block committed but broadcast failed; peers will reconcile via catch-up",
				ion.String("error", err.Error()),
				ion.Uint64("block_number", block.BlockNumber),
				ion.String("function", "ResumePendingCommit"))
		}
	}
	return nil
}

// resumeUnderLock reads the pending record and commits it while holding
// certifiedCommitMu, so a record the live path has just committed and cleared
// is not committed (or broadcast) again. It returns the block to broadcast,
// or nil when there was nothing to commit.
func resumeUnderLock(ctx context.Context) (*config.ZKBlock, []BLS_Signer.BLSresponse, error) {
	certifiedCommitMu.Lock()
	defer certifiedCommitMu.Unlock()

	rec, err := pendingCommits.Current()
	if err != nil {
		return nil, nil, fmt.Errorf("resume pending commit: %w", err)
	}
	if rec == nil {
		return nil, nil, nil
	}
	var block config.ZKBlock
	if err := json.Unmarshal(rec.Block, &block); err != nil {
		return nil, nil, fmt.Errorf("resume pending commit: decode block %d: %w", rec.Height, err)
	}
	var bls []BLS_Signer.BLSresponse
	if err := json.Unmarshal(rec.Results, &bls); err != nil {
		return nil, nil, fmt.Errorf("resume pending commit: decode certificate for block %d: %w", rec.Height, err)
	}
	if block.BlockNumber != rec.Height || block.BlockHash.Hex() != rec.BlockHash {
		return nil, nil, fmt.Errorf("resume pending commit: record for height %d (%s) holds block %d (%s)",
			rec.Height, rec.BlockHash, block.BlockNumber, block.BlockHash.Hex())
	}

	// The tip already reached this height: settle the record instead of
	// retrying (committed before the record was cleared, or a different
	// block is stored here).
	if tip, terr := committedTip(); terr == nil && tip >= rec.Height {
		if settleRecordBelowTip(ctx, rec) {
			return nil, nil, nil
		}
	}

	logger().Info(ctx, "Resuming a certified block that is not stored yet",
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("period", block.Period),
		ion.String("function", "ResumePendingCommit"))
	if err := commitCertifiedBlockLocked(ctx, &block, bls); err != nil {
		return nil, nil, err
	}
	return &block, bls, nil
}
