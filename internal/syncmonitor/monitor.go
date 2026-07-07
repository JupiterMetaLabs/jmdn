// Package syncmonitor runs a background goroutine that periodically computes
// this node's Merkle root and reports it to the seednode. When the seednode
// signals the node is out of sync it triggers block + account reconciliation.
//
// Design goals (public blockchain, 50+ nodes, ~2-min block interval):
//   - Fix 1: startup jitter — spreads nodes across the interval window, no thundering herd
//   - Fix 2: propagation guard — skips checks that race with an in-flight block write
//   - Fix 3: consecutive threshold — fires reconcile only after N out-of-sync reports
//   - Fix 4: block-delta filter — small head gap is propagation lag, not divergence
//   - Fix 5: seednode grace period — short outages don't suppress sync detection
//   - Adaptive interval via time.Timer — backs off when healthy, tightens on divergence
package syncmonitor

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"

	"gossipnode/internal/merkle"
	"gossipnode/seednode"
)

// ─── defaults ─────────────────────────────────────────────────────────────────

const (
	defaultBaseInterval               = 10 * time.Minute
	defaultMinCheckInterval           = 1 * time.Minute
	defaultMaxInterval                = 30 * time.Minute
	defaultBlockPropagationWindow     = 30 * time.Second
	defaultOutOfSyncThreshold         = 2
	defaultPropagationToleranceBlocks = uint64(3)
	defaultSeednodeGracePeriod        = 3
)

// ─── Public types ─────────────────────────────────────────────────────────────

// SyncStatus is the result of a ReportBlockState call.
type SyncStatus struct {
	IsSynced      bool
	SequencerHead uint64
	SequencerRoot []byte
	GoodPeers     []PeerInfo
	Message       string
}

// PeerInfo is a stripped-down peer record sufficient for dialling.
type PeerInfo struct {
	PeerID     string
	Multiaddrs []string
	Region     string
}

// SeedReporter is the interface the Monitor uses to report block state to the
// seednode. Production code uses NewSeednodeReporter; tests inject stubs.
type SeedReporter interface {
	ReportBlockState(ctx context.Context, blockHead uint64, merkleRoot []byte) (*SyncStatus, error)
}

// ReconcileFunc is called when the seednode says this node is out of sync.
type ReconcileFunc func(ctx context.Context, goodPeers []PeerInfo) error

// blockTimer is an optional interface for Fix 2 (propagation guard).
// sync_struct in DB_OPs/Nodeinfo implements this; stubs may omit it.
// If the type-assert fails the guard is silently skipped.
type blockTimer interface {
	LastBlockReceivedAt() time.Time
}

// ─── Seednode adapter ────────────────────────────────────────────────────────

type seednodeAdapter struct {
	client *seednode.Client
	selfID string
}

// NewSeednodeReporter returns a SeedReporter backed by the given seednode Client.
func NewSeednodeReporter(client *seednode.Client, selfPeerID string) SeedReporter {
	return &seednodeAdapter{client: client, selfID: selfPeerID}
}

func (a *seednodeAdapter) ReportBlockState(ctx context.Context, blockHead uint64, merkleRoot []byte) (*SyncStatus, error) {
	st, err := a.client.ReportBlockState(ctx, a.selfID, blockHead, merkleRoot)
	if err != nil {
		return nil, err
	}
	peers := make([]PeerInfo, 0, len(st.GoodPeers))
	for _, p := range st.GoodPeers {
		peers = append(peers, PeerInfo{
			PeerID:     p.PeerID,
			Multiaddrs: p.Multiaddrs,
			Region:     p.Region,
		})
	}
	return &SyncStatus{
		IsSynced:      st.IsSynced,
		SequencerHead: st.SequencerHead,
		SequencerRoot: st.SequencerRoot,
		GoodPeers:     peers,
		Message:       st.Message,
	}, nil
}

// ─── Monitor ─────────────────────────────────────────────────────────────────

// Status is a snapshot of the last sync check — safe to read from any goroutine.
type Status struct {
	LocalHead     uint64    `json:"local_head"`
	MerkleRoot    string    `json:"merkle_root"`
	IsSynced      bool      `json:"is_synced"`
	SequencerHead uint64    `json:"sequencer_head"`
	SequencerRoot string    `json:"sequencer_root"`
	GoodPeers     []string  `json:"good_peer_ids"`
	Message       string    `json:"message"`
	LastCheckedAt time.Time `json:"last_checked_at"`
	Error         string    `json:"error,omitempty"`
	// Fix 3, 5: observable state for ops/alerting
	ConsecutiveOutOfSync int           `json:"consecutive_out_of_sync"`
	SeednodeUnreachable  bool          `json:"seednode_unreachable"`
	CurrentInterval      time.Duration `json:"current_interval"`
}

