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
	"math"
	"os"
	"path/filepath"
	"time"

	NodeInfo "gossipnode/DB_OPs/Nodeinfo"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/WAL"
	availabilitypb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/availability"
	blockpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/block"
	headersyncpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/headersync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	wal_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types/wal"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/availability"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/datasync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/headersync"
	pots "github.com/JupiterMetaLabs/JMDN-FastSync/core/pots"
	potsrequesthelper "github.com/JupiterMetaLabs/JMDN-FastSync/core/pots/helper"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/priorsync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/reconsillation"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const (
	// checksumVersion is the checksum format used by PriorSync to validate block metadata.
	// Must match the version used by the NodeInfo adapter (DB_OPs/Nodeinfo.ChecksumVersion).
	checksumVersion = 2

	// commsVersion identifies this node's communication capabilities.
	// V1 = TCP only, V2 = TCP + QUIC.
	commsVersion = 2

	priorsyncVersion = 2

	// syncTimeout is the maximum wall-clock time for a complete sync operation.
	syncTimeout = 15 * time.Minute
)

// FastsyncV2 holds the router instances and shared state for the sync engine.
// Create with NewFastsyncV2; trigger sync with HandleSync.
type FastsyncV2 struct {
	Host         host.Host
	NodeInfo     *types.Nodeinfo
	WAL          *WAL.WAL
	PoTSWAL      *WAL.WAL
	PriorRouter  priorsync.Priorsync_router
	HeaderRouter headersync.Headersync_router
	DataRouter   datasync.DataSync_router
	AvailRouter  availability.Availability_router
	ReconRouter  reconsillation.Reconciliation_router
	PoTSRouter   *pots.PoTS

	// blockInfoAdapter is the ImmuDB-backed implementation of types.BlockInfo.
	// Used for local block queries, header/data writes, and account management.
	blockInfoAdapter types.BlockInfo
}

// NewFastsyncV2 initializes the JMDN-FastSync V2 engine over the given libp2p host.
//
// It creates the NodeInfo adapter (ImmuDB), initializes both WALs (standard + PoTS),
// creates and configures all protocol routers, and starts the server-side network handlers
// so this node can respond to incoming sync requests from other peers.
func NewFastsyncV2(h host.Host) (*FastsyncV2, error) {
	ctx := context.Background()

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

	// --- 6. Mark this node as available for sync and start server-side handlers ---
	// IAmAvailable allows other nodes to discover us via Availability requests.
	availability.FastsyncReady().IAmAvailable()

	// SetupNetworkHandlers registers libp2p stream handlers for all sync protocols:
	//   /priorsync/v1, /priorsync/v1/headersync, /priorsync/v1/datasync,
	//   /priorsync/v1/availability, /priorsync/v1/merkle, /priorsync/v1/pots,
	//   /fastsync/v1/pubsub/blocks
	// It blocks until the context is cancelled, so run in a goroutine.
	go func() {
		log.Printf("[FastsyncV2] Server handlers started on peer %s", h.ID().String())
		if err := priorRouter.SetupNetworkHandlers(true); err != nil && err != context.Canceled {
			log.Printf("[FastsyncV2] Server handler error: %v", err)
		}
	}()

	return &FastsyncV2{
		Host:             h,
		NodeInfo:         nodeinfo,
		WAL:              wal,
		PoTSWAL:          potsWAL,
		PriorRouter:      priorRouter,
		HeaderRouter:     headerRouter,
		DataRouter:       dataRouter,
		AvailRouter:      availRouter,
		ReconRouter:      reconRouter,
		PoTSRouter:       potsRouter,
		blockInfoAdapter: blockInfo,
	}, nil
}

