// Package FastsyncV2 implements the JMDN-FastSync V2 synchronization engine.
//
// It orchestrates a multi-phase block synchronization protocol over libp2p:
//
//	Phase 1 — PriorSync:    Compare Merkle roots to identify divergent block ranges.
//	Phase 2 — HeaderSync:   Fetch block headers for all differing ranges (batched, concurrent).
//	Phase 3 — DataSync:     Fetch full block data (transactions, ZK proofs, L1 finality).
//	Phase 4 — Reconcile:    Recompute and commit account balances from synced transactions.
//	Phase 5 — PoTS:         Catch up on blocks produced during phases 1–4 (Point-of-Time-Sync).
//
// The library (github.com/JupiterMetaLabs/JMDN-FastSync) handles the protocol-level
// details (Merkle bisection, concurrent workers, WAL persistence, heartbeat keepalive).
// This package wires it to JMDN's ImmuDB-backed storage via the DB_OPs/Nodeinfo adapter.
package FastsyncV2

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	NodeInfo "gossipnode/DB_OPs/Nodeinfo"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/WAL"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	wal_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types/wal"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/accountsync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/availability"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/datasync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/headersync"
	pots "github.com/JupiterMetaLabs/JMDN-FastSync/core/pots"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/priorsync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/reconsillation"

	"github.com/libp2p/go-libp2p/core/host"
)

const (
	// checksumVersion is the checksum format used by PriorSync to validate block metadata.
	// Must match the version used by the NodeInfo adapter (DB_OPs/Nodeinfo.ChecksumVersion).
	checksumVersion = 2

	// commsVersion identifies this node's communication capabilities.
	// V1 = TCP only, V2 = TCP + QUIC.
	commsVersion = 2

	priorsyncVersion = 2
)

// FastsyncV2 holds the router instances and shared state for the sync engine.
// Create with NewFastsyncV2; trigger sync with HandleSync.
type FastsyncV2 struct {
	Host              host.Host
	NodeInfo          *types.Nodeinfo
	WAL               *WAL.WAL
	PoTSWAL           *WAL.WAL
	PriorRouter       priorsync.Priorsync_router
	HeaderRouter      headersync.Headersync_router
	DataRouter        datasync.DataSync_router
	AvailRouter       availability.Availability_router
	ReconRouter       reconsillation.Reconciliation_router
	PoTSRouter        *pots.PoTS
	AccountSyncRouter accountsync.AccountSync_router

	// blockInfoAdapter is the ThebeDB-backed implementation of types.BlockInfo.
	// Used for local block queries, header/data writes, and account management.
	blockInfoAdapter types.BlockInfo

	// syncTimeout is the maximum wall-clock time for a complete sync operation.
	syncTimeout time.Duration
}

