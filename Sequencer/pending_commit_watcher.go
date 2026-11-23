package Sequencer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"

	"gossipnode/Sequencer/RejectionReport"
	"gossipnode/config"
	"gossipnode/internal/pendingcommit"
	"gossipnode/internal/proposalguard"
	"gossipnode/messaging"
)

// certifiedCommitMu serializes commit series of a certified block between
// the live consensus path and the background watcher.
var certifiedCommitMu sync.Mutex

// certifiedAttemptMu is held for one commit attempt (drop check, apply,
// clear). An operator drop takes it too, so it waits for at most one attempt,
// and no attempt starts after the drop.
var certifiedAttemptMu sync.Mutex

// ErrCertifiedBlockDropped reports that an operator dropped the pending
// record of the block being committed, so it is not applied.
var ErrCertifiedBlockDropped = errors.New("certified block dropped by operator")

// droppedPending is the certified round an operator last dropped; guarded by
// certifiedAttemptMu. It is matched by round (certifiedRoundID), not by
// BlockHash: BlockHash covers only the transactions, and the orchestrator may
// re-propose the same transactions at the dropped height after the drop is
// reported. That new round must be able to commit.
var droppedPending struct {
	height  uint64
	roundID string
}

// pendingDropped reports whether block's certified round is the one an
// operator dropped. The caller holds certifiedAttemptMu.
func pendingDropped(block *config.ZKBlock) bool {
	return droppedPending.roundID != "" && droppedPending.height == block.BlockNumber &&
		strings.EqualFold(droppedPending.roundID, certifiedRoundID(block))
}

// droppedRoundID returns the round of a dropped record: its RoundID, or for
// a record written before that field existed, the round of its decoded block.
func droppedRoundID(rec *pendingcommit.Record) string {
	if rec.RoundID != "" {
		return rec.RoundID
	}
	var block config.ZKBlock
	if err := json.Unmarshal(rec.Block, &block); err == nil {
		return certifiedRoundID(&block)
	}
	return ""
}

// DropUnreadablePendingCommit removes the stored pending record only when it
// cannot be decoded (operator last resort; its block cannot be reported to
// the orchestrator because it cannot be read). Taken under
// certifiedAttemptMu like DropPendingCommit.
func DropUnreadablePendingCommit() error {
	certifiedAttemptMu.Lock()
	defer certifiedAttemptMu.Unlock()
	return pendingCommits.DropUnreadable()
}

// DropPendingCommit removes the pending record when height and block hash
// both match it (operator last resort, consensus admin pending-commit drop).
// It waits for an attempt in progress, marks the block dropped so no later
// attempt applies it, and reports its transactions to the orchestrator as not
// landed. Errors: pendingcommit.ErrNoPending, pendingcommit.ErrRecordMismatch
// (with the current record), or a store error.
func DropPendingCommit(height uint64, blockHash string) (*pendingcommit.Record, error) {
	certifiedAttemptMu.Lock()
	rec, err := pendingCommits.DropMatching(height, blockHash)
	if err == nil {
		droppedPending.height, droppedPending.roundID = rec.Height, droppedRoundID(rec)
	}
	certifiedAttemptMu.Unlock()
	if err != nil {
		return rec, err
	}
	startNewRoundAfterDrop(rec)
	ReportDroppedPendingBlock(*rec)
	return rec, nil
}

// startNewRoundAfterDrop moves the dropped height to a period above the
// dropped block's and releases its proposal claim, so the next proposal at
// the height is a new round. Without it, the same transactions proposed again
// would rebuild the identical round (same period, same ConsensusHash), which
// the drop marker refuses, and the height could never commit.
func startNewRoundAfterDrop(rec *pendingcommit.Record) {
	var droppedPeriod uint64
	var block config.ZKBlock
	if err := json.Unmarshal(rec.Block, &block); err == nil {
		droppedPeriod = block.Period
	}
	// Bounded: each step raises the period by at least one, up to the cap.
	for i := 0; i < 1024; i++ {
		next, capped := advanceDroppedHeight(rec.Height)
		if capped || next > droppedPeriod {
			break
		}
	}
	releaseProposalClaim(rec.Height)
}

// certifiedNow is the clock for halt-alert pacing; a seam for tests.
var certifiedNow = time.Now

// pendingRetryInterval is how long the watcher waits between commit attempts
// of a pending certified block. Each attempt is itself a full retry series
// (certifiedCommitDelays), so a halted height is retried indefinitely with a
// pause between series.
var pendingRetryInterval = time.Minute

// haltAlertInterval bounds how often the "Certified Block Halted" alert is
// sent for one height while the watcher keeps retrying it. Every attempt is
// still logged.
const haltAlertInterval = 15 * time.Minute

type haltAlertPacer struct {
	mu     sync.Mutex
	height uint64
	last   time.Time
}

// due reports whether the halt alert for height should be sent now, and if
// so records it. A new height always alerts.
func (p *haltAlertPacer) due(height uint64, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if height == p.height && !p.last.IsZero() && now.Sub(p.last) < haltAlertInterval {
		return false
	}
	p.height, p.last = height, now
	return true
}

var haltAlerts haltAlertPacer

// RunPendingCommitWatcher commits a pending certified block left by the last
// run, then keeps retrying any pending record (after a halt, or one that
// could not be read at startup) every pendingRetryInterval until ctx is
// cancelled. Run it once on the sequencer, under a tracked goroutine, after
// the database is ready. It returns when ctx is cancelled.
//
// While a record is pending the block servers refuse proposals at that height
// and above, so without this a store outage longer than one retry series
// would stop block production until a restart.
func RunPendingCommitWatcher(ctx context.Context, h host.Host) {
	for {
		if err := ResumePendingCommit(ctx, h); err != nil && ctx.Err() == nil {
			lvl := "Pending certified block not committed yet; retrying"
			if errors.Is(err, ErrCertifiedBlockHalted) {
				lvl = "Pending certified block is halted; retrying in the background"
			}
			logger().Warn(ctx, lvl,
				ion.String("error", err.Error()),
				ion.String("retry_in", pendingRetryInterval.String()),
				ion.String("function", "RunPendingCommitWatcher"))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pendingRetryInterval):
		}
	}
}

// advanceDroppedHeight and releaseProposalClaim are seams for tests.
var (
	advanceDroppedHeight = func(height uint64) (uint64, bool) {
		return messaging.DefaultProposerPeriods.AdvanceAfterFailure(height, config.MaxMainPeers)
	}
	releaseProposalClaim = proposalguard.Default.Release
)

// sendRejectionReport is RejectionReport.Send; a seam for tests.
var sendRejectionReport = RejectionReport.Send

// droppedPendingReason is the reason reported to the orchestrator for the
// transactions of a certified block an operator dropped.
const droppedPendingReason = "certified block dropped by operator; never stored, its transactions did not land"

// ReportDroppedPendingBlock reports the transactions of a pending certified
// block that an operator dropped (consensus admin pending-commit drop): the
// block will never be stored, so the orchestrator, which marked them included
// when it submitted the block, must learn they did not land.
func ReportDroppedPendingBlock(rec pendingcommit.Record) {
	var block config.ZKBlock
	if err := json.Unmarshal(rec.Block, &block); err != nil {
		logger().Warn(context.Background(), "Could not decode a dropped pending block; its transactions are not reported to the orchestrator",
			ion.String("error", err.Error()),
			ion.Uint64("height", rec.Height),
			ion.String("block_hash", rec.BlockHash),
			ion.String("function", "ReportDroppedPendingBlock"))
		return
	}
	sendRejectionReport(&block, droppedPendingReason, "")
}