// HandleSync executes the full FastSync protocol with the target peer.
//
// The target peer must be a valid libp2p multiaddress with an embedded peer ID,
// e.g. "/ip4/192.168.1.5/tcp/15000/p2p/12D3KooW...".
//
// The sync flow is:
//  1. Connect to peer and verify availability (get auth UUID).
//  2. PriorSync — compare Merkle roots; exit early if databases match.
//  3. HeaderSync — fetch block headers for all differing ranges.
//  4. DataSync — fetch full block data (transactions, ZK proofs).
//  5. Reconciliation — recompute and commit account balances.
//  6. PoTS — catch up on blocks produced during steps 2–5.
func (fs *FastsyncV2) HandleSync(targetPeer string) error {
	return fs.handleSyncInternal(targetPeer, 0)
}

// HandleStartupSync syncs from an already-connected peer, starting from the local
// latest block number. This is used on node startup/restart to catch up on blocks
// missed while offline, without re-syncing the entire chain.
func (fs *FastsyncV2) HandleStartupSync(peerID peer.ID, addrs []multiaddr.Multiaddr) error {
	if len(addrs) == 0 {
		return fmt.Errorf("no addresses for peer %s", peerID)
	}

	// Build the full multiaddr string with embedded peer ID (required by handleSyncInternal)
	targetMultiaddr := fmt.Sprintf("%s/p2p/%s", addrs[0].String(), peerID.String())

	// Start from the local latest block so we only sync missing blocks
	localBlockNum := fs.blockInfoAdapter.GetBlockDetails().Blocknumber
	startBlock := localBlockNum
	if startBlock == 0 {
		// Fresh node with no blocks — do a full sync
		log.Printf("[FastsyncV2] StartupSync: fresh node (block 0), performing full sync")
	} else {
		log.Printf("[FastsyncV2] StartupSync: resuming from block %d", startBlock)
	}

	return fs.handleSyncInternal(targetMultiaddr, startBlock)
}