// Monitor runs the periodic sync-check loop.
type Monitor struct {
	blockInfo    fastsync_types.BlockInfo
	seedClient   SeedReporter
	baseInterval time.Duration

	// propagation / threshold / grace config
	blockPropagationWindow     time.Duration
	outOfSyncThreshold         int
	propagationToleranceBlocks uint64
	seednodeGracePeriod        int

	// mu guards lastStatus.
	mu         sync.RWMutex
	lastStatus Status

	// reconcileMu guards the reconcile func field.
	reconcileMu sync.RWMutex
	reconcile   ReconcileFunc

	// reconciling prevents concurrent reconciliation runs.
	reconciling atomic.Bool

	// started prevents double-start.
	started atomic.Bool

	// rootCtx is the long-lived node context supplied by Start.
	rootCtx context.Context

	// onCheck is an optional test hook called at the end of every runCheck cycle.
	onCheckMu sync.RWMutex
	onCheck   func()

	// runMu serialises runCheck so consecutive-counter state stays coherent
	// even when TriggerCheck and the background timer fire concurrently.
	runMu sync.Mutex

	// Fix 3, 5: mutable check state — only accessed under runMu
	consecutiveOutOfSync      int
	consecutiveSeednodeErrors int

	// Fix 6: adaptive interval — written under runMu, read in Start goroutine
	currentInterval  time.Duration
	immediateRecheck atomic.Bool
}

// New creates a Monitor with production defaults.
//
//	blockInfo     — NodeInfo adapter (call NodeInfo.NewSyncStruct())
//	seedReporter  — SeedReporter (use NewSeednodeReporter or a test stub)
//	checkInterval — base reporting interval; 0 or < 1 min → 10 min default
func New(
	blockInfo fastsync_types.BlockInfo,
	seedReporter SeedReporter,
	checkInterval time.Duration,
) *Monitor {
	if checkInterval < defaultMinCheckInterval {
		checkInterval = defaultBaseInterval
	}
	return &Monitor{
		blockInfo:                  blockInfo,
		seedClient:                 seedReporter,
		baseInterval:               checkInterval,
		currentInterval:            checkInterval,
		blockPropagationWindow:     defaultBlockPropagationWindow,
		outOfSyncThreshold:         defaultOutOfSyncThreshold,
		propagationToleranceBlocks: defaultPropagationToleranceBlocks,
		seednodeGracePeriod:        defaultSeednodeGracePeriod,
		rootCtx:                    context.Background(),
	}
}

// WithOutOfSyncThreshold overrides the consecutive out-of-sync threshold.
// Intended for tests that need threshold=1 to preserve single-TriggerCheck behaviour.
func (m *Monitor) WithOutOfSyncThreshold(n int) *Monitor {
	m.outOfSyncThreshold = n
	return m
}

// SetOnCheck installs a callback invoked at the end of each runCheck cycle.
// Intended for testing only (e.g., TestFix1_StartupJitter).
func (m *Monitor) SetOnCheck(fn func()) {
	m.onCheckMu.Lock()
	m.onCheck = fn
	m.onCheckMu.Unlock()
}

// SetReconcileFunc wires in the reconciliation callback.
func (m *Monitor) SetReconcileFunc(fn ReconcileFunc) {
	m.reconcileMu.Lock()
	m.reconcile = fn
	m.reconcileMu.Unlock()
}

// GetStatus returns a copy of the most recent sync status.
func (m *Monitor) GetStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastStatus
}

// TriggerCheck performs an immediate sync check. Safe to call concurrently.
func (m *Monitor) TriggerCheck(ctx context.Context) Status {
	return m.runCheck(ctx)
}

