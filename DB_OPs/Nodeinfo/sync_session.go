// MODULE: DB_OPs/Nodeinfo/sync_session
// PURPOSE: Coordinate the `latest_block` marker with FastSync sessions so the
//          marker only advances once a session's reconciliation effects are
//          committed to the database.
//
// INVARIANT: checkLinkage admits a live block at tip+1 as soon as the marker
// reaches tip. The marker therefore must not reach a sync range's tip before
// the balances for that range are applied and confirmed — otherwise live
// execution would begin on top of bases that reconciliation is still writing.
//
// MECHANISM: FastsyncV2 brackets every sync flow with BeginSyncSession /
// EndSyncSession. While at least one session is active, DeferLatestBlockAdvance
// captures would-be marker advances into an in-memory high-water mark instead
// of writing them. The sync flow advances the marker itself, explicitly, only
// after reconciliation is proven applied (anchor advanced + queue drain
// confirmed) — see FastsyncV2.endSyncSession.
//
// FAIL DIRECTION: every failure keeps the marker LAGGING. A lagging marker
// means checkLinkage keeps rejecting out-of-band tip blocks (height_gap) and
// the sync monitor retries catch-up — the self-healing direction. The marker
// is never advanced optimistically.
//
// The live path (blockPropagation → UpdateLatestBlockMonotonic) is
// deliberately NOT deferred: a node that is already contiguous keeps applying
// and advancing normally even while a background reconciliation of an older
// range runs.

package NodeInfo

import (
	"log"
	"sync"
)

var syncSession struct {
	mu     sync.Mutex
	active int    // nesting/overlap counter (CLI catchup + monitor reconcile)
	tip    uint64 // highest block written by sync writers during the session
}

// BeginSyncSession marks a FastSync flow as active. Reentrant: overlapping
// sessions (a CLI-triggered catch-up alongside a monitor reconcile) stack.
func BeginSyncSession() {
	syncSession.mu.Lock()
	defer syncSession.mu.Unlock()
	syncSession.active++
	log.Printf("[syncsession] begin (depth=%d) — latest_block advances are deferred", syncSession.active)
}

// EndSyncSession unmarks the flow and returns the deferred tip high-water mark
// (0 if nothing was written). The LAST session out clears the mark. Callers
// must decide explicitly what to advance the marker to — this function never
// writes the marker.
func EndSyncSession() uint64 {
	syncSession.mu.Lock()
	defer syncSession.mu.Unlock()
	if syncSession.active > 0 {
		syncSession.active--
	}
	tip := syncSession.tip
	if syncSession.active == 0 {
		syncSession.tip = 0
	}
	log.Printf("[syncsession] end (depth=%d, deferred tip=%d)", syncSession.active, tip)
	return tip
}

// DeferLatestBlockAdvance atomically checks for an active session and, if one
// is active, records blockNumber into the deferred high-water mark. Returns
// true when the advance was deferred (caller must NOT write the marker) and
// false when no session is active (caller proceeds with the normal monotonic
// write). Check + note are one critical section so a session ending between
// them cannot strand a tip.
func DeferLatestBlockAdvance(blockNumber uint64) bool {
	syncSession.mu.Lock()
	defer syncSession.mu.Unlock()
	if syncSession.active == 0 {
		return false
	}
	if blockNumber > syncSession.tip {
		syncSession.tip = blockNumber
	}
	return true
}

// PeekSyncSessionTip returns the current deferred high-water mark without
// consuming it. Used by in-session readers (PoTS range selection, anchor caps)
// that need the true written head while the marker is held back.
func PeekSyncSessionTip() uint64 {
	syncSession.mu.Lock()
	defer syncSession.mu.Unlock()
	return syncSession.tip
}
