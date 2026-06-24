// Package syncmonitor runs a background goroutine that periodically computes
// this node's Merkle root and reports it to the seednode. When the seednode
// signals the node is out of sync it triggers block + account reconciliation.
package syncmonitor

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	fastsync_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types"

	"gossipnode/internal/merkle"
	"gossipnode/seednode"
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
// goodPeers is the list of well-synced peers the seednode recommends.
type ReconcileFunc func(ctx context.Context, goodPeers []PeerInfo) error

// ─── Seednode adapter ────────────────────────────────────────────────────────

// seednodeAdapter wraps *seednode.Client and closes over the node's own PeerID,
// satisfying the SeedReporter interface without changing seednode.Client's API.
type seednodeAdapter struct {
	client *seednode.Client
	selfID string
}

// NewSeednodeReporter returns a SeedReporter backed by the given seednode Client.
// selfPeerID is this node's libp2p PeerID string — included in every report.
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
	MerkleRoot    string    `json:"merkle_root"` // hex-encoded
	IsSynced      bool      `json:"is_synced"`
	SequencerHead uint64    `json:"sequencer_head"`
	SequencerRoot string    `json:"sequencer_root"` // hex-encoded
	GoodPeers     []string  `json:"good_peer_ids"`
	Message       string    `json:"message"`
	LastCheckedAt time.Time `json:"last_checked_at"`
	Error         string    `json:"error,omitempty"`
}

// Monitor runs the periodic sync-check loop.
type Monitor struct {
	blockInfo     fastsync_types.BlockInfo
	seedClient    SeedReporter
	checkInterval time.Duration

	// mu guards lastStatus only.
	mu         sync.RWMutex
	lastStatus Status

	// reconcileMu guards the reconcile func field separately from lastStatus,
	// so SetReconcileFunc (write) and runCheck (read) don't share mu with
	// the setStatus path and risk confusion.
	reconcileMu sync.RWMutex
	reconcile   ReconcileFunc // nil until wired via SetReconcileFunc

	// reconciling prevents concurrent reconciliation runs.
	reconciling atomic.Bool

	// started is set on the first Start call to prevent double-start.
	started atomic.Bool

	// rootCtx is the long-lived node context supplied by Start. The reconcile
	// goroutine uses this rather than the caller's ctx, so a short-lived
	// TriggerCheck context cannot cancel an in-progress reconciliation mid-write.
	// Initialised to context.Background() so TriggerCheck works before Start.
	rootCtx context.Context
}

// New creates a Monitor.
//
//	blockInfo      — NodeInfo adapter (call NodeInfo.NewSyncStruct())
//	seedReporter   — SeedReporter (use NewSeednodeReporter or a test stub)
//	checkInterval  — how often to report (e.g. 60 * time.Second); 0 → 60s default
func New(
	blockInfo fastsync_types.BlockInfo,
	seedReporter SeedReporter,
	checkInterval time.Duration,
) *Monitor {
	if checkInterval < time.Minute {
		// Enforce a 1-minute floor: sub-minute intervals thrash the Merkle builder
		// and the seednode gRPC endpoint without operational benefit.
		// Default (0 passed by caller) → 10 minutes.
		checkInterval = 10 * time.Minute
	}
	return &Monitor{
		blockInfo:     blockInfo,
		seedClient:    seedReporter,
		checkInterval: checkInterval,
		rootCtx:       context.Background(), // overwritten by Start
	}
}

// SetReconcileFunc wires in the reconciliation callback. Safe to call
// concurrently; must be called before Start for the callback to be active
// on the first ticker tick.
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

// TriggerCheck performs an immediate sync check and triggers reconciliation if
// needed. Returns the resulting Status. Safe to call concurrently.
// Note: if reconciliation is triggered, it runs in a background goroutine bound
// to the Monitor's root context (not this ctx).
func (m *Monitor) TriggerCheck(ctx context.Context) Status {
	return m.runCheck(ctx)
}