// handleSyncInternal is the core sync engine. startBlock controls where PriorSync
// begins comparing: 0 for a full sync, or localBlockNum for incremental startup sync.
func (fs *FastsyncV2) handleSyncInternal(targetPeer string, startBlock uint64) error {
	syncStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()

	// --- Parse and connect to the target peer ---
	maddr, err := multiaddr.NewMultiaddr(targetPeer)
	if err != nil {
		return fmt.Errorf("invalid multiaddr %q: %w", targetPeer, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("extract peer info from multiaddr: %w", err)
	}

	if err := fs.Host.Connect(ctx, *info); err != nil {
		return fmt.Errorf("connect to peer %s: %w", info.ID, err)
	}
	log.Printf("[FastsyncV2] Connected to peer %s", info.ID)

	// After connecting, fetch all addresses the peer advertises from the peerstore.
	// info.Addrs only contains the single address from the user-supplied multiaddr,
	// which may be QUIC-only. PoTS V1 requires TCP; the peerstore will have both.
	peerAddrs := fs.Host.Peerstore().Addrs(info.ID)
	if len(peerAddrs) == 0 {
		peerAddrs = info.Addrs
	}

	// Construct the target's NodeInfo for all subsequent protocol calls.
	// BlockInfo is nil because we don't need to query the remote's DB locally — the
	// routers communicate with the remote via libp2p streams.
	targetNodeInfo := &types.Nodeinfo{
		PeerID:    info.ID,
		Multiaddr: peerAddrs,
		Version:   commsVersion,
	}

	// =========================================================================
	// PHASE 1: Availability — verify the remote is running FastSync and get auth
	// =========================================================================
	log.Printf("[FastsyncV2] Phase 1: Checking availability of peer %s", info.ID)

	availResp, err := fs.AvailRouter.SendAvailabilityRequest(
		ctx, fs.PriorRouter.GetSyncVars(), *targetNodeInfo, startBlock, math.MaxUint64,
	)
	if err != nil {
		return fmt.Errorf("availability request failed: %w", err)
	}
	if !availResp.IsAvailable {
		return fmt.Errorf("peer %s reports unavailable for FastSync", info.ID)
	}
	if availResp.Auth == nil || availResp.Auth.UUID == "" {
		return fmt.Errorf("peer %s returned no auth token", info.ID)
	}
	log.Printf("[FastsyncV2] Phase 1 complete: authorized (UUID=%s)", availResp.Auth.UUID)

	// =========================================================================
	// PHASE 2: PriorSync — identify divergent block ranges via Merkle comparison
	// =========================================================================
	localBlockNum := fs.blockInfoAdapter.GetBlockDetails().Blocknumber
	log.Printf("[FastsyncV2] Phase 2: PriorSync (local latest block: %d, start: %d)", localBlockNum, startBlock)

	// Compare [startBlock, localBlockNum] locally vs [startBlock, MaxUint64] on remote.
	// startBlock=0 → full sync (compare entire chain)
	// startBlock=N → incremental sync (only compare from block N onward)
	resp, err := fs.PriorRouter.PriorSync(
		startBlock, localBlockNum, startBlock, math.MaxUint64, targetNodeInfo, availResp.Auth,
	)
	if err != nil {
		return fmt.Errorf("priorsync failed: %w", err)
	}

	// If the remote returned no tag, the Merkle roots match — databases are identical.
	if resp.Headersync == nil || resp.Headersync.Tag == nil {
		log.Println("[FastsyncV2] Phase 2 complete: checksums match, databases in sync.")
		return nil
	}
	log.Printf("[FastsyncV2] Phase 2 complete: divergence detected, proceeding to HeaderSync")

	// Wrap the availability response for routers that accept multiple remotes.
	// In our case we sync from a single peer, but the API supports multi-peer failover.
	remotes := []*availabilitypb.AvailabilityResponse{availResp}

	// =========================================================================
	// PHASE 3: HeaderSync — fetch block headers for divergent ranges
	// =========================================================================
	// The library batches the tag into chunks of MAX_HEADERS_PER_REQUEST (1500),
	// fetches them with 3 concurrent workers, writes each batch to WAL first
	// (crash recovery), then to DB via HeadersWriter. After all batches,
	// SyncConfirmation re-compares Merkle trees to verify convergence (up to 4 rounds).
	log.Println("[FastsyncV2] Phase 3: HeaderSync")

	datasyncReq, err := fs.HeaderRouter.HeaderSync(resp.Headersync, remotes, true)
	if err != nil {
		return fmt.Errorf("headersync failed: %w", err)
	}
	if datasyncReq == nil {
		log.Println("[FastsyncV2] Phase 3 complete: HeaderSync returned no DataSync request (all synced at header level)")
		return nil
	}
	log.Println("[FastsyncV2] Phase 3 complete: headers synchronized")

	// =========================================================================
	// PHASE 4: DataSync — fetch full block data (transactions, ZK proofs, L1 finality)
	// =========================================================================
	// The library batches the tag into chunks of MAX_DATA_PER_REQUEST (30 blocks),
	// fetches with 3 concurrent workers (out-of-order collection, in-order DB write),
	// writes each batch to WAL first, then to DB via DataWriter.
	// Returns TaggedAccounts — the set of accounts affected by synced transactions.
	log.Println("[FastsyncV2] Phase 4: DataSync")

	taggedAccounts, err := fs.DataRouter.DataSync(datasyncReq, remotes)
	if err != nil {
		return fmt.Errorf("datasync failed: %w", err)
	}
	log.Println("[FastsyncV2] Phase 4 complete: block data synchronized")

	// =========================================================================
	// PHASE 5: Reconciliation — recompute and commit account balances
	// =========================================================================
	// Three-phase atomic operation:
	//   1. Concurrent balance computation (up to 15 goroutines replay transactions)
	//   2. WAL batch write (single ReconciliationBatchEvent for crash recovery)
	//   3. Atomic DB commit via AccountManager.BatchUpdateAccounts
	log.Println("[FastsyncV2] Phase 5: Reconciliation")

	reconciledCount, failedAccounts, err := fs.ReconRouter.Reconcile(taggedAccounts)
	if err != nil {
		log.Printf("[FastsyncV2] Phase 5 warning: reconciliation returned error: %v", err)
	}
	log.Printf("[FastsyncV2] Phase 5 complete: %d accounts reconciled, %d failed", reconciledCount, len(failedAccounts))
	if len(failedAccounts) > 0 {
		log.Printf("[FastsyncV2] Failed accounts: %v", failedAccounts)
	}

	// =========================================================================
	// PHASE 6: PoTS — catch up on blocks produced during phases 2–5
	// =========================================================================
	// While we were syncing, the network may have produced new blocks. PoTS
	// identifies and fetches any blocks we missed by comparing:
	//   - Blocks already in our PoTS WAL (received via pubsub subscriber, if running)
	//   - Our latest synced block number
	// Against the remote's current state. Missing blocks go through a secondary
	// HeaderSync → DataSync → Reconciliation pass.
	log.Println("[FastsyncV2] Phase 6: PoTS (Point-of-Time-Sync)")

	if err := fs.executePoTS(ctx, targetNodeInfo, remotes, availResp); err != nil {
		log.Printf("[FastsyncV2] Phase 6 warning: PoTS failed: %v", err)
		// PoTS failure is non-fatal — the node is mostly synced. The next sync
		// round or normal block propagation will catch the remaining blocks.
	} else {
		log.Println("[FastsyncV2] Phase 6 complete: PoTS synchronized")
	}

	elapsed := time.Since(syncStart).Round(time.Millisecond)
	log.Printf("[FastsyncV2] Sync complete in %s", elapsed)
	return nil
}

// executePoTS runs the Point-of-Time-Sync phase: finds blocks produced during
// the main sync window and runs a secondary sync pass for any that are missing.
func (fs *FastsyncV2) executePoTS(
	ctx context.Context,
	targetNodeInfo *types.Nodeinfo,
	remotes []*availabilitypb.AvailabilityResponse,
	availResp *availabilitypb.AvailabilityResponse,
) error {
	// Read any blocks that were buffered in the PoTS WAL during the main sync.
	// If no pubsub subscriber was running, this may be empty — that's fine,
	// the PoTS request will ask the remote for everything.
	potsWALIface, err := fs.PoTSRouter.GetWAL()
	if err != nil {
		return fmt.Errorf("access PoTS WAL: %w", err)
	}

	walCount, err := potsWALIface.Count(ctx)
	if err != nil {
		return fmt.Errorf("count PoTS WAL entries: %w", err)
	}

	// Build a map of {blockNumber → blockHash} for blocks we already have in the PoTS WAL.
	potsBlocksMap := make(map[uint64][]byte)
	if walCount > 0 {
		walBlocks, err := potsWALIface.Read(ctx, 0, int(walCount))
		if err != nil {
			return fmt.Errorf("read PoTS WAL: %w", err)
		}
		for _, blk := range walBlocks {
			potsBlocksMap[blk.BlockNumber] = blk.BlockHash.Bytes()
		}
		log.Printf("[FastsyncV2] PoTS WAL contains %d buffered blocks", len(potsBlocksMap))
	}

	// Ask the remote what blocks we're missing relative to our latest synced state.
	latestSyncedBlock := fs.blockInfoAdapter.GetBlockDetails().Blocknumber

	potsReq := potsrequesthelper.NewPoTSRequestBuilder().
		AddMap(potsBlocksMap).
		AddLatestBlock(latestSyncedBlock).
		AddAuth(availResp.Auth).
		Build()

	potsResp, err := fs.PoTSRouter.SendPoTSRequest(ctx, potsReq, *targetNodeInfo)
	if err != nil {
		return fmt.Errorf("PoTS request: %w", err)
	}

	// If the remote identified missing blocks, run a secondary sync pass.
	if potsResp.Tag != nil && (len(potsResp.Tag.Range) > 0 || len(potsResp.Tag.BlockNumber) > 0) {
		log.Println("[FastsyncV2] PoTS: missing blocks detected, running secondary sync pass")

		// Secondary HeaderSync — syncConfirmation=false because the remote already
		// identified exact blocks (no need for Merkle re-comparison).
		potsDatasyncReq, err := fs.HeaderRouter.HeaderSync(
			&headersyncpb.HeaderSyncRequest{Tag: potsResp.Tag}, remotes, false,
		)
		if err != nil {
			return fmt.Errorf("PoTS headersync: %w", err)
		}

		if potsDatasyncReq != nil {
			// Secondary DataSync for the PoTS blocks.
			potsTaggedAccts, err := fs.DataRouter.DataSync(potsDatasyncReq, remotes)
			if err != nil {
				return fmt.Errorf("PoTS datasync: %w", err)
			}

			// Secondary Reconciliation for accounts affected by PoTS blocks.
			if potsTaggedAccts != nil {
				reconCount, failed, err := fs.ReconRouter.Reconcile(potsTaggedAccts)
				if err != nil {
					log.Printf("[FastsyncV2] PoTS reconciliation warning: %v", err)
				}
				log.Printf("[FastsyncV2] PoTS reconciled %d accounts, %d failed", reconCount, len(failed))
			}
		}
	} else {
		log.Println("[FastsyncV2] PoTS: no missing blocks, fully caught up")
	}

	// Persist any blocks still in the PoTS WAL to the main database.
	// These are blocks received via pubsub during the sync window that haven't
	// been written through the normal HeaderSync/DataSync path.
	if walCount > 0 {
		if err := fs.dumpPoTSWALToDB(ctx); err != nil {
			log.Printf("[FastsyncV2] PoTS WAL dump warning: %v", err)
		}
	}

	return nil
}

// dumpPoTSWALToDB reads all blocks from the PoTS WAL and writes them to the main
// database via the HeadersWriter and DataWriter adapters. Blocks that already exist
// in the DB (from the normal sync path) are silently skipped by StoreZKBlock.
func (fs *FastsyncV2) dumpPoTSWALToDB(ctx context.Context) error {
	potsWALIface, err := fs.PoTSRouter.GetWAL()
	if err != nil {
		return fmt.Errorf("access PoTS WAL: %w", err)
	}

	walCount, err := potsWALIface.Count(ctx)
	if err != nil || walCount == 0 {
		return err
	}

	walBlocks, err := potsWALIface.Read(ctx, 0, int(walCount))
	if err != nil {
		return fmt.Errorf("read PoTS WAL for dump: %w", err)
	}

	log.Printf("[FastsyncV2] Dumping %d PoTS WAL blocks to main DB", len(walBlocks))

	headersWriter := fs.blockInfoAdapter.NewHeadersWriter()
	dataWriter := fs.blockInfoAdapter.NewDataWriter()

	for _, blk := range walBlocks {
		// Convert types.ZKBlock → proto Header for the header portion.
		header := zkBlockToProtoHeader(blk)
		if err := headersWriter.WriteHeaders([]*blockpb.Header{header}); err != nil {
			// Block may already exist from the normal sync path — log and continue.
			log.Printf("[FastsyncV2] PoTS dump: skip block %d header (may exist): %v", blk.BlockNumber, err)
			continue
		}

		// Convert types.ZKBlock → proto NonHeaders for the data portion (transactions, ZK proofs).
		nonHeaders := zkBlockToProtoNonHeaders(blk)
		if err := dataWriter.WriteData([]*blockpb.NonHeaders{nonHeaders}); err != nil {
			log.Printf("[FastsyncV2] PoTS dump: skip block %d data: %v", blk.BlockNumber, err)
		}
	}

	return nil
}

// Close tears down all routers and flushes WALs.
// Call this when the node shuts down.
func (fs *FastsyncV2) Close() {
	if fs.PriorRouter != nil {
		fs.PriorRouter.Close()
	}
	if fs.HeaderRouter != nil {
		fs.HeaderRouter.Close()
	}
	if fs.DataRouter != nil {
		fs.DataRouter.Close()
	}
	if fs.AvailRouter != nil {
		fs.AvailRouter.Close()
	}
	if fs.ReconRouter != nil {
		fs.ReconRouter.Close()
	}
	if fs.PoTSRouter != nil {
		fs.PoTSRouter.Close()
	}
	if fs.WAL != nil {
		fs.WAL.Close()
	}
	if fs.PoTSWAL != nil {
		fs.PoTSWAL.Close()
	}
}

// ---------------------------------------------------------------------------
// Type conversion helpers: types.ZKBlock → proto types
//
// These convert JMDN-FastSync's types.ZKBlock into the protobuf types expected
// by the HeadersWriter and DataWriter adapters, enabling PoTS WAL blocks to be
// persisted through the same path as normal sync blocks.
// ---------------------------------------------------------------------------

// zkBlockToProtoHeader extracts the header fields from a types.ZKBlock.
func zkBlockToProtoHeader(b *types.ZKBlock) *blockpb.Header {
	h := &blockpb.Header{
		ProofHash:   b.ProofHash,
		Status:      b.Status,
		TxnsRoot:    b.TxnsRoot,
		Timestamp:   b.Timestamp,
		ExtraData:   b.ExtraData,
		StateRoot:   b.StateRoot[:],
		BlockHash:   b.BlockHash[:],
		PrevHash:    b.PrevHash[:],
		GasLimit:    b.GasLimit,
		GasUsed:     b.GasUsed,
		BlockNumber: b.BlockNumber,
	}
	if b.CoinbaseAddr != nil {
		h.CoinbaseAddr = b.CoinbaseAddr[:]
	}
	if b.ZKVMAddr != nil {
		h.ZkvmAddr = b.ZKVMAddr[:]
	}
	return h
}

// zkBlockToProtoNonHeaders extracts the non-header fields (transactions, ZK proofs)
// from a types.ZKBlock into a blockpb.NonHeaders for persistence via DataWriter.
func zkBlockToProtoNonHeaders(b *types.ZKBlock) *blockpb.NonHeaders {
	nh := &blockpb.NonHeaders{
		BlockNumber: b.BlockNumber,
		Snapshot: &blockpb.SnapshotRecord{
			BlockHash: b.BlockHash[:],
			CreatedAt: b.Timestamp,
		},
	}

	if b.ProofHash != "" {
		nh.ZkProof = &blockpb.ZKProof{
			ProofHash:  b.ProofHash,
			StarkProof: b.StarkProof,
			Commitment: commitmentToBytes(b.Commitment),
		}
	}

	for idx, tx := range b.Transactions {
		pbTx := &blockpb.Transaction{
			Hash:     tx.Hash[:],
			Type:     uint32(tx.Type),
			Nonce:    tx.Nonce,
			GasLimit: tx.GasLimit,
			Data:     tx.Data,
		}
		if tx.From != nil {
			pbTx.From = tx.From[:]
		}
		if tx.To != nil {
			pbTx.To = tx.To[:]
		}
		if tx.Value != nil {
			pbTx.Value = tx.Value.Bytes()
		}
		if tx.GasPrice != nil {
			pbTx.GasPrice = tx.GasPrice.Bytes()
		}
		if tx.MaxFee != nil {
			pbTx.MaxFee = tx.MaxFee.Bytes()
		}
		if tx.MaxPriorityFee != nil {
			pbTx.MaxPriorityFee = tx.MaxPriorityFee.Bytes()
		}
		if tx.V != nil {
			pbTx.V = tx.V.Bytes()
		}
		if tx.R != nil {
			pbTx.R = tx.R.Bytes()
		}
		if tx.S != nil {
			pbTx.S = tx.S.Bytes()
		}

		nh.Transactions = append(nh.Transactions, &blockpb.DBTransaction{
			Tx:        pbTx,
			TxIndex:   uint32(idx),
			CreatedAt: b.Timestamp,
		})
	}

	return nh
}

// commitmentToBytes encodes a []uint32 commitment to raw bytes (4 bytes per element, little-endian).
// This matches the block_nonheader.proto ZKProof.commitment field (bytes).
func commitmentToBytes(c []uint32) []byte {
	if len(c) == 0 {
		return nil
	}
	buf := make([]byte, len(c)*4)
	for i, v := range c {
		buf[i*4+0] = byte(v)
		buf[i*4+1] = byte(v >> 8)
		buf[i*4+2] = byte(v >> 16)
		buf[i*4+3] = byte(v >> 24)
	}
	return buf
}
