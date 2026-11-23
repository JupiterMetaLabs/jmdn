package Sequencer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
)

// certifiedCommitMu serializes commit attempts of a certified block between
// the live consensus path and the background watcher.
var certifiedCommitMu sync.Mutex

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
