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
// height (alert, record kept) when the failure keeps repeating. The fix for a
// halted height is to repair the cause and apply the same block, never to skip
// it.

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
		Block:     rawBlock,
		Results:   rawResults,
	}, nil
}

// recordPendingCertified saves block and its certificate as the pending
// commit, first dropping a stale record left by an already-stored block. Call
// it as soon as the certificate is known (ProcessVoteCollection, right after
// quorum is verified), so a crash before the commit attempt still leaves the
// record on disk: the block servers then refuse new proposals at the height and
// startup resumes this block. Saving the same block again is allowed, so
// commitCertifiedBlock calls it too. A failure is logged and alerted; the
// returned error is informational (the record may still be held in memory).
func recordPendingCertified(ctx context.Context, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) error {
	// A record left over from an already-stored block (its Clear failed) would
	// make Save refuse this height. Drop it first. If the tip cannot be read the
	// record is kept: it may belong to a block that is not stored.
	if tip, terr := committedTip(); terr != nil {
		logger().Warn(ctx, "Could not read the committed tip before recording a certified block",
			ion.String("error", terr.Error()),
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("function", "recordPendingCertified"))
	} else if cerr := pendingCommits.ClearStale(tip); cerr != nil {
		logger().Warn(ctx, "Could not delete a stale pending record from the store; it no longer blocks proposals and will be overwritten",
			ion.String("error", cerr.Error()),
			ion.Uint64("committed_tip", tip),
			ion.String("function", "recordPendingCertified"))
	}

	rec, err := pendingRecordFor(block, bls)
	if err == nil {
		err = pendingCommits.Save(rec)
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
	// Normally already saved by ProcessVoteCollection when quorum was
	// verified; saving the same block again just rewrites the record.
	_ = recordPendingCertified(ctx, block, bls)

	var lastErr error
	for attempt := 0; ; attempt++ {
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
			return haltCertifiedHeight(ctx, block, attempt,
				errors.New("a different block is already stored at this height"))
		case state == storedSamePartial:
			// Some transactions are applied and some are not: applying again
			// could not tell which, and skipping would leave state behind the
			// chain. An operator must repair it.
			return haltCertifiedHeight(ctx, block, attempt,
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

		err := applyCertifiedBlock(block, bls)
		if err == nil {
			break
		}
		lastErr = err
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

	// Stored. Only now may the certificate feed the next block (it used to be
	// recorded before the commit attempt).
	recordCommitCert(block.BlockNumber, bls)
	if err := pendingCommits.Clear(block.BlockNumber); err != nil {
		// The record is now at or below the committed tip, so it no longer
		// blocks proposals. It would make Save refuse the next height, so the
		// next commitCertifiedBlock drops it first (ClearStale).
		logger().Warn(ctx, "Committed a certified block but could not clear its pending record",
			ion.String("error", err.Error()),
			ion.Uint64("block_number", block.BlockNumber),
			ion.String("function", "commitCertifiedBlock"))
	}
	return nil
}

func haltCertifiedHeight(ctx context.Context, block *config.ZKBlock, attempts int, cause error) error {
	logger().Error(ctx, "Certified block could not be committed; height halted until the cause is repaired", cause,
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("period", block.Period),
		ion.Int("attempts", attempts),
		ion.String("function", "haltCertifiedHeight"))
	Alerts.NewAlertBuilder(ctx).
		AlertName("Consensus: Certified Block Halted").
		Status(Alerts.AlertStatusError).
		Severity(Alerts.SeverityError).
		Description(fmt.Sprintf("Block %d has a valid certificate but could not be stored; the height is halted. Repair the cause and restart: the same block is retried, never skipped.", block.BlockNumber)).
		Label("block_number", fmt.Sprintf("%d", block.BlockNumber)).
		Label("block_hash", block.BlockHash.Hex()).
		Label("attempts", fmt.Sprintf("%d", attempts)).
		Label("cause", cause.Error()).
		Send()
	return fmt.Errorf("%w: block %d after %d attempt(s): %v", ErrCertifiedBlockHalted, block.BlockNumber, attempts, cause)
}

func broadcastCommittedBlock(h host.Host, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) error {
	return messaging.BroadcastBlockToEveryNodeWithExtraData(h, block, true, map[string]string{"status": "accepted"}, bls)
}

// ResumePendingCommit finishes a certified block recorded before the last
// shutdown. Call once on the sequencer at startup, after the database is
// ready. It returns nil when nothing is pending or the block is committed; an
// error leaves the record in place, so new proposals at the height stay
// refused.
func ResumePendingCommit(ctx context.Context, h host.Host) error {
	rec, err := pendingCommits.Current()
	if err != nil {
		return fmt.Errorf("resume pending commit: %w", err)
	}
	if rec == nil {
		return nil
	}
	var block config.ZKBlock
	if err := json.Unmarshal(rec.Block, &block); err != nil {
		return fmt.Errorf("resume pending commit: decode block %d: %w", rec.Height, err)
	}
	var bls []BLS_Signer.BLSresponse
	if err := json.Unmarshal(rec.Results, &bls); err != nil {
		return fmt.Errorf("resume pending commit: decode certificate for block %d: %w", rec.Height, err)
	}
	if block.BlockNumber != rec.Height || block.BlockHash.Hex() != rec.BlockHash {
		return fmt.Errorf("resume pending commit: record for height %d (%s) holds block %d (%s)",
			rec.Height, rec.BlockHash, block.BlockNumber, block.BlockHash.Hex())
	}

	// Committed before the record was cleared: just clear it.
	if tip, terr := committedTip(); terr == nil && tip >= rec.Height {
		if state, serr := storedBlockStateAt(&block); serr == nil && state == storedSameApplied {
			return pendingCommits.Clear(rec.Height)
		}
	}

	logger().Info(ctx, "Resuming a certified block that was not stored before shutdown",
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("period", block.Period),
		ion.String("function", "ResumePendingCommit"))
	if err := commitCertifiedBlock(ctx, &block, bls); err != nil {
		return err
	}
	if h != nil {
		if err := broadcastCommitted(h, &block, bls); err != nil {
			logger().Warn(ctx, "Resumed block committed but broadcast failed; peers will reconcile via catch-up",
				ion.String("error", err.Error()),
				ion.Uint64("block_number", block.BlockNumber),
				ion.String("function", "ResumePendingCommit"))
		}
	}
	return nil
}