// Start launches the background loop. Returns error if called twice.
// ctx governs the loop lifetime and is stored as the root context for reconciliation.
func (m *Monitor) Start(ctx context.Context) error {
	if !m.started.CompareAndSwap(false, true) {
		return fmt.Errorf("syncmonitor: Start called twice")
	}

	m.mu.Lock()
	m.rootCtx = ctx
	m.mu.Unlock()

	log.Printf("[syncmonitor] starting — base interval: %v", m.baseInterval)

	go func() {
		// Run first check immediately on startup, then apply jitter before the
		// second check so subsequent nodes joining later don't all fire in lockstep.
		m.runCheck(ctx)

		jitter := time.Duration(rand.Int64N(int64(m.baseInterval)))
		log.Printf("[syncmonitor] post-startup jitter before second check: %v", jitter.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			log.Println("[syncmonitor] stopped after first check")
			return
		case <-time.After(jitter):
		}

		m.runCheck(ctx)

		// Fix 6: use time.Timer (not time.Ticker) so interval can vary per cycle.
		timer := time.NewTimer(m.currentInterval)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("[syncmonitor] stopped")
				return
			case <-timer.C:
				m.runCheck(ctx)
				// After reconciliation, immediateRecheck is set — fire immediately.
				if m.immediateRecheck.CompareAndSwap(true, false) {
					timer.Reset(0)
				} else {
					timer.Reset(m.currentInterval)
				}
			}
		}
	}()

	return nil
}

// runCheck executes one full sync cycle. Serialised by runMu.
func (m *Monitor) runCheck(ctx context.Context) Status {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	// Fix 2: propagation guard — if a block was written recently, skip this cycle.
	// The node may be mid-propagation; reporting a stale root causes a false out-of-sync.
	if bt, ok := m.blockInfo.(blockTimer); ok {
		last := bt.LastBlockReceivedAt()
		if !last.IsZero() {
			if since := time.Since(last); since < m.blockPropagationWindow {
				log.Printf("[syncmonitor] skipping check — block received %v ago (propagation window %v)",
					since.Round(time.Millisecond), m.blockPropagationWindow)
				return m.getStatusLocked()
			}
		}
	}

	start := time.Now()

	// ── 1. Build local Merkle root ────────────────────────────────────────────
	result, err := merkle.BuildLocalMerkleRoot(ctx, m.blockInfo)
	if err != nil {
		log.Printf("[syncmonitor] Merkle build failed: %v", err)
		st := Status{
			Error:           err.Error(),
			LastCheckedAt:   time.Now(),
			CurrentInterval: m.currentInterval,
		}
		m.setStatus(st)
		return st
	}

	// ── 2. Report to seednode ─────────────────────────────────────────────────
	syncSt, err := m.seedClient.ReportBlockState(ctx, result.Head, result.Root[:])
	if err != nil {
		// Fix 5: seednode grace period — short outages keep IsSynced=true;
		// extended unreachability flips SeednodeUnreachable and clears IsSynced.
		m.consecutiveSeednodeErrors++
		unreachable := m.consecutiveSeednodeErrors >= m.seednodeGracePeriod
		if unreachable {
			log.Printf("[syncmonitor] WARN: seednode unreachable for %d consecutive checks — sync status unknown",
				m.consecutiveSeednodeErrors)
		} else {
			log.Printf("[syncmonitor] seednode report failed (%d/%d grace): %v",
				m.consecutiveSeednodeErrors, m.seednodeGracePeriod, err)
		}
		st := Status{
			LocalHead:            result.Head,
			MerkleRoot:           hex.EncodeToString(result.Root[:]),
			IsSynced:             !unreachable,
			SeednodeUnreachable:  unreachable,
			ConsecutiveOutOfSync: m.consecutiveOutOfSync,
			Error:                err.Error(),
			LastCheckedAt:        time.Now(),
			CurrentInterval:      m.currentInterval,
		}
		m.setStatus(st)
		return st
	}

	// Successful contact — reset seednode error counter.
	m.consecutiveSeednodeErrors = 0

	goodPeerIDs := make([]string, 0, len(syncSt.GoodPeers))
	for _, p := range syncSt.GoodPeers {
		goodPeerIDs = append(goodPeerIDs, p.PeerID)
	}

	st := Status{
		LocalHead:       result.Head,
		MerkleRoot:      hex.EncodeToString(result.Root[:]),
		SequencerHead:   syncSt.SequencerHead,
		SequencerRoot:   hex.EncodeToString(syncSt.SequencerRoot),
		GoodPeers:       goodPeerIDs,
		Message:         syncSt.Message,
		LastCheckedAt:   time.Now(),
		CurrentInterval: m.currentInterval,
	}

	if syncSt.IsSynced {
		// ── In-sync path ──────────────────────────────────────────────────────
		m.consecutiveOutOfSync = 0
		m.adjustInterval(true)
		st.IsSynced = true
		st.ConsecutiveOutOfSync = 0
	} else {
		// ── Out-of-sync path ──────────────────────────────────────────────────

		// Fix 4: block-delta filter — a small head gap is almost certainly
		// propagation lag (block in flight), not a genuine divergence.
		seqHead := syncSt.SequencerHead
		if seqHead > result.Head && seqHead-result.Head <= m.propagationToleranceBlocks {
			delta := seqHead - result.Head
			log.Printf("[syncmonitor] propagation lag: local=%d seq=%d delta=%d ≤ %d — not counting as divergence",
				result.Head, seqHead, delta, m.propagationToleranceBlocks)
			st.IsSynced = true
			st.Message = fmt.Sprintf("propagation lag: behind by %d block(s)", delta)
			m.setStatus(st)
			return st
		}

		// Fix 3: consecutive threshold — only reconcile after N out-of-sync reports
		// to absorb single-tick timing artifacts.
		m.consecutiveOutOfSync++
		st.IsSynced = false
		st.ConsecutiveOutOfSync = m.consecutiveOutOfSync
		m.adjustInterval(false)

		log.Printf("[syncmonitor] out of sync (%d/%d consecutive), head=%d seq=%d",
			m.consecutiveOutOfSync, m.outOfSyncThreshold, result.Head, seqHead)

		if m.consecutiveOutOfSync >= m.outOfSyncThreshold {
			m.triggerReconcile(syncSt.GoodPeers)
		}
	}

	m.setStatus(st)
	log.Printf("[syncmonitor] check done in %v — synced=%v head=%d interval=%v",
		time.Since(start), st.IsSynced, st.LocalHead, m.currentInterval)

	m.onCheckMu.RLock()
	hook := m.onCheck
	m.onCheckMu.RUnlock()
	if hook != nil {
		hook()
	}

	return st
}