// Start launches the background loop in a goroutine and returns immediately.
// Returns an error if called more than once on the same Monitor.
// The ctx governs the loop lifetime and is stored as the root context for all
// reconciliation goroutines — pass the node's top-level shutdown context.
func (m *Monitor) Start(ctx context.Context) error {
	if !m.started.CompareAndSwap(false, true) {
		return fmt.Errorf("syncmonitor: Start called twice")
	}

	// Store the long-lived node context so reconcile goroutines outlive
	// individual runCheck calls (e.g. those triggered by short-lived HTTP requests).
	m.mu.Lock()
	m.rootCtx = ctx
	m.mu.Unlock()

	log.Println("[syncmonitor] starting — interval:", m.checkInterval)

	go func() {
		// Initial check on startup before the first tick.
		m.runCheck(ctx)

		ticker := time.NewTicker(m.checkInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("[syncmonitor] stopped")
				return
			case <-ticker.C:
				m.runCheck(ctx)
			}
		}
	}()

	return nil
}

// runCheck does one full cycle: build Merkle root → report to seednode → reconcile if needed.
func (m *Monitor) runCheck(ctx context.Context) Status {
	start := time.Now()

	// 1. Build local Merkle root.
	result, err := merkle.BuildLocalMerkleRoot(ctx, m.blockInfo)
	if err != nil {
		log.Printf("[syncmonitor] Merkle build failed: %v", err)
		st := Status{
			Error:         err.Error(),
			LastCheckedAt: time.Now(),
		}
		m.setStatus(st)
		return st
	}

	// 2. Report to seednode.
	syncSt, err := m.seedClient.ReportBlockState(ctx, result.Head, result.Root[:])
	if err != nil {
		// Seednode unreachable: assume synced to avoid spurious reconciliation
		// when the authority node is temporarily down.
		log.Printf("[syncmonitor] seednode report failed (assuming synced): %v", err)
		st := Status{
			LocalHead:     result.Head,
			MerkleRoot:    hex.EncodeToString(result.Root[:]),
			IsSynced:      true,
			Error:         err.Error(),
			LastCheckedAt: time.Now(),
		}
		m.setStatus(st)
		return st
	}

	goodPeerIDs := make([]string, 0, len(syncSt.GoodPeers))
	for _, p := range syncSt.GoodPeers {
		goodPeerIDs = append(goodPeerIDs, p.PeerID)
	}

	st := Status{
		LocalHead:     result.Head,
		MerkleRoot:    hex.EncodeToString(result.Root[:]),
		IsSynced:      syncSt.IsSynced,
		SequencerHead: syncSt.SequencerHead,
		SequencerRoot: hex.EncodeToString(syncSt.SequencerRoot),
		GoodPeers:     goodPeerIDs,
		Message:       syncSt.Message,
		LastCheckedAt: time.Now(),
	}
	m.setStatus(st)

	log.Printf("[syncmonitor] check done in %v — synced=%v head=%d", time.Since(start), st.IsSynced, st.LocalHead)

	// 3. Trigger reconciliation if out of sync.
	if !syncSt.IsSynced && len(syncSt.GoodPeers) > 0 {
		m.reconcileMu.RLock()
		reconcileFn := m.reconcile
		m.reconcileMu.RUnlock()

		if reconcileFn == nil {
			log.Println("[syncmonitor] out of sync but no reconcile func registered — skipping")
		} else if m.reconciling.CompareAndSwap(false, true) {
			// Snapshot rootCtx under lock so we get the value set by Start
			// rather than the potentially short-lived caller ctx.
			m.mu.RLock()
			rootCtx := m.rootCtx
			m.mu.RUnlock()

			peers := syncSt.GoodPeers // capture before goroutine
			go func() {
				defer m.reconciling.Store(false)
				log.Printf("[syncmonitor] starting reconciliation against %d good peer(s)", len(peers))
				if err := reconcileFn(rootCtx, peers); err != nil {
					log.Printf("[syncmonitor] reconciliation failed: %v", err)
				} else {
					// Do NOT call runCheck recursively here: that can cascade
					// goroutines if the node remains out of sync. The ticker
					// loop re-checks on the next interval automatically.
					log.Println("[syncmonitor] reconciliation complete — ticker will re-check")
				}
			}()
		} else {
			log.Println("[syncmonitor] reconciliation already in progress — skipping")
		}
	}

	return st
}

func (m *Monitor) setStatus(st Status) {
	m.mu.Lock()
	m.lastStatus = st
	m.mu.Unlock()
}