// NewFastsyncV2 initializes the JMDN-FastSync V2 engine over the given libp2p host.
//
// ctx governs the lifetime of server-side network handlers and all router contexts.
// Pass the node's top-level shutdown context so handlers are cancelled on node exit.
// Close() must still be called on shutdown to flush WALs and tear down routers.
//
// It creates the NodeInfo adapter (ImmuDB), initializes both WALs (standard + PoTS),
// creates and configures all protocol routers, and starts the server-side network handlers
// so this node can respond to incoming sync requests from other peers.
func NewFastsyncV2(ctx context.Context, h host.Host, syncTimeout time.Duration) (*FastsyncV2, error) {

	// --- 1. Initialize the BlockInfo adapter (ImmuDB → JMDN-FastSync interface) ---
	blockInfo := NodeInfo.NewSyncStruct()

	nodeinfo := &types.Nodeinfo{
		PeerID:    h.ID(),
		Multiaddr: h.Addrs(),
		Version:   commsVersion,
		BlockInfo: blockInfo,
	}

	// --- 2. Initialize the standard WAL (PriorSync events, HeaderSync batches, DataSync batches) ---
	walDir := wal_types.DefaultDir
	if err := os.MkdirAll(walDir, 0755); err != nil {
		return nil, fmt.Errorf("create WAL directory %s: %w", walDir, err)
	}
	wal, err := WAL.NewWAL(walDir, 1)
	if err != nil {
		return nil, fmt.Errorf("init main WAL: %w", err)
	}

	// --- 3. Initialize the PoTS WAL (buffers live blocks received during sync) ---
	potsWALDir := filepath.Join(wal_types.DefaultDir, "..", "internal", "PoTS")
	if err := os.MkdirAll(potsWALDir, 0755); err != nil {
		return nil, fmt.Errorf("create PoTS WAL directory %s: %w", potsWALDir, err)
	}
	potsWAL, err := WAL.NewWAL(potsWALDir, 100)
	if err != nil {
		return nil, fmt.Errorf("init PoTS WAL: %w", err)
	}

	// --- 4. Create protocol routers ---
	priorRouter := priorsync.NewPriorSyncRouter()
	headerRouter := headersync.NewHeaderSync()
	dataRouter := datasync.NewDataSync()
	availRouter := availability.NewAvailability()
	reconRouter := reconsillation.NewReconciliation()
	potsRouter := pots.NewPoTS()
	accountSyncRouter := accountsync.NewAccountSync()

	// --- 5. Configure routers with shared sync variables ---
	// The first version parameter to SetSyncVars controls transport selection in the
	// Communication layer (V1=TCP-only, V2=TCP+QUIC). Since JMDN nodes listen on both
	// TCP and QUIC, we must use commsVersion (2) so server-side bisection callbacks
	// can reach peers that connected over QUIC.
	// PriorSync takes both comms version AND checksum version (unique among routers).
	priorRouter.SetSyncVars(ctx, priorsyncVersion, checksumVersion, *nodeinfo, h, wal)
	headerRouter.SetSyncVars(ctx, commsVersion, *nodeinfo, h, wal)
	dataRouter.SetSyncVars(ctx, commsVersion, *nodeinfo, h, wal)

	// Availability and Reconciliation share the same SyncVars derived from PriorSync.
	syncVars := priorRouter.GetSyncVars()
	availRouter.SetSyncVarsConfig(ctx, *syncVars)
	reconRouter.SetSyncVarsConfig(ctx, *syncVars)

	// PoTS uses its own isolated WAL for live block buffering.
	// commsVersion (2) enables QUIC transport with TCP fallback, matching the other routers.
	potsRouter.SetSyncVars(ctx, commsVersion, *nodeinfo, h)
	potsRouter.SetWAL(ctx, potsWAL)

	accountSyncRouter.SetSyncVars(ctx, commsVersion, *nodeinfo, h, wal)

	// --- 6. Mark this node as available for sync and start server-side handlers ---
	// IAmAvailable allows other nodes to discover us via Availability requests.
	availability.FastsyncReady().IAmAvailable()

	// SetupNetworkHandlers registers libp2p stream handlers for all sync protocols:
	//   /priorsync/v1, /priorsync/v1/headersync, /priorsync/v1/datasync,
	//   /priorsync/v1/availability, /priorsync/v1/merkle, /priorsync/v1/pots,
	//   /fastsync/v1/pubsub/blocks
	// It blocks until the context is cancelled, so run in a goroutine.
	// The goroutine exits when ctx is cancelled (node shutdown).
	go func() {
		log.Printf("[FastsyncV2] Server handlers started on peer %s", h.ID().String())
		if err := priorRouter.SetupNetworkHandlers(true); err != nil && err != context.Canceled {
			log.Printf("[FastsyncV2] Server handler error: %v", err)
		}
		log.Printf("[FastsyncV2] Server handlers stopped")
	}()

	return &FastsyncV2{
		Host:              h,
		NodeInfo:          nodeinfo,
		WAL:               wal,
		PoTSWAL:           potsWAL,
		PriorRouter:       priorRouter,
		HeaderRouter:      headerRouter,
		DataRouter:        dataRouter,
		AvailRouter:       availRouter,
		ReconRouter:       reconRouter,
		PoTSRouter:        potsRouter,
		AccountSyncRouter: accountSyncRouter,
		blockInfoAdapter:  blockInfo,
		syncTimeout:       syncTimeout,
	}, nil
}