// adjustInterval updates currentInterval up (in-sync) or down (out-of-sync).
// Must be called under runMu.
func (m *Monitor) adjustInterval(inSync bool) {
	if inSync {
		next := time.Duration(float64(m.currentInterval) * 1.5)
		if next > defaultMaxInterval {
			next = defaultMaxInterval
		}
		m.currentInterval = next
	} else {
		next := m.currentInterval / 2
		if next < defaultMinCheckInterval {
			next = defaultMinCheckInterval
		}
		m.currentInterval = next
	}
}

// triggerReconcile fires ReconcileFunc in a background goroutine if one is registered
// and no reconciliation is already in progress. Must be called under runMu.
func (m *Monitor) triggerReconcile(peers []PeerInfo) {
	m.reconcileMu.RLock()
	reconcileFn := m.reconcile
	m.reconcileMu.RUnlock()

	if reconcileFn == nil {
		log.Println("[syncmonitor] out of sync but no reconcile func registered — skipping")
		return
	}
	if !m.reconciling.CompareAndSwap(false, true) {
		log.Println("[syncmonitor] reconciliation already in progress — skipping")
		return
	}

	m.mu.RLock()
	rootCtx := m.rootCtx
	m.mu.RUnlock()

	captured := peers // capture before goroutine
	go func() {
		var reconcileErr error
		defer func() {
			// Reset counter and interval BEFORE releasing reconciling flag.
			// Without this ordering a concurrent runCheck could observe
			// reconciling=false while consecutiveOutOfSync is still 2, re-triggering
			// a second reconcile before cleanup is complete (race window).
			m.runMu.Lock()
			m.currentInterval = m.baseInterval
			m.consecutiveOutOfSync = 0
			m.runMu.Unlock()
			m.reconciling.Store(false)
			// Only fire immediateRecheck on success.
			// On failure the counter resets to 0 and interval to base; an immediate
			// recheck would find the node still out-of-sync, burn one counter
			// increment, then wait the full base interval (up to 10 min) before the
			// threshold fires again. Skipping immediateRecheck lets the normal timer
			// fire once at baseInterval, giving peers time to stabilise before retry.
			if reconcileErr == nil {
				m.immediateRecheck.Store(true)
			}
		}()
		log.Printf("[syncmonitor] starting reconciliation against %d good peer(s)", len(captured))
		reconcileErr = reconcileFn(rootCtx, captured)
		if reconcileErr != nil {
			log.Printf("[syncmonitor] reconciliation failed: %v", reconcileErr)
		} else {
			log.Println("[syncmonitor] reconciliation complete — re-checking immediately")
		}
	}()
}

func (m *Monitor) setStatus(st Status) {
	m.mu.Lock()
	m.lastStatus = st
	m.mu.Unlock()
}

// getStatusLocked returns lastStatus under read lock.
// Must only be called when runMu is already held (avoids double-locking mu).
func (m *Monitor) getStatusLocked() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastStatus
}
